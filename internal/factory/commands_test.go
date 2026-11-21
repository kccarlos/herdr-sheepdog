package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// report sends verb with args as tok and fails the test unless the refusal
// code is want ("" for accepted).
func (l *landing) report(tok, verb, args, want string) {
	l.t.Helper()
	if err := l.eng.Report(tok, verb, json.RawMessage(args)); refusalCode(err) != want {
		l.t.Fatalf("%s %s = %v, want %q", verb, args, err, want)
	}
}

// withTokens gives the landing's coordinator and worker their tokens.
func (l *landing) withTokens() *landing {
	l.edit(func(s *flow.State) {
		s.Project.RunTokenHash = tokenHash("tok-coord")
		s.Tasks[0].Attempts[0].RunTokenHash = tokenHash("tok-worker")
	})
	return l
}

// TestTaskCommandsAcceptance: the acceptance table rows for park, unpark,
// drop, close and send-back (design.md 7.2).
func TestTaskCommandsAcceptance(t *testing.T) {
	l := newLanding(t).withTokens()
	l.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks, flow.Task{ID: "T2", Title: "feat: after", EstimateLines: 50, After: []string{"T1"}},
			flow.Task{ID: "T3", Title: "feat: ended", EstimateLines: 50, Closed: &flow.Closed{Why: "x"}})
	})
	seq := l.state().Seq
	l.report("tok-worker", "task", `{"action":"park","task":"T1","why":"w"}`, "kind_not_allowed")
	l.report("tok-coord", "task", `{"action":"park","task":"T9","why":"w"}`, "invalid_report")
	l.report("tok-coord", "task", `{"action":"park","task":"T3","why":"w"}`, "already_closed")
	l.report("tok-coord", "task", `{"action":"park","task":"T1","why":"  "}`, "why_missing")
	l.report("tok-coord", "task", `{"action":"close","task":"T1","why":"w"}`, "not_allowed")
	l.report("tok-coord", "task", `{"action":"send-back","task":"T2","why":"w"}`, "invalid_report")
	l.report("tok-coord", "task", `{"action":"bogus","task":"T1","why":"w"}`, "invalid_report")
	if l.state().Seq != seq {
		t.Fatal("refusals changed state")
	}

	l.report("tok-coord", "task", `{"action":"park","task":"T1","why":"owner asked"}`, "")
	if !l.state().Tasks[0].Parked {
		t.Fatal("park did not mark parked")
	}
	l.report("tok-coord", "task", `{"action":"unpark","task":"T1","why":"go on"}`, "")
	if l.state().Tasks[0].Parked {
		t.Fatal("unpark did not clear parked")
	}
	l.report("tok-coord", "task", `{"action":"send-back","task":"T1","why":"add a test for the empty case"}`, "")
	r := l.state().Tasks[0].Attempts[0].Rounds[2]
	if r.N != 3 || r.Kind != flow.RoundSendBack || r.Head != l.c1 || r.Verdict != flow.VerdictChanges || r.Why != "add a test for the empty case" {
		t.Fatalf("send-back round = %+v", r)
	}
	// A send-back is not the worker's to reject (2.3).
	l.report("tok-worker", "ready", `{"head":"`+l.c1+`","reject":"no"}`, "nothing_to_reject")

	// After a ready, a tip reset to the base is not "nothing to change".
	base := l.state().Tasks[0].Attempts[0].BaseCommit
	gitIn(t, l.wt, "reset", "--hard", base)
	l.report("tok-worker", "ready", `{"head":"`+base+`","nothing_to_change":"w"}`, "nothing_new")

	l.report("tok-coord", "task", `{"action":"drop","task":"T1","why":"not needed"}`, "")
	st := l.state()
	if st.Tasks[0].Dropped == nil || st.Tasks[0].Dropped.Why != "not needed" || strings.Join(st.Tasks[1].After, ",") != "T1" {
		t.Fatalf("after drop: T1 %+v, T2 after %v", st.Tasks[0].Dropped, st.Tasks[1].After)
	}
	if last := l.logLines()[len(l.logLines())-1]; !strings.Contains(last.Decision, "still waiting on it: T2") {
		t.Fatalf("drop log decision = %q, want the dependants named", last.Decision)
	}
	l.report("tok-coord", "task", `{"action":"unpark","task":"T1","why":"w"}`, "already_closed")
	// No report on an ended task: the worker's token still resolves until N1 runs.
	l.report("tok-worker", "ready", `{"head":"`+base+`","nothing_to_change":"w"}`, "already_closed")
}

