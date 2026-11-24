package flow

import (
	"slices"
	"testing"
	"time"
)

// world is the records and facts a history moves through.
type world struct {
	task  Task
	facts Facts
	asks  []Ask
}

func (w *world) att() *Attempt { return &w.task.Attempts[len(w.task.Attempts)-1] }

// step is one pass: the row it must land on, what it must ask for, and
// what the world does before the next pass (executors, agents, GitHub).
type step struct {
	row   string
	asked []string
	then  func(*world)
}

// TestHistoryHappyPath walks History 1 of notes/table-audit.md with slice 2's
// review_rounds 2 (L8): round 1 (full) asks for changes at c1, round 2 (delta)
// passes at c2, L8 asks for round 3 (full) at c2 which passes, then N42
// accepts and merges, and the task ends done.
func TestHistoryHappyPath(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	w := &world{task: Task{ID: "t1", Title: "one task"}}
	steps := []step{
		{"N11", []string{"new_attempt"}, func(w *world) {
			w.task.Attempts = []Attempt{{BaseCommit: "b0", Branch: "t1"}}
			w.facts.Tip, w.facts.TipRel = "b0", RelSame
		}},
		{"N23", []string{"start_worker"}, func(w *world) { w.facts.Worker = Run{State: RunWorking} }},
		{"N23", nil, nil},
		{"N23", nil, func(w *world) { w.facts.Tip, w.facts.TipRel = "c1", RelAhead }},
		{"N23", nil, func(w *world) { // worker reports ready at c1; GitHub answers: no PR
			w.att().Ready = append(w.att().Ready, Ready{Head: "c1"})
			w.facts.TipRel, w.facts.Worker.State = RelSame, RunIdle
			w.facts.PR = PR{Answered: true}
		}},
		{"N26", []string{"push_and_open_pr@c1"}, func(w *world) {
			w.att().PR = 1
			w.facts.PR = PR{Answered: true, Number: 1, State: PROpen, Head: "c1", HeadRel: RelSame,
				Checks: ChecksPending, Mergeable: MergeableUnknown}
		}},
		{"N29", []string{"start_reviewer@1@c1"}, func(w *world) { w.facts.Reviewer = Run{State: RunWorking} }},
		{"N29", nil, nil},
		{"N29", nil, func(w *world) { // verdict in; the loop stops round 1's reviewer
			w.att().Rounds = append(w.att().Rounds, Round{N: 1, Kind: "full", Head: "c1", Verdict: VerdictChanges, Majors: 2})
			w.facts.Reviewer = Run{State: RunGone}
			w.facts.PR.Checks, w.facts.PR.Mergeable = ChecksGreen, MergeableClean
		}},
		{"N22", []string{"tell_worker_findings@1"}, func(w *world) {
			w.facts.Worker.State = RunWorking
			w.facts.Tip, w.facts.TipRel = "c2", RelAhead
		}},
		{"N22", nil, func(w *world) { // ready at c2: H moves, the PR is behind it
			w.att().Ready = append(w.att().Ready, Ready{Head: "c2"})
			w.facts.TipRel, w.facts.Worker.State = RelSame, RunIdle
			w.facts.PR.HeadRel = RelBehind
			w.facts.Reviewer = Run{} // the ladder now asks for round 2, which has no run yet (Facts.Reviewer)
		}},
		{"N26", []string{"push_and_open_pr@c2"}, func(w *world) {
			w.facts.PR.Head, w.facts.PR.HeadRel = "c2", RelSame
			w.facts.PR.Checks, w.facts.PR.Mergeable = ChecksPending, MergeableUnknown
		}},
		{"N29", []string{"start_reviewer@2@c2"}, func(w *world) { w.facts.Reviewer = Run{State: RunWorking} }},
		{"N29", nil, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 2, Kind: RoundDelta, Head: "c2", Verdict: VerdictPass})
			w.facts.Reviewer = Run{} // round 2 stopped; L8 now asks for round 3, which has no run yet
		}},
		{"N29", []string{"start_reviewer@3@c2"}, func(w *world) { w.facts.Reviewer = Run{State: RunWorking} }},
		{"N29", nil, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 3, Kind: RoundFull, Head: "c2", Verdict: VerdictPass})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N36", nil, func(w *world) { // CI green; a stray worker turn
			w.facts.PR.Checks, w.facts.PR.Mergeable = ChecksGreen, MergeableClean
			w.facts.Worker.State = RunWorking
		}},
		{"N39", nil, func(w *world) { w.facts.Worker.State = RunIdle }},
		{"N42", []string{"record_accept@c2", "merge@c2"}, func(w *world) {
			w.att().Accepts = append(w.att().Accepts, Accept{Head: "c2", By: "bar", Rounds: []int{2, 3}})
		}},
		{"N41", nil, func(w *world) { w.facts.PR.State, w.facts.PR.MergedCommit = PRMerged, "m1" }},
		{"N2", []string{"mark_done"}, func(w *world) { w.task.Done = &Merged{MergedCommit: "m1"} }},
		{"N1", []string{"cleanup"}, func(w *world) { w.facts.Worker = Run{State: RunGone} }},
		{"N1", nil, nil},
	}

	seen := map[string]int{}
	for i, s := range steps {
		pass := i + 1
		in := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0.Add(time.Duration(i) * PassInterval)}
		r, d := FirstMatch(TaskRows, in)
		if r.ID == "N43" {
			t.Fatalf("pass %d: no rule matched (want %s)", pass, s.row)
		}
		if r.ID != s.row || !slices.Equal(d.Asked, s.asked) {
			t.Fatalf("pass %d: row %s asked %v, want %s asked %v", pass, r.ID, d.Asked, s.row, s.asked)
		}
		for _, k := range d.Asked {
			if p, dup := seen[k]; dup {
				t.Fatalf("pass %d: %q asked again (first at pass %d)", pass, k, p)
			}
			seen[k] = pass
			if len(w.task.Attempts) > 0 { // the executor's result is a record
				w.att().Acts = append(w.att().Acts, Act{Key: k, Result: "done", At: in.Now})
			}
		}
		t.Logf("pass %2d: %-4s waits on %-14s asked %v", pass, r.ID, r.WaitsOn, d.Asked)
		if s.then != nil {
			s.then(w)
		}
	}
}

