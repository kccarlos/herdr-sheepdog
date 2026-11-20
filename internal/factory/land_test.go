package factory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

const greenRollup = `[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"SUCCESS"}]`

// landing is a task whose round 1 passed at c1, with PR #7 open at c1,
// checks green, mergeable, and its worker idle: N42's state.
type landing struct {
	t                                    *testing.T
	eng                                  *Engine
	srv                                  *hctest.Server
	client                               *hc.Client
	paths                                Paths
	log                                  *bytes.Buffer
	gh, id, repo, wt, branch, c1, worker string
}

func newLanding(t *testing.T) *landing {
	t.Helper()
	ctx := context.Background()
	srv := hctest.New(t)
	l := &landing{t: t, srv: srv, client: hc.NewClient(srv.Path), paths: testPaths(t), log: &bytes.Buffer{}}
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	l.repo, l.gh = repo, installFakeGH(t)
	l.eng = New(l.paths, l.client)
	l.eng.Log = logging.NewJSONLLogger(l.log)
	if err := l.eng.Start(repo, "Goal"); err != nil {
		t.Fatal(err)
	}
	l.id = l.eng.projectDirs()[0].Name()
	wt, err := world.NewWorktree(ctx, repo, l.paths.Worktree(l.id, "T1", 1), "T1-1", "feat: x")
	if err != nil {
		t.Fatal(err)
	}
	l.wt, l.branch = wt.Path, wt.Branch
	if err := os.WriteFile(filepath.Join(l.wt, "x.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.wt, "add", "x.go")
	gitIn(t, l.wt, "commit", "-m", "feat: x")
	l.c1 = gitIn(t, l.wt, "rev-parse", "HEAD")
	gitIn(t, l.wt, "push", "origin", l.c1+":refs/heads/"+l.branch)
	if l.worker, err = world.StartRun(ctx, l.client, world.RunSpec{Name: "worker-t1", Cwd: l.wt, Argv: []string{"sh"}}); err != nil {
		t.Fatal(err)
	}
	srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "idle"})
	l.edit(func(s *flow.State) {
		s.Project.RunID = "coord-elsewhere" // the coordinator is not this test's business
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: x", EstimateLines: 50, Attempts: []flow.Attempt{{SizeCap: 100,
			BaseCommit: base, Branch: l.branch, Worktree: l.wt, PR: 7, RunID: l.worker,
			Ready: []flow.Ready{{Head: l.c1}},
			Rounds: []flow.Round{
				{N: 1, Kind: "full", Head: l.c1, Verdict: flow.VerdictPass},
				{N: 2, Kind: "full", Head: l.c1, Verdict: flow.VerdictPass},
			},
			Accepts: []flow.Accept{}, Acts: []flow.Act{},
		}}}}
	})
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	return l
}

func (l *landing) pass() { l.eng.passProject(context.Background(), l.id, false) }

func (l *landing) state() flow.State {
	s, err := LoadState(l.paths.State(l.id))
	if err != nil {
		l.t.Fatal(err)
	}
	return s
}

func (l *landing) edit(fn func(*flow.State)) {
	s := l.state()
	fn(&s)
	if err := SaveState(l.paths.State(l.id), s); err != nil {
		l.t.Fatal(err)
	}
}

func (l *landing) logLines() []LogLine {
	raw, _ := os.ReadFile(l.paths.Log(l.id))
	var out []LogLine
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var ln LogLine
		if err := json.Unmarshal(sc.Bytes(), &ln); err != nil {
			l.t.Fatal(err)
		}
		out = append(out, ln)
	}
	return out
}

func (l *landing) writeMerged(commit string) {
	body := `{"number":7,"state":"MERGED","headRefOid":"` + l.c1 + `","mergeable":"UNKNOWN","statusCheckRollup":` + greenRollup + `,"baseRefName":"main","mergeCommit":{"oid":"` + commit + `"}}`
	if err := os.WriteFile(filepath.Join(l.gh, "view.json"), []byte(body), 0o600); err != nil {
		l.t.Fatal(err)
	}
}

func (l *landing) remoteBranch() string {
	return gitIn(l.t, l.repo, "ls-remote", "--heads", "origin", l.branch)
}

const mergedSHA = "abababababababababababababababababababab"

