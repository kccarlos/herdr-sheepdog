package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// makeStartCheckFake builds a good-default startFake for repo with optional overrides.
func makeStartCheckFake(repo string, over startFake) startFake {
	f := startFake{
		"git --version":                                             out("git version 2.45.0\n"),
		"rev-parse --show-toplevel":                                 out(repo),
		"rev-parse --verify HEAD":                                   out("abc1234\n"),
		"remote get-url --all origin":                               out("https://github.com/o/r.git"),
		"remote get-url --push --all origin":                        out("https://github.com/o/r.git"),
		"refs/remotes/origin/HEAD":                                  out("origin/main"),
		"rev-parse --verify --quiet origin/main":                    out("abc1234\n"),
		"claude auth status --json":                                 out(`{"loggedIn":true}`),
		"codex login status":                                        out("Logged in using ChatGPT\n"),
		"agy models":                                                out("gemini-4-carbon-high\tGemini 4 Carbon\n"),
		"ls-tree --name-only origin/main .github/workflows/":        out(".github/workflows/ci.yml"),
		"show origin/main:.github/workflows/ci.yml":                 out(goodWorkflow),
		"gh api repos/o/r/branches/main":                            out(`{"protected":false}`),
		"gh repo view o/r --json defaultBranchRef,viewerPermission": out(repoView),
		"gh api repos/o/r/branches/main/protection":                 {Exit: 1, Stderr: "HTTP 404: Branch not protected"},
		"gh api repos/o/r/rules/branches/main":                      out("[]"),
		"fetch --prune origin":                                      out(""),
	}
	for k, v := range over {
		for d := range f {
			if strings.HasSuffix(d, k) || strings.HasSuffix(k, d) {
				delete(f, d)
			}
		}
		f[k] = v
	}
	return f
}

// wireStartCheck attaches real StartChecks backed by fake to eng and returns a
// counter of how many times Engine.Start invoked StartCheck.
func wireStartCheck(eng *Engine, fake startFake) *int {
	calls := 0
	eng.StartCheck = func(ctx context.Context, opts DoctorOptions) []onboard.Result {
		calls++
		opts.HCSocket = ""
		opts.RunCommand = fake.run
		return StartChecks(ctx, opts)
	}
	return &calls
}

