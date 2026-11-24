package flow

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

// PR 12.1, TDD step 1: pure flow unit tests for retro records, ReviewRun.Role(),
// SizeRecapAllowed FIX guard, L2/L3 recap-decided-since and attempt scoping,
// retros_per_task counting at min/default/max, N28 EnsureRun/nudge/stop/retry,
// and KB-RET-9 / KB-RET-11 rules.

// TestRetroCausesAndReviewRunRole checks KB-RET-15 and plan.md 12.1:
// RetroCauses holds the fixed 8-cause set, and ReviewRun.Role() returns
// RoleRetro when Kind == RoundRetro and RoleReviewer otherwise.
func TestRetroCausesAndReviewRunRole(t *testing.T) {
	wantCauses := []string{
		"task-too-big", "wrong-seam", "vague-brief", "design-needed-first",
		"patch-shaped-fix", "worker-model", "reviewer-narrow", "none",
	}
	if !slices.Equal(RetroCauses, wantCauses) {
		t.Fatalf("RetroCauses = %v, want %v", RetroCauses, wantCauses)
	}
	for name, tc := range map[string]struct {
		rev  *ReviewRun
		want string
	}{
		"nil":        {nil, RoleReviewer},
		"empty_kind": {&ReviewRun{}, RoleReviewer},
		"full":       {&ReviewRun{Kind: RoundFull}, RoleReviewer},
		"delta":      {&ReviewRun{Kind: RoundDelta}, RoleReviewer},
		"retro":      {&ReviewRun{Kind: RoundRetro}, RoleRetro},
	} {
		if got := tc.rev.Role(); got != tc.want {
			t.Errorf("%s: ReviewRun.Role() = %q, want %q", name, got, tc.want)
		}
	}
}

// TestSizeRecapAllowedDuringRetroFix checks design.md 4.3, KB-RET-4, and
// Conflict C9: continue is refused while this attempt's newest counted retro
// verdict is FIX, and allowed again after SHIP, Boundary amend, or a new attempt.
func TestSizeRecapAllowedDuringRetroFix(t *testing.T) {
	s := DefaultSettings()

	t.Run("newest_retro_FIX_blocks_continue", func(t *testing.T) {
		in := with(retroFix)
		got := SizeRecapAllowed(in.Task, s)
		if !slices.Equal(got, []string{"split", "new", "drop"}) {
			t.Fatalf("SizeRecapAllowed during retro FIX = %v, want [split new drop]", got)
		}
	})

	t.Run("newest_retro_SHIP_allows_continue", func(t *testing.T) {
		in := with(retroShip)
		got := SizeRecapAllowed(in.Task, s)
		if !slices.Contains(got, ChoiceContinue) {
			t.Fatalf("SizeRecapAllowed after retro SHIP = %v, want continue allowed", got)
		}
	})

	t.Run("boundary_superseding_FIX_allows_continue", func(t *testing.T) {
		in := with(retroFix)
		a0(&in).Boundary = len(a0(&in).Rounds)
		got := SizeRecapAllowed(in.Task, s)
		if !slices.Contains(got, ChoiceContinue) {
			t.Fatalf("SizeRecapAllowed after plan amend Boundary = %v, want continue allowed", got)
		}
	})

	t.Run("closed_attempt_FIX_does_not_block_new_attempt_continue", func(t *testing.T) {
		in := with(retroFix)
		a0(&in).Closed = "new attempt"
		in.Task.Attempts = append(in.Task.Attempts, Attempt{BaseCommit: "b0", Ready: []Ready{{Head: "c2"}}})
		got := SizeRecapAllowed(in.Task, s)
		if !slices.Contains(got, ChoiceContinue) {
			t.Fatalf("SizeRecapAllowed in new attempt after closed FIX attempt = %v, want continue allowed", got)
		}
	})
}