// TestHistoryRejectThenTwoPassesMerge checks KB-REV-62, L10, L8 and D12:
// round 1 asks for changes at c1 -> N22 tells worker -> worker rejects round 1
// at c1 (no new commit, no round consumed) -> L10/N29 starts round 2 at c1 ->
// round 2 passes -> L8/N29 starts round 3 at c1 -> round 3 passes -> N42
// accepts rounds [2, 3] and merges.
func TestHistoryRejectThenTwoPassesMerge(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	w := &world{
		task: Task{ID: "t1", Title: "feat: add constant", Attempts: []Attempt{{
			BaseCommit: "b0", Branch: "t1",
			Ready: []Ready{{Head: "c1"}},
			PR:    1,
			Acts:  []Act{{Key: "new_attempt", Result: "done"}, {Key: "start_worker", Result: "done"}, {Key: "push_and_open_pr@c1", Result: "done"}},
		}}},
		facts: Facts{
			Tip: "c1", TipRel: RelSame, Worker: Run{State: RunIdle},
			PR: PR{Answered: true, Number: 1, State: PROpen, Head: "c1", HeadRel: RelSame, Checks: ChecksGreen, Mergeable: MergeableClean},
		},
	}
	steps := []step{
		{"N29", []string{"start_reviewer@1@c1"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictChanges, Majors: 1})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N22", []string{"tell_worker_findings@1"}, func(w *world) {
			w.att().Rejects = append(w.att().Rejects, Reject{Head: "c1", Round: 1, Why: "brief says no test needed"})
			w.facts.Reviewer = Run{} // L10 now asks for round 2 at c1, which has no run yet
		}},
		{"N29", []string{"start_reviewer@2@c1"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 2, Kind: RoundFull, Head: "c1", Verdict: VerdictPass})
			w.facts.Reviewer = Run{} // L8 now asks for round 3 at c1
		}},
		{"N29", []string{"start_reviewer@3@c1"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 3, Kind: RoundFull, Head: "c1", Verdict: VerdictPass})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N42", []string{"record_accept@c1", "merge@c1"}, func(w *world) {
			w.att().Accepts = append(w.att().Accepts, Accept{Head: "c1", By: "bar", Rounds: []int{2, 3}})
			w.facts.PR.State, w.facts.PR.MergedCommit = PRMerged, "m1"
		}},
		{"N2", []string{"mark_done"}, nil},
	}

	seen := map[string]int{}
	for i, s := range steps {
		pass := i + 1
		in := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0.Add(time.Duration(i) * PassInterval)}
		r, d := FirstMatch(TaskRows, in)
		if r.ID != s.row || !slices.Equal(d.Asked, s.asked) {
			t.Fatalf("pass %d: row %s asked %v, want %s asked %v", pass, r.ID, d.Asked, s.row, s.asked)
		}
		for _, k := range d.Asked {
			if p, dup := seen[k]; dup {
				t.Fatalf("pass %d: %q asked again (first at pass %d)", pass, k, p)
			}
			seen[k] = pass
			w.att().Acts = append(w.att().Acts, Act{Key: k, Result: "done", At: in.Now})
		}
		if s.then != nil {
			s.then(w)
		}
	}
}

