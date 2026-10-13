package brain_test

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

const sameNote = "Note from the daemon: this is the same as what you read of auth's screen at "

// readTwice runs two user turns that each read auth's screen and answer,
// with screen set before each read, and returns the text of the model
// call that saw the second read's result.
func readTwice(t *testing.T, first, second string, between func(*harness)) (string, *harness) {
	t.Helper()
	read := braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth"})}}
	h := newHarness(t, braintest.NewModel(
		read, braintest.Step{Text: braintest.Reply("It found a race in the checkout test.", "")},
		read, braintest.Step{Text: braintest.Reply("That is all it shows.", "")},
	), nil)
	h.host.SetScreen("w5:p1", first)
	if _, err := h.ask("what did auth find?", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.model.Calls()[1].Text(), "Note from the daemon: this is the same") {
		t.Errorf("the first read carries a note:\n%s", h.model.Calls()[1].Text())
	}
	if between != nil {
		between(h)
	}
	h.host.SetScreen("w5:p1", second)
	if _, err := h.ask("why?", ""); err != nil {
		t.Fatal(err)
	}
	return h.model.Calls()[3].Text(), h
}

const raceScreen = "› Find out why TestCheckoutTotals is flaky\n\n• The test races the price cache warm-up.\n  It fails about 1 run in 12.   \n\n✻ Idle (12s · 340 tokens) 14:02\n› █\n"

// A read whose content an earlier turn already got carries the daemon's
// note, after the worker-output end marker, naming when and that the
// answer was relayed; spinner, cursor, clock and whitespace do not count.
func TestReadUnchangedNote(t *testing.T) {
	again := strings.NewReplacer("12s", "47s", "340", "512", "14:02", "14:03", "█", "▌", "   \n", "\n").Replace(raceScreen)
	text, h := readTwice(t, raceScreen, again, nil)
	i := strings.Index(text, sameNote)
	if i < 0 {
		t.Fatalf("no note on the second read:\n%s", text)
	}
	if end := strings.LastIndex(text[:i], "<<<END_WORKER_OUTPUT"); end < 0 || strings.Contains(text[end:i], "<<<BEGIN_WORKER_OUTPUT") {
		t.Errorf("the note is not after the read's end marker:\n%s", text)
	}
	if !strings.Contains(text[i:], ", which you answered the user from; nothing in it has changed since.") {
		t.Errorf("the note does not say the read was relayed:\n%s", text[i:])
	}
	if ev := h.log.ofType("brain.read_unchanged"); len(ev) != 1 || payload(ev[0])["relayed"] != true {
		t.Errorf("read_unchanged events = %v", ev)
	}
}

// A shorter read of the same screen is the same content too.
func TestReadUnchangedContained(t *testing.T) {
	tail := raceScreen[strings.Index(raceScreen, "• The test"):]
	if text, _ := readTwice(t, raceScreen, tail, nil); !strings.Contains(text, sameNote) {
		t.Errorf("no note on a read contained in the earlier one:\n%s", text)
	}
}

// A real change, or a longer read with more on it, gets no note.
func TestReadChangedNoNote(t *testing.T) {
	changed := strings.Replace(raceScreen, "1 run in 12", "1 run in 9", 1)
	if text, _ := readTwice(t, raceScreen, changed, nil); strings.Contains(text, "Note from the daemon: this is the same") {
		t.Errorf("a changed screen carries the note:\n%s", text)
	}
	more := raceScreen + "• Recommended fix: wait for Ready() in the test setup.\n"
	if text, _ := readTwice(t, raceScreen, more, nil); strings.Contains(text, "Note from the daemon: this is the same") {
		t.Errorf("a longer screen carries the note:\n%s", text)
	}
}

// "New conversation" forgets the reads with the rest of the window.
func TestReadRecordReset(t *testing.T) {
	text, _ := readTwice(t, raceScreen, raceScreen, func(h *harness) {
		if _, err := h.brain.Reset(context.Background(), "app"); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(text, "Note from the daemon: this is the same") {
		t.Errorf("a read after a reset carries the note:\n%s", text)
	}
}

// The pruned stub keeps the fact.
func TestReadUnchangedStub(t *testing.T) {
	_, h := readTwice(t, raceScreen, raceScreen, nil)
	var stubs []string
	for _, m := range h.brain.Transcript().Messages() {
		if m.ToolResult != nil && m.ToolResult.Name == ToolReadScreen {
			stubs = append(stubs, m.ToolResult.Summary)
		}
	}
	if len(stubs) != 2 || strings.Contains(stubs[0], "unchanged since") || !strings.Contains(stubs[1], "; unchanged since ") {
		t.Errorf("stubs = %q", stubs)
	}
}

// A session read is compared by its last message: the tool counts may
// move while the report stays the same.
func TestSessionUnchangedNote(t *testing.T) {
	read := braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth", "source": "session"})}}
	h := newHarness(t, braintest.NewModel(
		read, braintest.Step{Text: braintest.Reply("It found seven glitches.", "")},
		read, braintest.Step{Text: braintest.Reply("That is all it said.", "")},
	), nil)
	h.host.SetSession("w5:p1", sessionRead())
	if _, err := h.ask("what exactly did it say?", ""); err != nil {
		t.Fatal(err)
	}
	more := sessionRead()
	more.Tools = map[string]int{"Read": 9, "Bash": 3}
	h.host.SetSession("w5:p1", more)
	if _, err := h.ask("and why?", ""); err != nil {
		t.Fatal(err)
	}
	if text := h.model.Calls()[3].Text(); !strings.Contains(text, "Note from the daemon: this is the same as what you read of auth's session's last message at ") {
		t.Errorf("no note on the unchanged session read:\n%s", text)
	}
}
