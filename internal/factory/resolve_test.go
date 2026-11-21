package factory

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

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

	// The engine cannot launch a Codex worker until slice 5b.5.
	s := flow.DefaultSettings()
	s.Models = map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}}}
	eng, id, tok := newCoord(s)
	err = eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`}]}`))
	if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), "the engine cannot launch codex as worker yet") {
		t.Fatalf("unsupported worker: err = %v", err)
	}
	if _, err := os.Stat(eng.Paths.Worktree(id, "T1", 1)); !os.IsNotExist(err) {
		t.Fatalf("impossible plan allocated a worktree: %v", err)
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
	eng, id, tok = newCoord(flow.DefaultSettings())
	if err := eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`,"request":{"worker":[{"cli":"claude","model":"sonnet","effort":"high"}]}}]}`)); err != nil {
		t.Fatalf("valid plan apply: %v", err)
	}
	st, err := LoadState(eng.Paths.State(id))
	if err != nil || len(st.Tasks) != 1 || len(st.Tasks[0].Request[flow.RoleWorker]) != 1 || st.Tasks[0].Request[flow.RoleWorker][0].Effort != "high" {
		t.Fatalf("stored request = %+v, err = %v", st.Tasks, err)
	}
	// plan amend accepts a valid change to the request.
	if err := eng.Report(tok, "plan amend", json.RawMessage(`{"why":"switch model","tasks":[{"id":"T1","request":{"worker":[{"cli":"claude","model":"opus","effort":"low"}]}}]}`)); err != nil {
		t.Fatalf("valid plan amend: %v", err)
	}
	st, _ = LoadState(eng.Paths.State(id))
	if st.Tasks[0].Request[flow.RoleWorker][0].Model != "opus" || st.Tasks[0].Request[flow.RoleWorker][0].Effort != "low" {
		t.Fatalf("amended request = %+v", st.Tasks[0].Request)
	}
}