// TestHistoryNonConvergenceRecapSplit checks KB-CAP-1 and KB-CAP-9:
// rounds 1, 2, 3 all ask for changes with majors (with retro unavailable) ->
// L12 answers recap -> N25 opens a recap once -> N6 wakes the coordinator once
// (no reviewer runs, worker is not told findings) -> coordinator splits -> N1 cleans up.
func TestHistoryNonConvergenceRecapSplit(t *testing.T) {
	t0 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	w := &world{
		task: Task{ID: "t1", Title: "feat: hard task", Attempts: []Attempt{{
			BaseCommit: "b0", Branch: "t1", PR: 1,
			Ready: []Ready{{Head: "c1"}},
			Acts:  []Act{{Key: "new_attempt", Result: "done"}, {Key: "start_worker", Result: "done"}, {Key: "push_and_open_pr@c1", Result: "done"}},
		}}},
		facts: Facts{
			Tip: "c1", TipRel: RelSame, Worker: Run{State: RunIdle},
			RetroUnavailable: true,
			PR:               PR{Answered: true, Number: 1, State: PROpen, Head: "c1", HeadRel: RelSame, Checks: ChecksGreen, Mergeable: MergeableClean},
		},
	}
	steps := []step{
		{"N29", []string{"start_reviewer@1@c1"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictChanges, Majors: 1})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N22", []string{"tell_worker_findings@1"}, func(w *world) {
			w.att().Ready = append(w.att().Ready, Ready{Head: "c2"})
			w.facts.Tip, w.facts.PR.Head = "c2", "c2"
			w.facts.Reviewer = Run{}
		}},
		{"N29", []string{"start_reviewer@2@c2"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 2, Kind: RoundDelta, Head: "c2", Verdict: VerdictChanges, Majors: 1})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N22", []string{"tell_worker_findings@2"}, func(w *world) {
			w.att().Ready = append(w.att().Ready, Ready{Head: "c3"})
			w.facts.Tip, w.facts.PR.Head = "c3", "c3"
			w.facts.Reviewer = Run{}
		}},
		{"N29", []string{"start_reviewer@3@c3"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 3, Kind: RoundDelta, Head: "c3", Verdict: VerdictChanges, Majors: 1})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N25", []string{"open_recap@review"}, func(w *world) {
			w.task.Recaps = append(w.task.Recaps, Recap{Trigger: "review", Allowed: []string{"split", "drop"}})
		}},
		{"N6", []string{"wake_coordinator@recap@1"}, nil},
		{"N6", nil, func(w *world) {
			w.task.Recaps[0].Decided = &RecapDecision{Choice: "split", Why: "too broad"}
			w.task.Split = &Split{Children: []string{"t1a", "t1b"}}
		}},
		{"N1", []string{"cleanup"}, func(w *world) { w.facts.Worker = Run{State: RunGone} }},
		{"N1", nil, nil},
	}

	seen := map[string]int{}
	for i, s := range steps {
		pass := i + 1
		in := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0.Add(time.Duration(i) * PassInterval)}
		r, d := FirstMatch(TaskRows, in)
		if r.ID != s.row || !slices.Equal(d.Asked, s.asked) {
			t.Fatalf("pass %d: row %s asked %v, want %s asked %v", pass, r.ID, d.Asked, s.row, s.asked)
		}
		for _, k := range d.Asked {
			if p, dup := seen[k]; dup {
				t.Fatalf("pass %d: %q asked again (first at pass %d)", pass, k, p)
			}
			seen[k] = pass
			w.att().Acts = append(w.att().Acts, Act{Key: k, Result: "done", At: in.Now})
		}
		if s.then != nil {
			s.then(w)
		}
	}
}

func runHistory(t *testing.T, w *world, settings Settings, t0 time.Time, steps []step) {
	t.Helper()
	seen := map[string]int{}
	for i, s := range steps {
		pass := i + 1
		in := In{Task: w.task, Settings: settings, Facts: w.facts, Asks: w.asks, Now: t0.Add(time.Duration(i) * PassInterval)}
		r, d := FirstMatch(TaskRows, in)
		if r.ID == "N43" {
			t.Fatalf("pass %d: no rule matched (want %s asked %v)", pass, s.row, s.asked)
		}
		if r.ID != s.row || !slices.Equal(d.Asked, s.asked) {
			t.Fatalf("pass %d: row %s asked %v (why=%q), want %s asked %v", pass, r.ID, d.Asked, d.Why, s.row, s.asked)
		}
		for _, k := range d.Asked {
			if p, dup := seen[k]; dup {
				t.Fatalf("pass %d: %q asked again (first at pass %d)", pass, k, p)
			}
			seen[k] = pass
			if len(w.task.Attempts) > 0 {
				w.att().Acts = append(w.att().Acts, Act{Key: k, Result: "done", At: in.Now})
			}
		}
		if s.then != nil {
			s.then(w)
		}
	}
}

