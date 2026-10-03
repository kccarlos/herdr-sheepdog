package supervisor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Tier-3 bench 2026-10-03, finding 4 (safety): herdr reported Codex's
// "update available" menu as idle, the CEO's instruction was typed into
// it, and its trailing Enter chose "Update now": Codex ran its
// installer. The screen (codex-update-menu.screen.txt, the real Codex
// menu as a brain read_screen captured it on 2026-09-23) is now read
// first: nothing is typed, and the brain gets the fact.
func TestInstructionNotTypedIntoAMenu(t *testing.T) {
	for _, c := range []struct{ name, kind, screen, option string }{
		{"codex update menu", "codex", "codex-update-menu.screen.txt", `"Skip"`},
		{"unnumbered menu on an idle pane", "claude", "claude-trust-dialog.screen.txt", `"Yes, I trust this folder"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.addAgent("w5:p1", "auth", c.kind, herdr.StateIdle)
			h.srv.SetScreen("w5:p1", fixture(t, c.screen))
			_, err := brainHost{s: h.sup}.Prompt(context.Background(), "w5:p1", "Please run the shop tests and report what fails.")
			if !errors.Is(err, ErrMenuOnScreen) {
				t.Fatalf("err = %v, want ErrMenuOnScreen", err)
			}
			if msg := err.Error(); !strings.Contains(msg, "auth is showing a menu") || !strings.Contains(msg, c.option) || !strings.Contains(msg, "nothing was typed") {
				t.Errorf("fact = %q", msg)
			}
			if got := h.promptRequests(); len(got) != 0 {
				t.Errorf("agent.prompt = %v, want nothing typed", got)
			}
			if got := h.sendKeysRequests(); len(got) != 0 {
				t.Errorf("send_keys = %v", got)
			}
			if h.log.count("supervisor.instruction_blocked_by_menu") != 1 {
				t.Error("supervisor.instruction_blocked_by_menu not logged")
			}
			if _, ok := h.pending(); ok {
				t.Error("a pending action was left behind")
			}
		})
	}
}

// An idle pane at its input line is no menu: the instruction is typed.
func TestInstructionTypedAtTheInputLine(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "claude", herdr.StateIdle)
	h.srv.SetScreen("w5:p1", fixture(t, "claude-idle.screen.txt"))
	if _, err := (brainHost{s: h.sup}).Prompt(context.Background(), "w5:p1", "Run the tests."); err != nil {
		t.Fatal(err)
	}
	if got := h.promptRequests(); len(got) != 1 {
		t.Errorf("agent.prompt = %v, want the instruction", got)
	}
	if h.log.count("supervisor.instruction_blocked_by_menu") != 0 {
		t.Error("blocked at an input line")
	}
}
