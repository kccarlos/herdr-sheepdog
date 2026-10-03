package supervisor

import (
	"fmt"

	"github.com/kccarlos/herdr-sheepdog/internal/state"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Unheard pushes (docs/bench-2026-10-03.md #17, #18, #19). The voice
// layer judges whether each spoken push reached the CEO (voice/heard.go)
// and re-queues, once, the lines they are owed a hearing of. Every other
// unheard line is a fact the brain cannot see: it becomes a transcript
// context line, and the brain decides on its next turn whether to
// mention it. No model call is made here.

// onPushUnheard records a push the CEO did not hear in the brain's
// transcript, unless the voice layer re-queued it.
func (s *Supervisor) onPushUnheard(e voice.EventPushUnheard) {
	s.markUnheard(e.Push.Text) // heard.go
	if e.Requeue || s.brain == nil || e.Push.Text == "" {
		return
	}
	line := fmt.Sprintf("the CEO did not hear: %q", e.Push.Text)
	if e.Push.Heard != "" {
		line += fmt.Sprintf(" (only %q reached them)", e.Push.Heard)
	}
	s.brain.Daemon(line)
}

// OpenCheck is the voice layer's voice.OpenCheck. With nil tags: is a
// worker's prompt waiting for the CEO (a pending answer_prompt; an
// instruction awaiting confirmation does not hold a session refresh)?
// With a push's tags: is what it is about still owed to them — a kept
// or due promise, or the prompt its worker event raised, still the
// pending one? An answer_prompt lives as long as the pane waits at it.
func (s *Supervisor) OpenCheck(tags map[string]any) bool {
	p, has := s.store.Pending()
	has = has && p.Kind == state.PendingAnswerPrompt
	if tags == nil {
		return has
	}
	if tags["due_promise"] == true {
		return true
	}
	switch ids := tags[voice.TagKeepsPromiseIDs].(type) {
	case []string:
		if len(ids) > 0 {
			return true
		}
	case []any:
		if len(ids) > 0 {
			return true
		}
	}
	evt, _ := tags[tagEventID].(string)
	return has && evt != "" && s.eventIDOf(p.PaneID) == evt
}
