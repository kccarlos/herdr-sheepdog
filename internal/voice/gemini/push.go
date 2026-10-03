package gemini

import (
	"context"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// pushItem is one queued Push.
type pushItem struct {
	text string
	// urgency is what will actually be sent: peekPush downgrades it from
	// INTERRUPT to WHEN_IDLE, in place, while the model is speaking.
	urgency voice.Urgency
	// requested is what the caller asked for, untouched by the
	// downgrade; the voice.push log keeps it as "urgency" so the record
	// shows the request even when scheduling_sent shows what happened.
	requested voice.Urgency
	retries   int
	// at is when the push was queued; heldAt when it was first held for
	// want of a listener (zero: never held). See holdPush.
	at     time.Time
	heldAt time.Time
	// tags travel onto the item's voice.push event (answers_job: the
	// investigation whose answer this push is; due_promise, needs_user:
	// the exceptions that keep INTERRUPT while the model is speaking).
	tags map[string]any
}

// exempt reports whether tags mark this push as one of the exceptions
// that keep INTERRUPT scheduling even while the model is speaking: a due
// promise, a decision on a block whose pending action this event
// created (docs/roles.md "Pushes"), or the stop button's reply (tag
// stop, as GPT-Live has it): the shepherd asked for silence, so what
// was playing is cut regardless.
func exempt(tags map[string]any) bool {
	return tags["due_promise"] == true || tags["needs_user"] == true || tags["stop"] == true
}

// Push policy (plan §3.6). UrgencySilent goes as soon as there is a
// connection and the voice is quiet (silentWaitLocked). UrgencyInterrupt goes as soon as the user is not speaking.
// UrgencyIdle additionally waits for the model's turn to complete and
// for PushGap since the previous spoken push. Which push goes next is
// pickPush's arbitration (voice/pushclass.go): the first ready line the
// shepherd is owed or must hear now, else the oldest other line — not
// merely the head of the queue.
//
// waitState means "block until the next state change"; a positive
// duration means "ready after this long unless something changes".
const waitState = -1

func (s *Session) pushLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		}
		for {
			p := s.pickPush()
			s.logPick(p)
			if p.ok {
				s.doPush(ctx, p.item)
				continue
			}
			if p.wait == waitState {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-s.kick:
			case <-time.After(p.wait):
			}
		}
	}
}

// pushPick is pickPush's verdict and what it has to log.
type pushPick struct {
	ok   bool
	item pushItem
	// wait is how long until something may be ready (waitState: until
	// the next state change) when nothing is ready now.
	wait time.Duration
	// over are the items the picked one overtook (voice.push_overtook).
	over []pushItem
	// timedOut: an open turn was given up on (TurnTimeout).
	timedOut bool
	// deferred, held and dropped are items downgraded to WHEN_IDLE while
	// the model speaks, first held for want of a listener, and dropped
	// after heldPushTTL.
	deferred int
	held     []pushItem
	dropped  []pushItem
}

// heldPushTTL is how long a spoken push waits for a listener before it is
// dropped (voice.push_dropped {reason: "no_listener"}): the same five
// minutes GPT-Live's standby queue waits (gptlive Config.PushTTL's
// default), so both providers treat an absent shepherd alike.
const heldPushTTL = 5 * time.Minute

