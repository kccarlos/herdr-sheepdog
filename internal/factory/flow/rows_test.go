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
	splitMark   = func(in *In) { in.Task.Split = &Split{Children: []string{"t-a", "t-b"}} }
	droppedMark = func(in *In) { in.Task.Dropped = &Dropped{Why: "no longer needed"} }
	openRecap   = func(in *In) { in.Task.Recaps = []Recap{{Trigger: "review", Allowed: []string{"split", "drop"}}} }
	merged      = func(in *In) { in.Facts.PR.State = PRMerged }
	workerEnded = func(in *In) { in.Facts.Worker.State = RunGone }
	reviewEnded = func(in *In) { in.Facts.Reviewer.State = RunGone }
	noAttempts  = func(in *In) { in.Task.Attempts = nil; in.Facts = Facts{} }
	waitDep     = func(in *In) { in.Facts.DepsNotDone = []string{"T1"} }
	dropDep     = func(in *In) { in.Facts.DepsDropped = []string{"T2"} }
	noSlotF     = func(in *In) { in.Facts.NoSlot = true }
	onePass     = func(in *In) { a0(in).Rounds = a0(in).Rounds[:1] }
	changes     = func(in *In) {
		a0(in).Rounds = []Round{{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictChanges, Majors: 1}}
	}
	streakRecap = func(in *In) {
		a0(in).Rounds = []Round{
			{N: 1, Kind: RoundFull, Head: "c0", Verdict: VerdictChanges, Majors: 1},
			{N: 2, Kind: RoundDelta, Head: "c0b", Verdict: VerdictChanges, Majors: 1},
			{N: 3, Kind: RoundDelta, Head: "c1", Verdict: VerdictChanges, Majors: 1},
		}
	}
	parked   = func(in *In) { in.Task.Parked = true }
	runsLive = func(in *In) { a0(in).RunID = "w1" }
	nothingF = func(in *In) {
		a0(in).Ready = nil
		a0(in).Rounds = nil
		a0(in).NothingToChange = "already done upstream"
	}
	rejected  = func(in *In) { a0(in).Rejects = []Reject{{Head: "c1", Round: 1, Why: "not a bug"}} }
	noRounds  = func(in *In) { a0(in).Rounds = nil }
	notReady  = func(in *In) { a0(in).Ready = nil; in.Facts.Worker = Run{} }
	tipAheadF = func(in *In) { in.Facts.Tip, in.Facts.TipRel = "c2", RelAhead }
	prGone    = func(in *In) { in.Facts.PR = PR{Answered: true} }
	prBehind  = func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "b0", RelBehind }
	pending   = func(in *In) { in.Facts.PR.Checks = ChecksPending }
	overCap   = func(in *In) { a0(in).SizeCap, in.Facts.Lines = 100, 101 }
	atCap     = func(in *In) { a0(in).SizeCap, in.Facts.Lines = 100, 100 }
	sizeRecap = func(in *In) {
		in.Task.Recaps = []Recap{{Trigger: TriggerSize, Allowed: []string{ChoiceContinue, "split", "new", "drop"}}}
	}
	mergeTip  = func(in *In) { in.Facts.Tip, in.Facts.TipRel, in.Facts.TipMergeOnly = "m2", RelAhead, true }
	movedToM2 = func(in *In) {
		a0(in).MergeOnly = []Advance{{From: "c1", To: "m2"}}
		in.Facts.Tip, in.Facts.PR.Head = "m2", "m2"
	}
	workingF   = func(in *In) { in.Facts.Worker.State = RunWorking }
	asked      = func(in *In) { a0(in).Acts = append(a0(in).Acts, Act{Key: "merge@c1", Result: "done"}) }
	red        = func(in *In) { in.Facts.PR.Checks = ChecksRed }
	noAnswer   = func(in *In) { in.Facts.PR.Answered = false }
	closedPR   = func(in *In) { in.Facts.PR.State = PRClosed }
	conflict   = func(in *In) { in.Facts.PR.Mergeable = MergeableConflict }
	behind     = func(in *In) { in.Facts.PR.Behind = true }
	behindUnkn = func(in *In) { in.Facts.PR.BehindUnknown = true }
	baseRedF   = func(in *In) { in.Facts.BaseRed, in.Facts.BaseRedSince = true, now }
	diverged   = func(in *In) { in.Facts.Tip, in.Facts.TipRel = "c9", RelDiverged }
	reviewReq  = func(in *In) { in.Facts.PR.ReviewRequired = true }
	ciRecap    = func(in *In) {
		in.Task.Recaps = []Recap{{Trigger: TriggerCI, Allowed: []string{ChoiceContinue, "split", "new", "drop"}}}
	}
	timeRecap = func(in *In) {
		in.Task.Recaps = []Recap{{Trigger: TriggerTime, Allowed: []string{ChoiceContinue, "split", "new", "drop"}}}
	}
	overTime = func(in *In) {
		a0(in).Working = time.Duration(DefaultSettings().BuildHours) * time.Hour
	}
	ciFailed3 = func(in *In) {
		a0(in).CIFails = []CIFail{
			{Head: "f1", At: now.Add(-30 * time.Minute)},
			{Head: "f2", At: now.Add(-20 * time.Minute)},
			{Head: "f3", At: now.Add(-10 * time.Minute)},
		}
	}
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
		"prOpen":                closedPR,
		"prHeadIsH":             func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "c9", RelAhead },
		"answered":              noAnswer,
		"githubAvail":           func(in *In) { in.Facts.GitHubUnavailable = true },
		"mergeClean":            conflict,
		"noReviewRequired":      reviewReq,
		"notBehind":             behind,
		"notBehindUnknown":      behindUnkn,
		"notWorking":            workingF,
		"onePassNeedsL8":        onePass,
		"ladderSays(Passed)":    func(in *In) { a0(in).Rounds[1].Verdict = "maybe" },
		"checksGreen":           red,
		"checksAbsent":          func(in *In) { in.Facts.PR.Checks = ChecksAbsent },
		"checksStoppedTimeout":  func(in *In) { in.Facts.PR.Checks = ChecksStoppedTimeout },
		"checksStoppedExternal": func(in *In) { in.Facts.PR.Checks = ChecksStoppedExternal },
		"checksStoppedUnknown":  func(in *In) { in.Facts.PR.Checks = ChecksStoppedUnknown },
		"checksUnreadable":      func(in *In) { in.Facts.PR.Checks = ChecksUnreadable },
		"tipAtOrBehindH":        diverged,
		"tipBehindNotCovered":   func(in *In) { in.Facts.Tip, in.Facts.TipRel = "b0", RelBehind },
		"tipBehindPastMove":     func(in *In) { movedToM2(in); in.Facts.Tip, in.Facts.TipRel = "b0", RelBehind },
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
	unmatched := func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "c9", "" }
	_, first := FirstMatch(TaskRows, with(unmatched))
	stalled := func(fs ...func(*In)) In {
		in := with(append([]func(*In){unmatched}, fs...)...)
		a0(&in).Acts = []Act{{Key: first.Asked[0], Result: "done"}}
		return in
	}
	newH := func(in *In) {
		a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"})
		a0(in).Rounds = append(a0(in).Rounds,
			Round{N: 3, Head: "c2", Verdict: VerdictPass},
			Round{N: 4, Head: "c2", Verdict: VerdictPass},
		)
	}
	for name, c := range map[string]struct {
		in   In
		wake bool
	}{
		"same stall":      {stalled(), false},
		"unknown verdict": {stalled(func(in *In) { a0(in).Rounds[1].Verdict = "maybe"; in.Facts.PR.Checks = ChecksRed }), true},
		"new H":           {stalled(newH), true},
	} {
		r, d := FirstMatch(TaskRows, c.in)
		if r.ID != "N43" || (len(d.Asked) == 1) != c.wake {
			t.Errorf("%s: %s asked %v, want wake=%v", name, r.ID, d.Asked, c.wake)
		}
	}
}

// TestClosedPR: a closed PR matches N4, wakes the coordinator once, runs no
// reviewer, never reopens, and never opens a second PR.
func TestClosedPR(t *testing.T) {
	for name, in := range map[string]In{
		"pending": with(closedPR, pending),
		"unknown": with(closedPR, func(in *In) { in.Facts.PR.Mergeable = MergeableUnknown }),
		"working": with(closedPR, workingF),
		"fix":     with(closedPR, changes),
		"review":  with(closedPR, noRounds),
	} {
		r, d := FirstMatch(TaskRows, in)
		if r.ID != "N4" || !slices.Equal(d.Asked, []string{"wake_coordinator@pr_closed"}) {
			t.Errorf("closed PR, %s: %s asked %v, want N4 wake once", name, r.ID, d.Asked)
		}
		a0(&in).Acts = append(a0(&in).Acts, Act{Key: "wake_coordinator@pr_closed", Result: "done", At: now})
		if r, d := FirstMatch(TaskRows, in); r.ID != "N4" || len(d.Asked) != 0 {
			t.Errorf("closed PR repeat, %s: %s asked %v, want N4 nil", name, r.ID, d.Asked)
		}
	}
}

