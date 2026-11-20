package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
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
	l.report("tok-coord", "plan amend", `{"why":"bigger than thought","tasks":[{"id":"T1","estimate_lines":30},{"id":"T2","title":"feat: two","estimate_lines":40}]}`, "")
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