// TestStartChecksO31Roles_KB_STA_18 verifies O31 (design.md section 6 J1a–J1c)
// for both StartChecks results and Engine.Start enforcement (rows 1–6, 11, 12).
func TestStartChecksO31Roles_KB_STA_18(t *testing.T) {
	t.Run("J1c row 1: reviewer claude then codex, codex signed out, claude signed in warns and starts", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		st := flow.State{
			Project:  flow.Project{ID: "p-r1", Folder: repo},
			Settings: flow.DefaultSettings(),
		}
		st.Settings.SecondReviewer = "codex"
		if err := SaveState(paths.State("p-r1"), st); err != nil {
			t.Fatal(err)
		}

		fake := makeStartCheckFake(repo, startFake{
			"codex login status": {Exit: 1, Stdout: "Not logged in\n"},
		})
		rs := StartChecks(context.Background(), DoctorOptions{Folder: repo, DataDir: paths.DataDir, RunCommand: fake.run})
		r := findResult(t, rs, "start codex")
		if r.Status != onboard.Warn || !strings.Contains(r.Fix, "codex login") {
			t.Fatalf("J1c start codex = %+v, want Warn with codex login fix", r)
		}
		if onboard.Failed(rs) {
			t.Fatalf("J1c unexpected Fail in StartChecks: %+v", rs)
		}

		eng := newGitTestEngine(t, paths, nil)
		calls := wireStartCheck(eng, fake)
		if err := eng.Start(repo, "ship row 1", 0); err != nil {
			t.Fatalf("J1c Engine.Start = %v, want nil", err)
		}
		if *calls != 1 {
			t.Fatalf("Engine.Start StartCheck calls = %d, want 1", *calls)
		}
	})

	t.Run("J1c row 2: coordinator claude then codex, claude signed out, codex signed in warns and starts", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		st := flow.State{
			Project:  flow.Project{ID: "p-r2", Folder: repo},
			Settings: flow.DefaultSettings(),
		}
		st.Settings.Models = map[string][]flow.Candidate{
			flow.RoleCoordinator: {
				{CLI: "claude", Model: "opus", Effort: "medium"},
				{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"},
			},
			flow.RoleWorker:   {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}},
			flow.RoleReviewer: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}},
		}
		if err := SaveState(paths.State("p-r2"), st); err != nil {
			t.Fatal(err)
		}

		fake := makeStartCheckFake(repo, startFake{
			"claude auth status --json": out(`{"loggedIn":false}`),
		})
		rs := StartChecks(context.Background(), DoctorOptions{Folder: repo, DataDir: paths.DataDir, RunCommand: fake.run})
		r := findResult(t, rs, "start claude")
		if r.Status != onboard.Warn || !strings.Contains(r.Fix, "claude auth login") {
			t.Fatalf("J1c coordinator fallback start claude = %+v, want Warn with claude auth login fix", r)
		}
		if onboard.Failed(rs) {
			t.Fatalf("J1c coordinator fallback unexpected Fail: %+v", rs)
		}

		eng := newGitTestEngine(t, paths, nil)
		calls := wireStartCheck(eng, fake)
		if err := eng.Start(repo, "ship row 2", 0); err != nil {
			t.Fatalf("J1c Engine.Start = %v, want nil", err)
		}
		if *calls != 1 {
			t.Fatalf("Engine.Start StartCheck calls = %d, want 1", *calls)
		}
	})

	t.Run("J1a row 3: only coordinator CLI signed out fails naming coordinator and fix", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		fake := makeStartCheckFake(repo, startFake{
			"claude auth status --json": out(`{"loggedIn":false}`),
		})
		rs := StartChecks(context.Background(), DoctorOptions{Folder: repo, DataDir: paths.DataDir, RunCommand: fake.run})
		r := findResult(t, rs, "start claude")
		if r.Status != onboard.Fail || !strings.Contains(strings.ToLower(r.Detail), "coordinator") || !strings.Contains(r.Fix, "claude auth login") {
			t.Fatalf("J1a start claude = %+v, want Fail naming coordinator and claude auth login", r)
		}
		fails := 0
		for _, res := range rs {
			if res.Status == onboard.Fail {
				fails++
			}
		}
		if fails != 1 {
			t.Fatalf("J1a fail count = %d, want 1: %+v", fails, rs)
		}

		eng := newGitTestEngine(t, paths, nil)
		calls := wireStartCheck(eng, fake)
		err := eng.Start(repo, "ship row 3", 0)
		if *calls != 1 {
			t.Fatalf("Engine.Start StartCheck calls = %d, want 1", *calls)
		}
		if err == nil || !strings.Contains(err.Error(), "claude auth login") {
			t.Fatalf("J1a Engine.Start = %v, want error carrying claude auth login fix", err)
		}
	})

	t.Run("J1a row 4: coordinator fine, every worker CLI missing fails naming worker and install fix", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		st := flow.State{
			Project:  flow.Project{ID: "p-r4", Folder: repo},
			Settings: flow.DefaultSettings(),
		}
		st.Settings.Models = map[string][]flow.Candidate{
			flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "medium"}},
			flow.RoleWorker:      {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}},
			flow.RoleReviewer:    {{CLI: "claude", Model: "opus", Effort: "medium"}},
		}
		if err := SaveState(paths.State("p-r4"), st); err != nil {
			t.Fatal(err)
		}

		fake := makeStartCheckFake(repo, startFake{
			"codex login status": {Exit: 127},
		})
		rs := StartChecks(context.Background(), DoctorOptions{Folder: repo, DataDir: paths.DataDir, RunCommand: fake.run})
		r := findResult(t, rs, "start codex")
		if r.Status != onboard.Fail || !strings.Contains(strings.ToLower(r.Detail), "worker") || !strings.Contains(strings.ToLower(r.Fix), "install") {
			t.Fatalf("J1a worker missing start codex = %+v, want Fail naming worker and install fix", r)
		}

		eng := newGitTestEngine(t, paths, nil)
		calls := wireStartCheck(eng, fake)
		err := eng.Start(repo, "ship row 4", 0)
		if *calls != 1 || err == nil || !strings.Contains(strings.ToLower(err.Error()), "worker") {
			t.Fatalf("J1a Engine.Start calls=%d err=%v, want 1 call and worker refusal", *calls, err)
		}
	})

	t.Run("J1b row 5: only candidate probe times out warns unknown and starts", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		fake := makeStartCheckFake(repo, startFake{
			"claude auth status --json": {Exit: -1, TimedOut: true},
		})
		rs := StartChecks(context.Background(), DoctorOptions{Folder: repo, DataDir: paths.DataDir, RunCommand: fake.run})
		r := findResult(t, rs, "start claude")
		low := strings.ToLower(r.Detail)
		if r.Status != onboard.Warn || !strings.Contains(low, "unknown (") || strings.Contains(low, "not signed in") || strings.Contains(low, "not logged in") {
			t.Fatalf("J1b timeout start claude = %+v, want Warn with unknown (<reason>), never not signed in", r)
		}
		if onboard.Failed(rs) {
			t.Fatalf("J1b unexpected Fail: %+v", rs)
		}

		eng := newGitTestEngine(t, paths, nil)
		calls := wireStartCheck(eng, fake)
		if err := eng.Start(repo, "ship row 5", 0); err != nil {
			t.Fatalf("J1b Engine.Start = %v, want nil", err)
		}
		if *calls != 1 {
			t.Fatalf("Engine.Start StartCheck calls = %d, want 1", *calls)
		}
	})

	t.Run("J1b row 6: role with one out CLI and one unknown CLI warns on both and starts", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		st := flow.State{
			Project:  flow.Project{ID: "p-r6", Folder: repo},
			Settings: flow.DefaultSettings(),
		}
		st.Settings.Models = map[string][]flow.Candidate{
			flow.RoleCoordinator: {
				{CLI: "claude", Model: "opus", Effort: "medium"},
				{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"},
			},
			flow.RoleWorker:   {{CLI: "claude", Model: "sonnet", Effort: "medium"}},
			flow.RoleReviewer: {{CLI: "claude", Model: "opus", Effort: "medium"}},
		}
		if err := SaveState(paths.State("p-r6"), st); err != nil {
			t.Fatal(err)
		}

		fake := makeStartCheckFake(repo, startFake{
			"claude auth status --json": {Exit: -1, TimedOut: true},
			"codex login status":        {Exit: 1, Stdout: "Not logged in\n"},
		})
		rs := StartChecks(context.Background(), DoctorOptions{Folder: repo, DataDir: paths.DataDir, RunCommand: fake.run})
		rc := findResult(t, rs, "start claude")
		rx := findResult(t, rs, "start codex")
		if rc.Status != onboard.Warn || !strings.Contains(strings.ToLower(rc.Detail), "unknown (") {
			t.Fatalf("J1b start claude = %+v, want Warn unknown", rc)
		}
		if rx.Status != onboard.Warn || !strings.Contains(rx.Fix, "codex login") {
			t.Fatalf("J1b+J1c start codex = %+v, want Warn with codex login fix", rx)
		}
		if onboard.Failed(rs) {
			t.Fatalf("J1b+J1c unexpected Fail: %+v", rs)
		}

		eng := newGitTestEngine(t, paths, nil)
		calls := wireStartCheck(eng, fake)
		if err := eng.Start(repo, "ship row 6", 0); err != nil {
			t.Fatalf("J1b+J1c Engine.Start = %v, want nil", err)
		}
		if *calls != 1 {
			t.Fatalf("Engine.Start StartCheck calls = %d, want 1", *calls)
		}
	})

	t.Run("J1c row 11: task request CLI out while project role list is signed in warns and starts", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		st := flow.State{
			Project:  flow.Project{ID: "p-r11", Folder: repo},
			Settings: flow.DefaultSettings(),
			Tasks: []flow.Task{{
				ID:    "T1",
				Title: "feat: task requesting codex",
				Request: map[string][]flow.Candidate{
					flow.RoleWorker: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}},
				},
			}},
		}
		if err := SaveState(paths.State("p-r11"), st); err != nil {
			t.Fatal(err)
		}

		fake := makeStartCheckFake(repo, startFake{
			"codex login status": {Exit: 1, Stdout: "Not logged in\n"},
		})
		rs := StartChecks(context.Background(), DoctorOptions{Folder: repo, DataDir: paths.DataDir, RunCommand: fake.run})
		rx := findResult(t, rs, "start codex")
		if rx.Status != onboard.Warn {
			t.Fatalf("row 11 start codex = %+v, want Warn", rx)
		}
		if onboard.Failed(rs) {
			t.Fatalf("row 11 unexpected Fail: %+v", rs)
		}

		eng := newGitTestEngine(t, paths, nil)
		calls := wireStartCheck(eng, fake)
		if err := eng.Start(repo, "ship row 11", 0); err != nil {
			t.Fatalf("row 11 Engine.Start = %v, want nil", err)
		}
		if *calls != 1 {
			t.Fatalf("Engine.Start StartCheck calls = %d, want 1", *calls)
		}
	})

	t.Run("row 12: new folder probes only default claude, never codex or agy", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		fake := makeStartCheckFake(repo, nil)
		var probed []string
		run := func(ctx context.Context, dir string, env []string, argv ...string) CommandOutput {
			if len(argv) > 0 && (argv[0] == "claude" || argv[0] == "codex" || argv[0] == "agy") {
				probed = append(probed, argv[0])
			}
			return fake.run(ctx, dir, env, argv...)
		}
		rs := StartChecks(context.Background(), DoctorOptions{Folder: repo, DataDir: paths.DataDir, RunCommand: run})
		if onboard.Failed(rs) {
			t.Fatalf("row 12 StartChecks failed: %+v", rs)
		}
		if !slices.Equal(probed, []string{"claude"}) {
			t.Fatalf("row 12 probed CLIs = %v, want [claude] only", probed)
		}
	})
}

