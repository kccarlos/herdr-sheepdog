package factory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
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

// Round 2, finding 1 (Codex blocker 1): the Codex-home inventory must run
// through the bounded herdr-core runner (not direct os.Lstat/filepath.WalkDir
// in the daemon), without holding the engine lock across it, and timeouts or
// refusals from herdr-core must surface as unknown with their reason.
func TestEngineAndDoctorCodexHomeThroughHCWithoutEngineLock(t *testing.T) {
	const authSecret = "TEST-HC-CODEX-SECRET"
	fileCountRe := regexp.MustCompile(`\b2\s+files?\b`)
	size500Re := regexp.MustCompile(`\b500\s*(B|bytes?)\b`)

	setupRepo := func(t *testing.T) (repo, id string, paths Paths) {
		t.Helper()
		ctx := context.Background()
		repo, _ = initGitRepoWithRemote(t)
		var err error
		repo, id, err = fixtureGit(t).RepoProject(ctx, repo, true)
		if err != nil {
			t.Fatal(err)
		}
		paths = testPaths(t)
		return repo, id, paths
	}

	populate := func(t *testing.T, paths Paths) string {
		t.Helper()
		codexHome := filepath.Join(paths.DataDir, "factory", "codex-home")
		ownerDir := t.TempDir()
		ownerAuth := filepath.Join(ownerDir, "auth.json")
		if err := os.WriteFile(ownerAuth, []byte(authSecret+strings.Repeat("x", 4096)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(codexHome, "sessions"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(ownerAuth, filepath.Join(codexHome, "auth.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(codexHome, "sessions", "r1.jsonl"), bytes.Repeat([]byte("a"), 300), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(codexHome, "codex.log"), bytes.Repeat([]byte("b"), 200), 0o600); err != nil {
			t.Fatal(err)
		}
		return codexHome
	}

	isCodexExec := func(p hc.ExecParams, codexHome string) bool {
		if p.Cwd == codexHome {
			return true
		}
		return slices.ContainsFunc(p.Argv, func(a string) bool {
			return strings.Contains(a, codexHome) || strings.Contains(a, "codex-home")
		})
	}

	t.Run("engine sweep reads codex home via bounded hc without holding engine lock", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses chmod 0000")
		}
		ctx := context.Background()
		repo, id, paths := setupRepo(t)
		codexHome := populate(t, paths)
		if err := os.Chmod(codexHome, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(codexHome, 0o700) })

		srv := hctest.New(t)
		eng := New(paths, hc.NewClient(srv.Path))
		eng.probeLaunch = func(context.Context, []string) flow.ResolveFacts { return flow.ResolveFacts{} }
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		eng.Now = func() time.Time { return now }

		client := hc.NewClient(srv.Path)
		coord, err := client.Run(ctx, hc.RunParams{
			Name: coordName(id), Cwd: repo, Argv: []string{"true"},
			Labels: testCoordLabels(id, "run-coord"),
		})
		if err != nil {
			t.Fatal(err)
		}
		st := flow.State{
			Project:  flow.Project{ID: id, Folder: repo, Goal: "codex hc", Started: true, RunID: coord.Address, Run: &flow.RunRecord{ID: "run-coord", Address: coord.Address, Role: flow.RoleCoordinator, State: flow.PhaseWorking, CreatedMS: coord.CreatedMS}},
			Settings: flow.DefaultSettings(),
		}
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}

		var chmodMu sync.Mutex
		var codexCalls, unboundedCalls, lockedCalls atomic.Int32
		srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			if isCodexExec(p, codexHome) {
				codexCalls.Add(1)
				if p.Timeout == nil || *p.Timeout <= 0 {
					unboundedCalls.Add(1)
				}
				if eng.mu.TryLock() {
					eng.mu.Unlock()
				} else {
					lockedCalls.Add(1)
				}
				chmodMu.Lock()
				_ = os.Chmod(codexHome, 0o700)
				res, execErr := hctest.GitExec(p)
				_ = os.Chmod(codexHome, 0o000)
				chmodMu.Unlock()
				return res, execErr
			}
			return hctest.GitExec(p)
		}

		eng.passProject(ctx, id, false)

		if codexCalls.Load() == 0 {
			t.Fatalf("codex-home probe bypassed herdr-core (0 machine.exec calls for %s)", codexHome)
		}
		if unboundedCalls.Load() != 0 {
			t.Fatalf("%d codex-home machine.exec call(s) had no bounded timeout", unboundedCalls.Load())
		}
		if lockedCalls.Load() != 0 {
			t.Fatalf("engine lock was held across %d codex-home machine.exec call(s)", lockedCalls.Load())
		}
		loaded, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		var codexText string
		for _, w := range loaded.Project.Wakes {
			if strings.Contains(w.Why+" "+w.Facts, codexHome) {
				codexText = w.Why + " " + w.Facts
				break
			}
		}
		if codexText == "" || !fileCountRe.MatchString(codexText) || !size500Re.MatchString(codexText) {
			t.Fatalf("codex-home wake = %q (all wakes=%+v), want 2 files and 500 B/bytes via hc", codexText, loaded.Project.Wakes)
		}
		if strings.Contains(codexText, authSecret) {
			t.Fatalf("auth secret leaked: %q", codexText)
		}
	})

	t.Run("doctor reads codex home via hc", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses chmod 0000")
		}
		ctx := context.Background()
		repo, _, paths := setupRepo(t)
		codexHome := populate(t, paths)
		if err := os.Chmod(codexHome, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(codexHome, 0o700) })

		srv := hctest.New(t)
		srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 0) }
		var chmodMu sync.Mutex
		var docCalls atomic.Int32
		srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			if isCodexExec(p, codexHome) {
				docCalls.Add(1)
				chmodMu.Lock()
				_ = os.Chmod(codexHome, 0o700)
				res, execErr := hctest.GitExec(p)
				_ = os.Chmod(codexHome, 0o000)
				chmodMu.Unlock()
				return res, execErr
			}
			return hctest.GitExec(p)
		}
		env := newFakeEnv("hc", "claude", "gh", "codexbar")
		env.handlers["claude auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: `{"loggedIn":true}`} }
		env.handlers["gh auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 0} }
		env.handlers["git -C"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 1} }
		env.handlers["codexbar --version"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: "1.0"} }

		rs := Doctor(ctx, DoctorOptions{
			Folder: repo, HCSocket: srv.Path, DataDir: paths.DataDir, Canary: false,
			TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
			VolumeTimeout: time.Second, AuthTimeout: time.Second,
		})
		if docCalls.Load() == 0 {
			t.Fatalf("doctor codex-home probe bypassed herdr-core (0 machine.exec calls)")
		}
		var detail string
		for _, r := range rs {
			if r.Name == "leftover codex_home" {
				detail = r.Detail
			}
		}
		if detail == "" || !fileCountRe.MatchString(detail) || !size500Re.MatchString(detail) {
			t.Fatalf("doctor codex_home = %q (all=%+v), want 2 files and 500 B/bytes", detail, rs)
		}
	})

	t.Run("hc timeout or permission refusal yields unknown with reason", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			res     hc.ExecResult
			err     error
			wantSub string
		}{
			{
				name:    "timeout",
				res:     hc.ExecResult{TimedOut: true},
				wantSub: "timed out",
			},
			{
				name:    "refused",
				err:     errors.New("access refused by herdr-core"),
				wantSub: "access refused",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ctx := context.Background()
				repo, id, paths := setupRepo(t)
				codexHome := populate(t, paths) // readable locally so direct WalkDir would falsely succeed

				srv := hctest.New(t)
				srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 0) }
				srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
					if isCodexExec(p, codexHome) {
						return tc.res, tc.err
					}
					return hctest.GitExec(p)
				}
				client := hc.NewClient(srv.Path)
				coord, err := client.Run(ctx, hc.RunParams{
					Name: coordName(id), Cwd: repo, Argv: []string{"true"},
					Labels: testCoordLabels(id, "run-coord"),
				})
				if err != nil {
					t.Fatal(err)
				}
				st := flow.State{
					Project:  flow.Project{ID: id, Folder: repo, Goal: "codex hc fail", Started: true, RunID: coord.Address, Run: &flow.RunRecord{ID: "run-coord", Address: coord.Address, Role: flow.RoleCoordinator, State: flow.PhaseWorking, CreatedMS: coord.CreatedMS}},
					Settings: flow.DefaultSettings(),
				}
				if err := SaveState(paths.State(id), st); err != nil {
					t.Fatal(err)
				}
				eng := New(paths, client)
				eng.probeLaunch = func(context.Context, []string) flow.ResolveFacts { return flow.ResolveFacts{} }
				eng.passProject(ctx, id, false)

				loaded, _ := LoadState(paths.State(id))
				for _, w := range loaded.Project.Wakes {
					if strings.Contains(w.Key, "codex_home") {
						t.Fatalf("failed hc probe emitted codex_home wake: %+v", w)
					}
				}
				var sawEngineUnknown bool
				for _, l := range sweepLogLines(t, paths, id) {
					low := strings.ToLower(l.Decision + " " + l.Why)
					if strings.Contains(low, "codex_home") && strings.Contains(low, "unknown") && strings.Contains(low, tc.wantSub) {
						sawEngineUnknown = true
					}
				}
				if !sawEngineUnknown {
					t.Fatalf("engine sweep logs = %+v, want codex_home unknown containing %q", sweepLogLines(t, paths, id), tc.wantSub)
				}

				env := newFakeEnv("hc", "claude", "gh", "codexbar")
				env.handlers["claude auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: `{"loggedIn":true}`} }
				env.handlers["gh auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 0} }
				env.handlers["git -C"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 1} }
				env.handlers["codexbar --version"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: "1.0"} }
				rs := Doctor(ctx, DoctorOptions{
					Folder: repo, HCSocket: srv.Path, DataDir: paths.DataDir, Canary: false,
					TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
					VolumeTimeout: time.Second, AuthTimeout: time.Second,
				})
				var sawDocUnknown bool
				for _, r := range rs {
					if r.Name == "sweep codex_home" && r.Status == onboard.Warn && strings.HasPrefix(r.Detail, "unknown: ") && strings.Contains(strings.ToLower(r.Detail), tc.wantSub) {
						sawDocUnknown = true
					}
				}
				if !sawDocUnknown {
					t.Fatalf("doctor results = %+v, want Warn 'sweep codex_home' unknown containing %q", rs, tc.wantSub)
				}
			})
		}
	})
}

