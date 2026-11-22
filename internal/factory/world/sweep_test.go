package world

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := fixtureGit(t).gitCmd(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func TestSweepFindersAndUnknown(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := filepath.Join(t.TempDir(), "repo")
	wtRoot := filepath.Join(t.TempDir(), "worktrees")
	mustGit(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
	mustGit(t, t.TempDir(), "clone", bare, repo)
	mustGit(t, repo, "config", "user.email", "test@example.com")
	mustGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "a.txt")
	mustGit(t, repo, "commit", "-m", "init")
	mustGit(t, repo, "push", "-u", "origin", "main")
	mustGit(t, repo, "remote", "set-head", "origin", "main")

	// Open attempt T1 (owned: must NOT be flagged).
	wt1, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, "T1-1"), "T1", "active")
	if err != nil {
		t.Fatal(err)
	}
	// Done task T2 whose worktree is still present (must be flagged).
	wt2, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, "T2-1"), "T2", "done")
	if err != nil {
		t.Fatal(err)
	}
	// Orphan worktree under wtRoot + orphan task/* branch (both flagged).
	wtOrphan, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, "T9-1"), "T9", "orphan")
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "branch", "task/orphan-only")

	// Local main 2 commits ahead of origin/main.
	for _, msg := range []string{"local 1", "local 2"} {
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte(msg+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGit(t, repo, "add", "a.txt")
		mustGit(t, repo, "commit", "-m", msg)
	}
	localMainSHA := mustGit(t, repo, "rev-parse", "refs/heads/main")

	// Terminals in hctest: owned coordinator, owned worker, other project, ended-run stray, unowned stray.
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	coord, err := client.Run(ctx, hc.RunParams{Name: "coord", Cwd: repo, Argv: []string{"true"}, Labels: map[string]string{"sheepdog.project": "proj-1", "sheepdog.role": "coordinator", "sheepdog.run": "run-coord"}})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := client.Run(ctx, hc.RunParams{Name: "worker", Cwd: wt1.Path, Argv: []string{"true"}, Labels: map[string]string{"sheepdog.project": "proj-1", "sheepdog.role": "worker", "sheepdog.run": "run-w1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Run(ctx, hc.RunParams{Name: "other", Cwd: repo, Argv: []string{"true"}, Labels: map[string]string{"sheepdog.project": "proj-other", "sheepdog.role": "worker"}}); err != nil {
		t.Fatal(err)
	}
	stray, err := client.Run(ctx, hc.RunParams{Name: "stray", Cwd: repo, Argv: []string{"true"}, Labels: map[string]string{"sheepdog.project": "proj-1", "sheepdog.role": "worker"}})
	if err != nil {
		t.Fatal(err)
	}
	endedStray, err := client.Run(ctx, hc.RunParams{Name: "ended-stray", Cwd: repo, Argv: []string{"true"}, Labels: map[string]string{"sheepdog.project": "proj-1", "sheepdog.role": "reviewer", "sheepdog.run": "run-ended"}})
	if err != nil {
		t.Fatal(err)
	}

	st := flow.State{
		Project: flow.Project{ID: "proj-1", Folder: repo, RunID: coord.Address, Run: &flow.RunRecord{ID: "run-coord", Address: coord.Address, State: flow.PhaseIdle}},
		Tasks: []flow.Task{
			{ID: "T1", Attempts: []flow.Attempt{{
				Branch: wt1.Branch, Worktree: wt1.Path, RunID: worker.Address,
				Run:    &flow.RunRecord{ID: "run-w1", Address: worker.Address, State: flow.PhaseWorking},
				Review: &flow.ReviewRun{RunID: endedStray.Address, Run: &flow.RunRecord{ID: "run-ended", Address: endedStray.Address, State: flow.PhaseEnded}},
			}}},
			{ID: "T2", Done: &flow.Merged{MergedCommit: "deadbeef"}, Attempts: []flow.Attempt{{Branch: wt2.Branch, Worktree: wt2.Path, Acts: []flow.Act{{Key: flow.AskCleanup, Result: "done"}}}}},
		},
	}

	probes := fixtureGit(t).Sweep(ctx, client, repo, wtRoot, st)
	byProbe := map[string]flow.SweepProbe{}
	for _, p := range probes {
		if p.Unknown != "" {
			t.Fatalf("probe %s unexpected unknown: %s", p.Probe, p.Unknown)
		}
		byProbe[p.Probe] = p
	}

	// Terminals: stray and endedStray only.
	if len(byProbe["terminals"].Items) != 2 {
		t.Fatalf("terminals = %+v, want 2 stray terminals", byProbe["terminals"].Items)
	}
	var gotStray, gotEnded bool
	for _, it := range byProbe["terminals"].Items {
		if strings.Contains(it.Summary, "a stray agent terminal "+stray.Address+" with factory labels") {
			gotStray = true
		}
		if strings.Contains(it.Summary, endedStray.Address) {
			gotEnded = true
		}
	}
	if !gotStray || !gotEnded {
		t.Fatalf("unexpected stray terminals: %+v", byProbe["terminals"].Items)
	}

	// Worktrees: wt2 (done task T2) and wtOrphan (no open attempt), NOT wt1.
	gotWT := map[string]string{}
	for _, it := range byProbe["worktrees"].Items {
		gotWT[it.Key] = it.Summary
	}
	if len(gotWT) != 2 || gotWT[wt2.Path] != "a leftover worktree "+wt2.Path+" of done task T2 (branch "+wt2.Branch+")" || !strings.Contains(gotWT[wtOrphan.Path], wtOrphan.Branch) {
		t.Fatalf("worktrees = %+v, want wt2 + wtOrphan with branches", gotWT)
	}

	// Branches: only standalone task/orphan-only (wt2 and wtOrphan branches are reported with their worktrees; wt1 is open).
	gotBr := map[string]bool{}
	for _, it := range byProbe["branches"].Items {
		gotBr[it.Key] = true
	}
	if len(gotBr) != 1 || !gotBr["task/orphan-only"] {
		t.Fatalf("branches = %+v, want only task/orphan-only", gotBr)
	}

	// Local base ahead: 2 commits ahead, keyed by localMainSHA, naming the compared ref.
	lb := byProbe["local_base"].Items
	if len(lb) != 1 || lb[0].Key != localMainSHA || !strings.Contains(lb[0].Summary, "the local main is 2 commits ahead") || !strings.Contains(lb[0].Summary, "refs/remotes/origin/main") {
		t.Fatalf("local_base = %+v, want 2 commits ahead naming refs/remotes/origin/main keyed by %s", lb, localMainSHA)
	}

	// Probe errors must report Unknown, never a false negative.
	badProbes := fixtureGit(t).Sweep(ctx, nil, t.TempDir(), "", st)
	for _, p := range badProbes {
		if p.Unknown == "" {
			t.Errorf("probe %s reported empty Unknown on failure (items=%+v)", p.Probe, p.Items)
		}
	}
	closedHC := hc.NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	if p := SweepTerminals(ctx, closedHC, st); p.Unknown == "" {
		t.Errorf("closed hc socket reported empty Unknown: %+v", p)
	}
}

func TestSweepLiveCoordinatorReportsNoStray(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	coord, err := client.Run(ctx, hc.RunParams{
		Name:   "coord-proj-live",
		Cwd:    t.TempDir(),
		Argv:   []string{"true"},
		Labels: map[string]string{"sheepdog.project": "proj-live", "sheepdog.role": "coordinator", "sheepdog.run": "run-coord-live"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		st   flow.State
	}{
		{"live run record", flow.State{Project: flow.Project{ID: "proj-live", RunID: coord.Address, Run: &flow.RunRecord{ID: "run-coord-live", Address: coord.Address, State: flow.PhaseIdle}}}},
		{"owned by terminal name", flow.State{Project: flow.Project{ID: "proj-live", RunID: coord.Name}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := SweepTerminals(ctx, client, tc.st)
			if p.Unknown != "" || len(p.Items) != 0 {
				t.Fatalf("SweepTerminals(%s) = %+v, want no stray terminals", tc.name, p)
			}
		})
	}
}

func TestSweepLeftoverWorktreeInProjectDirReportedOnceWithBranch(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := filepath.Join(t.TempDir(), "repo")
	mustGit(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
	mustGit(t, t.TempDir(), "clone", bare, repo)
	mustGit(t, repo, "config", "user.email", "test@example.com")
	mustGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "a.txt")
	mustGit(t, repo, "commit", "-m", "init")
	mustGit(t, repo, "push", "-u", "origin", "main")
	mustGit(t, repo, "remote", "set-head", "origin", "main")

	wtDir := filepath.Join(t.TempDir(), "data", "factory", "projects", "proj-wt1", "worktrees", "T99-1")
	if err := os.MkdirAll(filepath.Dir(wtDir), 0o700); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "worktree", "add", "-b", "task/T99-stray", wtDir, "origin/main")
	wtReal, err := filepath.EvalSymlinks(wtDir)
	if err != nil {
		t.Fatal(err)
	}

	st := flow.State{Project: flow.Project{ID: "proj-wt1", Folder: repo}}
	byProbe := map[string]flow.SweepProbe{}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	wtRoot, err := filepath.Rel(cwd, filepath.Dir(wtDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range fixtureGit(t).Sweep(ctx, nil, repo, wtRoot, st) {
		byProbe[p.Probe] = p
	}
	wtItems := byProbe["worktrees"].Items
	if len(wtItems) != 1 || wtItems[0].Key != wtReal || !strings.Contains(wtItems[0].Summary, "task/T99-stray") {
		t.Fatalf("worktrees = %+v, want 1 item for %s mentioning task/T99-stray", wtItems, wtReal)
	}
	if brItems := byProbe["branches"].Items; len(brItems) != 0 {
		t.Fatalf("branches = %+v, want none (branch already reported with its worktree)", brItems)
	}
}

func TestSweepOwnershipAnyAttemptAndDoneCleanupGate(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := filepath.Join(t.TempDir(), "repo")
	wtRoot := filepath.Join(t.TempDir(), "worktrees")
	mustGit(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
	mustGit(t, t.TempDir(), "clone", bare, repo)
	mustGit(t, repo, "config", "user.email", "test@example.com")
	mustGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "a.txt")
	mustGit(t, repo, "commit", "-m", "init")
	mustGit(t, repo, "push", "-u", "origin", "main")
	mustGit(t, repo, "remote", "set-head", "origin", "main")

	// T1: done, worktree cleanly removed, local branch still exists -> NOT a leftover branch.
	wt1, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, "T1-1"), "T1", "done-clean")
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := fixtureGit(t).RemoveWorktree(ctx, repo, wt1.Path); err != nil || !removed {
		t.Fatalf("RemoveWorktree(T1) = %v, %v", removed, err)
	}
	// T2: done, NO cleanup act yet, worktree still present -> NOT reported (pre-N1 pass).
	wt2, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, "T2-1"), "T2", "done-pre-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	// T3: done, cleanup act exists, worktree still present -> MUST be reported.
	wt3, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, "T3-1"), "T3", "done-after-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	// T4 (dropped) & T5 (split): local branches remain -> NOT leftover branches.
	mustGit(t, repo, "branch", "task/T4-dropped")
	mustGit(t, repo, "branch", "task/T5-split")
	// T6: retry-closed attempt 1 (worktree kept on purpose by engine) + open attempt 2 -> NEITHER reported.
	wt6a, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, "T6-1"), "T6-1", "retry-closed")
	if err != nil {
		t.Fatal(err)
	}
	wt6b, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, "T6-2"), "T6-2", "active")
	if err != nil {
		t.Fatal(err)
	}
	// True orphans named by no attempt -> MUST be reported.
	wtOrphan, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, "T9-1"), "T9", "orphan")
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "branch", "task/truly-orphan")

	st := flow.State{
		Project: flow.Project{ID: "proj-own", Folder: repo},
		Tasks: []flow.Task{
			{ID: "T1", Done: &flow.Merged{MergedCommit: "aaa"}, Attempts: []flow.Attempt{{Branch: wt1.Branch, Worktree: wt1.Path, Acts: []flow.Act{{Key: flow.AskCleanup, Result: "done"}}}}},
			{ID: "T2", Done: &flow.Merged{MergedCommit: "bbb"}, Attempts: []flow.Attempt{{Branch: wt2.Branch, Worktree: wt2.Path}}},
			{ID: "T3", Done: &flow.Merged{MergedCommit: "ccc"}, Attempts: []flow.Attempt{{Branch: wt3.Branch, Worktree: wt3.Path, Acts: []flow.Act{{Key: flow.AskCleanup, Result: "done"}}}}},
			{ID: "T4", Dropped: &flow.Dropped{Why: "no longer needed"}, Attempts: []flow.Attempt{{Branch: "task/T4-dropped", Worktree: filepath.Join(wtRoot, "T4-1")}}},
			{ID: "T5", Split: &flow.Split{Children: []string{"T5a"}}, Attempts: []flow.Attempt{{Branch: "task/T5-split", Worktree: filepath.Join(wtRoot, "T5-1")}}},
			{ID: "T6", Attempts: []flow.Attempt{
				{Branch: wt6a.Branch, Worktree: wt6a.Path, Closed: "recap new"},
				{Branch: wt6b.Branch, Worktree: wt6b.Path},
			}},
		},
	}

	byProbe := map[string]flow.SweepProbe{}
	for _, p := range fixtureGit(t).Sweep(ctx, nil, repo, wtRoot, st) {
		byProbe[p.Probe] = p
	}
	gotWT := map[string]string{}
	for _, it := range byProbe["worktrees"].Items {
		gotWT[it.Key] = it.Summary
	}
	if len(gotWT) != 2 || !strings.Contains(gotWT[wt3.Path], "of done task T3") || !strings.Contains(gotWT[wtOrphan.Path], "owned by no attempt") {
		t.Fatalf("worktrees = %+v, want only T3 (post-cleanup) and wtOrphan", gotWT)
	}
	gotBr := map[string]string{}
	for _, it := range byProbe["branches"].Items {
		gotBr[it.Key] = it.Summary
	}
	if len(gotBr) != 1 || !strings.Contains(gotBr["task/truly-orphan"], "owned by no attempt") {
		t.Fatalf("branches = %+v, want only task/truly-orphan", gotBr)
	}
}

