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
			{ID: "T2", Done: &flow.Merged{MergedCommit: "deadbeef"}, Attempts: []flow.Attempt{{Branch: wt2.Branch, Worktree: wt2.Path}}},
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
	for _, tc := range []struct{ name, wtRoot string }{
		{"matching worktreeRoot", filepath.Dir(wtDir)},
		{"mismatched worktreeRoot", filepath.Join(t.TempDir(), "other-data", "factory", "projects", "proj-wt1", "worktrees")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			byProbe := map[string]flow.SweepProbe{}
			for _, p := range Sweep(ctx, nil, repo, tc.wtRoot, st) {
				byProbe[p.Probe] = p
			}
			wtItems := byProbe["worktrees"].Items
			if len(wtItems) != 1 || wtItems[0].Key != wtReal || !strings.Contains(wtItems[0].Summary, "task/T99-stray") {
				t.Fatalf("worktrees = %+v, want 1 item for %s mentioning task/T99-stray", wtItems, wtReal)
			}
			if brItems := byProbe["branches"].Items; len(brItems) != 0 {
				t.Fatalf("branches = %+v, want none (branch already reported with its worktree)", brItems)
			}
		})
	}
}