// TestN3ParkStopsRunsUnparkStartsAgain: a parked task keeps no run alive;
// after unpark the rows start the worker again.
func TestN3ParkStopsRunsUnparkStartsAgain(t *testing.T) {
	l := newLanding(t).withTokens()
	ctx := context.Background()
	rev, err := startRun(ctx, l.client, world.RunSpec{Name: "rev-t1", Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(rev, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds = nil, nil // the worker is working: N23 once unparked
		a.Review = &flow.ReviewRun{RunID: rev, Round: 1, Head: l.c1, Code: "c"}
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done"}, {Key: "tell_worker_findings@1", Result: "done"}}
	})
	l.report("tok-coord", "task", `{"action":"park","task":"T1","why":"owner asked"}`, "")
	l.pass()
	for _, run := range []string{l.worker, rev} {
		if rs, err := termState(ctx, l.client, run); err != nil || rs != flow.RunGone {
			t.Fatalf("run %s after park: %s %v", run, rs, err)
		}
	}
	a := l.state().Tasks[0].Attempts[0]
	if a.RunID != "" || a.Review == nil || a.Review.RunID != "" || a.Review.Run == nil || a.Review.Run.Result != flow.EndStopped || a.Run == nil || a.Run.Result != flow.EndStopped {
		t.Fatalf("attempt after N3: run %q rec %+v review %+v", a.RunID, a.Run, a.Review)
	}
	l.pass()
	if a := l.state().Tasks[0].Attempts[0]; flow.Count(a.Acts, flow.AskStopRuns) != 1 || a.RunID != "" {
		t.Fatalf("parked pass asked again: run %q acts %+v", a.RunID, a.Acts)
	}
	l.report("tok-coord", "task", `{"action":"unpark","task":"T1","why":"go on"}`, "")
	l.pass()
	if a := l.state().Tasks[0].Attempts[0]; a.RunID == "" || a.Restarts != 0 {
		t.Fatalf("worker not started again uncounted after unpark: run %q restarts %d acts %+v", a.RunID, a.Restarts, a.Acts)
	}
}

// TestSendBackStaysWithWorkerUntilReady: the coordinator's send-back voids
// an in-flight review, N22 tells the worker the why once, the task stays
// with the worker (no review, no merge) through a commit without ready, and
// a ready again hands it back to review (KB-TSK-3, KB-RUN-6).
func TestSendBackStaysWithWorkerUntilReady(t *testing.T) {
	l := newLanding(t).withTokens()
	ctx := context.Background()
	rev, err := startRun(ctx, l.client, world.RunSpec{Name: "rev-t1-3", Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{RunID: rev, Round: 3, Head: l.c1, Code: "c"}
	})
	l.report("tok-coord", "task", `{"action":"send-back","task":"T1","why":"the owner wants a test for the empty case"}`, "")
	l.pass()
	l.pass()
	if rs, _ := termState(ctx, l.client, rev); rs != flow.RunGone {
		t.Fatalf("in-flight reviewer after send-back: %s", rs)
	}
	sends := sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends) != 1 || !strings.Contains(sends[0], "the owner wants a test for the empty case") {
		t.Fatalf("worker sends = %q, want the why once", sends)
	}
	if m := ghCalls(t, l.gh, "pr merge"); len(m) != 0 {
		t.Fatalf("merged a sent-back task: %q", m)
	}
	ladder := func() string {
		st := l.state()
		return flow.Ladder(flow.In{Task: st.Tasks[0], Settings: st.Settings})
	}
	if got := ladder(); got != flow.Fix {
		t.Fatalf("ladder after send-back = %s, want fix", got)
	}
	if err := os.WriteFile(filepath.Join(l.wt, "x_test.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.wt, "add", "x_test.go")
	gitIn(t, l.wt, "commit", "-m", "test: empty case")
	c2 := gitIn(t, l.wt, "rev-parse", "HEAD")
	l.pass()
	if a := l.state().Tasks[0].Attempts[0]; (a.Review != nil && a.Review.RunID != "") || ladder() != flow.Fix || len(sendsTo(t, l.srv.Requests(), l.worker)) != 1 {
		t.Fatalf("a commit without ready left the worker: review %+v ladder %s", a.Review, ladder())
	}
	l.report("tok-worker", "ready", `{"head":"`+c2+`"}`, "")
	if got := ladder(); got != flow.Review {
		t.Fatalf("ladder after ready again = %s, want review", got)
	}
}

// TestAmendBriefReviewsSameHeadAgain: plan amend of a started task's brief
// is a boundary: the two passes at c1 stop covering, so c1 is reviewed
// again rather than merged; the worker is told once. Title-only amends and
// new tasks are no boundary; ended tasks and other fields are refused.
func TestAmendBriefReviewsSameHeadAgain(t *testing.T) {
	l := newLanding(t).withTokens()
	l.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks, flow.Task{ID: "T3", Title: "feat: ended", EstimateLines: 50, Dropped: &flow.Dropped{Why: "x"}})
	})
	seq := l.state().Seq
	l.report("tok-worker", "plan amend", `{"why":"w","tasks":[{"id":"T1","brief":"b"}]}`, "kind_not_allowed")
	l.report("tok-coord", "plan amend", `{"tasks":[{"id":"T1","brief":"b"}]}`, "why_missing")
	l.report("tok-coord", "plan amend", `{"why":"w","tasks":[{"id":"T3","brief":"b"}]}`, "plan_invalid")
	l.report("tok-coord", "plan amend", `{"why":"w","tasks":[{"id":"T1","after":["T3"]}]}`, "plan_invalid")
	l.report("tok-coord", "plan amend", `{"why":"w","tasks":[{"id":"T4","after":["NOPE"],"estimate_lines":50,"title":"feat: n"}]}`, "plan_invalid")
	if l.state().Seq != seq {
		t.Fatal("refusals changed state")
	}

	l.report("tok-coord", "plan amend", `{"why":"retitle","tasks":[{"id":"T1","estimate_lines":50,"title":"feat: y"},{"id":"T2","estimate_lines":50,"title":"feat: new",`+testPlanJSON+`,"after":["T1"]}]}`, "")
	st := l.state()
	if st.Tasks[0].Title != "feat: y" || st.Tasks[0].Attempts[0].Boundary != 0 || len(st.Tasks) != 3 || st.Tasks[2].ID != "T2" {
		t.Fatalf("after title amend: %+v", st.Tasks)
	}
	l.finishWorkerTurn()
	l.report("tok-coord", "plan amend", `{"why":"the owner changed scope","tasks":[{"id":"T1","brief":"also handle empty input"}]}`, "")
	if a := l.state().Tasks[0].Attempts[0]; a.Boundary != 2 {
		t.Fatalf("boundary = %d, want 2", a.Boundary)
	}
	sends := sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends) != 2 || !strings.Contains(sends[1], "also handle empty input") || !strings.Contains(sends[1], "the owner changed scope") {
		t.Fatalf("worker sends = %q, want the brief change told", sends)
	}
	l.pass()
	if m := ghCalls(t, l.gh, "pr merge"); len(m) != 0 {
		t.Fatalf("merged on rounds from before the amend: %q", m)
	}
	if a := l.state().Tasks[0].Attempts[0]; flow.Count(a.Acts, "start_reviewer@3@"+l.c1) != 1 {
		t.Fatalf("same head not reviewed again: acts %+v", a.Acts)
	}
}

