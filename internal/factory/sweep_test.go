package factory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

func setupSweepRepo(t *testing.T) (repo, id string, paths Paths, srv *hctest.Server, strayAddr, wt2Path, localSHA string) {
	t.Helper()
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo = filepath.Join(t.TempDir(), "repo")
	gitIn(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
	gitIn(t, t.TempDir(), "clone", bare, repo)
	gitIn(t, repo, "config", "user.email", "test@example.com")
	gitIn(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "a.txt")
	gitIn(t, repo, "commit", "-m", "init")
	gitIn(t, repo, "push", "-u", "origin", "main")
	gitIn(t, repo, "remote", "set-head", "origin", "main")

	var err error
	repo, id, err = fixtureGit(t).RepoProject(ctx, repo, true)
	if err != nil {
		t.Fatal(err)
	}
	paths = Paths{DataDir: t.TempDir()}

	wt2, err := fixtureGit(t).NewWorktree(ctx, repo, paths.Worktree(id, "T2", 1), "T2-1", "done task")
	if err != nil {
		t.Fatal(err)
	}
	wt2Path = wt2.Path
	// Leave an uncommitted file so N1 cleanup keeps the done task's worktree (KB-MRG-78).
	if err := os.WriteFile(filepath.Join(wt2Path, "dirty.txt"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "branch", "task/orphan-branch")

	for _, msg := range []string{"ahead 1", "ahead 2"} {
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte(msg+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, repo, "add", "a.txt")
		gitIn(t, repo, "commit", "-m", msg)
	}
	localSHA = gitIn(t, repo, "rev-parse", "refs/heads/main")

	srv = hctest.New(t)

	srv.OnExec = hctest.GitExec
	client := hc.NewClient(srv.Path)
	coord, err := client.Run(ctx, hc.RunParams{Name: coordName(id), Cwd: repo, Argv: []string{"true"}, Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.run": "run-coord"}})
	if err != nil {
		t.Fatal(err)
	}
	stray, err := client.Run(ctx, hc.RunParams{Name: "stray-agent", Cwd: repo, Argv: []string{"true"}, Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleWorker}})
	if err != nil {
		t.Fatal(err)
	}
	strayAddr = stray.Address

	st := flow.State{
		Project:  flow.Project{ID: id, Folder: repo, Goal: "test sweep", Started: true, RunID: coord.Address, Run: &flow.RunRecord{ID: "run-coord", Address: coord.Address, Role: flow.RoleCoordinator, State: flow.PhaseWorking}},
		Settings: flow.DefaultSettings(),
		Tasks: []flow.Task{
			{ID: "T2", Title: "feat: done", Done: &flow.Merged{MergedCommit: "deadbeef"}, Attempts: []flow.Attempt{{Branch: wt2.Branch, Worktree: wt2.Path, Acts: []flow.Act{{Key: flow.AskCleanup, Result: "done"}}}}},
		},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	return repo, id, paths, srv, strayAddr, wt2Path, localSHA
}

func TestEngineSweepWakesOnceAndThrottledByDefaultInterval(t *testing.T) {
	repo, id, paths, srv, strayAddr, wt2Path, localSHA := setupSweepRepo(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
	eng.Now = func() time.Time { return now }

	// Pass 1: sweep finds stray terminal, done T2 worktree, leftover branches (wt2 + orphan), local main ahead.
	eng.passProject(context.Background(), id, false)
	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	hasWake := func(prefix, substr string) bool {
		for _, w := range st.Project.Wakes {
			if strings.HasPrefix(w.Key, prefix) && strings.Contains(w.Why, substr) {
				return true
			}
		}
		return false
	}
	for _, want := range []struct{ prefix, substr string }{
		{"sweep@stray_terminal@", "a stray agent terminal " + strayAddr + " with factory labels"},
		{"sweep@worktree@" + wt2Path, "a leftover worktree " + wt2Path + " of done task T2"},
		{"sweep@branch@task/orphan-branch", "a leftover branch task/orphan-branch"},
		{"sweep@local_base@" + localSHA, "the local main is 2 commits ahead"},
		{"sweep@local_base@" + localSHA, "refs/remotes/origin/main"},
	} {
		if !hasWake(want.prefix, want.substr) {
			t.Errorf("wakes missing prefix=%q substr=%q (all=%+v)", want.prefix, want.substr, st.Project.Wakes)
		}
	}
	// Nothing was deleted or killed.
	if _, err := hc.NewClient(srv.Path).Get(context.Background(), strayAddr); err != nil {
		t.Fatalf("stray terminal was killed: %v", err)
	}
	if _, err := os.Stat(wt2Path); err != nil {
		t.Fatalf("leftover worktree was removed: %v", err)
	}

	// Add a new orphan branch at +4 minutes: sweep must NOT run yet (< default 5m).
	now = now.Add(4 * time.Minute)
	gitIn(t, repo, "branch", "task/later-orphan")
	eng.passProject(context.Background(), id, false)
	st, _ = LoadState(paths.State(id))
	for _, w := range st.Project.Wakes {
		if w.Key == "sweep@branch@task/later-orphan" {
			t.Fatalf("sweep ran before default 5 minutes elapsed: %+v", st.Project.Wakes)
		}
	}

	// At +5 minutes: sweep runs again; old items do NOT wake again, only the new branch wakes.
	prevCount := len(st.Project.Wakes)
	now = now.Add(time.Minute)
	eng.passProject(context.Background(), id, false)
	st, _ = LoadState(paths.State(id))
	if len(st.Project.Wakes) != prevCount+1 {
		t.Fatalf("wakes after 5m = %d (%+v), want %d (only 1 new wake)", len(st.Project.Wakes), st.Project.Wakes, prevCount+1)
	}
	if last := st.Project.Wakes[len(st.Project.Wakes)-1]; last.Key != "sweep@branch@task/later-orphan" {
		t.Fatalf("last wake = %+v, want task/later-orphan", last)
	}
}

func TestDoctorListsSweepLeftoversAsWarn(t *testing.T) {
	repo, _, paths, srv, strayAddr, wt2Path, _ := setupSweepRepo(t)
	srv.OnRun = func(term hc.Terminal, _ hc.RunParams) {
		srv.FinishExit(term.Address, "exited", 0)
	}
	env := newFakeEnv("hc", "claude", "gh", "codexbar")
	env.handlers["claude auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: `{"loggedIn":true}`} }
	env.handlers["gh auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 0} }
	env.handlers["git -C"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 1} }
	env.handlers["codexbar --version"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: "1.0"} }

	rs := Doctor(context.Background(), DoctorOptions{
		Folder:        repo,
		HCSocket:      srv.Path,
		DataDir:       paths.DataDir,
		Canary:        false,
		TempDir:       t.TempDir(),
		LookPath:      env.lookPath,
		RunCommand:    env.run,
		VolumeTimeout: time.Second,
		AuthTimeout:   time.Second,
	})
	if onboard.Failed(rs) {
		t.Fatalf("doctor unexpectedly failed on leftovers: %+v", rs)
	}
	var warns []string
	for _, r := range rs {
		if r.Status == onboard.Warn && strings.HasPrefix(r.Name, "leftover ") {
			warns = append(warns, r.Detail)
		}
	}
	joined := strings.Join(warns, "\n")
	for _, want := range []string{
		"a stray agent terminal " + strayAddr + " with factory labels",
		"a leftover worktree " + wt2Path + " of done task T2",
		"a leftover branch task/orphan-branch",
		"the local main is 2 commits ahead",
		"refs/remotes/origin/main",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("doctor leftover warnings missing %q:\n%s", want, joined)
		}
	}
}

func TestEngineSweepLogLineNamesItems(t *testing.T) {
	_, id, paths, srv, strayAddr, wt2Path, _ := setupSweepRepo(t)
	eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
	eng.passProject(context.Background(), id, false)

	lines, _, err := ProjectLog(context.Background(), paths, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sweepLine *LogLine
	for i := range lines {
		if lines[i].Rule == "sweep" {
			sweepLine = &lines[i]
		}
	}
	if sweepLine == nil {
		t.Fatalf("no sweep log line found in %+v", lines)
	}
	for _, want := range []string{
		"a stray agent terminal " + strayAddr + " with factory labels",
		"a leftover worktree " + wt2Path + " of done task T2",
		"a leftover branch task/orphan-branch",
		"the local main is 2 commits ahead",
		"refs/remotes/origin/main",
	} {
		if !strings.Contains(sweepLine.Decision, want) {
			t.Errorf("sweep log Decision = %q, want item summary %q", sweepLine.Decision, want)
		}
	}
}

func TestDoctorLiveCoordinatorReportsNoStray(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := filepath.Join(t.TempDir(), "repo")
	gitIn(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
	gitIn(t, t.TempDir(), "clone", bare, repo)
	gitIn(t, repo, "config", "user.email", "test@example.com")
	gitIn(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "a.txt")
	gitIn(t, repo, "commit", "-m", "init")
	gitIn(t, repo, "push", "-u", "origin", "main")
	gitIn(t, repo, "remote", "set-head", "origin", "main")
	repo, id, err := fixtureGit(t).RepoProject(ctx, repo, true)
	if err != nil {
		t.Fatal(err)
	}

	srv := hctest.New(t)

	srv.OnExec = hctest.GitExec
	client := hc.NewClient(srv.Path)
	coord, err := client.Run(ctx, hc.RunParams{
		Name:   coordName(id),
		Cwd:    repo,
		Argv:   []string{"true"},
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.run": "run-coord"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.OnRun = func(term hc.Terminal, _ hc.RunParams) {
		srv.FinishExit(term.Address, "exited", 0)
	}

	matchingPaths := Paths{DataDir: t.TempDir()}
	if err := SaveState(matchingPaths.State(id), flow.State{
		Project:  flow.Project{ID: id, Folder: repo, Started: true, RunID: coord.Address, Run: &flow.RunRecord{ID: "run-coord", Address: coord.Address, Role: flow.RoleCoordinator, State: flow.PhaseIdle}},
		Settings: flow.DefaultSettings(),
	}); err != nil {
		t.Fatal(err)
	}
	stoppedPaths := Paths{DataDir: t.TempDir()}
	if err := SaveState(stoppedPaths.State(id), flow.State{
		Project:  flow.Project{ID: id, Folder: repo, Started: false},
		Settings: flow.DefaultSettings(),
	}); err != nil {
		t.Fatal(err)
	}

	env := newFakeEnv("hc", "claude", "gh", "codexbar")
	env.handlers["claude auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: `{"loggedIn":true}`} }
	env.handlers["gh auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 0} }
	env.handlers["git -C"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 1} }
	env.handlers["codexbar --version"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: "1.0"} }

	runDoc := func(dataDir string) []string {
		rs := Doctor(ctx, DoctorOptions{
			Folder: repo, HCSocket: srv.Path, DataDir: dataDir, Canary: false,
			TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
			VolumeTimeout: time.Second, AuthTimeout: time.Second,
		})
		var strays []string
		for _, r := range rs {
			if r.Name == "leftover stray_terminal" {
				strays = append(strays, r.Detail)
			}
		}
		return strays
	}

	for _, tc := range []struct {
		name    string
		dataDir string
		want    int
	}{
		{"matching DataDir", matchingPaths.DataDir, 0},
		{"mismatched DataDir", t.TempDir(), 0},
		{"stopped project flags leftover coordinator", stoppedPaths.DataDir, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runDoc(tc.dataDir); len(got) != tc.want {
				t.Fatalf("Doctor(%s) stray_terminal = %v, want %d", tc.name, got, tc.want)
			}
		})
	}
}

func TestDoctorNoStateReportsUnknownNeverItemsAndRunsLocalBase(t *testing.T) {
	ctx := context.Background()
	makeRepo := func(initProject bool) (string, string) {
		bare := filepath.Join(t.TempDir(), "remote.git")
		repo := filepath.Join(t.TempDir(), "repo")
		gitIn(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
		gitIn(t, t.TempDir(), "clone", bare, repo)
		gitIn(t, repo, "config", "user.email", "test@example.com")
		gitIn(t, repo, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, repo, "add", "a.txt")
		gitIn(t, repo, "commit", "-m", "init")
		gitIn(t, repo, "push", "-u", "origin", "main")
		gitIn(t, repo, "remote", "set-head", "origin", "main")
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("ahead\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, repo, "add", "a.txt")
		gitIn(t, repo, "commit", "-m", "ahead 1")
		repo, id, err := fixtureGit(t).RepoProject(ctx, repo, initProject)
		if err != nil {
			t.Fatal(err)
		}
		return repo, id
	}

	env := newFakeEnv("hc", "claude", "gh", "codexbar")
	env.handlers["claude auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: `{"loggedIn":true}`} }
	env.handlers["gh auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 0} }
	env.handlers["git -C"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 1} }
	env.handlers["codexbar --version"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: "1.0"} }

	assertUnknownOnlyAndLocalBase := func(t *testing.T, rs []onboard.Result) {
		t.Helper()
		seenUnknown := map[string]bool{}
		var gotLocalBase bool
		for _, r := range rs {
			switch r.Name {
			case "sweep terminals", "sweep worktrees", "sweep branches":
				if r.Status != onboard.Warn || !strings.HasPrefix(r.Detail, "unknown: ") {
					t.Errorf("%s = %+v, want Warn unknown", r.Name, r)
				}
				seenUnknown[r.Name] = true
			case "leftover stray_terminal", "leftover worktree", "leftover branch":
				t.Errorf("unexpected ownership leftover without state: %+v", r)
			case "leftover local_base":
				if strings.Contains(r.Detail, "1 commit ahead") {
					gotLocalBase = true
				}
			}
		}
		for _, want := range []string{"sweep terminals", "sweep worktrees", "sweep branches"} {
			if !seenUnknown[want] {
				t.Errorf("missing %q warning in %+v", want, rs)
			}
		}
		if !gotLocalBase {
			t.Errorf("missing leftover local_base in %+v", rs)
		}
	}

	t.Run("mismatched or corrupt state with live worker and worktree", func(t *testing.T) {
		repo, id := makeRepo(true)
		realPaths := Paths{DataDir: t.TempDir()}
		if _, err := fixtureGit(t).NewWorktree(ctx, repo, realPaths.Worktree(id, "T1", 1), "T1", "active"); err != nil {
			t.Fatal(err)
		}
		srv := hctest.New(t)
		srv.OnExec = hctest.GitExec
		client := hc.NewClient(srv.Path)
		for _, p := range []hc.RunParams{
			{Name: coordName(id), Cwd: repo, Argv: []string{"true"}, Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator}},
			{Name: "worker-t1", Cwd: repo, Argv: []string{"true"}, Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleWorker}},
		} {
			if _, err := client.Run(ctx, p); err != nil {
				t.Fatal(err)
			}
		}
		srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 0) }

		corruptPaths := Paths{DataDir: t.TempDir()}
		if err := os.MkdirAll(filepath.Dir(corruptPaths.State(id)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(corruptPaths.State(id), []byte("{corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}

		for _, tc := range []struct{ name, dataDir string }{
			{"mismatched DataDir", t.TempDir()},
			{"corrupt state.json", corruptPaths.DataDir},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rs := Doctor(ctx, DoctorOptions{
					Folder: repo, HCSocket: srv.Path, DataDir: tc.dataDir, Canary: false,
					TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
					VolumeTimeout: time.Second, AuthTimeout: time.Second,
				})
				assertUnknownOnlyAndLocalBase(t, rs)
				if tc.name == "corrupt state.json" {
					for _, r := range rs {
						if r.Name == "sweep terminals" && !strings.Contains(r.Detail, "cannot load factory state") {
							t.Errorf("corrupt state.json detail = %q, want load error", r.Detail)
						}
					}
				}
			})
		}
	})

	t.Run("repo with no factory project and user task branch", func(t *testing.T) {
		repo, _ := makeRepo(false)
		gitIn(t, repo, "branch", "task/my-feature")
		srv := hctest.New(t)
		srv.OnExec = hctest.GitExec
		srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 0) }
		rs := Doctor(ctx, DoctorOptions{
			Folder: repo, HCSocket: srv.Path, DataDir: t.TempDir(), Canary: false,
			TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
			VolumeTimeout: time.Second, AuthTimeout: time.Second,
		})
		var skipCount int
		var gotLocalBase bool
		for _, r := range rs {
			switch r.Name {
			case "sweep":
				if r.Status != onboard.Skip || r.Detail != "leftover checks skipped: this folder has no factory project yet" {
					t.Errorf("sweep = %+v, want Skip with no-project message", r)
				}
				skipCount++
			case "sweep terminals", "sweep worktrees", "sweep branches":
				t.Errorf("unexpected sweep warning without factory project: %+v", r)
			case "leftover stray_terminal", "leftover worktree", "leftover branch":
				t.Errorf("unexpected ownership leftover without factory project: %+v", r)
			case "leftover local_base":
				if strings.Contains(r.Detail, "1 commit ahead") {
					gotLocalBase = true
				}
			}
		}
		if skipCount != 1 {
			t.Errorf("sweep skip count = %d, want 1 (%+v)", skipCount, rs)
		}
		if !gotLocalBase {
			t.Errorf("missing leftover local_base in %+v", rs)
		}
	})

	t.Run("no git project id with damaged state.json", func(t *testing.T) {
		repo, _ := makeRepo(false)
		corruptPaths := Paths{DataDir: t.TempDir()}
		if err := os.MkdirAll(filepath.Dir(corruptPaths.State("proj-corrupt")), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(corruptPaths.State("proj-corrupt"), []byte("{corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		srv := hctest.New(t)
		srv.OnExec = hctest.GitExec
		srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 0) }
		rs := Doctor(ctx, DoctorOptions{
			Folder: repo, HCSocket: srv.Path, DataDir: corruptPaths.DataDir, Canary: false,
			TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
			VolumeTimeout: time.Second, AuthTimeout: time.Second,
		})
		var warnCount int
		var gotLocalBase bool
		for _, r := range rs {
			switch r.Name {
			case "sweep":
				if r.Status != onboard.Warn || !strings.Contains(r.Detail, "load project proj-corrupt") {
					t.Errorf("sweep = %+v, want Warn with load project error", r)
				}
				warnCount++
			case "sweep terminals", "sweep worktrees", "sweep branches":
				t.Errorf("unexpected per-probe sweep result: %+v", r)
			case "leftover local_base":
				if strings.Contains(r.Detail, "1 commit ahead") {
					gotLocalBase = true
				}
			}
		}
		if warnCount != 1 {
			t.Errorf("sweep warn count = %d, want 1 (%+v)", warnCount, rs)
		}
		if !gotLocalBase {
			t.Errorf("missing leftover local_base in %+v", rs)
		}
	})
}

func TestDoctorUnreachableHCReportsSingleStrayTerminalWarn(t *testing.T) {
	ctx := context.Background()
	repo, _, paths, srv, _, _, _ := setupSweepRepo(t)
	srv.Close()

	for _, tc := range []struct {
		name    string
		hcOK    bool
		wantSub string
	}{
		{"hcOK false", false, "unknown: no hc client"},
		{"hc socket closed", true, "unknown: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := checkSweepLeftovers(ctx, DoctorOptions{
				Folder:      repo,
				HCSocket:    srv.Path,
				DataDir:     paths.DataDir,
				AuthTimeout: time.Second,
			}, tc.hcOK, repo)
			var termWarns []onboard.Result
			for _, r := range rs {
				if r.Name == "sweep terminals" {
					termWarns = append(termWarns, r)
				}
			}
			if len(termWarns) != 1 {
				t.Fatalf("sweep terminals results = %d (%+v), want 1", len(termWarns), termWarns)
			}
			r := termWarns[0]
			if r.Status != onboard.Warn || !strings.Contains(r.Detail, tc.wantSub) || strings.TrimPrefix(r.Detail, "unknown: ") == "" {
				t.Fatalf("sweep terminals = %+v, want Warn containing %q with reason", r, tc.wantSub)
			}
		})
	}
}

func setSweepMinutesInState(t *testing.T, statePath string, minutes int) {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	settings, ok := doc["settings"].(map[string]any)
	if !ok {
		settings = map[string]any{}
		doc["settings"] = settings
	}
	settings["sweep_minutes"] = minutes
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, append(out, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

type sweepListProxy struct {
	path     string
	failList atomic.Bool
}

func startSweepListProxy(t *testing.T, upstream string) *sweepListProxy {
	t.Helper()
	dir, err := os.MkdirTemp("", "hcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := &sweepListProxy{path: filepath.Join(dir, "hc.sock")}
	ln, err := net.Listen("unix", p.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(conn, upstream)
		}
	}()
	return p
}

func (p *sweepListProxy) serve(conn net.Conn, upstream string) {
	defer func() { _ = conn.Close() }()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return
	}
	var req struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
	}
	if json.Unmarshal(line, &req) != nil {
		return
	}
	if req.Method == "terminal.list" && p.failList.Load() {
		_ = json.NewEncoder(conn).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error":   map[string]any{"code": -32000, "message": "hc list unavailable"},
		})
		return
	}
	up, err := net.Dial("unix", upstream)
	if err != nil {
		return
	}
	defer func() { _ = up.Close() }()
	if _, err := up.Write(line); err != nil {
		return
	}
	reply, err := bufio.NewReader(up).ReadBytes('\n')
	if err == nil {
		_, _ = conn.Write(reply)
	}
}

