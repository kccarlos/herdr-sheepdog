package supervisor

import (
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// The announce turn of a finish is told whether any decision is open
// (baseline 2026-10-05 cluster 3): none when nothing waits for the CEO.
func TestAnnounceSaysNoDecisionOpen(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("Auth finished.", "idle", "", "", false)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.touch("w5:p1")
	doneAuth(h)
	if text := m.Calls()[0].Text(); !strings.Contains(text, "Decisions open right now: none. No agent is waiting at a prompt for the user's answer.") {
		t.Errorf("model saw:\n%s", text)
	}
	if got := h.sup.DecisionsNow(); !strings.HasPrefix(got, "none:") {
		t.Errorf("DecisionsNow = %q", got)
	}
}

// Another agent waiting at a prompt is named, with its question, once
// whether the supervisor holds its pending prompt or only the pane is
// blocked.
func TestDecisionsNowNamesWaitingAgents(t *testing.T) {
	h := newBrainHarness(t, braintest.NewModel())
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)
	if _, err := h.store.Update("w6:p1", func(a *state.Agent) {
		a.State = herdr.StateBlocked
		a.LastBlock = &state.Block{Question: "whether it can delete the old docs"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetPending(state.PendingAction{ID: "ap-1", PaneID: "w6:p1", Kind: state.PendingAnswerPrompt, Question: "whether it can delete the old docs"}); err != nil {
		t.Fatal(err)
	}
	got := h.sup.DecisionsNow()
	if !strings.HasPrefix(got, "yes: ") || strings.Count(got, "waiting at a prompt") != 1 || !strings.Contains(got, "delete the old docs") || strings.Contains(got, "auth") {
		t.Errorf("DecisionsNow = %q", got)
	}
	if line := h.sup.decisionsLine(); !strings.HasPrefix(line, "Decisions open right now: ") || strings.Contains(line, "none") {
		t.Errorf("decisionsLine = %q", line)
	}
}