func retroHistoryWorld(rounds []Round, retros []Retro) *world {
	head := "c1"
	ready := []Ready{{Head: "c1"}}
	if len(rounds) > 0 {
		head = rounds[len(rounds)-1].Head
		if head != "c1" {
			ready = append(ready, Ready{Head: head})
		}
	}
	return &world{
		task: Task{
			ID: "t1", Title: "feat: retro calc",
			Retros: retros,
			Attempts: []Attempt{{
				BaseCommit: "b0", Branch: "t1", PR: 1, SizeCap: 200,
				Ready:  ready,
				Rounds: rounds,
				Acts: []Act{
					{Key: AskNewAttempt, Result: "done"},
					{Key: AskStartWorker, Result: "done"},
					{Key: key(AskPushPR, head), Result: "done"},
				},
			}},
		},
		facts: Facts{
			Tip: head, TipRel: RelSame, Worker: Run{State: RunIdle},
			PR: PR{Answered: true, Number: 1, State: PROpen, Head: head, HeadRel: RelSame, Checks: ChecksGreen, Mergeable: MergeableClean},
		},
	}
}

func threeMajorChangesRounds() []Round {
	return []Round{
		{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictChanges, Majors: 1},
		{N: 2, Kind: RoundDelta, Head: "c2", Verdict: VerdictChanges, Majors: 1},
		{N: 3, Kind: RoundDelta, Head: "c3", Verdict: VerdictChanges, Majors: 1},
	}
}

// TestHistorySDF16RetroFixThenSecondFixRecap walks SDF-16 / History 2 of
// notes/table-audit.md (plan.md 12.1, design.md 4.5 N28, 4.6 L11/L4/L5/L3;
// KB-RET-1, KB-RET-4, KB-CAP-1, KB-CAP-9):
// 3 review rounds with majors -> L11/N28 starts retro -> FIX -> L4/N22 tells
// worker (continue disallowed during FIX) -> ready at c4 -> N26 pushes c4 ->
// L5/N28 delta check -> 2nd FIX -> L3/N25 opens review recap with no continue ->
// N6 wakes coordinator once.
func TestHistorySDF16RetroFixThenSecondFixRecap(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	w := retroHistoryWorld(nil, nil)
	settings := DefaultSettings()
	steps := []step{
		{"N29", []string{"start_reviewer@1@c1"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictChanges, Majors: 1})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N22", []string{"tell_worker_findings@1"}, func(w *world) {
			w.att().Ready = append(w.att().Ready, Ready{Head: "c2"})
			w.facts.Tip, w.facts.PR.HeadRel = "c2", RelBehind
			w.facts.Reviewer = Run{}
		}},
		{"N26", []string{"push_and_open_pr@c2"}, func(w *world) {
			w.facts.PR.Head, w.facts.PR.HeadRel = "c2", RelSame
		}},
		{"N29", []string{"start_reviewer@2@c2"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 2, Kind: RoundDelta, Head: "c2", Verdict: VerdictChanges, Majors: 1})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N22", []string{"tell_worker_findings@2"}, func(w *world) {
			w.att().Ready = append(w.att().Ready, Ready{Head: "c3"})
			w.facts.Tip, w.facts.PR.HeadRel = "c3", RelBehind
			w.facts.Reviewer = Run{}
		}},
		{"N26", []string{"push_and_open_pr@c3"}, func(w *world) {
			w.facts.PR.Head, w.facts.PR.HeadRel = "c3", RelSame
		}},
		{"N29", []string{"start_reviewer@3@c3"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 3, Kind: RoundDelta, Head: "c3", Verdict: VerdictChanges, Majors: 1})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N28", []string{"start_retro@4@c3"}, func(w *world) {
			w.facts.Reviewer = Run{State: RunWorking}
		}},
		{"N28", nil, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 4, Kind: RoundRetro, Head: "c3", Verdict: VerdictFix, Majors: 1})
			w.task.Retros = append(w.task.Retros, Retro{
				Attempt: 1, Round: 4, Cause: "patch-shaped-fix",
				WorkerChange: "fix whole negative class", WorkerChangeKind: ChangeKindCheck,
				CoordinatorChange: "add table tests", CoordinatorChangeKind: ChangeKindCheck,
				At: t0.Add(8 * PassInterval),
			})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N22", []string{"tell_worker_findings@4"}, func(w *world) {
			if slices.Contains(SizeRecapAllowed(w.task, settings), ChoiceContinue) {
				t.Fatalf("SizeRecapAllowed during FIX allowed continue: %v", SizeRecapAllowed(w.task, settings))
			}
			w.att().Ready = append(w.att().Ready, Ready{Head: "c4"})
			w.facts.Tip, w.facts.PR.HeadRel = "c4", RelBehind
			w.facts.Reviewer = Run{}
		}},
		{"N26", []string{"push_and_open_pr@c4"}, func(w *world) {
			w.facts.PR.Head, w.facts.PR.HeadRel = "c4", RelSame
		}},
		{"N28", []string{"start_retro@5@c4"}, func(w *world) {
			w.facts.Reviewer = Run{State: RunWorking}
		}},
		{"N28", nil, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 5, Kind: RoundRetro, Head: "c4", Verdict: VerdictFix, Majors: 1})
			w.task.Retros = append(w.task.Retros, Retro{
				Attempt: 1, Round: 5, Cause: "patch-shaped-fix",
				WorkerChange: "handle zero", WorkerChangeKind: ChangeKindCheck,
				CoordinatorChange: "split edge cases", CoordinatorChangeKind: ChangeKindBrief,
				At: t0.Add(12 * PassInterval),
			})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N25", []string{"open_recap@review"}, func(w *world) {
			if slices.Contains(SizeRecapAllowed(w.task, settings), ChoiceContinue) {
				t.Fatalf("SizeRecapAllowed after 2nd FIX allowed continue: %v", SizeRecapAllowed(w.task, settings))
			}
			w.task.Recaps = append(w.task.Recaps, Recap{Trigger: TriggerReview, Allowed: []string{"split", "new", "drop"}})
		}},
		{"N6", []string{"wake_coordinator@recap@1"}, nil},
		{"N6", nil, nil},
	}
	runHistory(t, w, settings, t0, steps)
}

