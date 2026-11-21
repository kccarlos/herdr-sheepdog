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
	"time"

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
	if err := l.eng.Start(repo, "Goal", 0); err != nil {
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
	if l.worker, err = startRun(ctx, l.client, world.RunSpec{Name: "worker-t1", Cwd: l.wt, Argv: []string{"sh"}}); err != nil {
		t.Fatal(err)
	}
	setIdle(t, srv, l.client, l.worker, "idle")
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

func startRun(ctx context.Context, client *hc.Client, spec world.RunSpec) (string, error) {
	t, _, err := world.StartTerminal(ctx, client, spec, nil)
	return t.Address, err
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

func setIdle(t testing.TB, srv *hctest.Server, client *hc.Client, id, state string) {
	t.Helper()
	srv.SetStatus(id, &hc.Status{App: "claude-code", State: state})
	term, err := client.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ms := uint64(time.Now().Add(-time.Minute).UnixMilli())
	term.StatusMS = &ms
	srv.Add(term)
}

func termState(ctx context.Context, client *hc.Client, id string) (flow.RunState, error) {
	evs, err := world.ObserveRun(ctx, client, flow.RunRecord{Address: id, State: flow.PhaseWorking})
	if err != nil {
		return "", err
	}
	rec, s, now := flow.RunRecord{Address: id, State: flow.PhaseWorking}, flow.DefaultSettings(), time.Now()
	for _, ev := range evs {
		rec, _, _ = flow.StepRun(rec, ev, s, now)
	}
	return rec.TaskRun().State, nil
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
	if rs, err := termState(context.Background(), l.client, l.worker); err != nil || rs != flow.RunGone {
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
			if rs, err := termState(context.Background(), l.client, l.worker); err != nil || rs != flow.RunGone {
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
			if rs, err := termState(context.Background(), l.client, l.worker); err != nil || rs != flow.RunGone {
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

// TestIrreversibleActionsRecheckFreshFacts verifies section 8's irreversible-action
// guard: execMerge re-reads fresh facts and aborts without calling gh pr merge if
// N42 no longer holds (e.g. checks turned red or worker started working), and
// execCleanup keeps a worktree when a stray live terminal is inside it.
func TestIrreversibleActionsRecheckFreshFacts(t *testing.T) {
	l := newLanding(t)
	ctx := context.Background()
	f := flow.Facts{
		Tip: l.c1, TipRel: flow.RelSame, Worker: flow.Run{State: flow.RunIdle},
		PR: flow.PR{Answered: true, Number: 7, State: flow.PROpen, Head: l.c1, HeadRel: flow.RelSame, Mergeable: flow.MergeableClean, Checks: flow.ChecksGreen},
	}
	// Flip PR checks to red on disk after the pass's snapshot: execMerge must re-read and call off (non-error) before gh pr merge.
	writePRJSON(t, l.gh, "view.json", l.c1, `[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"FAILURE"}]`)
	st := l.state()
	if dec, calledOff, err := l.eng.execMerge(ctx, l.id, &st, 0, f); err != nil || !calledOff || !strings.Contains(dec, "called off") {
		t.Fatalf("execMerge with stale green facts = (%q, calledOff=%v, err=%v), want non-error call-off", dec, calledOff, err)
	}
	if merges := ghCalls(t, l.gh, "pr merge"); len(merges) != 0 {
		t.Fatalf("gh pr merge called despite fresh red checks: %q", merges)
	}

	// Three consecutive call-offs across full engine passes record no failed merge act and never reach N5,
	// then merge immediately with no back-off once fresh facts hold N42.
	lCall := newLanding(t)
	greenView, err := os.ReadFile(filepath.Join(lCall.gh, "view.json"))
	if err != nil {
		t.Fatal(err)
	}
	redView := strings.Replace(string(greenView), `"SUCCESS"`, `"FAILURE"`, 1)
	flipBin := t.TempDir()
	realFakeGH, err := os.ReadFile(filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh"))
	if err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(flipBin, "real-gh")
	if err := os.WriteFile(realPath, realFakeGH, 0o700); err != nil {
		t.Fatal(err)
	}
	flipFlag := filepath.Join(flipBin, "flip")
	wrapper := "#!/bin/sh\n" +
		"if [ \"$1 $2\" = \"pr view\" ] && [ -f " + strconvQuote(flipFlag) + " ]; then\n" +
		"  c=" + strconvQuote(filepath.Join(flipBin, "n")) + "\n" +
		"  if [ -f \"$c\" ]; then rm -f \"$c\"; printf '%s' " + strconvQuote(redView) + "; exit 0; fi\n" +
		"  : > \"$c\"\n" +
		"fi\n" +
		"exec " + strconvQuote(realPath) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(flipBin, "gh"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", flipBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(flipFlag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	callNow := time.Now()
	lCall.eng.Now = func() time.Time { return callNow }
	for range 3 {
		lCall.pass()
		callNow = callNow.Add(5 * time.Minute)
	}
	lCall.pass()
	stCall := lCall.state()
	if stCall.Tasks[0].LastRow == "N5" || len(stCall.Project.Wakes) != 0 || flow.Count(stCall.Tasks[0].Attempts[0].Acts, "merge@"+lCall.c1) != 0 {
		t.Fatalf("after 3 fresh-check call-offs: row=%s wakes=%+v acts=%+v, want not N5 and 0 merge acts", stCall.Tasks[0].LastRow, stCall.Project.Wakes, stCall.Tasks[0].Attempts[0].Acts)
	}
	_ = os.Remove(flipFlag)
	lCall.pass()
	if merges := ghCalls(t, lCall.gh, "pr merge"); len(merges) != 1 {
		t.Fatalf("merges after call-offs cleared = %q, want 1 immediate merge", merges)
	}

	// Cleanup with a stray live terminal inside the worktree keeps the worktree.
	if _, err := startRun(ctx, l.client, world.RunSpec{Name: "stray-shell", Cwd: l.wt, Argv: []string{"sh"}}); err != nil {
		t.Fatal(err)
	}
	l.edit(func(s *flow.State) { s.Tasks[0].Closed = &flow.Closed{Why: "done elsewhere"} })
	l.pass()
	if _, err := os.Stat(l.wt); err != nil {
		t.Fatalf("worktree removed while live terminal was inside: %v", err)
	}
	if !strings.Contains(l.log.String(), "live terminal inside") {
		t.Fatalf("missing worktree_kept log for live terminal:\n%s", l.log.String())
	}
}

// TestSection8EngineBackoffOutageN5AndRetry exercises section 8 end-to-end
// through passProject: transient backoff + doubling, uncounted failures during
// github_unavailable and hc_unavailable, ensure_failures -> single N5 wake with
// cleaned/capped last words (never S2), recorded retry clearing N5, and a
// single settled refusal going straight to N5.
func TestSection8EngineBackoffOutageN5AndRetry(t *testing.T) {
	l := newLanding(t)
	coord, err := startRun(context.Background(), l.client, world.RunSpec{Name: "coord", Cwd: l.repo, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setIdle(t, l.srv, l.client, coord, "idle")
	l.edit(func(s *flow.State) { s.Project.RunID = coord })
	now := time.Now()
	l.eng.Now = func() time.Time { return now }
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "merge_fail", "HTTP 502: Bad Gateway\n\x07"+strings.Repeat("z", 600))

	// Try 1 fails transiently at t=0; not retried at 59s; retried at +1m.
	l.pass()
	if got := len(ghCalls(t, l.gh, "pr merge")); got != 1 {
		t.Fatalf("merge calls at t=0 = %d, want 1", got)
	}
	now = now.Add(59 * time.Second)
	l.pass()
	if got := len(ghCalls(t, l.gh, "pr merge")); got != 1 {
		t.Fatalf("merge retried before 1m backoff: calls=%d", got)
	}

	// Failure of merge (a GitHub act) during hc_unavailable is STILL counted (wrong system's outage flag).
	now = now.Add(time.Second)
	origHC := l.eng.HC
	l.eng.HC = hc.NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].RunID = "" })
	l.pass() // Try 2 (counted #2)
	l.eng.HC = origHC
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].RunID = l.worker })
	acts := l.state().Tasks[0].Attempts[0].Acts
	if last := acts[len(acts)-1]; last.Uncounted {
		t.Fatalf("GitHub merge failure during hc_unavailable was marked uncounted: %+v", last)
	}
	if got := len(ghCalls(t, l.gh, "pr merge")); got != 2 {
		t.Fatalf("merge calls after counted #2 = %d, want 2", got)
	}
	// Backoff doubles to 2m: not retried at +1m59s, retried at +2m (counted #3 -> hits ensure_failures=3).
	now = now.Add(2*time.Minute - time.Second)
	l.pass()
	if got := len(ghCalls(t, l.gh, "pr merge")); got != 2 {
		t.Fatalf("merge retried before doubled 2m backoff: calls=%d", got)
	}
	now = now.Add(time.Second)
	l.pass() // Try 3 fails -> reaches ensure_failures=3
	l.pass() // N5 fires wake_coordinator@failed@merge@<c1>

	st := l.state()
	if st.Tasks[0].LastRow != "N5" {
		t.Fatalf("row after ensure_failures = %s, want N5", st.Tasks[0].LastRow)
	}
	if len(st.Project.Wakes) != 0 {
		t.Fatalf("pending wakes after coordinator delivery = %+v, want 0", st.Project.Wakes)
	}
	sends := sendsTo(t, l.srv.Requests(), coord)
	if len(sends) != 1 || !strings.Contains(sends[0], "failed@merge@"+l.c1) || !strings.Contains(sends[0], "HTTP 502: Bad Gateway") || strings.Contains(sends[0], "\x07") {
		t.Fatalf("coordinator wake sends = %q, want 1 cleaned message with failed@merge and last words", sends)
	}

	// Even 31m later, N5 stays quiet and N42 never fires again — no loop.
	now = now.Add(31 * time.Minute)
	l.pass()
	if st := l.state(); st.Tasks[0].LastRow != "N5" || len(sendsTo(t, l.srv.Requests(), coord)) != 1 || len(ghCalls(t, l.gh, "pr merge")) != 3 {
		t.Fatalf("after 31m at N5: row=%s sends=%d merges=%d", st.Tasks[0].LastRow, len(sendsTo(t, l.srv.Requests(), coord)), len(ghCalls(t, l.gh, "pr merge")))
	}

	// Record a retry act and switch failure to a settled HTTP 422 refusal -> asked once, then N5 at once!
	writeGHFile(t, l.gh, "merge_fail", "HTTP 422: the base branch policy prohibits the merge")
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Acts = append(a.Acts, flow.Act{Key: flow.AskRetry, Result: "done", At: now})
	})
	l.pass() // retries merge@c1 once -> fails with ClassSettled
	l.pass() // goes straight to N5 on 1 settled failure
	st = l.state()
	if st.Tasks[0].LastRow != "N5" || len(sendsTo(t, l.srv.Requests(), coord)) != 2 || len(ghCalls(t, l.gh, "pr merge")) != 4 {
		t.Fatalf("after retry + settled refusal: row=%s sends=%d merges=%d", st.Tasks[0].LastRow, len(sendsTo(t, l.srv.Requests(), coord)), len(ghCalls(t, l.gh, "pr merge")))
	}
}

// TestGitHubOutageHourBoundedTriesAndNoStateGrowth verifies that an hour of
// github_unavailable gives a bounded number of tries (doubling back-off with a
// 30m cap) and overwrites the uncounted act in place (no state growth), while
// a settled failure is never uncounted even during github_unavailable.
func TestGitHubOutageHourBoundedTriesAndNoStateGrowth(t *testing.T) {
	l := newLanding(t)
	now := time.Now()
	l.eng.Now = func() time.Time { return now }
	l.edit(func(s *flow.State) { s.Tasks[0].Dropped = &flow.Dropped{Why: "obsolete"} })
	writeGHFile(t, l.gh, "fail", "") // HTTP 502: Bad Gateway -> github_unavailable

	for range 60 {
		l.pass()
		now = now.Add(time.Minute)
	}
	st := l.state()
	a := st.Tasks[0].Attempts[0]
	if views := len(ghCalls(t, l.gh, "pr view")); views != 6 {
		t.Fatalf("cleanup pr view tries during 1h github_unavailable = %d, want 6 (doubling backoff capped at 30m)", views)
	}
	if acts := flow.Count(a.Acts, flow.AskCleanup); acts != 1 {
		t.Fatalf("cleanup acts in state.json after 1h outage = %d, want 1 overwritten entry (acts=%+v)", acts, a.Acts)
	}
	if len(st.Project.Wakes) != 0 {
		t.Fatalf("wakes during transient github_unavailable = %+v, want none", st.Project.Wakes)
	}

	// A settled failure (HTTP 401) is never uncounted even while github_unavailable holds -> wakes once at once.
	_ = os.Remove(filepath.Join(l.gh, "fail"))
	writeGHFile(t, l.gh, "close_fail", "")
	// Replace pr close failure message via view+close: make pr close output HTTP 401 via a gh shim.
	binDir := t.TempDir()
	origGH, err := os.ReadFile(filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh"))
	if err != nil {
		t.Fatal(err)
	}
	origPath := filepath.Join(binDir, "orig-gh")
	if err := os.WriteFile(origPath, origGH, 0o700); err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\nif [ \"$1 $2\" = \"run list\" ]; then echo 'HTTP 502: Bad Gateway' >&2; exit 1; fi\nif [ \"$1 $2\" = \"pr close\" ]; then echo 'HTTP 401: Bad credentials' >&2; exit 1; fi\nexec " + strconvQuote(origPath) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	l.eng.ghUnavail = map[string]bool{l.id: true}
	now = now.Add(30 * time.Minute)
	l.pass() // cleanup fails with settled HTTP 401 while github_unavailable is true -> counted!
	l.pass() // N1 emits failed@cleanup wake once
	st = l.state()
	last := flow.LastAct(st.Tasks[0].Attempts[0].Acts, flow.AskCleanup)
	if last == nil || last.Uncounted || last.Class != flow.ClassSettled {
		t.Fatalf("settled cleanup act during github_unavailable = %+v, want Uncounted=false Class=settled", last)
	}
	if len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != "failed@cleanup" {
		t.Fatalf("wakes after settled cleanup failure = %+v, want [failed@cleanup]", st.Project.Wakes)
	}
}
