package supervisor

import (
	"strings"
	"testing"

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

// With nothing else pending a muted question is the pending one as always:
// the card and a tap work, and nothing is held.
func TestMutedQuestionIsPendingWhenNothingElseIs(t *testing.T) {
	w := newMuteWorld(t, nil, Quiet)
	w.mute(t)
	w.block("w5:p1", "codex-approval-prompt.screen.txt")
	p, ok := w.pending()
	if !ok || p.PaneID != "w5:p1" || w.sup.heldQuestion(p.ID) {
		t.Fatalf("pending = %+v (held %v)", p, w.sup.heldQuestion(p.ID))
	}
	if got := w.pushTexts(); len(got) != 0 {
		t.Errorf("the muted question was spoken: %v", got)
	}
}

// A promise to be told when the agent blocks is kept even when the question
// is held back from the pending slot, and the pending answer stays the one
// the user heard.
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
	if p, _ := w.pending(); p.PaneID != "w6:p1" {
		t.Errorf("the held question took over the pending answer: %+v", p)
	}
	if n := len(w.fn.all()); n != 2 {
		t.Errorf("notifications = %d, want docs's approval and the kept promise", n)
	}
}
