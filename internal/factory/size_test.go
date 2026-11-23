package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// TestPlanEstimateRequiredAndLimited: estimate_lines is required and a task
// over task_size_limit is refused in plan apply, plan amend and split
// children (design.md 11.5); an amended estimate changes a started task's
// size_cap, and a new attempt starts at size_cap_factor × estimate_lines.
func TestPlanEstimateRequiredAndLimited(t *testing.T) {
	_, fails := checkPlanWith(json.RawMessage(`{"tasks":[{"id":"A","title":"a"},{"id":"B","title":"b","estimate_lines":401}]}`), nil, "", flow.Settings{TaskSizeLimit: 400})
	if got := strings.Join(fails, "; "); !strings.Contains(got, `task "A" needs estimate_lines`) || !strings.Contains(got, `"B" estimates 401 lines, over task_size_limit 400: split it before it exists`) {
		t.Fatalf("plan apply fails = %q", got)
	}
	l := newLanding(t).withTokens()
	for _, bad := range []string{
		`{"why":"w","tasks":[{"id":"T1","estimate_lines":0}]}`,
		`{"why":"w","tasks":[{"id":"T1","estimate_lines":401}]}`,
		`{"why":"w","tasks":[{"id":"T2","title":"feat: two"}]}`,
	} {
		l.report("tok-coord", "plan amend", bad, "plan_invalid")
	}
	l.report("tok-coord", "plan amend", `{"why":"bigger than thought","tasks":[{"id":"T1","estimate_lines":30},{"id":"T2","title":"feat: two","estimate_lines":40,`+testPlanJSON+`}]}`, "")
	if st := l.state(); st.Tasks[0].EstimateLines != 30 || st.Tasks[0].Attempts[0].SizeCap != 60 {
		t.Fatalf("after amend: estimate %d, size_cap %d, want 30 and 60", st.Tasks[0].EstimateLines, st.Tasks[0].Attempts[0].SizeCap)
	}
	l.pass() // T2: N11
	if a := l.state().Tasks[1].Attempts; len(a) != 1 || a[0].SizeCap != 80 {
		t.Fatalf("T2 attempts = %+v, want one with size_cap 80", a)
	}
}

// TestSizeCapRecapContinue: N12 opens a size recap at cap + 1 and not at the
// cap, and the worker is told once to stop; continue raises the cap to lines
// now + continue_lines and tells the worker; the next size recap does not
// offer continue (continues_per_task 1), and a child without an estimate is
// refused.
func TestSizeCapRecapContinue(t *testing.T) {
	l := newLanding(t).withTokens()
	writePRJSON(t, l.gh, "view.json", l.c1, "[]") // no checks yet: N36 waits, nothing merges
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].SizeCap = 1 })
	l.pass()
	if st := l.state(); len(st.Tasks[0].Recaps) != 0 {
		t.Fatalf("1 changed line at cap 1 opened a recap: %+v", st.Tasks[0].Recaps)
	}
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].SizeCap = 0 })
	l.pass() // N12
	l.pass() // N6: wake and tell
	l.pass()
	rc := l.state().Tasks[0].Recaps
	if len(rc) != 1 || rc[0].Trigger != "size" || !slices.Equal(rc[0].Allowed, []string{"continue", "split", "new", "drop"}) {
		t.Fatalf("recaps = %+v", rc)
	}
	if w := l.state().Project.Wakes; len(w) != 1 || !strings.Contains(w[0].Facts, "changed lines 1, size cap 0, estimate 50") {
		t.Fatalf("wakes = %+v, want the size facts", w)
	}
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 1 || !strings.Contains(sends[0], "Stop now and write nothing more") {
		t.Fatalf("worker sends = %q, want one stop", sends)
	}

	l.finishWorkerTurn()
	l.report("tok-coord", "recap decide", `{"task":"T1","choice":"continue","why":"the test needs room"}`, "")
	st := l.state()
	if a := st.Tasks[0].Attempts[0]; a.SizeCap != 1+st.Settings.ContinueLines || flow.OpenRecap(st.Tasks[0]) != nil {
		t.Fatalf("after continue: size_cap %d, open recap %+v", a.SizeCap, flow.OpenRecap(st.Tasks[0]))
	}
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 2 || !strings.Contains(sends[1], "size cap is now 61") {
		t.Fatalf("worker sends = %q, want the continue", sends)
	}

	if err := os.WriteFile(filepath.Join(l.wt, "big.go"), []byte(strings.Repeat("// line\n", 70)), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.wt, "add", "big.go")
	gitIn(t, l.wt, "commit", "-m", "feat: grows")
	l.pass() // N12 again
	rc = l.state().Tasks[0].Recaps
	if len(rc) != 2 || slices.Contains(rc[1].Allowed, "continue") {
		t.Fatalf("second recap = %+v, want no continue", rc)
	}
	l.report("tok-coord", "recap decide", `{"task":"T1","choice":"continue","why":"again"}`, "not_allowed")
	l.report("tok-coord", "recap decide", `{"task":"T1","choice":"split","why":"w","children":[{"id":"T1a","title":"feat: a"}]}`, "plan_invalid")
}

