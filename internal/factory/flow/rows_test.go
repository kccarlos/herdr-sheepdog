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

// landing is a task that N42 merges: passed at c1, green, clean, open.
func landing() In {
	return In{
		Task: Task{ID: "t", Attempts: []Attempt{{
			BaseCommit: "b0",
			Ready:      []Ready{{Head: "c1"}},
			Rounds:     []Round{{N: 1, Head: "c1", Verdict: VerdictPass}},
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
	changes     = func(in *In) { a0(in).Rounds[0].Verdict = VerdictChanges }
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
)

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
		"ladderSays(Passed)": func(in *In) { a0(in).Rounds[0].Verdict = "maybe" },
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
		a0(in).Rounds = append(a0(in).Rounds, Round{N: 2, Head: "c2", Verdict: VerdictPass})
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

// TestLadder: one case per ladder row. No two ladder rows can both hold:
// each reads the one newest covering round.
func TestLadder(t *testing.T) {
	for _, c := range []struct {
		row, answer string
		in          In
	}{
		{"L7", Review, with(noRounds)},
		{"L7", Review, with(func(in *In) { a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"}) })},
		{"L9", Passed, landing()},
		{"L13", Fix, with(changes)},
		{"L-", NoLadderRow, with(func(in *In) { a0(in).Rounds[0].Verdict = "maybe" })},
	} {
		r, d := FirstMatch(LadderRows, c.in)
		if r.ID != c.row || d.Why != c.answer || Ladder(c.in) != c.answer {
			t.Errorf("want %s %s, got %s %s", c.row, c.answer, r.ID, d.Why)
		}
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