// TestHistoryRetroFixThenDeltaShipMerges checks KB-RET-2 and KB-RET-4:
// retro FIX at c3 -> L4/N22 -> clean merge_only base merge m3 stays at L4
// (does not prematurely trigger L5 delta check) -> worker commits c4 and
// reports ready -> N26 pushes c4 -> L5/N28 delta check -> SHIP -> L6/N42
// accepts and merges -> N2 -> N1.
func TestHistoryRetroFixThenDeltaShipMerges(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	rounds := append(threeMajorChangesRounds(), Round{N: 4, Kind: RoundRetro, Head: "c3", Verdict: VerdictFix, Majors: 1})
	retros := []Retro{{Attempt: 1, Round: 4, Cause: "patch-shaped-fix", WorkerChange: "fix negatives and zero", WorkerChangeKind: ChangeKindCheck, CoordinatorChange: "table test", CoordinatorChangeKind: ChangeKindCheck, At: t0}}
	w := retroHistoryWorld(rounds, retros)
	steps := []step{
		{"N22", []string{"tell_worker_findings@4"}, func(w *world) {
			w.facts.Tip, w.facts.TipRel, w.facts.TipMergeOnly = "m3", RelAhead, true
		}},
		{"N20", []string{"move_h_merge_only@m3"}, func(w *world) {
			w.att().MergeOnly = append(w.att().MergeOnly, Advance{From: "c3", To: "m3"})
			w.facts.Tip, w.facts.TipRel, w.facts.TipMergeOnly = "m3", RelSame, false
		}},
		{"N22", nil, func(w *world) {
			// Covers(a, c3, m3) is true, so L4 stays active; now worker commits real fix c4.
			w.att().Ready = append(w.att().Ready, Ready{Head: "c4"})
			w.facts.Tip, w.facts.TipRel, w.facts.PR.HeadRel = "c4", RelSame, RelBehind
			w.facts.Reviewer = Run{}
		}},
		{"N26", []string{"push_and_open_pr@c4"}, func(w *world) {
			w.facts.PR.Head, w.facts.PR.HeadRel = "c4", RelSame
		}},
		{"N28", []string{"start_retro@5@c4"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 5, Kind: RoundRetro, Head: "c4", Verdict: VerdictShip, Majors: 0})
			w.task.Retros = append(w.task.Retros, Retro{Attempt: 1, Round: 5, Cause: "none", WorkerChange: "keep tests", WorkerChangeKind: ChangeKindCheck, CoordinatorChange: "keep brief", CoordinatorChangeKind: ChangeKindBrief, At: t0.Add(4 * PassInterval)})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N42", []string{"record_accept@c4", "merge@c4"}, func(w *world) {
			w.att().Accepts = append(w.att().Accepts, Accept{Head: "c4", By: "bar", Rounds: []int{5}})
			w.facts.PR.State, w.facts.PR.MergedCommit = PRMerged, "m4"
		}},
		{"N2", []string{"mark_done"}, func(w *world) { w.task.Done = &Merged{MergedCommit: "m4"} }},
		{"N1", []string{"cleanup"}, func(w *world) { w.facts.Worker = Run{State: RunGone} }},
		{"N1", nil, nil},
	}
	runHistory(t, w, DefaultSettings(), t0, steps)
}

