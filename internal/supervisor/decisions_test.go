package supervisor

import (
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// The announce turn of a finish is told whether any decision is open
// (baseline 2026-10-05 cluster 3): none when nothing waits for the user.
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

// Two prompts open at once reach the utterance turn as a fact, each with
// its worker's kind and command, and which share a kind (harness
// iteration 5: "approve the codex one" with cache-codex and etl-codex
// both asking "would you like to run the following command"). One open
// prompt adds nothing: the roster already has it.
func TestUtteranceSeesEveryOpenPrompt(t *testing.T) {
	m := braintest.NewModel()
	m.Default = &braintest.Step{Text: `{"spoken":"Which one?","clarify":true}`}
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.addAgent("w6:p1", "docs", "codex", herdr.StateIdle)
	h.addAgent("w7:p1", "notes", "claude", herdr.StateIdle)
	block := func(pane, cmd string) {
		if _, err := h.store.Update(pane, func(a *state.Agent) {
			a.State = herdr.StateBlocked
			a.LastBlock = &state.Block{Question: "would you like to run the following command", Command: cmd}
		}); err != nil {
			t.Fatal(err)
		}
	}
	block("w5:p1", "redis-cli -h cache.internal INFO stats")
	h.say("status")
	if text := m.Calls()[0].Text(); strings.Contains(text, "prompts are open at once") {
		t.Errorf("one open prompt, model saw:\n%s", text)
	}
	block("w6:p1", "rm -rf ./data")
	if err := h.store.SetPending(state.PendingAction{ID: "ap-2", PaneID: "w6:p1", Kind: state.PendingAnswerPrompt, Question: "would you like to run the following command", Command: "rm -rf ./data"}); err != nil {
		t.Fatal(err)
	}
	h.say("approve the codex one")
	text := m.Calls()[len(m.Calls())-1].Text()
	for _, want := range []string{"2 prompts are open at once, each a separate decision: docs (codex) is waiting", `asking to run "rm -rf ./data"`, `auth (codex) is waiting at a prompt for the user's answer ("would you like to run the following command"), asking to run "redis-cli -h cache.internal INFO stats"`, "docs and auth are both codex workers."} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q; model saw:\n%s", want, text)
		}
	}
	if strings.Contains(text, "(claude) is waiting") {
		t.Errorf("an idle agent listed as waiting:\n%s", text)
	}
	if len(h.sendKeysRequests()) != 0 {
		t.Errorf("keys sent: %v", h.sendKeysRequests())
	}
	// The voice's fact keeps its old shape: no command, no kind.
	if got := h.sup.DecisionsNow(); strings.Contains(got, "rm -rf") || strings.Contains(got, "(codex)") {
		t.Errorf("DecisionsNow = %q", got)
	}
}