func sweepLogLines(t *testing.T, paths Paths, id string) []LogLine {
	t.Helper()
	lines, _, err := ProjectLog(context.Background(), paths, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []LogLine
	for _, l := range lines {
		if l.Rule == "sweep" {
			out = append(out, l)
		}
	}
	return out
}

func clearProjectWakes(t *testing.T, paths Paths, id string) {
	t.Helper()
	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.Wakes = nil
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
}

func hasWakeMatching(wakes []flow.Wake, prefix, substr string) bool {
	for _, w := range wakes {
		if (prefix == "" || strings.HasPrefix(w.Key, prefix)) && strings.Contains(w.Why+" "+w.Facts, substr) {
			return true
		}
	}
	return false
}

// Slice 5b row 5b.10 (design.md 7.3, 10.1): the sweep runs immediately at
// daemon start regardless of a persisted sweep mark, and then repeats on the
// configured sweep_minutes interval (default 5 minutes, not a hardcoded 10m).
func TestEngineSweepRunsAtDaemonStartAndConfiguredInterval(t *testing.T) {
	t.Run("daemon start ignores recent persisted sweep mark", func(t *testing.T) {
		_, id, paths, srv, strayAddr, wt2Path, _ := setupSweepRepo(t)
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

		// Persist a recent "sweep" act from 1 minute ago (well inside both 5m and 10m).
		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		st.Project.Acts = append(st.Project.Acts, flow.Act{Key: "sweep", Result: "done", At: now.Add(-time.Minute)})
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}

		// Fresh Engine (daemon start): first pass must run the sweep immediately.
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Now = func() time.Time { return now }
		eng.passProject(context.Background(), id, false)

		st, err = LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		if !hasWakeMatching(st.Project.Wakes, "sweep@stray_terminal@", strayAddr) ||
			!hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wt2Path) {
			t.Fatalf("daemon start skipped sweep despite fresh Engine (wakes=%+v)", st.Project.Wakes)
		}
	})

	t.Run("default interval is 5 minutes", func(t *testing.T) {
		repo, id, paths, srv, _, _, _ := setupSweepRepo(t)
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Now = func() time.Time { return now }
		eng.passProject(context.Background(), id, false)

		gitIn(t, repo, "branch", "task/at-default-5m")
		now = now.Add(4 * time.Minute)
		eng.passProject(context.Background(), id, false)
		st, _ := LoadState(paths.State(id))
		if hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/at-default-5m") {
			t.Fatalf("sweep ran at +4m before default 5m interval: %+v", st.Project.Wakes)
		}

		now = now.Add(time.Minute)
		eng.passProject(context.Background(), id, false)
		st, _ = LoadState(paths.State(id))
		if !hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/at-default-5m") {
			t.Fatalf("sweep did not run at default +5m interval: %+v", st.Project.Wakes)
		}
	})

	t.Run("configured shorter interval (2 minutes)", func(t *testing.T) {
		repo, id, paths, srv, _, _, _ := setupSweepRepo(t)
		setSweepMinutesInState(t, paths.State(id), 2)
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Now = func() time.Time { return now }
		eng.passProject(context.Background(), id, false)

		gitIn(t, repo, "branch", "task/at-2m")
		now = now.Add(time.Minute)
		eng.passProject(context.Background(), id, false)
		st, _ := LoadState(paths.State(id))
		if hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/at-2m") {
			t.Fatalf("sweep ran at +1m with sweep_minutes=2: %+v", st.Project.Wakes)
		}

		now = now.Add(time.Minute)
		eng.passProject(context.Background(), id, false)
		st, _ = LoadState(paths.State(id))
		if !hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/at-2m") {
			t.Fatalf("sweep did not run at +2m with sweep_minutes=2: %+v", st.Project.Wakes)
		}
	})

	t.Run("configured longer interval (12 minutes)", func(t *testing.T) {
		repo, id, paths, srv, _, _, _ := setupSweepRepo(t)
		setSweepMinutesInState(t, paths.State(id), 12)
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Now = func() time.Time { return now }
		eng.passProject(context.Background(), id, false)

		gitIn(t, repo, "branch", "task/at-12m")
		now = now.Add(10 * time.Minute)
		eng.passProject(context.Background(), id, false)
		st, _ := LoadState(paths.State(id))
		if hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/at-12m") {
			t.Fatalf("sweep ran at +10m despite sweep_minutes=12: %+v", st.Project.Wakes)
		}

		now = now.Add(2 * time.Minute)
		eng.passProject(context.Background(), id, false)
		st, _ = LoadState(paths.State(id))
		if !hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/at-12m") {
			t.Fatalf("sweep did not run at +12m with sweep_minutes=12: %+v", st.Project.Wakes)
		}
	})
}