// TestN20MergeOnlyPushedNoRound: a clean merge of the base into the branch
// moves H with no round, N26 pushes it, and the two passes at c1 still
// cover it, so the task merges at the merge commit.
func TestN20MergeOnlyPushedNoRound(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, "[]")
	if err := os.WriteFile(filepath.Join(l.repo, "base.go"), []byte("package demo\nvar Base = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.repo, "add", "base.go")
	gitIn(t, l.repo, "commit", "-m", "feat: base moves")
	gitIn(t, l.repo, "push", "origin", "main")
	gitIn(t, l.wt, "fetch", "origin")
	gitIn(t, l.wt, "merge", "--no-edit", "origin/main")
	m1 := gitIn(t, l.wt, "rev-parse", "HEAD")

	l.pass() // N20
	a := l.state().Tasks[0].Attempts[0]
	if flow.H(a) != m1 || len(a.MergeOnly) != 1 || a.Review != nil || len(a.Rounds) != 2 {
		t.Fatalf("after N20: H %s (want %s), merge_only %+v, review %+v, rounds %d", flow.H(a), m1, a.MergeOnly, a.Review, len(a.Rounds))
	}
	l.pass() // N26 pushes m1
	if b := l.remoteBranch(); !strings.HasPrefix(b, m1) {
		t.Fatalf("remote branch = %q, want %s", b, m1)
	}
	writePRJSON(t, l.gh, "view.json", m1, greenRollup)
	l.pass() // N42
	a = l.state().Tasks[0].Attempts[0]
	if merges := ghCalls(t, l.gh, "pr merge"); len(merges) != 1 || !strings.Contains(merges[0], "--match-head-commit "+m1) || len(a.Rounds) != 2 || a.Review != nil {
		t.Fatalf("merges %q, rounds %d, review %+v", merges, len(a.Rounds), a.Review)
	}
	if len(a.Accepts) != 1 || a.Accepts[0].Head != m1 || !slices.Equal(a.Accepts[0].Rounds, []int{1, 2}) {
		t.Fatalf("accepts = %+v, want one at %s on rounds 1 and 2", a.Accepts, m1)
	}
}

// TestReadyAtMergeOnlyTip: a worker that reports ready at a clean base merge
// before N20's pass keeps the passes at c1 covering; a merge with a hand
// edit does not, so it is reviewed.
func TestReadyAtMergeOnlyTip(t *testing.T) {
	for _, edited := range []bool{false, true} {
		l := newLanding(t).withTokens()
		if err := os.WriteFile(filepath.Join(l.repo, "base.go"), []byte("package demo\nvar Base = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, l.repo, "add", "base.go")
		gitIn(t, l.repo, "commit", "-m", "feat: base moves")
		gitIn(t, l.repo, "push", "origin", "main")
		gitIn(t, l.wt, "fetch", "origin")
		gitIn(t, l.wt, "merge", "--no-commit", "--no-ff", "origin/main")
		if edited {
			if err := os.WriteFile(filepath.Join(l.wt, "smuggled.go"), []byte("package demo\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitIn(t, l.wt, "add", "smuggled.go")
		}
		gitIn(t, l.wt, "commit", "--no-edit")
		m1 := gitIn(t, l.wt, "rev-parse", "HEAD")
		l.report("tok-worker", "ready", `{"head":"`+m1+`"}`, "")
		st := l.state()
		want := flow.Passed
		if edited {
			want = flow.Review
		}
		if got := flow.Ladder(flow.In{Task: st.Tasks[0], Settings: st.Settings}); got != want || flow.H(st.Tasks[0].Attempts[0]) != m1 {
			t.Fatalf("edited=%v: ladder %s (want %s), H %s", edited, got, want, flow.H(st.Tasks[0].Attempts[0]))
		}
	}
}

// passReport submits a valid pass verdict for rev's reviewer through its
// CLI's channel (report review command for Claude/agy, screen tag for Codex).
func (l *landing) passReport(rev *flow.ReviewRun) {
	l.t.Helper()
	if err := submitFakeReviewerVerdict(l.t, l.eng, l.srv, l.paths, l.id, "T1", rev, l.reviewerToken(rev.RunID), flow.VerdictPass, 0, "Fine.\n"); err != nil {
		l.t.Fatalf("passReport round %d: %v", rev.Round, err)
	}
}

// TestRoundInFlightSurvivesMergeOnlyMove: a clean base merge while round 1
// runs at c1 moves H but does not stop the reviewer; its pass at c1 counts
// at the new H, and with a second pass the task merges there.
func TestRoundInFlightSurvivesMergeOnlyMove(t *testing.T) {
	ctx := context.Background()
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, "[]")
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Rounds = []flow.Round{} })
	l.pass() // N29: round 1 at c1
	rev := l.state().Tasks[0].Attempts[0].Review
	if rev == nil || rev.RunID == "" || rev.Head != l.c1 {
		t.Fatalf("review = %+v, want round 1 running at c1", rev)
	}
	if err := os.WriteFile(filepath.Join(l.repo, "base.go"), []byte("package demo\nvar Base = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, l.repo, "add", "base.go")
	gitIn(t, l.repo, "commit", "-m", "feat: base moves")
	gitIn(t, l.repo, "push", "origin", "main")
	gitIn(t, l.wt, "fetch", "origin")
	gitIn(t, l.wt, "merge", "--no-edit", "origin/main")
	m1 := gitIn(t, l.wt, "rev-parse", "HEAD")

	l.pass() // N20
	l.pass() // N26 pushes m1
	writePRJSON(t, l.gh, "view.json", m1, "[]")
	a := l.state().Tasks[0].Attempts[0]
	if rs, _ := termState(ctx, l.client, rev.RunID); flow.H(a) != m1 || a.Review == nil || a.Review.RunID != rev.RunID || rs == flow.RunGone {
		t.Fatalf("after the merge-only move: H %s, review %+v, reviewer %s", flow.H(a), a.Review, rs)
	}
	l.passReport(rev)
	l.pass() // the verdict at c1
	a = l.state().Tasks[0].Attempts[0]
	if len(a.Rounds) != 1 || a.Rounds[0].Head != l.c1 || a.Rounds[0].Verdict != flow.VerdictPass {
		t.Fatalf("rounds = %+v, want round 1 pass at c1", a.Rounds)
	}
	l.pass() // N29: round 2 at m1
	rev2 := l.state().Tasks[0].Attempts[0].Review
	if rev2 == nil || rev2.Round != 2 || rev2.Head != m1 {
		t.Fatalf("second review = %+v, want round 2 at %s", rev2, m1)
	}
	l.passReport(rev2)
	l.pass() // verdict 2
	writePRJSON(t, l.gh, "view.json", m1, greenRollup)
	l.pass() // N42
	a = l.state().Tasks[0].Attempts[0]
	if len(a.Accepts) != 1 || a.Accepts[0].Head != m1 || !slices.Equal(a.Accepts[0].Rounds, []int{1, 2}) {
		t.Fatalf("accepts = %+v, want one at %s on rounds 1 and 2", a.Accepts, m1)
	}
}

// TestRecapStopsReviewerInFlight: an amend that lowers the estimate under
// the lines while a reviewer runs opens a size recap; the reviewer is then
// stopped and its tag is not accepted (N6: no reviewer runs).
func TestRecapStopsReviewerInFlight(t *testing.T) {
	ctx := context.Background()
	l := newLanding(t).withTokens()
	c2 := commitFile(t, l.wt, "more.go", strings.Repeat("// line\n", 10))
	gitIn(t, l.wt, "push", "origin", c2+":refs/heads/"+l.branch)
	writePRJSON(t, l.gh, "view.json", c2, "[]")
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds = append(a.Ready, flow.Ready{Head: c2}), []flow.Round{}
	})
	l.pass() // N29 at c2
	rev := l.state().Tasks[0].Attempts[0].Review
	if rev == nil || rev.RunID == "" {
		t.Fatalf("review = %+v, want one running", rev)
	}
	l.report("tok-coord", "plan amend", `{"why":"smaller","tasks":[{"id":"T1","estimate_lines":3}]}`, "")
	l.pass() // N12 opens the size recap and stops the in-flight reviewer immediately (N6, I1)
	if flow.OpenRecap(l.state().Tasks[0]) == nil {
		t.Fatal("no size recap opened")
	}
	a := l.state().Tasks[0].Attempts[0]
	if rs, _ := termState(ctx, l.client, rev.RunID); rs != flow.RunGone || a.Review == nil || a.Review.RunID != "" || a.Review.Run == nil || a.Review.Run.Result != flow.EndStopped || len(a.Rounds) != 0 {
		t.Fatalf("with a recap open: reviewer %s, review %+v, rounds %+v", rs, a.Review, a.Rounds)
	}
	// If a live reviewer still exists alongside an open recap (e.g. crash before stop),
	// report review is refused already_closed (RV2) and observeReviewLocked stops it.
	rev2ID, err := startRun(ctx, l.client, world.RunSpec{Name: "rev-t1-recap", Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	const rev2Tok = "tok-rev-recap"
	rev2 := &flow.ReviewRun{Round: 1, Head: c2, Code: "recapcode", RunID: rev2ID, RunTokenHash: tokenHash(rev2Tok)}
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Review = rev2 })
	if err := submitFakeReviewerVerdict(t, l.eng, l.srv, l.paths, l.id, "T1", rev2, rev2Tok, flow.VerdictPass, 0, "Fine.\n"); refusalCode(err) != "already_closed" {
		t.Fatalf("review verdict while recap open = %v, want already_closed", err)
	}
	l.pass()
	a = l.state().Tasks[0].Attempts[0]
	if rs, _ := termState(ctx, l.client, rev2ID); rs != flow.RunGone || a.Review == nil || a.Review.RunID != "" || a.Review.Run == nil || a.Review.Run.Result != flow.EndStopped || len(a.Rounds) != 0 {
		t.Fatalf("observeReview with open recap: reviewer %s, review %+v, rounds %+v", rs, a.Review, a.Rounds)
	}
}

func commitFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", name)
	gitIn(t, dir, "commit", "-m", "feat: "+name)
	return gitIn(t, dir, "rev-parse", "HEAD")
}

// TestAmendEstimateSizeCapRules checks the two PR 3.1 amend fixes (design.md 4.9):
// raising estimate_lines on a started task never raises the open attempt's
// size_cap (bypassing continues_per_task), and while a size recap is open no
// estimate amend changes the open attempt's size_cap; the new estimate applies
// to the next attempt (recap decide new) or to an unstarted task.
func TestAmendEstimateSizeCapRules(t *testing.T) {
	l := newLanding(t).withTokens()
	// T1 starts with EstimateLines=50, SizeCap=100.
	// 1. Raising estimate on a started task (no recap): estimate updates, SizeCap stays 100.
	l.report("tok-coord", "plan amend", `{"why":"bigger","tasks":[{"id":"T1","estimate_lines":200}]}`, "")
	if st := l.state(); st.Tasks[0].EstimateLines != 200 || st.Tasks[0].Attempts[0].SizeCap != 100 {
		t.Fatalf("after raise amend: estimate %d, size_cap %d, want 200 and 100", st.Tasks[0].EstimateLines, st.Tasks[0].Attempts[0].SizeCap)
	}
	if last := l.logLines()[len(l.logLines())-1]; !strings.Contains(last.Decision, "size cap unchanged at 100") || !strings.Contains(last.Decision, "amend cannot raise") {
		t.Fatalf("raise amend decision = %q, want plain note that size cap is unchanged and why", last.Decision)
	}
	// 2. Lowering estimate when no size recap is open lowers SizeCap.
	l.report("tok-coord", "plan amend", `{"why":"tighter","tasks":[{"id":"T1","estimate_lines":25}]}`, "")
	if st := l.state(); st.Tasks[0].EstimateLines != 25 || st.Tasks[0].Attempts[0].SizeCap != 50 {
		t.Fatalf("after lower amend: estimate %d, size_cap %d, want 25 and 50", st.Tasks[0].EstimateLines, st.Tasks[0].Attempts[0].SizeCap)
	}
	// 3. Open a size recap: neither raising nor lowering estimate changes SizeCap.
	l.edit(func(s *flow.State) {
		s.Tasks[0].Recaps = []flow.Recap{{Trigger: flow.TriggerSize, Allowed: []string{"continue", "split", "new", "drop"}}}
	})
	l.report("tok-coord", "plan amend", `{"why":"raise during size recap","tasks":[{"id":"T1","estimate_lines":150}]}`, "")
	if st := l.state(); st.Tasks[0].EstimateLines != 150 || st.Tasks[0].Attempts[0].SizeCap != 50 {
		t.Fatalf("raise during size recap: estimate %d, size_cap %d, want 150 and 50", st.Tasks[0].EstimateLines, st.Tasks[0].Attempts[0].SizeCap)
	}
	if last := l.logLines()[len(l.logLines())-1]; !strings.Contains(last.Decision, "size cap unchanged at 50") || !strings.Contains(last.Decision, "size recap is open") {
		t.Fatalf("amend during size recap decision = %q, want plain note that size cap is unchanged while size recap is open", last.Decision)
	}
	l.report("tok-coord", "plan amend", `{"why":"lower during size recap","tasks":[{"id":"T1","estimate_lines":10,"brief":"new brief"}]}`, "")
	if st := l.state(); st.Tasks[0].EstimateLines != 10 || st.Tasks[0].Attempts[0].SizeCap != 50 {
		t.Fatalf("lower during size recap: estimate %d, size_cap %d, want 10 and 50", st.Tasks[0].EstimateLines, st.Tasks[0].Attempts[0].SizeCap)
	}
	// 4. recap decide new starts attempt 2 with the amended estimate_lines (10 * 2 = 20).
	l.report("tok-coord", "recap decide", `{"task":"T1","choice":"new","why":"fresh start"}`, "")
	l.pass() // N11 starts attempt 2
	if a := l.state().Tasks[0].Attempts; len(a) != 2 || a[1].SizeCap != 20 {
		t.Fatalf("attempt 2 size_cap = %+v, want 20", a)
	}
	// 5. Unstarted task: raising estimate sets its first attempt's SizeCap.
	l.report("tok-coord", "plan amend", `{"why":"add T2","tasks":[{"id":"T2","title":"feat: two","estimate_lines":30,`+testPlanJSON+`}]}`, "")
	l.report("tok-coord", "plan amend", `{"why":"raise unstarted","tasks":[{"id":"T2","estimate_lines":90}]}`, "")
	l.pass() // N11 starts T2
	if a := l.state().Tasks[1].Attempts; len(a) != 1 || a[0].SizeCap != 180 {
		t.Fatalf("unstarted T2 size_cap = %+v, want 180", a)
	}
}

// fakeDiffExec routes engine git calls through an hctest.Server whose `diff`
// invocations deterministically time out while slow is true, fail while fail is
// true, and otherwise delegate to hctest.GitExec.
func fakeDiffExec(t *testing.T, engs ...*Engine) (slow, fail *atomic.Bool, calls *atomic.Int32) {
	t.Helper()
	slow, fail, calls = &atomic.Bool{}, &atomic.Bool{}, &atomic.Int32{}
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if len(p.Argv) > 1 && p.Argv[0] == "git" && p.Argv[1] == "diff" {
			calls.Add(1)
			if slow.Load() {
				return hc.ExecResult{TimedOut: true}, nil
			}
			if fail.Load() {
				code := 1
				return hc.ExecResult{Code: &code, Stderr: []byte("git diff failed\n")}, nil
			}
		}
		return hctest.GitExec(p)
	}
	for _, eng := range engs {
		eng.git = world.NewGit(hc.NewClient(srv.Path), eng.now, func(event, folder string) {
			eng.logEvent(event, map[string]string{"folder": folder})
		})
	}
	return slow, fail, calls
}

