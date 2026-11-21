package factory

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func withPlanFacts(t *testing.T, facts flow.ResolveFacts) {
	t.Helper()
	prev := planResolveFacts
	planResolveFacts = func(context.Context, []string) flow.ResolveFacts { return facts }
	t.Cleanup(func() { planResolveFacts = prev })
}

func TestPlanApplyLaunchChecks(t *testing.T) {
	newCoord := func(s flow.Settings) (*Engine, string, string) {
		eng := newGitTestEngine(t, testPaths(t), nil)
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
	withPlanFacts(t, flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{"claude": {Installed: true, SignedIn: true}}})
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

func TestPlanApplyDefaultProjectDoesNotProbe(t *testing.T) {
	prev := planResolveFacts
	probed := false
	planResolveFacts = func(context.Context, []string) flow.ResolveFacts {
		probed = true
		return flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{"claude": {Installed: true, SignedIn: true}}}
	}
	t.Cleanup(func() { planResolveFacts = prev })

	eng := newGitTestEngine(t, testPaths(t), nil)
	st := flow.State{Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "p1", Started: true, RunTokenHash: tokenHash("tok")}}
	if err := SaveState(eng.Paths.State("p1"), st); err != nil {
		t.Fatal(err)
	}
	if err := eng.Report("tok", "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`}]}`)); err != nil {
		t.Fatalf("default plan apply: %v", err)
	}
	if probed {
		t.Fatal("default project plan apply probed CLIs, want no probe")
	}
}

func TestPlanApplyProbeTimeoutDoesNotRefuse(t *testing.T) {
	var calls int
	var probedCLIs []string
	prev := planResolveFacts
	planResolveFacts = func(_ context.Context, clis []string) flow.ResolveFacts {
		calls++
		probedCLIs = clis
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		return probeResolveFacts(ctx, clis, DoctorOptions{
			RunCommand: func(ctx context.Context, _ string, _ []string, _ ...string) CommandOutput {
				<-ctx.Done()
				return CommandOutput{Exit: -1, TimedOut: true}
			},
		})
	}
	t.Cleanup(func() { planResolveFacts = prev })

	eng := newGitTestEngine(t, testPaths(t), nil)
	st := flow.State{Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "p1", Started: true, RunTokenHash: tokenHash("tok")}}
	if err := SaveState(eng.Paths.State("p1"), st); err != nil {
		t.Fatal(err)
	}
	// Request names only codex (plus default reviewer claude); agy is never probed.
	raw := json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",` + testPlanJSON + `,"request":{"worker":[{"cli":"codex","model":"gpt-6.2-sol","effort":"medium"}]}}]}`)
	if err := eng.Report("tok", "plan apply", raw); err != nil {
		t.Fatalf("probe timeout refused plan: %v", err)
	}
	if calls != 1 || !slices.Equal(probedCLIs, []string{"claude", "codex"}) {
		t.Fatalf("probed calls=%d clis=%v, want 1 [claude codex]", calls, probedCLIs)
	}
}

func TestProbeResolveFacts(t *testing.T) {
	t.Run("signed in and DISABLE_AUTOUPDATER", func(t *testing.T) {
		var sawClaudeEnv atomic.Bool
		facts := probeResolveFacts(context.Background(), []string{"claude", "codex", "agy"}, DoctorOptions{
			RunCommand: func(_ context.Context, _ string, env []string, argv ...string) CommandOutput {
				switch argv[0] {
				case "claude":
					sawClaudeEnv.Store(slices.Contains(env, "DISABLE_AUTOUPDATER=1"))
					return CommandOutput{Stdout: `{"loggedIn":true}`, Exit: 0}
				case "codex":
					return CommandOutput{Stdout: "Logged in using ChatGPT\n", Exit: 0}
				case "agy":
					return CommandOutput{Stdout: "gemini-4-neon-medium  Gemini 4 Neon\n", Exit: 0}
				}
				return CommandOutput{Exit: 1}
			},
		})
		if !sawClaudeEnv.Load() {
			t.Fatal("claude probe missing DISABLE_AUTOUPDATER=1")
		}
		if c := facts.CLIs["claude"]; !c.Installed || !c.SignedIn || c.Unknown {
			t.Fatalf("claude = %+v", c)
		}
		if c := facts.CLIs["codex"]; !c.Installed || !c.SignedIn || c.Unknown {
			t.Fatalf("codex = %+v", c)
		}
		if c := facts.CLIs["agy"]; !c.Installed || !c.SignedIn || !c.Models["gemini-4-neon-medium"] {
			t.Fatalf("agy = %+v", c)
		}
	})

	t.Run("signed out and not installed", func(t *testing.T) {
		facts := probeResolveFacts(context.Background(), []string{"claude", "codex", "agy"}, DoctorOptions{
			RunCommand: func(_ context.Context, _ string, _ []string, argv ...string) CommandOutput {
				switch argv[0] {
				case "claude":
					return CommandOutput{Stdout: `{"loggedIn":false}`, Exit: 1}
				case "codex":
					return CommandOutput{Stdout: "Not logged in\n", Exit: 1}
				case "agy":
					return CommandOutput{Stderr: "Error: Please sign in\n", Exit: 1}
				}
				return CommandOutput{Exit: 127}
			},
		})
		for _, cli := range []string{"claude", "codex", "agy"} {
			if c := facts.CLIs[cli]; !c.Installed || c.SignedIn || c.Unknown {
				t.Fatalf("signed-out %s = %+v", cli, c)
			}
		}
		missing := probeResolveFacts(context.Background(), []string{"claude", "codex"}, DoctorOptions{
			RunCommand: func(_ context.Context, _ string, _ []string, argv ...string) CommandOutput {
				if argv[0] == "claude" {
					return CommandOutput{Exit: 127}
				}
				return CommandOutput{Exit: -1, Err: exec.ErrNotFound}
			},
		})
		for _, cli := range []string{"claude", "codex"} {
			if c := missing.CLIs[cli]; c.Installed || c.Unknown {
				t.Fatalf("not-installed %s = %+v", cli, c)
			}
		}
	})

	t.Run("hang unreadable exit and empty model list are unknown", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		facts := probeResolveFacts(ctx, []string{"claude", "codex", "agy"}, DoctorOptions{
			RunCommand: func(ctx context.Context, _ string, _ []string, argv ...string) CommandOutput {
				switch argv[0] {
				case "claude":
					<-ctx.Done()
					return CommandOutput{Exit: -1, TimedOut: true}
				case "codex":
					return CommandOutput{Stderr: "unexpected error", Exit: 2}
				case "agy":
					return CommandOutput{Stdout: "", Exit: 0}
				}
				return CommandOutput{Exit: 1}
			},
		})
		if time.Since(start) > time.Second {
			t.Fatalf("probe took %v, want bounded by ctx", time.Since(start))
		}
		for _, cli := range []string{"claude", "codex", "agy"} {
			if c := facts.CLIs[cli]; !c.Unknown {
				t.Fatalf("unknown %s = %+v, want Unknown=true", cli, c)
			}
		}
	})
}

