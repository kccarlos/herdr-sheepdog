package gemini

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// turnStats follows one input turn: from the user's last input fragment
// (or typed text) to the model's first response (a tool call or the first
// output audio) and on to turnComplete. It backs voice.turn, the stall
// detector and the recycle decision; see docs/dev/gemini-live-notes.md §13.
type turnStats struct {
	id           int
	startedAt    time.Time // first input fragment
	inputAt      time.Time // last input fragment before the response
	responded    bool
	responseMs   int64
	toolCalls    int
	pushesSpoken int
	pendingTools int // tool calls whose response has not been sent yet
	stalled      bool
	stallTimer   *time.Timer
}

// noteInput records an input fragment (transcription) or typed text: it
// opens a turn when none is open, refreshes the stall timer, and returns
// the voice.turn payload of a turn it superseded (the model had already
// answered and the user spoke again), or nil.
func (s *Session) noteInput(now time.Time) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var superseded map[string]any
	t := s.turn
	if t != nil && t.responded {
		superseded = s.finishTurnLocked("superseded")
		t = nil
	}
	if t == nil {
		s.turnSeq++
		t = &turnStats{id: s.turnSeq, startedAt: now}
		s.turn = t
	}
	t.inputAt = now
	if t.stallTimer != nil {
		t.stallTimer.Stop()
	}
	id := t.id
	t.stallTimer = time.AfterFunc(s.cfg.StallAfter, func() { s.onStall(id) })
	return superseded
}

// noteResponseLocked marks the model's first reaction to the open turn.
// Caller holds s.mu.
func (s *Session) noteResponseLocked(now time.Time) {
	t := s.turn
	if t == nil || t.responded {
		return
	}
	t.responded = true
	t.responseMs = now.Sub(t.inputAt).Milliseconds()
	if t.stallTimer != nil {
		t.stallTimer.Stop()
	}
}

// noteToolCallLocked counts a tool call against the open turn.
func (s *Session) noteToolCallLocked(now time.Time) {
	s.noteResponseLocked(now)
	if t := s.turn; t != nil {
		t.toolCalls++
		t.pendingTools++
	}
}

// noteToolDoneLocked is called once a tool's response was sent (or the
// call was cancelled): the turn may complete on the next turnComplete.
func (s *Session) noteToolDoneLocked() {
	if t := s.turn; t != nil && t.pendingTools > 0 {
		t.pendingTools--
	}
}

// noteTurnCompleteLocked closes the open turn on the model's turnComplete
// unless a tool response is still owed (the model will speak the result in
// a further generation). Returns the voice.turn payload or nil.
func (s *Session) noteTurnCompleteLocked() map[string]any {
	t := s.turn
	if t == nil || t.pendingTools > 0 {
		return nil
	}
	return s.finishTurnLocked("complete")
}

// finishTurnLocked closes the open turn and returns its log payload.
func (s *Session) finishTurnLocked(how string) map[string]any {
	t := s.turn
	if t == nil {
		return nil
	}
	s.turn = nil
	if t.stallTimer != nil {
		t.stallTimer.Stop()
	}
	payload := map[string]any{
		"turn": t.id, "end": how, "responded": t.responded, "tool_calls": t.toolCalls,
		"pushes_spoken": t.pushesSpoken, "stalled": t.stalled,
		"dur_ms": s.now().Sub(t.startedAt).Milliseconds(),
	}
	if t.responded {
		payload["input_to_response_ms"] = t.responseMs
	}
	return payload
}

// onStall fires StallAfter after the last input of turn id. A turn the
// model has still not reacted to is logged as stalled and surfaced as
// EventStatus{Stalled:true}; the second stall on one connection recycles
// it (docs/dev/gemini-live-notes.md §13).
func (s *Session) onStall(id int) {
	s.mu.Lock()
	t := s.turn
	if t == nil || t.id != id || t.responded || t.stalled || s.status != voice.StatusConnected {
		s.mu.Unlock()
		return
	}
	t.stalled = true
	s.stalls++
	s.stalledNow = true
	stalls := s.stalls
	waited := s.now().Sub(t.inputAt).Milliseconds()
	s.mu.Unlock()
	s.log.Log(logSource, "voice.stalled", "", map[string]any{"turn": id, "waited_ms": waited, "stalls": stalls})
	s.emit(voice.EventStatus{Status: voice.StatusConnected, Stalled: true})
	if stalls >= s.cfg.StallsBeforeRecycle {
		s.requestRecycle("stalled")
	}
}

// clearStalled resets the stalled flag once the model reacts again.
func (s *Session) clearStalled() {
	s.mu.Lock()
	was := s.stalledNow
	s.stalledNow = false
	s.mu.Unlock()
	if was {
		s.emit(voice.EventStatus{Status: voice.StatusConnected, Stalled: false})
	}
}

// notePushTimeout is called when an idle push outwaited TurnTimeout; the
// second time on one connection recycles it.
func (s *Session) notePushTimeout() {
	s.mu.Lock()
	s.pushTimeouts++
	n := s.pushTimeouts
	s.mu.Unlock()
	s.log.Log(logSource, "voice.push_timeout", "", map[string]any{"count": n})
	if n >= s.cfg.StallsBeforeRecycle {
		s.requestRecycle("push_timeout")
	}
}

// Recycle swaps the live connection for a fresh one resumed from the
// current handle, the path a GoAway or repeated stalls take (logged as
// voice.recycle). For the bench and debugging: it forces one resume
// without waiting for the server's ~9 minute GoAway. Coalesced.
func (s *Session) Recycle(reason string) { s.requestRecycle(reason) }

// requestRecycle asks the run loop for a proactive reconnect with the
// current resumption handle (the GoAway path). Coalesced.
func (s *Session) requestRecycle(reason string) {
	select {
	case s.recycle <- reason:
	default:
	}
}