func TestTablesCheck(t *testing.T) {
	if err := CheckTable(TaskRows); err != nil {
		t.Error(err)
	}
	for _, r := range TaskRows {
		if r.Stage == "" {
			t.Errorf("TaskRows[%s]: empty Stage", r.ID)
		}
	}
	if err := CheckTable(LadderRows); err != nil {
		t.Error(err)
	}
	if err := CheckTable(MergeOnlyRows); err != nil {
		t.Error(err)
	}
	if err := CheckTable(PRAdoptRows); err != nil {
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
		{"N1", with(splitMark), []string{"cleanup"}},
		{"N1", with(droppedMark), []string{"cleanup"}},
		{"N2", with(merged), []string{"mark_done", "wake_coordinator@merged_outside_bar"}},
		{"N3", with(parked, runsLive), []string{"stop_runs"}},
		{"N3", with(parked), nil},
		{"N4", with(closedPR), []string{"wake_coordinator@pr_closed"}},
		{"N5", with(func(in *In) {
			a0(in).Acts = []Act{{Key: "merge@c1", Result: "failed", Class: ClassSettled, Err: "HTTP 405: Pull Request is not mergeable", At: now}}
		}), []string{"wake_coordinator@failed@merge@c1"}},
		{"N5", with(func(in *In) {
			a0(in).Acts = []Act{
				{Key: "merge@c1", Result: "failed", Class: ClassTransient, At: now},
				{Key: "merge@c1", Result: "failed", Class: ClassTransient, At: now.Add(time.Minute)},
				{Key: "merge@c1", Result: "failed", Class: ClassTimeout, Err: "context deadline exceeded", At: now.Add(3 * time.Minute)},
			}
		}), []string{"wake_coordinator@failed@merge@c1"}},
		{"N6", with(openRecap), []string{"wake_coordinator@recap@1"}},
		{"N6", with(sizeRecap), []string{"wake_coordinator@recap@1", "tell_worker_stop@1"}},
		{"N6", with(ciRecap), []string{"wake_coordinator@recap@1", "tell_worker_stop@1"}},
		{"N6", with(timeRecap), []string{"wake_coordinator@recap@1", "tell_worker_stop@1"}},
		{"N6", with(sizeRecap, func(in *In) { in.Facts.Worker = Run{} }), []string{"wake_coordinator@recap@1"}},
		{"N6", with(sizeRecap, workerEnded), []string{"wake_coordinator@recap@1"}},
		{"N7", with(diverged), []string{"tell_worker_diverged@tip@c1"}},
		{"N8", with(nothingF, prGone), []string{"wake_coordinator@nothing_to_change@0"}},
		{"N9", with(noAttempts, waitDep), nil},
		{"N9", with(noAttempts, dropDep), []string{"wake_coordinator@dep_dropped@T2"}},
		{"N9", with(noAttempts, dropDep, func(in *In) { in.Task.Acts = []Act{{Key: "wake_coordinator@dep_dropped@T2", Result: "done"}} }), nil},
		{"N10", with(noAttempts, noSlotF), nil},
		{"N11", with(noAttempts), []string{"new_attempt"}},
		{"N11", with(func(in *In) { a0(in).Closed = "new attempt" }), []string{"new_attempt"}},
		{"N12", with(overCap), []string{"open_size_recap@0"}},
		{"N12", with(func(in *In) { in.Facts.Lines = 1 }), []string{"open_size_recap@0"}}, // no estimate (cap 0) never turns the check off (KB-CAP-18)
		{"N13", with(noRounds, ciFailed3), []string{"open_ci_recap@0"}},
		{"N14", with(noRounds, overTime), []string{"open_time_recap@0"}},
		{"N42", with(atCap), []string{"record_accept@c1", "merge@c1"}}, // at the cap is not over it
		{"N20", with(mergeTip), []string{"move_h_merge_only@m2"}},
		{"N20", with(func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel, in.Facts.PR.HeadMergeOnly = "m2", RelAhead, true }), []string{"move_h_merge_only@m2"}},
		{"N26", with(movedToM2, func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "c1", RelBehind }), []string{"push_and_open_pr@m2"}},
		{"N42", with(movedToM2), []string{"record_accept@m2", "merge@m2"}},                                                                   // the passes at c1 still cover m2
		{"N42", with(movedToM2, func(in *In) { in.Facts.Tip, in.Facts.TipRel = "c1", RelBehind }), []string{"record_accept@m2", "merge@m2"}}, // PR updated on GitHub: tip behind H still merges
		{"N22", with(changes), []string{"tell_worker_findings@1"}},
		{"N23", with(notReady, noRounds, prGone), []string{"start_worker"}},
		{"N23", with(notReady, noRounds, prGone, waitDep), []string{"start_worker"}}, // open attempt with unmet deps still matches an attempt row, not N9
		{"N24", with(streakRecap, splitOfRetro), []string{"open_n24_ask@c1"}},
		{"N25", with(streakRecap), []string{"open_recap@review"}},
		{"N26", with(noRounds, prGone), []string{"push_and_open_pr@c1"}},
		{"N26", with(noRounds, prGone, func(in *In) { in.Facts.PR.Number, in.Facts.PR.OtherBase = 7, "release" }), []string{"wake_coordinator@pr_other_base@7@release"}},
		{"N26", with(noRounds, prGone, noAnswer), nil},                    // "no PR" from a failed read: wait, no push
		{"N26", with(noRounds, prBehind, noAnswer), nil},                  // a stale behind PR, unanswered: wait
		{"N26", with(noRounds, func(in *In) { in.Facts.PR = PR{} }), nil}, // no snapshot yet
		{"N29", with(noRounds), []string{"start_reviewer@1@c1"}},
		{"N29", with(onePass), []string{"start_reviewer@2@c1"}},
		{"N29", with(changes, rejected), []string{"start_reviewer@2@c1"}}, // L10: reject consumes no round number
		{"N30", with(gateOwnerTask, func(in *In) {
			in.Asks = []Ask{answeredN31Ask("ask-gate-no", "t", "c1", GateOptionHold, now)}
		}), []string{"open_owner_no_recap@0"}},
		{"N31", with(gateOwnerTask), []string{"open_gate_ask@c1"}},
		{"N32", with(conflict), []string{"tell_worker_conflict@c1"}},
		{"N33", with(red, func(in *In) { in.Facts.PR.Failed = []Failure{{Run: "42"}} }), []string{"rerun_ci@42@@c1"}},
		{"N34", with(func(in *In) { in.Facts.PR.Checks = ChecksStoppedExternal; in.Facts.PR.Failed = []Failure{{Run: "43"}} }), []string{"rerun_ci@43@@c1"}},
		{"N34", with(func(in *In) { in.Facts.PR.Checks = ChecksStoppedTimeout }), []string{"wake_coordinator@checks_stopped@limit@c1"}},
		{"N34", with(func(in *In) { in.Facts.PR.Checks = ChecksStoppedUnknown; in.Facts.PR.Failed = []Failure{{Run: "44"}} }), []string{"wake_coordinator@checks_stopped@limit@c1"}},
		{"N35", with(func(in *In) {
			in.Facts.PR.Checks = ChecksAbsent
			in.Now = now.Add(time.Duration(DefaultSettings().ChecksGraceMinutes) * time.Minute)
		}), []string{"dispatch_ci@c1"}},
		{"N38", with(baseRedF, behind), nil},
		{"N38", with(baseRedF, behindUnkn), nil},
		{"N38", with(baseRedF, behind, func(in *In) {
			in.Facts.BaseRedSpell = "sha101"
			in.Now = now.Add(time.Duration(DefaultSettings().MainRedMinutes) * time.Minute)
		}), []string{"tell_worker_base_red@sha101", "wake_coordinator@base_red_held@sha101"}},
		{"N38", with(baseRedF, behind, func(in *In) {
			in.Facts.BaseRedSpell, in.Facts.BaseRedWoken = "sha101", true
			in.Now = now.Add(time.Duration(DefaultSettings().MainRedMinutes) * time.Minute)
		}), []string{"tell_worker_base_red@sha101"}},
		{"N38", with(baseRedF, behind, func(in *In) {
			in.Now = now.Add(time.Duration(DefaultSettings().MainRedMinutes) * time.Minute)
		}), nil},
		{"N42", with(baseRedF), []string{"record_accept@c1", "merge@c1"}},
		{"N36", with(pending), nil},
		{"N36", with(noAnswer), nil}, // checks unreadable: wait, no wake
		{"N36", with(func(in *In) { in.Facts.PR.Checks = ChecksUnreadable }), nil},
		{"N36", with(func(in *In) { in.Facts.GitHubUnavailable = true }), nil},
		{"N36", with(behindUnkn), nil},
		{"N36", with(behindUnkn, func(in *In) { in.Now = now.Add(time.Duration(DefaultSettings().ChecksGraceMinutes) * time.Minute) }), []string{"wake_coordinator@behind_unknown@c1"}},
		{"N37", with(reviewReq), []string{"wake_coordinator@review_required@c1"}},
		{"N39", with(workingF), nil},
		{"N40", with(behind), []string{"update_branch@c1"}},
		{"N41", with(asked), nil},
		{"N42", landing(), []string{"record_accept@c1", "merge@c1"}},
		{"N43", with(func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "c9", "" }), []string{"wake_coordinator@no_rule_matched@c1@open@green@clean@same"}},
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
		{"N2", "N3", true, with(merged, parked)},
		{"N3", "N4", true, with(parked, closedPR)},
		{"N4", "N5", true, with(closedPR, noRounds, func(in *In) {
			a0(in).Acts = []Act{{Key: "start_reviewer@1@c1", Result: "failed", Class: ClassSettled, At: now}}
		})},
		{"N5", "N6", true, with(sizeRecap, func(in *In) {
			a0(in).Acts = []Act{{Key: "tell_worker_stop@1", Result: "failed", Class: ClassSettled, At: now}}
		})},
		{"N4", "N6", false, with(closedPR, openRecap)},
		{"N6", "N7", true, with(openRecap, diverged)},
		{"N7", "N8", true, with(diverged, nothingF)},
		{"N8", "N23", false, with(nothingF, prGone)},
		{"N9", "N10", true, with(noAttempts, waitDep, noSlotF)},
		{"N10", "N11", true, with(noAttempts, noSlotF)},
		{"N1", "N9", false, with(done, noAttempts, waitDep)},
		{"N3", "N9", false, with(parked, noAttempts, waitDep)},
		{"N6", "N9", false, with(openRecap, noAttempts, waitDep)},
		{"N9", "N11", false, with(noAttempts, waitDep)},
		{"N6", "N22", false, with(openRecap, changes)},
		{"N6", "N29", false, with(openRecap, noRounds)},
		{"N6", "N12", false, with(openRecap, overCap)},
		{"N6", "N13", false, with(openRecap, noRounds, ciFailed3)},
		{"N6", "N14", false, with(openRecap, noRounds, overTime)},
		{"N12", "N13", true, with(overCap, noRounds, ciFailed3)},
		{"N13", "N14", true, with(noRounds, ciFailed3, overTime)},
		{"N14", "N20", true, with(noRounds, overTime, mergeTip)},
		{"N14", "N29", false, with(noRounds, overTime)},
		{"N13", "N29", false, with(noRounds, ciFailed3)},
		{"N20", "N22", true, with(changes, mergeTip)},
		{"N12", "N23", false, with(overCap, tipAheadF)},
		{"N22", "N23", true, with(changes, tipAheadF)},
		{"N22", "N26", false, with(changes, prBehind)},
		{"N23", "N24", true, with(streakRecap, splitOfRetro, tipAheadF)},
		{"N24", "N25", true, with(streakRecap, splitOfRetro)},
		{"N23", "N25", false, with(streakRecap, tipAheadF)},
		{"N25", "N26", true, with(streakRecap, prGone)},
		{"N23", "N36", false, with(tipAheadF, pending)},
		{"N26", "N29", true, with(noRounds, prGone)},
		{"N30", "N31", true, with(gateOwnerTask, func(in *In) {
			in.Asks = []Ask{answeredN31Ask("ask-gate-no", "t", "c1", GateOptionHold, now)}
		})},
		{"N31", "N32", true, with(gateOwnerTask, conflict)},
		{"N32", "N33", true, with(conflict, red)},
		{"N32", "N36", false, with(conflict, pending)},
		{"N32", "N37", false, with(conflict, reviewReq)},
		{"N35", "N38", true, with(baseRedF, behind, func(in *In) {
			in.Facts.PR.Checks = ChecksAbsent
			in.Now = now.Add(time.Duration(DefaultSettings().ChecksGraceMinutes) * time.Minute)
		})},
		{"N38", "N36", true, with(baseRedF, behindUnkn)},
		{"N38", "N40", false, with(baseRedF, behind)},
		{"N36", "N37", true, with(pending, reviewReq)},
		{"N36", "N40", false, with(pending, behind)},
		{"N37", "N39", true, with(reviewReq, workingF)},
		{"N39", "N40", true, with(workingF, behind)},
		{"N39", "N41", false, with(workingF, asked)},
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
	}{{0, "N36"}, {grace - time.Second, "N36"}, {grace, "N35"}} {
		in := with(func(in *In) { in.Facts.PR.Checks = ChecksAbsent; in.Now = now.Add(c.age) })
		if r, _ := FirstMatch(TaskRows, in); r.ID != c.want {
			t.Errorf("no checks after %v: %s, want %s", c.age, r.ID, c.want)
		}
	}
}