// Round 2, finding 2 (Codex major 2, Carbon minor a & 3): a freshly created
// Codex home (empty or holding only the auth symlink) is listed with its path,
// 0 files, and 0 bytes; a symlink or regular file at that location reports
// unknown with a reason; and Doctor still reports the home when the project
// folder check fails early.
func TestSweepAndDoctorEmptyAndNonDirCodexHome(t *testing.T) {
	const authSecret = "TEST-EMPTY-CODEX-SECRET"
	zeroFilesRe := regexp.MustCompile(`\b0\s+files?\b`)
	zeroBytesRe := regexp.MustCompile(`\b0\s*(B|bytes?)\b`)

	fakeDoctorEnv := func() *fakeEnv {
		env := newFakeEnv("hc", "claude", "gh", "codexbar")
		env.handlers["claude auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: `{"loggedIn":true}`} }
		env.handlers["gh auth"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 0} }
		env.handlers["git -C"] = func(string, []string, []string) CommandOutput { return CommandOutput{Exit: 1} }
		env.handlers["codexbar --version"] = func(string, []string, []string) CommandOutput { return CommandOutput{Stdout: "1.0"} }
		return env
	}

	for _, tc := range []struct {
		name     string
		authOnly bool
	}{
		{"fresh codex home with only auth symlink", true},
		{"empty codex home directory", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo, id, paths, srv, _, _, _ := setupSweepRepo(t)
			srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 0) }
			codexHome := filepath.Join(paths.DataDir, "factory", "codex-home")
			if err := os.MkdirAll(codexHome, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.authOnly {
				ownerAuth := filepath.Join(t.TempDir(), "auth.json")
				if err := os.WriteFile(ownerAuth, []byte(authSecret+strings.Repeat("x", 4096)), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(ownerAuth, filepath.Join(codexHome, "auth.json")); err != nil {
					t.Fatal(err)
				}
			}

			eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
			eng.passProject(ctx, id, false)

			st, err := LoadState(paths.State(id))
			if err != nil {
				t.Fatal(err)
			}
			var wakeText string
			for _, w := range st.Project.Wakes {
				if strings.Contains(w.Why+" "+w.Facts, codexHome) {
					wakeText = w.Why + " " + w.Facts
					break
				}
			}
			if wakeText == "" || !zeroFilesRe.MatchString(wakeText) || !zeroBytesRe.MatchString(wakeText) {
				t.Fatalf("codex home wake = %q (all=%+v), want path %s with 0 files and 0 B/bytes", wakeText, st.Project.Wakes, codexHome)
			}
			if strings.Contains(wakeText, authSecret) {
				t.Fatalf("auth secret leaked: %q", wakeText)
			}

			env := fakeDoctorEnv()
			rs := Doctor(ctx, DoctorOptions{
				Folder: repo, HCSocket: srv.Path, DataDir: paths.DataDir, Canary: false,
				TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
				VolumeTimeout: time.Second, AuthTimeout: time.Second,
			})
			var docDetail string
			for _, r := range rs {
				if r.Name == "leftover codex_home" {
					docDetail = r.Detail
				}
			}
			if docDetail == "" || !strings.Contains(docDetail, codexHome) || !zeroFilesRe.MatchString(docDetail) || !zeroBytesRe.MatchString(docDetail) {
				t.Fatalf("doctor leftover codex_home = %q (all=%+v), want path %s with 0 files and 0 B/bytes", docDetail, rs, codexHome)
			}
		})
	}

	for _, kind := range []string{"symlink", "regular file"} {
		t.Run("non-directory codex home ("+kind+") reports unknown with reason", func(t *testing.T) {
			ctx := context.Background()
			repo, id, paths, srv, _, _, _ := setupSweepRepo(t)
			srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 0) }
			codexHome := filepath.Join(paths.DataDir, "factory", "codex-home")
			if err := os.MkdirAll(filepath.Dir(codexHome), 0o700); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(t.TempDir(), codexHome); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(codexHome, []byte("not a directory\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
			eng.passProject(ctx, id, false)

			st, _ := LoadState(paths.State(id))
			for _, w := range st.Project.Wakes {
				if strings.Contains(w.Key, "codex_home") {
					t.Fatalf("non-directory codex_home emitted leftover wake: %+v", w)
				}
			}
			var sawUnknown bool
			for _, l := range sweepLogLines(t, paths, id) {
				low := strings.ToLower(l.Decision + " " + l.Why)
				if strings.Contains(low, "codex_home") && strings.Contains(low, "unknown:") && strings.TrimSpace(strings.SplitN(low, "unknown:", 2)[1]) != "" {
					sawUnknown = true
				}
			}
			if !sawUnknown {
				t.Fatalf("engine sweep log = %+v, want codex_home unknown with reason", sweepLogLines(t, paths, id))
			}

			env := fakeDoctorEnv()
			rs := Doctor(ctx, DoctorOptions{
				Folder: repo, HCSocket: srv.Path, DataDir: paths.DataDir, Canary: false,
				TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
				VolumeTimeout: time.Second, AuthTimeout: time.Second,
			})
			var docWarn bool
			for _, r := range rs {
				if r.Name == "leftover codex_home" {
					t.Fatalf("doctor reported non-directory codex_home as leftover instead of unknown: %+v", r)
				}
				if r.Name == "sweep codex_home" && r.Status == onboard.Warn && strings.HasPrefix(r.Detail, "unknown: ") && strings.TrimSpace(strings.TrimPrefix(r.Detail, "unknown: ")) != "" {
					docWarn = true
				}
			}
			if !docWarn {
				t.Fatalf("doctor results = %+v, want Warn 'sweep codex_home' with 'unknown: <reason>'", rs)
			}
		})
	}

	t.Run("doctor still reports codex home when project folder check fails early", func(t *testing.T) {
		ctx := context.Background()
		makeHome := func(t *testing.T) (Paths, string) {
			t.Helper()
			paths := testPaths(t)
			codexHome := filepath.Join(paths.DataDir, "factory", "codex-home")
			if err := os.MkdirAll(codexHome, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(codexHome, "note.log"), bytes.Repeat([]byte("z"), 100), 0o600); err != nil {
				t.Fatal(err)
			}
			return paths, codexHome
		}

		t.Run("non-git folder", func(t *testing.T) {
			paths, codexHome := makeHome(t)
			srv := hctest.New(t)
			srv.OnExec = hctest.GitExec
			srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 0) }
			env := fakeDoctorEnv()
			rs := Doctor(ctx, DoctorOptions{
				Folder: t.TempDir(), HCSocket: srv.Path, DataDir: paths.DataDir, Canary: false,
				TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
				VolumeTimeout: time.Second, AuthTimeout: time.Second,
			})
			var found bool
			for _, r := range rs {
				if r.Name == "leftover codex_home" && strings.Contains(r.Detail, codexHome) {
					found = true
				}
			}
			if !found {
				t.Fatalf("doctor omitted codex_home on non-git folder: %+v", rs)
			}
		})

		t.Run("project volume check failed", func(t *testing.T) {
			paths, codexHome := makeHome(t)
			notDir := filepath.Join(t.TempDir(), "not-a-dir.txt")
			if err := os.WriteFile(notDir, []byte("file\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			srv := hctest.New(t)
			srv.OnExec = hctest.GitExec
			srv.OnRun = func(term hc.Terminal, _ hc.RunParams) { srv.FinishExit(term.Address, "exited", 2) }
			env := fakeDoctorEnv()
			rs := Doctor(ctx, DoctorOptions{
				Folder: notDir, HCSocket: srv.Path, DataDir: paths.DataDir, Canary: false,
				TempDir: t.TempDir(), LookPath: env.lookPath, RunCommand: env.run,
				VolumeTimeout: time.Second, AuthTimeout: time.Second,
			})
			var volFailed, found bool
			for _, r := range rs {
				if r.Name == "project volume" && r.Status == onboard.Fail {
					volFailed = true
				}
				if r.Name == "leftover codex_home" && strings.Contains(r.Detail, codexHome) {
					found = true
				}
			}
			if !volFailed {
				t.Fatalf("expected project volume check to fail: %+v", rs)
			}
			if !found {
				t.Fatalf("doctor omitted codex_home when project volume check failed: %+v", rs)
			}
		})
	})
}

