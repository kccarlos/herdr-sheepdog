package gptlive

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// tickLoop does the session's housekeeping every Tick: the silence
// keep-alive, the synthesised end of an input utterance and of the
// model's speech, the idle close and the push queue.
func (s *Session) tickLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.tick()
	}
}

func (s *Session) tick() {
	now := s.now()
	s.keepAlive(now)
	s.settle(now)
	s.speechEnd(now)
	s.idleCheck(now)
	s.pumpPushes(now)
}

// keepAlive sends silence while the client sends nothing (silence gate
// closed, muted, no client): the Live session timeline advances with
// input audio, and appended context or commentary waits for it
// (docs: "If the session timeline stops, the acknowledgment can remain
// pending"). It pays the input's debt against the wall clock (pace.go)
// so the timeline runs at real time, not at the tick's mercy; debt over
// paceMaxBurst is forgiven and logged as voice.input_debt_dropped.
func (s *Session) keepAlive(now time.Time) {
	s.mu.Lock()
	lc := s.live
	s.mu.Unlock()
	if lc == nil {
		return
	}
	n, dropped := lc.pace.owe(now, paceClientTicks*s.cfg.Tick)
	if dropped > 0 {
		s.log.Log(logSource, "voice.input_debt_dropped", "", map[string]any{"ms": dropped.Milliseconds(), "session_id": lc.id})
	}
	if n <= 0 {
		return
	}
	_ = s.sendAudio(lc, make([]byte, n*2), srcSilence)
}

// settle ends the input utterance SettleWait after its last fragment:
// EventTranscript{DirIn, "", Final} (the supervisor's input and the
// brain's transcript close on it), the recap gets the utterance, and a
// bare stop word nobody delegated runs stop.
func (s *Session) settle(now time.Time) {
	s.mu.Lock()
	if !s.inputOpen || now.Sub(s.lastInput) < s.cfg.SettleWait {
		s.mu.Unlock()
		return
	}
	s.inputOpen = false
	s.addRecapLocked("user", s.inText)
	s.inText = ""
	var stop *voice.ToolCall
	if s.unclaimed != "" && s.cfg.Classify != nil {
		if call, ok := s.cfg.Classify(s.unclaimed); ok && call.Name == voice.ToolStop {
			// The stop takes the words, and what settled before them: a
			// later delegation must not relay them after all.
			s.unclaimed, s.settledText = "", ""
			stop = &call
		}
	}
	if s.unclaimed != "" {
		// Not claimed (yet): a delegation that follows shortly takes it;
		// otherwise the model answered on its own and the words must not
		// be glued to the next utterance. A pause mid-sentence settles
		// the first half, and the second half is added to it rather than
		// replacing it — replacing it relayed only the tail of the
		// sentence ("sometimes", 2026-09-23 23:53:26).
		if s.settledText != "" && now.Sub(s.settledAt) < settledKeep {
			s.settledText = strings.TrimSpace(s.settledText) + " " + strings.TrimSpace(s.unclaimed)
		} else {
			s.settledText = s.unclaimed
		}
		s.settledAt = now
		s.unclaimed = ""
	}
	owed := s.turnDoneOwed
	s.turnDoneOwed = false
	s.mu.Unlock()
	if owed {
		s.emit(voice.EventTurnComplete{})
	}
	s.rec.Heard("", true)
	s.emit(voice.EventTranscript{Dir: voice.DirIn, Final: true})
	if stop != nil {
		s.interrupt("stop")
		s.startTool(nil, "", *stop)
	}
}

// speechEnd emits EventTurnComplete when the model stops speaking.
func (s *Session) speechEnd(now time.Time) {
	s.mu.Lock()
	if !s.speakingNow || s.speakingLocked(now) {
		s.mu.Unlock()
		return
	}
	s.speakingNow = false
	s.flushReplyLocked()
	timing := s.finishAudioTimingLocked()
	owed := s.inputOpen
	if owed {
		// The shepherd is talking: closing the turn now would close their
		// input mid-utterance; settle emits it.
		s.turnDoneOwed = true
	}
	s.mu.Unlock()
	if timing != nil {
		s.log.Log(logSource, "voice.audio_timing", "", timing)
	}
	s.rec.ModelDone()
	if owed {
		return
	}
	s.emit(voice.EventTurnComplete{})
}

