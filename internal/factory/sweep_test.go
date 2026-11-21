package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
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

func TestEngineSweepWakesOnceAndThrottled10Minutes(t *testing.T) {
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
	gotKeys := map[string]string{}
	for _, w := range st.Project.Wakes {
		gotKeys[w.Key] = w.Why
	}
	for _, want := range []struct{ key, substr string }{
		{"sweep@stray_terminal@" + strayAddr, "a stray agent terminal " + strayAddr + " with factory labels"},
		{"sweep@worktree@" + wt2Path, "a leftover worktree " + wt2Path + " of done task T2"},
		{"sweep@branch@task/orphan-branch", "a leftover branch task/orphan-branch"},
		{"sweep@local_base@" + localSHA, "the local main is 2 commits ahead of origin/main"},
	} {
		if !strings.Contains(gotKeys[want.key], want.substr) {
			t.Errorf("wake[%s] = %q, want %q (all=%+v)", want.key, gotKeys[want.key], want.substr, gotKeys)
		}
	}
	// Nothing was deleted or killed.
	if _, err := hc.NewClient(srv.Path).Get(context.Background(), strayAddr); err != nil {
		t.Fatalf("stray terminal was killed: %v", err)
	}
	if _, err := os.Stat(wt2Path); err != nil {
		t.Fatalf("leftover worktree was removed: %v", err)
	}

	// Add a new orphan branch at +5 minutes: sweep must NOT run yet (< 10m).
	now = now.Add(5 * time.Minute)
	gitIn(t, repo, "branch", "task/later-orphan")
	eng.passProject(context.Background(), id, false)
	st, _ = LoadState(paths.State(id))
	for _, w := range st.Project.Wakes {
		if w.Key == "sweep@branch@task/later-orphan" {
			t.Fatalf("sweep ran before 10 minutes elapsed: %+v", st.Project.Wakes)
		}
	}

	// At +10 minutes: sweep runs again; old items do NOT wake again, only the new branch wakes.
	prevCount := len(st.Project.Wakes)
	now = now.Add(5 * time.Minute)
	eng.passProject(context.Background(), id, false)
	st, _ = LoadState(paths.State(id))
	if len(st.Project.Wakes) != prevCount+1 {
		t.Fatalf("wakes after 10m = %d (%+v), want %d (only 1 new wake)", len(st.Project.Wakes), st.Project.Wakes, prevCount+1)
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
		"the local main is 2 commits ahead of origin/main",
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
		"the local main is 2 commits ahead of origin/main",
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
