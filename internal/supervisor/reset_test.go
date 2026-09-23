package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// The voice phrase asks a confirming question and sends nothing yet.
func TestResetPhraseAsksForConfirmation(t *testing.T) {
	h := newHarness(t)
	res := h.say("start over")
	if res.Spoken != `Start a new conversation? Say "yes, start over" to confirm.` {
		t.Fatalf("result = %+v", res)
	}
	if got := h.log.ofType("conversation.reset"); len(got) != 0 {
		t.Fatalf("reset already happened: %v", got)
	}
	if got := h.pusher.allResets(); len(got) != 0 {
		t.Fatalf("voice reset already happened: %v", got)
	}
}

// "yes, start over" within the window performs the reset and the fresh
// session speaks one short line.
func TestResetPhraseThenConfirmResets(t *testing.T) {
	h := newHarness(t)
	h.say("new conversation")
	res := h.say("yes, start over")
	if res.Spoken != "Fresh start. What's next?" {
		t.Fatalf("result = %+v", res)
	}
	got := h.log.ofType("conversation.reset")
	if len(got) != 1 || payload(got[0])["via"] != "voice" {
		t.Fatalf("conversation.reset = %v", got)
	}
	if resets := h.pusher.allResets(); len(resets) != 1 || resets[0] != "voice" {
		t.Fatalf("voice resets = %v", resets)
	}
}

// A plain "yes" never confirms the reset — with no open worker prompt it
// falls through to the brain (or, with none, the roster fallback) like
// any other utterance, and no reset happens.
func TestResetPhrasePlainYesDoesNotConfirm(t *testing.T) {
	h := newHarness(t)
	h.say("start over")
	h.say("yes")
	if got := h.log.ofType("conversation.reset"); len(got) != 0 {
		t.Fatalf("plain yes must not confirm a reset: %v", got)
	}
}

// A plain "yes" while a worker prompt is open keeps answering that
// prompt, not the pending reset confirmation.
func TestResetPhrasePlainYesStillAnswersOpenPrompt(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	h.say("start over")
	res := h.say("yes")
	if res.Spoken != "Told auth yes." {
		t.Fatalf("result = %+v, want the prompt answered", res)
	}
	if got := h.sendKeysRequests(); len(got) != 1 {
		t.Fatalf("keys sent = %v, want the prompt's yes keys", got)
	}
	if got := h.log.ofType("conversation.reset"); len(got) != 0 {
		t.Fatalf("must not have reset: %v", got)
	}
}

// The confirmation expires after 20 s.
func TestResetConfirmationExpires(t *testing.T) {
	h := newHarness(t)
	h.say("start over")
	h.clock.Advance(21 * time.Second)
	h.say("yes, start over")
	if got := h.log.ofType("conversation.reset"); len(got) != 0 {
		t.Fatalf("expired confirmation must not reset: %v", got)
	}
	if got := h.log.ofType("supervisor.reset_confirm"); len(got) == 0 {
		t.Fatal("expected supervisor.reset_confirm to be logged")
	}
}

// Only a real shepherd input can confirm: a to_supervisor relay with
// nothing fresh behind it (the voice model's own initiative) neither
// asks nor confirms.
func TestResetPhraseNeedsShepherdInput(t *testing.T) {
	h := newHarness(t)
	res := h.relay("start over") // relayed with no hear() behind it
	if res.Spoken != "" {
		t.Fatalf("result = %+v, want the voice-initiative silent result", res)
	}
	if got := h.log.ofType("supervisor.reset_confirm"); len(got) != 0 {
		t.Fatalf("must not have asked: %v", got)
	}
}

// ResetConversation (the API path) resets with no brain attached: it
// still drops the voice session's memory, logs conversation.reset and
// moves conversation_started_at.
func TestResetConversationAPIPathNoBrain(t *testing.T) {
	h := newHarness(t)
	before := h.sup.ConversationStartedAt()
	h.clock.Advance(5 * time.Minute)

	id, at, err := h.sup.ResetConversation(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected a reset id")
	}
	if !at.Equal(h.clock.Now()) {
		t.Fatalf("at = %v, want %v", at, h.clock.Now())
	}
	if got := h.sup.ConversationStartedAt(); !got.After(before) {
		t.Fatalf("conversation_started_at = %v, want after %v", got, before)
	}
	if resets := h.pusher.allResets(); len(resets) != 1 || resets[0] != "app" {
		t.Fatalf("voice resets = %v", resets)
	}
	got := h.log.ofType("conversation.reset")
	if len(got) != 1 || payload(got[0])["via"] != "app" || payload(got[0])["reset_id"] != id {
		t.Fatalf("conversation.reset = %v", got)
	}
}

// A brain's ResetConversation runs Brain.Reset and reports its id.
func TestResetConversationWithBrainUsesBrainID(t *testing.T) {
	h := newBrainHarness(t, braintest.NewModel())
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	id, _, err := h.sup.ResetConversation(context.Background(), "debug")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected a reset id")
	}
	if got := h.sup.Brain().TranscriptPath(); got == "" || !strings.Contains(got, id) {
		t.Fatalf("transcript path %s does not name reset id %s", got, id)
	}
}