// idleCheck closes a live session nothing has used for IdleClose: no
// transcript either way, no voiced model audio, no delegation or push in
// flight. Closing disarms the microphone trigger so the same room noise
// does not reopen it at once.
func (s *Session) idleCheck(now time.Time) {
	s.mu.Lock()
	lc := s.live
	// Loud frames right now may be the start of a sentence whose first
	// transcript has not arrived yet (seen live: the idle close fired
	// 0.7 s into an utterance and lost it). Only a run of loud frames
	// long past any sentence counts as room noise and does not hold the
	// session open.
	talking := !s.lastLoud.IsZero() && now.Sub(s.lastLoud) < 1500*time.Millisecond && now.Sub(s.loudSince) < s.cfg.NoiseRun
	if s.muted {
		// A muted client sends no frames: whatever it said before the
		// mute is not a sentence in progress, so a muted shepherd with
		// nothing playing and no push pending is idle (a delegation in
		// flight still holds the session for its answer).
		talking = false
	}
	// A push held for want of a listener (pumpPushes) does not hold a
	// billed session open: nobody is there to hear it, and it opens a
	// fresh one when a client attaches.
	pushWaiting := len(s.pushQ) > 0 && s.client
	if lc == nil || len(s.inflight) > 0 || pushWaiting || (s.inputOpen && !s.muted) || talking || s.speakingLocked(now) ||
		now.Sub(s.lastActivity) < s.cfg.IdleClose {
		s.mu.Unlock()
		return
	}
	// Disarm only while loud frames keep coming (a noisy room would
	// reopen the session at once); a closed gate stays armed for the
	// next utterance.
	s.armed = s.lastLoud.IsZero() || now.Sub(s.lastLoud) > time.Second
	s.quietSince = time.Time{}
	s.mu.Unlock()
	s.closeLive(lc, "idle")
}

// exempt reports whether tags keep an INTERRUPT push at INTERRUPT while
// the model is speaking (docs/roles.md "Pushes").
func exempt(tags map[string]any) bool {
	return tags["due_promise"] == true || tags["needs_user"] == true || tags["stop"] == true
}

// pumpPushes delivers at most one queued push per tick, chosen by the
// arbitration in voice/pushclass.go: the first ready line the shepherd
// is owed or must hear now, else the oldest other line — not merely the
// head of the queue (docs/timelines.md C29). A spoken push waits (up to
// PushTTL) while no client is attached, whether a session is open or
// not — the model must not speak it to nobody (2026-09-24 18:22:04) —
// logging voice.push_held once and voice.push_released when it goes
// out. In standby a spoken push opens a session when a client is
// listening, and silent context is kept for the next session's history.
func (s *Session) pumpPushes(now time.Time) {
	s.mu.Lock()
	p := s.pickPushLocked(now)
	lc := s.live
	s.mu.Unlock()
	for i := 0; i < p.deferred; i++ {
		s.log.Log(logSource, "voice.push_deferred", "", map[string]any{"kind": "push", "reason": "speaking", "urgency_requested": voice.UrgencyInterrupt.String()})
	}
	for _, it := range p.dropped {
		d := heldPayload(it)
		d["reason"], d["age_ms"] = "no_listener", now.Sub(it.at).Milliseconds()
		s.log.Log(logSource, "voice.push_dropped", "", d)
	}
	for _, it := range p.held {
		h := heldPayload(it)
		h["reason"] = "no_listener"
		s.log.Log(logSource, "voice.push_held", "", h)
	}
	if !p.ok || lc == nil {
		return
	}
	if !p.item.heldAt.IsZero() {
		r := heldPayload(p.item)
		r["held_ms"] = now.Sub(p.item.heldAt).Milliseconds()
		s.log.Log(logSource, "voice.push_released", "", r)
	}
	if len(p.over) > 0 {
		s.log.Log(logSource, "voice.push_overtook", "", overtookPayload(p.item, p.over))
	}
	s.deliverPush(lc, p.item)
}