func TestEnsureRun(t *testing.T) {
	// Worker table (design.md 4.8): pending, engine-stopped, fresh, backoff, resume/handoff, no-slot, quota/signed_out, exhausted -> failure wake.
	wBase := func(fs ...func(*In)) In {
		in := with(notReady, noRounds, prGone)
		for _, f := range fs {
			f(&in)
		}
		return in
	}
	for name, tc := range map[string]struct {
		in   In
		want []string
	}{
		"none yet starts fresh":       {wBase(), []string{"start_worker"}},
		"none yet waits without slot": {wBase(noSlotF), nil},
		"live idle needs nothing":     {wBase(func(in *In) { in.Facts.Worker.State = RunIdle }), nil},
		"engine stopped resumes uncounted": {wBase(workerEnded, func(in *In) {
			a0(in).Run = &RunRecord{State: PhaseEnded, Result: EndStopped, CLI: "claude", SessionID: "s1", Resumable: true}
		}), []string{"start_worker@resume"}},
		"engine stopped waits without slot": {wBase(workerEnded, noSlotF, func(in *In) {
			a0(in).Run = &RunRecord{State: PhaseEnded, Result: EndStopped, CLI: "claude", SessionID: "s1", Resumable: true}
		}), nil},
		"exited waits before backoff": {wBase(workerEnded, func(in *In) {
			a0(in).Run = &RunRecord{State: PhaseEnded, Result: EndExited, CLI: "claude", SessionID: "s1", Resumable: true, EndedAt: now.Add(-30 * time.Second)}
		}), nil},
		"exited resumes past backoff": {wBase(workerEnded, func(in *In) {
			a0(in).Run = &RunRecord{State: PhaseEnded, Result: EndExited, CLI: "claude", SessionID: "s1", Resumable: true, EndedAt: now.Add(-time.Minute)}
		}), []string{"start_worker@resume@1"}},
		"resume_failed starts handoff": {wBase(workerEnded, func(in *In) {
			a0(in).Restarts = 1
			a0(in).Run = &RunRecord{State: PhaseEnded, Result: EndResumeFailed, CLI: "claude", SessionID: "s1", Resumable: false, EndedAt: now.Add(-2 * time.Minute)}
		}), []string{"start_worker@handoff@2"}},
		"quota starts handoff uncounted": {wBase(workerEnded, func(in *In) {
			a0(in).Run = &RunRecord{State: PhaseEnded, Result: EndQuota, EndedAt: now}
		}), []string{"start_worker@handoff"}},
		"restarts exhausted wakes coordinator once": {wBase(workerEnded, func(in *In) {
			a0(in).Restarts = DefaultSettings().WorkerRestarts
			a0(in).Run = &RunRecord{State: PhaseEnded, Result: EndExited, EndedAt: now.Add(-time.Hour)}
		}), []string{"wake_coordinator@worker_failed"}},
		"crash window restart asks uncounted handoff": {wBase(func(in *In) {
			a0(in).Restarts = 1
			a0(in).Acts = []Act{{Key: AskStartWorker, Result: "done"}}
			a0(in).Run = &RunRecord{State: PhaseStarting, CLI: "claude", SessionID: "s1"}
		}), []string{"start_worker@handoff"}},
		"crash window resumed restart asks uncounted resume": {wBase(func(in *In) {
			a0(in).Restarts = 1
			a0(in).Acts = []Act{{Key: AskStartWorker, Result: "done"}}
			a0(in).Run = &RunRecord{State: PhaseStarting, CLI: "claude", SessionID: "s1", Resumed: true}
		}), []string{"start_worker@resume"}},
	} {
		if got := EnsureRun(tc.in, RoleWorker); !slices.Equal(got, tc.want) {
			t.Errorf("worker %s: got %v, want %v", name, got, tc.want)
		}
	}

	// Reviewer table (design.md 4.8): fresh, engine-stopped, retry below reviewer_retries, failure at reviewer_retries.
	rBase := func(fs ...func(*In)) In {
		in := with(noRounds)
		for _, f := range fs {
			f(&in)
		}
		return in
	}
	for name, tc := range map[string]struct {
		in   In
		want []string
	}{
		"fresh reviewer": {rBase(), []string{"start_reviewer@1@c1"}},
		"engine stopped reviewer": {rBase(reviewEnded, func(in *In) {
			a0(in).Review = &ReviewRun{Round: 1, Head: "c1", Run: &RunRecord{State: PhaseEnded, Result: EndStopped}}
		}), []string{"start_reviewer@1@resume@c1"}},
		"crash window reviewer retry asks uncounted": {rBase(func(in *In) {
			a0(in).Acts = []Act{{Key: "start_reviewer@1@c1", Result: "done"}}
			a0(in).Review = &ReviewRun{Round: 1, Head: "c1", Retries: 1, Run: &RunRecord{State: PhaseStarting}}
		}), []string{"start_reviewer@1@resume@c1"}},
		"no report retry 1": {rBase(reviewEnded, func(in *In) {
			a0(in).Review = &ReviewRun{Round: 1, Head: "c1", Retries: 0, Run: &RunRecord{State: PhaseEnded, Result: EndExited}}
		}), []string{"start_reviewer@1@1@c1"}},
		"no report retry 2": {rBase(reviewEnded, func(in *In) {
			a0(in).Review = &ReviewRun{Round: 1, Head: "c1", Retries: 1, Run: &RunRecord{State: PhaseEnded, Result: EndStuck}}
		}), []string{"start_reviewer@1@2@c1"}},
		"retries exhausted wakes once": {rBase(reviewEnded, func(in *In) {
			a0(in).Review = &ReviewRun{Round: 1, Head: "c1", Retries: DefaultSettings().ReviewerRetries, Run: &RunRecord{State: PhaseEnded, Result: EndExited}}
		}), []string{"wake_coordinator@reviewer_failed@1@c1"}},
	} {
		if got := EnsureRun(tc.in, RoleReviewer); !slices.Equal(got, tc.want) {
			t.Errorf("reviewer %s: got %v, want %v", name, got, tc.want)
		}
	}
}

func TestNoS2AndTimeLimitsN14N29(t *testing.T) {
	// S2 is gone from TaskRows.
	for _, r := range TaskRows {
		if r.ID == "S2" {
			t.Fatal("S2 row still present in TaskRows")
		}
	}
	// N14 reads Attempt.Working (single accumulator), resets when cleared, and is skipped when ladder passed.
	s := DefaultSettings()
	in := with(noRounds, func(in *In) { a0(in).Working = time.Duration(s.BuildHours)*time.Hour - time.Minute })
	if got := WorkingTime(in.Task); got != a0(&in).Working {
		t.Fatalf("WorkingTime = %v, want %v", got, a0(&in).Working)
	}
	if r, _ := FirstMatch(TaskRows, in); r.ID == "N14" {
		t.Fatalf("N14 fired under build_hours: %v", WorkingTime(in.Task))
	}
	a0(&in).Working = time.Duration(s.BuildHours) * time.Hour
	if r, d := FirstMatch(TaskRows, in); r.ID != "N14" || !slices.Equal(d.Asked, []string{"open_time_recap@0"}) {
		t.Fatalf("at build_hours = %s %v, want N14 [open_time_recap@0]", r.ID, d.Asked)
	}
	// Clearing Attempt.Working on recap continue resets N14.
	in.Task.Recaps = []Recap{{Trigger: TriggerTime, Decided: &RecapDecision{Choice: ChoiceContinue, At: now}}}
	a0(&in).Working = 0
	if r, _ := FirstMatch(TaskRows, in); r.ID == "N14" {
		t.Fatalf("N14 still fired after recap continue reset")
	}
	// Ladder == Passed does not fire N14.
	if r, _ := FirstMatch(TaskRows, with(overTime)); r.ID != "N42" {
		t.Fatalf("ladder passed with overTime = %s, want N42", r.ID)
	}

	// N29: idle no-tag reviewer gets one nudge, then stop; refused reviewer waits until review_minutes; review_minutes stops even while working.
	revIdle := with(noRounds, func(in *In) {
		a0(in).Review = &ReviewRun{Round: 1, Head: "c1"}
		in.Facts.Reviewer = Run{State: RunIdle, LastTurnEnd: now}
	})
	if r, d := FirstMatch(TaskRows, revIdle); r.ID != "N29" || !slices.Equal(d.Asked, []string{"nudge_reviewer@1@0@c1"}) {
		t.Fatalf("idle reviewer first = %s %v, want N29 nudge", r.ID, d.Asked)
	}
	// After nudge done, idle again -> stop.
	a0(&revIdle).Acts = []Act{{Key: "nudge_reviewer@1@0@c1", Result: "done", At: now}}
	if r, d := FirstMatch(TaskRows, revIdle); r.ID != "N29" || !slices.Equal(d.Asked, []string{"stop_reviewer@1@0@c1"}) {
		t.Fatalf("idle reviewer after nudge = %s %v, want N29 stop", r.ID, d.Asked)
	}
	// Refused idle reviewer is not nudged, but is stopped once Age reaches review_minutes.
	revRefused := with(noRounds, func(in *In) {
		a0(in).Review = &ReviewRun{Round: 1, Head: "c1", Age: time.Duration(s.ReviewMinutes-1) * time.Minute}
		in.Facts.Reviewer = Run{State: RunIdle, Refused: true}
	})
	if r, d := FirstMatch(TaskRows, revRefused); r.ID != "N29" || len(d.Asked) != 0 {
		t.Fatalf("refused reviewer under review_minutes = %s %v, want wait", r.ID, d.Asked)
	}
	a0(&revRefused).Review.Age = time.Duration(s.ReviewMinutes) * time.Minute
	if r, d := FirstMatch(TaskRows, revRefused); r.ID != "N29" || !slices.Equal(d.Asked, []string{"stop_reviewer@1@0@c1"}) {
		t.Fatalf("refused reviewer at review_minutes = %s %v, want N29 stop", r.ID, d.Asked)
	}
	// Reviewer older than review_minutes is stopped even while working.
	revOld := with(noRounds, func(in *In) {
		a0(in).Review = &ReviewRun{Round: 1, Head: "c1", Age: time.Duration(s.ReviewMinutes) * time.Minute}
		in.Facts.Reviewer = Run{State: RunWorking}
	})
	if r, d := FirstMatch(TaskRows, revOld); r.ID != "N29" || !slices.Equal(d.Asked, []string{"stop_reviewer@1@0@c1"}) {
		t.Fatalf("reviewer past review_minutes = %s %v, want N29 stop", r.ID, d.Asked)
	}
}

func TestSection8BackoffDoublingN5OutageAndRetry(t *testing.T) {
	in := landing()
	k := "merge@c1"
	a0(&in).Acts = []Act{{Key: "record_accept@c1", Result: "done", At: now}}

	// 1. Transient failure 1: not retried before 1m back-off; retried at 1m.
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: k, Result: "failed", Class: ClassTransient, Err: "HTTP 502", At: now})
	in.Now = now.Add(59 * time.Second)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N42" || len(d.Asked) != 0 {
		t.Fatalf("before 1m backoff: %s %v, want N42 nil", r.ID, d.Asked)
	}
	in.Now = now.Add(time.Minute)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N42" || !slices.Equal(d.Asked, []string{k}) {
		t.Fatalf("at 1m backoff: %s %v, want N42 [%s]", r.ID, d.Asked, k)
	}

	// 2. Uncounted outage failure still doubles back-off (now 2m), without counting toward N5.
	t1 := in.Now
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: k, Result: "failed", Class: ClassTransient, Err: "outage", Uncounted: true, At: t1})
	in.Now = t1.Add(time.Minute)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N42" || len(d.Asked) != 0 {
		t.Fatalf("after uncounted failure + 1m (before doubled 2m): %s %v, want N42 nil", r.ID, d.Asked)
	}
	in.Now = t1.Add(2*time.Minute - time.Second)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N42" || len(d.Asked) != 0 {
		t.Fatalf("before 2m doubled backoff after uncounted: %s %v, want N42 nil", r.ID, d.Asked)
	}
	in.Now = t1.Add(2 * time.Minute)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N42" || !slices.Equal(d.Asked, []string{k}) {
		t.Fatalf("at 2m doubled backoff after uncounted: %s %v, want N42 [%s]", r.ID, d.Asked, k)
	}

	// 3. Transient failure 2 (2 counted, 3 total tries): back-off doubles to 4m, still not N5.
	t2 := in.Now
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: k, Result: "failed", Class: ClassTransient, Err: "HTTP 503", At: t2})
	in.Now = t2.Add(4*time.Minute - time.Second)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N42" || len(d.Asked) != 0 {
		t.Fatalf("before 4m backoff: %s %v, want N42 nil", r.ID, d.Asked)
	}
	in.Now = t2.Add(4 * time.Minute)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N42" || !slices.Equal(d.Asked, []string{k}) {
		t.Fatalf("at 4m backoff: %s %v, want N42 [%s]", r.ID, d.Asked, k)
	}

	// 4. Failure 3 (reaches ensure_failures=3): goes to N5, wakes once with cleaned+capped last words, never re-fires.
	longErr := "line1\n\x07line2   " + strings.Repeat("x", 600)
	t3 := in.Now
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: k, Result: "failed", Class: ClassTimeout, Err: longErr, At: t3})
	r, d := FirstMatch(TaskRows, in)
	if r.ID != "N5" || !slices.Equal(d.Asked, []string{"wake_coordinator@failed@merge@c1"}) {
		t.Fatalf("at ensure_failures: %s %v, want N5 wake once", r.ID, d.Asked)
	}
	if !strings.Contains(d.Why, "merge@c1 failed (timeout): line1 line2 ") || !strings.Contains(d.Why, "task retry ") || strings.ContainsAny(d.Why, "\n\x07") || len([]rune(d.Why)) > MaxActErrRunes+120 {
		t.Fatalf("N5 why not cleaned/capped: len=%d why=%q", len([]rune(d.Why)), d.Why)
	}
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: "wake_coordinator@failed@merge@c1", Result: "done", At: t3})
	in.Now = t3.Add(time.Hour)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N5" || len(d.Asked) != 0 {
		t.Fatalf("N5 repeat pass: %s %v, want N5 nil", r.ID, d.Asked)
	}

	// 5. After a recorded retry act, N5 clears and the key is asked again.
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: AskRetry, Result: "done", At: in.Now})
	if r, d := FirstMatch(TaskRows, in); r.ID != "N42" || !slices.Equal(d.Asked, []string{k}) {
		t.Fatalf("after recorded retry: %s %v, want N42 [%s]", r.ID, d.Asked, k)
	}

	// 6. One settled failure -> N5 at once (with fresh retry-indexed wake key).
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: k, Result: "failed", Class: ClassSettled, Err: "HTTP 403: forbidden", At: in.Now})
	if r, d := FirstMatch(TaskRows, in); r.ID != "N5" || !slices.Equal(d.Asked, []string{"wake_coordinator@failed@1@merge@c1"}) || !strings.Contains(d.Why, "HTTP 403: forbidden") {
		t.Fatalf("after 1 settled failure: %s %v (%q), want immediate N5", r.ID, d.Asked, d.Why)
	}

	// 7. Many uncounted outage failures cap back-off at 30m and never reach N5.
	capIn := landing()
	a0(&capIn).Acts = []Act{{Key: "record_accept@c1", Result: "done", At: now}}
	for i := range 6 {
		a0(&capIn).Acts = append(a0(&capIn).Acts, Act{Key: k, Result: "failed", Class: ClassTransient, Uncounted: true, At: now.Add(time.Duration(i) * time.Minute)})
	}
	lastAt := now.Add(5 * time.Minute)
	capIn.Now = lastAt.Add(30*time.Minute - time.Second)
	if r, d := FirstMatch(TaskRows, capIn); r.ID != "N42" || len(d.Asked) != 0 {
		t.Fatalf("before 30m cap: %s %v, want N42 nil", r.ID, d.Asked)
	}
	capIn.Now = lastAt.Add(30 * time.Minute)
	if r, d := FirstMatch(TaskRows, capIn); r.ID != "N42" || !slices.Equal(d.Asked, []string{k}) {
		t.Fatalf("at 30m cap: %s %v, want N42 [%s]", r.ID, d.Asked, k)
	}
}