// TestRecapDecideNewOnlyAfterAmend: new is refused until the brief was
// amended since the recap opened; then it closes the attempt and its PR with
// a note (keeping the branch), and N11 starts a new attempt.
func TestRecapDecideNewOnlyAfterAmend(t *testing.T) {
	l := newLanding(t).withTokens()
	l.edit(func(s *flow.State) {
		s.Tasks[0].Recaps = []flow.Recap{{Trigger: "review", Allowed: []string{"split", "new", "drop"}}}
	})
	l.report("tok-coord", "recap decide", `{"task":"T1","choice":"new","why":"start over"}`, "not_allowed")
	l.report("tok-coord", "plan amend", `{"why":"narrower","tasks":[{"id":"T1","outcomes":[{"text":"go test passes","proved_by":"cmd: go test ./..."}]}]}`, "")
	if !l.state().Tasks[0].Recaps[0].Amended {
		t.Fatal("amend did not mark the open recap amended")
	}
	l.report("tok-coord", "recap decide", `{"task":"T1","choice":"new","why":"start over"}`, "")
	a := l.state().Tasks[0].Attempts[0]
	if a.Closed == "" || a.RunID != "" {
		t.Fatalf("attempt after new: %+v", a)
	}
	if c := ghCalls(t, l.gh, "pr close"); len(c) != 1 || !strings.Contains(c[0], "new attempt") || strings.Contains(c[0], "--delete-branch") {
		t.Fatalf("pr close calls = %q", c)
	}
	if rs, _ := termState(context.Background(), l.client, l.worker); rs != flow.RunGone {
		t.Fatalf("old worker after new: %s", rs)
	}
	l.pass()
	st := l.state()
	if n := len(st.Tasks[0].Attempts); n != 2 || st.Tasks[0].Attempts[1].Branch == l.branch || l.remoteBranch() == "" {
		t.Fatalf("after N11: %d attempts %+v, old branch %q", n, st.Tasks[0].Attempts, l.remoteBranch())
	}
}