// TestEngineStartEnforcesJ1_KB_STA_18 verifies Engine.Start J1 ordering,
// timeout, before-e.mu execution, before-RepoProject git-config hygiene (row 12),
// and J1d state preservation (rows 7, 8, 9).
func TestEngineStartEnforcesJ1_KB_STA_18(t *testing.T) {
	t.Run("runs before e.mu with bounded timeout and populates DoctorOptions", func(t *testing.T) {
		srv := hctest.New(t)
		srv.OnExec = hctest.GitExec
		client := hc.NewClient(srv.Path)
		paths := testPaths(t)
		repo := initGitRepo(t)

		eng := newGitTestEngine(t, paths, client)
		var called bool
		eng.StartCheck = func(ctx context.Context, opts DoctorOptions) []onboard.Result {
			called = true
			dl, ok := ctx.Deadline()
			if !ok {
				t.Errorf("StartCheck ctx has no deadline")
			} else if rem := time.Until(dl); rem <= 0 || rem > 5*time.Minute {
				t.Errorf("StartCheck deadline remaining = %v, want bounded positive duration", rem)
			}
			if opts.Folder != repo || opts.DataDir != paths.DataDir || opts.HCSocket != srv.Path {
				t.Errorf("StartCheck opts = %+v, want Folder=%q DataDir=%q HCSocket=%q", opts, repo, paths.DataDir, srv.Path)
			}
			// Calling eng.Projects() acquires e.mu; if Engine.Start held e.mu across
			// StartCheck this would deadlock.
			_ = eng.Projects()
			return nil
		}
		if err := eng.Start(repo, "verify seam", 0); err != nil {
			t.Fatalf("Engine.Start: %v", err)
		}
		if !called {
			t.Fatal("Engine.Start never called StartCheck")
		}
	})

	t.Run("row 12 order: refused start on new folder writes no sheepdog.projectId and no project dir", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		eng := newGitTestEngine(t, paths, nil)
		fake := makeStartCheckFake(repo, startFake{
			"claude auth status --json": out(`{"loggedIn":false}`),
		})
		calls := wireStartCheck(eng, fake)

		err := eng.Start(repo, "refused new folder", 0)
		if *calls != 1 {
			t.Fatalf("StartCheck calls = %d, want 1", *calls)
		}
		if err == nil || !strings.Contains(err.Error(), "claude auth login") {
			t.Fatalf("Engine.Start = %v, want refusal with claude auth login", err)
		}
		// git config sheepdog.projectId must still be empty.
		pidOut, _ := exec.Command("git", "-C", repo, "config", "--local", "--get", "sheepdog.projectId").Output()
		if got := strings.TrimSpace(string(pidOut)); got != "" {
			t.Fatalf("refused start wrote sheepdog.projectId = %q, want empty", got)
		}
		// DataDir must have no project folder.
		entries, rerr := os.ReadDir(filepath.Join(paths.DataDir, "factory", "projects"))
		if rerr == nil && len(entries) > 0 {
			t.Fatalf("refused start created project folders: %+v", entries)
		}
	})

	t.Run("J1d row 7: refused start on stopped project leaves state.json byte-identical and logs nothing", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		if b, err := exec.Command("git", "-C", repo, "config", "--local", "sheepdog.projectId", "p-stopped").CombinedOutput(); err != nil {
			t.Fatalf("git config projectId: %v: %s", err, b)
		}
		st := flow.State{
			Seq:      3,
			Project:  flow.Project{ID: "p-stopped", Folder: repo, Goal: "old goal", Started: false},
			Settings: flow.DefaultSettings(),
			Tasks:    []flow.Task{{ID: "T1", Title: "feat: kept"}},
		}
		statePath := paths.State("p-stopped")
		if err := SaveState(statePath, st); err != nil {
			t.Fatal(err)
		}
		beforeBytes, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}

		eng := newGitTestEngine(t, paths, nil)
		fake := makeStartCheckFake(repo, startFake{
			"claude auth status --json": out(`{"loggedIn":false}`),
		})
		calls := wireStartCheck(eng, fake)

		if err := eng.Start(repo, "new goal", 0); err == nil {
			t.Fatal("J1d stopped project: Engine.Start succeeded, want refusal")
		}
		if *calls != 1 {
			t.Fatalf("StartCheck calls = %d, want 1", *calls)
		}
		afterBytes, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(beforeBytes, afterBytes) {
			t.Fatalf("J1d stopped project: state.json changed on refused start:\nbefore: %s\nafter:  %s", beforeBytes, afterBytes)
		}
		if logBytes, lerr := os.ReadFile(paths.Log("p-stopped")); lerr == nil && strings.Contains(string(logBytes), `"started"`) {
			t.Fatalf("J1d stopped project: refused start wrote started line to log: %s", logBytes)
		}
	})

	t.Run("J1d row 8: refused start on running J5-held project leaves goal, Started, alert and wakes unchanged", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		if b, err := exec.Command("git", "-C", repo, "config", "--local", "sheepdog.projectId", "p-j5").CombinedOutput(); err != nil {
			t.Fatalf("git config projectId: %v: %s", err, b)
		}
		now := time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC)
		st := flow.State{
			Seq: 5,
			Project: flow.Project{
				ID:      "p-j5",
				Folder:  repo,
				Goal:    "original goal",
				Started: true,
				RunID:   "coord-1",
				Run:     &flow.RunRecord{ID: "r1", Role: flow.RoleCoordinator, CLI: "claude", State: flow.PhaseIdle},
				Alerts: map[string]flow.Alert{
					flow.AlertAgentsDown: {
						Kind:  flow.AlertAgentsDown,
						Cause: "claude is not signed in",
						At:    now,
						Fix:   "run: claude auth login",
						Rule:  "J5",
						Scope: "signed_out:claude",
					},
				},
			},
			Settings: flow.DefaultSettings(),
			Tasks:    []flow.Task{{ID: "T1", Title: "feat: active"}},
		}
		statePath := paths.State("p-j5")
		if err := SaveState(statePath, st); err != nil {
			t.Fatal(err)
		}
		beforeBytes, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}

		eng := newGitTestEngine(t, paths, nil)
		fake := makeStartCheckFake(repo, startFake{
			"claude auth status --json": out(`{"loggedIn":false}`),
		})
		calls := wireStartCheck(eng, fake)

		if err := eng.Start(repo, "replacement goal", 0); err == nil {
			t.Fatal("J1d J5-held project: Engine.Start succeeded, want refusal")
		}
		if *calls != 1 {
			t.Fatalf("StartCheck calls = %d, want 1", *calls)
		}
		afterBytes, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(beforeBytes, afterBytes) {
			t.Fatalf("J1d J5-held project: state.json changed on refused start:\nbefore: %s\nafter:  %s", beforeBytes, afterBytes)
		}
		got, err := LoadState(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if got.Project.Goal != "original goal" || !got.Project.Started || !got.Project.HasAlert(flow.AlertAgentsDown) || len(got.Project.Wakes) != 0 {
			t.Fatalf("J1d J5-held project state mutated: %+v", got.Project)
		}
	})

	t.Run("J1d row 9: J5-held project with second signed-in coordinator candidate passes and keeps agents_down alert", func(t *testing.T) {
		paths := testPaths(t)
		repo := initGitRepo(t)
		if b, err := exec.Command("git", "-C", repo, "config", "--local", "sheepdog.projectId", "p-j5-fb").CombinedOutput(); err != nil {
			t.Fatalf("git config projectId: %v: %s", err, b)
		}
		now := time.Date(2026, 11, 23, 7, 0, 0, 0, time.UTC)
		st := flow.State{
			Seq: 5,
			Project: flow.Project{
				ID:      "p-j5-fb",
				Folder:  repo,
				Goal:    "original goal",
				Started: true,
				Alerts: map[string]flow.Alert{
					flow.AlertAgentsDown: {
						Kind:  flow.AlertAgentsDown,
						Cause: "claude is not signed in",
						At:    now,
						Fix:   "run: claude auth login",
						Rule:  "J5",
						Scope: "signed_out:claude",
					},
				},
			},
			Settings: flow.DefaultSettings(),
		}
		st.Settings.Models = map[string][]flow.Candidate{
			flow.RoleCoordinator: {
				{CLI: "claude", Model: "opus", Effort: "medium"},
				{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"},
			},
			flow.RoleWorker:   {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}},
			flow.RoleReviewer: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}},
		}
		statePath := paths.State("p-j5-fb")
		if err := SaveState(statePath, st); err != nil {
			t.Fatal(err)
		}

		eng := newGitTestEngine(t, paths, nil)
		fake := makeStartCheckFake(repo, startFake{
			"claude auth status --json": out(`{"loggedIn":false}`),
		})
		calls := wireStartCheck(eng, fake)

		if err := eng.Start(repo, "original goal", 0); err != nil {
			t.Fatalf("J1d fallback Engine.Start = %v, want nil", err)
		}
		if *calls != 1 {
			t.Fatalf("StartCheck calls = %d, want 1", *calls)
		}
		got, err := LoadState(statePath)
		if err != nil {
			t.Fatal(err)
		}
		if a, ok := got.Project.Alerts[flow.AlertAgentsDown]; !ok || a.Scope != "signed_out:claude" {
			t.Fatalf("J1d fallback cleared agents_down alert: %+v", got.Project.Alerts)
		}
	})
}