// TestRetroRecapDecidedSinceAndAttemptScoping checks design.md 4.6 L2/L3 and
// plan.md brief decision R-7: L2 and L3 join RoundRetro to Task.Retros by
// (attempt, round) and compare Retro.At against LastRecapDecidedAt(t), and
// retro verdicts steer only the attempt they were given in.
func TestRetroRecapDecidedSinceAndAttemptScoping(t *testing.T) {
	t0 := now.Add(-10 * time.Minute)
	t1 := now.Add(-5 * time.Minute)
	t2 := now.Add(-1 * time.Minute)

	t.Run("L2_redesign_holds_after_earlier_recap_and_after_spent_override", func(t *testing.T) {
		in := with(retroRedesign)
		in.Task.Retros[0].At = t1
		in.Task.Recaps = []Recap{{Trigger: TriggerSize, Decided: &RecapDecision{Choice: ChoiceContinue, At: t0}}}
		if r, d := FirstMatch(LadderRows, in); r.ID != "L2" || d.Why != RecapAnswer {
			t.Fatalf("REDESIGN newer than prior recap = (%s, %s), want (L2, recap)", r.ID, d.Why)
		}
		// Active owner override closes the recap and wins via L1.
		in.Task.Recaps = append(in.Task.Recaps, Recap{Trigger: TriggerReview, Decided: &RecapDecision{Choice: "override", At: t2}})
		withOverride("c1", ChecksGreen, false)(&in)
		if r, d := FirstMatch(LadderRows, in); r.ID != "L1" || d.Why != Passed {
			t.Fatalf("REDESIGN with active override = (%s, %s), want (L1, passed)", r.ID, d.Why)
		}
		// Once N19b spends the override, normal rules resume: L2 must match (never L- / N43).
		a0(&in).Overrides[0].Spent = true
		if r, d := FirstMatch(LadderRows, in); r.ID != "L2" || d.Why != RecapAnswer {
			t.Fatalf("REDESIGN after spent override = (%s, %s), want (L2, recap)", r.ID, d.Why)
		}
	})

	t.Run("L3_second_fix_holds_after_earlier_recap_and_after_spent_override", func(t *testing.T) {
		in := with(retroSecondFix)
		in.Task.Retros[0].At = t0
		in.Task.Retros[1].At = t1
		in.Task.Recaps = []Recap{{Trigger: TriggerSize, Decided: &RecapDecision{Choice: ChoiceContinue, At: t0}}}
		if r, d := FirstMatch(LadderRows, in); r.ID != "L3" || d.Why != RecapAnswer {
			t.Fatalf("2nd FIX newer than prior recap = (%s, %s), want (L3, recap)", r.ID, d.Why)
		}
		// Active owner override closes the recap and wins via L1.
		in.Task.Recaps = append(in.Task.Recaps, Recap{Trigger: TriggerReview, Decided: &RecapDecision{Choice: "override", At: t2}})
		withOverride("c1", ChecksGreen, false)(&in)
		if r, d := FirstMatch(LadderRows, in); r.ID != "L1" || d.Why != Passed {
			t.Fatalf("2nd FIX with active override = (%s, %s), want (L1, passed)", r.ID, d.Why)
		}
		// Once N19b spends the override, normal rules resume: L3 must match (never L4/L5/L-).
		a0(&in).Overrides[0].Spent = true
		if r, d := FirstMatch(LadderRows, in); r.ID != "L3" || d.Why != RecapAnswer {
			t.Fatalf("2nd FIX after spent override = (%s, %s), want (L3, recap)", r.ID, d.Why)
		}
	})

	t.Run("prior_attempt_redesign_does_not_steer_new_attempt", func(t *testing.T) {
		in := with(retroRedesign)
		a0(&in).Closed = "new attempt"
		in.Task.Attempts = append(in.Task.Attempts, Attempt{
			BaseCommit: "b0",
			Ready:      []Ready{{Head: "c2"}},
		})
		in.Facts.Tip, in.Facts.PR.Head = "c2", "c2"
		if r, d := FirstMatch(LadderRows, in); r.ID != "L7" || d.Why != Review {
			t.Fatalf("new attempt after closed REDESIGN attempt = (%s, %s), want (L7, review)", r.ID, d.Why)
		}
	})
}