// TestHistoryRetroRedesignOpensRecap checks KB-RET-5 and design.md 4.6 L2:
// 3 rounds with majors -> L11/N28 -> REDESIGN -> L2/N25 opens review recap
// (no continue) -> N6 wakes coordinator -> split -> N1 cleanup.
func TestHistoryRetroRedesignOpensRecap(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	w := retroHistoryWorld(threeMajorChangesRounds(), nil)
	steps := []step{
		{"N28", []string{"start_retro@4@c3"}, func(w *world) {
			w.att().Rounds = append(w.att().Rounds, Round{N: 4, Kind: RoundRetro, Head: "c3", Verdict: VerdictRedesign, Majors: 1})
			w.task.Retros = append(w.task.Retros, Retro{Attempt: 1, Round: 4, Cause: "wrong-seam", WorkerChange: "rework seam", WorkerChangeKind: ChangeKindBrief, CoordinatorChange: "design first", CoordinatorChangeKind: ChangeKindBrief, At: t0})
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N25", []string{"open_recap@review"}, func(w *world) {
			w.task.Recaps = append(w.task.Recaps, Recap{Trigger: TriggerReview, Allowed: []string{"split", "new", "drop"}})
		}},
		{"N6", []string{"wake_coordinator@recap@1"}, func(w *world) {
			w.task.Recaps[0].Decided = &RecapDecision{Choice: "split", Why: "wrong seam", At: t0.Add(2 * PassInterval)}
			w.task.Split = &Split{Children: []string{"t1a", "t1b"}}
		}},
		{"N1", []string{"cleanup"}, func(w *world) { w.facts.Worker = Run{State: RunGone} }},
		{"N1", nil, nil},
	}
	runHistory(t, w, DefaultSettings(), t0, steps)
}

// TestHistoryDeltaCheckRetroFailsOutToL3Recap checks KB-RET-4 and design.md 4.6
// L3/M4: after retro FIX and ready at c4, if the delta-check retro fails out
// (RetroUnavailable), L5 skips and L3 sends the task to a review recap at N25
// with no continue, then N6.
func TestHistoryDeltaCheckRetroFailsOutToL3Recap(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	rounds := append(threeMajorChangesRounds(), Round{N: 4, Kind: RoundRetro, Head: "c3", Verdict: VerdictFix, Majors: 1})
	retros := []Retro{{Attempt: 1, Round: 4, Cause: "patch-shaped-fix", WorkerChange: "fix class", WorkerChangeKind: ChangeKindCheck, CoordinatorChange: "add test", CoordinatorChangeKind: ChangeKindCheck, At: t0}}
	w := retroHistoryWorld(rounds, retros)
	settings := DefaultSettings()
	steps := []step{
		{"N22", []string{"tell_worker_findings@4"}, func(w *world) {
			w.att().Ready = append(w.att().Ready, Ready{Head: "c4"})
			w.facts.Tip, w.facts.PR.HeadRel = "c4", RelBehind
			w.facts.Reviewer = Run{}
		}},
		{"N26", []string{"push_and_open_pr@c4"}, func(w *world) {
			w.facts.PR.Head, w.facts.PR.HeadRel = "c4", RelSame
		}},
		{"N28", []string{"start_retro@5@c4"}, func(w *world) {
			w.facts.RetroUnavailable = true
			w.facts.Reviewer = Run{State: RunGone}
		}},
		{"N25", []string{"open_recap@review"}, func(w *world) {
			if slices.Contains(SizeRecapAllowed(w.task, settings), ChoiceContinue) {
				t.Fatalf("SizeRecapAllowed after failed delta check allowed continue: %v", SizeRecapAllowed(w.task, settings))
			}
			w.task.Recaps = append(w.task.Recaps, Recap{Trigger: TriggerReview, Allowed: []string{"split", "new", "drop"}})
		}},
		{"N6", []string{"wake_coordinator@recap@1"}, nil},
		{"N6", nil, nil},
	}
	runHistory(t, w, settings, t0, steps)
}