func TestN3StopRunsSharedBackoffOutageAndExhaustedWake(t *testing.T) {
	in := with(parked, runsLive)
	// Uncounted outage failure backs off (1m, then 2m) instead of hot-looping every pass.
	a0(&in).Acts = []Act{{Key: AskStopRuns, Result: "failed", Class: ClassTransient, Uncounted: true, At: now}}
	in.Now = now.Add(15 * time.Second)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N3" || len(d.Asked) != 0 {
		t.Fatalf("N3 uncounted + 15s: %s %v, want N3 nil (no hot loop)", r.ID, d.Asked)
	}
	in.Now = now.Add(time.Minute)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N3" || !slices.Equal(d.Asked, []string{AskStopRuns}) {
		t.Fatalf("N3 uncounted + 1m: %s %v, want N3 [%s]", r.ID, d.Asked, AskStopRuns)
	}
	t1 := in.Now
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: AskStopRuns, Result: "failed", Class: ClassTransient, Uncounted: true, At: t1})
	in.Now = t1.Add(time.Minute)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N3" || len(d.Asked) != 0 {
		t.Fatalf("N3 2nd uncounted + 1m: %s %v, want N3 nil (2m doubled backoff)", r.ID, d.Asked)
	}
	in.Now = t1.Add(2 * time.Minute)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N3" || !slices.Equal(d.Asked, []string{AskStopRuns}) {
		t.Fatalf("N3 2nd uncounted + 2m: %s %v, want N3 [%s]", r.ID, d.Asked, AskStopRuns)
	}

	// Exhausted stop_runs wakes the coordinator once (runs still alive), then waits silently until retry.
	ex := with(parked, runsLive)
	for i := range DefaultSettings().EnsureFailures {
		a0(&ex).Acts = append(a0(&ex).Acts, Act{Key: AskStopRuns, Result: "failed", Class: ClassTransient, Err: "pid\nstill alive", At: now.Add(time.Duration(i) * time.Minute)})
	}
	r, d := FirstMatch(TaskRows, ex)
	if r.ID != "N5" || !slices.Equal(d.Asked, []string{"wake_coordinator@failed@stop_runs"}) || !strings.Contains(d.Why, "stop_runs failed (transient): pid still alive") {
		t.Fatalf("N5 exhausted: %s %v (%q), want once wake naming stop_runs failure", r.ID, d.Asked, d.Why)
	}
	a0(&ex).Acts = append(a0(&ex).Acts, Act{Key: "wake_coordinator@failed@stop_runs", Result: "done", At: now})
	if r, d := FirstMatch(TaskRows, ex); r.ID != "N5" || len(d.Asked) != 0 {
		t.Fatalf("N5 exhausted repeat: %s %v, want N5 nil", r.ID, d.Asked)
	}
	a0(&ex).Acts = append(a0(&ex).Acts, Act{Key: AskRetry, Result: "done", At: now})
	if r, d := FirstMatch(TaskRows, ex); r.ID != "N3" || !slices.Equal(d.Asked, []string{AskStopRuns}) {
		t.Fatalf("N3 after retry: %s %v, want N3 [%s]", r.ID, d.Asked, AskStopRuns)
	}
}

