package flow

// Ladder answers (design.md 4.6). NoLadderRow is the catch-all's: slice 1
// has no ladder row for it, so no task row acts on it and N43 wakes the
// coordinator.
const (
	Passed      = "passed"
	Fix         = "fix"
	Review      = "review"
	RecapAnswer = "recap"
	NoLadderRow = "no ladder rule matched"
)

// roundBase is the number of the first round (design.md 3: round{n}).
const roundBase = 1

// LadderRows is slice 2's review ladder: L7, L8, L9, L10, L12 and L13 of design.md 4.6.
// A row's Decision.Why is its answer; WaitsOn is who that answer waits on.
var LadderRows = []Row[In]{
	{ID: "L7", WaitsOn: "the reviewer", Stage: "working", Why: WhyReview, When: noRoundCovers, Then: answer(Review)},
	{ID: "L8", WaitsOn: "the reviewer", Stage: "working", Why: WhyReview, When: all(coveringIs(VerdictPass), morePassesNeeded), Then: answer(Review)},
	{ID: "L9", WaitsOn: "the engine", Stage: "ci", Why: WhyCI, When: coveringIs(VerdictPass), Then: answer(Passed)},
	{ID: "L10", WaitsOn: "the reviewer", Stage: "working", Why: WhyReview, When: coveringRoundRejected, Then: answer(Review)},
	{ID: "L12", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: all(coveringIs(VerdictChanges), nonConverging), Then: answer(RecapAnswer)},
	{ID: "L13", WaitsOn: "the worker", Stage: "working", When: coveringIs(VerdictChanges), Then: answer(Fix)},
	{ID: "L-", WaitsOn: "the coordinator", Stage: "blocked_or_failed", Then: answer(NoLadderRow)},
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

func coveringRoundRejected(in In) bool {
	_, rej := RejectedRound(*in.attempt(), headH(in))
	return rej != nil
}

func morePassesNeeded(in In) bool {
	return len(PassingCovering(*in.attempt(), headH(in))) < in.Settings.ReviewRounds
}

// PassingCovering is the passing rounds that cover h (design.md 3.1, 4.6).
func PassingCovering(a Attempt, h string) []Round {
	var out []Round
	for _, r := range Counted(a) {
		if Covers(a, r.Head, h) && r.Verdict == VerdictPass {
			out = append(out, r)
		}
	}
	return out
}

// nonConverging is L12's guard: the last retro_after_rounds_with_majors
// reviewer rounds all had majors at round retro_from_round or later, or
// max_rounds reviewer rounds were run (design.md 4.6).
func nonConverging(in In) bool {
	a := *in.attempt()
	if r := newestCovering(a, headH(in)); r == nil || !isReviewerRound(*r) {
		return false
	}
	var rr []Round
	for _, r := range a.Rounds {
		if isReviewerRound(r) {
			rr = append(rr, r)
		}
	}
	if len(rr) >= in.Settings.MaxRounds {
		return true
	}
	k := in.Settings.RetroAfterRoundsWithMajors
	if len(rr) < in.Settings.RetroFromRound || len(rr) < k {
		return false
	}
	for _, r := range rr[len(rr)-k:] {
		if r.Majors == 0 {
			return false
		}
	}
	return true
}

// nextRound is the number the next review round gets.
func nextRound(a Attempt) int { return len(a.Rounds) + roundBase }