func TestSweepWithoutStateReportsUnknownAndRunsLocalBase(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := filepath.Join(t.TempDir(), "repo")
	wtRoot := filepath.Join(t.TempDir(), "worktrees")
	mustGit(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
	mustGit(t, t.TempDir(), "clone", bare, repo)
	mustGit(t, repo, "config", "user.email", "test@example.com")
	mustGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "a.txt")
	mustGit(t, repo, "commit", "-m", "init")
	mustGit(t, repo, "push", "-u", "origin", "main")
	mustGit(t, repo, "remote", "set-head", "origin", "main")

	if _, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, "T1-1"), "T1", "active"); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "branch", "task/user-branch")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("ahead\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "a.txt")
	mustGit(t, repo, "commit", "-m", "ahead 1")

	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	if _, err := client.Run(ctx, hc.RunParams{Name: "worker", Cwd: repo, Argv: []string{"true"}, Labels: map[string]string{"sheepdog.project": "proj-x", "sheepdog.role": "worker"}}); err != nil {
		t.Fatal(err)
	}

	byProbe := map[string]flow.SweepProbe{}
	for _, p := range fixtureGit(t).Sweep(ctx, client, repo, wtRoot, flow.State{}) {
		byProbe[p.Probe] = p
	}
	for _, name := range []string{"terminals", "worktrees", "branches"} {
		p := byProbe[name]
		if p.Unknown == "" || len(p.Items) != 0 {
			t.Errorf("probe %s without state = %+v, want Unknown != \"\" and 0 items", name, p)
		}
	}
	lb := byProbe["local_base"]
	if lb.Unknown != "" || len(lb.Items) != 1 || !strings.Contains(lb.Items[0].Summary, "1 commit ahead") {
		t.Errorf("local_base without state = %+v, want 1 commit ahead", lb)
	}
}