// TestNothingToChangeN8ThenClose: ready --nothing-to-change needs a reason
// and no commit over the base; N8 wakes the coordinator once; close is
// refused before it and accepted after, and N1 then cleans up.
func TestNothingToChangeN8ThenClose(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	eng := New(paths, nil)
	const id = "proj-nothing"
	wt, err := world.NewWorktree(ctx, repo, paths.Worktree(id, "T1", 1), "T1", "feat: x")
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveState(paths.State(id), flow.State{
		Seq: 1, Project: flow.Project{ID: id, Folder: repo, Started: true, RunID: "coord", RunTokenHash: tokenHash("tok-coord")},
		Settings: flow.DefaultSettings(),
		Tasks: []flow.Task{{ID: "T1", Title: "feat: x", EstimateLines: 50, Attempts: []flow.Attempt{{SizeCap: 100,
			BaseCommit: base, Branch: wt.Branch, Worktree: wt.Path, RunTokenHash: tokenHash("tok-worker"),
			Acts: []flow.Act{{Key: flow.AskStartWorker, Result: "done"}},
		}}}},
	}); err != nil {
		t.Fatal(err)
	}
	rep := func(tok, verb, args, want string) {
		t.Helper()
		if err := eng.Report(tok, verb, json.RawMessage(args)); refusalCode(err) != want {
			t.Fatalf("%s %s = %v, want %q", verb, args, err, want)
		}
	}
	st := func() flow.State { s, _ := LoadState(paths.State(id)); return s }
	rep("tok-coord", "task", `{"action":"close","task":"T1","why":"w"}`, "not_allowed")
	rep("tok-worker", "ready", `{"head":"`+base+`","nothing_to_change":" "}`, "why_missing")
	if err := os.WriteFile(filepath.Join(wt.Path, "y.go"), []byte("package y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "add", "y.go")
	gitIn(t, wt.Path, "commit", "-m", "feat: y")
	rep("tok-worker", "ready", `{"head":"`+gitIn(t, wt.Path, "rev-parse", "HEAD")+`","nothing_to_change":"w"}`, "nothing_new")
	gitIn(t, wt.Path, "reset", "--hard", base)

	rep("tok-worker", "ready", `{"head":"`+base+`","nothing_to_change":"the helper already exists upstream"}`, "")
	eng.passProject(ctx, id, false)
	eng.passProject(ctx, id, false)
	w := st().Project.Wakes
	if len(w) != 1 || w[0].Key != "nothing_to_change@0" || !strings.Contains(w[0].Why, "N8") || !strings.Contains(w[0].Why, "already exists upstream") {
		t.Fatalf("held wakes = %+v, want one N8 wake with the reason", w)
	}
	// A commit after the report, with no ready: close is refused.
	if err := os.WriteFile(filepath.Join(wt.Path, "z.go"), []byte("package z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "add", "z.go")
	gitIn(t, wt.Path, "commit", "-m", "feat: z")
	rep("tok-coord", "task", `{"action":"close","task":"T1","why":"nothing to do"}`, "nothing_new")
	gitIn(t, wt.Path, "reset", "--hard", base)
	rep("tok-coord", "task", `{"action":"close","task":"T1","why":"nothing to do"}`, "")
	if c := st().Tasks[0].Closed; c == nil || c.Why != "nothing to do" {
		t.Fatalf("closed = %+v", c)
	}
	eng.passProject(ctx, id, false)
	if a := st().Tasks[0].Attempts[0]; !flow.Done(a.Acts, flow.AskCleanup) {
		t.Fatalf("N1 did not clean up a closed task: %+v", a.Acts)
	}
}

// TestWhysCleanedAndCapped: every why goes through cleanWhy: control
// characters become spaces and over 500 characters is refused, for a worker
// command (nothing-to-change) and coordinator ones (send-back, plan amend,
// whose titles are cleaned too).
func TestWhysCleanedAndCapped(t *testing.T) {
	l := newLanding(t).withTokens()
	long := strings.Repeat("x", 501)
	l.report("tok-coord", "task", `{"action":"send-back","task":"T1","why":"`+long+`"}`, "invalid_report")
	l.report("tok-coord", "plan amend", `{"why":"`+long+`","tasks":[{"id":"T1","brief":"b"}]}`, "invalid_report")
	l.report("tok-coord", "task", `{"action":"send-back","task":"T1","why":"add\u0007 a\ntest"}`, "")
	if r := l.state().Tasks[0].Attempts[0].Rounds[2]; r.Why != "add a test" {
		t.Fatalf("send-back why = %q, want cleaned", r.Why)
	}
	l.report("tok-coord", "plan amend", `{"why":"w\u001b[2J","tasks":[{"id":"T1","estimate_lines":50,"title":"feat:\ny"},{"id":"T2","estimate_lines":50,"title":"feat:\u0007 z",`+testPlanJSON+`}]}`, "")
	if st := l.state(); st.Tasks[0].Title != "feat: y" || st.Tasks[1].Title != "feat: z" {
		t.Fatalf("titles = %q, %q, want cleaned", st.Tasks[0].Title, st.Tasks[1].Title)
	}
	base := l.state().Tasks[0].Attempts[0].BaseCommit
	l.edit(func(s *flow.State) { a := &s.Tasks[0].Attempts[0]; a.Ready, a.Rounds = nil, nil })
	gitIn(t, l.wt, "reset", "--hard", base)
	l.report("tok-worker", "ready", `{"head":"`+base+`","nothing_to_change":"`+long+`"}`, "invalid_report")
	l.report("tok-worker", "ready", `{"head":"`+base+`","nothing_to_change":"done\u001b upstream"}`, "")
	if got := l.state().Tasks[0].Attempts[0].NothingToChange; got != "done upstream" {
		t.Fatalf("nothing-to-change why = %q, want cleaned", got)
	}
}

// TestRecapDecideNewCloseFailureKeepsRuns: a failed gh close leaves the
// worker alive and nothing committed, and returns the error.
func TestRecapDecideNewCloseFailureKeepsRuns(t *testing.T) {
	l := newLanding(t).withTokens()
	l.edit(func(s *flow.State) {
		s.Tasks[0].Recaps = []flow.Recap{{Trigger: "review", Allowed: []string{"split", "new", "drop"}, Amended: true}}
	})
	if err := os.WriteFile(filepath.Join(l.gh, "close_fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	seq := l.state().Seq
	err := l.eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T1","choice":"new","why":"start over"}`))
	if err == nil || refusalCode(err) != "" {
		t.Fatalf("new with a failing gh close = %v, want the gh error", err)
	}
	if rs, _ := termState(context.Background(), l.client, l.worker); rs == flow.RunGone {
		t.Fatal("worker stopped although the PR close failed")
	}
	if st := l.state(); st.Seq != seq || st.Tasks[0].Attempts[0].Closed != "" || st.Tasks[0].Attempts[0].RunID != l.worker {
		t.Fatalf("state changed after a failed new: seq %d->%d attempt %+v", seq, st.Seq, st.Tasks[0].Attempts[0])
	}
}

// TestTaskRetryN5AndRefusals verifies: retry is refused when not at N5/N4
// (nothing_to_retry) and on ended tasks (already_closed); at N5 it records
// retry in the failing key's acts list so the key is asked again and failure
// counts restart from zero; works for both attempt-level (merge) and
// task-level (new_attempt) failures, plus owner-quit / worker-restart resets.
func TestTaskRetryN5AndRefusals(t *testing.T) {
	l := newLanding(t).withTokens()
	now := l.eng.now()
	l.eng.Now = func() time.Time { return now }
	l.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks, flow.Task{ID: "T2", Title: "feat: ended", EstimateLines: 50, Closed: &flow.Closed{Why: "done"}})
	})

	// Refusals: worker role, ended task, healthy task not at N4/N5.
	l.report("tok-worker", "task", `{"action":"retry","task":"T1","why":"try"}`, "kind_not_allowed")
	l.report("tok-coord", "task", `{"action":"retry","task":"T2","why":"try"}`, "already_closed")
	l.report("tok-coord", "task", `{"action":"retry","task":"T1","why":"try"}`, "nothing_to_retry")

	// Drive T1 to N5 via 3 transient merge failures.
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "merge_fail", "HTTP 502: Bad Gateway")
	l.pass()
	now = now.Add(time.Minute)
	l.pass()
	now = now.Add(2 * time.Minute)
	l.pass()
	l.pass() // N5 wakes
	st := l.state()
	if st.Tasks[0].LastRow != "N5" || len(st.Project.Wakes) != 1 || !strings.Contains(st.Project.Wakes[0].Why, `task retry T1 --why`) {
		t.Fatalf("at N5: row=%s wakes=%+v", st.Tasks[0].LastRow, st.Project.Wakes)
	}

	// Retry clears N5; a second immediate retry is refused; removing merge_fail lets it merge.
	l.report("tok-coord", "task", `{"action":"retry","task":"T1","why":"GitHub back"}`, "")
	l.report("tok-coord", "task", `{"action":"retry","task":"T1","why":"again"}`, "nothing_to_retry")
	if err := os.Remove(filepath.Join(l.gh, "merge_fail")); err != nil {
		t.Fatal(err)
	}
	l.pass()
	if got := len(ghCalls(t, l.gh, "pr merge")); got != 4 {
		t.Fatalf("merge calls after retry = %d, want 4", got)
	}
	// If it fails again after retry, counts restarted from zero (1 transient failure does not reach N5).
	writeGHFile(t, l.gh, "merge_fail", "HTTP 502: Bad Gateway")
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Acts = append(a.Acts, flow.Act{Key: flow.AskRetry, Result: "done", At: now})
	})
	l.pass() // 1st failure after retry
	l.pass() // still waiting in backoff at N42, not N5
	if row := l.state().Tasks[0].LastRow; row != "N42" {
		t.Fatalf("after 1 failure post-retry: row=%s, want N42 (counts restarted from zero)", row)
	}

	// Task-level failure (new_attempt in t.Acts when no open attempt): retry records in t.Acts.
	l.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks, flow.Task{
			ID: "T3", Title: "feat: new", EstimateLines: 50, LastRow: "N5",
			Acts: []flow.Act{{Key: flow.AskNewAttempt, Result: "failed", Class: flow.ClassSettled, Err: "bad ref", At: now}},
		})
	})
	l.report("tok-coord", "task", `{"action":"retry","task":"T3","why":"fixed remote"}`, "")
	if !flow.Done(l.state().Tasks[2].Acts, flow.AskRetry) || flow.HasExhausted(l.state().Tasks[2].Acts, l.state().Settings) {
		t.Fatalf("T3 acts after retry = %+v", l.state().Tasks[2].Acts)
	}
}

