package flow

// Ladder answers (design.md 4.6). NoLadderRow is the catch-all's: slice 1
// has no ladder row for it, so no task row acts on it and N43 wakes the
// coordinator.
const (
	Passed      = "passed"
	Fix         = "fix"
	Review      = "review"
	RetroAnswer = "retro"
	RecapAnswer = "recap"
	NoLadderRow = "no ladder rule matched"
)

// roundBase is the number of the first round (design.md 3: round{n}).
const roundBase = 1

// LadderRows is the review ladder: L1–L13 of design.md 4.6.
// A row's Decision.Why is its answer; WaitsOn is who that answer waits on.
var LadderRows = []Row[In]{
	{ID: "L1", WaitsOn: "the engine", Stage: "ci", Why: WhyCI, When: overrideCovers, Then: answer(Passed)},
	{ID: "L2", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: newestRetroRedesign, Then: answer(RecapAnswer)},
	{ID: "L3", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: retroFixCapOrFailedDelta, Then: answer(RecapAnswer)},
	{ID: "L4", WaitsOn: "the worker", Stage: "working", When: retroFixWaitingWorker, Then: answer(Fix)},
	{ID: "L5", WaitsOn: "the retro reviewer", Stage: "working", When: retroFixNeedsDelta, Then: answer(RetroAnswer)},
	{ID: "L6", WaitsOn: "the engine", Stage: "ci", Why: WhyCI, When: coveringRetroShip, Then: answer(Passed)},
	{ID: "L7", WaitsOn: "the reviewer", Stage: "working", When: noRoundCovers, Then: answer(Review)},
	{ID: "L8", WaitsOn: "the reviewer", Stage: "working", When: all(coveringIs(VerdictPass), morePassesNeeded), Then: answer(Review)},
	{ID: "L9", WaitsOn: "the engine", Stage: "ci", Why: WhyCI, When: coveringIs(VerdictPass), Then: answer(Passed)},
	{ID: "L10", WaitsOn: "the reviewer", Stage: "working", When: coveringRoundRejected, Then: answer(Review)},
	{ID: "L11", WaitsOn: "the retro reviewer", Stage: "working", When: all(coveringIs(VerdictChanges), majorStreak, retroAllowed), Then: answer(RetroAnswer)},
	{ID: "L12", WaitsOn: "the coordinator", Stage: "blocked_or_failed", When: all(coveringIs(VerdictChanges), anyOf(majorStreak, maxRoundsHit)), Then: answer(RecapAnswer)},
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

func noRoundCovers(in In) bool { return NewestCovering(*in.attempt(), headH(in)) == nil }

func coveringIs(verdict string) Clause {
	return func(in In) bool {
		r := NewestCovering(*in.attempt(), headH(in))
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

func newestAttemptRetro(in In) (Round, []Round, bool) {
	rs := AttemptRetros(*in.attempt())
	if len(rs) == 0 {
		return Round{}, nil, false
	}
	return rs[len(rs)-roundBase], rs[:len(rs)-roundBase], true
}

func noRecapSinceRetro(in In, round int) bool {
	last := LastRecapDecidedAt(in.Task)
	return last.IsZero() || retroAt(in.Task, len(in.Task.Attempts), round).After(last)
}

func newestRetroRedesign(in In) bool {
	last, _, ok := newestAttemptRetro(in)
	return ok && last.Verdict == VerdictRedesign && noRecapSinceRetro(in, last.N)
}

func retroFixCapOrFailedDelta(in In) bool {
	last, earlier, ok := newestAttemptRetro(in)
	if !ok || last.Verdict != VerdictFix || !noRecapSinceRetro(in, last.N) {
		return false
	}
	for _, r := range earlier {
		if r.Verdict == VerdictFix {
			return true
		}
	}
	return !Covers(*in.attempt(), last.Head, headH(in)) && in.Facts.RetroUnavailable
}

func retroFixWaitingWorker(in In) bool {
	last, _, ok := newestAttemptRetro(in)
	return ok && last.Verdict == VerdictFix && Covers(*in.attempt(), last.Head, headH(in))
}

func retroFixNeedsDelta(in In) bool {
	last, _, ok := newestAttemptRetro(in)
	return ok && last.Verdict == VerdictFix && !Covers(*in.attempt(), last.Head, headH(in))
}

func coveringRetroShip(in In) bool {
	r := NewestCovering(*in.attempt(), headH(in))
	return r != nil && r.Kind == RoundRetro && r.Verdict == VerdictShip
}

func reviewerRounds(a Attempt) []Round {
	var rr []Round
	for _, r := range a.Rounds {
		if isReviewerRound(r) {
			rr = append(rr, r)
		}
	}
	return rr
}

func coveringIsReviewer(in In) bool {
	r := NewestCovering(*in.attempt(), headH(in))
	return r != nil && isReviewerRound(*r)
}

// majorStreak is L11/L12's streak guard: the last retro_after_rounds_with_majors
// reviewer rounds all had majors at round retro_from_round or later (design.md 4.6).
func majorStreak(in In) bool {
	if !coveringIsReviewer(in) {
		return false
	}
	rr, k := reviewerRounds(*in.attempt()), in.Settings.RetroAfterRoundsWithMajors
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

func maxRoundsHit(in In) bool {
	return coveringIsReviewer(in) && len(reviewerRounds(*in.attempt())) >= in.Settings.MaxRounds
}

func retroAllowed(in In) bool {
	return len(in.Task.Retros) < in.Settings.RetrosPerTask && !in.Facts.RetroUnavailable &&
		(in.Task.SplitFrom == "" || !in.Facts.SplitFromHadRetro)
}

// nextRound is the number the next review round gets.
func nextRound(a Attempt) int { return len(a.Rounds) + roundBase }