// TestMergeAfterRetryAndWakeCommands checks that after a retry, mergeAsked and
// ensure agree (a done merge whose PR is still open is asked again, and failing
// again reaches N5), and that N4, N5 and N7 wake texts name the exact task
// retry / task say commands.
func TestMergeAfterRetryAndWakeCommands(t *testing.T) {
	// 1. Done merge whose PR is still open: N41 before retry, N42 asking merge@c1 after retry.
	in := landing()
	in.Task.ID = "T1"
	a0(&in).Acts = []Act{
		{Key: "record_accept@c1", Result: "done", At: now},
		{Key: "merge@c1", Result: "done", At: now},
	}
	if r, d := FirstMatch(TaskRows, in); r.ID != "N41" || len(d.Asked) != 0 {
		t.Fatalf("before retry with done merge: %s %v, want N41 nil", r.ID, d.Asked)
	}
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: AskRetry, Result: "done", At: now})
	if r, d := FirstMatch(TaskRows, in); r.ID != "N42" || !slices.Equal(d.Asked, []string{"merge@c1"}) {
		t.Fatalf("after retry with prior done merge: %s %v, want N42 [merge@c1]", r.ID, d.Asked)
	}
	// Failing settled after retry reaches N5 (not masked by pre-retry done merge).
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: "merge@c1", Result: "failed", Class: ClassSettled, Err: "HTTP 422", At: now})
	if r, d := FirstMatch(TaskRows, in); r.ID != "N5" || !slices.Equal(d.Asked, []string{"wake_coordinator@failed@1@merge@c1"}) {
		t.Fatalf("settled merge failure after prior done+retry: %s %v, want N5", r.ID, d.Asked)
	}

	// 2. N4, N5 and N7 wake texts name exact commands with task ID.
	n4 := with(closedPR, func(in *In) { in.Task.ID = "T4" })
	if _, d := FirstMatch(TaskRows, n4); !strings.Contains(d.Why, `task retry T4 --why`) {
		t.Fatalf("N4 why = %q, want exact task retry command", d.Why)
	}
	n5 := with(func(in *In) {
		in.Task.ID = "T5"
		a0(in).Acts = []Act{{Key: "merge@c1", Result: "failed", Class: ClassSettled, Err: "HTTP 422", At: now}}
	})
	if _, d := FirstMatch(TaskRows, n5); !strings.Contains(d.Why, `task retry T5 --why`) {
		t.Fatalf("N5 why = %q, want exact task retry command", d.Why)
	}
	n7 := with(diverged, func(in *In) {
		in.Task.ID = "T7"
		a0(in).Acts = []Act{{Key: key(AskTellDiverged, "tip", "c1"), Result: "done", At: now.Add(-time.Hour)}}
	})
	if _, d := FirstMatch(TaskRows, n7); !strings.Contains(d.Why, `task say T7 --why`) || !strings.Contains(d.Why, `task retry T7 --why`) {
		t.Fatalf("N7 why = %q, want exact task say and task retry commands", d.Why)
	}
	// After N7 wake + retry, N7 tells the worker again and wakes again if still diverged.
	a0(&n7).Acts = append(a0(&n7).Acts,
		Act{Key: key(AskWake, "diverged", "tip", "c1"), Result: "done", At: now},
		Act{Key: AskRetry, Result: "done", At: now},
	)
	if r, d := FirstMatch(TaskRows, n7); r.ID != "N7" || !slices.Contains(d.Asked, key(AskTellDiverged, "tip", "c1")) {
		t.Fatalf("N7 after retry: %s %v, want tell_worker_diverged@tip@c1", r.ID, d.Asked)
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
		{"L10", Review, with(changes, rejected)},
		{"L10", Review, with(func(in *In) {
			a0(in).Rounds = []Round{{N: 1, Kind: RoundDelta, Head: "c1", Verdict: VerdictChanges}}
			a0(in).Rejects = []Reject{{Head: "c1", Round: 1, Why: "delta claim wrong"}}
		})},
		{"L12", RecapAnswer, with(streakRecap)},
		{"L13", Fix, with(changes)},
		{"L13", Fix, with(changes, rejected, func(in *In) { // round 2 after a round-1 reject asks for changes -> Fix until round 2 is fixed or rejected
			a0(in).Rounds = append(a0(in).Rounds, Round{N: 2, Kind: RoundFull, Head: "c1", Verdict: VerdictChanges})
		})},
		{"L13", Fix, with(rejected, func(in *In) { // send_back / retro cannot match L10 even if a Reject record exists
			a0(in).Rounds = []Round{{N: 1, Kind: "send_back", Head: "c1", Verdict: VerdictChanges}}
		})},
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

// TestLadderOrder checks L7/L8/L9/L10/L12/L13 ordering and neighbour swaps
// (design.md 16, KB-REV-62, KB-CAP-1, KB-CAP-9).
func TestLadderOrder(t *testing.T) {
	wantIDs := []string{"L7", "L8", "L9", "L10", "L12", "L13", "L-"}
	var gotIDs []string
	for _, r := range LadderRows {
		gotIDs = append(gotIDs, r.ID)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("ladder rows = %v, want %v", gotIDs, wantIDs)
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
	// Neighbours L9 and L10: disjoint on landing() (pass) and rejected changes.
	swap910 := slices.Clone(LadderRows)
	swap910[2], swap910[3] = swap910[3], swap910[2]
	if r, _ := FirstMatch(swap910, landing()); r.ID != "L9" {
		t.Fatalf("swapped L9/L10 on landing() matched %s, want L9", r.ID)
	}
	if r, _ := FirstMatch(swap910, with(changes, rejected)); r.ID != "L10" {
		t.Fatalf("swapped L9/L10 on rejected changes matched %s, want L10", r.ID)
	}
	// Neighbours L10 and L12: both hold on a rejected streak round; L10 wins,
	// and swapping makes L12 win.
	rejStreak := with(streakRecap, func(in *In) {
		a0(in).Rejects = []Reject{{Head: "c1", Round: 3, Why: "wrong"}}
	})
	if !LadderRows[3].When(rejStreak) || !LadderRows[4].When(rejStreak) {
		t.Fatal("expected both L10 and L12 conditions to hold on rejected streak")
	}
	if r, d := FirstMatch(LadderRows, rejStreak); r.ID != "L10" || d.Why != Review {
		t.Fatalf("L10 vs L12: got %s (%s), want L10 (%s)", r.ID, d.Why, Review)
	}
	swap1012 := slices.Clone(LadderRows)
	swap1012[3], swap1012[4] = swap1012[4], swap1012[3]
	if r, d := FirstMatch(swap1012, rejStreak); r.ID != "L12" || d.Why != RecapAnswer {
		t.Fatalf("swapped L10/L12: got %s (%s), want L12 (%s)", r.ID, d.Why, RecapAnswer)
	}
	// Neighbours L12 and L13: both hold on streakRecap; L12 wins (Recap),
	// and swapping makes L13 win (Fix).
	sIn := with(streakRecap)
	if !LadderRows[4].When(sIn) || !LadderRows[5].When(sIn) {
		t.Fatal("expected both L12 and L13 conditions to hold on streakRecap")
	}
	if r, d := FirstMatch(LadderRows, sIn); r.ID != "L12" || d.Why != RecapAnswer {
		t.Fatalf("L12 vs L13: got %s (%s), want L12 (%s)", r.ID, d.Why, RecapAnswer)
	}
	swap1213 := slices.Clone(LadderRows)
	swap1213[4], swap1213[5] = swap1213[5], swap1213[4]
	if r, d := FirstMatch(swap1213, sIn); r.ID != "L13" || d.Why != Fix {
		t.Fatalf("swapped L12/L13: got %s (%s), want L13 (%s)", r.ID, d.Why, Fix)
	}
}

// TestLadderL12StreakAndMaxRounds checks KB-CAP-1 and KB-CAP-9:
// rounds 1, 2, 3 with majors hit L12 at round 3 when retro_from_round is 3;
// a pass in between resets the streak; max_rounds hits L12 even without a streak.
func TestLadderL12StreakAndMaxRounds(t *testing.T) {
	mk := func(rounds ...Round) In {
		return with(func(in *In) { a0(in).Rounds = rounds })
	}
	chg := func(n int, head string) Round {
		return Round{N: n, Kind: RoundFull, Head: head, Verdict: VerdictChanges, Majors: 1}
	}
	pass := func(n int, head string) Round {
		return Round{N: n, Kind: RoundFull, Head: head, Verdict: VerdictPass, Majors: 0}
	}
	for name, c := range map[string]struct {
		in   In
		want string
	}{
		"round 1 changes -> fix":                {mk(chg(1, "c1")), Fix},
		"round 2 changes -> fix (below from 3)": {mk(chg(1, "c0"), chg(2, "c1")), Fix},
		"round 3 changes -> recap (streak 2)":   {mk(chg(1, "c0"), chg(2, "c0b"), chg(3, "c1")), RecapAnswer},
		"pass in between resets streak":         {mk(chg(1, "c0"), pass(2, "c0b"), chg(3, "c1")), Fix},
		"streak resumes at round 4":             {mk(chg(1, "c0"), pass(2, "c0b"), chg(3, "c0c"), chg(4, "c1")), RecapAnswer},
		"max_rounds 5 without streak -> recap":  {mk(chg(1, "c0"), pass(2, "c0b"), chg(3, "c0c"), pass(4, "c0d"), chg(5, "c1")), RecapAnswer},
		"send_back does not count toward L12":   {mk(chg(1, "c0"), chg(2, "c0b"), Round{N: 3, Kind: "send_back", Head: "c1", Verdict: VerdictChanges, Majors: 1}), Fix},
	} {
		if got := Ladder(c.in); got != c.want {
			t.Errorf("%s: Ladder = %s, want %s", name, got, c.want)
		}
	}
}

// TestLadderL12NonDefaultStreak: with retro_from_round 2, a streak setting of 3
// opens the recap one round later (round 3) than the default 2 (round 2).
func TestLadderL12NonDefaultStreak(t *testing.T) {
	chg := func(n int, head string) Round {
		return Round{N: n, Kind: RoundFull, Head: head, Verdict: VerdictChanges, Majors: 1}
	}
	mk := func(k int, rounds ...Round) In {
		return with(func(in *In) {
			a0(in).Rounds = rounds
			in.Settings.RetroFromRound, in.Settings.RetroAfterRoundsWithMajors = 2, k
		})
	}
	if got := Ladder(mk(2, chg(1, "c0"), chg(2, "c1"))); got != RecapAnswer {
		t.Fatalf("2 rounds with streak setting 2 = %s, want recap", got)
	}
	if got := Ladder(mk(3, chg(1, "c0"), chg(2, "c1"))); got != Fix {
		t.Fatalf("2 rounds with streak setting 3 = %s, want fix", got)
	}
	if got := Ladder(mk(3, chg(1, "c0"), chg(2, "c0b"), chg(3, "c1"))); got != RecapAnswer {
		t.Fatalf("3 rounds with streak setting 3 = %s, want recap", got)
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
	// A send-back reviewed nothing: no delta starts from it.
	sb := Attempt{Rounds: []Round{{N: 1, Kind: RoundSendBack, Head: "c1", Verdict: VerdictChanges}}}
	if k, s := RoundKind(sb, "c2", RelAhead); k != RoundFull || s != "" {
		t.Fatalf("after a send-back with no review = (%s, %s), want (full, \"\")", k, s)
	}
	sb.Rounds = []Round{
		{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictChanges},
		{N: 2, Kind: RoundSendBack, Head: "c2", Verdict: VerdictChanges},
	}
	if k, s := RoundKind(sb, "c3", RelAhead); k != RoundDelta || s != "c1" {
		t.Fatalf("review c1, send-back c2, ready c3 = (%s, %s), want (delta, c1)", k, s)
	}
}

// TestNoNumberInRules: rules take numbers from Settings or named constants
// (design.md 2.3). 0 as "empty" is allowed.
func TestNoNumberInRules(t *testing.T) {
	fset := token.NewFileSet()
	for _, name := range []string{"rows.go", "ladder.go", "runs.go", "project.go"} {
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

func TestSettingsChanged(t *testing.T) {
	s := DefaultSettings()
	if got := s.Changed(); len(got) != 0 {
		t.Errorf("defaults changed: %v", got)
	}
	s.WorkerRestarts = 5
	if got := s.Changed(); !slices.Equal(got, []string{"worker_restarts 5"}) {
		t.Errorf("changed: %v", got)
	}
	s.ChecksGraceMinutes, s.ReviewRounds = 7, 2
	if got := s.Changed(); !slices.Equal(got, []string{"checks_grace_minutes 7", "worker_restarts 5"}) {
		t.Errorf("changed: %v", got)
	}
	s.ReviewRounds = 1
	if got := s.Changed(); !slices.Equal(got, []string{"checks_grace_minutes 7", "review_rounds 1", "worker_restarts 5"}) {
		t.Errorf("changed with review_rounds 1: %v", got)
	}
	s.EnsureFailures = 4
	if !slices.Contains(s.Changed(), "ensure_failures 4") {
		t.Errorf("changed missing ensure_failures: %v", s.Changed())
	}
}

// TestBoundaryAndSendBack: a plan amend boundary voids earlier rounds, so the
// same head is reviewed again (design.md 3.1); a send-back is a fix for the
// worker that it cannot reject (4.4 N22, 7.2), until a ready moves H.
func TestBoundaryAndSendBack(t *testing.T) {
	amended := with(func(in *In) { a0(in).Boundary = 2 })
	if r, d := FirstMatch(TaskRows, amended); r.ID != "N29" || !slices.Equal(d.Asked, []string{"start_reviewer@3@c1"}) {
		t.Errorf("after amend: %s %v, want N29 start_reviewer@3@c1", r.ID, d.Asked)
	}
	sentBack := with(func(in *In) {
		a0(in).Rounds = append(a0(in).Rounds, Round{N: 3, Kind: RoundSendBack, Head: "c1", Verdict: VerdictChanges, Why: "add a test"})
	})
	if r, d := FirstMatch(TaskRows, sentBack); r.ID != "N22" || !slices.Equal(d.Asked, []string{"tell_worker_findings@3"}) {
		t.Errorf("send-back: %s %v, want N22 tell_worker_findings@3", r.ID, d.Asked)
	}
	if RejectableRound(sentBack.Task.Attempts[0], "c1") != nil {
		t.Error("a send-back must not be rejectable")
	}
	working := sentBack
	working.Facts.Tip, working.Facts.TipRel = "c2", RelAhead
	if r, _ := FirstMatch(TaskRows, working); r.ID != "N22" {
		t.Errorf("send-back, worker committed without ready: %s, want N22", r.ID)
	}
	readyAgain := with(func(in *In) {
		a0(in).Rounds = append(a0(in).Rounds, Round{N: 3, Kind: RoundSendBack, Head: "c1", Verdict: VerdictChanges})
		a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"})
	})
	if got := Ladder(readyAgain); got != Review {
		t.Errorf("after ready again: ladder %s, want review", got)
	}
}

// TestCoversMergeOnly: a record covers H when H was reached from it by
// merge_only moves only; a ready with changes in between breaks the chain.
func TestCoversMergeOnly(t *testing.T) {
	a := Attempt{BaseCommit: "b0", Ready: []Ready{{Head: "c1"}}, MergeOnly: []Advance{{From: "c1", To: "m1"}, {From: "m1", To: "m2"}}}
	if h := H(a); h != "m2" {
		t.Fatalf("H = %s, want m2", h)
	}
	for _, c := range []struct {
		x, h string
		want bool
	}{{"c1", "m2", true}, {"m1", "m2", true}, {"m2", "m2", true}, {"c1", "c1", true}, {"b0", "m2", false}, {"m2", "c1", false}, {"c0", "m2", false}} {
		if got := Covers(a, c.x, c.h); got != c.want {
			t.Errorf("Covers(%s, %s) = %v, want %v", c.x, c.h, got, c.want)
		}
	}
	a.Ready = append(a.Ready, Ready{Head: "c3"}) // a later ready with changes: H is c3, and m2 does not cover it
	if H(a) != "c3" || Covers(a, "m2", "c3") || Covers(a, "c1", "c3") {
		t.Fatalf("after ready c3: H = %s", H(a))
	}
}

// TestSizeRecapAllowed: continue is offered while fewer than
// continues_per_task continues were decided.
func TestSizeRecapAllowed(t *testing.T) {
	s, task := DefaultSettings(), Task{}
	if got := SizeRecapAllowed(task, s); got[0] != ChoiceContinue {
		t.Fatalf("first size recap allowed %v, want continue first", got)
	}
	task.Recaps = []Recap{{Trigger: TriggerSize, Decided: &RecapDecision{Choice: ChoiceContinue}}}
	if got := SizeRecapAllowed(task, s); slices.Contains(got, ChoiceContinue) {
		t.Fatalf("after one continue (default 1) allowed %v", got)
	}
}

// TestN12LinesTimeout: a changed-lines read that timed out matches N12 even
// when Facts.Lines is 0 (design.md 3, last_tip).
func TestN12LinesTimeout(t *testing.T) {
	in := In{
		Task:     Task{Attempts: []Attempt{{BaseCommit: "b0", SizeCap: 100}}},
		Settings: DefaultSettings(),
		Facts:    Facts{Tip: "c1", TipRel: RelAhead, Lines: 0, LinesTimeout: true},
	}
	r, d := FirstMatch(TaskRows, in)
	if r.ID != "N12" || len(d.Asked) != 1 || d.Asked[0] != "open_size_recap@0" || !strings.Contains(d.Why, "timed out") {
		t.Fatalf("FirstMatch = (%s, %+v), want N12 opening size recap with timeout why", r.ID, d)
	}
}

// TestN9N10Why checks that N9 names its waiting and dropped dependencies and
// N10 names the slot wait.
func TestN9N10Why(t *testing.T) {
	if _, d := FirstMatch(TaskRows, with(noAttempts, waitDep, dropDep)); !strings.Contains(d.Why, "T1") || !strings.Contains(d.Why, "dropped T2") {
		t.Errorf("N9 why = %q, want T1 and dropped T2", d.Why)
	}
	if _, d := FirstMatch(TaskRows, with(noAttempts, noSlotF)); d.Why != "no slot is free" {
		t.Errorf("N10 why (slot) = %q, want no slot is free", d.Why)
	}
}

// TestCIRowsN33N34N35 covers N33 (red: rerun below flake_reruns, else tell
// worker once per failing run, KB-GIT-14), N34 (external cancel rerun vs
// timeout wake once) and N35 (absent past grace: dispatch once, wait within
// grace, wake once naming ways out; never green).
func TestCIRowsN33N34N35(t *testing.T) {
	grace := time.Duration(DefaultSettings().ChecksGraceMinutes) * time.Minute
	redRun := func(run, job string) func(*In) {
		return func(in *In) { in.Facts.PR.Checks, in.Facts.PR.Failed = ChecksRed, []Failure{{Run: run, Job: job}} }
	}
	// N33: flake_reruns=1 -> rerun once; stale same job waits; new failing job tells worker once; repeat ready at same H does not re-tell (KB-GIT-14).
	in := with(redRun("42", "100"))
	r, d := FirstMatch(TaskRows, in)
	if r.ID != "N33" || !slices.Equal(d.Asked, []string{"rerun_ci@42@100@c1"}) {
		t.Fatalf("N33 first red = %s %v", r.ID, d.Asked)
	}
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: "rerun_ci@42@100@c1", Result: "done", At: now})
	if r, d := FirstMatch(TaskRows, in); r.ID != "N33" || len(d.Asked) != 0 {
		t.Fatalf("N33 stale same job after rerun = %s %v, want wait", r.ID, d.Asked)
	}
	in.Facts.PR.Failed = []Failure{{Run: "42", Job: "200"}} // rerun finished red again
	if r, d := FirstMatch(TaskRows, in); r.ID != "N33" || !slices.Equal(d.Asked, []string{"tell_worker_ci@42@c1"}) {
		t.Fatalf("N33 red again = %s %v", r.ID, d.Asked)
	}
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: "tell_worker_ci@42@c1", Result: "done", At: now})
	a0(&in).Ready = append(a0(&in).Ready, Ready{Head: "c1"}) // ready again at same commit (KB-GIT-14)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N33" || len(d.Asked) != 0 {
		t.Fatalf("N33 KB-GIT-14 repeat = %s %v, want nil", r.ID, d.Asked)
	}
	// flake_reruns=0 skips rerun and tells worker at once.
	noRerun := with(redRun("42", "100"), func(in *In) { in.Settings.FlakeReruns = 0 })
	if r, d := FirstMatch(TaskRows, noRerun); r.ID != "N33" || !slices.Equal(d.Asked, []string{"tell_worker_ci@42@c1"}) {
		t.Fatalf("N33 flake_reruns=0 = %s %v", r.ID, d.Asked)
	}

	// N34: external cancel reruns once, then wakes coordinator once; timeout wakes once immediately.
	ext := with(func(in *In) {
		in.Facts.PR.Checks, in.Facts.PR.Failed = ChecksStoppedExternal, []Failure{{Run: "55", Job: "300"}}
	})
	if r, d := FirstMatch(TaskRows, ext); r.ID != "N34" || !slices.Equal(d.Asked, []string{"rerun_ci@55@300@c1"}) {
		t.Fatalf("N34 external first = %s %v", r.ID, d.Asked)
	}
	a0(&ext).Acts = append(a0(&ext).Acts, Act{Key: "rerun_ci@55@300@c1", Result: "done", At: now})
	ext.Facts.PR.Failed = []Failure{{Run: "55", Job: "301"}}
	if r, d := FirstMatch(TaskRows, ext); r.ID != "N34" || !slices.Equal(d.Asked, []string{"wake_coordinator@checks_stopped@external@c1"}) {
		t.Fatalf("N34 external after rerun = %s %v", r.ID, d.Asked)
	}
	ext.Facts.PR.Failed, ext.Facts.PR.Running = []Failure{{Run: "57", Job: "500"}}, true
	if r, d := FirstMatch(TaskRows, ext); r.ID != "N34" || len(d.Asked) != 0 {
		t.Fatalf("N34 external while a check runs = %s %v, want wait", r.ID, d.Asked)
	}
	to := with(func(in *In) { in.Facts.PR.Checks = ChecksStoppedTimeout })
	if r, d := FirstMatch(TaskRows, to); r.ID != "N34" || !slices.Equal(d.Asked, []string{"wake_coordinator@checks_stopped@limit@c1"}) {
		t.Fatalf("N34 timeout = %s %v", r.ID, d.Asked)
	}
	// N34: a cancel whose cause is unread (stopped_unknown) is a time limit:
	// wake once, never re-run, even with re-runs left and a run ID to re-run.
	unk := with(func(in *In) {
		in.Facts.PR.Checks, in.Facts.PR.Failed = ChecksStoppedUnknown, []Failure{{Run: "56", Job: "400"}}
		in.Settings.FlakeReruns = 3
	})
	if r, d := FirstMatch(TaskRows, unk); r.ID != "N34" || !slices.Equal(d.Asked, []string{"wake_coordinator@checks_stopped@limit@c1"}) {
		t.Fatalf("N34 unknown = %s %v", r.ID, d.Asked)
	}
	a0(&unk).Acts = append(a0(&unk).Acts, Act{Key: "wake_coordinator@checks_stopped@limit@c1", Result: "done", At: now})
	if r, d := FirstMatch(TaskRows, unk); r.ID != "N34" || len(d.Asked) != 0 {
		t.Fatalf("N34 unknown after wake = %s %v, want nothing", r.ID, d.Asked)
	}

	// N35: absent past grace -> dispatch once -> wait within grace -> wake once naming ways out; skipped dispatch wakes immediately.
	// Absent only because the workflow runs were unread: never dispatch, N43 wakes.
	unread := with(func(in *In) {
		in.Facts.PR.Checks, in.Facts.PR.ChecksUnread = ChecksAbsent, true
		in.Now = now.Add(grace)
	})
	if r, d := FirstMatch(TaskRows, unread); r.ID != "N43" || slices.ContainsFunc(d.Asked, func(k string) bool { return strings.HasPrefix(k, AskDispatchCI) }) {
		t.Fatalf("N35 on unread workflow runs = %s %v, want N43 and no dispatch", r.ID, d.Asked)
	}
	abs := with(func(in *In) { in.Facts.PR.Checks = ChecksAbsent; in.Now = now.Add(grace) })
	if r, d := FirstMatch(TaskRows, abs); r.ID != "N35" || !slices.Equal(d.Asked, []string{"dispatch_ci@c1"}) {
		t.Fatalf("N35 dispatch = %s %v", r.ID, d.Asked)
	}
	a0(&abs).Acts = append(a0(&abs).Acts, Act{Key: "dispatch_ci@c1", Result: "done", At: abs.Now})
	abs.Now = abs.Now.Add(grace - time.Second)
	if r, d := FirstMatch(TaskRows, abs); r.ID != "N35" || len(d.Asked) != 0 {
		t.Fatalf("N35 within dispatch grace = %s %v, want wait", r.ID, d.Asked)
	}
	abs.Now = abs.Now.Add(time.Second)
	r, d = FirstMatch(TaskRows, abs)
	if r.ID != "N35" || !slices.Equal(d.Asked, []string{"wake_coordinator@no_checks@c1"}) ||
		!strings.Contains(d.Why, "override") || !strings.Contains(d.Why, "drop") {
		t.Fatalf("N35 wake = %s %v (%q)", r.ID, d.Asked, d.Why)
	}
	a0(&abs).Acts = append(a0(&abs).Acts, Act{Key: "wake_coordinator@no_checks@c1", Result: "done", At: abs.Now})
	if r, d := FirstMatch(TaskRows, abs); r.ID != "N35" || len(d.Asked) != 0 {
		t.Fatalf("N35 wake repeat = %s %v, want nil", r.ID, d.Asked)
	}
	// Skipped dispatch (no workflow_dispatch) skips the grace wait and wakes immediately.
	noWF := with(func(in *In) {
		in.Facts.PR.Checks = ChecksAbsent
		in.Now = now.Add(grace)
		a0(in).Acts = []Act{{Key: "dispatch_ci@c1", Result: "skipped", At: in.Now}}
	})
	if r, d := FirstMatch(TaskRows, noWF); r.ID != "N35" || !slices.Equal(d.Asked, []string{"wake_coordinator@no_checks@c1"}) {
		t.Fatalf("N35 skipped dispatch = %s %v", r.ID, d.Asked)
	}
}