// TestStartChecksPRWorkflowBranches_KB_STA_18 verifies that checkPRWorkflow
// closes the branches:/branches-ignore: gap on on.pull_request and
// on.pull_request_target so workflows excluding the base branch fail instead of
// stalling at N35.
func TestStartChecksPRWorkflowBranches_KB_STA_18(t *testing.T) {
	cases := []struct {
		name     string
		workflow string
		over     startFake
		want     onboard.Status
	}{
		{
			name:     "branches excludes main (flow list)",
			workflow: "on:\n  pull_request:\n    branches: [other]\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "branches excludes main (block list)",
			workflow: "on:\n  pull_request:\n    branches:\n      - release\n      - dev\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "branches excludes main (flow map)",
			workflow: "on: { pull_request: { branches: [other] } }\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "pull_request_target branches excludes main",
			workflow: "on:\n  pull_request_target:\n    branches: [other]\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "branches glob does not match main",
			workflow: "on:\n  pull_request:\n    branches: ['release/*']\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "branches glob matches release/1.0 base",
			workflow: "on:\n  pull_request:\n    branches: ['release/*']\njobs: {}\n",
			over: startFake{
				"gh repo view o/r --json defaultBranchRef,viewerPermission": out(`{"defaultBranchRef":{"name":"release/1.0"},"viewerPermission":"WRITE"}`),
				"refs/remotes/origin/HEAD":                                  out("origin/release/1.0"),
				"rev-parse --verify --quiet origin/release/1.0":             out("abc1234\n"),
				"ls-tree --name-only origin/release/1.0 .github/workflows/": out(".github/workflows/ci.yml"),
				"show origin/release/1.0:.github/workflows/ci.yml":          out("on:\n  pull_request:\n    branches: ['release/*']\njobs: {}\n"),
				"gh api repos/o/r/branches/release%2F1.0":                   out(`{"protected":false}`),
				"gh api repos/o/r/rules/branches/release%2F1.0":             out("[]"),
			},
			want: onboard.OK,
		},
		{
			name:     "branches-ignore excludes main (flow list)",
			workflow: "on:\n  pull_request:\n    branches-ignore: [main]\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "branches-ignore excludes main (block list)",
			workflow: "on:\n  pull_request:\n    branches-ignore:\n      - main\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "branches-ignore excludes main (flow map)",
			workflow: "on: { pull_request: { branches-ignore: [main] } }\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "branches-ignore other branch allows main",
			workflow: "on:\n  pull_request:\n    branches-ignore: [docs]\njobs: {}\n",
			want:     onboard.OK,
		},
		{
			name:     "push branches filter does not affect unfiltered pull_request",
			workflow: "on:\n  push:\n    branches: [other]\n  pull_request:\njobs: {}\n",
			want:     onboard.OK,
		},
		{
			name:     "second workflow covers main when first excludes it",
			workflow: "on:\n  pull_request:\n    branches: [other]\njobs: {}\n",
			over: startFake{
				"ls-tree --name-only origin/main .github/workflows/": out(".github/workflows/a.yml\n.github/workflows/b.yml\n"),
				"show origin/main:.github/workflows/a.yml":           out("on:\n  pull_request:\n    branches: [other]\njobs: {}\n"),
				"show origin/main:.github/workflows/b.yml":           out(goodWorkflow),
			},
			want: onboard.OK,
		},
		{
			name:     "both branches and branches-ignore excluding main fails",
			workflow: "on:\n  pull_request:\n    branches: ['*']\n    branches-ignore: [main]\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "both branches and branches-ignore matching main fails",
			workflow: "on:\n  pull_request:\n    branches: [main]\n    branches-ignore: [main]\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "both branches and branches-ignore in flow map fails",
			workflow: "on: { pull_request: { branches: [main], branches-ignore: [main] } }\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "both branches and branches-ignore on same event is invalid in GitHub Actions",
			workflow: "on: { pull_request: { branches: [main], branches-ignore: [dev] } }\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "branches pattern with mid-segment **/ requires slash",
			workflow: "on:\n  pull_request:\n    branches: ['ma**/in']\njobs: {}\n",
			want:     onboard.Fail,
		},
		{
			name:     "branches-ignore pattern with mid-segment **/ requires slash",
			workflow: "on:\n  pull_request:\n    branches-ignore: ['ma**/in']\njobs: {}\n",
			want:     onboard.OK,
		},
		{
			name:     "branches pattern combining ** and character class matches",
			workflow: "on:\n  pull_request:\n    branches: ['**/v[0-9]']\njobs: {}\n",
			over: startFake{
				"gh repo view o/r --json defaultBranchRef,viewerPermission": out(`{"defaultBranchRef":{"name":"release/v1"},"viewerPermission":"WRITE"}`),
				"refs/remotes/origin/HEAD":                                  out("origin/release/v1"),
				"rev-parse --verify --quiet origin/release/v1":              out("abc1234\n"),
				"ls-tree --name-only origin/release/v1 .github/workflows/":  out(".github/workflows/ci.yml"),
				"show origin/release/v1:.github/workflows/ci.yml":           out("on:\n  pull_request:\n    branches: ['**/v[0-9]']\njobs: {}\n"),
				"gh api repos/o/r/branches/release%2Fv1":                    out(`{"protected":false}`),
				"gh api repos/o/r/rules/branches/release%2Fv1":              out("[]"),
			},
			want: onboard.OK,
		},
		{
			name:     "branches pattern combining **, prefix, and character class matches",
			workflow: "on:\n  pull_request:\n    branches: ['releases/**/v[0-9]']\njobs: {}\n",
			over: startFake{
				"gh repo view o/r --json defaultBranchRef,viewerPermission":      out(`{"defaultBranchRef":{"name":"releases/2026/v1"},"viewerPermission":"WRITE"}`),
				"refs/remotes/origin/HEAD":                                       out("origin/releases/2026/v1"),
				"rev-parse --verify --quiet origin/releases/2026/v1":             out("abc1234\n"),
				"ls-tree --name-only origin/releases/2026/v1 .github/workflows/": out(".github/workflows/ci.yml"),
				"show origin/releases/2026/v1:.github/workflows/ci.yml":          out("on:\n  pull_request:\n    branches: ['releases/**/v[0-9]']\njobs: {}\n"),
				"gh api repos/o/r/branches/releases%2F2026%2Fv1":                 out(`{"protected":false}`),
				"gh api repos/o/r/rules/branches/releases%2F2026%2Fv1":           out("[]"),
			},
			want: onboard.OK,
		},
		{
			name:     "branches negation combining ** and character class excludes matching branch",
			workflow: "on:\n  pull_request:\n    branches: ['**', '!**/v[0-9]']\njobs: {}\n",
			over: startFake{
				"gh repo view o/r --json defaultBranchRef,viewerPermission": out(`{"defaultBranchRef":{"name":"release/v1"},"viewerPermission":"WRITE"}`),
				"refs/remotes/origin/HEAD":                                  out("origin/release/v1"),
				"rev-parse --verify --quiet origin/release/v1":              out("abc1234\n"),
				"ls-tree --name-only origin/release/v1 .github/workflows/":  out(".github/workflows/ci.yml"),
				"show origin/release/v1:.github/workflows/ci.yml":           out("on:\n  pull_request:\n    branches: ['**', '!**/v[0-9]']\njobs: {}\n"),
				"gh api repos/o/r/branches/release%2Fv1":                    out(`{"protected":false}`),
				"gh api repos/o/r/rules/branches/release%2Fv1":              out("[]"),
			},
			want: onboard.Fail,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runStart(t, tc.workflow, tc.over)["pr workflow"]
			if r.Status != tc.want {
				t.Fatalf("pr workflow = %+v, want %v", r, tc.want)
			}
			if r.Status == onboard.Fail && r.Fix == "" {
				t.Errorf("pr workflow Fail missing fix: %+v", r)
			}
		})
	}
}

