package gemini

import (
	"context"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// pushItem is one queued Push.
type pushItem struct {
	text    string
	urgency voice.Urgency
	retries int
	// tags travel onto the item's voice.push event (answers_job: the
	// investigation whose answer this push is).
	tags map[string]any
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

func (s *Session) peekPush() (pushItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pushQ) == 0 {
		return pushItem{}, false
	}
	return s.pushQ[0], true
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
		timedOut = true
	}
	if gap := s.cfg.PushGap - now.Sub(s.lastSpoken); gap > 0 {
		return gap, timedOut
	}
	return 0, timedOut
}

// speakingUntilLocked returns how long the user still counts as speaking
// (0 when not). Heuristic: input transcription activity within SpeechHold
// that has not been marked finished. Muted users are never speaking.
func (s *Session) speakingUntilLocked(now time.Time) time.Duration {
	if s.muted || s.inputDone || s.lastInput.IsZero() {
		return 0
	}
	if left := s.cfg.SpeechHold - now.Sub(s.lastInput); left > 0 {
		return left
	}
	return 0
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
	s.mu.Unlock()
	payload := map[string]any{"text": item.text, "urgency": item.urgency.String()}
	for k, v := range item.tags {
		payload[k] = v
	}
	s.log.Log(logSource, "voice.push", "", payload)
	s.emit(voice.EventPush{Text: item.text, Urgency: item.urgency})
}