func TestFailingRuns(t *testing.T) {
	checks := []Check{
		{Kind: "CheckRun", Name: "ok", Status: "COMPLETED", Conclusion: "SUCCESS", Actions: true, DetailsURL: "https://github.com/o/r/actions/runs/111/job/1"},
		{Kind: "CheckRun", Name: "lint", Status: "COMPLETED", Conclusion: "FAILURE", Actions: true, DetailsURL: "https://github.com/O/R/actions/runs/222/job/20"},
		{Kind: "CheckRun", Name: "test", Status: "COMPLETED", Conclusion: "FAILURE", Actions: true, DetailsURL: "https://github.com/o/r/actions/runs/222/job/30"},
		{Kind: "CheckRun", Name: "ext", Status: "COMPLETED", Conclusion: "FAILURE", DetailsURL: "https://github.com/o/r/actions/runs/333/job/40"},
		{Kind: "CheckRun", Name: "fork", Status: "COMPLETED", Conclusion: "FAILURE", Actions: true, DetailsURL: "https://github.com/x/r/actions/runs/444/job/50"},
		{Kind: "CheckRun", Name: "stale", Status: "COMPLETED", Conclusion: "FAILURE", Actions: true, DetailsURL: "https://github.com/o/r/actions/runs/555/job/60"},
	}
	fails, running := FailingRuns(checks, ChecksRed, "o/r", map[string]bool{"222": true, "333": true, "444": true})
	want := []Failure{{Run: "222", Job: "20", Name: "lint"}, {Run: "222", Job: "30", Name: "test"}, {Job: "40", Name: "ext"}, {Job: "50", Name: "fork"}, {Job: "60", Name: "stale"}}
	if !slices.Equal(fails, want) || running {
		t.Fatalf("FailingRuns(red) = %+v, %t; want %+v, false", fails, running, want)
	}
	for _, c := range []Check{{Kind: "CheckRun", Name: "e2e", Status: "IN_PROGRESS"}, {Kind: "StatusContext", Context: "ci/x", State: "EXPECTED"}} {
		if f, r := FailingRuns(append(checks[:1:1], c), ChecksGreen, "o/r", nil); f != nil || !r {
			t.Fatalf("FailingRuns(green, %s) = %+v, %t; want none, running", c.Name+c.Context, f, r)
		}
	}
}

// TestN13N32N37 covers N13 (distinct failing commits within the last hour and
// since the last recap decision, skipped when ladder says passed), N32 (conflict
// tells worker once per H) and N37 (review_required wakes coordinator once per H).
func TestN13N32N37(t *testing.T) {
	// N13: 3 distinct commits in the last hour -> open_ci_recap@0; duplicate head counts once; old fail excluded.
	dup := with(noRounds, func(in *In) {
		a0(in).CIFails = []CIFail{
			{Head: "f1", At: now.Add(-30 * time.Minute)},
			{Head: "f1", At: now.Add(-25 * time.Minute)},
			{Head: "f2", At: now.Add(-10 * time.Minute)},
			{Head: "f0", At: now.Add(-time.Hour)}, // outside the last hour
		}
	})
	if r, _ := FirstMatch(TaskRows, dup); r.ID != "N29" {
		t.Fatalf("2 distinct recent fails + 1 dup + 1 old = %s, want N29", r.ID)
	}
	// Recap decision resets the window.
	reset := with(noRounds, ciFailed3, func(in *In) {
		in.Task.Recaps = []Recap{{Trigger: TriggerCI, Decided: &RecapDecision{Choice: ChoiceContinue, At: now.Add(-15 * time.Minute)}}}
	})
	if r, _ := FirstMatch(TaskRows, reset); r.ID != "N29" {
		t.Fatalf("after recap decision resetting 2 of 3 fails = %s, want N29", r.ID)
	}
	// Ladder == Passed does not fire N13.
	if r, _ := FirstMatch(TaskRows, with(ciFailed3)); r.ID != "N42" {
		t.Fatalf("ladder passed with 3 CI fails = %s, want N42", r.ID)
	}
	// N32: conflict tells worker once per H, not again on repeat pass.
	cIn := with(conflict)
	if r, d := FirstMatch(TaskRows, cIn); r.ID != "N32" || !slices.Equal(d.Asked, []string{"tell_worker_conflict@c1"}) {
		t.Fatalf("N32 first = %s %v", r.ID, d.Asked)
	}
	a0(&cIn).Acts = append(a0(&cIn).Acts, Act{Key: "tell_worker_conflict@c1", Result: "done", At: now})
	if r, d := FirstMatch(TaskRows, cIn); r.ID != "N32" || len(d.Asked) != 0 {
		t.Fatalf("N32 repeat = %s %v, want nil", r.ID, d.Asked)
	}
	// N37: review_required wakes once per H, not again on repeat pass.
	rrIn := with(reviewReq)
	if r, d := FirstMatch(TaskRows, rrIn); r.ID != "N37" || !slices.Equal(d.Asked, []string{"wake_coordinator@review_required@c1"}) {
		t.Fatalf("N37 first = %s %v", r.ID, d.Asked)
	}
	a0(&rrIn).Acts = append(a0(&rrIn).Acts, Act{Key: "wake_coordinator@review_required@c1", Result: "done", At: now})
	if r, d := FirstMatch(TaskRows, rrIn); r.ID != "N37" || len(d.Asked) != 0 {
		t.Fatalf("N37 repeat = %s %v, want nil", r.ID, d.Asked)
	}
}