// TestTaskRetryN4StartsNewAttemptAndBranch verifies N4 -> retry -> reads the PR
// fresh and closes it with closeOpenPR, stops runs, reports any dropped held
// say message, closes the attempt, and N11/N26 start a new attempt and open a
// new PR on a new branch while the old PR stays closed.
func TestTaskRetryN4StartsNewAttemptAndBranch(t *testing.T) {
	l := newLanding(t).withTokens()
	now := l.eng.now()
	l.eng.Now = func() time.Time { return now }
	writeGHFile(t, l.gh, "view.json", `{"number":7,"state":"CLOSED","headRefOid":"`+l.c1+`","mergeable":"MERGEABLE","statusCheckRollup":[],"baseRefName":"main"}`)
	l.pass()
	st := l.state()
	if st.Tasks[0].LastRow != "N4" || len(st.Project.Wakes) != 1 || !strings.Contains(st.Project.Wakes[0].Why, `task retry T1 --why`) {
		t.Fatalf("at N4: row=%s wakes=%+v", st.Tasks[0].LastRow, st.Project.Wakes)
	}
	// Hold a say message on the working worker, and reopen the PR before retry runs.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseWorking, Status: "working", StatusSince: now}
	})
	l.report("tok-coord", "task", `{"action":"say","task":"T1","why":"keep this in mind"}`, "")
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)

	l.report("tok-coord", "task", `{"action":"retry","task":"T1","why":"PR was closed by hand"}`, "")
	if closes := ghCalls(t, l.gh, "pr close"); len(closes) != 1 || !strings.Contains(closes[0], "7") {
		t.Fatalf("N4 retry did not close reopened PR #7: pr close calls = %q", closes)
	}
	if last := l.logLines()[len(l.logLines())-1]; !strings.Contains(last.Decision, "dropped 1 held message") {
		t.Fatalf("N4 retry decision = %q, want dropped held message reported", last.Decision)
	}
	if st := l.state(); len(st.Tasks[0].Attempts) != 1 || st.Tasks[0].Attempts[0].Closed == "" {
		t.Fatalf("after N4 retry: attempts=%+v", st.Tasks[0].Attempts)
	}
	if rs, _ := termState(context.Background(), l.client, l.worker); rs != flow.RunGone {
		t.Fatalf("old worker still live after N4 retry: %s", rs)
	}
	// Second retry before new attempt is refused.
	l.report("tok-coord", "task", `{"action":"retry","task":"T1","why":"again"}`, "nothing_to_retry")

	// Next pass: N11 creates attempt 2 on a new branch; then a ready commit opens a new PR on it.
	l.pass()
	st = l.state()
	if len(st.Tasks[0].Attempts) != 2 || st.Tasks[0].Attempts[1].Branch == l.branch || st.Tasks[0].Attempts[1].Branch == "" {
		t.Fatalf("after N11: attempts=%+v (old branch %q)", st.Tasks[0].Attempts, l.branch)
	}
	a2 := st.Tasks[0].Attempts[1]
	if err := os.WriteFile(filepath.Join(a2.Worktree, "x2.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, a2.Worktree, "add", "x2.go")
	gitIn(t, a2.Worktree, "commit", "-m", "feat: x2")
	c2 := gitIn(t, a2.Worktree, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(l.gh, "view.json")); err != nil {
		t.Fatal(err)
	}
	writePRJSON(t, l.gh, "pr.json", c2, greenRollup)
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[1]
		a.Ready = []flow.Ready{{Head: c2}}
		a.Acts = append(a.Acts, flow.Act{Key: flow.AskStartWorker, Result: "done", At: now})
	})
	l.pass()
	creates := ghCalls(t, l.gh, "pr create")
	if len(creates) != 1 || !strings.Contains(creates[0], a2.Branch) {
		t.Fatalf("pr create calls = %q, want 1 on new branch %q", creates, a2.Branch)
	}
	if reopens := ghCalls(t, l.gh, "pr reopen"); len(reopens) != 0 {
		t.Fatalf("old PR was reopened: %q", reopens)
	}
}