// TestHistoryPlanAmendSupersedesInAttemptRetro checks KB-RET-5 and plan.md R5:
// a plan amend advancing Attempt.Boundary supersedes earlier in-attempt retro
// verdicts (both REDESIGN and FIX) by reusing Counted(a), while t.Retros keeps
// the task's retro count so a later non-converging streak hits L12/N25 instead
// of drawing a second retro.
func TestHistoryPlanAmendSupersedesInAttemptRetro(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	settings := DefaultSettings()

	t.Run("boundary_supersedes_redesign_to_L7_N29", func(t *testing.T) {
		rounds := append(threeMajorChangesRounds(), Round{N: 4, Kind: RoundRetro, Head: "c3", Verdict: VerdictRedesign, Majors: 1})
		retros := []Retro{{Attempt: 1, Round: 4, Cause: "vague-brief", WorkerChange: "n/a", WorkerChangeKind: ChangeKindBrief, CoordinatorChange: "clarify brief", CoordinatorChangeKind: ChangeKindBrief, At: t0}}
		w := retroHistoryWorld(rounds, retros)
		inBefore := In{Task: w.task, Settings: settings, Facts: w.facts, Now: t0}
		if r, d := FirstMatch(LadderRows, inBefore); r.ID != "L2" || d.Why != RecapAnswer {
			t.Fatalf("before boundary: Ladder = (%s, %s), want (L2, recap)", r.ID, d.Why)
		}
		w.att().Boundary = len(w.att().Rounds)
		w.facts.Reviewer = Run{}
		runHistory(t, w, settings, t0, []step{
			{"N29", []string{"start_reviewer@5@c3"}, nil},
		})
	})

	t.Run("boundary_supersedes_fix_and_retros_count_persists", func(t *testing.T) {
		rounds := append(threeMajorChangesRounds(), Round{N: 4, Kind: RoundRetro, Head: "c3", Verdict: VerdictFix, Majors: 1})
		retros := []Retro{{Attempt: 1, Round: 4, Cause: "patch-shaped-fix", WorkerChange: "fix", WorkerChangeKind: ChangeKindCheck, CoordinatorChange: "clarify", CoordinatorChangeKind: ChangeKindBrief, At: t0}}
		w := retroHistoryWorld(rounds, retros)
		steps := []step{
			{"N22", []string{"tell_worker_findings@4"}, func(w *world) {
				// plan amend advances Boundary past round 4 FIX.
				w.att().Boundary = len(w.att().Rounds)
				w.facts.Reviewer = Run{}
				if !slices.Contains(SizeRecapAllowed(w.task, settings), ChoiceContinue) {
					t.Fatalf("SizeRecapAllowed after Boundary cleared FIX = %v, want continue allowed", SizeRecapAllowed(w.task, settings))
				}
			}},
			{"N29", []string{"start_reviewer@5@c3"}, func(w *world) {
				w.att().Rounds = append(w.att().Rounds, Round{N: 5, Kind: RoundFull, Head: "c3", Verdict: VerdictChanges, Majors: 1})
				w.facts.Reviewer = Run{State: RunGone}
			}},
			// t.Retros still has length 1 == retros_per_task, so the non-converging streak hits L12/N25 (never L11/N28).
			{"N25", []string{"open_recap@review"}, func(w *world) {
				w.task.Recaps = append(w.task.Recaps, Recap{Trigger: TriggerReview, Allowed: []string{"split", "new", "drop"}})
			}},
			{"N6", []string{"wake_coordinator@recap@1"}, nil},
		}
		runHistory(t, w, settings, t0, steps)
	})
}

// TestHistoryRetroFixSizeOverrunNoContinue checks KB-RET-4, design.md 4.3, and
// Conflict C9 / table-audit F2.2: a size overrun during a retro's bounded FIX
// hits N12 and opens a size recap whose allowed choices omit continue, even
// with 0 continues used.
func TestHistoryRetroFixSizeOverrunNoContinue(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	rounds := append(threeMajorChangesRounds(), Round{N: 4, Kind: RoundRetro, Head: "c3", Verdict: VerdictFix, Majors: 1})
	retros := []Retro{{Attempt: 1, Round: 4, Cause: "patch-shaped-fix", WorkerChange: "fix class", WorkerChangeKind: ChangeKindCheck, CoordinatorChange: "tighten brief", CoordinatorChangeKind: ChangeKindBrief, At: t0}}
	w := retroHistoryWorld(rounds, retros)
	w.att().SizeCap = 160
	settings := DefaultSettings()
	steps := []step{
		{"N22", []string{"tell_worker_findings@4"}, func(w *world) {
			w.facts.Lines = 165
			w.facts.Tip, w.facts.TipRel = "c4", RelAhead
			w.facts.Worker.State = RunWorking
		}},
		{"N12", []string{"open_size_recap@0"}, func(w *world) {
			allowed := SizeRecapAllowed(w.task, settings)
			if !slices.Equal(allowed, []string{"split", "new", "drop"}) {
				t.Fatalf("SizeRecapAllowed during retro FIX = %v, want [split new drop] (no continue)", allowed)
			}
			w.task.Recaps = append(w.task.Recaps, Recap{Trigger: TriggerSize, Allowed: allowed})
		}},
		{"N6", []string{"wake_coordinator@recap@1", "tell_worker_stop@1"}, nil},
		{"N6", nil, nil},
	}
	runHistory(t, w, settings, t0, steps)
}