// Slice 5b row 5b.10 (design.md 5, 11.3): terminal ownership matches run label
// plus creation time (with a legacy fallback when older state lacks them), and
// stray terminals at a reused address with a new creation time get distinct keys.
func TestSweepTerminalsIdentityAndReusedAddress(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	cwd := t.TempDir()

	// Active worker run with modern identity: sheepdog.run + CreatedMS.
	stModern := flow.State{
		Project: flow.Project{ID: "proj-id", Folder: cwd},
		Tasks: []flow.Task{{
			ID: "T1",
			Attempts: []flow.Attempt{{
				RunID: "t1",
				Run: &flow.RunRecord{
					ID:        "run-w1",
					Address:   "t1",
					Role:      flow.RoleWorker,
					State:     flow.PhaseWorking,
					CreatedMS: 1000,
				},
			}},
		}},
	}

	t.Run("exact run label and creation time is owned", func(t *testing.T) {
		srv.Add(hc.Terminal{
			Address: "t1", Name: "worker-1", State: "live", Cwd: cwd, CreatedMS: 1000,
			Labels: map[string]string{"sheepdog.project": "proj-id", "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-w1"},
		})
		defer srv.Remove("t1")
		p := SweepTerminals(ctx, client, stModern)
		if p.Unknown != "" || len(p.Items) != 0 {
			t.Fatalf("matching run label + CreatedMS = %+v, want 0 strays", p)
		}
	})

	t.Run("reused address with different creation time and label is stray", func(t *testing.T) {
		srv.Add(hc.Terminal{
			Address: "t1", Name: "worker-reused", State: "live", Cwd: cwd, CreatedMS: 2000,
			Labels: map[string]string{"sheepdog.project": "proj-id", "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-other"},
		})
		defer srv.Remove("t1")
		p := SweepTerminals(ctx, client, stModern)
		if p.Unknown != "" || len(p.Items) != 1 {
			t.Fatalf("reused address t1 with different run+CreatedMS = %+v, want 1 stray", p)
		}
	})

	t.Run("same run label with different creation time is stray", func(t *testing.T) {
		srv.Add(hc.Terminal{
			Address: "t2", Name: "worker-recycled", State: "live", Cwd: cwd, CreatedMS: 2000,
			Labels: map[string]string{"sheepdog.project": "proj-id", "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-w1"},
		})
		defer srv.Remove("t2")
		p := SweepTerminals(ctx, client, stModern)
		if p.Unknown != "" || len(p.Items) != 1 {
			t.Fatalf("same run label with mismatched CreatedMS = %+v, want 1 stray", p)
		}
	})

	t.Run("matching run label and creation time at moved address is owned", func(t *testing.T) {
		srv.Add(hc.Terminal{
			Address: "t3", Name: "worker-1", State: "live", Cwd: cwd, CreatedMS: 1000,
			Labels: map[string]string{"sheepdog.project": "proj-id", "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-w1"},
		})
		defer srv.Remove("t3")
		p := SweepTerminals(ctx, client, stModern)
		if p.Unknown != "" || len(p.Items) != 0 {
			t.Fatalf("matching run label + CreatedMS at different address = %+v, want 0 strays", p)
		}
	})

	t.Run("legacy fallback without RunRecord or CreatedMS", func(t *testing.T) {
		srv.Add(hc.Terminal{
			Address: "t-leg1", Name: "coord-leg", State: "live", Cwd: cwd, CreatedMS: 5000,
			Labels: map[string]string{"sheepdog.project": "proj-leg", "sheepdog.role": flow.RoleCoordinator},
		})
		srv.Add(hc.Terminal{
			Address: "t-leg2", Name: "worker-leg", State: "live", Cwd: cwd, CreatedMS: 6000,
			Labels: map[string]string{"sheepdog.project": "proj-leg", "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-leg"},
		})
		srv.Add(hc.Terminal{
			Address: "t-leg3", Name: "rev-leg", State: "live", Cwd: cwd, CreatedMS: 7000,
			Labels: map[string]string{"sheepdog.project": "proj-leg", "sheepdog.role": flow.RoleReviewer},
		})
		defer func() {
			srv.Remove("t-leg1")
			srv.Remove("t-leg2")
			srv.Remove("t-leg3")
		}()
		stLegacy := flow.State{
			Project: flow.Project{ID: "proj-leg", Folder: cwd, RunID: "coord-leg"}, // nil RunRecord: legacy name match
			Tasks: []flow.Task{{
				ID: "T1",
				Attempts: []flow.Attempt{{
					RunID: "t-leg2",
					Run:   &flow.RunRecord{ID: "run-leg", Address: "t-leg2", State: flow.PhaseWorking}, // CreatedMS==0: legacy run-label match
					Review: &flow.ReviewRun{
						RunID: "t-leg3",
						Run:   &flow.RunRecord{Address: "t-leg3", State: flow.PhaseWorking}, // ID=="" && CreatedMS==0: legacy address match
					},
				}},
			}},
		}
		p := SweepTerminals(ctx, client, stLegacy)
		if p.Unknown != "" || len(p.Items) != 0 {
			t.Fatalf("legacy fallback = %+v, want 0 strays", p)
		}
	})

	t.Run("reused stray address with new creation time gets distinct key", func(t *testing.T) {
		stEmpty := flow.State{Project: flow.Project{ID: "proj-stray", Folder: cwd}}
		srv.Add(hc.Terminal{
			Address: "t9", Name: "stray-1", State: "live", Cwd: cwd, CreatedMS: 1000,
			Labels: map[string]string{"sheepdog.project": "proj-stray", "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-s1"},
		})
		p1 := SweepTerminals(ctx, client, stEmpty)
		if p1.Unknown != "" || len(p1.Items) != 1 {
			t.Fatalf("first stray = %+v, want 1 item", p1)
		}
		srv.Add(hc.Terminal{
			Address: "t9", Name: "stray-2", State: "live", Cwd: cwd, CreatedMS: 2000,
			Labels: map[string]string{"sheepdog.project": "proj-stray", "sheepdog.role": flow.RoleWorker, "sheepdog.run": "run-s2"},
		})
		defer srv.Remove("t9")
		p2 := SweepTerminals(ctx, client, stEmpty)
		if p2.Unknown != "" || len(p2.Items) != 1 {
			t.Fatalf("second stray = %+v, want 1 item", p2)
		}
		if p1.Items[0].Key == p2.Items[0].Key {
			t.Fatalf("reused address t9 with CreatedMS 1000 vs 2000 produced identical Key %q, want distinct episode keys", p1.Items[0].Key)
		}
	})
}