// TestChangedLinesTimeoutOpensSizeRecap: a changed-lines read that times out
// counts as over size_cap (N12 opens a size recap) instead of skipping the
// pass, and is never cached so after continue a normal read opens no second
// recap; a non-timeout error still skips the pass.
func TestChangedLinesTimeoutOpensSizeRecap(t *testing.T) {
	lErr := newLanding(t)
	l := newLanding(t).withTokens()
	writePRJSON(t, l.gh, "view.json", l.c1, "[]")
	slow, fail, calls := fakeDiffExec(t, lErr.eng, l.eng)

	// Non-timeout error skips the pass and opens no recap.
	fail.Store(true)
	lErr.pass()
	fail.Store(false)
	if rc := lErr.state().Tasks[0].Recaps; len(rc) != 0 {
		t.Fatalf("non-timeout error opened recap: %+v", rc)
	}

	// Timeout opens a size recap via N12, then N6 wakes the coordinator and tells the worker.
	slow.Store(true)
	l.pass() // N12
	l.pass() // N6: wakes coordinator, tells worker
	st := l.state()
	if len(st.Tasks[0].Recaps) != 1 || st.Tasks[0].Recaps[0].Trigger != flow.TriggerSize {
		t.Fatalf("recaps after timeout = %+v, want one size recap", st.Tasks[0].Recaps)
	}
	if w := st.Project.Wakes; len(w) != 1 || !strings.Contains(w[0].Facts, "changed lines timed out, size cap 100") {
		t.Fatalf("wakes = %+v, want timeout facts", w)
	}
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 1 || !strings.Contains(sends[0], "timed out reading changed lines") {
		t.Fatalf("worker sends = %q, want timeout stop message", sends)
	}

	// Once git diff recovers, continue + next pass must read again and open no second recap.
	slow.Store(false)
	before := calls.Load()
	l.report("tok-coord", "recap decide", `{"task":"T1","choice":"continue","why":"transient stall"}`, "")
	l.pass()
	if got := calls.Load() - before; got != 2 {
		t.Errorf("continue and next pass ran git diff %d times, want 2", got)
	}
	if rc := l.state().Tasks[0].Recaps; len(rc) != 1 || flow.OpenRecap(l.state().Tasks[0]) != nil {
		t.Fatalf("recaps after continue + normal pass = %+v, want no second recap", rc)
	}
}

