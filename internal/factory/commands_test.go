package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		s.Tasks = append(s.Tasks, flow.Task{ID: "T2", Title: "feat: after", After: []string{"T1"}},
			flow.Task{ID: "T3", Title: "feat: ended", Closed: &flow.Closed{Why: "x"}})
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
}

// TestN3ParkStopsRunsUnparkStartsAgain: a parked task keeps no run alive;
// after unpark the rows start the worker again.
func TestN3ParkStopsRunsUnparkStartsAgain(t *testing.T) {
	l := newLanding(t).withTokens()
	ctx := context.Background()
	rev, err := world.StartRun(ctx, l.client, world.RunSpec{Name: "rev-t1", Cwd: l.wt, Argv: []string{"sh"}})
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
		if rs, err := world.RunState(ctx, l.client, run); err != nil || rs != flow.RunGone {
			t.Fatalf("run %s after park: %s %v", run, rs, err)
		}
	}
	a := l.state().Tasks[0].Attempts[0]
	if a.RunID != "" || a.Review != nil || flow.Count(a.Acts, flow.AskStartWorker) != 0 || flow.Count(a.Acts, "tell_worker_findings@1") != 0 {
		t.Fatalf("attempt after N3: run %q review %+v acts %+v", a.RunID, a.Review, a.Acts)
	}
	l.pass()
	if a := l.state().Tasks[0].Attempts[0]; flow.Count(a.Acts, flow.AskStopRuns) != 1 || flow.Count(a.Acts, flow.AskStartWorker) != 0 {
		t.Fatalf("parked pass asked again: %+v", a.Acts)
	}
	l.report("tok-coord", "task", `{"action":"unpark","task":"T1","why":"go on"}`, "")
	l.pass()
	if a := l.state().Tasks[0].Attempts[0]; !flow.Done(a.Acts, flow.AskStartWorker) || a.RunID == "" {
		t.Fatalf("worker not started again after unpark: run %q acts %+v", a.RunID, a.Acts)
	}
}

// TestSendBackStaysWithWorkerUntilReady: the coordinator's send-back voids
// an in-flight review, N22 tells the worker the why once, the task stays
// with the worker (no review, no merge) through a commit without ready, and
// a ready again hands it back to review (KB-TSK-3, KB-RUN-6).
func TestSendBackStaysWithWorkerUntilReady(t *testing.T) {
	l := newLanding(t).withTokens()
	ctx := context.Background()
	rev, err := world.StartRun(ctx, l.client, world.RunSpec{Name: "rev-t1-3", Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{RunID: rev, Round: 3, Head: l.c1, Code: "c"}
	})
	l.report("tok-coord", "task", `{"action":"send-back","task":"T1","why":"the owner wants a test for the empty case"}`, "")
	l.pass()
	l.pass()
	if rs, _ := world.RunState(ctx, l.client, rev); rs != flow.RunGone {
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
	if a := l.state().Tasks[0].Attempts[0]; a.Review != nil || ladder() != flow.Fix || len(sendsTo(t, l.srv.Requests(), l.worker)) != 1 {
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
		s.Tasks = append(s.Tasks, flow.Task{ID: "T3", Title: "feat: ended", Dropped: &flow.Dropped{Why: "x"}})
	})
	seq := l.state().Seq
	l.report("tok-worker", "plan amend", `{"why":"w","tasks":[{"id":"T1","brief":"b"}]}`, "kind_not_allowed")
	l.report("tok-coord", "plan amend", `{"tasks":[{"id":"T1","brief":"b"}]}`, "plan_invalid")
	l.report("tok-coord", "plan amend", `{"why":"w","tasks":[{"id":"T3","brief":"b"}]}`, "plan_invalid")
	l.report("tok-coord", "plan amend", `{"why":"w","tasks":[{"id":"T1","after":["T3"]}]}`, "plan_invalid")
	l.report("tok-coord", "plan amend", `{"why":"w","tasks":[{"id":"T4","after":["NOPE"],"title":"feat: n"}]}`, "plan_invalid")
	if l.state().Seq != seq {
		t.Fatal("refusals changed state")
	}

	l.report("tok-coord", "plan amend", `{"why":"retitle","tasks":[{"id":"T1","title":"feat: y"},{"id":"T2","title":"feat: new","after":["T1"]}]}`, "")
	st := l.state()
	if st.Tasks[0].Title != "feat: y" || st.Tasks[0].Attempts[0].Boundary != 0 || len(st.Tasks) != 3 || st.Tasks[2].ID != "T2" {
		t.Fatalf("after title amend: %+v", st.Tasks)
	}
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
	l.report("tok-coord", "plan amend", `{"why":"narrower","tasks":[{"id":"T1","outcomes":["go test ./... passes"]}]}`, "")
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
	if rs, _ := world.RunState(context.Background(), l.client, l.worker); rs != flow.RunGone {
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
		Tasks: []flow.Task{{ID: "T1", Title: "feat: x", Attempts: []flow.Attempt{{
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
	rep("tok-coord", "task", `{"action":"close","task":"T1","why":"nothing to do"}`, "")
	if c := st().Tasks[0].Closed; c == nil || c.Why != "nothing to do" {
		t.Fatalf("closed = %+v", c)
	}
	eng.passProject(ctx, id, false)
	if a := st().Tasks[0].Attempts[0]; !flow.Done(a.Acts, flow.AskCleanup) {
		t.Fatalf("N1 did not clean up a closed task: %+v", a.Acts)
	}
}
