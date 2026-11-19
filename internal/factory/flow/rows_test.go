package flow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

// landing is a task that N42 merges: passed twice at c1, green, clean, open.
func landing() In {
	return In{
		Task: Task{ID: "t", Attempts: []Attempt{{
			BaseCommit: "b0",
			Ready:      []Ready{{Head: "c1"}},
			Rounds: []Round{
				{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictPass},
				{N: 2, Kind: RoundFull, Head: "c1", Verdict: VerdictPass},
			},
		}}},
		Settings: DefaultSettings(),
		Facts: Facts{
			Tip: "c1", TipRel: RelSame, Worker: Run{State: RunIdle},
			PR: PR{Answered: true, Number: 1, State: PROpen, Head: "c1", HeadRel: RelSame,
				HeadSince: now, Mergeable: MergeableClean, Checks: ChecksGreen},
		},
		Now: now,
	}
}

// with returns landing() changed by each f.
func with(fs ...func(*In)) In {
	in := landing()
	for _, f := range fs {
		f(&in)
	}
	return in
}

func a0(in *In) *Attempt { return &in.Task.Attempts[0] }

var (
	done        = func(in *In) { in.Task.Done = &Merged{MergedCommit: "m1"} }
	merged      = func(in *In) { in.Facts.PR.State = PRMerged }
	workerEnded = func(in *In) { in.Facts.Worker.State = RunGone }
	reviewEnded = func(in *In) { in.Facts.Reviewer.State = RunGone }
	noAttempts  = func(in *In) { in.Task.Attempts = nil; in.Facts = Facts{} }
	onePass     = func(in *In) { a0(in).Rounds = a0(in).Rounds[:1] }
	changes     = func(in *In) { a0(in).Rounds = []Round{{N: 1, Head: "c1", Verdict: VerdictChanges}} }
	noRounds    = func(in *In) { a0(in).Rounds = nil }
	notReady    = func(in *In) { a0(in).Ready = nil; in.Facts.Worker = Run{} }
	tipAheadF   = func(in *In) { in.Facts.Tip, in.Facts.TipRel = "c2", RelAhead }
	prGone      = func(in *In) { in.Facts.PR = PR{Answered: true} }
	prBehind    = func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "b0", RelBehind }
	pending     = func(in *In) { in.Facts.PR.Checks = ChecksPending }
	workingF    = func(in *In) { in.Facts.Worker.State = RunWorking }
	asked       = func(in *In) { a0(in).Acts = append(a0(in).Acts, Act{Key: "merge@c1", Result: "done"}) }
	red         = func(in *In) { in.Facts.PR.Checks = ChecksRed }
	noAnswer    = func(in *In) { in.Facts.PR.Answered = false }
	closedPR    = func(in *In) { in.Facts.PR.State = PRClosed }
	conflict    = func(in *In) { in.Facts.PR.Mergeable = MergeableConflict }
	// stuckAct: an act failed past stuck_minutes; idleRev: the round's
	// reviewer read idle with no tag past stuck_minutes.
	stuckAct = func(k string) func(*In) {
		return func(in *In) {
			a0(in).Acts = append(a0(in).Acts, Act{Key: k, Result: "failed", At: now.Add(-stuckFor(in))})
		}
	}
	idleRev = func(in *In) {
		t := now.Add(-stuckFor(in))
		a0(in).Review = &ReviewRun{Round: 1, Head: "c1", IdleSince: &t}
		in.Facts.Reviewer.State = RunIdle
	}
)

func stuckFor(in *In) time.Duration { return time.Duration(in.Settings.StuckMinutes) * time.Minute }

func rowByID(id string) Row[In] {
	return TaskRows[slices.IndexFunc(TaskRows, func(r Row[In]) bool { return r.ID == id })]
}

// TestN42Guards: each N42 clause false and the rest true. N42's condition
// fails (called directly, so a clause an earlier row shadows is tested
// too), and no row asks for a merge.
func TestN42Guards(t *testing.T) {
	n42 := rowByID("N42")
	for name, f := range map[string]func(*In){
		"prOpen":             closedPR,
		"prHeadIsH":          func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "c9", RelAhead },
		"answered":           noAnswer,
		"mergeClean":         conflict,
		"notBehind":          func(in *In) { in.Facts.PR.Behind = true },
		"notWorking":         workingF,
		"onePassNeedsL8":     onePass,
		"ladderSays(Passed)": func(in *In) { a0(in).Rounds[1].Verdict = "maybe" },
		"checksGreen":        red,
		"tipSame":            func(in *In) { in.Facts.Tip, in.Facts.TipRel = "c9", RelDiverged },
	} {
		in := with(f)
		if n42.When(in) {
			t.Errorf("%s false: N42 holds", name)
		}
		r, d := FirstMatch(TaskRows, in)
		if r.ID == "N42" || slices.ContainsFunc(d.Asked, func(k string) bool { return strings.HasPrefix(k, "merge@") }) {
			t.Errorf("%s false: %s asked %v", name, r.ID, d.Asked)
		}
	}
}

