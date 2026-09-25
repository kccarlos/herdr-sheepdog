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

// Push policy (plan §3.6): one at a time, FIFO. UrgencySilent goes as
// soon as there is a connection. UrgencyInterrupt goes as soon as the user
// is not speaking. UrgencyIdle additionally waits for the model's turn to
// complete and for PushGap since the previous spoken push.
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
			item, ok := s.peekPush()
			if !ok {
				break
			}
			if held, wait := s.holdPush(); held {
				if wait == 0 {
					continue // the head expired and was dropped
				}
				select {
				case <-ctx.Done():
					return
				case <-s.kick:
				case <-time.After(wait):
				}
				continue
			}
			wait, timedOut := s.pushReady(item)
			if timedOut {
				s.notePushTimeout()
			}
			if wait == 0 {
				s.popPush()
				s.doPush(ctx, item)
				continue
			}
			if wait == waitState {
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-s.kick:
			case <-time.After(wait):
			}
		}
	}
}

// peekPush returns the head of the queue, downgrading it from INTERRUPT
// to WHEN_IDLE in place — once, since the mutation makes the condition
// below false on every later peek — while the model is speaking and the
// item is not exempt (docs/roles.md "Pushes": nothing interrupts the
// model mid-sentence except the shepherd). The downgraded item then
// runs the ordinary WHEN_IDLE wait below; voice.push_deferred is logged
// outside the lock.
func (s *Session) peekPush() (pushItem, bool) {
	s.mu.Lock()
	if len(s.pushQ) == 0 {
		s.mu.Unlock()
		return pushItem{}, false
	}
	head := &s.pushQ[0]
	deferNow := head.urgency == voice.UrgencyInterrupt && s.speakingLocked(s.now()) && !exempt(head.tags)
	if deferNow {
		head.urgency = voice.UrgencyIdle
	}
	item := *head
	s.mu.Unlock()
	if deferNow {
		s.log.Log(logSource, "voice.push_deferred", "", map[string]any{
			"kind": "push", "reason": "speaking", "urgency_requested": voice.UrgencyInterrupt.String(),
		})
	}
	return item, true
}

// heldPushTTL is how long a spoken push waits for a listener before it is
// dropped (voice.push_dropped {reason: "no_listener"}): the same five
// minutes GPT-Live's standby queue waits (gptlive Config.PushTTL's
// default), so both providers treat an absent shepherd alike.
const heldPushTTL = 5 * time.Minute

// holdPush keeps a spoken push (idle or interrupt) queued while no /ws
// client is attached, so the model does not speak it to nobody
// (2026-09-24 18:22:04: a 12 s answer spoken with the page closed). It
// guards the invariant "a spoken push is heard or recorded as unheard":
// the session cannot make an absent listener unrepresentable, since the
// browser can leave at any moment between the push and the speech. The
// first hold of an item logs voice.push_held; an item held longer than
// heldPushTTL is dropped with voice.push_dropped. held reports that the
// head must not be sent now; wait is how long until the TTL (0 after a
// drop: look at the new head at once). Silent pushes are context, not
// speech, and are never held. ClientAttached(true) pokes the loop.
func (s *Session) holdPush() (held bool, wait time.Duration) {
	s.mu.Lock()
	if !s.noClient || len(s.pushQ) == 0 || s.pushQ[0].urgency == voice.UrgencySilent {
		s.mu.Unlock()
		return false, 0
	}
	now := s.now()
	head := &s.pushQ[0]
	if age := now.Sub(head.at); age >= heldPushTTL {
		item := *head
		s.pushQ = s.pushQ[1:]
		s.mu.Unlock()
		s.log.Log(logSource, "voice.push_dropped", "", heldPayload(item, "no_listener", age))
		return true, 0
	}
	first := head.heldAt.IsZero()
	if first {
		head.heldAt = now
	}
	item := *head
	s.mu.Unlock()
	if first {
		s.log.Log(logSource, "voice.push_held", "", heldPayload(item, "no_listener", 0))
	}
	return true, heldPushTTL - now.Sub(item.at)
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

func (s *Session) popPush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pushQ) > 0 {
		s.pushQ = s.pushQ[1:]
	}
}

// pushReady returns 0 when item may be sent now, waitState when it must
// wait for a state change (connection, turn, speech) or a duration when
// only time stands in the way. timedOut reports that an open turn was
// given up on because TurnTimeout elapsed (the caller logs and counts it).
func (s *Session) pushReady(item pushItem) (wait time.Duration, timedOut bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return waitState, false
	}
	if item.urgency == voice.UrgencySilent {
		return 0, false
	}
	now := s.now()
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
		if item.retries <= 3 {
			s.pushQ = append([]pushItem{item}, s.pushQ...)
		}
		s.mu.Unlock()
		s.emit(voice.EventError{Err: err})
		return
	}
	s.mu.Lock()
	if turnComplete {
		s.lastSpoken = s.now()
		s.openTurnLocked()
		if s.turn != nil {
			s.turn.pushesSpoken++
		}
	}
	now := s.now()
	s.mu.Unlock()
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