// pickPush chooses the push to send now and removes it from the queue
// (voice/pushclass.go has the arbitration): among the lines the shepherd
// is owed or must hear now (class 0), the first ready one; failing
// that, the oldest other line, if it is ready. A line never overtakes an
// earlier one that answers the same utterance.
//
// Per item, as before for the head: an INTERRUPT push is downgraded to
// WHEN_IDLE, in place and once, while the model is speaking and no tag
// exempts it (docs/roles.md "Pushes": nothing interrupts the model
// mid-sentence except the shepherd); a spoken push is held while no /ws
// client is attached, so the model does not speak it to nobody
// (2026-09-24 18:22:04: a 12 s answer spoken with the page closed) —
// the invariant "a spoken push is heard or recorded as unheard", which
// the session cannot make unrepresentable since the browser can leave
// at any moment — and dropped once held longer than heldPushTTL. Silent
// pushes are context, not speech, and are never held.
// ClientAttached(true) pokes the loop.
func (s *Session) pickPush() pushPick {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var p pushPick
	if s.noClient {
		kept := s.pushQ[:0]
		for _, it := range s.pushQ {
			if it.urgency != voice.UrgencySilent && now.Sub(it.at) >= heldPushTTL {
				p.dropped = append(p.dropped, it)
				continue
			}
			kept = append(kept, it)
		}
		s.pushQ = kept
	}
	p.wait = waitState
	pick, firstOther, otherReady, sawFirst := -1, -1, false, false
	blocked := map[string]bool{} // utterances an earlier, unready line answers
	for i := range s.pushQ {
		it := &s.pushQ[i]
		first := voice.PushFirstClass(it.requested, it.tags)
		if !first {
			if firstOther >= 0 || sawFirst {
				// Only the oldest other line is ever a candidate, and
				// never past an owed one still waiting.
				continue
			}
			firstOther = i
		}
		sawFirst = sawFirst || first
		if it.urgency == voice.UrgencyInterrupt && s.speakingLocked(now) && !exempt(it.tags) {
			it.urgency = voice.UrgencyIdle
			p.deferred++
		}
		var wait time.Duration
		if s.noClient && it.urgency != voice.UrgencySilent {
			if it.heldAt.IsZero() {
				it.heldAt = now
				p.held = append(p.held, *it)
			}
			wait = heldPushTTL - now.Sub(it.at)
		} else {
			var timedOut bool
			wait, timedOut = s.pushReadyLocked(*it, now)
			p.timedOut = p.timedOut || timedOut
		}
		utt := voice.PushUtterance(it.tags)
		if wait == 0 && !blocked[utt] {
			if first || it.urgency == voice.UrgencySilent {
				// Silent context, once the voice is quiet, waits for
				// nothing more, so taking it in order costs an owed
				// line nothing.
				pick = i
				break
			}
			otherReady = true
			continue // the oldest other line: taken only if no owed line is ready
		}
		if utt != "" {
			blocked[utt] = true
		}
		if wait > 0 && (p.wait == waitState || wait < p.wait) {
			p.wait = wait
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
	return p
}

// logPick writes what pickPush decided: voice.push_dropped for a held
// line past heldPushTTL, voice.push_held the first time a line is held,
// voice.push_deferred for a downgrade, voice.push_timeout (and its
// recycle count) for a turn given up on, and voice.push_overtook when
// the line picked was not the oldest one queued.
func (s *Session) logPick(p pushPick) {
	now := s.now()
	for _, it := range p.dropped {
		s.log.Log(logSource, "voice.push_dropped", "", heldPayload(it, "no_listener", now.Sub(it.at)))
	}
	for _, it := range p.held {
		s.log.Log(logSource, "voice.push_held", "", heldPayload(it, "no_listener", 0))
	}
	for i := 0; i < p.deferred; i++ {
		s.log.Log(logSource, "voice.push_deferred", "", map[string]any{
			"kind": "push", "reason": "speaking", "urgency_requested": voice.UrgencyInterrupt.String(),
		})
	}
	if p.timedOut {
		s.notePushTimeout()
	}
	if p.ok && len(p.over) > 0 {
		s.log.Log(logSource, "voice.push_overtook", "", overtookPayload(p.item.text, p.item.tags, p.over))
	}
}

// overtookPayload is voice.push_overtook's payload: the line sent and
// the push ids (or texts, for a line without one) of those it passed.
func overtookPayload(text string, tags map[string]any, over []pushItem) map[string]any {
	ids := make([]string, 0, len(over))
	for _, o := range over {
		id := voice.PushID(o.tags)
		if id == "" {
			id = o.text
		}
		ids = append(ids, id)
	}
	return map[string]any{"push_id": voice.PushID(tags), "text": text, "over": ids}
}

// heldPayload is the voice.push_held / voice.push_released /
// voice.push_dropped payload: the push's text, urgency and tags (push_id,
// answers_utterance), with reason and age when given.
func heldPayload(item pushItem, reason string, age time.Duration) map[string]any {
	p := map[string]any{"text": item.text, "urgency": item.requested.String()}
	for k, v := range item.tags {
		p[k] = v
	}
	if reason != "" {
		p["reason"] = reason
	}
	if age > 0 {
		p["age_ms"] = age.Milliseconds()
	}
	return p
}

// pushReadyLocked returns 0 when item may be sent now, waitState when it must
// wait for a state change (connection, turn, speech) or a duration when
// only time stands in the way. timedOut reports that an open turn was
// given up on because TurnTimeout elapsed (the caller logs and counts it).
// Caller holds s.mu.
func (s *Session) pushReadyLocked(item pushItem, now time.Time) (wait time.Duration, timedOut bool) {
	if s.conn == nil || s.refreshing {
		// A refresh dials while the old connection is still live: the
		// line waits for the fresh one (install pokes the loop).
		return waitState, false
	}
	if item.urgency == voice.UrgencySilent {
		return s.silentWaitLocked(now), false
	}
	if until := s.speakingUntilLocked(now); until > 0 {
		return until, false
	}
	if item.urgency == voice.UrgencyInterrupt {
		return 0, false
	}
	if s.turnOpen {
		// Wait for turnComplete, but re-check when TurnTimeout elapses:
		// the model sometimes answers a push with nothing at all (seen
		// live), and waitState would then hold every later push until the
		// next unrelated event.
		if left := s.cfg.TurnTimeout - now.Sub(s.turnOpenedAt); left > 0 {
			return left, false
		}
		s.turnOpen = false // the turn never completed; do not wait forever
		s.speaking = false
		timedOut = true
	}
	// The model's turn is over, but the client may still be playing it
	// (Gemini generates ~4× real time): wait for the real end of speech
	// (the client's drained; without playback reports, the timing
	// guess), and measure the gap from it.
	if playing, left := s.play.Playing(now); playing {
		return left, timedOut
	}
	last := s.lastSpoken
	if ended := s.play.EndedAt(now); ended.After(last) {
		last = ended
	}
	if gap := s.cfg.PushGap - now.Sub(last); gap > 0 {
		return gap, timedOut
	}
	return 0, timedOut
}

// Silent context waits for the voice to be quiet (campaign D N1,
// docs/eval/campaign-d-2026-10-03.md): it used to go out at once, as a
// user-role client turn, even while a spoken push was being generated.
// 11 of 135 spoken pushes stopped mid-sentence for 3–24 s and their
// tail came back only at the next turn, glued to the reply to the
// CEO; 6 of the 11 had the names line or the current-focus line sent
// while they were spoken (15 % of the 41 pushes a silent line landed
// in, 5 % of the other 94). Context is not urgent, so it now waits
// until no output is being generated and the client is not playing.
// silentRecheck is how often a silent line held for generation looks
// again (the reader pokes the loop on turnComplete, but not on every
// interruption).
const silentRecheck = 250 * time.Millisecond

// silentWaitLocked is how long a silent push must still wait: while the
// model generates output (bounded by TurnTimeout since its last output,
// so a turn that never completes cannot hold context for ever) and
// while the client plays it. Caller holds s.mu.
func (s *Session) silentWaitLocked(now time.Time) time.Duration {
	if s.speaking {
		if left := s.cfg.TurnTimeout - now.Sub(s.turnOpenedAt); left > 0 {
			return min(left, silentRecheck)
		}
		return 0
	}
	if playing, left := s.play.Playing(now); playing {
		return left
	}
	return 0
}

// speakingUntilLocked returns how long the user still counts as speaking
// (0 when not): the client's own speech detector when it reports one
// (open, and ClientSpeechRelease after it closed — it sees the onset
// long before a transcript does), or input transcription activity within
// SpeechHold that has not been marked finished, whichever holds longer.
// Muted users are never speaking.
func (s *Session) speakingUntilLocked(now time.Time) time.Duration {
	if s.muted {
		return 0
	}
	hold := s.vad.Hold(now)
	if !s.inputDone && !s.lastInput.IsZero() {
		if left := s.cfg.SpeechHold - now.Sub(s.lastInput); left > hold {
			hold = left
		}
	}
	return hold
}

// maxPushRetries is how many times a push whose send failed is put back
// at the front of the queue before it is dropped (voice.push_dropped
// {reason: send_failed}).
const maxPushRetries = 3

// doPush sends one item. Interrupt flushes local output first; the model
// interrupts itself when the client content arrives.
func (s *Session) doPush(ctx context.Context, item pushItem) {
	gen := s.liveGen()
	if item.urgency == voice.UrgencyInterrupt {
		s.interrupt("push")
	}
	turnComplete := item.urgency != voice.UrgencySilent
	err := s.sendOn(gen, func(c Conn) error {
		setLinks(c, ctxcap.LinksFromTags(item.tags))
		return c.SendClientContent(genai.LiveClientContentInput{
			Turns:        pushTurn(item.text, item.urgency),
			TurnComplete: genai.Ptr(turnComplete),
		})
	})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		item.retries++
		s.mu.Lock()
		requeue := item.retries <= maxPushRetries
		if requeue {
			s.pushQ = append([]pushItem{item}, s.pushQ...)
		}
		s.mu.Unlock()
		s.emit(voice.EventError{Err: err})
		if !requeue {
			// Given up on: say so, or the line is lost without a trace
			// (docs/timelines.md C23, §5 "a spoken push is heard or
			// recorded unheard").
			p := heldPayload(item, "send_failed", 0)
			p["retries"] = item.retries
			p["error"] = err.Error()
			s.log.Log(logSource, "voice.push_dropped", "", p)
		}
		return
	}
	s.mu.Lock()
	if id, _ := item.tags[ctxcap.TagCallID].(string); id != "" {
		delete(s.owedReplies, id) // the reply the voice was told follows
	}
	var unheard []voice.UnheardPush
	if turnComplete {
		unheard = s.heard.Pushed(s.now(), item.text, item.tags, item.tags[tagRequeued] == true)
		s.flushRecapLocked()
		s.recap.Add(voice.RecapSupervisor, item.text)
		s.lastSpoken = s.now()
		s.playUtt.Handed(voice.PushUtteranceID(item.tags))
		s.openTurnLocked()
		if s.turn != nil {
			s.turn.pushesSpoken++
		}
	}
	now := s.now()
	s.mu.Unlock()
	s.reportUnheard(unheard)
	if !item.heldAt.IsZero() {
		p := heldPayload(item, "", 0)
		p["held_ms"] = now.Sub(item.heldAt).Milliseconds()
		s.log.Log(logSource, "voice.push_released", "", p)
	}
	payload := map[string]any{"text": item.text, "urgency": item.requested.String(), "scheduling_sent": item.urgency.SchedulingName()}
	for k, v := range item.tags {
		payload[k] = v
	}
	s.log.Log(logSource, "voice.push", "", payload)
	s.emit(voice.EventPush{Text: item.text, Urgency: item.urgency})
}

// PushBacklog implements voice.PushBacklogger: the queued pushes and
// when the oldest was queued (the daemon watchdog).
func (s *Session) PushBacklog() (n int, oldest time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return backlog(s.pushQ)
}

func backlog(q []pushItem) (n int, oldest time.Time) {
	for _, it := range q {
		if oldest.IsZero() || it.at.Before(oldest) {
			oldest = it.at
		}
	}
	return len(q), oldest
}

// DrainPushes empties the push queue and returns what was in it, in
// order, at the urgency each caller asked for: the voice Switcher
// carries it over to the next provider's session on a switch.
func (s *Session) DrainPushes() []voice.QueuedPush {
	s.mu.Lock()
	q := s.pushQ
	s.pushQ = nil
	s.mu.Unlock()
	out := make([]voice.QueuedPush, len(q))
	for i, it := range q {
		out[i] = voice.QueuedPush{Text: it.text, Urgency: it.requested, Tags: it.tags}
	}
	return out
}
