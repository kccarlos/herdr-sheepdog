package factory

// TDD step 1: agy adapter for worker, coordinator and reviewer (design.md 9.2,
// 7.3, spike S0.1, KB-QTA-4, KB-LCH-2, KB-LCH-4, KB-LCH-26).

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

const (
	agyCoordModel    = "gemini-4-neon-medium"
	agyWorkerModel   = "gemini-4-carbon-medium"
	agyReviewModel   = "gemini-4-hydrogen-high"
	agyNonGemini     = "claude-sonnet-5-5-medium"
	agyHistoryConvID = "70f9969a-d48c-40e4-8f30-5d6e6d0ffe7b"
	agyModelsList    = "gemini-4-carbon-medium\tGemini 4 Carbon (Medium)\n" +
		"gemini-4-neon-medium\tGemini 4 Neon (Medium)\n" +
		"gemini-4-hydrogen-high\tGemini 4 Hydrogen (High)\n" +
		"claude-sonnet-5-5-medium\tClaude Sonnet 5.5 (Medium)\n"
)

// makeDirOutsideTmp creates a temporary directory outside os.TempDir() so that
// tests isolating $HOME never trip the os.TempDir() fixture guard.
func makeDirOutsideTmp(t *testing.T, prefix string) string {
	t.Helper()
	tmpReal, _ := filepath.EvalSymlinks(os.TempDir())
	for _, base := range []string{"/tmp", filepath.Dir(filepath.Clean(tmpReal))} {
		baseReal, err := filepath.EvalSymlinks(base)
		if err != nil {
			continue
		}
		rel, rerr := filepath.Rel(tmpReal, baseReal)
		if rerr == nil && (rel == "." || !strings.HasPrefix(rel, "..")) {
			continue
		}
		dir, err := os.MkdirTemp(baseReal, prefix)
		if err == nil {
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return dir
		}
	}
	dir := t.TempDir()
	return dir
}

type agyRig struct {
	t          *testing.T
	srv        *hctest.Server
	cl         *hc.Client
	eng        *Engine
	paths      Paths
	id         string
	repo       string
	gh         string
	runs       *[]hc.RunParams
	now        *time.Time
	logs       *bytes.Buffer
	daemonHome string
	hcHome     string
	shellExecs atomic.Int32
	revMu      sync.Mutex
	revToks    map[string]string
}

func (r *agyRig) reviewerToken(runID string) string {
	r.revMu.Lock()
	defer r.revMu.Unlock()
	return r.revToks[runID]
}

func newAgyRig(t *testing.T, models map[string][]flow.Candidate) *agyRig {
	t.Helper()
	daemonHome := makeDirOutsideTmp(t, "sd-agy-daemon-home-")
	hcHome := makeDirOutsideTmp(t, "sd-agy-hc-home-")
	t.Setenv("HOME", daemonHome)

	r := &agyRig{
		t:          t,
		srv:        hctest.New(t),
		paths:      testPaths(t),
		runs:       &[]hc.RunParams{},
		logs:       &bytes.Buffer{},
		daemonHome: daemonHome,
		hcHome:     hcHome,
		revToks:    map[string]string{},
	}
	now := time.Now().UTC().Truncate(time.Second)
	r.now = &now
	r.srv.OnRun = func(term hc.Terminal, p hc.RunParams) {
		term.CreatedMS = uint64(r.now.UnixMilli())
		r.srv.Add(term)
		*r.runs = append(*r.runs, p)
		if p.Labels["sheepdog.role"] == flow.RoleReviewer {
			r.revMu.Lock()
			r.revToks[term.Address] = p.Env["SHEEPDOG_RUN_TOKEN"]
			r.revMu.Unlock()
		}
	}
	r.srv.OnExec = r.execInHC
	r.cl = hc.NewClient(r.srv.Path)
	r.repo, _ = initGitRepoWithRemote(t)
	githubOrigin(t, r.repo)
	r.gh = installFakeGH(t)
	r.eng = New(r.paths, r.cl)
	r.eng.StartCheck = noopStartCheck
	r.eng.Log = logging.NewJSONLLogger(r.logs)
	r.eng.Now = func() time.Time { return *r.now }
	if err := r.eng.Start(r.repo, "Ship agy support", 2); err != nil {
		t.Fatal(err)
	}
	r.id = r.eng.projectDirs()[0].Name()
	r.edit(func(s *flow.State) { s.Settings.PlanReviewRounds = 0 })
	if models != nil {
		r.edit(func(s *flow.State) { s.Settings.Models = models })
	}
	return r
}

func (r *agyRig) execInHC(p hc.ExecParams) (hc.ExecResult, error) {
	zero := 0
	if len(p.Argv) == 0 {
		return hc.ExecResult{Code: &zero}, nil
	}
	if p.Argv[0] == "git" || p.Argv[0] == "gh" {
		return hctest.GitExec(p)
	}
	// Handle hcExecRunner probes ("sh", "-c", cleanExecScript, "sh", <cli>, ...).
	inner := p.Argv
	if len(p.Argv) >= 5 && p.Argv[0] == "sh" && p.Argv[1] == "-c" && p.Argv[3] == "sh" {
		inner = p.Argv[4:]
	}
	switch inner[0] {
	case "claude":
		return hc.ExecResult{Code: &zero, Stdout: []byte(`{"loggedIn":true}`)}, nil
	case "codex":
		return hc.ExecResult{Code: &zero, Stdout: []byte("Logged in using ChatGPT\n")}, nil
	case "agy":
		return hc.ExecResult{Code: &zero, Stdout: []byte(agyModelsList)}, nil
	case "codexbar":
		notFound := 127
		return hc.ExecResult{Code: &notFound, Stderr: []byte("codexbar: not found")}, nil
	}
	r.shellExecs.Add(1)
	timeout := 5 * time.Second
	if p.Timeout != nil && *p.Timeout > 0 {
		timeout = time.Duration(*p.Timeout * float64(time.Second))
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.Argv[0], p.Argv[1:]...)
	cmd.Dir = cmp.Or(p.Cwd, os.TempDir())
	cmd.WaitDelay = time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + r.hcHome,
		"TMPDIR=" + os.TempDir(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	for k, v := range p.Env {
		if k != "HOME" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := hc.ExecResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), TimedOut: ctx.Err() != nil}
	if cmd.ProcessState == nil {
		return res, err
	}
	code := cmd.ProcessState.ExitCode()
	res.Code = &code
	return res, nil
}

