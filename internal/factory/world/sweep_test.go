package world

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmd(context.Background(), dir, args...)
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
	wt1, err := NewWorktree(ctx, repo, filepath.Join(wtRoot, "T1-1"), "T1", "active")
	if err != nil {
		t.Fatal(err)
	}
	// Done task T2 whose worktree is still present (must be flagged).
	wt2, err := NewWorktree(ctx, repo, filepath.Join(wtRoot, "T2-1"), "T2", "done")
	if err != nil {
		t.Fatal(err)
	}
	// Orphan worktree under wtRoot + orphan task/* branch (both flagged).
	wtOrphan, err := NewWorktree(ctx, repo, filepath.Join(wtRoot, "T9-1"), "T9", "orphan")
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

	probes := Sweep(ctx, client, repo, wtRoot, st)
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
	gotTerms := map[string]string{}
	for _, it := range byProbe["terminals"].Items {
		gotTerms[it.Key] = it.Summary
	}
	if !strings.Contains(gotTerms[stray.Address], "a stray agent terminal "+stray.Address+" with factory labels") || gotTerms[endedStray.Address] == "" {
		t.Fatalf("unexpected stray terminals: %+v", gotTerms)
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

	// Local base ahead: 2 commits ahead, keyed by localMainSHA.
	lb := byProbe["local_base"].Items
	if len(lb) != 1 || lb[0].Key != localMainSHA || lb[0].Summary != "the local main is 2 commits ahead of origin/main" {
		t.Fatalf("local_base = %+v, want 2 commits ahead keyed by %s", lb, localMainSHA)
	}

	// Probe errors must report Unknown, never a false negative.
	badProbes := Sweep(ctx, nil, t.TempDir(), "", st)
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
	wtReal := realPath(wtDir)

	st := flow.State{Project: flow.Project{ID: "proj-wt1", Folder: repo}}
	byProbe := map[string]flow.SweepProbe{}
	for _, p := range Sweep(ctx, nil, repo, filepath.Dir(wtDir), st) {
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
	wt1, err := NewWorktree(ctx, repo, filepath.Join(wtRoot, "T1-1"), "T1", "done-clean")
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveWorktree(ctx, repo, wt1.Path); err != nil || !removed {
		t.Fatalf("RemoveWorktree(T1) = %v, %v", removed, err)
	}
	// T2: done, NO cleanup act yet, worktree still present -> NOT reported (pre-N1 pass).
	wt2, err := NewWorktree(ctx, repo, filepath.Join(wtRoot, "T2-1"), "T2", "done-pre-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	// T3: done, cleanup act exists, worktree still present -> MUST be reported.
	wt3, err := NewWorktree(ctx, repo, filepath.Join(wtRoot, "T3-1"), "T3", "done-after-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	// T4 (dropped) & T5 (split): local branches remain -> NOT leftover branches.
	mustGit(t, repo, "branch", "task/T4-dropped")
	mustGit(t, repo, "branch", "task/T5-split")
	// T6: retry-closed attempt 1 (worktree kept on purpose by engine) + open attempt 2 -> NEITHER reported.
	wt6a, err := NewWorktree(ctx, repo, filepath.Join(wtRoot, "T6-1"), "T6-1", "retry-closed")
	if err != nil {
		t.Fatal(err)
	}
	wt6b, err := NewWorktree(ctx, repo, filepath.Join(wtRoot, "T6-2"), "T6-2", "active")
	if err != nil {
		t.Fatal(err)
	}
	// True orphans named by no attempt -> MUST be reported.
	wtOrphan, err := NewWorktree(ctx, repo, filepath.Join(wtRoot, "T9-1"), "T9", "orphan")
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
	for _, p := range Sweep(ctx, nil, repo, wtRoot, st) {
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

	if _, err := NewWorktree(ctx, repo, filepath.Join(wtRoot, "T1-1"), "T1", "active"); err != nil {
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
	for _, p := range Sweep(ctx, client, repo, wtRoot, flow.State{}) {
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