// TestMergeOnlyTable covers every row M1..M6 of MergeOnlyRows (design.md 3.1, 9.4).
func TestMergeOnlyTable(t *testing.T) {
	clean := MergeCommit{Parents: 2, SecondInBase: true, TreeMatches: true}
	cases := []struct {
		row  string
		want bool
		in   MergeOnlyIn
	}{
		{"M1", false, MergeOnlyIn{Rel: RelSame, Commits: []MergeCommit{clean}}},
		{"M1", false, MergeOnlyIn{Rel: RelDiverged, Commits: []MergeCommit{clean}}},
		{"M2", false, MergeOnlyIn{Rel: RelAhead}},
		{"M3", false, MergeOnlyIn{Rel: RelAhead, Commits: []MergeCommit{clean, {Parents: 1, SecondInBase: true, TreeMatches: true}}}},
		{"M3", false, MergeOnlyIn{Rel: RelAhead, Commits: []MergeCommit{{Parents: 3, SecondInBase: true, TreeMatches: true}}}},
		{"M4", false, MergeOnlyIn{Rel: RelAhead, Commits: []MergeCommit{{Parents: 2, SecondInBase: false, TreeMatches: true}}}},
		{"M5", false, MergeOnlyIn{Rel: RelAhead, Commits: []MergeCommit{{Parents: 2, SecondInBase: true, TreeMatches: false}}}},
		{"M6", true, MergeOnlyIn{Rel: RelAhead, Commits: []MergeCommit{clean}}},
		{"M6", true, MergeOnlyIn{Rel: RelAhead, Commits: []MergeCommit{clean, clean}}},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		r, _ := FirstMatch(MergeOnlyRows, c.in)
		if r.ID != c.row || IsMergeOnly(c.in) != c.want {
			t.Errorf("MergeOnlyRows(%+v) = %s (%v), want %s (%v)", c.in, r.ID, IsMergeOnly(c.in), c.row, c.want)
		}
		covered[r.ID] = true
	}
	for _, r := range MergeOnlyRows {
		if !covered[r.ID] {
			t.Errorf("MergeOnlyRows row %s has no case", r.ID)
		}
	}
}

// TestN40BehindAndKBMRG6 covers N40 (KB-MRG-43: update once, wait within grace,
// failed try past grace, BehindUnknown waits at N36, not behind merges at N42),
// merge_only PR head moving H, and KB-MRG-6 (stray worker wake with no new
// commit never undoes a pass).
func TestN40BehindAndKBMRG6(t *testing.T) {
	grace := time.Duration(DefaultSettings().ChecksGraceMinutes) * time.Minute
	in := with(behind)
	r, d := FirstMatch(TaskRows, in)
	if r.ID != "N40" || !slices.Equal(d.Asked, []string{"update_branch@c1"}) {
		t.Fatalf("N40 first = %s %v", r.ID, d.Asked)
	}
	// Asked within checks_grace_minutes -> wait.
	a0(&in).Acts = append(a0(&in).Acts, Act{Key: "update_branch@c1", Result: "done", At: now})
	in.Now = now.Add(grace - time.Second)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N40" || len(d.Asked) != 0 {
		t.Fatalf("N40 within grace = %s %v, want wait", r.ID, d.Asked)
	}
	// Past checks_grace_minutes -> failed try.
	in.Now = now.Add(grace)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N40" || !slices.Equal(d.Asked, []string{"fail_update_branch@c1"}) {
		t.Fatalf("N40 past grace = %s %v, want fail_update_branch@c1", r.ID, d.Asked)
	}
	// Once marked failed at in.Now, N40 waits during back-off, then retries past back-off.
	a0(&in).Acts[0] = Act{Key: "update_branch@c1", Result: "failed", Class: ClassTimeout, At: in.Now}
	if r, d := FirstMatch(TaskRows, in); r.ID != "N40" || len(d.Asked) != 0 {
		t.Fatalf("N40 failed within backoff = %s %v, want wait", r.ID, d.Asked)
	}
	in.Now = in.Now.Add(time.Duration(DefaultSettings().EnsureBackoffMinutes) * time.Minute)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N40" || !slices.Equal(d.Asked, []string{"update_branch@c1"}) {
		t.Fatalf("N40 past backoff = %s %v, want retry update_branch@c1", r.ID, d.Asked)
	}
	// BehindUnknown always holds at N36 (never merges, never asks update_branch);
	// past checks_grace_minutes it wakes the coordinator once and keeps holding.
	bu := with(behindUnkn)
	if r, d := FirstMatch(TaskRows, bu); r.ID != "N36" || len(d.Asked) != 0 {
		t.Fatalf("BehindUnknown in grace = %s %v, want N36 wait", r.ID, d.Asked)
	}
	bu.Now = now.Add(grace)
	if r, d := FirstMatch(TaskRows, bu); r.ID != "N36" || !slices.Equal(d.Asked, []string{"wake_coordinator@behind_unknown@c1"}) {
		t.Fatalf("BehindUnknown past grace = %s %v, want N36 wake once", r.ID, d.Asked)
	}
	a0(&bu).Acts = append(a0(&bu).Acts, Act{Key: "wake_coordinator@behind_unknown@c1", Result: "done", At: bu.Now})
	if r, d := FirstMatch(TaskRows, bu); r.ID != "N36" || len(d.Asked) != 0 {
		t.Fatalf("BehindUnknown after wake = %s %v, want N36 hold", r.ID, d.Asked)
	}
	// KB-MRG-6: a wake with no new commit holds at N39 while working, then merges at N42 when idle.
	w := with(workingF)
	if r, d := FirstMatch(TaskRows, w); r.ID != "N39" || len(d.Asked) != 0 {
		t.Fatalf("KB-MRG-6 working = %s %v, want N39 wait", r.ID, d.Asked)
	}
	w.Facts.Worker.State = RunIdle
	if r, d := FirstMatch(TaskRows, w); r.ID != "N42" || !slices.Equal(d.Asked, []string{"record_accept@c1", "merge@c1"}) {
		t.Fatalf("KB-MRG-6 idle again = %s %v, want N42 merge", r.ID, d.Asked)
	}
}

// TestN13CIFailsScopedToAttempt verifies m4: a new attempt does not inherit
// failing CI commits from an earlier closed attempt.
func TestN13CIFailsScopedToAttempt(t *testing.T) {
	in := with(noRounds, ciFailed3)
	if r, _ := FirstMatch(TaskRows, in); r.ID != "N13" {
		t.Fatalf("first attempt with 3 fails = %s, want N13", r.ID)
	}
	a0(&in).Closed = "new attempt"
	in.Task.Attempts = append(in.Task.Attempts, Attempt{BaseCommit: "b0", Ready: []Ready{{Head: "c2"}}})
	in.Facts.Tip, in.Facts.PR.Head = "c2", "c2"
	if got := RecentCIFails(in.Task, in.Now); len(got) != 0 {
		t.Fatalf("new attempt RecentCIFails = %+v, want none", got)
	}
	if r, _ := FirstMatch(TaskRows, in); r.ID != "N29" {
		t.Fatalf("new attempt after closed failing attempt = %s, want N29", r.ID)
	}
}

// TestN7DivergedAndForeignPush covers N7: diverged tip, force-pushed (diverged)
// PR head, foreign commit on PR head, and uncovered tip behind H -> tell worker
// once per H, wait until the worker's turn ends after the tell, wake coordinator
// once per H naming say+retry; merge_only unknown waits without telling or waking.
func TestN7DivergedAndForeignPush(t *testing.T) {
	prDiv := func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "f9", RelDiverged }
	prForeign := func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel, in.Facts.PR.HeadMergeOnly = "f8", RelAhead, false }
	tipBehindUncovered := func(in *In) { in.Facts.Tip, in.Facts.TipRel = "b0", RelBehind }

	for name, tc := range map[string]struct {
		mod  func(*In)
		kind string
	}{
		"tip diverged":         {diverged, "tip"},
		"PR head diverged":     {prDiv, "pr_diverged"},
		"PR head foreign push": {prForeign, "pr_foreign"},
		"tip behind uncovered": {tipBehindUncovered, "tip"},
	} {
		in := with(tc.mod)
		tellKey := "tell_worker_diverged@" + tc.kind + "@c1"
		wakeKey := "wake_coordinator@diverged@" + tc.kind + "@c1"
		r, d := FirstMatch(TaskRows, in)
		if r.ID != "N7" || !slices.Equal(d.Asked, []string{tellKey}) {
			t.Fatalf("%s first = %s %v, want N7 [%s]", name, r.ID, d.Asked, tellKey)
		}
		// Told once; still idle within checks_grace_minutes before worker has had a turn -> wait (no wake).
		a0(&in).Acts = append(a0(&in).Acts, Act{Key: tellKey, Result: "done", At: now})
		if r, d := FirstMatch(TaskRows, in); r.ID != "N7" || len(d.Asked) != 0 {
			t.Fatalf("%s idle before turn = %s %v, want N7 wait", name, r.ID, d.Asked)
		}
		// Still working even past checks_grace_minutes -> wait.
		pastGrace := in
		pastGrace.Now = now.Add(checksGrace(in))
		pastGrace.Facts.Worker.State = RunWorking
		if r, d := FirstMatch(TaskRows, pastGrace); r.ID != "N7" || len(d.Asked) != 0 {
			t.Fatalf("%s working past grace = %s %v, want N7 wait", name, r.ID, d.Asked)
		}
		// Never seen working, tell older than checks_grace_minutes -> wake once.
		pastGrace.Facts.Worker.State = RunIdle
		if r, d := FirstMatch(TaskRows, pastGrace); r.ID != "N7" || !slices.Equal(d.Asked, []string{wakeKey}) {
			t.Fatalf("%s idle past grace without turn = %s %v, want N7 [%s]", name, r.ID, d.Asked, wakeKey)
		}
		// While worker is working -> wait.
		in.Facts.Worker.State = RunWorking
		if r, d := FirstMatch(TaskRows, in); r.ID != "N7" || len(d.Asked) != 0 {
			t.Fatalf("%s while working = %s %v, want N7 wait", name, r.ID, d.Asked)
		}
		// Worker turn ends after tell and N7 still holds -> wake coordinator once naming say and retry.
		in.Facts.Worker.State = RunIdle
		in.Facts.Worker.LastTurnEnd = now.Add(time.Second)
		r, d = FirstMatch(TaskRows, in)
		if r.ID != "N7" || !slices.Equal(d.Asked, []string{wakeKey}) || !strings.Contains(d.Why, "say") || !strings.Contains(d.Why, "retry") {
			t.Fatalf("%s after turn end = %s %v (%q), want N7 [%s] naming say+retry", name, r.ID, d.Asked, d.Why, wakeKey)
		}
		// Repeat pass does not wake again.
		a0(&in).Acts = append(a0(&in).Acts, Act{Key: wakeKey, Result: "done", At: now})
		if r, d := FirstMatch(TaskRows, in); r.ID != "N7" || len(d.Asked) != 0 {
			t.Fatalf("%s repeat after wake = %s %v, want N7 nil", name, r.ID, d.Asked)
		}
	}

	// HeadMergeOnlyUnknown waits at N7 within checks_grace_minutes, then wakes once past grace (never tells or merges).
	unk := with(func(in *In) {
		in.Facts.PR.Head, in.Facts.PR.HeadRel, in.Facts.PR.HeadMergeOnlyUnknown = "m2", RelAhead, true
	})
	if r, d := FirstMatch(TaskRows, unk); r.ID != "N7" || len(d.Asked) != 0 {
		t.Fatalf("HeadMergeOnlyUnknown within grace = %s %v, want N7 wait (no asks)", r.ID, d.Asked)
	}
	unk.Now = now.Add(checksGrace(unk))
	unkWake := "wake_coordinator@merge_only_unknown@c1"
	if r, d := FirstMatch(TaskRows, unk); r.ID != "N7" || !slices.Equal(d.Asked, []string{unkWake}) {
		t.Fatalf("HeadMergeOnlyUnknown past grace = %s %v, want N7 [%s]", r.ID, d.Asked, unkWake)
	}
	a0(&unk).Acts = append(a0(&unk).Acts, Act{Key: unkWake, Result: "done", At: unk.Now})
	if r, d := FirstMatch(TaskRows, unk); r.ID != "N7" || len(d.Asked) != 0 {
		t.Fatalf("HeadMergeOnlyUnknown repeat after wake = %s %v, want N7 nil", r.ID, d.Asked)
	}
}