func TestPlanAmendKeepsStoredWorkerAndRequestOnlyNoWorkerNotice(t *testing.T) {
	withPlanFacts(t, flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{
		"claude": {Installed: true, SignedIn: true},
		"codex":  {Installed: true, SignedIn: true},
	}})
	l := newLanding(t).withTokens()
	// Give T1 a stored request.worker = codex/gpt-6.2-sol, project models.reviewer = codex/gpt-6.2-sol
	// (no fallback to claude), and an open attempt with a live RunID and a passing round.
	st := l.state()
	st.Settings.Models = map[string][]flow.Candidate{
		flow.RoleReviewer: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}},
	}
	st.Tasks[0].Request = map[string][]flow.Candidate{
		flow.RoleWorker: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}},
	}
	st.Tasks[0].Attempts[0].Rounds = []flow.Round{{N: 1, Head: "h1", Verdict: flow.VerdictPass}}
	if err := SaveState(l.eng.Paths.State(l.id), st); err != nil {
		t.Fatal(err)
	}
	// Amend sets request.reviewer to the same (cli, model) as stored request.worker -> must refuse.
	err := l.eng.Report("tok-coord", "plan amend", json.RawMessage(`{"why":"same rev","tasks":[{"id":"T1","request":{"reviewer":[{"cli":"codex","model":"gpt-6.2-sol","effort":"high"}]}}]}`))
	if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), "the reviewer must differ from the worker") {
		t.Fatalf("amend reviewer==stored worker: err = %v, want reviewer-differs refusal", err)
	}
	// Amend sets request.reviewer to claude/opus and adds a new task -> probes only once, keeps stored worker,
	// does not void rounds or message the live worker.
	var probeCalls int
	prev := planResolveFacts
	planResolveFacts = func(_ context.Context, _ []string) flow.ResolveFacts {
		probeCalls++
		return flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{
			"claude": {Installed: true, SignedIn: true},
			"codex":  {Installed: true, SignedIn: true},
		}}
	}
	t.Cleanup(func() { planResolveFacts = prev })
	sentBefore := len(sendsTo(t, l.srv.Requests(), l.worker))
	amendRaw := json.RawMessage(`{"why":"switch rev","tasks":[{"id":"T1","request":{"reviewer":[{"cli":"claude","model":"opus","effort":"medium"}]}},{"id":"T2","estimate_lines":40,"title":"feat: two",` + testPlanJSON + `,"request":{"reviewer":[{"cli":"claude","model":"opus","effort":"medium"}]}}]}`)
	if err := l.eng.Report("tok-coord", "plan amend", amendRaw); err != nil {
		t.Fatalf("amend valid reviewer: %v", err)
	}
	if probeCalls != 1 {
		t.Fatalf("amend probed %d times, want 1", probeCalls)
	}
	st = l.state()
	if len(st.Tasks[0].Request[flow.RoleWorker]) != 1 || st.Tasks[0].Request[flow.RoleWorker][0].CLI != "codex" {
		t.Fatalf("stored worker lost after amend: %+v", st.Tasks[0].Request)
	}
	if st.Tasks[0].Attempts[0].Boundary != 0 {
		t.Fatalf("request-only amend voided rounds: Boundary = %d, want 0", st.Tasks[0].Attempts[0].Boundary)
	}
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != sentBefore {
		t.Fatalf("request-only amend messaged worker: %q", sends[sentBefore:])
	}
}

func TestCheckTasksLaunchUnprobedCLIIsUnknown(t *testing.T) {
	s := flow.DefaultSettings()
	s.Models = map[string][]flow.Candidate{
		flow.RoleWorker: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}},
	}
	facts := flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{
		"claude": {Installed: true, SignedIn: true},
	}}
	if fails := checkTasksLaunch([]flow.Task{{ID: "T1"}}, s, facts); len(fails) > 0 {
		t.Fatalf("unprobed codex refused plan: %v", fails)
	}
	if _, probed := facts.CLIs["codex"]; probed {
		t.Fatal("checkTasksLaunch mutated caller facts.CLIs")
	}
}