// TestHistorySplitChildAfterParentRetroGoesToN24 checks KB-RET-7 and
// table-audit F2.4: a split child whose parent already had a retro skips L11
// even with 0 retros of its own, routing via L12 to N24 (owner ask), then N25
// and N6 once the owner answers Continue.
func TestHistorySplitChildAfterParentRetroGoesToN24(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	settings := DefaultSettings()

	// Control: same 3 rounds without SplitFromHadRetro draws L11/N28.
	ctrl := retroHistoryWorld(threeMajorChangesRounds(), nil)
	ctrl.task.SplitFrom = "P1"
	if r, d := FirstMatch(LadderRows, In{Task: ctrl.task, Settings: settings, Facts: ctrl.facts, Now: t0}); r.ID != "L11" || d.Why != RetroAnswer {
		t.Fatalf("control split child without parent retro: Ladder = (%s, %s), want (L11, retro)", r.ID, d.Why)
	}

	w := retroHistoryWorld(threeMajorChangesRounds(), nil)
	w.task.SplitFrom = "P1"
	w.facts.SplitFromHadRetro = true
	steps := []step{
		{"N24", []string{"open_n24_ask@c3"}, func(w *world) {
			w.asks = []Ask{openN24Ask("ask-n24-1", "t1", "c3")}
		}},
		{"N24", nil, func(w *world) {
			w.asks = []Ask{answeredN24Ask("ask-n24-1", "t1", "c3", N24OptionContinue, t0.Add(PassInterval))}
		}},
		{"N25", []string{"open_recap@review"}, func(w *world) {
			w.task.Recaps = append(w.task.Recaps, Recap{Trigger: TriggerReview, Allowed: []string{"split", "new", "drop"}})
		}},
		{"N6", []string{"wake_coordinator@recap@1"}, nil},
	}
	runHistory(t, w, settings, t0, steps)
}

// TestHistoryRetroUnavailableOrExhaustedFallsToL12N25 checks KB-RET-1,
// KB-RET-3, and design.md 4.6 M4: a non-converging task normally hits L11/N28,
// and falls through to L12/N25 when retro is unavailable, retros_per_task is
// exhausted across attempts, or retros_per_task is 0.
func TestHistoryRetroUnavailableOrExhaustedFallsToL12N25(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	settings := DefaultSettings()

	// Control: retro available -> L11/N28.
	ctrl := retroHistoryWorld(threeMajorChangesRounds(), nil)
	if r, d := FirstMatch(TaskRows, In{Task: ctrl.task, Settings: settings, Facts: ctrl.facts, Now: t0}); r.ID != "N28" || !slices.Equal(d.Asked, []string{"start_retro@4@c3"}) {
		t.Fatalf("control retro available = (%s, %v), want (N28, [start_retro@4@c3])", r.ID, d.Asked)
	}

	for name, setup := range map[string]func() (*world, Settings){
		"unavailable": func() (*world, Settings) {
			w := retroHistoryWorld(threeMajorChangesRounds(), nil)
			w.facts.RetroUnavailable = true
			return w, settings
		},
		"exhausted_in_prior_attempt": func() (*world, Settings) {
			w := retroHistoryWorld(threeMajorChangesRounds(), []Retro{{Attempt: 1, Round: 4, Cause: "wrong-seam", At: t0.Add(-time.Hour)}})
			w.task.Attempts = append([]Attempt{{BaseCommit: "b0", Closed: "new attempt"}}, w.task.Attempts...)
			return w, settings
		},
		"disabled_zero": func() (*world, Settings) {
			s := settings
			s.RetrosPerTask = 0
			return retroHistoryWorld(threeMajorChangesRounds(), nil), s
		},
	} {
		t.Run(name, func(t *testing.T) {
			w, s := setup()
			runHistory(t, w, s, t0, []step{
				{"N25", []string{"open_recap@review"}, func(w *world) {
					w.task.Recaps = append(w.task.Recaps, Recap{Trigger: TriggerReview, Allowed: []string{"split", "new", "drop"}})
				}},
				{"N6", []string{"wake_coordinator@recap@1"}, nil},
			})
		})
	}
}

// TestHistoryWorkerRejectOnRetroFixRefused checks design.md 4.6 L10, 7.2, and
// KB-RET-4: a retro FIX round is never rejectable by the worker, and even if a
// Reject record exists at that head/round the ladder stays on L4 (fix) / N22,
// never L10 (review).
func TestHistoryWorkerRejectOnRetroFixRefused(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	rounds := append(threeMajorChangesRounds(), Round{N: 4, Kind: RoundRetro, Head: "c3", Verdict: VerdictFix, Majors: 1})
	retros := []Retro{{Attempt: 1, Round: 4, Cause: "patch-shaped-fix", WorkerChange: "fix class", WorkerChangeKind: ChangeKindCheck, CoordinatorChange: "table test", CoordinatorChangeKind: ChangeKindCheck, At: t0}}
	w := retroHistoryWorld(rounds, retros)
	settings := DefaultSettings()

	if r := RejectableRound(*w.att(), "c3"); r != nil {
		t.Fatalf("RejectableRound on retro FIX = %+v, want nil", r)
	}
	w.att().Rejects = []Reject{{Head: "c3", Round: 4, Why: "disagree with retro"}}
	in := In{Task: w.task, Settings: settings, Facts: w.facts, Now: t0}
	if r, d := FirstMatch(LadderRows, in); r.ID != "L4" || d.Why != Fix {
		t.Fatalf("Ladder with stray reject on retro FIX = (%s, %s), want (L4, fix)", r.ID, d.Why)
	}
	runHistory(t, w, settings, t0, []step{
		{"N22", []string{"tell_worker_findings@4"}, nil},
	})
}
