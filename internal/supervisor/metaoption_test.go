package supervisor

import (
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// docs/dev/eval/eval-campaign-2026-10-03.md H2 (bench-askuser-meta-options):
// Claude Code's "Type something." reached the orchestrator as an ordinary
// option, and it was offered as a choice. The orchestrator now sees it flagged
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

// The phone card gets each option's description and the agent's
// recommendation as their own fields; the label stays the screen's, so
// the voice and the roster read what they read before.
func TestPendingOptionsCarryDescriptions(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p2", "docs", "claude", herdr.StateWorking)
	h.block("w5:p2", "claude-askuserquestion-zh.screen.txt")
	p, ok := h.pending()
	if !ok {
		t.Fatal("no pending prompt")
	}
	opts := PendingOptions(p)
	if len(opts) != 3 {
		t.Fatalf("options = %+v", opts)
	}
	first := opts[0]
	if first.Label != "点一下开始，再点发送 (推荐)" || first.Title != "点一下开始，再点发送" || !first.Recommended ||
		first.Description != "按住说话模式下，锁屏按钮改成：点一下开始说，再点一下发送。" {
		t.Errorf("first option = %+v", first)
	}
	if opts[1].Recommended || opts[1].Title != "" || opts[1].Description == "" {
		t.Errorf("second option = %+v", opts[1])
	}
}
