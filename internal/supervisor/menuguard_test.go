package supervisor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Tier-3 bench 2026-10-03, finding 4 (safety): herdr reported Codex's
// "update available" menu as idle, the user's instruction was typed into
// it, and its trailing Enter chose "Update now": Codex ran its
// installer. The screen (codex-update-menu.screen.txt, the real Codex
// menu as an orchestrator read_screen captured it on 2026-09-23) is now read
// first: nothing is typed, and the orchestrator gets the fact.
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

// #107's known limit: a menu on an idle pane got no pending, so "skip"
// by voice hit "no pending approval prompt". The send_keys path now
// revives a menu shown on an idle pane when the user's newest words
// are about it — they name an option or a word of its question — and the
// rail checks those words as for any prompt. Words about something else,
// or a working pane, still get nothing.
func TestIdleMenuAnswerableByVoice(t *testing.T) {
	screen := fixture(t, "codex-update-menu.screen.txt")
	prompt, ok := herdr.ParsePrompt(screen)
	if !ok || len(prompt.Options) < 2 {
		t.Fatalf("fixture does not parse: %+v", prompt)
	}
	skip := prompt.Options[1].Keys
	for _, c := range []struct {
		name   string
		state  herdr.State
		words  string
		sent   bool
		reason string
	}{
		{"skip by voice", herdr.StateIdle, "Skip it.", true, ""},
		{"by number", herdr.StateIdle, "option two", true, ""},
		{"words about something else", herdr.StateIdle, "Tell codex to run the tests.", false, "auth is showing a menu, but the user's last words are not about it"},
		{"working pane", herdr.StateWorking, "Skip it.", false, "has no pending approval prompt now"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.addAgent("w5:p1", "auth", "codex", c.state)
			h.srv.SetScreen("w5:p1", screen)
			h.clock.Advance(time.Second)
			h.hear(c.words)
			_, err := brainHost{s: h.sup}.AnswerPrompt(context.Background(), "w5:p1", skip)
			sent := len(h.sendKeysRequests()) == 1
			if sent != c.sent {
				t.Fatalf("sent = %v (err %v), want %v", sent, err, c.sent)
			}
			if c.sent {
				if err != nil {
					t.Fatal(err)
				}
				ev := h.log.ofType("pending.revived")
				if len(ev) != 1 || payload(ev[0])["state"] != string(herdr.StateIdle) {
					t.Errorf("pending.revived = %v", ev)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.reason) {
				t.Errorf("err = %v, want %q", err, c.reason)
			}
			if _, ok := h.pending(); ok {
				t.Error("a pending action was left behind")
			}
		})
	}
}

// Campaign D N6 (j2-deny, all 5 tier-1 runs): the user declined Codex's
// force push, then asked for a follow-up; the sim's decline screen kept
// the old menu, and the guard refused the instruction three times. Right
// after a decline the screen is read once more: a menu that is gone lets
// the instruction through, and one still there goes back to the orchestrator
// as the declined menu, possibly stale.
func TestMenuGuardAfterADecline(t *testing.T) {
	for _, c := range []struct {
		name    string
		redraw  bool
		blocked bool
	}{
		{"the screen redraws a moment later", true, false},
		{"the declined menu stays on screen", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			h.block("w5:p1", "codex-approval-prompt.screen.txt")
			p, ok := h.pending()
			if !ok || len(p.NoKeys) == 0 {
				t.Fatalf("pending = %+v", p)
			}
			h.hear("No, don't do that.")
			if _, err := (brainHost{s: h.sup}).AnswerPrompt(context.Background(), "w5:p1", p.NoKeys); err != nil {
				t.Fatal(err)
			}
			h.transition("w5:p1", herdr.StateBlocked, herdr.StateIdle)
			h.clock.Advance(3 * time.Second)
			if c.redraw {
				go func() {
					time.Sleep(declineRecheckDelay / 3)
					h.srv.SetScreen("w5:p1", fixture(t, "codex-declined-esc.screen.txt"))
				}()
			}
			_, err := brainHost{s: h.sup}.Prompt(context.Background(), "w5:p1", "Push to a new branch instead.")
			if blocked := errors.Is(err, ErrMenuOnScreen); blocked != c.blocked {
				t.Fatalf("err = %v, want blocked %v", err, c.blocked)
			}
			if c.blocked {
				if msg := err.Error(); !strings.Contains(msg, "the menu the user declined 3 s ago") || !strings.Contains(msg, "may be a stale picture") {
					t.Errorf("fact = %q", msg)
				}
				return
			}
			if got := h.promptRequests(); len(got) != 1 {
				t.Errorf("agent.prompt = %v, want the instruction", got)
			}
			if h.log.count("supervisor.menu_gone_after_decline") != 1 {
				t.Error("supervisor.menu_gone_after_decline not logged")
			}
		})
	}
}