// TestRetrosPerTaskCountingAtMinDefaultMax checks design.md 4.6 L11, 10.1,
// plan.md R3, KB-RET-1, and KB-RET-4 across RetrosPerTask = 0, 1, and 2:
// every retro record in t.Retros counts (including delta checks), while L5
// sits above L11 so a FIX delta check still runs at default retros_per_task 1.
func TestRetrosPerTaskCountingAtMinDefaultMax(t *testing.T) {
	// Min (0): retros disabled -> L11 never fires, falls through to L12.
	in0 := with(retroStreak, func(in *In) { in.Settings.RetrosPerTask = 0 })
	if r, d := FirstMatch(LadderRows, in0); r.ID != "L12" || d.Why != RecapAnswer {
		t.Fatalf("RetrosPerTask=0: got (%s, %s), want (L12, recap)", r.ID, d.Why)
	}

	// Default (1): L11 fires with 0 retros, L5 delta check still fires with 1 retro (FIX),
	// and a second attempt after 1 retro falls through L11 to L12.
	in1 := with(retroStreak)
	if r, d := FirstMatch(LadderRows, in1); r.ID != "L11" || d.Why != RetroAnswer {
		t.Fatalf("RetrosPerTask=1, 0 used: got (%s, %s), want (L11, retro)", r.ID, d.Why)
	}
	in1Delta := with(retroFixReadyC2)
	if len(in1Delta.Task.Retros) != 1 {
		t.Fatalf("fixture retros = %d, want 1", len(in1Delta.Task.Retros))
	}
	if r, d := FirstMatch(LadderRows, in1Delta); r.ID != "L5" || d.Why != RetroAnswer {
		t.Fatalf("RetrosPerTask=1, L5 delta with 1 retro used: got (%s, %s), want (L5, retro)", r.ID, d.Why)
	}
	in1Exhausted := with(retroStreak, retrosExhausted)
	if r, d := FirstMatch(LadderRows, in1Exhausted); r.ID != "L12" || d.Why != RecapAnswer {
		t.Fatalf("RetrosPerTask=1, 1 used: got (%s, %s), want (L12, recap)", r.ID, d.Why)
	}

	// Max (2): 1 retro used (e.g. REDESIGN in attempt 1) allows another in attempt 2 (L11);
	// 2 retros used (e.g. FIX + delta check in attempt 1) exhausts the budget (L12).
	in2OneUsed := with(retroStreak, retrosExhausted, func(in *In) { in.Settings.RetrosPerTask = RetrosPerTaskMax })
	if r, d := FirstMatch(LadderRows, in2OneUsed); r.ID != "L11" || d.Why != RetroAnswer {
		t.Fatalf("RetrosPerTask=2, 1 used: got (%s, %s), want (L11, retro)", r.ID, d.Why)
	}
	in2TwoUsed := with(retroStreak, func(in *In) {
		in.Settings.RetrosPerTask = RetrosPerTaskMax
		in.Task.Retros = []Retro{
			{Attempt: 1, Round: 4, Cause: "patch-shaped-fix", At: now.Add(-2 * time.Hour)},
			{Attempt: 1, Round: 5, Cause: "patch-shaped-fix", At: now.Add(-time.Hour)},
		}
	})
	if r, d := FirstMatch(LadderRows, in2TwoUsed); r.ID != "L12" || d.Why != RecapAnswer {
		t.Fatalf("RetrosPerTask=2, 2 used (FIX + delta): got (%s, %s), want (L12, recap)", r.ID, d.Why)
	}
}