// TestStartChecksGHUnknownAndDoctorCleanup_KB_STA_18 verifies row 8.1's
// doctor.go cleanup: checkStartGH preserves unknown (<reason>) on timeout/exec
// error (never "not signed in"), Doctor() emits no duplicate "gh" line or
// "local-merge mode" prose, and doctor.go drops the stale launch.py comment.
func TestStartChecksGHUnknownAndDoctorCleanup_KB_STA_18(t *testing.T) {
	t.Run("checkStartGH timeout and exec error report unknown (<reason>), never not signed in", func(t *testing.T) {
		for _, tc := range []struct {
			name, want string
			out        CommandOutput
		}{
			{"timeout", "time", CommandOutput{Exit: -1, TimedOut: true}},
			{"exec error", "hc exec transport closed", CommandOutput{Exit: -1, Err: errors.New("hc exec transport closed")}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := runStart(t, goodWorkflow, startFake{
					"gh repo view o/r --json defaultBranchRef,viewerPermission": tc.out,
				})["gh push access"]
				if r.Status != onboard.Warn {
					t.Fatalf("gh push access (%s) status = %v, want Warn: %+v", tc.name, r.Status, r)
				}
				low := strings.ToLower(r.Detail)
				if !strings.Contains(low, "unknown (") || !strings.Contains(low, tc.want) {
					t.Fatalf("gh push access (%s) = %+v, want unknown (<reason>) containing %q", tc.name, r, tc.want)
				}
				if strings.Contains(low, "not signed in") || strings.Contains(low, "not logged in") {
					t.Fatalf("gh push access (%s) reported unknown as signed out: %+v", tc.name, r)
				}
			})
		}
	})

	t.Run("Doctor emits no duplicate gh check and no local-merge mode text", func(t *testing.T) {
		srv := hctest.New(t)
		srv.OnRun = func(term hc.Terminal, _ hc.RunParams) {
			srv.FinishExit(term.Address, "exited", 0)
		}
		tmp := t.TempDir()
		env := newFakeEnv("hc", "claude", "codexbar")
		env.handlers["claude auth"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: `{"loggedIn":true}`}
		}
		env.handlers["codexbar --version"] = func(string, []string, []string) CommandOutput {
			return CommandOutput{Stdout: "codexbar 1.0"}
		}
		rs := Doctor(context.Background(), DoctorOptions{
			Folder:     tmp,
			HCSocket:   srv.Path,
			TempDir:    tmp,
			LookPath:   env.lookPath,
			RunCommand: env.run,
		})
		for _, r := range rs {
			if r.Name == "gh" {
				t.Fatalf("Doctor emitted duplicate gh result: %+v", r)
			}
			if strings.Contains(strings.ToLower(r.Detail+" "+r.Fix), "local-merge mode") {
				t.Fatalf("Doctor emitted stale local-merge mode prose: %+v", r)
			}
		}
	})

	t.Run("doctor.go drops stale launch.py comment and local-merge mode prose", func(t *testing.T) {
		src, err := os.ReadFile("doctor.go")
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, stale := range []string{"launch.py", "local-merge mode"} {
			if strings.Contains(text, stale) {
				t.Errorf("doctor.go still contains stale %q prose", stale)
			}
		}
	})
}

