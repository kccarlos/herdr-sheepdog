package supervisor

import (
	"context"
	"fmt"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// "New conversation" (docs/roles.md): resets the brain's transcript
// window and the voice model's session, so both start from scratch. The
// roster and its ledger, open approval prompts, promises, notes, running
// investigations and the event log itself are untouched — they never
// lived in the window or the voice session's own memory, so there is
// nothing to restore.
//
// Two triggers reach the same ResetConversation: POST
// /api/conversation/reset (web/conversation.go), and the bare voice
// phrase handled here — a two-step confirm, since this is destructive to
// the conversation so far and the shepherd may have said the trigger
// words in passing.

// resetConfirmWindow is how long a "start a new conversation?" question
// stays answerable by the confirm phrase.
const resetConfirmWindow = 20 * time.Second

// bareResetTriggerWords are the whole-utterance phrases that ask for a
// reset (BareWord-normalised: punctuation and case do not matter).
var bareResetTriggerWords = map[string]bool{
	"start over": true, "new conversation": true, "start from scratch": true,
	"reset the conversation": true, "clear the conversation": true,
}

// bareResetConfirmWords are the whole-utterance phrases that confirm a
// pending reset; a bare "yes" is deliberately not among them — it keeps
// its ordinary approval-prompt meaning (docs/roles.md "The one rail").
var bareResetConfirmWords = map[string]bool{
	"yes start over": true, // "yes, start over" normalises to this too
	"confirm reset":  true,
}

// resetConfirmState is the "start a new conversation?" question waiting
// for its confirm phrase, under s.mu.
type resetConfirmState struct {
	at time.Time
}

// voiceResetter is a Pusher that can also drop the voice session's
// memory (the concrete type behind it is *voice.Switcher, or the
// daemon's lazy indirection to one); a Pusher without it (--voice=off,
// a test fake) just skips that half of the reset.
type voiceResetter interface {
	Reset(reason string) error
}

// ConversationStartedAt is when the current conversation began: the
// last reset, or when this Supervisor was built (daemon start),
// GET /api/state's conversation_started_at.
func (s *Supervisor) ConversationStartedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conversationStartedAt
}

// ResetConversation is "New conversation": it empties the brain's
// transcript window (a fresh segment; the compaction summary is gone)
// and drops the voice session's own memory (Gemini: a fresh reconnect
// with no resumption handle; GPT-Live: the billed session, if open,
// closes now and reopens later with no recap), while the roster,
// promises, notes, investigations, pending prompts and the event log are
// untouched. via names the trigger ("app" | "debug" | "voice_page" |
// "voice") for the conversation.reset event and the API response; id
// also names the brain's fresh transcript segment when there is a
// brain. Logs conversation.reset {via, reset_id, at}.
func (s *Supervisor) ResetConversation(ctx context.Context, via string) (id string, at time.Time, err error) {
	if s.brain != nil {
		id, err = s.brain.Reset(ctx, via)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("supervisor: reset: %w", err)
		}
	} else {
		id = s.nextID("reset")
	}
	if r, ok := s.pusher.(voiceResetter); ok {
		if verr := r.Reset(via); verr != nil {
			s.log.Log(logSource, "supervisor.voice_reset_error", "", map[string]any{"error": verr.Error(), "via": via})
		}
	}
	at = s.cfg.Now()
	s.mu.Lock()
	s.conversationStartedAt = at
	s.resetConfirm = nil
	s.mu.Unlock()
	s.log.Log(logSource, "conversation.reset", "", map[string]any{"via": via, "reset_id": id, "at": at.UTC().Format(time.RFC3339)})
	return id, at, nil
}

// resetConfirmFastPath is the voice bare-phrase trigger (docs/roles.md
// "Triggers" (b)): a code fast path, like bareStop — no model. Called
// from ToSupervisor with the shepherd's already-validated input text
// (claimInput ran first, so only real shepherd input reaches here — "the
// only shepherd input can confirm" requirement). ok is false for
// anything else, which falls through to the brain turn as an ordinary
// utterance.
func (s *Supervisor) resetConfirmFastPath(ctx context.Context, text string) (voice.ToolResult, bool) {
	word := BareWord(text)
	s.mu.Lock()
	pending := s.resetConfirm
	if pending != nil && s.cfg.Now().Sub(pending.at) > resetConfirmWindow {
		s.resetConfirm = nil
		pending = nil
		s.mu.Unlock()
		s.log.Log(logSource, "supervisor.reset_confirm", "", map[string]any{"phase": "expired"})
	} else {
		s.mu.Unlock()
	}
	switch {
	case pending != nil && bareResetConfirmWords[word]:
		s.mu.Lock()
		s.resetConfirm = nil
		s.mu.Unlock()
		s.log.Log(logSource, "supervisor.reset_confirm", "", map[string]any{"phase": "confirmed"})
		return s.performReset(ctx, "voice")
	case bareResetTriggerWords[word]:
		s.mu.Lock()
		s.resetConfirm = &resetConfirmState{at: s.cfg.Now()}
		s.mu.Unlock()
		s.log.Log(logSource, "supervisor.reset_confirm", "", map[string]any{"phase": "asked"})
		return voice.ToolResult{Spoken: `Start a new conversation? Say "yes, start over" to confirm.`, Clarify: true, Scheduling: voice.UrgencyInterrupt}, true
	default:
		return voice.ToolResult{}, false
	}
}

// performReset runs ResetConversation and speaks the fresh-start line
// (docs/roles.md "Triggers" (b)), or a gentle apology on failure —
// either way this is a fast path's result, not a brain turn's.
func (s *Supervisor) performReset(ctx context.Context, via string) (voice.ToolResult, bool) {
	if _, _, err := s.ResetConversation(ctx, via); err != nil {
		s.log.Log(logSource, "supervisor.reset_error", "", map[string]any{"error": err.Error(), "via": via})
		return voice.ToolResult{Spoken: "Sorry, I could not start a new conversation.", Scheduling: voice.UrgencyInterrupt}, true
	}
	return voice.ToolResult{Spoken: "Fresh start. What's next?", Scheduling: voice.UrgencyInterrupt}, true
}