// TestS1FinishedReviewer: the reviewer is stopped after its verdict, so
// once the ladder no longer says review, its gone run stops nothing.
func TestS1FinishedReviewer(t *testing.T) {
	for want, in := range map[string]In{
		"N22": with(changes, reviewEnded),
		"N36": with(pending, reviewEnded),
		"N42": with(reviewEnded),
	} {
		if r, d := FirstMatch(TaskRows, in); r.ID != want {
			t.Errorf("reviewer gone: %s %v, want %s", r.ID, d.Asked, want)
		}
	}
}

// TestN43WakeOncePerStall: the same stall wakes the coordinator once; a
// different stall, or the same at a new H, wakes it again.
func TestN43WakeOncePerStall(t *testing.T) {
	_, first := FirstMatch(TaskRows, with(red))
	stalled := func(fs ...func(*In)) In {
		in := with(append([]func(*In){red}, fs...)...)
		a0(&in).Acts = []Act{{Key: first.Asked[0], Result: "done"}}
		return in
	}
	newH := func(in *In) {
		a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"})
		a0(in).Rounds = append(a0(in).Rounds,
			Round{N: 3, Head: "c2", Verdict: VerdictPass},
			Round{N: 4, Head: "c2", Verdict: VerdictPass},
		)
		in.Facts.PR.Head = "c2"
	}
	for name, c := range map[string]struct {
		in   In
		wake bool
	}{
		"same stall": {stalled(), false},
		"conflict":   {stalled(conflict), true},
		"new H":      {stalled(newH), true},
	} {
		r, d := FirstMatch(TaskRows, c.in)
		if r.ID != "N43" || (len(d.Asked) == 1) != c.wake {
			t.Errorf("%s: %s asked %v, want wake=%v", name, r.ID, d.Asked, c.wake)
		}
	}
}

// TestClosedPR: a closed PR waits on nothing and gets no worker or
// reviewer work; it falls to N43 (design.md N4 comes in a later slice).
func TestClosedPR(t *testing.T) {
	for name, in := range map[string]In{
		"pending": with(closedPR, pending),
		"unknown": with(closedPR, func(in *In) { in.Facts.PR.Mergeable = MergeableUnknown }),
		"working": with(closedPR, workingF),
		"fix":     with(closedPR, changes),
		"review":  with(closedPR, noRounds),
	} {
		if r, d := FirstMatch(TaskRows, in); r.ID != "N43" {
			t.Errorf("closed PR, %s: %s asked %v, want N43", name, r.ID, d.Asked)
		}
	}
}

func TestTablesCheck(t *testing.T) {
	if err := CheckTable(TaskRows); err != nil {
		t.Error(err)
	}
	if err := CheckTable(LadderRows); err != nil {
		t.Error(err)
	}
}

