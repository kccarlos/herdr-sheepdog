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
// falls through to the orchestrator (or, with none, the roster fallback) like
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

// Only a real user input can confirm: a to_supervisor relay with
// nothing fresh behind it (the voice model's own initiative) neither
// asks nor confirms.
func TestResetPhraseNeedsShepherdInput(t *testing.T) {
	h := newHarness(t)
	res := h.relay("start over") // relayed with no hear() behind it
	if !noFreshWords(res) {
		t.Fatalf("result = %+v, want the voice-initiative silent result", res)
	}
	if got := h.log.ofType("supervisor.reset_confirm"); len(got) != 0 {
		t.Fatalf("must not have asked: %v", got)
	}
}

// ResetConversation (the API path) resets with no orchestrator attached: it
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

// An utterance heard just before a reset is not relayed by the daemon
// into the new conversation (relay.go; audit "Reset leftovers").
func TestResetDisarmsTheDaemonRelay(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("should not run", "")})
	h := newBrainHarness(t, m, relayAfter(30*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.hear("what's auth doing")

	if _, _, err := h.sup.ResetConversation(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	h.sup.replies.Wait()
	if n := h.log.count("supervisor.relay_by_daemon"); n != 0 {
		t.Errorf("relay_by_daemon = %d after a reset, want 0", n)
	}
}

// A user turn still detached from its tool call (awaitReply, past
// Config.ReplyWait) when "New conversation" happens must not deliver its
// reply as a push into the fresh conversation — it is marked dropped
// exactly like a stop would (audit "Reset leftovers").
func TestResetDropsADetachedUtteranceReply(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	m := braintest.NewModel(braintest.Step{Block: release, Text: braintest.Reply("The tests are green.", "")})
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)

	assertPending(t, h.say("how is auth doing"))
	waitFor(t, func() bool {
		h.sup.mu.Lock()
		defer h.sup.mu.Unlock()
		return h.sup.utterance != nil && h.sup.utterance.detached
	}, "the reply to detach")

	if _, _, err := h.sup.ResetConversation(context.Background(), "voice"); err != nil {
		t.Fatal(err)
	}
	h.sup.replies.Wait()

	if pushes, _ := utterancePushes(h); len(pushes) != 0 {
		t.Errorf("a reset-dropped reply was pushed: %v", pushes)
	}
	if n := h.log.count("brain.reply_delivered") + h.log.count("brain.reply_stale"); n != 0 {
		t.Errorf("%d reply events for a reset-dropped turn, want none", n)
	}
	h.sup.mu.Lock()
	defer h.sup.mu.Unlock()
	if h.sup.utterance != nil {
		t.Error("the slot outlived the reset")
	}
}

// No user input recorded before a reset may authorise a
// to_supervisor call in the fresh conversation: the voice relaying an
// old, unclaimed input's words on its own initiative after the reset
// gets nothing, exactly as it would with no input behind it at all
// (audit "Reset leftovers": the relay counters are bumped past every
// pre-reset input).
func TestResetPreventsPreResetInputFromAuthorising(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.hear("tell auth to run the tests") // recorded, never claimed by a relay

	if _, _, err := h.sup.ResetConversation(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}

	res := h.relay("tell auth to run the tests") // relayed with no hear() behind it
	if !noFreshWords(res) {
		t.Fatalf("result = %+v, want the voice-initiative silent result", res)
	}
	if h.log.count("supervisor.voice_initiative_ignored") != 1 {
		t.Errorf("voice_initiative_ignored = %d, want 1", h.log.count("supervisor.voice_initiative_ignored"))
	}
}

// A orchestrator's ResetConversation runs Brain.Reset and reports its id.
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