func TestPRHeadDivergedOrForeign(t *testing.T) {
	if !PRHeadDivergedOrForeign(with(func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "f9", RelDiverged })) {
		t.Fatal("diverged open answered PR = false, want true")
	}
	if !PRHeadDivergedOrForeign(with(func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "f8", RelAhead })) {
		t.Fatal("foreign ahead open answered PR = false, want true")
	}
	if PRHeadDivergedOrForeign(with(func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel, in.Facts.PR.HeadMergeOnly = "m2", RelAhead, true })) {
		t.Fatal("merge_only ahead PR = true, want false")
	}
	if PRHeadDivergedOrForeign(with(func(in *In) {
		in.Facts.PR.Head, in.Facts.PR.HeadRel, in.Facts.PR.HeadMergeOnlyUnknown = "m2", RelAhead, true
	})) {
		t.Fatal("merge_only unknown PR = true, want false")
	}
	if PRHeadDivergedOrForeign(with(diverged)) {
		t.Fatal("tip-only diverged = true, want false")
	}
}

// TestPRAdoptTable covers every row P2..P4 of PRAdoptRows (design.md 9.4, KB-GIT-2).
func TestPRAdoptTable(t *testing.T) {
	own := PRCandidate{Number: 7, State: PROpen, Base: "main"}
	cases := []struct {
		name, row, action string
		num               int
		in                PRAdoptIn
	}{
		{"no PR", "P2", PRCreate, 0, PRAdoptIn{Base: "main"}},
		{"own open PR", "P3", PRAdopt, 7, PRAdoptIn{Base: "main", PRs: []PRCandidate{own}}},
		{"fork PR", "P4", PRRefuse, 0, PRAdoptIn{Base: "main", PRs: []PRCandidate{{Number: 8, State: PROpen, Base: "main", IsCrossRepository: true}}}},
		{"other base", "P4", PRRefuse, 0, PRAdoptIn{Base: "main", PRs: []PRCandidate{{Number: 9, State: PROpen, Base: "release"}}}},
		{"closed PR", "P4", PRRefuse, 0, PRAdoptIn{Base: "main", PRs: []PRCandidate{{Number: 11, State: PRClosed, Base: "main"}}}},
		{"merged PR", "P4", PRRefuse, 0, PRAdoptIn{Base: "main", PRs: []PRCandidate{{Number: 12, State: PRMerged, Base: "main"}}}},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		r, _ := FirstMatch(PRAdoptRows, c.in)
		act, num, why := AdoptPR(c.in)
		if r.ID != c.row || act != c.action || num != c.num {
			t.Errorf("%s: (%s, %s, %d, %q), want (%s, %s, %d)", c.name, r.ID, act, num, why, c.row, c.action, c.num)
		}
		covered[r.ID] = true
	}
	for _, r := range PRAdoptRows {
		if !covered[r.ID] {
			t.Errorf("PRAdoptRows row %s has no case", r.ID)
		}
	}
}

// TestN36CIStalledWake checks that checks pending past ci_stalled_minutes wake
// the coordinator once per H, not again at the same H, and do not wake under
// the limit or when GitHub is unanswered/unavailable.
func TestN36CIStalledWake(t *testing.T) {
	limit := time.Duration(DefaultSettings().CIStalledMinutes) * time.Minute
	pendingSpell := func(in *In) {
		pending(in)
		a0(in).PendingChecks = &PendingChecks{Head: "c1", Since: now}
	}
	under := with(pendingSpell, func(in *In) { in.Now = now.Add(limit - time.Second) })
	if r, d := FirstMatch(TaskRows, under); r.ID != "N36" || len(d.Asked) != 0 {
		t.Fatalf("pending under ci_stalled_minutes: %s %v, want N36 nil", r.ID, d.Asked)
	}
	stalled := with(pendingSpell, func(in *In) { in.Now = now.Add(limit) })
	wantWake := "wake_coordinator@ci_stalled@c1"
	if r, d := FirstMatch(TaskRows, stalled); r.ID != "N36" || !slices.Equal(d.Asked, []string{wantWake}) {
		t.Fatalf("pending at ci_stalled_minutes: %s %v, want N36 [%s]", r.ID, d.Asked, wantWake)
	}
	// Repeat at same H after wake recorded -> no second wake.
	a0(&stalled).Acts = append(a0(&stalled).Acts, Act{Key: wantWake, Result: "done", At: stalled.Now})
	if r, d := FirstMatch(TaskRows, stalled); r.ID != "N36" || len(d.Asked) != 0 {
		t.Fatalf("pending repeat at same H: %s %v, want N36 nil", r.ID, d.Asked)
	}
	// New H past ci_stalled_minutes -> wakes once for the new H.
	newH := with(pending, func(in *In) {
		a0(in).Acts = []Act{{Key: wantWake, Result: "done", At: now}}
		a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"})
		a0(in).Rounds = append(a0(in).Rounds,
			Round{N: 3, Kind: RoundFull, Head: "c2", Verdict: VerdictPass},
			Round{N: 4, Kind: RoundFull, Head: "c2", Verdict: VerdictPass},
		)
		in.Facts.Tip, in.Facts.PR.Head = "c2", "c2"
		a0(in).PendingChecks = &PendingChecks{Head: "c2", Since: now}
		in.Settings.CIStalledMinutes = CIStalledMin
		in.Now = now.Add(time.Duration(CIStalledMin) * time.Minute)
	})
	if r, d := FirstMatch(TaskRows, newH); r.ID != "N36" || !slices.Equal(d.Asked, []string{"wake_coordinator@ci_stalled@c2"}) {
		t.Fatalf("pending at new H past ci_stalled_minutes: %s %v, want N36 wake for c2", r.ID, d.Asked)
	}
	// Unanswered or unavailable GitHub never fires ci_stalled.
	for name, f := range map[string]func(*In){
		"unanswered":    noAnswer,
		"githubUnavail": func(in *In) { in.Facts.GitHubUnavailable = true },
	} {
		in := with(pendingSpell, func(in *In) { in.Now = now.Add(limit) }, f)
		if r, d := FirstMatch(TaskRows, in); r.ID != "N36" || len(d.Asked) != 0 {
			t.Errorf("%s: %s %v, want N36 nil", name, r.ID, d.Asked)
		}
	}
}

// TestN29UnansweredPRHoldsReviewer verifies that an unanswered PR snapshot
// holds N29 from starting or resuming a stopped reviewer until GitHub answers.
func TestN29UnansweredPRHoldsReviewer(t *testing.T) {
	stoppedDiv := with(noRounds, reviewEnded, noAnswer, func(in *In) {
		in.Facts.PR.Head, in.Facts.PR.HeadRel = "ext", RelDiverged
		a0(in).Review = &ReviewRun{Round: 1, Head: "c1", Run: &RunRecord{State: PhaseEnded, Result: EndStopped}}
	})
	if r, d := FirstMatch(TaskRows, stoppedDiv); r.ID != "N29" || len(d.Asked) != 0 {
		t.Fatalf("unanswered PR with stopped reviewer = %s %v, want N29 nil", r.ID, d.Asked)
	}
	// Fresh unanswered PR open at H also holds N29 start.
	freshUnanswered := with(noRounds, noAnswer)
	if r, d := FirstMatch(TaskRows, freshUnanswered); r.ID != "N29" || len(d.Asked) != 0 {
		t.Fatalf("unanswered PR with fresh reviewer = %s %v, want N29 nil", r.ID, d.Asked)
	}
	// Answered PR still diverged -> N7; answered PR back at H -> N29 resumes uncounted.
	stoppedDiv.Facts.PR.Answered = true
	if r, _ := FirstMatch(TaskRows, stoppedDiv); r.ID != "N7" {
		t.Fatalf("answered still-diverged PR = %s, want N7", r.ID)
	}
	stoppedDiv.Facts.PR.Head, stoppedDiv.Facts.PR.HeadRel = "c1", RelSame
	if r, d := FirstMatch(TaskRows, stoppedDiv); r.ID != "N29" || !slices.Equal(d.Asked, []string{"start_reviewer@1@resume@c1"}) {
		t.Fatalf("answered PR back at H = %s %v, want N29 [start_reviewer@1@resume@c1]", r.ID, d.Asked)
	}
	// Exhausted settled start_reviewer still surfaces as N5 even while unanswered.
	exUnanswered := with(noRounds, noAnswer, func(in *In) {
		a0(in).Acts = []Act{{Key: "start_reviewer@1@c1", Result: "failed", Class: ClassSettled, Err: "boom", At: now}}
	})
	if r, _ := FirstMatch(TaskRows, exUnanswered); r.ID != "N5" {
		t.Fatalf("exhausted start_reviewer while unanswered = %s, want N5", r.ID)
	}
}

// TestN29UnclassifiedPRHeadHoldsReviewer verifies that an answered open PR
// whose head relation is unknown (empty HeadRel, e.g. commit missing locally
// and fetch failed) holds N29 from starting or resuming a reviewer until known.
func TestN29UnclassifiedPRHeadHoldsReviewer(t *testing.T) {
	stoppedUnknown := with(noRounds, reviewEnded, func(in *In) {
		in.Facts.PR.Head, in.Facts.PR.HeadRel = "ext2", ""
		a0(in).Review = &ReviewRun{Round: 1, Head: "c1", Run: &RunRecord{State: PhaseEnded, Result: EndStopped}}
	})
	if r, d := FirstMatch(TaskRows, stoppedUnknown); r.ID != "N29" || len(d.Asked) != 0 {
		t.Fatalf("unclassified PR head with stopped reviewer = %s %v, want N29 nil", r.ID, d.Asked)
	}
	freshUnknown := with(noRounds, func(in *In) {
		in.Facts.PR.Head, in.Facts.PR.HeadRel = "ext2", ""
	})
	if r, d := FirstMatch(TaskRows, freshUnknown); r.ID != "N29" || len(d.Asked) != 0 {
		t.Fatalf("unclassified PR head with fresh reviewer = %s %v, want N29 nil", r.ID, d.Asked)
	}
	exUnknown := with(noRounds, func(in *In) {
		in.Facts.PR.Head, in.Facts.PR.HeadRel = "ext2", ""
		a0(in).Acts = []Act{{Key: "start_reviewer@1@c1", Result: "failed", Class: ClassSettled, Err: "boom", At: now}}
	})
	if r, _ := FirstMatch(TaskRows, exUnknown); r.ID != "N5" {
		t.Fatalf("exhausted start_reviewer with unclassified PR head = %s, want N5", r.ID)
	}
}