// Round 2, finding 3 (Codex major 3, Carbon major 1B): one episode per probe
// until a success, even when failure texts alternate; and no-origin /
// not-a-git-repository failures go through the same persisted episode path.
func TestEngineSweepUnknownOneEpisodeUntilSuccess(t *testing.T) {
	setupStartedRepo := func(t *testing.T) (repo, bare, id string, paths Paths, srv *hctest.Server) {
		t.Helper()
		ctx := context.Background()
		bare = filepath.Join(t.TempDir(), "remote.git")
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
		paths = testPaths(t)
		srv = hctest.New(t)
		srv.OnExec = hctest.GitExec
		client := hc.NewClient(srv.Path)
		coord, err := client.Run(ctx, hc.RunParams{
			Name: coordName(id), Cwd: repo, Argv: []string{"true"},
			Labels: testCoordLabels(id, "run-coord"),
		})
		if err != nil {
			t.Fatal(err)
		}
		st := flow.State{
			Project:  flow.Project{ID: id, Folder: repo, Goal: "episode test", Started: true, RunID: coord.Address, Run: &flow.RunRecord{ID: "run-coord", Address: coord.Address, Role: flow.RoleCoordinator, State: flow.PhaseWorking, CreatedMS: coord.CreatedMS}},
			Settings: flow.DefaultSettings(),
		}
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}
		return repo, bare, id, paths, srv
	}

	startErrMsgProxy := func(t *testing.T, upstream string, msg *atomic.Value) string {
		t.Helper()
		dir, err := os.MkdirTemp("", "hce")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		sock := filepath.Join(dir, "hc.sock")
		ln, err := net.Listen("unix", sock)
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
				go func(c net.Conn) {
					defer func() { _ = c.Close() }()
					line, err := bufio.NewReader(c).ReadBytes('\n')
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
					if errText, _ := msg.Load().(string); req.Method == "terminal.list" && errText != "" {
						_ = json.NewEncoder(c).Encode(map[string]any{
							"jsonrpc": "2.0",
							"id":      req.ID,
							"error":   map[string]any{"code": -32000, "message": errText},
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
					if reply, err := bufio.NewReader(up).ReadBytes('\n'); err == nil {
						_, _ = c.Write(reply)
					}
				}(conn)
			}
		}()
		return sock
	}

	t.Run("alternating failure texts across three sweeps log once until recovery", func(t *testing.T) {
		ctx := context.Background()
		_, _, id, paths, srv := setupStartedRepo(t)
		var errMsg atomic.Value
		proxySock := startErrMsgProxy(t, srv.Path, &errMsg)

		var logBuf bytes.Buffer
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		eng := newGitTestEngine(t, paths, hc.NewClient(proxySock))
		eng.Now = func() time.Time { return now }
		eng.Log = logging.NewJSONLLogger(&logBuf)

		for i, text := range []string{"connection refused", "deadline exceeded", "connection refused"} {
			errMsg.Store(text)
			if i > 0 {
				now = now.Add(5 * time.Minute)
			}
			eng.passProject(ctx, id, false)
			if got := len(sweepLogLines(t, paths, id)); got != 1 {
				t.Fatalf("after sweep %d (%q): sweep log lines = %d, want 1 (%+v)", i+1, text, got, sweepLogLines(t, paths, id))
			}
			if n := strings.Count(logBuf.String(), "factory.sweep_unknown"); n != 1 {
				t.Fatalf("after sweep %d (%q): factory.sweep_unknown events = %d, want 1", i+1, text, n)
			}
		}

		// Successful observation clears the episode.
		errMsg.Store("")
		now = now.Add(5 * time.Minute)
		eng.passProject(ctx, id, false)

		// Next failure starts a new episode and logs once more.
		errMsg.Store("deadline exceeded")
		now = now.Add(5 * time.Minute)
		eng.passProject(ctx, id, false)
		if got := len(sweepLogLines(t, paths, id)); got != 2 {
			t.Fatalf("after recovery and second outage: sweep log lines = %d, want 2", got)
		}
		if n := strings.Count(logBuf.String(), "factory.sweep_unknown"); n != 2 {
			t.Fatalf("after recovery and second outage: factory.sweep_unknown events = %d, want 2", n)
		}
	})

	t.Run("no-origin outage uses persisted episode path across restart and recovery", func(t *testing.T) {
		ctx := context.Background()
		repo, bare, id, paths, srv := setupStartedRepo(t)
		gitIn(t, repo, "remote", "remove", "origin")

		var logBuf1 bytes.Buffer
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		eng1 := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng1.Now = func() time.Time { return now }
		eng1.Log = logging.NewJSONLLogger(&logBuf1)

		// Pass 1: logs one unknown sweep line and one factory.sweep_unknown event, persisted in state.json.
		eng1.passProject(ctx, id, false)
		sLines := sweepLogLines(t, paths, id)
		if len(sLines) != 1 || !strings.Contains(strings.ToLower(sLines[0].Decision+" "+sLines[0].Why), "local_base") {
			t.Fatalf("pass 1 sweep log lines = %+v, want 1 local_base unknown line", sLines)
		}
		if n := strings.Count(logBuf1.String(), "factory.sweep_unknown"); n != 1 {
			t.Fatalf("pass 1 factory.sweep_unknown events = %d, want 1", n)
		}
		st, _ := LoadState(paths.State(id))
		if st.Project.SweepUnknown["local_base"] == "" {
			t.Fatalf("pass 1 did not persist SweepUnknown[local_base]: %+v", st.Project.SweepUnknown)
		}

		// Restart during the same outage: logs nothing new.
		var logBuf2 bytes.Buffer
		now = now.Add(time.Minute)
		eng2 := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng2.Now = func() time.Time { return now }
		eng2.Log = logging.NewJSONLLogger(&logBuf2)
		eng2.passProject(ctx, id, false)
		if got := len(sweepLogLines(t, paths, id)); got != 1 {
			t.Fatalf("restart during same outage added sweep log line: %d", got)
		}
		if n := strings.Count(logBuf2.String(), "factory.sweep_unknown"); n != 0 {
			t.Fatalf("restart during same outage re-emitted factory.sweep_unknown: %s", logBuf2.String())
		}

		// Recover origin -> ends the episode.
		gitIn(t, repo, "remote", "add", "origin", bare)
		gitIn(t, repo, "fetch", "origin")
		gitIn(t, repo, "remote", "set-head", "origin", "main")
		now = now.Add(5 * time.Minute)
		eng2.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		if st.Project.SweepUnknown["local_base"] != "" {
			t.Fatalf("recovery did not clear SweepUnknown[local_base]: %+v", st.Project.SweepUnknown)
		}

		// Remove origin again on the same daemon -> new episode logs again.
		gitIn(t, repo, "remote", "remove", "origin")
		now = now.Add(5 * time.Minute)
		eng2.passProject(ctx, id, false)
		if got := len(sweepLogLines(t, paths, id)); got != 2 {
			t.Fatalf("second no-origin episode sweep log lines = %d, want 2", got)
		}
		if n := strings.Count(logBuf2.String(), "factory.sweep_unknown"); n != 1 {
			t.Fatalf("second no-origin episode factory.sweep_unknown events = %d, want 1", n)
		}
	})

	t.Run("not-a-git-repository outage uses persisted episode path across restart and recovery", func(t *testing.T) {
		ctx := context.Background()
		repo, _, id, paths, srv := setupStartedRepo(t)
		gitDir := filepath.Join(repo, ".git")
		hiddenGitDir := filepath.Join(repo, ".git.hidden")
		if err := os.Rename(gitDir, hiddenGitDir); err != nil {
			t.Fatal(err)
		}

		var logBuf1 bytes.Buffer
		now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
		eng1 := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng1.Now = func() time.Time { return now }
		eng1.Log = logging.NewJSONLLogger(&logBuf1)

		eng1.passProject(ctx, id, false)
		if got := len(sweepLogLines(t, paths, id)); got != 1 {
			t.Fatalf("pass 1 sweep log lines = %d, want 1 (%+v)", got, sweepLogLines(t, paths, id))
		}
		if n := strings.Count(logBuf1.String(), "factory.sweep_unknown"); n == 0 {
			t.Fatalf("pass 1 missing factory.sweep_unknown event")
		}
		st, _ := LoadState(paths.State(id))
		if len(st.Project.SweepUnknown) == 0 {
			t.Fatalf("pass 1 did not persist SweepUnknown for not-a-git-repository")
		}

		// Restart during the same outage: logs nothing new.
		var logBuf2 bytes.Buffer
		now = now.Add(time.Minute)
		eng2 := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
		eng2.Now = func() time.Time { return now }
		eng2.Log = logging.NewJSONLLogger(&logBuf2)
		eng2.passProject(ctx, id, false)
		if got := len(sweepLogLines(t, paths, id)); got != 1 {
			t.Fatalf("restart during same outage added sweep log line: %d", got)
		}
		if n := strings.Count(logBuf2.String(), "factory.sweep_unknown"); n != 0 {
			t.Fatalf("restart during same outage re-emitted factory.sweep_unknown: %s", logBuf2.String())
		}

		// Restore .git -> ends the episode.
		if err := os.Rename(hiddenGitDir, gitDir); err != nil {
			t.Fatal(err)
		}
		now = now.Add(5 * time.Minute)
		eng2.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		if len(st.Project.SweepUnknown) != 0 {
			t.Fatalf("recovery did not clear SweepUnknown: %+v", st.Project.SweepUnknown)
		}

		// Hide .git again -> new episode logs again.
		if err := os.Rename(gitDir, hiddenGitDir); err != nil {
			t.Fatal(err)
		}
		now = now.Add(5 * time.Minute)
		eng2.passProject(ctx, id, false)
		if got := len(sweepLogLines(t, paths, id)); got != 2 {
			t.Fatalf("second not-a-git-repository episode sweep log lines = %d, want 2", got)
		}
		if n := strings.Count(logBuf2.String(), "factory.sweep_unknown"); n == 0 {
			t.Fatalf("second not-a-git-repository episode did not emit factory.sweep_unknown")
		}
	})
}