func (r *agyRig) pass()              { r.eng.passProject(context.Background(), r.id, false) }
func (r *agyRig) last() hc.RunParams { return (*r.runs)[len(*r.runs)-1] }
func (r *agyRig) state() flow.State {
	r.t.Helper()
	st, err := LoadState(r.paths.State(r.id))
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}
func (r *agyRig) edit(fn func(*flow.State)) {
	r.t.Helper()
	st := r.state()
	fn(&st)
	if err := SaveState(r.paths.State(r.id), st); err != nil {
		r.t.Fatal(err)
	}
}

func assertAgyFreshArgv(t *testing.T, role, model string, p hc.RunParams, wantPromptParts ...string) {
	t.Helper()
	wantHead := []string{"env", "-u", "GEMINI_API_KEY", "agy", "--model", model, "--dangerously-skip-permissions"}
	if len(p.Argv) != len(wantHead)+1 || !slices.Equal(p.Argv[:len(wantHead)], wantHead) {
		t.Fatalf("%s argv = %q, want %q followed by -i=<prompt>", role, p.Argv, wantHead)
	}
	promptArg := p.Argv[len(wantHead)]
	prompt, ok := strings.CutPrefix(promptArg, "-i=")
	if !ok || strings.TrimSpace(prompt) == "" {
		t.Fatalf("%s last argv element = %q, want non-empty -i=<prompt>", role, promptArg)
	}
	for _, part := range wantPromptParts {
		if !strings.Contains(prompt, part) {
			t.Errorf("%s -i= prompt lacks %q:\n%s", role, part, prompt)
		}
	}
	assertNoGeminiAPIKey(t, role, p)
}

func assertNoGeminiAPIKey(t *testing.T, role string, p hc.RunParams) {
	t.Helper()
	for k, v := range p.Env {
		if strings.Contains(k, "GEMINI_API_KEY") || strings.Contains(v, "paid-key-must-not-leak") {
			t.Errorf("%s env leaked GEMINI_API_KEY: %s=%q", role, k, v)
		}
	}
	for i, a := range p.Argv {
		if i == 2 && p.Argv[0] == "env" && p.Argv[1] == "-u" && a == "GEMINI_API_KEY" {
			continue
		}
		if strings.Contains(a, "GEMINI_API_KEY") || strings.Contains(a, "paid-key-must-not-leak") {
			t.Errorf("%s argv[%d] leaked GEMINI_API_KEY: %q", role, i, a)
		}
	}
}

