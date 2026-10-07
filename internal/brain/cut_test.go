package brain_test

import (
	"context"
	"strings"
	"testing"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// TestSaidCutReplacesTheAssistantLine is P5: the assistant's reply was
// ingested whole at the model's turnComplete, then the user barged in
// 4 s into it. The next turn sees only the heard part, marked cut off.
func TestSaidCutReplacesTheAssistantLine(t *testing.T) {
	h := newHarness(t, braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth.", "")}), nil)
	full := "one two three four five six seven eight nine ten"
	for _, w := range strings.Fields(full) {
		h.brain.Said(" "+w, false)
	}
	h.brain.TurnComplete()
	h.brain.SaidCut("one two three four", full, 4000)
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindUtterance, Question: "which one?"}); err != nil {
		t.Fatal(err)
	}
	text := h.model.Calls()[0].Text()
	if !strings.Contains(text, `Assistant said (cut off after 4.0 s): "one two three four"`) {
		t.Errorf("no cut line in:\n%s", text)
	}
	if strings.Contains(text, `Assistant said: "one two`) {
		t.Errorf("the full line is still in the window:\n%s", text)
	}
}

// TestSaidCutWhilePending: the cut arrives before the model's turn
// ended (the fragments are still pending): same result.
func TestSaidCutWhilePending(t *testing.T) {
	h := newHarness(t, braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth.", "")}), nil)
	h.brain.Said("Auth is running", false)
	h.brain.Said(" the tests", false)
	h.brain.SaidCut("", "Auth is running the tests", 0)
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindUtterance, Question: "what?"}); err != nil {
		t.Fatal(err)
	}
	text := h.model.Calls()[0].Text()
	if !strings.Contains(text, "Assistant started to speak but was cut off before the user heard any of it.") || strings.Contains(text, "Auth is running") {
		t.Errorf("window:\n%s", text)
	}
}

// TestFullReplyUnchanged: without a cut the line is the plain one.
func TestFullReplyUnchanged(t *testing.T) {
	h := newHarness(t, braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth.", "")}), nil)
	h.brain.Said("All of it.", true)
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindUtterance, Question: "ok"}); err != nil {
		t.Fatal(err)
	}
	if text := h.model.Calls()[0].Text(); !strings.Contains(text, `Assistant said: "All of it."`) || strings.Contains(text, "cut off") {
		t.Errorf("window:\n%s", text)
	}
}

// TestSaidCutWithTheNextReplyPending: the model started its next reply
// before the client's flushed report came back; the next reply's words
// stay pending and are not mistaken for the cut run.
func TestSaidCutWithTheNextReplyPending(t *testing.T) {
	h := newHarness(t, braintest.NewModel(braintest.Step{Text: braintest.Reply("Auth.", "")}), nil)
	full := "Auth is running the tests now"
	h.brain.Said(full, false)
	h.brain.TurnComplete()
	h.brain.Said("Sure, stopping", false)
	h.brain.SaidCut("Auth is running", full, 1500)
	h.brain.TurnComplete()
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindUtterance, Question: "ok"}); err != nil {
		t.Fatal(err)
	}
	text := h.model.Calls()[0].Text()
	if !strings.Contains(text, `Assistant said (cut off after 1.5 s): "Auth is running"`) ||
		strings.Contains(text, `Assistant said: "Auth is running the tests now"`) ||
		!strings.Contains(text, `Assistant said: "Sure, stopping"`) {
		t.Errorf("window:\n%s", text)
	}
}