func TestStartChecksDaemonLookupIsLexicalOnly_KB_ENV_7(t *testing.T) {
	dataDir := t.TempDir()
	realDir := t.TempDir()
	realRepo := filepath.Join(realDir, "real-repo")
	if err := os.MkdirAll(realRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	linkRepo := filepath.Join(realDir, "link-repo")
	if err := os.Symlink(realRepo, linkRepo); err != nil {
		t.Fatal(err)
	}
	st := flow.State{
		Project:  flow.Project{ID: "p-real", Folder: realRepo},
		Settings: flow.DefaultSettings(),
	}
	st.Settings.Models = map[string][]flow.Candidate{
		flow.RoleCoordinator: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}},
		flow.RoleWorker:      {{CLI: "claude", Model: "sonnet", Effort: "medium"}},
		flow.RoleReviewer:    {{CLI: "claude", Model: "opus", Effort: "medium"}},
	}
	if err := SaveState(Paths{DataDir: dataDir}.State("p-real"), st); err != nil {
		t.Fatal(err)
	}
	srv := hctest.New(t)
	fake := makeStartCheckFake(linkRepo, startFake{
		"codex login status": {Exit: 1, Stdout: "Not logged in\n"},
	})
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		c := fake.run(context.Background(), p.Cwd, nil, p.Argv...)
		return hc.ExecResult{Code: &c.Exit, Stdout: []byte(c.Stdout), Stderr: []byte(c.Stderr)}, nil
	}
	// With HCSocket set (daemon mode), StartChecks must NOT call EvalSymlinks locally
	// to resolve linkRepo -> realRepo; it should only match lexically on r.root.
	rs := StartChecks(context.Background(), DoctorOptions{Folder: linkRepo, DataDir: dataDir, HCSocket: srv.Path, RunCommand: fake.run})
	for _, r := range rs {
		if r.Name == "start codex" {
			t.Fatalf("daemon-mode StartChecks resolved symlink locally via EvalSymlinks: %+v", r)
		}
	}
}

