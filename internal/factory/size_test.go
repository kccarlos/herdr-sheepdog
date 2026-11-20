package factory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// TestPlanEstimateRequiredAndLimited: estimate_lines is required and a task
// over task_size_limit is refused in plan apply, plan amend and split
// children (design.md 11.5); an amended estimate changes a started task's
// size_cap, and a new attempt starts at size_cap_factor × estimate_lines.
func TestPlanEstimateRequiredAndLimited(t *testing.T) {
	_, fails := checkPlan(json.RawMessage(`{"tasks":[{"id":"A","title":"a"},{"id":"B","title":"b","estimate_lines":401}]}`), 400)
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
	l.report("tok-coord", "plan amend", `{"why":"bigger than thought","tasks":[{"id":"T1","estimate_lines":30},{"id":"T2","title":"feat: two","estimate_lines":40,`+testScopeJSON+`}]}`, "")
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

// passReport makes rev's reviewer idle with a valid pass tag for round at head.
func (l *landing) passReport(rev *flow.ReviewRun) {
	l.srv.SetScreen(rev.RunID, "❯ prompt\n\n⏺ Fine.\n<report>{\"code\":\""+rev.Code+"\",\"round\":"+strconv.Itoa(rev.Round)+",\"head\":\""+rev.Head+"\",\"verdict\":\"pass\",\"majors\":0}</report>\n")
	l.srv.SetStatus(rev.RunID, &hc.Status{App: "claude-code", State: "done"})
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
	if rs, _ := world.RunState(ctx, l.client, rev.RunID); flow.H(a) != m1 || a.Review == nil || a.Review.RunID != rev.RunID || rs == flow.RunGone {
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
	l.pass() // N12 opens the size recap
	if flow.OpenRecap(l.state().Tasks[0]) == nil {
		t.Fatal("no size recap opened")
	}
	l.passReport(rev)
	l.pass()
	a := l.state().Tasks[0].Attempts[0]
	if rs, _ := world.RunState(ctx, l.client, rev.RunID); rs != flow.RunGone || a.Review != nil || len(a.Rounds) != 0 {
		t.Fatalf("with a recap open: reviewer %s, review %+v, rounds %+v", rs, a.Review, a.Rounds)
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
	l.report("tok-coord", "plan amend", `{"why":"add T2","tasks":[{"id":"T2","title":"feat: two","estimate_lines":30,`+testScopeJSON+`}]}`, "")
	l.report("tok-coord", "plan amend", `{"why":"raise unstarted","tasks":[{"id":"T2","estimate_lines":90}]}`, "")
	l.pass() // N11 starts T2
	if a := l.state().Tasks[1].Attempts; len(a) != 1 || a[0].SizeCap != 180 {
		t.Fatalf("unstarted T2 size_cap = %+v, want 180", a)
	}
}

// TestChangedLinesTimeoutOpensSizeRecap: a changed-lines read that times out
// counts as over size_cap (N12 opens a size recap) instead of skipping the
// pass, and is cached per (tip, H); a non-timeout error still skips the pass.
func TestChangedLinesTimeoutOpensSizeRecap(t *testing.T) {
	orig := changedLines
	t.Cleanup(func() { changedLines = orig })

	// Non-timeout error skips the pass and opens no recap.
	lErr := newLanding(t)
	changedLines = func(context.Context, string, string) (int, error) {
		return 0, errors.New("git broken")
	}
	lErr.pass()
	if rc := lErr.state().Tasks[0].Recaps; len(rc) != 0 {
		t.Fatalf("non-timeout error opened recap: %+v", rc)
	}

	// Timeout opens a size recap via N12, then N6 wakes the coordinator and tells the worker.
	l := newLanding(t)
	calls := 0
	changedLines = func(context.Context, string, string) (int, error) {
		calls++
		return 0, context.DeadlineExceeded
	}
	l.pass() // N12
	l.pass() // N6: uses cached timeout reading, wakes coordinator, tells worker
	if calls != 1 {
		t.Fatalf("changedLines called %d times for same (tip, H), want 1", calls)
	}
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
	l.report("tok-coord", "plan amend", `{"why":"add T2","tasks":[{"id":"T2","title":"feat: two","brief":"Do two.","estimate_lines":30,"for_whom":"u","if_skipped":"x","smallest":"s","not_doing":"caching or retries"}]}`, "")
	l.pass() // N11 for T2
	l.pass() // N23 starts worker for T2
	if !strings.Contains(prompt, "Not doing:\ncaching or retries") {
		t.Fatalf("worker prompt missing not_doing:\n%s", prompt)
	}
}
