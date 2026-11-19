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
		{"N23", []string{"start_worker"}, func(w *world) { w.facts.Worker.Pending = true }},
		{"N23", nil, func(w *world) { w.facts.Worker = Run{State: RunWorking} }},
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
		{"N29", []string{"start_reviewer@1@c1"}, func(w *world) { w.facts.Reviewer.Pending = true }},
		{"N29", nil, func(w *world) { w.facts.Reviewer = Run{State: RunWorking} }},
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