// TestContinueWhenLineReadFails: recap decide continue raises the cap from the
// last good reading (or the current cap) plus continue_lines when ChangedLines
// fails, says so in the decision text, and never returns a raw error.
func TestContinueWhenLineReadFails(t *testing.T) {
	l1 := newLanding(t).withTokens()
	l2 := newLanding(t).withTokens()
	_, fail, _ := fakeDiffExec(t, l1.eng, l2.eng)

	// 1. With a last good reading (1 line > SizeCap 0): raises from last good reading.
	l1.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].SizeCap = 0 })
	l1.pass() // caches good reading (1 line) and opens size recap
	fail.Store(true)
	if err := l1.eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T1","choice":"continue","why":"give room"}`)); err != nil {
		t.Fatalf("continue with failed line read returned error: %v", err)
	}
	st1 := l1.state()
	if a := st1.Tasks[0].Attempts[0]; a.SizeCap != 1+st1.Settings.ContinueLines || flow.OpenRecap(st1.Tasks[0]) != nil {
		t.Fatalf("after continue with last good reading: size_cap %d, open recap %+v", a.SizeCap, flow.OpenRecap(st1.Tasks[0]))
	}
	if last := l1.logLines()[len(l1.logLines())-1]; !strings.Contains(last.Decision, "changed-lines read failed") || !strings.Contains(last.Decision, "last good reading") {
		t.Fatalf("continue decision (last good) = %q", last.Decision)
	}

	// 2. With no prior good reading: raises from current cap (100).
	l2.edit(func(s *flow.State) {
		s.Tasks[0].Recaps = []flow.Recap{{Trigger: flow.TriggerSize, Allowed: []string{"continue", "split", "new", "drop"}}}
	})
	if err := l2.eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T1","choice":"continue","why":"give room"}`)); err != nil {
		t.Fatalf("continue with failed line read (no prior reading) returned error: %v", err)
	}
	st2 := l2.state()
	if a := st2.Tasks[0].Attempts[0]; a.SizeCap != 100+st2.Settings.ContinueLines || flow.OpenRecap(st2.Tasks[0]) != nil {
		t.Fatalf("after continue with no prior reading: size_cap %d, open recap %+v", a.SizeCap, flow.OpenRecap(st2.Tasks[0]))
	}
	if last := l2.logLines()[len(l2.logLines())-1]; !strings.Contains(last.Decision, "changed-lines read failed") || !strings.Contains(last.Decision, "current cap") {
		t.Fatalf("continue decision (current cap) = %q", last.Decision)
	}
}

