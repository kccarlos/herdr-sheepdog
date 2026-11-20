package supervisor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// A question from a muted space must not take over the answer to a question
// the user was just told about: their "yes" belongs to what they heard.
func TestMutedQuestionDoesNotTakeOverAnAnnouncedOne(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.addAgent("w6:p1", "docs", "codex", herdr.StateWorking)
	w.mute(t)

	// docs, in a space that is not muted, asks first and is heard.
	w.block("w6:p1", "codex-approval-prompt.screen.txt")
	if got := w.pushTexts(); len(got) != 1 {
		t.Fatalf("docs's question was not announced: %v", got)
	}
	heard, ok := w.pending()
	if !ok || heard.PaneID != "w6:p1" {
		t.Fatalf("pending after docs asked = %+v", heard)
	}
	w.speak(w.pushTexts()[0])

	// auth, in the muted space, asks next, silently.
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	if got := w.pushTexts(); len(got) != 1 {
		t.Fatalf("the muted question was spoken: %v", got)
	}
	if qs := w.store.Questions(); len(qs) != 2 {
		t.Fatalf("both questions should be listed for the phone: %+v", qs)
	}

	// The user says yes to what they heard.
	w.say("yes")
	var targets []string
	for _, r := range w.sendKeysRequests() {
		targets = append(targets, r[0])
	}
	if len(targets) != 1 || targets[0] != "w6:p1" {
		t.Errorf("keys went to %v, want only the question the user heard (w6:p1)", targets)
	}
}

// keysTo lists the panes pane.send_keys went to.
func keysTo(w *muteWorld) []string {
	var out []string
	for _, r := range w.sendKeysRequests() {
		out = append(out, r[0])
	}
	return out
}

// A question nobody was told about is never what a spoken or typed "yes"
// answers, even when it is the only one open: the voice says so in plain
// words and sends nothing. A tap on the question's own id still works; the
// orchestrator may not answer it either.
func TestOnlyHeldQuestionIsNotAnsweredByAYes(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	if p, ok := w.pending(); ok {
		t.Fatalf("a held question took the pending slot: %+v", p)
	}
	qs := w.store.Questions()
	if len(qs) != 1 || !w.sup.heldQuestion(qs[0].ID) {
		t.Fatalf("questions = %+v, want the one held question", qs)
	}
	if got := w.pushTexts(); len(got) != 0 {
		t.Errorf("the muted question was spoken: %v", got)
	}

	for _, word := range []string{"yes", "approve it", "no"} {
		res := w.say(word)
		if !strings.Contains(res.Spoken, "Nothing is waiting for an answer here") || !strings.Contains(res.Spoken, "muted space") || !strings.Contains(res.Spoken, "phone") {
			t.Errorf("%q answered %q, want the plain words about the muted question", word, res.Spoken)
		}
	}
	if got := keysTo(w); len(got) != 0 {
		t.Fatalf("a bare answer sent keys to %v", got)
	}

	// The orchestrator's answer_prompt is refused too.
	_, err := brainHost{w.sup}.AnswerPrompt(context.Background(), "w5:p1", []string{"y"})
	if !errors.Is(err, brain.ErrNotPermitted) || !strings.Contains(err.Error(), "muted") {
		t.Errorf("AnswerPrompt = %v, want a refusal that names the mute", err)
	}
	if got := keysTo(w); len(got) != 0 {
		t.Fatalf("the orchestrator sent keys to %v", got)
	}

	// The user's own tap on the card still answers it.
	if _, err := w.sup.AnswerTap(context.Background(), w.store.Questions()[0].ID, 1); err != nil {
		t.Fatalf("tap: %v", err)
	}
	if got := keysTo(w); len(got) != 1 || got[0] != "w5:p1" {
		t.Errorf("tap keys went to %v, want w5:p1", got)
	}
}

// Unmuting the space announces the held question once; from then on it is
// the pending answer and a "yes" answers it.
func TestUnmuteAnnouncesHeldQuestionsSoTheyAreAnswerableByVoice(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	ctx := context.Background()
	if _, err := w.sup.SetSpaceMute(ctx, "auth", true, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	if _, ok := w.pending(); ok || len(w.pushTexts()) != 0 {
		t.Fatalf("pending %v, pushes %v: the muted question must be silent and held", ok, w.pushTexts())
	}
	if _, err := w.sup.SetSpaceMute(ctx, "auth", false, nil, ViaPhone); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p, ok := w.pending(); ok && p.PaneID == "w5:p1" && len(w.pushTexts()) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("held question not announced after unmute: pushes %v", w.pushTexts())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := w.log.count("pending.promoted"); n != 1 {
		t.Errorf("pending.promoted = %d, want 1", n)
	}
	w.speak(w.pushTexts()[0])
	w.say("yes")
	if got := keysTo(w); len(got) != 1 || got[0] != "w5:p1" {
		t.Errorf("after it was announced, yes sent keys to %v, want w5:p1", got)
	}
}

// A promise to be told when the agent blocks is kept even in a muted space,
// and the question, now announced, becomes the pending answer.
func TestHeldQuestionStillKeepsABlockedPromise(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.addAgent("w6:p1", "docs", "codex", herdr.StateWorking)
	w.mute(t)
	w.block("w6:p1", "codex-approval-prompt.screen.txt")
	if _, err := w.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it asks", Event: "blocked"}); err != nil {
		t.Fatal(err)
	}
	before := len(w.pushTexts())
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	got := w.pushTexts()
	if len(got) != before+1 || !strings.HasPrefix(got[len(got)-1], "As you asked:") {
		t.Fatalf("pushes = %v, want the kept promise spoken", got)
	}
	// The kept-promise line was spoken, so this question was heard and is
	// now the pending answer.
	if p, _ := w.pending(); p.PaneID != "w5:p1" {
		t.Errorf("pending = %+v, want the question that was just announced", p)
	}
	if n := len(w.fn.all()); n != 2 {
		t.Errorf("notifications = %d, want docs's approval and the kept promise", n)
	}
}