// TestRowCases: each row has a case that lands on it, so on no earlier row.
func TestRowCases(t *testing.T) {
	cases := []struct {
		row   string
		in    In
		asked []string
	}{
		{"S2", with(done, stuckAct("cleanup")), []string{"wake_coordinator@stuck@cleanup@c1"}},
		{"S2", with(noRounds, idleRev), []string{"wake_coordinator@stuck@reviewer_idle@1@c1"}},
		{"N1", with(done), []string{"cleanup"}},
		{"N2", with(merged), []string{"mark_done", "wake_coordinator@merged_outside_bar"}},
		{"S1", with(workerEnded), []string{"stop_task"}},
		{"S1", with(noRounds, reviewEnded), []string{"stop_task"}},
		{"N11", with(noAttempts), []string{"new_attempt"}},
		{"N22", with(changes), []string{"tell_worker_findings@1"}},
		{"N23", with(notReady, noRounds, prGone), []string{"start_worker"}},
		{"N26", with(noRounds, prGone), []string{"push_and_open_pr@c1"}},
		{"N26", with(noRounds, prGone, noAnswer), nil},                    // "no PR" from a failed read: wait, no push
		{"N26", with(noRounds, prBehind, noAnswer), nil},                  // a stale behind PR, unanswered: wait
		{"N26", with(noRounds, func(in *In) { in.Facts.PR = PR{} }), nil}, // no snapshot yet
		{"N29", with(noRounds), []string{"start_reviewer@1@c1"}},
		{"N29", with(onePass), []string{"start_reviewer@2@c1"}},
		{"N36", with(pending), nil},
		{"N36", with(noAnswer), nil}, // checks unreadable: wait, no wake
		{"N39", with(workingF), nil},
		{"N41", with(asked), nil},
		{"N42", landing(), []string{"record_accept@c1", "merge@c1"}},
		{"N43", with(red), []string{"wake_coordinator@no_rule_matched@c1@open@red@clean@same"}},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		r, d := FirstMatch(TaskRows, c.in)
		if r.ID != c.row || !slices.Equal(d.Asked, c.asked) {
			t.Errorf("want %s %v, got %s %v (%s)", c.row, c.asked, r.ID, d.Asked, d.Why)
		}
		covered[c.row] = true
	}
	for _, r := range TaskRows {
		if !covered[r.ID] {
			t.Errorf("row %s has no case", r.ID)
		}
	}
}

// TestRowOrder: where two rows both hold, which wins. For neighbours the
// pair is also swapped, and the other must win (design.md 16).
func TestRowOrder(t *testing.T) {
	cases := []struct {
		win, lose  string
		neighbours bool
		in         In
	}{
		// S2 is first: nothing must win before it (plan.md PR 2.1).
		{"S2", "N1", true, with(done, stuckAct("cleanup"))},
		{"S2", "N29", false, with(noRounds, idleRev)},
		{"S2", "N41", false, with(stuckAct("merge@c1"))},
		{"N1", "N2", true, with(done, merged)},
		{"N2", "S1", true, with(merged, workerEnded)},
		{"S1", "N22", false, with(workerEnded, changes)},
		{"N22", "N23", true, with(changes, tipAheadF)},
		{"N22", "N26", false, with(changes, prBehind)},
		{"N23", "N26", true, with(noRounds, tipAheadF, prBehind)},
		{"N23", "N36", false, with(tipAheadF, pending)},
		{"N26", "N29", true, with(noRounds, prGone)},
		{"N36", "N39", true, with(pending, workingF)},
		{"N39", "N41", true, with(workingF, asked)},
		{"N41", "N42", true, with(asked)},
	}
	for _, c := range cases {
		if r, _ := FirstMatch(TaskRows, c.in); r.ID != c.win {
			t.Errorf("%s vs %s: got %s", c.win, c.lose, r.ID)
		}
		if !c.neighbours {
			continue
		}
		rows := slices.Clone(TaskRows)
		i := slices.IndexFunc(rows, func(r Row[In]) bool { return r.ID == c.win })
		if rows[i+1].ID != c.lose {
			t.Fatalf("%s and %s are not neighbours", c.win, c.lose)
		}
		rows[i], rows[i+1] = rows[i+1], rows[i]
		if r, _ := FirstMatch(rows, c.in); r.ID != c.lose {
			t.Errorf("swapped %s/%s: got %s", c.win, c.lose, r.ID)
		}
	}
}

func TestN2MergedThroughBar(t *testing.T) {
	in := with(merged)
	a0(&in).Accepts = []Accept{{Head: "c1", By: "bar"}}
	if _, d := FirstMatch(TaskRows, in); !slices.Equal(d.Asked, []string{"mark_done"}) {
		t.Errorf("asked %v", d.Asked)
	}
}

func TestChecksGrace(t *testing.T) {
	grace := time.Duration(DefaultSettings().ChecksGraceMinutes) * time.Minute
	for _, c := range []struct {
		age  time.Duration
		want string
	}{{0, "N36"}, {grace - time.Second, "N36"}, {grace, "N43"}} {
		in := with(func(in *In) { in.Facts.PR.Checks = ChecksNone; in.Now = now.Add(c.age) })
		if r, _ := FirstMatch(TaskRows, in); r.ID != c.want {
			t.Errorf("no checks after %v: %s, want %s", c.age, r.ID, c.want)
		}
	}
}

