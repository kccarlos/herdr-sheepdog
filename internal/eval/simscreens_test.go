package eval

import (
	"os"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Campaign D N6: the sim's decline screen kept the force-push menu above
// "Okay, I won't do that." and the input line, so the menu guard still
// saw a menu. Real Codex removes the overlay
// (testdata/herdr/codex-declined-esc.screen.txt).
func TestScreenAfterAnswerDropsTheMenu(t *testing.T) {
	b, err := os.ReadFile("../../testdata/eval/sim/screens/bench-codex-force-push.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := herdr.ParsePrompt(string(b)); !ok {
		t.Fatal("the fixture shows no menu")
	}
	for _, declined := range []bool{true, false} {
		got := ScreenAfterAnswer(string(b), "codex", declined)
		if _, ok := herdr.ParsePrompt(got); ok {
			t.Errorf("declined=%v: still a menu:\n%s", declined, got)
		}
		for _, gone := range []string{"1. Yes, proceed", "Press enter to confirm"} {
			if strings.Contains(got, gone) {
				t.Errorf("declined=%v: %q still on screen:\n%s", declined, gone, got)
			}
		}
		if !strings.Contains(got, "I squashed the last four commits") || !strings.HasSuffix(got, "› Ask Codex to do anything\n") {
			t.Errorf("declined=%v: lost the history or the input line:\n%s", declined, got)
		}
	}
	if got := ScreenAfterAnswer(string(b), "codex", true); !strings.Contains(got, "Okay, I won't do that.") {
		t.Errorf("no decline line:\n%s", got)
	}
}

// A Ctrl-C to a working worker leaves no running-turn line behind.
func TestScreenAfterInterrupt(t *testing.T) {
	b, err := os.ReadFile("../../testdata/eval/sim/screens/bench-claude-working.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := ScreenAfterInterrupt(string(b), "claude")
	if strings.Contains(strings.ToLower(got), "to interrupt") || !strings.Contains(got, "Interrupted") || !strings.HasSuffix(got, "❯ \n") {
		t.Errorf("screen:\n%s", got)
	}
}