// pushPick is pickPushLocked's verdict and what it has to log.
type pushPick struct {
	ok   bool
	item pushItem
	// over are the items the picked one overtook (voice.push_overtook).
	over []pushItem
	// deferred, held and dropped are items downgraded to WHEN_IDLE while
	// the model speaks, first held for want of a listener, and dropped
	// after PushTTL.
	deferred int
	held     []pushItem
	dropped  []pushItem
}

// pickPushLocked chooses the push to send now and removes it from the
// queue: among the lines the shepherd is owed or must hear now (class
// 0, voice/pushclass.go), the first ready one; failing that, the oldest
// other line, if it is ready; never one that would pass an earlier line
// answering the same utterance. Caller holds s.mu.
func (s *Session) pickPushLocked(now time.Time) pushPick {
	var p pushPick
	lc := s.live
	kept := s.pushQ[:0]
	for _, it := range s.pushQ {
		switch {
		case it.urgency != voice.UrgencySilent && (!s.client || lc == nil) && now.Sub(it.at) > s.cfg.PushTTL:
			p.dropped = append(p.dropped, it)
		case it.urgency == voice.UrgencySilent && lc == nil:
			// Standby: silent context waits for the next session's
			// history.
			s.held = append(s.held, it.text)
			if len(s.held) > 10 {
				s.held = s.held[len(s.held)-10:]
			}
		default:
			kept = append(kept, it)
		}
	}
	s.pushQ = kept
	if len(s.pushQ) == 0 {
		return p
	}
	if lc == nil && s.client {
		if !s.muted && !s.opening && !now.Before(s.nextOpen) {
			s.startOpenLocked("push")
		}
		return p
	}
	// Speaking includes the client still playing the reply, and the gap
	// runs from the real end of speech (the client's drained).
	speaking := s.speakingOrPlayingLocked(now)
	shepherd := s.inputOpen || s.vad.Hold(now) > 0 // never over the shepherd
	last := s.lastSpoken
	if ended := s.play.EndedAt(now); ended.After(last) {
		last = ended
	}
	gapOK := now.Sub(last) >= s.cfg.PushGap

	pick, firstOther, otherReady, sawFirst := -1, -1, false, false
	blocked := map[string]bool{} // utterances an earlier, unready line answers
	for i := range s.pushQ {
		it := &s.pushQ[i]
		first := voice.PushFirstClass(it.requested, it.tags)
		if !first {
			if firstOther >= 0 || sawFirst {
				continue // only the oldest other line, and never past an owed one waiting
			}
			firstOther = i
		}
		sawFirst = sawFirst || first
		var ready bool
		switch {
		case it.urgency == voice.UrgencySilent:
			ready = true
		case !s.client:
			if it.heldAt.IsZero() {
				it.heldAt = now
				p.held = append(p.held, *it)
			}
		default:
			if it.urgency == voice.UrgencyInterrupt && speaking && !exempt(it.tags) {
				it.urgency = voice.UrgencyIdle
				p.deferred++
			}
			ready = !shepherd && (it.urgency == voice.UrgencyInterrupt || (!speaking && gapOK))
		}
		utt := voice.PushUtterance(it.tags)
		if ready && !blocked[utt] {
			if first || it.urgency == voice.UrgencySilent {
				pick = i
				break
			}
			otherReady = true
			continue
		}
		if utt != "" {
			blocked[utt] = true
		}
	}
	if pick < 0 && otherReady {
		pick = firstOther
	}
	if pick < 0 {
		return p
	}
	p.ok, p.item = true, s.pushQ[pick]
	p.over = append(p.over, s.pushQ[:pick]...)
	s.pushQ = append(s.pushQ[:pick:pick], s.pushQ[pick+1:]...)
	if p.item.urgency != voice.UrgencySilent {
		s.lastSpoken = now
		s.lastActivity = now
	}
	return p
}