// Slice 5b row 5b.10 (design.md 5, 11.3; KB-WKT-5): a stray labelled terminal
// and a retained done-task worktree are each reported once per episode, left in
// place, survive daemon restart and transient unknown probes without repeating,
// and are reported again after disappearing and returning (or when an address
// is reused with a new creation time).
func TestEngineSweepOncePerEpisodeAndReusedAddress(t *testing.T) {
	t.Run("disappear and return after restart and unknown probe", func(t *testing.T) {
		ctx := context.Background()
		repo, id, paths, srv, origStray, wt2Path, _ := setupSweepRepo(t)
		srv.Remove(origStray)
		const strayAddr = "t9"
		srv.Add(hc.Terminal{
			Address: strayAddr, Name: "stray-ep", State: "live", Cwd: repo, CreatedMS: 1000,
			Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-stray-1"},
		})
		st0, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		wt2Branch := st0.Tasks[0].Attempts[0].Branch

		proxy := startSweepListProxy(t, srv.Path)
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		eng := newGitTestEngine(t, paths, hc.NewClient(proxy.path))
		eng.Now = func() time.Time { return now }

		// Pass 1 (startup): both stray terminal and retained done-task worktree reported once and left in place.
		eng.passProject(ctx, id, false)
		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		if !hasWakeMatching(st.Project.Wakes, "sweep@stray_terminal@", strayAddr) ||
			!hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wt2Path) {
			t.Fatalf("pass 1 missing expected wakes: %+v", st.Project.Wakes)
		}
		if _, err := hc.NewClient(srv.Path).Get(ctx, strayAddr); err != nil {
			t.Fatalf("stray terminal was killed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(wt2Path, "dirty.txt")); err != nil {
			t.Fatalf("retained worktree dirty file was removed: %v", err)
		}
		logsAfterPass1 := len(sweepLogLines(t, paths, id))
		if logsAfterPass1 != 1 {
			t.Fatalf("sweep log lines after pass 1 = %d, want 1", logsAfterPass1)
		}

		// Simulate coordinator received the wakes. Pass 2 (+5m): still present -> no repeat.
		clearProjectWakes(t, paths, id)
		now = now.Add(5 * time.Minute)
		eng.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		if len(st.Project.Wakes) != 0 || len(sweepLogLines(t, paths, id)) != logsAfterPass1 {
			t.Fatalf("pass 2 repeated active episode: wakes=%+v logs=%d", st.Project.Wakes, len(sweepLogLines(t, paths, id)))
		}

		// Daemon restart (+1m): startup sweep runs immediately, but active episode persists -> no repeat.
		now = now.Add(time.Minute)
		eng = newGitTestEngine(t, paths, hc.NewClient(proxy.path))
		eng.Now = func() time.Time { return now }
		eng.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		if len(st.Project.Wakes) != 0 || len(sweepLogLines(t, paths, id)) != logsAfterPass1 {
			t.Fatalf("restart repeated active episode: wakes=%+v logs=%d", st.Project.Wakes, len(sweepLogLines(t, paths, id)))
		}

		// Unknown probe on terminals must NOT clear the active episode for strayAddr.
		proxy.failList.Store(true)
		now = now.Add(5 * time.Minute)
		eng.passProject(ctx, id, false)
		proxy.failList.Store(false)
		now = now.Add(5 * time.Minute)
		eng.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		if hasWakeMatching(st.Project.Wakes, "sweep@stray_terminal@", strayAddr) {
			t.Fatalf("unknown terminals probe cleared active episode and re-reported %s: %+v", strayAddr, st.Project.Wakes)
		}

		// Disappear: remove stray terminal and worktree, then sweep -> episodes clear.
		srv.Remove(strayAddr)
		gitIn(t, repo, "worktree", "remove", "--force", wt2Path)
		now = now.Add(5 * time.Minute)
		eng.passProject(ctx, id, false)
		clearProjectWakes(t, paths, id)
		logsBeforeReturn := len(sweepLogLines(t, paths, id))

		// Reappear at the same address (same CreatedMS) and same worktree path -> BOTH reported again!
		srv.Add(hc.Terminal{
			Address: strayAddr, Name: "stray-ep", State: "live", Cwd: repo, CreatedMS: 1000,
			Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-stray-1"},
		})
		gitIn(t, repo, "worktree", "add", wt2Path, wt2Branch)
		if err := os.WriteFile(filepath.Join(wt2Path, "dirty.txt"), []byte("back\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		now = now.Add(5 * time.Minute)
		eng.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		if !hasWakeMatching(st.Project.Wakes, "sweep@stray_terminal@", strayAddr) {
			t.Fatalf("stray terminal did not report again after disappearing and coming back (wakes=%+v)", st.Project.Wakes)
		}
		if !hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wt2Path) {
			t.Fatalf("retained worktree did not report again after disappearing and coming back (wakes=%+v)", st.Project.Wakes)
		}
		if len(sweepLogLines(t, paths, id)) <= logsBeforeReturn {
			t.Fatalf("reappearance did not append a new sweep log line")
		}
		if _, err := hc.NewClient(srv.Path).Get(ctx, strayAddr); err != nil {
			t.Fatalf("reappeared stray terminal was killed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(wt2Path, "dirty.txt")); err != nil {
			t.Fatalf("reappeared retained worktree was removed: %v", err)
		}
	})

	t.Run("reused address with new creation time reports immediately", func(t *testing.T) {
		ctx := context.Background()
		repo, id, paths, srv, origStray, _, _ := setupSweepRepo(t)
		srv.Remove(origStray)
		const strayAddr = "t9"
		srv.Add(hc.Terminal{
			Address: strayAddr, Name: "stray-1", State: "live", Cwd: repo, CreatedMS: 1000,
			Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-stray-1"},
		})

		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Now = func() time.Time { return now }

		// Pass 1: report t9 (CreatedMS 1000).
		eng.passProject(ctx, id, false)
		st, _ := LoadState(paths.State(id))
		if !hasWakeMatching(st.Project.Wakes, "sweep@stray_terminal@", strayAddr) {
			t.Fatalf("pass 1 missing stray wake for %s: %+v", strayAddr, st.Project.Wakes)
		}

		// Replace t9 directly with a new CreatedMS (2000) without any empty pass in between.
		clearProjectWakes(t, paths, id)
		logsBeforeReuse := len(sweepLogLines(t, paths, id))
		srv.Add(hc.Terminal{
			Address: strayAddr, Name: "stray-2", State: "live", Cwd: repo, CreatedMS: 2000,
			Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-stray-2"},
		})
		now = now.Add(10 * time.Minute) // beyond both 5m and legacy 10m so failure isolates identity/episode handling
		eng.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		if !hasWakeMatching(st.Project.Wakes, "sweep@stray_terminal@", strayAddr) {
			t.Fatalf("reused address %s with new CreatedMS 2000 did not report again (wakes=%+v)", strayAddr, st.Project.Wakes)
		}
		if len(sweepLogLines(t, paths, id)) <= logsBeforeReuse {
			t.Fatalf("reused address with new CreatedMS did not append a new sweep log line")
		}
		if _, err := hc.NewClient(srv.Path).Get(ctx, strayAddr); err != nil {
			t.Fatalf("reused stray terminal was killed: %v", err)
		}

		// Next sweep with the same reused terminal (CreatedMS 2000) still present -> no repeat.
		clearProjectWakes(t, paths, id)
		logsAfterReuse := len(sweepLogLines(t, paths, id))
		now = now.Add(5 * time.Minute)
		eng.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		if hasWakeMatching(st.Project.Wakes, "sweep@stray_terminal@", strayAddr) || len(sweepLogLines(t, paths, id)) != logsAfterReuse {
			t.Fatalf("same reused terminal (CreatedMS 2000) repeated on next sweep: wakes=%+v", st.Project.Wakes)
		}
	})
}

// Slice 5b row 5b.10 (design.md 11.3; KB-WKT-5): a done task whose cleanup is
// still pending or retrying is not reported as retained, including across a
// daemon restart between done (N2) and cleanup (N1).
func TestEngineSweepRestartBetweenDoneAndCleanup(t *testing.T) {
	setupDoneCleanup := func(t *testing.T, dirty, persistedSweep bool, acts []flow.Act) (id, wtPath string, paths Paths, srv *hctest.Server, now time.Time) {
		t.Helper()
		ctx := context.Background()
		repo, _ := initGitRepoWithRemote(t)
		githubOrigin(t, repo)
		bindRepoFakeGH(t, repo)
		var err error
		repo, id, err = fixtureGit(t).RepoProject(ctx, repo, true)
		if err != nil {
			t.Fatal(err)
		}
		paths = testPaths(t)
		wt, err := fixtureGit(t).NewWorktree(ctx, repo, paths.Worktree(id, "T2", 1), "T2-1", "done task")
		if err != nil {
			t.Fatal(err)
		}
		wtPath = wt.Path
		if dirty {
			if err := os.WriteFile(filepath.Join(wtPath, "dirty.txt"), []byte("keep me\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// Canary orphan branch proves the startup sweep actually ran on restart.
		gitIn(t, repo, "branch", "task/canary-orphan")

		srv = hctest.New(t)
		srv.OnExec = hctest.GitExec
		client := hc.NewClient(srv.Path)
		coord, err := client.Run(ctx, hc.RunParams{
			Name: coordName(id), Cwd: repo, Argv: []string{"true"},
			Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.run": "run-coord"},
		})
		if err != nil {
			t.Fatal(err)
		}
		now = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		var projActs []flow.Act
		if persistedSweep {
			projActs = []flow.Act{{Key: "sweep", Result: "done", At: now.Add(-time.Minute)}}
		}
		st := flow.State{
			Project: flow.Project{
				ID: id, Folder: repo, Goal: "test cleanup", Started: true, RunID: coord.Address,
				Run:  &flow.RunRecord{ID: "run-coord", Address: coord.Address, Role: flow.RoleCoordinator, State: flow.PhaseWorking, CreatedMS: coord.CreatedMS},
				Acts: projActs,
			},
			Settings: flow.DefaultSettings(),
			Tasks: []flow.Task{{
				ID: "T2", Title: "feat: done", Done: &flow.Merged{MergedCommit: "deadbeef"},
				Attempts: []flow.Attempt{{Branch: wt.Branch, Worktree: wtPath, Acts: acts}},
			}},
		}
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}
		return id, wtPath, paths, srv, now
	}

	t.Run("restart between done and cleanup with clean worktree", func(t *testing.T) {
		id, wtPath, paths, srv, now := setupDoneCleanup(t, false, true, nil)
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Now = func() time.Time { return now }
		eng.passProject(context.Background(), id, false)

		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		if !hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/canary-orphan") {
			t.Fatalf("startup sweep did not run after restart (wakes=%+v)", st.Project.Wakes)
		}
		if hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wtPath) {
			t.Fatalf("done task worktree mis-reported as retained before N1 cleanup ran: %+v", st.Project.Wakes)
		}
		if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
			t.Fatalf("N1 cleanup did not remove clean worktree %s (stat err=%v)", wtPath, err)
		}
	})

	t.Run("restart while cleanup is retrying after transient failure", func(t *testing.T) {
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		acts := []flow.Act{{Key: flow.AskCleanup, Result: "failed", Class: flow.ClassTransient, Err: "temporary error", At: now.Add(-10 * time.Second)}}
		id, wtPath, paths, srv, now := setupDoneCleanup(t, false, false, acts)
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Now = func() time.Time { return now }
		eng.passProject(context.Background(), id, false)

		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		if !hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/canary-orphan") {
			t.Fatalf("startup sweep did not run after restart (wakes=%+v)", st.Project.Wakes)
		}
		if hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wtPath) {
			t.Fatalf("done task worktree mis-reported while cleanup is retrying under backoff: %+v", st.Project.Wakes)
		}

		// Past EnsureBackoffMinutes (1m): N1 retries cleanup and removes the clean worktree.
		now = now.Add(2 * time.Minute)
		eng.passProject(context.Background(), id, false)
		if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
			t.Fatalf("retried N1 cleanup did not remove clean worktree %s (stat err=%v)", wtPath, err)
		}
	})

	t.Run("restart between done and cleanup with dirty worktree reports only after cleanup retains it", func(t *testing.T) {
		id, wtPath, paths, srv, now := setupDoneCleanup(t, true, false, nil)
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Now = func() time.Time { return now }

		// Pass 1 (restart): startup sweep must NOT report wtPath yet; N1 runs and retains dirty wtPath.
		eng.passProject(context.Background(), id, false)
		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		if !hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/canary-orphan") {
			t.Fatalf("startup sweep did not run after restart (wakes=%+v)", st.Project.Wakes)
		}
		if hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wtPath) {
			t.Fatalf("dirty done-task worktree reported before cleanup finished: %+v", st.Project.Wakes)
		}
		if _, err := os.Stat(filepath.Join(wtPath, "dirty.txt")); err != nil {
			t.Fatalf("dirty worktree was removed by N1: %v", err)
		}

		// Pass 2 (+5m): cleanup is now done and worktree was retained -> sweep reports it once and leaves it intact.
		now = now.Add(5 * time.Minute)
		eng.passProject(context.Background(), id, false)
		st, _ = LoadState(paths.State(id))
		if !hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wtPath) {
			t.Fatalf("retained dirty done-task worktree not reported on next sweep after cleanup finished: %+v", st.Project.Wakes)
		}
		if _, err := os.Stat(filepath.Join(wtPath, "dirty.txt")); err != nil {
			t.Fatalf("retained worktree was removed by sweep: %v", err)
		}
	})
}