// TestTaskRetryN7SayStillDivergedToldAndWokenAgain verifies N7 -> say ->
// still diverged -> retry -> worker told again -> coordinator woken again.
func TestTaskRetryN7SayStillDivergedToldAndWokenAgain(t *testing.T) {
	l := newLanding(t).withTokens()
	now := l.eng.now()
	l.eng.Now = func() time.Time { return now }
	// Diverge the worktree tip from H (l.c1) via an amended commit.
	gitIn(t, l.wt, "commit", "--amend", "-m", "feat: diverged")
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseIdle, LastTurnEnd: now}
	})
	// Pass 1: N7 tells the worker once.
	l.pass()
	if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 1 {
		t.Fatalf("N7 initial tell sends = %d, want 1", got)
	}
	// Worker turn ends still diverged -> pass 2 wakes coordinator once.
	setStatusAt(t, l, l.worker, "idle", now.Add(time.Second))
	now = now.Add(time.Minute)
	l.pass()
	if st := l.state(); st.Tasks[0].LastRow != "N7" || len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != "diverged@tip@"+l.c1 {
		t.Fatalf("N7 first wake: row=%s wakes=%+v", st.Tasks[0].LastRow, st.Project.Wakes)
	}
	// Coordinator received the wake, sends say, tip is still diverged, then runs retry.
	l.edit(func(s *flow.State) { s.Project.Wakes = nil })
	l.report("tok-coord", "task", `{"action":"say","task":"T1","why":"restore the branch"}`, "")
	if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 2 {
		t.Fatalf("after say sends = %d, want 2", got)
	}
	l.report("tok-coord", "task", `{"action":"retry","task":"T1","why":"still diverged"}`, "")
	// Immediate second retry is refused.
	l.report("tok-coord", "task", `{"action":"retry","task":"T1","why":"again"}`, "nothing_to_retry")

	// Pass 3: N7 tells the worker again.
	setStatusAt(t, l, l.worker, "idle", now.Add(time.Second))
	now = now.Add(time.Minute)
	l.pass()
	if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 3 {
		t.Fatalf("after N7 retry tell sends = %d, want 3", got)
	}
	// Worker's next turn ends still diverged -> pass 4 wakes coordinator again.
	setStatusAt(t, l, l.worker, "idle", now.Add(time.Second))
	now = now.Add(time.Minute)
	l.pass()
	if st := l.state(); len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != "diverged@tip@"+l.c1 {
		t.Fatalf("N7 second wake after retry: wakes=%+v", st.Project.Wakes)
	}
}