// TestN28EnsureRunNudgeStopAndRetry checks design.md 4.5 N28, 4.8, and plan.md
// 12.1: EnsureRun(RoleRetro), N28 unanswered/unclassified PR holds, idle nudge
// and stop, review_minutes timeout, and SinceRetry scoping via doneActs.
func TestN28EnsureRunNudgeStopAndRetry(t *testing.T) {
	rBase := func(fs ...func(*In)) In {
		in := with(retroStreak)
		for _, f := range fs {
			f(&in)
		}
		return in
	}

	// EnsureRun(RoleRetro) mirrors RoleReviewer with AskStartRetro keys.
	for name, tc := range map[string]struct {
		in   In
		want []string
	}{
		"fresh retro": {rBase(), []string{"start_retro@4@c1"}},
		"engine stopped retro": {rBase(reviewEnded, func(in *In) {
			a0(in).Review = &ReviewRun{Round: 4, Kind: RoundRetro, Head: "c1", Run: &RunRecord{State: PhaseEnded, Result: EndStopped}}
		}), []string{"start_retro@4@resume@c1"}},
		"quota ended retro": {rBase(reviewEnded, func(in *In) {
			a0(in).Review = &ReviewRun{Round: 4, Kind: RoundRetro, Head: "c1", Run: &RunRecord{State: PhaseEnded, Result: EndQuota}}
		}), []string{"start_retro@4@resume@c1"}},
		"no report retry 1": {rBase(reviewEnded, func(in *In) {
			a0(in).Review = &ReviewRun{Round: 4, Kind: RoundRetro, Head: "c1", Retries: 0, Run: &RunRecord{State: PhaseEnded, Result: EndExited}}
		}), []string{"start_retro@4@1@c1"}},
		"no report retry 2": {rBase(reviewEnded, func(in *In) {
			a0(in).Review = &ReviewRun{Round: 4, Kind: RoundRetro, Head: "c1", Retries: 1, Run: &RunRecord{State: PhaseEnded, Result: EndStuck}}
		}), []string{"start_retro@4@2@c1"}},
	} {
		if got := EnsureRun(tc.in, RoleRetro); !slices.Equal(got, tc.want) {
			t.Errorf("EnsureRun(RoleRetro) %s = %v, want %v", name, got, tc.want)
		}
	}

	// N28 holds on unanswered or unclassified PR head.
	if r, d := FirstMatch(TaskRows, rBase(noAnswer)); r.ID != "N28" || len(d.Asked) != 0 {
		t.Fatalf("N28 unanswered PR = (%s, %v), want (N28, nil)", r.ID, d.Asked)
	}
	if r, d := FirstMatch(TaskRows, rBase(func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "ext", "" })); r.ID != "N28" || len(d.Asked) != 0 {
		t.Fatalf("N28 unclassified PR head = (%s, %v), want (N28, nil)", r.ID, d.Asked)
	}

	// N28 idle nudge once -> stop after nudge -> review_minutes stops even while working.
	s := DefaultSettings()
	idleRetro := rBase(func(in *In) {
		a0(in).Review = &ReviewRun{Round: 4, Kind: RoundRetro, Head: "c1"}
		in.Facts.Reviewer = Run{State: RunIdle, LastTurnEnd: now}
	})
	if r, d := FirstMatch(TaskRows, idleRetro); r.ID != "N28" || !slices.Equal(d.Asked, []string{"nudge_retro@4@0@c1"}) {
		t.Fatalf("N28 idle first = (%s, %v), want (N28, [nudge_retro@4@0@c1])", r.ID, d.Asked)
	}
	a0(&idleRetro).Acts = []Act{{Key: "nudge_retro@4@0@c1", Result: "done", At: now}}
	if r, d := FirstMatch(TaskRows, idleRetro); r.ID != "N28" || !slices.Equal(d.Asked, []string{"stop_retro@4@0@c1"}) {
		t.Fatalf("N28 idle after nudge = (%s, %v), want (N28, [stop_retro@4@0@c1])", r.ID, d.Asked)
	}
	// After AskRetry, doneActs resets nudge_retro/stop_retro so nudge is asked again.
	a0(&idleRetro).Acts = append(a0(&idleRetro).Acts,
		Act{Key: "stop_retro@4@0@c1", Result: "done", At: now},
		Act{Key: AskRetry, Result: "done", At: now},
	)
	if r, d := FirstMatch(TaskRows, idleRetro); r.ID != "N28" || !slices.Equal(d.Asked, []string{"nudge_retro@4@0@c1"}) {
		t.Fatalf("N28 idle after retry = (%s, %v), want (N28, [nudge_retro@4@0@c1])", r.ID, d.Asked)
	}

	oldRetro := rBase(func(in *In) {
		a0(in).Review = &ReviewRun{Round: 4, Kind: RoundRetro, Head: "c1", Age: time.Duration(s.ReviewMinutes) * time.Minute}
		in.Facts.Reviewer = Run{State: RunWorking}
	})
	if r, d := FirstMatch(TaskRows, oldRetro); r.ID != "N28" || !slices.Equal(d.Asked, []string{"stop_retro@4@0@c1"}) {
		t.Fatalf("N28 past review_minutes = (%s, %v), want (N28, [stop_retro@4@0@c1])", r.ID, d.Asked)
	}
}