// Round 2, finding 4 (Codex major 4): a retained worktree moved away without
// `git worktree prune` (listed as prunable by git) counts as gone so its
// episode ends, and reappearing later starts a new episode.
func TestEngineSweepPrunableWorktreeEndsEpisodeAndReappearsAsNew(t *testing.T) {
	ctx := context.Background()
	repo, id, paths, srv, _, wt2Path, _ := setupSweepRepo(t)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
	eng.Now = func() time.Time { return now }

	// Pass 1: retained worktree wt2Path is reported.
	eng.passProject(ctx, id, false)
	st, _ := LoadState(paths.State(id))
	if !hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wt2Path) {
		t.Fatalf("pass 1 missing worktree wake for %s: %+v", wt2Path, st.Project.Wakes)
	}
	logsAfterPass1 := len(sweepLogLines(t, paths, id))
	clearProjectWakes(t, paths, id)

	// Move wt2Path away without pruning git worktree metadata.
	movedPath := wt2Path + ".moved"
	if err := os.Rename(wt2Path, movedPath); err != nil {
		t.Fatal(err)
	}
	if listOut := gitIn(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(listOut, "prunable") {
		t.Fatalf("expected git worktree list to show prunable entry:\n%s", listOut)
	}

	// Pass 2 (+5m): prunable worktree counts as gone, ending its episode while leaving git registration untouched.
	now = now.Add(5 * time.Minute)
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	for k := range st.Project.SweepItems {
		if strings.Contains(k, wt2Path) {
			t.Fatalf("stale prunable worktree kept episode active in SweepItems: %+v", st.Project.SweepItems)
		}
	}
	if listOut := gitIn(t, repo, "worktree", "list", "--porcelain"); !strings.Contains(listOut, "prunable") {
		t.Fatalf("sweep pruned git worktree metadata instead of leaving it untouched:\n%s", listOut)
	}

	// Move wt2Path back: new episode must report again!
	if err := os.Rename(movedPath, wt2Path); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if !hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wt2Path) {
		t.Fatalf("reappeared worktree %s did not report as a new episode (wakes=%+v)", wt2Path, st.Project.Wakes)
	}
	if len(sweepLogLines(t, paths, id)) <= logsAfterPass1 {
		t.Fatalf("reappeared worktree did not append a new sweep log line")
	}
}