// Slice 5b row 5b.10 (design.md 11.3; KB-WKT-5): a done task whose cleanup is
// still in progress (not yet started, asked, transiently failing under retry
// budget, or retrying after retry) is NOT reported as retained; only a
// completed cleanup that retained the worktree or an exhausted/failed cleanup
// is reported, and the worktrees stay in place.
func TestSweepDoneWorktreeCleanupInProgressVsRetainedOrFailed(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := filepath.Join(t.TempDir(), "repo")
	wtRoot := filepath.Join(t.TempDir(), "worktrees")
	mustGit(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
	mustGit(t, t.TempDir(), "clone", bare, repo)
	mustGit(t, repo, "config", "user.email", "test@example.com")
	mustGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "a.txt")
	mustGit(t, repo, "commit", "-m", "init")
	mustGit(t, repo, "push", "-u", "origin", "main")
	mustGit(t, repo, "remote", "set-head", "origin", "main")

	mkWT := func(id string) Worktree {
		wt, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(wtRoot, id+"-1"), id, "case")
		if err != nil {
			t.Fatal(err)
		}
		return wt
	}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	wtPending := mkWT("T1")
	wtAsked := mkWT("T2")
	wtRetrying := mkWT("T3")
	wtAfterRetry := mkWT("T4")
	wtRetained := mkWT("T5")
	wtExhausted := mkWT("T6")
	wtSettled := mkWT("T7")

	st := flow.State{
		Project:  flow.Project{ID: "proj-clean", Folder: repo},
		Settings: flow.DefaultSettings(), // EnsureFailures == 3
		Tasks: []flow.Task{
			{ID: "T1", Done: &flow.Merged{MergedCommit: "c1"}, Attempts: []flow.Attempt{{Branch: wtPending.Branch, Worktree: wtPending.Path}}},
			{ID: "T2", Done: &flow.Merged{MergedCommit: "c2"}, Attempts: []flow.Attempt{{Branch: wtAsked.Branch, Worktree: wtAsked.Path, Acts: []flow.Act{{Key: flow.AskCleanup, Result: "asked", At: now}}}}},
			{ID: "T3", Done: &flow.Merged{MergedCommit: "c3"}, Attempts: []flow.Attempt{{Branch: wtRetrying.Branch, Worktree: wtRetrying.Path, Acts: []flow.Act{{Key: flow.AskCleanup, Result: "failed", Class: flow.ClassTransient, Err: "temporary error", At: now}}}}},
			{ID: "T4", Done: &flow.Merged{MergedCommit: "c4"}, Attempts: []flow.Attempt{{Branch: wtAfterRetry.Branch, Worktree: wtAfterRetry.Path, Acts: []flow.Act{
				{Key: flow.AskCleanup, Result: "failed", Class: flow.ClassTransient, At: now.Add(-3 * time.Minute)},
				{Key: flow.AskCleanup, Result: "failed", Class: flow.ClassTransient, At: now.Add(-2 * time.Minute)},
				{Key: flow.AskCleanup, Result: "failed", Class: flow.ClassTransient, At: now.Add(-1 * time.Minute)},
				{Key: flow.AskRetry, Result: "done", At: now},
			}}}},
			{ID: "T5", Done: &flow.Merged{MergedCommit: "c5"}, Attempts: []flow.Attempt{{Branch: wtRetained.Branch, Worktree: wtRetained.Path, Acts: []flow.Act{{Key: flow.AskCleanup, Result: "done", At: now}}}}},
			{ID: "T6", Done: &flow.Merged{MergedCommit: "c6"}, Attempts: []flow.Attempt{{Branch: wtExhausted.Branch, Worktree: wtExhausted.Path, Acts: []flow.Act{
				{Key: flow.AskCleanup, Result: "failed", Class: flow.ClassTransient, At: now.Add(-3 * time.Minute)},
				{Key: flow.AskCleanup, Result: "failed", Class: flow.ClassTransient, At: now.Add(-2 * time.Minute)},
				{Key: flow.AskCleanup, Result: "failed", Class: flow.ClassTransient, At: now.Add(-1 * time.Minute)},
			}}}},
			{ID: "T7", Done: &flow.Merged{MergedCommit: "c7"}, Attempts: []flow.Attempt{{Branch: wtSettled.Branch, Worktree: wtSettled.Path, Acts: []flow.Act{{Key: flow.AskCleanup, Result: "failed", Class: flow.ClassSettled, Err: "permission denied", At: now}}}}},
		},
	}

	byProbe := map[string]flow.SweepProbe{}
	for _, p := range fixtureGit(t).Sweep(ctx, nil, repo, wtRoot, st) {
		byProbe[p.Probe] = p
	}
	if byProbe["worktrees"].Unknown != "" {
		t.Fatalf("worktrees unexpected unknown: %s", byProbe["worktrees"].Unknown)
	}
	got := map[string]string{}
	for _, it := range byProbe["worktrees"].Items {
		got[it.Key] = it.Summary
	}
	for _, tc := range []struct {
		id, path string
		want     bool
	}{
		{"T1 (pending cleanup)", wtPending.Path, false},
		{"T2 (asked in flight)", wtAsked.Path, false},
		{"T3 (1 transient failure, retrying)", wtRetrying.Path, false},
		{"T4 (retrying after AskRetry)", wtAfterRetry.Path, false},
		{"T5 (cleanup done, retained)", wtRetained.Path, true},
		{"T6 (cleanup exhausted)", wtExhausted.Path, true},
		{"T7 (cleanup settled failure)", wtSettled.Path, true},
	} {
		_, present := got[tc.path]
		if present != tc.want {
			t.Errorf("%s: reported=%v, want %v (summary=%q)", tc.id, present, tc.want, got[tc.path])
		}
		if _, err := os.Stat(tc.path); err != nil {
			t.Errorf("%s: sweep modified or removed worktree %s: %v", tc.id, tc.path, err)
		}
	}
}