func TestPlanApplyDoesNotProbe(t *testing.T) {
	eng := newGitTestEngine(t, testPaths(t), nil)
	eng.probeLaunch = func(context.Context, []string) flow.ResolveFacts {
		t.Fatal("plan apply probed CLIs")
		return flow.ResolveFacts{}
	}
	st := flow.State{Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{ID: "p1", Started: true, RunTokenHash: tokenHash("tok")}}
	if err := SaveState(eng.Paths.State("p1"), st); err != nil {
		t.Fatal(err)
	}
	if err := eng.Report("tok", "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",`+testPlanJSON+`}]}`)); err != nil {
		t.Fatalf("default plan apply: %v", err)
	}
}

func TestProbeResolveFacts(t *testing.T) {
	var sawClaudeEnv atomic.Bool
	facts := probeResolveFacts(context.Background(), []string{"claude", "codex"}, func(_ context.Context, _ string, env []string, argv ...string) CommandOutput {
		if argv[0] == "claude" {
			sawClaudeEnv.Store(slices.Contains(env, "DISABLE_AUTOUPDATER=1"))
			return CommandOutput{Stdout: `{"loggedIn":true}`, Exit: 0}
		}
		return CommandOutput{Stdout: "Not logged in\n", Exit: 1}
	})
	if !sawClaudeEnv.Load() {
		t.Fatal("claude probe missing DISABLE_AUTOUPDATER=1")
	}
	if c := facts.CLIs["claude"]; !c.Installed || !c.SignedIn {
		t.Fatalf("claude = %+v", c)
	}
	if c := facts.CLIs["codex"]; !c.Installed || c.SignedIn {
		t.Fatalf("codex = %+v", c)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	facts = probeResolveFacts(ctx, []string{"claude", "codex"}, func(ctx context.Context, _ string, _ []string, argv ...string) CommandOutput {
		if argv[0] == "claude" {
			<-ctx.Done()
			return CommandOutput{Exit: -1, TimedOut: true}
		}
		return CommandOutput{Stderr: "unexpected error", Exit: 2}
	})
	if time.Since(start) > time.Second {
		t.Fatalf("probe took %v, want bounded by ctx", time.Since(start))
	}
	if len(facts.CLIs) != 0 {
		t.Fatalf("expected no settled probe results, got %+v", facts.CLIs)
	}
}

func TestPlanAmendKeepsStoredWorkerAndRequestOnlyNoWorkerNotice(t *testing.T) {
	l := newLanding(t).withTokens()
	// Give T1 a stored request.worker = claude/opus, project models.reviewer = claude/opus
	// (no distinct fallback), and an open attempt with a live RunID and a passing round.
	st := l.state()
	st.Settings.Models = map[string][]flow.Candidate{
		flow.RoleReviewer: {{CLI: "claude", Model: "opus", Effort: "medium"}},
	}
	st.Tasks[0].Request = map[string][]flow.Candidate{
		flow.RoleWorker: {{CLI: "claude", Model: "opus", Effort: "medium"}},
	}
	st.Tasks[0].Attempts[0].Run = &flow.RunRecord{CLI: "claude", Model: "opus"}
	st.Tasks[0].Attempts[0].Rounds = []flow.Round{{N: 1, Head: "h1", Verdict: flow.VerdictPass}}
	if err := SaveState(l.eng.Paths.State(l.id), st); err != nil {
		t.Fatal(err)
	}
	// Amend sets request.reviewer to the same (cli, model) as stored request.worker -> must refuse.
	err := l.eng.Report("tok-coord", "plan amend", json.RawMessage(`{"why":"same rev","tasks":[{"id":"T1","request":{"reviewer":[{"cli":"claude","model":"opus","effort":"high"}]}}]}`))
	if refusalCode(err) != "plan_invalid" || !strings.Contains(err.Error(), "the reviewer must differ from the worker") {
		t.Fatalf("amend reviewer==stored worker: err = %v, want reviewer-differs refusal", err)
	}
	// Amend sets request.reviewer to claude/haiku and adds a new task. It keeps the stored worker,
	// does not void rounds or message the live worker.
	sentBefore := len(sendsTo(t, l.srv.Requests(), l.worker))
	amendRaw := json.RawMessage(`{"why":"switch rev","tasks":[{"id":"T1","request":{"reviewer":[{"cli":"claude","model":"haiku","effort":"medium"}]}},{"id":"T2","estimate_lines":40,"title":"feat: two",` + testPlanJSON + `,"request":{"reviewer":[{"cli":"claude","model":"haiku","effort":"medium"}]}}]}`)
	if err := l.eng.Report("tok-coord", "plan amend", amendRaw); err != nil {
		t.Fatalf("amend valid reviewer: %v", err)
	}
	st = l.state()
	if len(st.Tasks[0].Request[flow.RoleWorker]) != 1 || st.Tasks[0].Request[flow.RoleWorker][0].Model != "opus" {
		t.Fatalf("stored worker lost after amend: %+v", st.Tasks[0].Request)
	}
	if st.Tasks[0].Attempts[0].Boundary != 0 {
		t.Fatalf("request-only amend voided rounds: Boundary = %d, want 0", st.Tasks[0].Attempts[0].Boundary)
	}
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != sentBefore {
		t.Fatalf("request-only amend messaged worker: %q", sends[sentBefore:])
	}
}

func TestLaunchWaitsBeforeWorktreeAndUsesProbedModels(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	srv := hctest.New(t)
	eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
	repo, _ := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	installFakeGH(t)
	if err := eng.Start(repo, "Resolve at launch", 0); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	load := func() flow.State {
		t.Helper()
		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := load()
	st.Settings.Models = map[string][]flow.Candidate{
		flow.RoleCoordinator: {{CLI: "claude", Model: "coordinator-model", Effort: "high"}},
		flow.RoleWorker:      {{CLI: "claude", Model: "worker-model", Effort: "low"}},
	}
	st.Project.RunTokenHash = tokenHash("tok")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	eng.Now = func() time.Time { return now }
	probes := 0
	account := flow.CLIFacts{Installed: true}
	eng.probeLaunch = func(context.Context, []string) flow.ResolveFacts {
		probes++
		return flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{"claude": account}}
	}
	plan := json.RawMessage(`{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: x",` + testPlanJSON + `, "request":{"worker":[{"cli":"claude","model":"unoffered","effort":"high"}]}}]}`)
	if err := eng.Report("tok", "plan apply", plan); err != nil {
		t.Fatalf("plan while signed out: %v", err)
	}
	checkWait := func(reason string) {
		t.Helper()
		eng.passProject(ctx, id, false)
		st := load()
		if tk := st.Tasks[0]; len(tk.Attempts) != 0 || len(tk.Acts) != 0 || tk.HoldWhy != reason {
			t.Fatalf("queued task = %+v, want wait %q before allocating or charging a retry", tk, reason)
		}
		if _, err := os.Stat(paths.Worktree(id, "T1", 1)); !os.IsNotExist(err) {
			t.Fatalf("worktree while waiting: %v", err)
		}
		if st.Project.RunID != "" || flow.LastAct(st.Project.Acts, flow.AskStartCoordinator) != nil {
			t.Fatalf("coordinator run or retry while waiting: %+v", st.Project)
		}
	}
	checkWait("claude is not signed in")
	if probes != 1 {
		t.Fatalf("N11 and J7 probed %d times, want one shared probe", probes)
	}
	now = now.Add(cliProbeTTL)
	account.SignedIn = true
	account.Models = map[string]bool{"coordinator-model": true, "worker-model": true, "opus": true}
	eng.passProject(ctx, id, false) // N11 and J7
	eng.passProject(ctx, id, false) // N23
	if probes != 2 {
		t.Fatalf("after refresh and N23: %d probes, want two total", probes)
	}
	st = load()
	for _, want := range []struct {
		run           *flow.RunRecord
		model, effort string
	}{
		{st.Project.Run, "coordinator-model", "high"},
		{flow.OpenAttempt(st.Tasks[0]).Run, "worker-model", "low"},
	} {
		if want.run == nil || want.run.CLI != "claude" || want.run.Model != want.model {
			t.Fatalf("persisted run = %+v, want claude/%s", want.run, want.model)
		}
		term, err := eng.HC.Get(ctx, want.run.Address)
		if err != nil || argValue(term.Argv, "--model") != want.model || argValue(term.Argv, "--effort") != want.effort {
			t.Fatalf("run %s argv = %q (%v), want %s/%s", want.run.Role, term.Argv, err, want.model, want.effort)
		}
	}
}