// Round 2, finding 5 (Codex major 5): while the coordinator stays busy, an item
// that recovers and recurs at the same key delivers the new episode's facts to
// the coordinator when it becomes idle, rather than swallowing the new wake.
func TestEngineSweepHeldWakeDoesNotSwallowNextEpisode(t *testing.T) {
	ctx := context.Background()
	repo, _ := initGitRepoWithRemote(t)
	var err error
	repo, id, err := fixtureGit(t).RepoProject(ctx, repo, true)
	if err != nil {
		t.Fatal(err)
	}
	paths := testPaths(t)
	srv := hctest.New(t)
	srv.OnExec = hctest.GitExec
	client := hc.NewClient(srv.Path)
	coord, err := client.Run(ctx, hc.RunParams{
		Name: coordName(id), Cwd: repo, Argv: []string{"true"},
		Labels: testCoordLabels(id, "run-coord"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetStatus(coord.Address, &hc.Status{App: "claude-code", State: "working"})

	wtPath := paths.Worktree(id, "T9", 1)
	if err := os.MkdirAll(filepath.Dir(wtPath), 0o700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "worktree", "add", "-b", "task/orphan-ep1", wtPath, "origin/main")

	t0 := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	now := t0
	st := flow.State{
		Project: flow.Project{
			ID: id, Folder: repo, Goal: "held wake recurrence", Started: true, RunID: coord.Address,
			Run: &flow.RunRecord{ID: "run-coord", Address: coord.Address, Role: flow.RoleCoordinator, State: flow.PhaseWorking, CreatedMS: coord.CreatedMS},
		},
		Settings: flow.DefaultSettings(),
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	eng := newGitTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }

	// Episode 1 at t0 while coordinator is busy -> wake held with task/orphan-ep1.
	eng.passProject(ctx, id, false)
	if len(sendsTo(t, srv.Requests(), coord.Address)) != 0 {
		t.Fatalf("coordinator received wake while working")
	}

	// Recover at t0+5m while coordinator stays busy.
	gitIn(t, repo, "worktree", "remove", "--force", wtPath)
	now = t0.Add(5 * time.Minute)
	eng.passProject(ctx, id, false)

	// Recur at the same path with new branch/facts at t2 while coordinator stays busy.
	t2 := t0.Add(10 * time.Minute)
	now = t2
	gitIn(t, repo, "worktree", "add", "-b", "task/orphan-ep2", wtPath, "origin/main")
	eng.passProject(ctx, id, false)
	if len(sendsTo(t, srv.Requests(), coord.Address)) != 0 {
		t.Fatalf("coordinator received wake while still working")
	}

	// Coordinator becomes idle past idle_grace_seconds and receives held wakes.
	now = t2.Add(time.Minute)
	srv.SetStatus(coord.Address, &hc.Status{App: "claude-code", State: "idle"})
	term, err := client.Get(ctx, coord.Address)
	if err != nil {
		t.Fatal(err)
	}
	pastMS := uint64(now.Add(-time.Minute).UnixMilli())
	term.StatusMS = &pastMS
	srv.Add(term)
	for range 3 {
		eng.passProject(ctx, id, false)
	}

	sends := sendsTo(t, srv.Requests(), coord.Address)
	if len(sends) == 0 {
		t.Fatalf("idle coordinator received no wake")
	}
	joined := strings.Join(sends, "\n")
	if !strings.Contains(joined, "task/orphan-ep2") || !strings.Contains(joined, t2.Format(time.RFC3339)) {
		t.Fatalf("coordinator did not receive new episode facts (task/orphan-ep2 at %s):\n%s", t2.Format(time.RFC3339), joined)
	}
}

// Round 2, finding 6 (Codex minor 6, Carbon minor 2): persisted sweep_minutes
// of -1, 0, or 61 on a started project is clamped to 1–60 or refused at load
// per design 10.1, never running every pass or silently becoming 5 or 61.
func TestEngineSweepIntervalBoundsAtLoadOrUse(t *testing.T) {
	for _, tc := range []struct {
		name string
		val  int
	}{
		{"negative", -1},
		{"explicit zero", 0},
		{"above max", 61},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo, id, paths, srv, _, _, _ := setupSweepRepo(t)
			setSweepMinutesInState(t, paths.State(id), tc.val)

			now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
			eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
			eng.Now = func() time.Time { return now }

			_, loadErr := LoadState(paths.State(id))
			if loadErr != nil {
				// Refused at load: passProject must not sweep either.
				eng.passProject(ctx, id, false)
				now = now.Add(15 * time.Second)
				eng.passProject(ctx, id, false)
				if lines := sweepLogLines(t, paths, id); len(lines) != 0 {
					t.Fatalf("refused state still swept: %+v", lines)
				}
				return
			}

			// Clamped at load or use: initial sweep runs at t0.
			eng.passProject(ctx, id, false)

			// At +15s: must NEVER sweep on every pass.
			gitIn(t, repo, "branch", "task/at-15s")
			now = now.Add(15 * time.Second)
			eng.passProject(ctx, id, false)
			st, err := LoadState(paths.State(id))
			if err != nil {
				t.Fatal(err)
			}
			if hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/at-15s") {
				t.Fatalf("sweep_minutes=%d ran at +15s (every pass): %+v", tc.val, st.Project.Wakes)
			}

			switch tc.val {
			case 0:
				// Explicit 0 clamped to SweepMin (1m) must sweep at +1m, not wait 5m.
				gitIn(t, repo, "branch", "task/at-1m")
				now = now.Add(45 * time.Second)
				eng.passProject(ctx, id, false)
				st, _ = LoadState(paths.State(id))
				if !hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/at-1m") {
					t.Fatalf("sweep_minutes=0 did not clamp to 1m (did not run at +1m): %+v", st.Project.Wakes)
				}
			case 61:
				// 61 clamped to SweepMax (60m) must sweep at +60m, not wait 61m.
				gitIn(t, repo, "branch", "task/at-60m")
				now = now.Add(60*time.Minute - 15*time.Second)
				eng.passProject(ctx, id, false)
				st, _ = LoadState(paths.State(id))
				if !hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/at-60m") {
					t.Fatalf("sweep_minutes=61 did not clamp to 60m (did not run at +60m): %+v", st.Project.Wakes)
				}
			}
		})
	}
}