// Slice 5b row 5b.10 (design.md 11.3; KB-ENV-9): the base-ahead line names the
// compared tracking ref and explains its local/fetched basis without fetching,
// and an unreadable local_base probe names its reason.
func TestSweepLocalBaseNamesComparedRefAndUnknownReason(t *testing.T) {
	ctx := context.Background()
	bare := filepath.Join(t.TempDir(), "remote.git")
	repo := filepath.Join(t.TempDir(), "repo")
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
	mustGit(t, t.TempDir(), "clone", bare, repo)
	mustGit(t, repo, "config", "user.email", "test@example.com")
	mustGit(t, repo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "a.txt")
	mustGit(t, repo, "commit", "-m", "init")
	mustGit(t, repo, "push", "-u", "origin", "main")
	mustGit(t, repo, "remote", "set-head", "origin", "main")
	remoteSHA := mustGit(t, repo, "rev-parse", "refs/remotes/origin/main")

	// Advance remote bare repo from a second clone without fetching in repo.
	mustGit(t, t.TempDir(), "clone", bare, other)
	mustGit(t, other, "config", "user.email", "test@example.com")
	mustGit(t, other, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(other, "remote.txt"), []byte("upstream\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, other, "add", "remote.txt")
	mustGit(t, other, "commit", "-m", "remote move")
	mustGit(t, other, "push", "origin", "main")

	// Local main commits ahead of the last-fetched tracking ref.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("ahead\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "a.txt")
	mustGit(t, repo, "commit", "-m", "local ahead")

	var lb flow.SweepProbe
	for _, p := range fixtureGit(t).Sweep(ctx, nil, repo, "", flow.State{Project: flow.Project{ID: "proj-lb", Folder: repo}}) {
		if p.Probe == "local_base" {
			lb = p
		}
	}
	if lb.Unknown != "" || len(lb.Items) != 1 {
		t.Fatalf("local_base = %+v, want 1 item", lb)
	}
	summary := lb.Items[0].Summary
	lower := strings.ToLower(summary)
	if !strings.Contains(summary, "refs/remotes/origin/main") {
		t.Errorf("local_base summary = %q, want compared ref refs/remotes/origin/main", summary)
	}
	if !strings.Contains(lower, "fetched") && !strings.Contains(lower, "tracking") && !strings.Contains(summary, remoteSHA[:7]) {
		t.Errorf("local_base summary = %q, want comparison basis (fetched/tracking ref or %s)", summary, remoteSHA[:7])
	}
	// Sweep must never mutate or fetch the local tracking ref.
	if gotSHA := mustGit(t, repo, "rev-parse", "refs/remotes/origin/main"); gotSHA != remoteSHA {
		t.Fatalf("sweep mutated refs/remotes/origin/main from %s to %s", remoteSHA, gotSHA)
	}

	// Missing local base branch (detached HEAD, refs/heads/main deleted) must report
	// unknown naming the missing local branch/ref rather than a bare exit status.
	mustGit(t, repo, "checkout", "--detach", "HEAD")
	mustGit(t, repo, "branch", "-D", "main")
	lb = flow.SweepProbe{}
	for _, p := range fixtureGit(t).Sweep(ctx, nil, repo, "", flow.State{Project: flow.Project{ID: "proj-lb", Folder: repo}}) {
		if p.Probe == "local_base" {
			lb = p
		}
	}
	if lb.Unknown == "" || len(lb.Items) != 0 {
		t.Fatalf("local_base without local main = %+v, want Unknown with 0 items", lb)
	}
	if !strings.Contains(lb.Unknown, "refs/heads/main") && !strings.Contains(strings.ToLower(lb.Unknown), "local") && !strings.Contains(lb.Unknown, "main") {
		t.Errorf("local_base Unknown = %q, want plain reason naming missing local main branch", lb.Unknown)
	}
}

// Round 2, finding 7 (Codex minor 7): the base-ahead count is computed from
// the two resolved commits so a ref moving between lookup and rev-list cannot
// make the count disagree with the named SHAs.
func TestSweepLocalBaseCountsResolvedCommits(t *testing.T) {
	setup := func(t *testing.T) (repo, c0, c1, c2 string) {
		t.Helper()
		bare := filepath.Join(t.TempDir(), "remote.git")
		repo = filepath.Join(t.TempDir(), "repo")
		mustGit(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
		mustGit(t, t.TempDir(), "clone", bare, repo)
		mustGit(t, repo, "config", "user.email", "test@example.com")
		mustGit(t, repo, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGit(t, repo, "add", "a.txt")
		mustGit(t, repo, "commit", "-m", "init")
		mustGit(t, repo, "push", "-u", "origin", "main")
		mustGit(t, repo, "remote", "set-head", "origin", "main")
		c0 = mustGit(t, repo, "rev-parse", "refs/remotes/origin/main")

		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("ahead 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGit(t, repo, "add", "a.txt")
		mustGit(t, repo, "commit", "-m", "ahead 1")
		c1 = mustGit(t, repo, "rev-parse", "refs/heads/main")

		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("ahead 2\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustGit(t, repo, "add", "a.txt")
		mustGit(t, repo, "commit", "-m", "ahead 2")
		c2 = mustGit(t, repo, "rev-parse", "refs/heads/main")

		mustGit(t, repo, "reset", "--hard", c1)
		return repo, c0, c1, c2
	}

	for _, tc := range []struct {
		name      string
		targetRef string
		newSHA    func(c1, c2 string) string
	}{
		{"local ref advances before rev-list", "refs/heads/main", func(_, c2 string) string { return c2 }},
		{"tracking ref advances before rev-list", "refs/remotes/origin/main", func(c1, _ string) string { return c1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo, c0, c1, c2 := setup(t)
			target := tc.newSHA(c1, c2)

			srv := hctest.New(t)
			var once sync.Once
			srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
				if len(p.Argv) >= 2 && p.Argv[0] == "git" && p.Argv[1] == "rev-list" {
					once.Do(func() {
						if out, err := exec.Command("git", "-C", repo, "update-ref", tc.targetRef, target).CombinedOutput(); err != nil {
							t.Errorf("update-ref %s: %v: %s", tc.targetRef, err, out)
						}
					})
				}
				return hctest.GitExec(p)
			}
			g := NewGit(hc.NewClient(srv.Path), nil, nil)

			var lb flow.SweepProbe
			for _, p := range g.Sweep(ctx, nil, repo, "", flow.State{Project: flow.Project{ID: "proj-lb-sha", Folder: repo}}) {
				if p.Probe == "local_base" {
					lb = p
				}
			}
			if lb.Unknown != "" || len(lb.Items) != 1 {
				t.Fatalf("local_base = %+v, want 1 item matching resolved SHAs %s..%s", lb, c0[:7], c1[:7])
			}
			it := lb.Items[0]
			if it.Key != c1 || !strings.Contains(it.Summary, "1 commit ahead") || !strings.Contains(it.Summary, "refs/remotes/origin/main") || !strings.Contains(it.Summary, c0[:7]) {
				t.Fatalf("local_base item = %+v, want Key=%s and '1 commit ahead' of refs/remotes/origin/main (%s)", it, c1, c0[:7])
			}
		})
	}
}

// Round 2, finding 9 (Carbon major 2): removing remote.origin from git config
// while leaving refs/remotes/origin/* intact must report unknown (ErrNoOrigin),
// matching remoteBaseCommit instead of reporting clean or stale ahead counts.
func TestSweepLocalBaseRemovedOriginWithLeftoverRefs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ahead bool
	}{
		{"local main matches leftover remote ref", false},
		{"local main ahead of leftover remote ref", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			bare := filepath.Join(t.TempDir(), "remote.git")
			repo := filepath.Join(t.TempDir(), "repo")
			mustGit(t, t.TempDir(), "init", "--bare", "--initial-branch=main", bare)
			mustGit(t, t.TempDir(), "clone", bare, repo)
			mustGit(t, repo, "config", "user.email", "test@example.com")
			mustGit(t, repo, "config", "user.name", "Test")
			if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("init\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mustGit(t, repo, "add", "a.txt")
			mustGit(t, repo, "commit", "-m", "init")
			mustGit(t, repo, "push", "-u", "origin", "main")
			mustGit(t, repo, "remote", "set-head", "origin", "main")
			if tc.ahead {
				if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("local\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				mustGit(t, repo, "add", "a.txt")
				mustGit(t, repo, "commit", "-m", "local ahead")
			}

			// Remove remote.origin config section without deleting refs/remotes/origin/*.
			mustGit(t, repo, "config", "--remove-section", "remote.origin")
			if ref := mustGit(t, repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); ref != "refs/remotes/origin/main" {
				t.Fatalf("expected leftover refs/remotes/origin/HEAD, got %q", ref)
			}

			g := fixtureGit(t)
			if _, _, err := g.remoteBaseCommit(ctx, repo, false); !errors.Is(err, ErrNoOrigin) {
				t.Fatalf("remoteBaseCommit err = %v, want ErrNoOrigin", err)
			}

			var lb flow.SweepProbe
			for _, p := range g.Sweep(ctx, nil, repo, "", flow.State{Project: flow.Project{ID: "proj-removed-origin", Folder: repo}}) {
				if p.Probe == "local_base" {
					lb = p
				}
			}
			if lb.Unknown == "" || !strings.Contains(lb.Unknown, ErrNoOrigin.Error()) || len(lb.Items) != 0 {
				t.Fatalf("local_base = %+v, want Unknown containing %q and 0 items", lb, ErrNoOrigin.Error())
			}
		})
	}
}
