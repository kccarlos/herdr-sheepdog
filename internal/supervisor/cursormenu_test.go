package supervisor

import (
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Claude Code 2.1.288's folder trust dialog has no numbers (tier-3 bench
// 2026-10-03, finding 1): it used to be read as a block with no question,
// no pending was created, and the rail refused the brain's correct
// ["down", "enter"] ("bench-claude has no pending approval prompt now").
// Now the unnumbered menu is a prompt: the CEO's "yes" lets the brain's
// navigation keys through the rail.
func TestTrustDialogAnsweredByBrainKeys(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"down", "enter"}})}},
		braintest.Step{Text: braintest.Reply("Told Claude to trust the folder.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)
	h.block("w5:p1", "claude-trust-dialog.screen.txt")
	p, ok := h.pending()
	if !ok || len(p.Options) != 2 || p.Options[1].Label != "Yes, I trust this folder" {
		t.Fatalf("pending = %+v, %v", p, ok)
	}
	h.say("yes, trust that folder")
	if got := h.sendKeysRequests(); len(got) != 1 || got[0] != [2]string{"w5:p1", "down enter"} {
		t.Errorf("keys = %v, want down enter", got)
	}
	if n := h.log.count("brain.rail_blocked"); n != 0 {
		t.Errorf("rail_blocked = %d", n)
	}
}

// The fast path maps a bare "yes" on the trust dialog to its yes row,
// one down from the cursor; "no" to the cursor's own row.
func TestTrustDialogFastPath(t *testing.T) {
	for _, c := range []struct{ say, keys string }{{"yes", "down enter"}, {"no", "enter"}} {
		t.Run(c.say, func(t *testing.T) {
			h := newHarness(t)
			h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)
			h.block("w5:p1", "claude-trust-dialog.screen.txt")
			h.say(c.say)
			if got := h.sendKeysRequests(); len(got) != 1 || got[0][1] != c.keys {
				t.Errorf("keys = %v, want %q", got, c.keys)
			}
		})
	}
}