func TestStartChecksGHMissingViaHC_KB_STA_18(t *testing.T) {
	t.Run("herdr-core spawn_failed missing gh fails", func(t *testing.T) {
		root := t.TempDir()
		srv := hctest.New(t)
		fake := makeStartCheckFake(root, nil)
		srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			if len(p.Argv) > 0 && p.Argv[0] == "gh" {
				return hc.ExecResult{}, &hc.Error{
					Kind:    "spawn_failed",
					Message: "cannot start gh: No such file or directory (os error 2)",
				}
			}
			c := fake.run(context.Background(), p.Cwd, nil, p.Argv...)
			return hc.ExecResult{Code: &c.Exit, Stdout: []byte(c.Stdout), Stderr: []byte(c.Stderr)}, nil
		}
		rs := StartChecks(context.Background(), DoctorOptions{Folder: root, HCSocket: srv.Path, RunCommand: fake.run})
		r := findResult(t, rs, "gh push access")
		if r.Status != onboard.Fail || !strings.Contains(strings.ToLower(r.Detail), "not installed") {
			t.Fatalf("missing gh via hc spawn_failed = %+v, want Fail (not installed)", r)
		}
	})

	t.Run("hctest.GitExec missing gh emits spawn_failed and fails", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		root := t.TempDir()
		srv := hctest.New(t)
		fake := makeStartCheckFake(root, nil)
		srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			if len(p.Argv) > 0 && p.Argv[0] == "gh" {
				return hctest.GitExec(p)
			}
			c := fake.run(context.Background(), p.Cwd, nil, p.Argv...)
			return hc.ExecResult{Code: &c.Exit, Stdout: []byte(c.Stdout), Stderr: []byte(c.Stderr)}, nil
		}
		timeout := 2.0
		_, execErr := hc.NewClient(srv.Path).Exec(context.Background(), hc.ExecParams{
			Argv:    []string{"gh", "--version"},
			Cwd:     root,
			Timeout: &timeout,
		})
		var rpcErr *hc.Error
		if !errors.As(execErr, &rpcErr) || rpcErr.Kind != "spawn_failed" || rpcErr.Message != "cannot start gh: No such file or directory (os error 2)" {
			t.Fatalf("hctest.GitExec missing gh = %#v, want spawn_failed capture", execErr)
		}

		rs := StartChecks(context.Background(), DoctorOptions{Folder: root, HCSocket: srv.Path, RunCommand: fake.run})
		r := findResult(t, rs, "gh push access")
		if r.Status != onboard.Fail || !strings.Contains(strings.ToLower(r.Detail), "not installed") {
			t.Fatalf("missing gh via hctest.GitExec = %+v, want Fail (not installed)", r)
		}
	})

	t.Run("hc method not found stays Warn unknown", func(t *testing.T) {
		root := t.TempDir()
		srv := hctest.New(t)
		fake := makeStartCheckFake(root, nil)
		srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			if len(p.Argv) > 0 && p.Argv[0] == "gh" {
				return hc.ExecResult{}, &hc.Error{Code: -32601, Message: "method not found"}
			}
			c := fake.run(context.Background(), p.Cwd, nil, p.Argv...)
			return hc.ExecResult{Code: &c.Exit, Stdout: []byte(c.Stdout), Stderr: []byte(c.Stderr)}, nil
		}
		rs := StartChecks(context.Background(), DoctorOptions{Folder: root, HCSocket: srv.Path, RunCommand: fake.run})
		r := findResult(t, rs, "gh push access")
		if r.Status != onboard.Warn || !strings.Contains(strings.ToLower(r.Detail), "unknown (") {
			t.Fatalf("method not found via hc = %+v, want Warn (unknown)", r)
		}
	})
}