// TestAgyLaunchCapabilityAndRoleArgv checks Decision 1 (KB-LCH-2, KB-LCH-4,
// KB-LCH-26): launchCapability accepts agy for worker, coordinator and
// reviewer, and each start executor builds `env -u GEMINI_API_KEY agy --model
// <model> --mode auto -i=<first prompt>` with no GEMINI_API_KEY in env or argv.
func TestAgyLaunchCapabilityAndRoleArgv(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "paid-key-must-not-leak")
	cand := flow.Candidate{CLI: "agy", Model: agyWorkerModel, Effort: "medium"}
	for _, role := range []string{flow.RoleWorker, flow.RoleCoordinator, flow.RoleReviewer} {
		if got := launchCapability(role)(cand); got.Why != "" {
			t.Errorf("launchCapability(%s)(agy) = %q, want empty", role, got.Why)
		}
	}

	r := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleCoordinator: {{CLI: "agy", Model: agyCoordModel, Effort: "medium"}},
		flow.RoleWorker:      {{CLI: "agy", Model: agyWorkerModel, Effort: "medium"}},
		flow.RoleReviewer: {
			{CLI: "agy", Model: agyReviewModel, Effort: "high"},
			{CLI: "claude", Model: "opus", Effort: "medium"},
		},
	})

	// 1. Coordinator launch (J7).
	r.pass()
	if len(*r.runs) != 1 {
		t.Fatalf("coordinator did not launch on agy: %d runs (state=%+v)", len(*r.runs), r.state().Project)
	}
	coordParams := r.last()
	assertAgyFreshArgv(t, "coordinator", agyCoordModel, coordParams, "Ship agy support", "plan apply")

	// 2. Worker launch (N11 -> N23).
	coordTok := coordParams.Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: agy hello","brief":"Add hello.go.",` + testScopeJSON + `,"outcomes":[{"text":"hello exists","proved_by":"manual: hello.go"}]}]}`
	if err := r.eng.Report(coordTok, "plan apply", []byte(plan)); err != nil {
		t.Fatalf("plan apply with agy roles: %v", err)
	}
	r.pass() // N11 worktree
	r.pass() // N23 worker
	if len(*r.runs) != 2 {
		t.Fatalf("worker did not launch on agy: %d runs", len(*r.runs))
	}
	workerParams := r.last()
	wt := r.state().Tasks[0].Attempts[0].Worktree
	assertAgyFreshArgv(t, "worker", agyWorkerModel, workerParams, "T1", "Add hello.go.", "factory report ready", wt)

	// 3. Reviewer launch (ready -> N26 -> N29).
	if err := os.WriteFile(filepath.Join(wt, "hello.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "hello.go")
	gitIn(t, wt, "commit", "-m", "feat: add hello")
	c1 := gitIn(t, wt, "rev-parse", "HEAD")
	writePRJSON(t, r.gh, "pr.json", c1, greenRollup)
	workerTok := workerParams.Env["SHEEPDOG_RUN_TOKEN"]
	if err := r.eng.Report(workerTok, "ready", []byte(`{"head":"`+c1+`","proofs":[{"outcome":"hello exists","artifact":"hello.go"}],"touched":"hello.go","skipped":""}`)); err != nil {
		t.Fatalf("ready: %v", err)
	}
	r.pass() // N26 pushes and opens PR
	writePRJSON(t, r.gh, "view.json", c1, greenRollup)
	r.pass() // N29 starts round 1 reviewer on agy
	if len(*r.runs) != 3 {
		t.Fatalf("reviewer did not launch on agy: %d runs", len(*r.runs))
	}
	assertAgyFreshArgv(t, "reviewer", agyReviewModel, r.last(), "Review round 1", "factory report review")
}

// TestAgyObserveRunStatusesAndEarlyAuth checks Decision 2 (spike S0.1):
// ObserveRun maps captured antigravity hc statuses (working, idle, done with
// msg, blocked/permission with command in msg) to their run states, treats a
// blocked/auth within the initial start window as starting (not blocked and
// not signed out), and maps a later blocked/auth as auth (signed out).
func TestAgyObserveRunStatusesAndEarlyAuth(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)

	rawTerm, err := os.ReadFile(filepath.Join("testdata", "agy-working.get.json"))
	if err != nil {
		t.Fatal(err)
	}
	rawScreen, err := os.ReadFile(filepath.Join("testdata", "agy-working.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var captured hc.Terminal
	if err := json.Unmarshal(rawTerm, &captured); err != nil {
		t.Fatal(err)
	}
	settings := flow.DefaultSettings()

	put := func(addr, runID string, createdMS, statusMS uint64, st *hc.Status, screen string) {
		term := captured
		term.Address = addr
		term.CreatedMS = createdMS
		term.StatusMS = &statusMS
		term.Status = st
		term.Reported = st != nil
		term.Labels = map[string]string{"sheepdog.run": runID, "sheepdog.model": agyWorkerModel}
		srv.Add(term)
		srv.SetScreen(addr, screen)
	}
	stepAll := func(rec flow.RunRecord, evs []flow.RunEvent, now time.Time) flow.RunRecord {
		for _, ev := range evs {
			rec, _, _ = flow.StepRun(rec, ev, settings, now)
		}
		return rec
	}

	// 1. Captured `working` status moves starting -> working.
	baseNow := time.UnixMilli(int64(*captured.StatusMS))
	put("t-work", "run-work", captured.CreatedMS, *captured.StatusMS, captured.Status, string(rawScreen))
	recWork := flow.RunRecord{
		ID: "run-work", Address: "t-work", CreatedMS: captured.CreatedMS,
		Role: flow.RoleWorker, CLI: "agy", State: flow.PhaseStarting,
		StartedAt: time.UnixMilli(int64(captured.CreatedMS)),
	}
	evs, err := world.ObserveRun(ctx, client, recWork)
	if err != nil {
		t.Fatal(err)
	}
	recWork = stepAll(recWork, evs, baseNow)
	if recWork.State != flow.PhaseWorking || recWork.TaskRun().State != flow.RunWorking {
		t.Fatalf("captured working status -> state=%s taskRun=%s, want working", recWork.State, recWork.TaskRun().State)
	}

	// 2. `idle` and `done` (with first line of reply in msg) end the turn as idle.
	for _, tc := range []struct {
		name string
		st   hc.Status
	}{
		{"idle", hc.Status{App: "antigravity", State: "idle"}},
		{"done with reply line", hc.Status{App: "antigravity", State: "done", Msg: "Implemented hello.go and ran tests."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.st
			doneMS := captured.CreatedMS + 10_000
			put("t-end", "run-end", captured.CreatedMS, doneMS, &st, string(rawScreen))
			rec := recWork
			rec.ID, rec.Address = "run-end", "t-end"
			evs, err := world.ObserveRun(ctx, client, rec)
			if err != nil {
				t.Fatal(err)
			}
			tDone := time.UnixMilli(int64(doneMS))
			rec = stepAll(rec, evs, tDone)
			rec, _, _ = flow.StepRun(rec, flow.RunEvent{Kind: flow.EvTick}, settings, tDone.Add(25*time.Second))
			if rec.State != flow.PhaseIdle || rec.TaskRun().State != flow.RunIdle || rec.LastTurnEnd.IsZero() {
				t.Fatalf("%s -> state=%s taskRun=%s lastTurnEnd=%v, want idle with turn ended", tc.name, rec.State, rec.TaskRun().State, rec.LastTurnEnd)
			}
		})
	}

	// 3. `blocked` with kind `permission` keeps its kind and command msg.
	permKind := "permission"
	permScreen := readScreen(t, "agy-approval-prompt.screen.txt")
	permMS := captured.CreatedMS + 15_000
	put("t-perm", "run-perm", captured.CreatedMS, permMS,
		&hc.Status{App: "antigravity", State: "blocked", Kind: &permKind, Msg: "go test ./..."},
		permScreen)
	recPerm := recWork
	recPerm.ID, recPerm.Address = "run-perm", "t-perm"
	evs, err = world.ObserveRun(ctx, client, recPerm)
	if err != nil {
		t.Fatal(err)
	}
	recPerm = stepAll(recPerm, evs, time.UnixMilli(int64(permMS)))
	if recPerm.State != flow.PhaseBlocked || recPerm.BlockedKind != "permission" || recPerm.Question != "go test ./..." {
		t.Fatalf("blocked/permission -> state=%s kind=%q msg=%q, want blocked/permission/\"go test ./...\"", recPerm.State, recPerm.BlockedKind, recPerm.Question)
	}

	// 4. Early `blocked` with kind `auth` (2s after start) is starting, not blocked and not signed out.
	authKind := "auth"
	earlyStart := time.Now().UTC().Add(-2 * time.Second).Truncate(time.Millisecond)
	earlyCreatedMS := uint64(earlyStart.UnixMilli())
	earlyStatusMS := uint64(earlyStart.Add(2 * time.Second).UnixMilli())
	put("t-early-auth", "run-early-auth", earlyCreatedMS, earlyStatusMS,
		&hc.Status{App: "antigravity", State: "blocked", Kind: &authKind, Msg: "Refreshing credentials"},
		"")
	recEarly := flow.RunRecord{
		ID: "run-early-auth", Address: "t-early-auth", CreatedMS: earlyCreatedMS,
		Role: flow.RoleWorker, CLI: "agy", State: flow.PhaseStarting,
		StartedAt: earlyStart, LastActivity: earlyStart,
	}
	evs, err = world.ObserveRun(ctx, client, recEarly)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind == flow.EvStatus && (ev.Status == "blocked" || ev.StatusKind == "auth") {
			t.Fatalf("early agy blocked/auth emitted blocked/auth status event: %+v", evs)
		}
	}
	recEarly = stepAll(recEarly, evs, earlyStart.Add(2*time.Second))
	if recEarly.State != flow.PhaseStarting || recEarly.BlockedKind != "" || recEarly.Result != "" {
		t.Fatalf("early agy blocked/auth -> state=%s(%s) kind=%q, want starting (not blocked, not signed_out)", recEarly.State, recEarly.Result, recEarly.BlockedKind)
	}
	// When the brief auth status clears to working 2s later, the run enters working normally.
	clearMS := earlyStatusMS + 2000
	put("t-early-auth", "run-early-auth", earlyCreatedMS, clearMS,
		&hc.Status{App: "antigravity", State: "working"},
		string(rawScreen))
	evs, err = world.ObserveRun(ctx, client, recEarly)
	if err != nil {
		t.Fatal(err)
	}
	recEarly = stepAll(recEarly, evs, earlyStart.Add(4*time.Second))
	if recEarly.State != flow.PhaseWorking {
		t.Fatalf("after early auth cleared -> state=%s, want working", recEarly.State)
	}

	// 5. Later `blocked` with kind `auth` (3m after start) maps as auth and ends signed_out.
	lateStart := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Millisecond)
	lateCreatedMS := uint64(lateStart.UnixMilli())
	lateStatusMS := uint64(lateStart.Add(3 * time.Minute).UnixMilli())
	put("t-late-auth", "run-late-auth", lateCreatedMS, lateStatusMS,
		&hc.Status{App: "antigravity", State: "blocked", Kind: &authKind, Msg: "Please sign in"},
		"")
	recLate := flow.RunRecord{
		ID: "run-late-auth", Address: "t-late-auth", CreatedMS: lateCreatedMS,
		Role: flow.RoleWorker, CLI: "agy", State: flow.PhaseStarting,
		StartedAt: lateStart, LastActivity: lateStart,
	}
	evs, err = world.ObserveRun(ctx, client, recLate)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(evs, func(ev flow.RunEvent) bool {
		return ev.Kind == flow.EvStatus && ev.Status == "blocked" && ev.StatusKind == "auth"
	}) {
		t.Fatalf("later agy blocked/auth events = %+v, want EvStatus{Status:\"blocked\", StatusKind:\"auth\"}", evs)
	}
	recLate = stepAll(recLate, evs, lateStart.Add(3*time.Minute))
	if recLate.State != flow.PhaseEnded || recLate.Result != flow.EndSignedOut {
		t.Fatalf("later agy blocked/auth -> state=%s(%s), want ended(signed_out)", recLate.State, recLate.Result)
	}
}