func TestEnsureRun(t *testing.T) {
	in := with(noRounds)
	for _, c := range []struct {
		run  Run
		acts []Act
		want []string
	}{
		{Run{Pending: true}, nil, nil},
		{Run{}, nil, []string{"go"}},
		{Run{}, []Act{{Key: "go", Result: "done"}}, nil},
		{Run{State: RunIdle}, nil, nil},
	} {
		a0(&in).Acts = c.acts
		if got := ensureRun(in, c.run, "go"); !slices.Equal(got, c.want) {
			t.Errorf("%+v %v: %v, want %v", c.run, c.acts, got, c.want)
		}
	}
}

// TestLadder: each ladder row has a case that lands on it and on no earlier
// row, including L8 (fewer than review_rounds passes) and review_rounds 1
// passing after a single round (design.md 4.6, D12).
func TestLadder(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range []struct {
		row, answer string
		in          In
	}{
		{"L7", Review, with(noRounds)},
		{"L7", Review, with(func(in *In) { a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"}) })},
		{"L8", Review, with(onePass)},
		{"L9", Passed, landing()},
		{"L9", Passed, with(onePass, func(in *In) { in.Settings.ReviewRounds = 1 })},
		{"L13", Fix, with(changes)},
		{"L13", Fix, with(func(in *In) { a0(in).Rounds[1].Verdict = VerdictChanges })},
		{"L-", NoLadderRow, with(func(in *In) { a0(in).Rounds[1].Verdict = "maybe" })},
	} {
		r, d := FirstMatch(LadderRows, c.in)
		if r.ID != c.row || d.Why != c.answer || Ladder(c.in) != c.answer {
			t.Errorf("want %s %s, got %s %s", c.row, c.answer, r.ID, d.Why)
		}
		covered[r.ID] = true
	}
	for _, r := range LadderRows {
		if !covered[r.ID] {
			t.Errorf("ladder row %s has no case", r.ID)
		}
	}
}

// TestLadderOrder checks L7/L8/L9 ordering and neighbour swaps (design.md 16):
// L7 and L8 are disjoint neighbours (swapping them never lets L8 match an
// unreviewed head), while L8 and L9 both hold on one pass and swapping them
// flips Review to Passed.
func TestLadderOrder(t *testing.T) {
	if LadderRows[0].ID != "L7" || LadderRows[1].ID != "L8" || LadderRows[2].ID != "L9" {
		t.Fatalf("ladder prefix = %s/%s/%s, want L7/L8/L9", LadderRows[0].ID, LadderRows[1].ID, LadderRows[2].ID)
	}
	// Neighbours L7 and L8: disjoint on both noRounds and onePass, so swapping keeps each winner.
	swap78 := slices.Clone(LadderRows)
	swap78[0], swap78[1] = swap78[1], swap78[0]
	if r, _ := FirstMatch(swap78, with(noRounds)); r.ID != "L7" {
		t.Fatalf("swapped L7/L8 on noRounds matched %s, want L7", r.ID)
	}
	if r, _ := FirstMatch(swap78, with(onePass)); r.ID != "L8" {
		t.Fatalf("swapped L7/L8 on onePass matched %s, want L8", r.ID)
	}
	// Neighbours L8 and L9: both hold on onePass; L8 wins, and swapping makes L9 win.
	in := with(onePass)
	if !LadderRows[1].When(in) || !LadderRows[2].When(in) {
		t.Fatal("expected both L8 and L9 conditions to hold on onePass")
	}
	if r, d := FirstMatch(LadderRows, in); r.ID != "L8" || d.Why != Review {
		t.Fatalf("L8 vs L9: got %s (%s), want L8 (%s)", r.ID, d.Why, Review)
	}
	swap89 := slices.Clone(LadderRows)
	swap89[1], swap89[2] = swap89[2], swap89[1]
	if r, d := FirstMatch(swap89, in); r.ID != "L9" || d.Why != Passed {
		t.Fatalf("swapped L8/L9: got %s (%s), want L9 (%s)", r.ID, d.Why, Passed)
	}
}

// TestLadderCountsPerHeadAndResetsAfterChanges checks design.md 4.6 and D12:
// L8 asks review until review_rounds passing rounds cover H (tested up to
// ReviewRoundsMax), and a changes verdict followed by a new commit resets the
// count at the new head so earlier passes at the old head do not cover it.
func TestLadderCountsPerHeadAndResetsAfterChanges(t *testing.T) {
	for target := ReviewRoundsMin; target <= ReviewRoundsMax; target++ {
		in := with(noRounds, func(in *In) { in.Settings.ReviewRounds = target })
		for n := 1; n <= target; n++ {
			if got := Ladder(in); got != Review {
				t.Fatalf("target=%d before round %d: got %s, want %s", target, n, got, Review)
			}
			a0(&in).Rounds = append(a0(&in).Rounds, Round{N: n, Head: "c1", Verdict: VerdictPass})
		}
		if got := Ladder(in); got != Passed {
			t.Fatalf("target=%d after %d passes: got %s, want %s", target, target, got, Passed)
		}
	}

	// Pass at c1 -> changes at c1 (Fix) -> new commit c2 resets passing count to 0 at c2.
	in := with(onePass)
	if r, _ := FirstMatch(LadderRows, in); r.ID != "L8" {
		t.Fatalf("after 1 pass at c1: %s, want L8", r.ID)
	}
	a0(&in).Rounds = append(a0(&in).Rounds, Round{N: 2, Head: "c1", Verdict: VerdictChanges, Majors: 1})
	if r, _ := FirstMatch(LadderRows, in); r.ID != "L13" || Ladder(in) != Fix {
		t.Fatalf("after round 2 changes at c1: %s (%s), want L13 (fix)", r.ID, Ladder(in))
	}
	a0(&in).Ready = append(a0(&in).Ready, Ready{Head: "c2"})
	if r, _ := FirstMatch(LadderRows, in); r.ID != "L7" || Ladder(in) != Review {
		t.Fatalf("at new head c2: %s (%s), want L7 (review)", r.ID, Ladder(in))
	}
	a0(&in).Rounds = append(a0(&in).Rounds, Round{N: 3, Kind: RoundDelta, Head: "c2", Verdict: VerdictPass})
	if r, _ := FirstMatch(LadderRows, in); r.ID != "L8" || Ladder(in) != Review {
		t.Fatalf("after 1 pass at c2 (despite earlier pass at c1): %s (%s), want L8 (review)", r.ID, Ladder(in))
	}
	a0(&in).Rounds = append(a0(&in).Rounds, Round{N: 4, Kind: RoundFull, Head: "c2", Verdict: VerdictPass})
	if r, _ := FirstMatch(LadderRows, in); r.ID != "L9" || Ladder(in) != Passed {
		t.Fatalf("after 2 passes at c2: %s (%s), want L9 (passed)", r.ID, Ladder(in))
	}
}

func TestRoundKind(t *testing.T) {
	var a Attempt
	if k, s := RoundKind(a, "c1", ""); k != RoundFull || s != "" {
		t.Fatalf("first round = (%s, %s), want (full, \"\")", k, s)
	}
	a.Rounds = []Round{{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictChanges}}
	if k, s := RoundKind(a, "c2", RelAhead); k != RoundDelta || s != "c1" {
		t.Fatalf("after worker commit c2 = (%s, %s), want (delta, c1)", k, s)
	}
	for _, rel := range []Rel{RelDiverged, RelBehind, RelSame, ""} {
		if k, s := RoundKind(a, "c2", rel); k != RoundFull || s != "" {
			t.Fatalf("RoundKind with rel=%q = (%s, %s), want (full, \"\")", rel, k, s)
		}
	}
	a.Rounds = []Round{{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictPass}}
	if k, s := RoundKind(a, "c1", RelSame); k != RoundFull || s != "" {
		t.Fatalf("L8 second pass at c1 = (%s, %s), want (full, \"\")", k, s)
	}
	a.Rounds = []Round{
		{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictChanges},
		{N: 2, Kind: RoundDelta, Head: "c2", Verdict: VerdictPass},
	}
	if k, s := RoundKind(a, "c2", RelSame); k != RoundFull || s != "" {
		t.Fatalf("L8 second pass at c2 after delta pass = (%s, %s), want (full, \"\")", k, s)
	}
	a.Rounds = append(a.Rounds, Round{N: 3, Kind: RoundFull, Head: "c2", Verdict: VerdictChanges})
	if k, s := RoundKind(a, "c3", RelAhead); k != RoundDelta || s != "c2" {
		t.Fatalf("after fix c3 = (%s, %s), want (delta, c2)", k, s)
	}
}

// TestNoNumberInRules: rules take numbers from Settings or named constants
// (design.md 2.3). 0 as "empty" is allowed.
func TestNoNumberInRules(t *testing.T) {
	fset := token.NewFileSet()
	for _, name := range []string{"rows.go", "ladder.go"} {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
				continue
			}
			ast.Inspect(d, func(n ast.Node) bool {
				if l, ok := n.(*ast.BasicLit); ok && (l.Kind == token.INT || l.Kind == token.FLOAT) && l.Value != "0" {
					t.Errorf("%s: numeric literal %s in a rule", fset.Position(l.Pos()), l.Value)
				}
				return true
			})
		}
	}
}

// TestS2StuckWakesOnce: a stall wakes once its clock reaches stuck_minutes;
// once woken, the rows below go on; a new H or another stall wakes again.
func TestS2StuckWakesOnce(t *testing.T) {
	woken := func(in *In) {
		_, d := FirstMatch(TaskRows, *in)
		for _, k := range d.Asked {
			a0(in).Acts = append(a0(in).Acts, Act{Key: k, Result: "done", At: now})
		}
	}
	young := func(in *In) { in.Now = now.Add(-time.Second) }
	newH := func(in *In) {
		a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"})
		a0(in).Review.Head = "c2"
	}
	for name, c := range map[string]struct {
		in   In
		row  string
		wake []string
	}{
		"idle, young":         {with(noRounds, idleRev, young), "N29", nil},
		"failed act, young":   {with(stuckAct("merge@c1"), young), "N41", nil},
		"idle, woken":         {with(noRounds, idleRev, woken), "N29", nil},
		"idle, woken, new H":  {with(noRounds, idleRev, woken, newH), "S2", []string{"wake_coordinator@stuck@reviewer_idle@1@c2"}},
		"woken, another act":  {with(noRounds, idleRev, woken, stuckAct("push_and_open_pr@c1")), "S2", []string{"wake_coordinator@stuck@push_and_open_pr@c1@c1"}},
		"done act never wake": {with(func(in *In) { a0(in).Acts = []Act{{Key: "merge@c1", Result: "done", At: now.Add(-time.Hour)}} }, asked), "N41", nil},
	} {
		r, d := FirstMatch(TaskRows, c.in)
		if r.ID != c.row || !slices.Equal(d.Asked, c.wake) {
			t.Errorf("%s: %s %v, want %s %v", name, r.ID, d.Asked, c.row, c.wake)
		}
	}
}

// TestS2StaleFailedActNeverWakes: a round voided at c1 (its start act
// failed) never wakes once H has moved to c2, neither at once nor after
// stuck_minutes; a failed act for the current H still does.
func TestS2StaleFailedActNeverWakes(t *testing.T) {
	moved := func(in *In) {
		a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"})
		in.Facts.Tip, in.Facts.PR.Head = "c2", "c2"
	}
	later := func(d time.Duration) func(*In) { return func(in *In) { in.Now = now.Add(d) } }
	for name, in := range map[string]In{
		"at once":             with(noRounds, stuckAct("start_reviewer@1@c1"), moved),
		"after stuck_minutes": with(noRounds, stuckAct("start_reviewer@1@c1"), moved, later(time.Duration(StuckMax)*time.Minute)),
	} {
		if r, d := FirstMatch(TaskRows, in); r.ID == "S2" || slices.ContainsFunc(d.Asked, func(k string) bool { return strings.HasPrefix(k, AskWake) }) {
			t.Errorf("%s: %s asked %v, want no wake for a voided round at an older H", name, r.ID, d.Asked)
		}
	}
	in := with(noRounds, stuckAct("start_reviewer@1@c1"), moved, stuckAct("start_reviewer@1@c2"))
	if r, d := FirstMatch(TaskRows, in); r.ID != "S2" || !slices.Equal(d.Asked, []string{"wake_coordinator@stuck@start_reviewer@1@c2@c2"}) {
		t.Errorf("failed act at H: %s %v, want S2's wake", r.ID, d.Asked)
	}
}

func TestSettingsChanged(t *testing.T) {
	s := DefaultSettings()
	if got := s.Changed(); len(got) != 0 {
		t.Errorf("defaults changed: %v", got)
	}
	s.StuckMinutes = StuckMin
	if got := s.Changed(); !slices.Equal(got, []string{"stuck_minutes 5"}) {
		t.Errorf("changed: %v", got)
	}
	s.ChecksGraceMinutes, s.ReviewRounds = 7, 2
	if got := s.Changed(); !slices.Equal(got, []string{"checks_grace_minutes 7", "stuck_minutes 5"}) {
		t.Errorf("changed: %v", got)
	}
	s.ReviewRounds = 1
	if got := s.Changed(); !slices.Equal(got, []string{"checks_grace_minutes 7", "review_rounds 1", "stuck_minutes 5"}) {
		t.Errorf("changed with review_rounds 1: %v", got)
	}
}