func TestReviewerLaunchFallsBackFromSignedOutCandidate(t *testing.T) {
	ctx := context.Background()
	_, eng, paths, id, _, _, runs, _ := codexSetup(t)
	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Settings.Models = map[string][]flow.Candidate{flow.RoleReviewer: {
		{CLI: "codex", Model: codexModel, Effort: "low"},
		{CLI: "claude", Model: "haiku", Effort: "high"},
	}}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.cliProbes = nil
	eng.probeLaunch = func(_ context.Context, clis []string) flow.ResolveFacts {
		if !slices.Equal(clis, []string{"codex", "claude"}) {
			t.Fatalf("probed %v, want Claude and Codex", clis)
		}
		return flow.ResolveFacts{CLIs: map[string]flow.CLIFacts{
			"claude": {Installed: true, SignedIn: true},
			"codex":  {Installed: true},
		}}
	}
	eng.passProject(ctx, id, false) // N26
	eng.passProject(ctx, id, false) // N29
	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	rev := flow.OpenAttempt(st.Tasks[0]).Review
	last := (*runs)[len(*runs)-1]
	if rev == nil || rev.Run.CLI != "claude" || rev.Run.Model != "haiku" || rev.Retries != 0 ||
		last.Labels["sheepdog.role"] != flow.RoleReviewer || argValue(last.Argv, "--model") != "haiku" || argValue(last.Argv, "--effort") != "high" {
		t.Fatalf("fallback review = %+v, terminal labels = %v", rev, last.Labels)
	}
}

func TestReviewerLadderUsesCLIWithModelAndExplicitList(t *testing.T) {
	s := flow.DefaultSettings()
	s.SecondReviewer = "codex"
	s.Models = map[string][]flow.Candidate{flow.RoleReviewer: {
		{CLI: "claude", Model: "shared", Effort: "medium"},
		{CLI: "codex", Model: "shared", Effort: "high"},
	}}
	facts := flow.ResolveFacts{Worker: flow.Candidate{CLI: "claude", Model: "worker"}}
	a := flow.Attempt{Rounds: []flow.Round{{Head: "h", Verdict: flow.VerdictPass, Reviewer: flow.Reviewer{CLI: "claude", Model: "shared"}}}}
	in, repeat, why := reviewChoice(flow.Task{}, s, a, "h", facts, nil)
	if why != "" || repeat != "" || in.CLI != "codex" || in.Model != "shared" {
		t.Fatalf("after Claude's pass: %+v repeat=%q reason=%q", in, repeat, why)
	}
	facts.Worker.Model = "shared"
	in, repeat, why = reviewChoice(flow.Task{}, s, a, "h", facts, []string{"codex/shared"})
	if why != "" || repeat == "" || in.CLI != "codex" || in.Model != "shared" {
		t.Fatalf("after Codex ran out of alternatives: %+v repeat=%q reason=%q", in, repeat, why)
	}
}