// Slice 5b row 5b.10 (plan.md 5b.10; design.md 11.3; KB-ENV-9): the sweep and
// doctor list the shared Codex home (<data_dir>/factory/codex-home) with its
// location, file count and gathered size, once per episode, without ever
// reading or leaking any auth file contents, and report unknown with its reason
// if the directory cannot be read.
func TestEngineSweepAndDoctorListCodexHomeWithoutReadingAuth(t *testing.T) {
	const authSecret = "TEST-CODEX-AUTH-SECRET-DO-NOT-READ"
	fileCountRe := regexp.MustCompile(`\b2\s+files?\b`)
	size500Re := regexp.MustCompile(`\b500\s*(B|bytes?)\b`)
	anyCountRe := regexp.MustCompile(`\b[0-9]+\s+files?\b`)
	anySizeRe := regexp.MustCompile(`\b[0-9]+(\.[0-9]+)?\s*(B|bytes?|KiB|KB|MiB|MB)\b`)

	populateCodexHome := func(t *testing.T, paths Paths) (codexHome string) {
		t.Helper()
		codexHome = filepath.Join(paths.DataDir, "factory", "codex-home")
		ownerDir := t.TempDir()
		ownerAuth := filepath.Join(ownerDir, "auth.json")
		// Pad auth.json to 4096 bytes so following the symlink would skew size away from 500 bytes.
		payload := authSecret + strings.Repeat("x", 4096-len(authSecret))
		if err := os.WriteFile(ownerAuth, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(codexHome, "sessions", "2026", "11", "21"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(codexHome, "log"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(ownerAuth, filepath.Join(codexHome, "auth.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(codexHome, "sessions", "2026", "11", "21", "rollout-1.jsonl"), bytes.Repeat([]byte("a"), 300), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(codexHome, "log", "codex.log"), bytes.Repeat([]byte("b"), 200), 0o600); err != nil {
			t.Fatal(err)
		}
		return codexHome
	}

	mentionsCodexHome := func(s, dir string) bool {
		if strings.Contains(s, dir) {
			return true
		}
		if real, err := filepath.EvalSymlinks(dir); err == nil && real != "" && strings.Contains(s, real) {
			return true
		}
		return false
	}
	mentionsCodexLabel := func(s string) bool {
		low := strings.ToLower(s)
		return strings.Contains(low, "codex_home") || strings.Contains(low, "codex-home") || strings.Contains(low, "codex home")
	}

	t.Run("engine sweep lists location, count and size once per episode without leaking auth", func(t *testing.T) {
		_, id, paths, srv, _, _, _ := setupSweepRepo(t)
		codexHome := populateCodexHome(t, paths)

		var logBuf bytes.Buffer
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Now = func() time.Time { return now }
		eng.Log = logging.NewJSONLLogger(&logBuf)

		// Pass 1: Codex home reported with location, 2 files, 500 bytes, no auth secret, untouched.
		eng.passProject(context.Background(), id, false)
		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		var codexWake *flow.Wake
		for i := range st.Project.Wakes {
			if mentionsCodexHome(st.Project.Wakes[i].Why+" "+st.Project.Wakes[i].Facts, codexHome) {
				codexWake = &st.Project.Wakes[i]
				break
			}
		}
		if codexWake == nil {
			t.Fatalf("no sweep wake listing codex home %s (wakes=%+v)", codexHome, st.Project.Wakes)
		}
		text := codexWake.Why + " " + codexWake.Facts
		if !fileCountRe.MatchString(text) || !size500Re.MatchString(text) {
			t.Errorf("codex home wake = %q, want 2 files and 500 B/bytes (without following auth symlink)", text)
		}
		sLines := sweepLogLines(t, paths, id)
		if len(sLines) != 1 || !mentionsCodexHome(sLines[0].Decision, codexHome) {
			t.Fatalf("sweep log lines = %+v, want 1 line naming %s", sLines, codexHome)
		}
		if strings.Contains(text, authSecret) || strings.Contains(sLines[0].Decision, authSecret) || strings.Contains(logBuf.String(), authSecret) {
			t.Fatalf("auth secret leaked into sweep output")
		}
		if _, err := os.Stat(filepath.Join(codexHome, "sessions", "2026", "11", "21", "rollout-1.jsonl")); err != nil {
			t.Fatalf("sweep removed codex session file: %v", err)
		}

		// Pass 2 (+5m) and restart (+6m): still present -> NOT reported again.
		clearProjectWakes(t, paths, id)
		now = now.Add(5 * time.Minute)
		eng.passProject(context.Background(), id, false)
		st, _ = LoadState(paths.State(id))
		for _, w := range st.Project.Wakes {
			if mentionsCodexHome(w.Why+" "+w.Facts, codexHome) {
				t.Fatalf("codex home repeated on pass 2: %+v", st.Project.Wakes)
			}
		}
		now = now.Add(time.Minute)
		eng = newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Now = func() time.Time { return now }
		eng.passProject(context.Background(), id, false)
		st, _ = LoadState(paths.State(id))
		for _, w := range st.Project.Wakes {
			if mentionsCodexHome(w.Why+" "+w.Facts, codexHome) {
				t.Fatalf("codex home repeated after restart: %+v", st.Project.Wakes)
			}
		}

		// Disappear and reappear: reported again.
		if err := os.RemoveAll(codexHome); err != nil {
			t.Fatal(err)
		}
		now = now.Add(5 * time.Minute)
		eng.passProject(context.Background(), id, false)
		clearProjectWakes(t, paths, id)

		if err := os.MkdirAll(codexHome, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(codexHome, "new.log"), bytes.Repeat([]byte("z"), 120), 0o600); err != nil {
			t.Fatal(err)
		}
		now = now.Add(5 * time.Minute)
		eng.passProject(context.Background(), id, false)
		st, _ = LoadState(paths.State(id))
		var returned bool
		for _, w := range st.Project.Wakes {
			if mentionsCodexHome(w.Why+" "+w.Facts, codexHome) {
				returned = true
			}
		}
		if !returned {
			t.Fatalf("codex home did not report again after disappearing and coming back: %+v", st.Project.Wakes)
		}
	})

	t.Run("unreadable auth.json is never opened", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses chmod 0000")
		}
		_, id, paths, srv, _, _, _ := setupSweepRepo(t)
		codexHome := populateCodexHome(t, paths)
		authPath := filepath.Join(codexHome, "auth.json")
		if err := os.Remove(authPath); err != nil {
			t.Fatal(err)
		}
		const divergedSecret = "TEST-CODEX-DIVERGED-SECRET"
		if err := os.WriteFile(authPath, []byte(divergedSecret), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(authPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(authPath, 0o600) })

		var logBuf bytes.Buffer
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.Log = logging.NewJSONLLogger(&logBuf)
		eng.passProject(context.Background(), id, false)

		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		var got string
		for _, w := range st.Project.Wakes {
			if mentionsCodexHome(w.Why+" "+w.Facts, codexHome) {
				got = w.Why + " " + w.Facts
			}
		}
		if got == "" {
			t.Fatalf("codex home with chmod-0000 auth.json was not listed (wakes=%+v, log=%s)", st.Project.Wakes, logBuf.String())
		}
		if !anyCountRe.MatchString(got) || !anySizeRe.MatchString(got) {
			t.Errorf("codex home line = %q, want file count and size", got)
		}
		if strings.Contains(got, divergedSecret) || strings.Contains(logBuf.String(), divergedSecret) {
			t.Fatalf("diverged auth.json secret leaked")
		}
		if strings.Contains(strings.ToLower(logBuf.String()), "permission denied") {
			t.Fatalf("sweep attempted to open unreadable auth.json: %s", logBuf.String())
		}
	})

	t.Run("doctor lists shared codex home", func(t *testing.T) {
		repo, _, paths, srv, _, _, _ := setupSweepRepo(t)
		codexHome := populateCodexHome(t, paths)
		srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 0) }
		env := newFakeEnv("hc", "claude", "gh", "codexbar")
		env.handlers["claude auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: `{"loggedIn":true}`} }
		env.handlers["gh auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 0} }
		env.handlers["git -C"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 1} }
		env.handlers["codexbar --version"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: "1.0"} }

		rs := Doctor(context.Background(), DoctorOptions{
			Folder: repo, HCSocket: srv.Path, DataDir: paths.DataDir, Canary: false,
			TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
			VolumeTimeout: time.Second, AuthTimeout: time.Second,
		})
		var found string
		for _, r := range rs {
			if strings.Contains(r.Detail, authSecret) {
				t.Fatalf("doctor leaked auth secret: %+v", r)
			}
			if mentionsCodexHome(r.Detail, codexHome) {
				found = r.Detail
			}
		}
		if found == "" {
			t.Fatalf("doctor did not list shared codex home %s in %+v", codexHome, rs)
		}
		if !fileCountRe.MatchString(found) || !size500Re.MatchString(found) {
			t.Errorf("doctor codex home detail = %q, want 2 files and 500 B/bytes", found)
		}
	})

	t.Run("unreadable codex home reports unknown with reason", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses chmod 0000")
		}
		repo, id, paths, srv, _, _, _ := setupSweepRepo(t)
		codexHome := filepath.Join(paths.DataDir, "factory", "codex-home")
		locked := filepath.Join(codexHome, "sessions")
		if err := os.MkdirAll(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.passProject(context.Background(), id, false)
		sLines := sweepLogLines(t, paths, id)
		var sawUnknown bool
		for _, l := range sLines {
			text := l.Decision + " " + l.Why
			low := strings.ToLower(text)
			if (mentionsCodexLabel(text) || mentionsCodexHome(text, codexHome)) && strings.Contains(low, "unknown") && strings.Contains(low, "permission denied") {
				sawUnknown = true
			}
		}
		if !sawUnknown {
			t.Fatalf("engine sweep log lines = %+v, want unknown codex home with permission denied reason", sLines)
		}

		srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 0) }
		env := newFakeEnv("hc", "claude", "gh", "codexbar")
		env.handlers["claude auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: `{"loggedIn":true}`} }
		env.handlers["gh auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 0} }
		env.handlers["git -C"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 1} }
		env.handlers["codexbar --version"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: "1.0"} }
		rs := Doctor(context.Background(), DoctorOptions{
			Folder: repo, HCSocket: srv.Path, DataDir: paths.DataDir, Canary: false,
			TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
			VolumeTimeout: time.Second, AuthTimeout: time.Second,
		})
		var sawDocUnknown bool
		for _, r := range rs {
			text := r.Name + " " + r.Detail
			low := strings.ToLower(text)
			if r.Status == onboard.Warn && (mentionsCodexLabel(text) || mentionsCodexHome(text, codexHome)) && strings.Contains(low, "unknown") && strings.Contains(low, "permission denied") {
				sawDocUnknown = true
			}
		}
		if !sawDocUnknown {
			t.Fatalf("doctor results = %+v, want Warn for unknown codex home with permission denied reason", rs)
		}
	})

	t.Run("absent codex home is not reported or unknown", func(t *testing.T) {
		_, id, paths, srv, _, _, _ := setupSweepRepo(t)
		codexHome := filepath.Join(paths.DataDir, "factory", "codex-home")
		eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng.passProject(context.Background(), id, false)
		st, _ := LoadState(paths.State(id))
		for _, w := range st.Project.Wakes {
			if mentionsCodexHome(w.Why+" "+w.Facts, codexHome) || mentionsCodexLabel(w.Key+" "+w.Why+" "+w.Facts) {
				t.Fatalf("absent codex home produced a wake: %+v", w)
			}
		}
		for _, l := range sweepLogLines(t, paths, id) {
			if mentionsCodexHome(l.Decision+" "+l.Why, codexHome) || mentionsCodexLabel(l.Decision+" "+l.Why) {
				t.Fatalf("absent codex home logged in sweep: %+v", l)
			}
		}
	})
}