// TestTaskRetryZeroBudgetStartsRun verifies worker_restarts: 0 and
// reviewer_retries: 0: failure -> retry -> next pass starts the run.
func TestTaskRetryZeroBudgetStartsRun(t *testing.T) {
	// Worker with worker_restarts: 0.
	lw := newLanding(t).withTokens()
	now := lw.eng.now()
	lw.eng.Now = func() time.Time { return now }
	lw.edit(func(s *flow.State) {
		s.Settings.WorkerRestarts = 0
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds, a.RunID = nil, nil, ""
		a.Run = &flow.RunRecord{Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: now}
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now}}
	})
	lw.pass() // wakes worker_failed
	if st := lw.state(); len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != "worker_failed" {
		t.Fatalf("worker budget 0 wakes = %+v, want worker_failed", st.Project.Wakes)
	}
	lw.report("tok-coord", "task", `{"action":"retry","task":"T1","why":"restart worker"}`, "")
	lw.pass() // must start worker uncounted
	if a := lw.state().Tasks[0].Attempts[0]; a.RunID == "" {
		t.Fatalf("worker not started after retry with worker_restarts=0: run=%+v acts=%+v", a.Run, a.Acts)
	}

	// Reviewer with reviewer_retries: 0.
	lr := newLanding(t).withTokens()
	now = lr.eng.now()
	lr.eng.Now = func() time.Time { return now }
	lr.edit(func(s *flow.State) {
		s.Settings.ReviewerRetries = 0
		a := &s.Tasks[0].Attempts[0]
		a.Rounds = nil
		a.Review = &flow.ReviewRun{Round: 1, Head: lr.c1, Run: &flow.RunRecord{Role: flow.RoleReviewer, CLI: "claude", State: flow.PhaseEnded, Result: flow.EndFailed, EndedAt: now}}
		a.Acts = []flow.Act{{Key: "start_reviewer@1@" + lr.c1, Result: "done", At: now}}
	})
	lr.pass() // wakes reviewer_failed
	if st := lr.state(); len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Key != "reviewer_failed@1@"+lr.c1 {
		t.Fatalf("reviewer budget 0 wakes = %+v, want reviewer_failed", st.Project.Wakes)
	}
	lr.report("tok-coord", "task", `{"action":"retry","task":"T1","why":"restart reviewer"}`, "")
	lr.pass() // must start reviewer uncounted
	if a := lr.state().Tasks[0].Attempts[0]; a.Review == nil || a.Review.RunID == "" {
		t.Fatalf("reviewer not started after retry with reviewer_retries=0: review=%+v acts=%+v", a.Review, a.Acts)
	}
}