// TestContinueUnderGitTimeout: a slow git diff during recap decide continue
// does not starve SendRun of context, so the worker still gets the continue
// message; if SendRun itself fails, continue is not committed.
func TestContinueUnderGitTimeout(t *testing.T) {
	l := newLanding(t).withTokens()
	writePRJSON(t, l.gh, "view.json", l.c1, "[]")
	slow, _, calls := fakeDiffExec(t, l.eng)
	slow.Store(true)
	l.pass() // N12: timeout opens size recap
	l.pass() // N6: wakes coordinator, tells worker to stop
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 1 || !strings.Contains(sends[0], "Stop now") {
		t.Fatalf("worker sends before continue = %q, want one stop", sends)
	}

	// Keep slow set during continue: ChangedLines times out, SendRun must still succeed.
	l.finishWorkerTurn()
	before := calls.Load()
	l.report("tok-coord", "recap decide", `{"task":"T1","choice":"continue","why":"git is slow"}`, "")
	if got := calls.Load() - before; got != 1 {
		t.Errorf("continue ran git diff %d times, want 1", got)
	}
	sends := sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends) != 2 || !strings.Contains(sends[1], "lets task T1 go on") || !strings.Contains(sends[1], "size cap is now 160") {
		t.Fatalf("worker sends after continue under git timeout = %q, want continue message", sends)
	}
	st := l.state()
	if a := st.Tasks[0].Attempts[0]; a.SizeCap != 160 || flow.OpenRecap(st.Tasks[0]) != nil {
		t.Fatalf("after continue under git timeout: size_cap %d, open recap %+v", a.SizeCap, flow.OpenRecap(st.Tasks[0]))
	}

	// If SendRun fails, continue returns an error and does not commit the decision.
	slow.Store(false)
	lFail := newLanding(t).withTokens()
	lFail.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].RunID = "missing-worker"
		s.Tasks[0].Recaps = []flow.Recap{{Trigger: flow.TriggerSize, Allowed: []string{"continue", "split", "new", "drop"}}}
	})
	if err := lFail.eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T1","choice":"continue","why":"worker unreachable"}`)); err == nil {
		t.Fatal("continue with unreachable worker succeeded, want error")
	}
	stFail := lFail.state()
	if a := stFail.Tasks[0].Attempts[0]; a.SizeCap != 100 || flow.OpenRecap(stFail.Tasks[0]) == nil {
		t.Fatalf("after failed send: size_cap %d, open recap %+v; want cap 100 and recap still open", a.SizeCap, flow.OpenRecap(stFail.Tasks[0]))
	}
}

