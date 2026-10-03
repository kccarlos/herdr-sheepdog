package supervisor

import (
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// docs/eval-campaign-2026-10-03.md H2 (bench-askuser-meta-options):
// Claude Code's "Type something." reached the brain as an ordinary
// option, and it was offered as a choice. The brain now sees it flagged
// as a free-text entry; the phone card still leaves it out.
func TestMetaOptionReachesTheBrainFlagged(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("It asks whether to proceed.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "docs", "claude", herdr.StateWorking)
	h.block("w5:p1", "claude-askuserquestion.screen.txt")
	p, ok := h.pending()
	if !ok {
		t.Fatal("no pending prompt")
	}
	var info brain.AgentInfo
	for _, a := range (brainHost{s: h.sup}).Agents() {
		if a.PaneID == "w5:p1" {
			info = a
		}
	}
	if info.Pending == nil || len(info.Pending.Options) != 3 || !info.Pending.Options[2].Meta || info.Pending.Options[0].Meta {
		t.Fatalf("brain pending = %+v", info.Pending)
	}
	for _, o := range PendingOptions(p) {
		if o.Label == "Type something." {
			t.Errorf("the phone card offers the free-text entry: %+v", o)
		}
	}
	h.say("what does docs need from me")
	calls := m.Calls()
	if len(calls) == 0 {
		t.Fatal("no brain call")
	}
	roster := calls[len(calls)-1].System()
	if !strings.Contains(roster, `"Type something." -> keys [3] (a free-text entry, not an answer: never offer it)`) {
		t.Errorf("the roster does not flag the free-text entry:\n%s", roster)
	}
	if strings.Contains(roster, `"Yes, proceed" -> keys [1] (a free-text`) {
		t.Error("a real answer is flagged")
	}
}
