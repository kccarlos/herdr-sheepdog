package factory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func init() {
	// Default planResolveFacts in tests so unrelated plan tests never shell out.
	planResolveFacts = func(context.Context) flow.ResolveFacts {
		ok := flow.CLIFacts{Installed: true, SignedIn: true}
		return flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{"claude": ok, "codex": ok, "agy": ok}}
	}
}

func withPlanFacts(t *testing.T, facts flow.ResolveFacts) {
	t.Helper()
	prev := planResolveFacts
	planResolveFacts = func(context.Context) flow.ResolveFacts { return facts }
	t.Cleanup(func() { planResolveFacts = prev })
}

func TestPlanApplyLaunchChecks(t *testing.T) {
	newCoord := func(s flow.Settings) (*Engine, string, string) {
		eng := New(testPaths(t), nil)
		st := flow.State{Seq: 1, Settings: s, Project: flow.Project{ID: "p1", Started: true, RunTokenHash: tokenHash("tok")}}
		if err := SaveState(eng.Paths.State("p1"), st); err != nil {
			t.Fatal(err)
		}
		return eng, "p1", "tok"
	}

	// Unknown CLI in task.request is refused plan_invalid.
	eng, _, tok := newCoord(flow.DefaultSettings())
	err := eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`,"request":{"worker":[{"cli":"bogus","model":"m","effort":"medium"}]}}]}`))
	if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), `unknown cli "bogus"`) {
		t.Fatalf("unknown CLI: err = %v", err)
	}

	// Signed-out CLI with no signed-in fallback is refused plan_invalid with the table's reason.
	withPlanFacts(t, flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{
		"codex":  {Installed: true, SignedIn: false},
		"claude": {Installed: true, SignedIn: true},
	}})
	s := flow.DefaultSettings()
	s.Models = map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}}}
	eng, _, tok = newCoord(s)
	err = eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`}]}`))
	if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), "codex is not signed in") {
		t.Fatalf("signed-out worker: err = %v", err)
	}

	// Reviewer identical to worker with no other candidate is refused.
	s = flow.DefaultSettings()
	s.Models = map[string][]flow.Candidate{
		flow.RoleWorker:   {{CLI: "claude", Model: "sonnet", Effort: "medium"}},
		flow.RoleReviewer: {{CLI: "claude", Model: "sonnet", Effort: "high"}},
	}
	eng, _, tok = newCoord(s)
	err = eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`}]}`))
	if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), "the reviewer must differ from the worker") {
		t.Fatalf("same reviewer: err = %v", err)
	}

	// Valid task.request is accepted and persisted, and plan amend can update request.
	eng, id, tok := newCoord(flow.DefaultSettings())
	if err := eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`,"request":{"worker":[{"cli":"claude","model":"sonnet","effort":"high"}]}}]}`)); err != nil {
		t.Fatalf("valid plan apply: %v", err)
	}
	st, err := LoadState(eng.Paths.State(id))
	if err != nil || len(st.Tasks) != 1 || len(st.Tasks[0].Request[flow.RoleWorker]) != 1 || st.Tasks[0].Request[flow.RoleWorker][0].Effort != "high" {
		t.Fatalf("stored request = %+v, err = %v", st.Tasks, err)
	}
	// plan amend refuses an unlaunchable request on an existing task (all CLIs signed out).
	withPlanFacts(t, flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{"claude": {Installed: true, SignedIn: false}}})
	err = eng.Report(tok, "plan amend", json.RawMessage(`{"why":"switch model","tasks":[{"id":"T1","request":{"worker":[{"cli":"claude","model":"opus","effort":"medium"}]}}]}`))
	if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), "claude is not signed in") {
		t.Fatalf("amend unlaunchable: err = %v", err)
	}
	// plan amend accepts a valid request once signed in.
	withPlanFacts(t, flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{"claude": {Installed: true, SignedIn: true}}})
	if err := eng.Report(tok, "plan amend", json.RawMessage(`{"why":"switch model","tasks":[{"id":"T1","request":{"worker":[{"cli":"claude","model":"opus","effort":"low"}]}}]}`)); err != nil {
		t.Fatalf("valid plan amend: %v", err)
	}
	st, _ = LoadState(eng.Paths.State(id))
	if st.Tasks[0].Request[flow.RoleWorker][0].Model != "opus" || st.Tasks[0].Request[flow.RoleWorker][0].Effort != "low" {
		t.Fatalf("amended request = %+v", st.Tasks[0].Request)
	}
}