// TestAgyResolveAndAntigravityPool checks Decision 5 and candidate resolution
// (KB-QTA-4): [agy gemini, claude] picks agy when usable (never falling back
// with "cannot launch agy"), maps agy gemini-* models to the CodexBar
// `antigravity` pool so floor exhaustion falls back or holds, and leaves
// non-gemini-* agy models on an unknown pool that never blocks.
func TestAgyResolveAndAntigravityPool(t *testing.T) {
	geminiCand := flow.Candidate{CLI: "agy", Model: agyWorkerModel, Effort: "medium"}
	otherCand := flow.Candidate{CLI: "agy", Model: "custom-agy-model", Effort: "medium"}
	claudeCand := flow.Candidate{CLI: "claude", Model: "sonnet", Effort: "medium"}

	if got := poolProvider(geminiCand); got != "antigravity" {
		t.Errorf("poolProvider(%+v) = %q, want \"antigravity\"", geminiCand, got)
	}
	if got := poolProvider(otherCand); got != "" {
		t.Errorf("poolProvider(%+v) = %q, want \"\" (unknown)", otherCand, got)
	}

	s := flow.DefaultSettings()
	s.Models = map[string][]flow.Candidate{
		flow.RoleWorker: {geminiCand, claudeCand},
	}
	in, why := resolveWorker(flow.Task{}, s, flow.ResolveFacts{})
	if why != "" || in.CLI != "agy" || in.Model != agyWorkerModel {
		t.Fatalf("resolveWorker([agy gemini, claude]) = (%+v, %q), want agy/%s", in, why, agyWorkerModel)
	}

	// Engine launch with [agy gemini, claude] and healthy antigravity pool picks agy.
	r := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleWorker: {geminiCand, claudeCand},
	})
	fake := newPoolFake(t)
	r.eng.poolRun = fake.run
	fake.healthy(*r.now)
	fake.set("antigravity", antigravityPoolOut(*r.now, 80, 3*time.Hour, 70))

	r.pass() // J7 coordinator
	coordTok := (*r.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	if err := r.eng.Report(coordTok, "plan apply", []byte(`{"tasks":[`+poolTask("T1", "")+`]}`)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	r.pass() // N11
	r.pass() // N23
	st := r.state()
	if len(st.Tasks[0].Attempts) != 1 || st.Tasks[0].Attempts[0].Run == nil || st.Tasks[0].Attempts[0].Run.CLI != "agy" {
		t.Fatalf("worker with healthy antigravity pool = %+v, want agy", st.Tasks[0].Attempts)
	}
	fake.mu.Lock()
	antiCalls := fake.calls["antigravity"]
	fake.mu.Unlock()
	if antiCalls == 0 {
		t.Error("engine did not read codexbar --provider antigravity for agy gemini-* candidate")
	}

	// Floor exhaustion on antigravity holds an agy-gemini-only task, while an unpooled agy task starts.
	rFloor := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleWorker: {geminiCand},
	})
	fakeFloor := newPoolFake(t)
	rFloor.eng.poolRun = fakeFloor.run
	fakeFloor.healthy(*rFloor.now)
	fakeFloor.set("antigravity", antigravityPoolOut(*rFloor.now, 80, 3*time.Hour, 70))
	rFloor.pass() // J7
	tokFloor := (*rFloor.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	planBoth := `{"tasks":[` +
		poolTask("T1", "") + `,` +
		poolTask("T2", `,"request":{"worker":[{"cli":"agy","model":"`+agyNonGemini+`","effort":"medium"}]}`) +
		`]}`
	if err := rFloor.eng.Report(tokFloor, "plan apply", []byte(planBoth)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	*rFloor.now = rFloor.now.Add(6 * time.Minute)
	reset := rFloor.now.Add(90 * time.Minute)
	fakeFloor.set("antigravity", antigravityPoolOut(*rFloor.now, 2, 90*time.Minute, 70))
	for range 3 {
		rFloor.pass()
	}
	stFloor := rFloor.state()
	if len(stFloor.Tasks[0].Attempts) != 0 || !strings.Contains(stFloor.Tasks[0].HoldWhy, "antigravity") || !reasonHasTime(stFloor.Tasks[0].HoldWhy, reset) {
		t.Fatalf("gemini agy task at antigravity floor = attempts %d hold %q, want held naming antigravity and reset", len(stFloor.Tasks[0].Attempts), stFloor.Tasks[0].HoldWhy)
	}
	if len(stFloor.Tasks[1].Attempts) != 1 || stFloor.Tasks[1].Attempts[0].Run == nil || stFloor.Tasks[1].Attempts[0].Run.Model != agyNonGemini {
		t.Fatalf("non-gemini agy task with healthy 3p pool did not start: %+v", stFloor.Tasks[1])
	}
}

// TestAgyWorkerSessionFromHistoryAndResume checks Decision 4: the worker's
// session id is read through herdr-core (never from the daemon's own $HOME)
// from ~/.gemini/antigravity-cli/history.jsonl in agy's real line shape,
// resume passes `--conversation <id>` only when asked, and a missing id starts
// fresh with a handoff instead of resuming.
func TestAgyWorkerSessionFromHistoryAndResume(t *testing.T) {
	rawFixture, err := os.ReadFile(filepath.Join("testdata", "agy-history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rawScreen, err := os.ReadFile(filepath.Join("testdata", "agy-working.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}

	writeHistory := func(t *testing.T, hcHome, workspace string, tsMS int64) {
		t.Helper()
		wsJSON, _ := json.Marshal(workspace)
		content := strings.ReplaceAll(string(rawFixture), `"/Users/alice/worktree"`, string(wsJSON))
		content = strings.ReplaceAll(content, "1795371095000", strconv.FormatInt(tsMS, 10))
		histPath := filepath.Join(hcHome, ".gemini", "antigravity-cli", "history.jsonl")
		if err := os.MkdirAll(filepath.Dir(histPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(histPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	startWorker := func(t *testing.T) (*agyRig, string, string) {
		t.Helper()
		r := newAgyRig(t, map[string][]flow.Candidate{
			flow.RoleWorker: {{CLI: "agy", Model: agyWorkerModel, Effort: "medium"}},
		})
		r.pass() // J7 coordinator (claude)
		tok := (*r.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
		plan := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: agy session","brief":"Add session.go.",` + testScopeJSON + `,"outcomes":[{"text":"session exists","proved_by":"manual: session.go"}]}]}`
		if err := r.eng.Report(tok, "plan apply", []byte(plan)); err != nil {
			t.Fatalf("plan apply: %v", err)
		}
		r.pass() // N11
		r.pass() // N23
		st := r.state()
		if len(st.Tasks) != 1 || st.Tasks[0].Attempts[0].RunID == "" {
			t.Fatalf("agy worker did not start: %+v", st.Tasks)
		}
		if argv := r.last().Argv; slices.Contains(argv, "--conversation") || slices.Contains(argv, "-c") || slices.Contains(argv, "--continue") {
			t.Fatalf("fresh agy worker passed resume flags: %q", argv)
		}
		realWT, _ := filepath.EvalSymlinks(st.Tasks[0].Attempts[0].Worktree)
		return r, st.Tasks[0].Attempts[0].RunID, realWT
	}

	t.Run("history conversation id read via hc and resumed", func(t *testing.T) {
		r, w, realWT := startWorker(t)
		writeHistory(t, r.hcHome, realWT, r.now.Add(3*time.Second).UnixMilli())
		execsBefore := r.shellExecs.Load()

		r.srv.SetStatus(w, &hc.Status{App: "antigravity", State: "working"})
		r.srv.SetScreen(w, string(rawScreen))
		*r.now = r.now.Add(10 * time.Second)
		r.pass()

		run := r.state().Tasks[0].Attempts[0].Run
		if r.shellExecs.Load() <= execsBefore {
			t.Fatal("engine did not read agy history.jsonl through herdr-core machine.exec")
		}
		if run == nil || run.SessionID != agyHistoryConvID || !run.Resumable {
			t.Fatalf("worker run after observing history = %+v, want SessionID=%q Resumable=true", run, agyHistoryConvID)
		}

		// Crash the worker and verify restart uses `--conversation <id>` with held text.
		r.srv.Finish(w, "killed")
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		r.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Held = []string{"Also add a doc comment."} })
		runsBefore := len(*r.runs)
		*r.now = r.now.Add(time.Minute)
		r.pass()
		if len(*r.runs) != runsBefore+1 {
			t.Fatalf("crashed agy worker did not restart: %d runs, was %d", len(*r.runs), runsBefore)
		}
		resumed := r.last()
		assertNoGeminiAPIKey(t, "resumed worker", resumed)
		if got := argValue(resumed.Argv, "--conversation"); got != agyHistoryConvID {
			t.Fatalf("resumed argv = %q, want --conversation %s", resumed.Argv, agyHistoryConvID)
		}
		if slices.Contains(resumed.Argv, "-c") || slices.Contains(resumed.Argv, "--continue") {
			t.Errorf("resumed argv used cwd-latest flag (-c/--continue): %q", resumed.Argv)
		}
		prompt, ok := strings.CutPrefix(resumed.Argv[len(resumed.Argv)-1], "-i=")
		if !ok || !strings.Contains(prompt, "Also add a doc comment.") || strings.Contains(prompt, "Handoff from an earlier worker run:") {
			t.Errorf("resumed -i= prompt = %q, want held instruction without handoff", resumed.Argv[len(resumed.Argv)-1])
		}
	})

	t.Run("missing history id starts fresh with handoff", func(t *testing.T) {
		r, w, realWT := startWorker(t)
		writeHistory(t, r.hcHome, filepath.Join(realWT, "other-folder"), r.now.Add(3*time.Second).UnixMilli())

		r.srv.SetStatus(w, &hc.Status{App: "antigravity", State: "working"})
		r.srv.SetScreen(w, string(rawScreen))
		*r.now = r.now.Add(10 * time.Second)
		r.pass()

		if run := r.state().Tasks[0].Attempts[0].Run; run == nil || run.SessionID != "" || run.Resumable {
			t.Fatalf("worker run without matching history = %+v, want empty SessionID and Resumable=false", run)
		}

		r.srv.Finish(w, "killed")
		*r.now = r.now.Add(5 * time.Second)
		r.pass()
		runsBefore := len(*r.runs)
		*r.now = r.now.Add(time.Minute)
		r.pass()
		if len(*r.runs) != runsBefore+1 {
			t.Fatalf("worker did not restart fresh: %d runs, was %d", len(*r.runs), runsBefore)
		}
		fresh := r.last()
		if slices.Contains(fresh.Argv, "--conversation") || slices.Contains(fresh.Argv, "-c") || slices.Contains(fresh.Argv, "--continue") {
			t.Fatalf("restart with missing session id passed resume flag: %q", fresh.Argv)
		}
		if prompt, ok := strings.CutPrefix(fresh.Argv[len(fresh.Argv)-1], "-i="); !ok || !strings.Contains(prompt, "Handoff from an earlier worker run:") {
			t.Errorf("fresh restart prompt lacks handoff note: %q", fresh.Argv[len(fresh.Argv)-1])
		}
	})
}

// TestAgyDoctorChecks checks Decision 6: factory doctor (StartChecks and
// CheckAgentCLIs) checks agy through herdr-core when a role's candidates
// include it, reports a missing binary or unreadable status with its reason
// (unknown is never "fine" and never "signed out"), and passes when agy is
// present.
func TestAgyDoctorChecks(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	root := t.TempDir()
	gitIn(t, root, "init", "-b", "main")
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".github", "workflows", "ci.yml"), []byte(goodWorkflow), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-m", "chore: ci")
	bare := filepath.Join(t.TempDir(), "remote.git")
	gitIn(t, root, "init", "--bare", "-b", "main", bare)
	gitIn(t, root, "remote", "add", "origin", bare)
	gitIn(t, root, "push", "-u", "origin", "main")
	gitIn(t, root, "remote", "set-head", "origin", "main")
	gitIn(t, root, "config", "url.https://github.com/acme/demo.git.insteadOf", bare)
	realRoot, _ := filepath.EvalSymlinks(root)

	st := flow.State{
		Project:  flow.Project{ID: "p1", Folder: realRoot},
		Settings: flow.DefaultSettings(),
	}
	st.Settings.Models = map[string][]flow.Candidate{
		flow.RoleWorker: {{CLI: "agy", Model: agyWorkerModel, Effort: "medium"}},
	}
	if err := SaveState(Paths{DataDir: dataDir}.State("p1"), st); err != nil {
		t.Fatal(err)
	}

	runStartWithHC := func(t *testing.T, dir string, onAgy func(hc.ExecParams) (hc.ExecResult, error)) ([]onboard.Result, int) {
		t.Helper()
		srv := hctest.New(t)
		var agyCalls atomic.Int32
		srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			zero := 0
			if len(p.Argv) > 0 && p.Argv[0] == "git" {
				return hctest.GitExec(p)
			}
			if len(p.Argv) > 0 && p.Argv[0] == "gh" {
				if slices.Contains(p.Argv, "view") {
					return hc.ExecResult{Code: &zero, Stdout: []byte(repoView)}, nil
				}
				if slices.ContainsFunc(p.Argv, func(a string) bool { return strings.HasSuffix(a, "/branches/main") && !strings.Contains(a, "/rules/") }) {
					return hc.ExecResult{Code: &zero, Stdout: []byte(`{"protected":false}`)}, nil
				}
				return hc.ExecResult{Code: &zero, Stdout: []byte("[]")}, nil
			}
			inner := p.Argv
			if len(p.Argv) >= 5 && p.Argv[0] == "sh" && p.Argv[1] == "-c" && p.Argv[3] == "sh" {
				inner = p.Argv[4:]
			}
			if len(inner) > 0 && inner[0] == "claude" {
				return hc.ExecResult{Code: &zero, Stdout: []byte(`{"loggedIn":true}`)}, nil
			}
			if len(inner) > 0 && inner[0] == "agy" {
				agyCalls.Add(1)
				return onAgy(p)
			}
			return hc.ExecResult{Code: &zero}, nil
		}
		rs := StartChecks(ctx, DoctorOptions{
			Folder:   dir,
			DataDir:  dataDir,
			HCSocket: srv.Path,
			LookPath: func(string) (string, error) { return "/usr/local/bin/present-locally", nil },
		})
		return rs, int(agyCalls.Load())
	}

	t.Run("agy missing in herdr-core gives reason", func(t *testing.T) {
		rs, calls := runStartWithHC(t, realRoot, func(hc.ExecParams) (hc.ExecResult, error) {
			code := 127
			return hc.ExecResult{Code: &code}, nil
		})
		if calls == 0 {
			t.Fatal("StartChecks did not probe agy through herdr-core")
		}
		r := findResult(t, rs, "start agy")
		if r.Status != onboard.Fail || strings.TrimSpace(r.Detail) == "" || strings.TrimSpace(r.Fix) == "" {
			t.Fatalf("missing agy result = %+v, want Fail with reason and fix", r)
		}
		low := strings.ToLower(r.Detail)
		if strings.Contains(low, "signed out") || strings.Contains(low, "not signed in") || strings.Contains(low, "not logged in") {
			t.Errorf("missing agy reported as signed out instead of missing binary: %+v", r)
		}
	})

	t.Run("agy unreadable status gives reason and is neither fine nor signed out", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			fn   func(hc.ExecParams) (hc.ExecResult, error)
		}{
			{"timed out", func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{TimedOut: true}, nil
			}},
			{"rpc error", func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{}, errors.New("hc exec: broken pipe")
			}},
			{"unreadable exit", func(hc.ExecParams) (hc.ExecResult, error) {
				code := 2
				return hc.ExecResult{Code: &code, Stderr: []byte("ipc transport broken\n")}, nil
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rs, calls := runStartWithHC(t, realRoot, tc.fn)
				if calls == 0 {
					t.Fatal("StartChecks did not probe agy through herdr-core")
				}
				r := findResult(t, rs, "start agy")
				if r.Status == onboard.OK || r.Status == onboard.Skip || strings.TrimSpace(r.Detail) == "" {
					t.Fatalf("unreadable agy (%s) = %+v, want non-OK/non-Skip with reason", tc.name, r)
				}
				low := strings.ToLower(r.Detail)
				if strings.Contains(low, "signed out") || strings.Contains(low, "not signed in") || strings.Contains(low, "not logged in") {
					t.Errorf("unreadable agy (%s) reported as signed out: %+v", tc.name, r)
				}
			})
		}

		// CheckAgentCLIs (factory doctor's top half) must also never report an unreadable error as "not logged in".
		env := newFakeEnv("agy")
		env.handlers["agy models"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Exit: 2, Stderr: "ipc transport broken\n"}
		}
		docRs := CheckAgentCLIs(ctx, DoctorOptions{TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run, Canary: false}, true)
		agyRes := findResult(t, docRs, "agy")
		low := strings.ToLower(agyRes.Detail)
		if agyRes.Status == onboard.OK || strings.Contains(low, "not logged in") || strings.Contains(low, "not signed in") {
			t.Fatalf("CheckAgentCLIs on unreadable agy error = %+v, want reason (never OK and never signed out)", agyRes)
		}
	})

	t.Run("agy present in herdr-core passes", func(t *testing.T) {
		rs, calls := runStartWithHC(t, realRoot, func(hc.ExecParams) (hc.ExecResult, error) {
			zero := 0
			return hc.ExecResult{Code: &zero, Stdout: []byte(agyModelsList)}, nil
		})
		if calls == 0 {
			t.Fatal("StartChecks did not probe agy through herdr-core")
		}
		if r := findResult(t, rs, "start agy"); r.Status != onboard.OK {
			t.Fatalf("present agy = %+v, want OK", r)
		}
	})

	t.Run("agy omitted when no role uses it", func(t *testing.T) {
		other := t.TempDir()
		gitIn(t, other, "init", "-b", "main")
		realOther, _ := filepath.EvalSymlinks(other)
		rs, calls := runStartWithHC(t, realOther, func(hc.ExecParams) (hc.ExecResult, error) {
			code := 127
			return hc.ExecResult{Code: &code}, nil
		})
		if calls != 0 {
			t.Fatalf("probed agy %d times for a project whose candidates do not include agy", calls)
		}
		for _, r := range rs {
			if r.Name == "start agy" {
				t.Fatalf("unexpected start agy check when unused: %+v", r)
			}
		}
	})
}

