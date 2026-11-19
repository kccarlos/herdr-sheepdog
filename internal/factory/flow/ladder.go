package flow

// Ladder answers (design.md 4.6). NoLadderRow is the catch-all's: slice 1
// has no ladder row for it, so no task row acts on it and N43 wakes the
// coordinator.
const (
	Passed      = "passed"
	Fix         = "fix"
	Review      = "review"
	NoLadderRow = "no ladder rule matched"
)

// roundBase is the number of the first round (design.md 3: round{n}).
const roundBase = 1

// LadderRows is slice 2's review ladder: L7, L8, L9 and L13 of design.md 4.6.
// A row's Decision.Why is its answer; WaitsOn is who that answer waits on.
var LadderRows = []Row[In]{
	{ID: "L7", WaitsOn: "the reviewer", When: noRoundCovers, Then: answer(Review)},
	{ID: "L8", WaitsOn: "the reviewer", When: all(coveringIs(VerdictPass), morePassesNeeded), Then: answer(Review)},
	{ID: "L9", WaitsOn: "the engine", When: coveringIs(VerdictPass), Then: answer(Passed)},
	{ID: "L13", WaitsOn: "the worker", When: coveringIs(VerdictChanges), Then: answer(Fix)},
	{ID: "L-", WaitsOn: "the coordinator", Then: answer(NoLadderRow)},
}

// Ladder is what review needs next at H. The task must have an attempt.
func Ladder(in In) string {
	_, d := FirstMatch(LadderRows, in)
	return d.Why
}

func answer(a string) func(In) Decision {
	return func(In) Decision { return Decision{Why: a} }
}

func noRoundCovers(in In) bool { return newestCovering(*in.attempt(), headH(in)) == nil }

func coveringIs(verdict string) Clause {
	return func(in In) bool {
		r := newestCovering(*in.attempt(), headH(in))
		return r != nil && r.Verdict == verdict
	}
}

func morePassesNeeded(in In) bool {
	return passingCovering(*in.attempt(), headH(in)) < in.Settings.ReviewRounds
}

func passingCovering(a Attempt, h string) int {
	n := 0
	for _, r := range a.Rounds {
		if Covers(r.Head, h) && r.Verdict == VerdictPass {
			n++
		}
	}
	return n
}

// nextRound is the number the next review round gets.
func nextRound(a Attempt) int { return len(a.Rounds) + roundBase }