// TestLandMergesOncePinnedDoneOnlyOnMergedSnapshot: N42 writes the accept
// in the same log line and state write as the merge ask; the merge is
// pinned to H and asked once while GitHub still reads open (N41); a merge
// call that "succeeded" is not done; the merged snapshot marks done with
// its merge commit (N2); then N1 stops the runs, deletes the branch and
// removes the clean worktree, once.
func TestLandMergesOncePinnedDoneOnlyOnMergedSnapshot(t *testing.T) {
	l := newLanding(t)
	l.pass()
	a := l.state().Tasks[0].Attempts[0]
	if len(a.Accepts) != 1 || a.Accepts[0].Head != l.c1 || a.Accepts[0].By != "bar" || !slices.Equal(a.Accepts[0].Rounds, []int{1, 2}) ||
		!flow.Done(a.Acts, "record_accept@"+l.c1) || !flow.Done(a.Acts, "merge@"+l.c1) {
		t.Fatalf("after N42: accepts=%+v acts=%+v", a.Accepts, a.Acts)
	}
	lines := l.logLines()
	if last := lines[len(lines)-1]; last.Rule != "N42" || !slices.Equal(last.Asked, []string{"record_accept@" + l.c1, "merge@" + l.c1}) {
		t.Fatalf("accept and merge not in one log line: %+v", last)
	}
	merges := ghCalls(t, l.gh, "pr merge")
	if len(merges) != 1 || !strings.HasPrefix(merges[0], "pr merge 7 --repo acme/demo --squash --match-head-commit "+l.c1+" --body (no brief)") {
		t.Fatalf("merge calls = %q", merges)
	}
	for _, bad := range []string{"--admin", "--auto", "--delete-branch"} {
		if strings.Contains(merges[0], bad) {
			t.Fatalf("merge call has %s: %q", bad, merges[0])
		}
	}

	// The merge call "succeeded", but GitHub still reads open: no done, no second merge.
	for range 3 {
		l.pass()
	}
	if s := l.state(); s.Tasks[0].Done != nil || len(ghCalls(t, l.gh, "pr merge")) != 1 {
		t.Fatalf("open snapshot after merge call: done=%+v merges=%q", s.Tasks[0].Done, ghCalls(t, l.gh, "pr merge"))
	}

	l.writeMerged(mergedSHA)
	l.pass() // N2
	if s := l.state(); s.Tasks[0].Done == nil || s.Tasks[0].Done.MergedCommit != mergedSHA {
		t.Fatalf("after merged snapshot: done=%+v", s.Tasks[0].Done)
	}
	if l.remoteBranch() == "" {
		t.Fatal("branch deleted in the same pass as done, before cleanup")
	}
	l.pass() // N1 cleanup
	if rs, err := world.RunState(context.Background(), l.client, l.worker); err != nil || rs != flow.RunGone {
		t.Fatalf("worker after cleanup: %s %v", rs, err)
	}
	if _, err := os.Stat(l.wt); !os.IsNotExist(err) {
		t.Fatalf("clean worktree not removed: %v", err)
	}
	if b := l.remoteBranch(); b != "" {
		t.Fatalf("remote branch not deleted: %q", b)
	}
	n := len(l.logLines())
	l.pass()
	l.pass()
	if a := l.state().Tasks[0].Attempts[0]; len(l.logLines()) != n || flow.Count(a.Acts, "cleanup") != 1 || len(ghCalls(t, l.gh, "pr merge")) != 1 {
		t.Fatalf("passes after cleanup changed things: lines %d -> %d, acts=%+v", n, len(l.logLines()), a.Acts)
	}
}