// TestWorkerPromptShowsNotDoing checks prompts.md 5.3: the worker prompt
// carries the task's not_doing fence.
func TestWorkerPromptShowsNotDoing(t *testing.T) {
	l := newLanding(t).withTokens()
	var prompt string
	l.srv.OnRun = func(_ hc.Terminal, p hc.RunParams) {
		if strings.Contains(p.Name, "-t2-") && len(p.Argv) > 0 {
			prompt = p.Argv[len(p.Argv)-1]
		}
	}
	l.report("tok-coord", "plan amend", `{"why":"add T2","tasks":[{"id":"T2","title":"feat: two","brief":"Do two.","estimate_lines":30,"for_whom":"u","if_skipped":"x","smallest":"s","not_doing":"caching or retries",`+testOutcomesJSON+`}]}`, "")
	l.pass() // N11 for T2
	l.pass() // N23 starts worker for T2
	if !strings.Contains(prompt, "Not doing:\ncaching or retries") {
		t.Fatalf("worker prompt missing not_doing:\n%s", prompt)
	}
}

// TestWorkerAndReviewerPromptsShowOutcomesWithProofs checks PR 3.2: both the
// worker prompt and the reviewer prompt list each outcome with its proof, one
// per line, and the coordinator template shows the outcome shape.
func TestWorkerAndReviewerPromptsShowOutcomesWithProofs(t *testing.T) {
	l := newLanding(t).withTokens()
	var workerPrompt, reviewerPrompt string
	l.srv.OnRun = func(_ hc.Terminal, p hc.RunParams) {
		if len(p.Argv) == 0 {
			return
		}
		switch {
		case strings.Contains(p.Name, "worker-") && strings.Contains(p.Name, "-t2-"):
			workerPrompt = p.Argv[len(p.Argv)-1]
		case strings.Contains(p.Name, "rev-") && strings.Contains(p.Name, "-t2-"):
			reviewerPrompt = p.Argv[len(p.Argv)-1]
		}
	}
	l.report("tok-coord", "plan amend", `{"why":"add T2","tasks":[{"id":"T2","title":"feat: two","brief":"Do two.","estimate_lines":30,`+testScopeJSON+`,"outcomes":[`+
		`{"text":"returns greeting","proved_by":"test: TestGreet"},`+
		`{"text":"builds cleanly","proved_by":"cmd: go build ./..."},`+
		`{"text":"button aligns","proved_by":"manual: screenshot of home screen"}`+
		`]}]}`, "")
	l.pass() // N11 creates worktree for T2
	l.pass() // N23 starts worker for T2

	st := l.state()
	wt := st.Tasks[1].Attempts[0].Worktree
	if err := os.WriteFile(filepath.Join(wt, "two.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt, "add", "two.txt")
	gitIn(t, wt, "commit", "-m", "feat: two")
	c := strings.TrimSpace(gitIn(t, wt, "rev-parse", "HEAD"))
	l.edit(func(s *flow.State) {
		s.Tasks[1].Attempts[0].RunTokenHash = tokenHash("tok-worker-t2")
	})
	art := filepath.Join(t.TempDir(), "home.png")
	if err := os.WriteFile(art, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	l.report("tok-worker-t2", "ready", `{"head":"`+c+`","proofs":[`+
		`{"outcome":"returns greeting","command":"go test -run TestGreet","exit":0,"output_path":"/tmp/p1.log"},`+
		`{"outcome":"builds cleanly","command":"go build ./...","exit":0,"output_path":"/tmp/p2.log"},`+
		`{"outcome":"button aligns","artifact":`+strconvQuote(art)+`}`+
		`],"touched":"two.txt","skipped":""}`, "")
	writePRJSON(t, l.gh, "view.json", c, greenRollup)
	l.pass() // N29 starts reviewer for T2

	wantBlock := "Outcomes:\n" +
		"- returns greeting (proved by: test: TestGreet)\n" +
		"- builds cleanly (proved by: cmd: go build ./...)\n" +
		"- button aligns (proved by: manual: screenshot of home screen)"
	if !strings.Contains(workerPrompt, wantBlock) {
		t.Fatalf("worker prompt missing outcomes with proofs:\n%s", workerPrompt)
	}
	wantReady := `factory report ready --proof 'returns greeting=<command>' --proof 'builds cleanly=go build ./...' --artifact 'button aligns=<path>' --touched "<places>" --skipped "<what was left out or not checked>"`
	if !strings.Contains(workerPrompt, wantReady) {
		t.Fatalf("worker prompt missing ready --proof form:\n%s", workerPrompt)
	}
	if !strings.Contains(reviewerPrompt, wantBlock) {
		t.Fatalf("reviewer prompt missing outcomes with proofs:\n%s", reviewerPrompt)
	}
	for _, want := range []string{`"proved_by"`, "cmd:", "test:", "manual:"} {
		if !strings.Contains(coordinatorTemplate, want) {
			t.Errorf("coordinator template missing %q:\n%s", want, coordinatorTemplate)
		}
	}
}