func TestStartChecksGHLocalNotInstalled_KB_STA_18(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  CommandOutput
	}{
		{"exit 127", CommandOutput{Exit: 127}},
		{"exec.ErrNotFound", CommandOutput{Exit: -1, Err: exec.ErrNotFound}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runStart(t, goodWorkflow, startFake{
				"gh repo view o/r --json defaultBranchRef,viewerPermission": tc.out,
			})["gh push access"]
			if r.Status != onboard.Fail || !strings.Contains(strings.ToLower(r.Detail), "not installed") || r.Fix == "" {
				t.Fatalf("local gh %s = %+v, want Fail (not installed) with fix", tc.name, r)
			}
		})
	}
}

func TestEngineStartRefusalListsAllFailLinesAndNoWarnLines_KB_STA_18(t *testing.T) {
	paths := testPaths(t)
	repo := initGitRepo(t)
	eng := newGitTestEngine(t, paths, nil)
	failA := resFail("check-a", "first failure", "fix a")
	failB := resFail("check-b", "second failure", "fix b")
	warnR := resWarn("check-warn", "non-blocking warning", "fix warn")
	okR := resOK("check-ok", "all good")
	eng.StartCheck = func(context.Context, DoctorOptions) []onboard.Result {
		return []onboard.Result{okR, warnR, failA, failB}
	}
	err := eng.Start(repo, "verify refusal lines", 0)
	if err == nil {
		t.Fatal("Engine.Start succeeded, want refusal")
	}
	msg := err.Error()
	for _, want := range []string{failA.Line(), failB.Line()} {
		if !strings.Contains(msg, want) {
			t.Errorf("Engine.Start refusal missing Fail line %q:\n%s", want, msg)
		}
	}
	for _, unwanted := range []string{warnR.Line(), "check-warn", "non-blocking warning", okR.Line()} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("Engine.Start refusal unexpectedly included %q:\n%s", unwanted, msg)
		}
	}
}

func TestGlobToRegexpCharacterClassEdgeCases_KB_STA_18(t *testing.T) {
	t.Run("positive class must never match slash", func(t *testing.T) {
		for _, tc := range []struct {
			pat      string
			match    []string
			notMatch []string
		}{
			{
				pat:      "a[.-0]b",
				match:    []string{"a.b", "a0b"},
				notMatch: []string{"a/b"},
			},
			{
				pat:      "a[a/b]c",
				match:    []string{"aac", "abc"},
				notMatch: []string{"a/c"},
			},
		} {
			re, err := globToRegexp(tc.pat)
			if err != nil {
				t.Fatalf("globToRegexp(%q) error = %v", tc.pat, err)
			}
			for _, s := range tc.match {
				if !re.MatchString(s) {
					t.Errorf("globToRegexp(%q) (%s) did not match %q", tc.pat, re.String(), s)
				}
			}
			for _, s := range tc.notMatch {
				if re.MatchString(s) {
					t.Errorf("globToRegexp(%q) (%s) matched slash in %q", tc.pat, re.String(), s)
				}
				if matchBranchList([]string{tc.pat}, s) {
					t.Errorf("matchBranchList([%q], %q) = true, want false", tc.pat, s)
				}
			}
		}
	})

	t.Run("unescaped open bracket inside class compiles", func(t *testing.T) {
		for _, pat := range []string{"v[[:a]", "v[[:a:]", "v[[:alpha:]"} {
			re, err := globToRegexp(pat)
			if err != nil {
				t.Fatalf("globToRegexp(%q) failed to compile: %v", pat, err)
			}
			for _, s := range []string{"v[", "v:", "va"} {
				if !re.MatchString(s) {
					t.Errorf("globToRegexp(%q) (%s) did not match %q", pat, re.String(), s)
				}
			}
			for _, s := range []string{"vb", "v/"} {
				if re.MatchString(s) {
					t.Errorf("globToRegexp(%q) (%s) unexpectedly matched %q", pat, re.String(), s)
				}
			}
			if !matchBranchList([]string{pat}, "va") {
				t.Errorf("matchBranchList([%q], %q) = false, want true", pat, "va")
			}
		}
	})
}