// Round 2, finding 8 (Codex minor 8): starting an already-running project with
// a new goal keeps active sweep episodes, so a still-retained worktree is not
// re-reported during the immediate sweep.
func TestEngineStartWithNewGoalKeepsActiveSweepEpisodes(t *testing.T) {
	ctx := context.Background()
	repo, id, paths, srv, _, wt2Path, _ := setupSweepRepo(t)
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	eng := newGitTestEngine(t, paths, hc.NewClient(srv.Path))
	eng.Now = func() time.Time { return now }

	// Pass 1: retained worktree wt2Path is reported.
	eng.passProject(ctx, id, false)
	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if !hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wt2Path) {
		t.Fatalf("pass 1 missing retained worktree wake: %+v", st.Project.Wakes)
	}
	clearProjectWakes(t, paths, id)
	logsBeforeStart := len(sweepLogLines(t, paths, id))

	// Owner starts the already-running project with a new goal.
	if err := eng.Start(repo, "new goal while worktree still retained", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	gitIn(t, repo, "branch", "task/canary-after-start")

	// Immediate pass runs sweep (since Start resets lastSweep) and finds the canary,
	// but must NOT re-report the still-retained wt2Path.
	now = now.Add(10 * time.Second)
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if !hasWakeMatching(st.Project.Wakes, "sweep@branch@", "task/canary-after-start") {
		t.Fatalf("immediate sweep after Start did not run (wakes=%+v)", st.Project.Wakes)
	}
	if hasWakeMatching(st.Project.Wakes, "sweep@worktree@", wt2Path) {
		t.Fatalf("Start with new goal re-reported still-retained worktree %s: %+v", wt2Path, st.Project.Wakes)
	}
	for _, l := range sweepLogLines(t, paths, id)[logsBeforeStart:] {
		if strings.Contains(l.Decision+" "+l.Why, wt2Path) {
			t.Fatalf("Start with new goal re-logged still-retained worktree %s: %+v", wt2Path, l)
		}
	}
}