// overtookPayload is voice.push_overtook's payload: the line sent and
// the push ids (or texts, for a line without one) of those it passed.
func overtookPayload(item pushItem, over []pushItem) map[string]any {
	ids := make([]string, 0, len(over))
	for _, o := range over {
		id := voice.PushID(o.tags)
		if id == "" {
			id = o.text
		}
		ids = append(ids, id)
	}
	return map[string]any{"push_id": voice.PushID(item.tags), "text": item.text, "over": ids}
}

// heldPayload is the voice.push_held / voice.push_released /
// voice.push_dropped payload: the push's text, urgency and tags (push_id,
// answers_utterance).
func heldPayload(item pushItem) map[string]any {
	p := map[string]any{"text": item.text, "urgency": item.requested.String()}
	for k, v := range item.tags {
		p[k] = v
	}
	return p
}

// deliverPush sends one push: silent → session.thinking.append, idle →
// session.commentary.append (the model says it in its own words),
// interrupt → local flush + session.instructions.append, which the docs
// say can interrupt speech in progress.
func (s *Session) deliverPush(lc *liveConn, item pushItem) {
	var ev appendEvent
	switch item.urgency {
	case voice.UrgencySilent:
		ev = appendEvent{Type: evThinkingAppend, Content: clip(item.text)}
	case voice.UrgencyInterrupt:
		s.interrupt("push")
		ev = appendEvent{Type: evInstructionsAppend, Content: clip("Stop what you are saying and tell the shepherd this now, briefly, in your own words: " + item.text)}
	default:
		ev = appendEvent{Type: evCommentaryAppend, Content: clip(item.text)}
	}
	s.mu.Lock()
	s.toolSeq++
	ev.EventID = "push_" + strconv.Itoa(s.toolSeq)
	s.mu.Unlock()
	setPushLinks(lc, ctxcap.LinksFromTags(item.tags))
	err := s.send(lc, ev)
	if err == nil && item.urgency != voice.UrgencySilent {
		s.mu.Lock()
		s.playUtt.Handed(voice.PushUtteranceID(item.tags))
		s.mu.Unlock()
	}
	payload := map[string]any{"text": item.text, "urgency": item.requested.String(), "scheduling_sent": item.urgency.SchedulingName(), "event": ev.Type}
	for k, v := range item.tags {
		payload[k] = v
	}
	if err != nil {
		payload["send_error"] = err.Error()
		s.emit(voice.EventError{Err: err})
	}
	s.log.Log(logSource, "voice.push", "", payload)
	if err != nil {
		// Not retried: the session is going away (a send fails only on
		// a dead socket). Say the line is lost (docs/timelines.md §4 (d)).
		d := heldPayload(item)
		d["reason"], d["error"] = "send_failed", err.Error()
		s.log.Log(logSource, "voice.push_dropped", "", d)
		return
	}
	s.emit(voice.EventPush{Text: item.text, Urgency: item.urgency})
}

// clip bounds an append's content (the API limit is 500 tokens; ~4
// characters a token leaves headroom at 1,600 characters).
func clip(s string) string {
	const max = 1600
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// PushBacklog implements voice.PushBacklogger: the queued pushes and
// when the oldest was queued (the daemon watchdog).
func (s *Session) PushBacklog() (n int, oldest time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.pushQ {
		if oldest.IsZero() || it.at.Before(oldest) {
			oldest = it.at
		}
	}
	return len(s.pushQ), oldest
}

// DrainPushes empties the push queue and the silent context held for the
// next session, and returns them (the held context first, then the
// queue, in order, at the urgency each caller asked for): the voice
// Switcher carries them over to the next provider's session on a
// switch.
func (s *Session) DrainPushes() []voice.QueuedPush {
	s.mu.Lock()
	held, q := s.held, s.pushQ
	s.held, s.pushQ = nil, nil
	s.mu.Unlock()
	out := make([]voice.QueuedPush, 0, len(held)+len(q))
	for _, text := range held {
		out = append(out, voice.QueuedPush{Text: text, Urgency: voice.UrgencySilent})
	}
	for _, it := range q {
		out = append(out, voice.QueuedPush{Text: it.text, Urgency: it.requested, Tags: it.tags})
	}
	return out
}