// TestKBRET9AndKBRET11RetroExclusionsAndGates checks KB-RET-9 and KB-RET-11:
//   - KB-RET-9: RoundRetro is excluded from reviewer-round counts (FirstLookPassRate,
//     LastReviewed, RejectableRound); an unrelated open CI recap survives retro SHIP (N6 > N42).
//   - KB-RET-11: a design task with design_gate=owner that gets retro SHIP passes
//     the ladder via L6 but still holds at N31 until the owner approves Merge.
func TestKBRET9AndKBRET11RetroExclusionsAndGates(t *testing.T) {
	// KB-RET-9: RoundRetro is not counted as a reviewer round.
	a := Attempt{
		BaseCommit: "b0",
		Ready:      []Ready{{Head: "c1"}},
		Rounds: []Round{
			{N: 1, Kind: RoundRetro, Head: "c1", Verdict: VerdictShip, Majors: 0},
		},
	}
	if passed, reviewed := FirstLookPassRate([]Task{{ID: "t1", Attempts: []Attempt{a}}}); passed != 0 || reviewed != 0 {
		t.Fatalf("FirstLookPassRate with only RoundRetro = (%d, %d), want (0, 0)", passed, reviewed)
	}
	if lr := LastReviewed(a); lr != nil {
		t.Fatalf("LastReviewed with only RoundRetro = %+v, want nil", lr)
	}

	// KB-RET-9: open CI recap survives retro SHIP (N6 beats N42, while Ladder says L6 Passed).
	shipWithCIRecap := with(retroShip, ciRecap)
	if r, d := FirstMatch(LadderRows, shipWithCIRecap); r.ID != "L6" || d.Why != Passed {
		t.Fatalf("Ladder on retro SHIP with CI recap = (%s, %s), want (L6, passed)", r.ID, d.Why)
	}
	if r, _ := FirstMatch(TaskRows, shipWithCIRecap); r.ID != "N6" {
		t.Fatalf("TaskRows on retro SHIP with open CI recap = %s, want N6", r.ID)
	}

	// KB-RET-11: design task with design_gate=owner and retro SHIP passes L6 but still holds at N31.
	designShip := with(retroShip, designTask, designGateOn)
	if r, d := FirstMatch(LadderRows, designShip); r.ID != "L6" || d.Why != Passed {
		t.Fatalf("Ladder on design retro SHIP = (%s, %s), want (L6, passed)", r.ID, d.Why)
	}
	if r, d := FirstMatch(TaskRows, designShip); r.ID != "N31" || !slices.Equal(d.Asked, []string{"open_gate_ask@c1"}) {
		t.Fatalf("TaskRows on design retro SHIP (design_gate=owner) = (%s, %v), want (N31, [open_gate_ask@c1])", r.ID, d.Asked)
	}
	designShip.Asks = []Ask{answeredN31Ask("ask-gate-1", "t", "c1", GateOptionMerge, now)}
	if r, d := FirstMatch(TaskRows, designShip); r.ID != "N42" || !slices.Equal(d.Asked, []string{"record_accept@c1", "merge@c1"}) {
		t.Fatalf("TaskRows on design retro SHIP after Merge approval = (%s, %v), want (N42, accept+merge)", r.ID, d.Asked)
	}
}