// TestTaskRetryAndSaySmallFixes covers: spent restarts alone refused without
// re-opening merge@H; worker_failed/reviewer_failed wakes & coordinator.md
// name retry; wakes use e.Bin; say refuses no_worker instead of a raw error.
func TestTaskRetryAndSaySmallFixes(t *testing.T) {
	l := newLanding(t).withTokens()
	l.eng.Bin = "/opt/custom/sheepdog"
	now := l.eng.now()
	l.eng.Now = func() time.Time { return now }

	// 1. Spent restarts alone (no failure wake) -> retry refused, merge@H stays done.
	l.edit(func(s *flow.State) {
		s.Tasks[0].LastRow = "N41"
		a := &s.Tasks[0].Attempts[0]
		a.Restarts = s.Settings.WorkerRestarts
		a.RunID = ""
		a.Run = &flow.RunRecord{Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseEnded, Result: flow.EndStopped, EndedAt: now}
		a.Acts = []flow.Act{{Key: "merge@" + l.c1, Result: "done", At: now}}
	})
	l.report("tok-coord", "task", `{"action":"retry","task":"T1","why":"nothing failed"}`, "nothing_to_retry")
	if a := l.state().Tasks[0].Attempts[0]; flow.Count(a.Acts, flow.AskRetry) != 0 {
		t.Fatalf("refused retry recorded AskRetry (would re-open merge@H): acts=%+v", a.Acts)
	}

	// 2. say with no worker run after start_worker -> no_worker refusal, never raw error.
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.RunID, a.Run = "", nil
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now}}
	})
	l.report("tok-coord", "task", `{"action":"say","task":"T1","why":"hello"}`, "no_worker")

	// 3. worker_failed and reviewer_failed wakes name retry and use e.Bin.
	l.edit(func(s *flow.State) {
		s.Project.Wakes = nil
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds, a.RunID = nil, nil, ""
		a.Restarts = s.Settings.WorkerRestarts
		a.Run = &flow.RunRecord{Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: now}
		a.Acts = []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now}}
	})
	l.pass()
	if w := l.state().Project.Wakes; len(w) != 1 || !strings.Contains(w[0].Why, "/opt/custom/sheepdog factory task retry T1 --why") {
		t.Fatalf("worker_failed wake = %+v, want retry command with e.Bin", w)
	}
	l.edit(func(s *flow.State) {
		s.Project.Wakes = nil
		a := &s.Tasks[0].Attempts[0]
		a.Ready = []flow.Ready{{Head: l.c1}}
		a.Rounds = nil
		a.Review = &flow.ReviewRun{Round: 1, Head: l.c1, Retries: s.Settings.ReviewerRetries, Run: &flow.RunRecord{Role: flow.RoleReviewer, CLI: "claude", State: flow.PhaseEnded, Result: flow.EndFailed, EndedAt: now}}
		a.Acts = []flow.Act{{Key: "start_reviewer@1@" + l.c1, Result: "done", At: now}}
	})
	l.pass()
	if w := l.state().Project.Wakes; len(w) != 1 || !strings.Contains(w[0].Why, "/opt/custom/sheepdog factory task retry T1 --why") {
		t.Fatalf("reviewer_failed wake = %+v, want retry command with e.Bin", w)
	}

	// 4. N4 wake uses e.Bin, not hardcoded sheepdog.
	l.edit(func(s *flow.State) { s.Project.Wakes = nil })
	writeGHFile(t, l.gh, "view.json", `{"number":7,"state":"CLOSED","headRefOid":"`+l.c1+`","mergeable":"MERGEABLE","statusCheckRollup":[],"baseRefName":"main"}`)
	l.pass()
	if w := l.state().Project.Wakes; len(w) != 1 || !strings.Contains(w[0].Why, "/opt/custom/sheepdog factory task retry T1 --why") || strings.Contains(w[0].Why, "`sheepdog factory") {
		t.Fatalf("N4 wake = %+v, want e.Bin not hardcoded sheepdog", w)
	}

	// 5. coordinator.md names retry and say.
	if !strings.Contains(coordinatorTemplate, "send-back|retry|say") {
		t.Fatalf("coordinator.md missing retry|say:\n%s", coordinatorTemplate)
	}
}

// TestTaskSayDeliversIdleHoldsWorkingAndRefusesNoAttempt verifies task say:
// refused with no open attempt (no_worker), on ended task (already_closed),
// cleaned and capped; delivered immediately to an idle worker; held for a
// working worker and delivered once it goes idle.
func TestTaskSayDeliversIdleHoldsWorkingAndRefusesNoAttempt(t *testing.T) {
	l := newLanding(t).withTokens()
	now := l.eng.now()
	l.eng.Now = func() time.Time { return now }
	l.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks,
			flow.Task{ID: "T2", Title: "feat: no attempt", EstimateLines: 50},
			flow.Task{ID: "T3", Title: "feat: ended", EstimateLines: 50, Closed: &flow.Closed{Why: "x"}})
	})
	l.report("tok-worker", "task", `{"action":"say","task":"T1","why":"hi"}`, "kind_not_allowed")
	l.report("tok-coord", "task", `{"action":"say","task":"T2","why":"hi"}`, "no_worker")
	l.report("tok-coord", "task", `{"action":"say","task":"T3","why":"hi"}`, "already_closed")
	l.report("tok-coord", "task", `{"action":"say","task":"T1","why":"  "}`, "why_missing")
	l.report("tok-coord", "task", `{"action":"say","task":"T1","why":"`+strings.Repeat("x", 501)+`"}`, "invalid_report")

	// Idle worker: delivered immediately (R24), cleaned.
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseIdle}
	})
	l.report("tok-coord", "task", `{"action":"say","task":"T1","why":"restore\n\u0007 branch"}`, "")
	sends := sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends) != 1 || !strings.Contains(sends[0], "restore branch") || strings.ContainsAny(sends[0], "\n\x07") {
		t.Fatalf("idle say sends = %q, want 1 cleaned message", sends)
	}

	// Working worker: held in Run.Held (R26), not sent until idle grace passes (R18).
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseWorking, Status: "working", StatusSince: now}
	})
	l.report("tok-coord", "task", `{"action":"say","task":"T1","why":"check origin/main"}`, "")
	if got := len(sendsTo(t, l.srv.Requests(), l.worker)); got != 1 {
		t.Fatalf("working say sent immediately: sends=%d, want 1", got)
	}
	if held := l.state().Tasks[0].Attempts[0].Run.Held; len(held) != 1 || !strings.Contains(held[0], "check origin/main") {
		t.Fatalf("working say held = %q, want 1 held message", held)
	}
	// Worker becomes idle and idle grace elapses -> R18 delivers held message.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "idle"})
	l.pass()
	now = now.Add(time.Duration(l.state().Settings.IdleGraceSeconds) * time.Second)
	l.pass()
	sends = sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends) != 2 || !strings.Contains(sends[1], "check origin/main") {
		t.Fatalf("after idle grace sends = %q, want held message delivered", sends)
	}
	if held := l.state().Tasks[0].Attempts[0].Run.Held; len(held) != 0 {
		t.Fatalf("held after delivery = %q, want empty", held)
	}
}