// Slice 5b row 5b.10 (design.md 11.3; KB-ENV-9): an unknown sweep probe result
// is logged to ProjectLog with the probe name and reason once per episode
// (surviving daemon restart without repeating), never treated as clean, and
// logged again only after the probe recovers and fails again.
func TestEngineSweepUnknownLogsReasonOncePerEpisode(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := filepath.Join(t.TempDir(), "repo")
	gitIn(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
	gitIn(t, t.TempDir(), "clone", bare, repo)
	gitIn(t, repo, "config", "user.email", "test@example.com")
	gitIn(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "a.txt")
	gitIn(t, repo, "commit", "-m", "init")
	gitIn(t, repo, "push", "-u", "origin", "main")
	gitIn(t, repo, "remote", "set-head", "origin", "main")
	repo, id, err := fixtureGit(t).RepoProject(ctx, repo, true)
	if err != nil {
		t.Fatal(err)
	}
	paths := Paths{DataDir: t.TempDir()}

	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	client := hc.NewClient(srv.Path)
	coord, err := client.Run(ctx, hc.RunParams{
		Name: coordName(id), Cwd: repo, Argv: []string{"true"},
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.run": "run-coord"},
	})
	if err != nil {
		t.Fatal(err)
	}
	st := flow.State{
		Project:  flow.Project{ID: id, Folder: repo, Goal: "test unknown", Started: true, RunID: coord.Address, Run: &flow.RunRecord{ID: "run-coord", Address: coord.Address, Role: flow.RoleCoordinator, State: flow.PhaseWorking, CreatedMS: coord.CreatedMS}},
		Settings: flow.DefaultSettings(),
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	proxy := startSweepListProxy(t, srv.Path)
	proxy.failList.Store(true)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	eng := newGitTestEngine(t, paths, hc.NewClient(proxy.path))
	eng.Now = func() time.Time { return now }

	// Pass 1: terminals probe fails -> ProjectLog records one sweep unknown line naming probe and reason.
	eng.passProject(ctx, id, false)
	sLines := sweepLogLines(t, paths, id)
	if len(sLines) != 1 {
		t.Fatalf("sweep log lines on unknown terminals = %d (%+v), want 1", len(sLines), sLines)
	}
	lineText := strings.ToLower(sLines[0].Decision + " " + sLines[0].Why)
	if !strings.Contains(lineText, "terminals") || !strings.Contains(lineText, "unknown") || !strings.Contains(lineText, "hc list unavailable") {
		t.Fatalf("sweep unknown log line = %+v, want probe 'terminals', 'unknown', and reason 'hc list unavailable'", sLines[0])
	}
	st, _ = LoadState(paths.State(id))
	if hasWakeMatching(st.Project.Wakes, "sweep@stray_terminal@", "") {
		t.Fatalf("unknown terminals probe emitted stray_terminal wake: %+v", st.Project.Wakes)
	}

	// Pass 2 (+5m, still failing): NOT logged a second time.
	now = now.Add(5 * time.Minute)
	eng.passProject(ctx, id, false)
	if got := len(sweepLogLines(t, paths, id)); got != 1 {
		t.Fatalf("sweep unknown log lines after pass 2 = %d, want 1", got)
	}

	// Restart (+1m, still failing): startup sweep runs, still same episode -> NOT logged again.
	now = now.Add(time.Minute)
	eng = newGitTestEngine(t, paths, hc.NewClient(proxy.path))
	eng.Now = func() time.Time { return now }
	eng.passProject(ctx, id, false)
	if got := len(sweepLogLines(t, paths, id)); got != 1 {
		t.Fatalf("sweep unknown log lines after restart = %d, want 1", got)
	}

	// Recover (+5m): probe succeeds -> clears episode.
	proxy.failList.Store(false)
	now = now.Add(5 * time.Minute)
	eng.passProject(ctx, id, false)

	// Fail again (+5m): new episode -> logged a second time!
	proxy.failList.Store(true)
	now = now.Add(5 * time.Minute)
	eng.passProject(ctx, id, false)
	sLines = sweepLogLines(t, paths, id)
	if len(sLines) != 2 {
		t.Fatalf("sweep unknown log lines after recovery and second failure = %d (%+v), want 2", len(sLines), sLines)
	}
}
