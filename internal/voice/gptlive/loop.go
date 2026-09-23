package gptlive

import (
	"context"
	"strconv"
	"time"

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
// pending").
func (s *Session) keepAlive(now time.Time) {
	s.mu.Lock()
	lc := s.live
	s.mu.Unlock()
	if lc == nil {
		return
	}
	if last := lc.lastSend.Load(); last != 0 && now.Sub(time.Unix(0, last)) < 2*s.cfg.Tick {
		return
	}
	n := int(s.cfg.Tick.Seconds()*SampleRate) * 2
	_ = s.sendAudio(lc, make([]byte, n))
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
			s.unclaimed = ""
			stop = &call
		}
	}
	s.mu.Unlock()
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
	s.mu.Unlock()
	s.emit(voice.EventTurnComplete{})
}

// idleCheck closes a live session nothing has used for IdleClose: no
// transcript either way, no voiced model audio, no delegation or push in
// flight. Closing disarms the microphone trigger so the same room noise
// does not reopen it at once.
func (s *Session) idleCheck(now time.Time) {
	s.mu.Lock()
	lc := s.live
	if lc == nil || len(s.inflight) > 0 || len(s.pushQ) > 0 || s.inputOpen || s.speakingLocked(now) ||
		now.Sub(s.lastActivity) < s.cfg.IdleClose {
		s.mu.Unlock()
		return
	}
	s.armed = false
	s.quietSince = time.Time{}
	s.mu.Unlock()
	s.closeLive(lc, "idle")
}

// exempt reports whether tags keep an INTERRUPT push at INTERRUPT while
// the model is speaking (docs/roles.md "Pushes").
func exempt(tags map[string]any) bool {
	return tags["due_promise"] == true || tags["needs_user"] == true || tags["stop"] == true
}

// pumpPushes delivers at most one queued push per tick. In standby a
// spoken push opens a session when a client is listening, waits (up to
// PushTTL) when none is, and silent context is kept for the next
// session's history.
func (s *Session) pumpPushes(now time.Time) {
	s.mu.Lock()
	var item pushItem
	var send bool
	var deferred, dropped bool
	for len(s.pushQ) > 0 {
		head := &s.pushQ[0]
		lc := s.live
		if lc == nil {
			if head.urgency == voice.UrgencySilent {
				s.held = append(s.held, head.text)
				if len(s.held) > 10 {
					s.held = s.held[len(s.held)-10:]
				}
				s.pushQ = s.pushQ[1:]
				continue
			}
			if now.Sub(head.at) > s.cfg.PushTTL {
				item = *head
				s.pushQ = s.pushQ[1:]
				dropped = true
				break
			}
			if s.client && !s.muted && !s.opening && !now.Before(s.nextOpen) {
				s.startOpenLocked("push")
			}
			break
		}
		if head.urgency == voice.UrgencySilent {
			item, send = *head, true
			s.pushQ = s.pushQ[1:]
			break
		}
		speaking := s.speakingLocked(now)
		if head.urgency == voice.UrgencyInterrupt && speaking && !exempt(head.tags) {
			head.urgency = voice.UrgencyIdle
			deferred = true
		}
		if s.inputOpen {
			break // never over the shepherd
		}
		if head.urgency == voice.UrgencyIdle && (speaking || now.Sub(s.lastSpoken) < s.cfg.PushGap) {
			break
		}
		item, send = *head, true
		s.pushQ = s.pushQ[1:]
		s.lastSpoken = now
		s.lastActivity = now
		break
	}
	lc := s.live
	s.mu.Unlock()
	if deferred {
		s.log.Log(logSource, "voice.push_deferred", "", map[string]any{"kind": "push", "reason": "speaking", "urgency_requested": voice.UrgencyInterrupt.String()})
	}
	if dropped {
		s.log.Log(logSource, "voice.push_dropped", "", map[string]any{"text": item.text, "reason": "no_listener", "age_ms": now.Sub(item.at).Milliseconds()})
		return
	}
	if send && lc != nil {
		s.deliverPush(lc, item)
	}
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
	err := s.send(lc, ev)
	payload := map[string]any{"text": item.text, "urgency": item.requested.String(), "scheduling_sent": item.urgency.SchedulingName(), "event": ev.Type}
	for k, v := range item.tags {
		payload[k] = v
	}
	if err != nil {
		payload["send_error"] = err.Error()
		s.emit(voice.EventError{Err: err})
	}
	s.log.Log(logSource, "voice.push", "", payload)
	if err == nil {
		s.emit(voice.EventPush{Text: item.text, Urgency: item.urgency})
	}
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