// TestProbeL2L3AfterSpentOverride checks design.md 4.3b (N19b: spent override
// resumes normal rules) and 4.6 (L2/L3/L4/L5): an override-decided recap whose
// override was later spent must resume at L2/L3 (never fall through to L-/N43,
// grant a 3rd FIX via L4, launch a 3rd retro via L5, or send RetroUnavailable
// to L5/N28).
func TestProbeL2L3AfterSpentOverride(t *testing.T) {
	t.Run("B1a_L2_redesign_spent_override_no_N43", func(t *testing.T) {
		in := with(retroRedesign, withOverride("c1", ChecksGreen, true))
		in.Task.Retros[0].At = now.Add(-5 * time.Minute)
		in.Task.Recaps = []Recap{{
			Trigger: TriggerReview,
			Decided: &RecapDecision{Choice: "override", At: now.Add(-time.Minute)},
		}}
		if r, d := FirstMatch(LadderRows, in); r.ID != "L2" || d.Why != RecapAnswer {
			t.Fatalf("after spent override on REDESIGN: got (%s, %q), want (L2, %q)", r.ID, d.Why, RecapAnswer)
		}
	})

	t.Run("B1b_L3_second_fix_spent_override_no_L4_or_L5", func(t *testing.T) {
		inA := with(retroSecondFix, withOverride("c1", ChecksGreen, true))
		inA.Task.Retros[0].At = now.Add(-10 * time.Minute)
		inA.Task.Retros[1].At = now.Add(-5 * time.Minute)
		inA.Task.Recaps = []Recap{{
			Trigger: TriggerReview,
			Decided: &RecapDecision{Choice: "override", At: now.Add(-time.Minute)},
		}}
		if r, d := FirstMatch(LadderRows, inA); r.ID != "L3" || d.Why != RecapAnswer {
			t.Fatalf("after spent override on 2nd FIX at c1: got (%s, %q), want (L3, %q)", r.ID, d.Why, RecapAnswer)
		}

		// Worker committing c2 after 2nd FIX + spent override must still hit L3, never L5 (3rd retro).
		inB := inA
		inB.Task.Attempts = slices.Clone(inA.Task.Attempts)
		a0(&inB).Ready = append(slices.Clone(a0(&inA).Ready), Ready{Head: "c2"})
		inB.Facts.Tip, inB.Facts.PR.Head = "c2", "c2"
		if r, d := FirstMatch(LadderRows, inB); r.ID != "L3" || d.Why != RecapAnswer {
			t.Fatalf("after spent override on 2nd FIX + new commit c2: got (%s, %q), want (L3, %q)", r.ID, d.Why, RecapAnswer)
		}
	})

	t.Run("B1c_L3_failed_delta_spent_override_no_unavailable_L5", func(t *testing.T) {
		inC := with(retroFixReadyC2, retroUnavail, withOverride("c2", ChecksGreen, true))
		inC.Task.Retros[0].At = now.Add(-5 * time.Minute)
		inC.Task.Recaps = []Recap{{
			Trigger: TriggerReview,
			Decided: &RecapDecision{Choice: "override", At: now.Add(-time.Minute)},
		}}
		if r, d := FirstMatch(LadderRows, inC); r.ID != "L3" || d.Why != RecapAnswer {
			t.Fatalf("after spent override on failed delta check: got (%s, %q), want (L3, %q)", r.ID, d.Why, RecapAnswer)
		}
	})
}

// TestProbeTaskHadRetroDeleted checks plan.md 12.1: Task.HadRetro is deleted
// outright in 12.1 and replaced by len(parent.Retros) > 0.
func TestProbeTaskHadRetroDeleted(t *testing.T) {
	if _, ok := reflect.TypeOf(Task{}).FieldByName("HadRetro"); ok {
		t.Fatal("Task.HadRetro still exists; plan.md 12.1 deletes it outright")
	}
}