// TestLandMergeErrorStillDoneOnMergedSnapshot: a merge call that errs is
// not asked again while the PR reads open, and a snapshot that reads merged
// still marks the task done (KB-MRG-9: it may have gone through).
func TestLandMergeErrorStillDoneOnMergedSnapshot(t *testing.T) {
	l := newLanding(t)
	if err := os.WriteFile(filepath.Join(l.gh, "merge_fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l.pass()
	l.pass()
	a := l.state().Tasks[0].Attempts[0]
	if len(ghCalls(t, l.gh, "pr merge")) != 1 || flow.Count(a.Acts, "merge@"+l.c1) != 1 || flow.Done(a.Acts, "merge@"+l.c1) || len(a.Accepts) != 1 {
		t.Fatalf("failed merge: acts=%+v accepts=%+v merges=%q", a.Acts, a.Accepts, ghCalls(t, l.gh, "pr merge"))
	}
	l.writeMerged(mergedSHA)
	l.pass()
	if d := l.state().Tasks[0].Done; d == nil || d.MergedCommit != mergedSHA {
		t.Fatalf("merged snapshot after a failed merge call: done=%+v", d)
	}
}

// TestLandNoMergeAtOldHead: after the merge was asked at c1, a new ready
// moves H to c2. The engine asks no merge at c1 again and none at c2 until
// c2 passes review; the one call it made was pinned to c1.
func TestLandNoMergeAtOldHead(t *testing.T) {
	l := newLanding(t)
	l.pass()
	if err := os.WriteFile(filepath.Join(l.wt, "y.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.wt, "add", "y.go")
	gitIn(t, l.wt, "commit", "-m", "feat: y")
	c2 := gitIn(t, l.wt, "rev-parse", "HEAD")
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Ready = append(s.Tasks[0].Attempts[0].Ready, flow.Ready{Head: c2})
	})
	writePRJSON(t, l.gh, "view.json", c2, greenRollup)
	for range 3 {
		l.pass()
	}
	a := l.state().Tasks[0].Attempts[0]
	merges := ghCalls(t, l.gh, "pr merge")
	if len(merges) != 1 || !strings.Contains(merges[0], "--match-head-commit "+l.c1) || flow.Count(a.Acts, "merge@"+c2) != 0 || len(a.Accepts) != 1 {
		t.Fatalf("after H moved to c2: merges=%q acts=%+v accepts=%+v", merges, a.Acts, a.Accepts)
	}
}

// TestLandCleanup: cleanup stops the task's runs; it keeps a dirty
// worktree and logs it (KB-MRG-78); it deletes the remote branch only for
// a done task, whose PR read merged.
func TestLandCleanup(t *testing.T) {
	for _, tc := range []struct {
		name               string
		done, dirty        bool
		wantWT, wantBranch bool
	}{
		{"done and dirty", true, true, true, false},
		{"closed and clean", false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLanding(t)
			l.edit(func(s *flow.State) {
				if tc.done {
					s.Tasks[0].Done = &flow.Merged{MergedCommit: mergedSHA}
				} else {
					s.Tasks[0].Closed = &flow.Closed{Why: "test"}
				}
			})
			if tc.dirty {
				if err := os.WriteFile(filepath.Join(l.wt, "wip.txt"), []byte("unsaved"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			l.pass()
			if rs, err := world.RunState(context.Background(), l.client, l.worker); err != nil || rs != flow.RunGone {
				t.Fatalf("worker after cleanup: %s %v", rs, err)
			}
			_, err := os.Stat(filepath.Join(l.wt, "x.go"))
			if gotWT := err == nil; gotWT != tc.wantWT {
				t.Fatalf("worktree kept = %v, want %v", gotWT, tc.wantWT)
			}
			if gotBranch := l.remoteBranch() != ""; gotBranch != tc.wantBranch {
				t.Fatalf("remote branch kept = %v, want %v", gotBranch, tc.wantBranch)
			}
			if kept := strings.Contains(l.log.String(), "factory.worktree_kept"); kept != tc.dirty {
				t.Fatalf("worktree_kept logged = %v, want %v:\n%s", kept, tc.dirty, l.log.String())
			}
			s := l.state()
			if a := s.Tasks[0].Attempts[0]; !flow.Done(a.Acts, "cleanup") || a.RunID != "" {
				t.Fatalf("after cleanup: acts=%+v run=%q", a.Acts, a.RunID)
			}
		})
	}
}

// TestLandCleanupSplitAndDrop: N1 cleanup on a split or dropped task stops
// runs, closes its open PR with a short note (never merging it), keeps the
// remote branch, and removes a clean worktree.
func TestLandCleanupSplitAndDrop(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mark     func(*flow.Task)
		wantNote string
	}{
		{"split", func(tk *flow.Task) { tk.Split = &flow.Split{Children: []string{"T1a", "T1b"}} }, "split into T1a, T1b"},
		{"dropped", func(tk *flow.Task) { tk.Dropped = &flow.Dropped{Why: "out of scope"} }, "dropped (out of scope)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLanding(t)
			l.edit(func(s *flow.State) { tc.mark(&s.Tasks[0]) })
			l.pass()
			if rs, err := world.RunState(context.Background(), l.client, l.worker); err != nil || rs != flow.RunGone {
				t.Fatalf("worker after cleanup: %s %v", rs, err)
			}
			closes := ghCalls(t, l.gh, "pr close")
			if len(closes) != 1 || !strings.Contains(closes[0], tc.wantNote) || strings.Contains(closes[0], "--delete-branch") {
				t.Fatalf("pr close calls = %q, want one containing %q without --delete-branch", closes, tc.wantNote)
			}
			if merges := ghCalls(t, l.gh, "pr merge"); len(merges) != 0 {
				t.Fatalf("split/dropped task merged PR: %q", merges)
			}
			if b := l.remoteBranch(); b == "" {
				t.Fatal("remote branch was deleted; split/dropped task must keep it")
			}
			if _, err := os.Stat(l.wt); !os.IsNotExist(err) {
				t.Fatalf("clean worktree not removed: %v", err)
			}
		})
	}
}