// TestAgyWorkerReadyAndMergeHistory checks the full worker loop for an agy
// worker: it starts in its worktree, enters working from antigravity hc
// status, commits and reports ready via its run token, goes idle from done
// status, opens a PR, passes two review rounds, merges and cleans up.
func TestAgyWorkerReadyAndMergeHistory(t *testing.T) {
	rawScreen, err := os.ReadFile(filepath.Join("testdata", "agy-working.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	r := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleWorker: {{CLI: "agy", Model: agyWorkerModel, Effort: "medium"}},
	})

	r.pass() // J7 coordinator
	coordTok := (*r.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: agy greeting","brief":"Add hello.go with Greet.",` + testScopeJSON + `,"outcomes":[{"text":"hello exists","proved_by":"manual: hello.go"}]}]}`
	if err := r.eng.Report(coordTok, "plan apply", []byte(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	r.pass() // N11 worktree
	r.pass() // N23 starts agy worker
	st := r.state()
	if len(st.Tasks) != 1 || st.Tasks[0].Attempts[0].RunID == "" || st.Tasks[0].Attempts[0].Run.CLI != "agy" {
		t.Fatalf("agy worker not started: %+v", st.Tasks)
	}
	w := st.Tasks[0].Attempts[0].RunID
	wt := st.Tasks[0].Attempts[0].Worktree
	workerTok := r.last().Env["SHEEPDOG_RUN_TOKEN"]

	// Worker reports working through herdr-core.
	r.srv.SetScreen(w, string(rawScreen))
	r.srv.SetStatus(w, &hc.Status{App: "antigravity", State: "working"})
	*r.now = r.now.Add(5 * time.Second)
	r.pass()
	if got := r.state().Tasks[0].Attempts[0].Run.State; got != flow.PhaseWorking {
		t.Fatalf("agy worker state while working = %s, want working", got)
	}

	// Worker commits and sends ready report, then reports done.
	if err := os.WriteFile(filepath.Join(wt, "hello.go"), []byte("package demo\n\nfunc Greet() string { return \"hi\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "hello.go")
	gitIn(t, wt, "commit", "-m", "feat: add Greet")
	c1 := gitIn(t, wt, "rev-parse", "HEAD")
	writePRJSON(t, r.gh, "pr.json", c1, greenRollup)
	if err := r.eng.Report(workerTok, "ready", []byte(`{"head":"`+c1+`","proofs":[{"outcome":"hello exists","artifact":"hello.go"}],"touched":"hello.go","skipped":""}`)); err != nil {
		t.Fatalf("ready from agy worker: %v", err)
	}

	r.srv.SetStatus(w, &hc.Status{App: "antigravity", State: "done", Msg: "Added Greet in hello.go and reported ready."})
	*r.now = r.now.Add(5 * time.Second)
	r.pass() // R17 records done status
	*r.now = r.now.Add(25 * time.Second)
	r.pass() // R20 moves worker to idle; N26 pushes and opens PR #7
	if got := r.state().Tasks[0].Attempts[0].Run; got == nil || got.State != flow.PhaseIdle || got.TaskRun().State != flow.RunIdle {
		t.Fatalf("agy worker after done + grace = %+v, want idle", got)
	}

	writePRJSON(t, r.gh, "view.json", c1, greenRollup)
	if r.state().Tasks[0].Attempts[0].PR == 0 {
		r.pass() // N26 if needed
	}
	r.pass() // N29 starts round 1 reviewer
	for round := 1; round <= 2; round++ {
		rv := r.state().Tasks[0].Attempts[0].Review
		if rv == nil || rv.Round != round {
			t.Fatalf("round %d review = %+v", round, rv)
		}
		if err := submitFakeReviewerVerdict(t, r.eng, r.srv, r.paths, r.id, "T1", rv, r.reviewerToken(rv.RunID), flow.VerdictPass, 0, "Looks good.\n"); err != nil {
			t.Fatalf("round %d review verdict: %v", round, err)
		}
		if rv.Reviewer.CLI == world.CodexCLI {
			r.pass()
			*r.now = r.now.Add(25 * time.Second)
			r.pass()
			if round == 1 {
				r.pass() // N29 starts round 2 reviewer
			}
		} else {
			r.pass() // stops accepted reviewer and advances (starts round 2 or N42)
		}
	}
	r.pass() // N42 asks merge
	merged := `[{"number":7,"state":"MERGED","headRefOid":"` + c1 + `","baseRefName":"main","isCrossRepository":false,"mergeCommit":{"oid":"merged99"},"mergeStateStatus":"CLEAN","reviewDecision":"","statusCheckRollup":` + greenRollup + `}]`
	writeGHFile(t, r.gh, "pr.json", merged)
	writeGHFile(t, r.gh, "view.json", merged[1:len(merged)-1])
	r.pass() // N2 done
	r.pass() // N1 cleanup
	if d := r.state().Tasks[0].Done; d == nil || d.MergedCommit != "merged99" {
		t.Fatalf("agy worker task did not finish merged: %+v", r.state().Tasks[0])
	}
}

// TestAgyReviewerFirstPromptReadOnlyInstruction checks Decision 1's reviewer
// rule: because agy has no unattended read-only mode, an agy reviewer runs in
// `--mode auto` and its first prompt (`-i=<prompt>`) states plainly "do not
// modify, create or delete any files; read and run tests only".
func TestAgyReviewerFirstPromptReadOnlyInstruction(t *testing.T) {
	const wantInstruction = "do not modify, create or delete any files; read and run tests only"

	r := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleReviewer: {
			{CLI: "agy", Model: agyReviewModel, Effort: "high"},
			{CLI: "agy", Model: agyCoordModel, Effort: "medium"},
		},
	})

	r.pass() // J7 coordinator
	coordTok := (*r.runs)[0].Env["SHEEPDOG_RUN_TOKEN"]
	plan := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: agy readonly review","brief":"Add item.go.",` + testScopeJSON + `,"outcomes":[{"text":"item exists","proved_by":"manual: item.go"}]}]}`
	if err := r.eng.Report(coordTok, "plan apply", []byte(plan)); err != nil {
		t.Fatalf("plan apply with agy reviewer: %v", err)
	}
	r.pass() // N11 worktree
	r.pass() // N23 claude worker
	wt := r.state().Tasks[0].Attempts[0].Worktree
	workerTok := r.last().Env["SHEEPDOG_RUN_TOKEN"]
	if err := os.WriteFile(filepath.Join(wt, "item.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "item.go")
	gitIn(t, wt, "commit", "-m", "feat: add item")
	c1 := gitIn(t, wt, "rev-parse", "HEAD")
	writePRJSON(t, r.gh, "pr.json", c1, greenRollup)
	if err := r.eng.Report(workerTok, "ready", []byte(`{"head":"`+c1+`","proofs":[{"outcome":"item exists","artifact":"item.go"}],"touched":"item.go","skipped":""}`)); err != nil {
		t.Fatalf("ready: %v", err)
	}
	r.pass() // N26 pushes and opens PR
	writePRJSON(t, r.gh, "view.json", c1, greenRollup)
	r.pass() // N29 starts round 1 agy reviewer

	rv := r.state().Tasks[0].Attempts[0].Review
	if rv == nil || rv.RunID == "" || rv.Reviewer.CLI != "agy" {
		t.Fatalf("agy reviewer did not start: %+v", rv)
	}
	revParams := r.last()
	assertAgyFreshArgv(t, "reviewer", agyReviewModel, revParams, "Review round 1", "factory report review")
	prompt, _ := strings.CutPrefix(revParams.Argv[len(revParams.Argv)-1], "-i=")
	if !strings.Contains(strings.ToLower(prompt), wantInstruction) {
		t.Fatalf("agy reviewer -i= first prompt lacks %q:\n%s", wantInstruction, prompt)
	}
}
