package flow

// PR 7.2, TDD step 1: pure flow table, guard, invariant, history, and O26
// owner-result-line tests for the override rules (design.md 3, 4.1 N6, 4.3
// N12–N14, 4.3b N15–N19b & OR1–OR6/OF1–OF5, 4.6 L1, 8 I1/I4, 12.1; review-2.md
// §2 except m3; notes/verify-review2.md M1 first half; plan.md 7.2).

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func withOverride(head string, shown Checks, spent bool) func(*In) { //nolint:unparam // helper kept general for later histories
	return func(in *In) {
		a0(in).Overrides = append(a0(in).Overrides, Override{
			Head:        head,
			ChecksShown: shown,
			Spent:       spent,
			ProofID:     "prf-ovr-1",
		})
	}
}

func isAcceptAndMergeAt(asked []string, h string) bool { //nolint:unparam // helper kept general for later histories
	return len(asked) == 2 &&
		strings.Contains(asked[0], "accept") && strings.HasSuffix(asked[0], "@"+h) &&
		asked[1] == key(AskMerge, h)
}

func isSpendOverrideAt(asked []string, h string) bool { //nolint:unparam // helper kept general for later histories
	return len(asked) == 1 && (asked[0] == AskSpendOverride || asked[0] == key(AskSpendOverride, h))
}

// TestLadderL1OverrideCoversH verifies design.md 4.6 L1 and plan.md 7.2
// (KB-ASK-16, KB-MRG-7): L1 answers Passed whenever an unspent override covers
// H (including merge_only advances), beating L7–L13, while a spent override or
// a newer non-merge_only commit is not covered and falls through to normal
// ladder rules.
func TestLadderL1OverrideCoversH(t *testing.T) {
	cases := []struct {
		name     string
		in       In
		wantRow  string
		wantAns  string
		mustBeL1 bool
	}{
		{
			name:     "unspent_override_at_H_with_zero_rounds_matches_L1",
			in:       with(noRounds, withOverride("c1", ChecksPending, false)),
			wantRow:  "L1",
			wantAns:  Passed,
			mustBeL1: true,
		},
		{
			name:     "unspent_override_at_H_with_one_pass_beats_L8",
			in:       with(onePass, withOverride("c1", ChecksGreen, false)),
			wantRow:  "L1",
			wantAns:  Passed,
			mustBeL1: true,
		},
		{
			name:     "unspent_override_at_H_with_changes_beats_L13",
			in:       with(changes, withOverride("c1", ChecksGreen, false)),
			wantRow:  "L1",
			wantAns:  Passed,
			mustBeL1: true,
		},
		{
			name:     "unspent_override_at_H_with_rejected_round_beats_L10",
			in:       with(changes, rejected, withOverride("c1", ChecksGreen, false)),
			wantRow:  "L1",
			wantAns:  Passed,
			mustBeL1: true,
		},
		{
			name:     "unspent_override_at_H_with_major_streak_beats_L12",
			in:       with(streakRecap, withOverride("c1", ChecksGreen, false)),
			wantRow:  "L1",
			wantAns:  Passed,
			mustBeL1: true,
		},
		{
			name:     "unspent_override_at_c1_covers_merge_only_m2",
			in:       with(noRounds, movedToM2, withOverride("c1", ChecksPending, false)),
			wantRow:  "L1",
			wantAns:  Passed,
			mustBeL1: true,
		},
		{
			name:    "spent_override_at_H_covers_nothing_falls_to_L7",
			in:      with(noRounds, withOverride("c1", ChecksGreen, true)),
			wantRow: "L7",
			wantAns: Review,
		},
		{
			name:    "spent_override_with_changes_falls_to_L13",
			in:      with(changes, withOverride("c1", ChecksGreen, true)),
			wantRow: "L13",
			wantAns: Fix,
		},
		{
			name: "override_at_c1_does_not_cover_newer_commit_c2_KB_MRG_7",
			in: with(noRounds, withOverride("c1", ChecksGreen, false), func(in *In) {
				a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"})
				in.Facts.Tip, in.Facts.PR.Head = "c2", "c2"
			}),
			wantRow: "L7",
			wantAns: Review,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, d := FirstMatch(LadderRows, tc.in)
			if r.ID != tc.wantRow || d.Why != tc.wantAns || Ladder(tc.in) != tc.wantAns {
				t.Fatalf("FirstMatch(LadderRows) = (%s, %q), Ladder = %q; want (%s, %q)",
					r.ID, d.Why, Ladder(tc.in), tc.wantRow, tc.wantAns)
			}
			if tc.mustBeL1 && r.Stage != "ci" {
				t.Errorf("L1 Stage = %q, want %q", r.Stage, "ci")
			}
		})
	}
}

// TestOverrideBypassesLimitsN12N13N14AndClosesCoveredRecapN6 verifies design.md
// 4.1 N6, 4.3 N12–N14 (!overrideCovers), 4.9 recap closing on covered tasks,
// and plan.md 7.2 (KB-CAP-1, KB-CAP-18, KB-MRG-7).
func TestOverrideBypassesLimitsN12N13N14AndClosesCoveredRecapN6(t *testing.T) {
	linesTimedOut := func(in *In) {
		a0(in).SizeCap, in.Facts.Lines, in.Facts.LinesTimeout = 100, 0, true
	}

	t.Run("covered_override_bypasses_N12_N13_N14_and_open_recap_N6", func(t *testing.T) {
		cases := []struct {
			name string
			in   In
		}{
			{name: "N12_over_size_cap", in: with(noRounds, overCap, withOverride("c1", ChecksGreen, false))},
			{name: "N12_lines_timeout", in: with(noRounds, linesTimedOut, withOverride("c1", ChecksGreen, false))},
			{name: "N13_ci_fails_per_hour", in: with(noRounds, ciFailed3, withOverride("c1", ChecksGreen, false))},
			{name: "N14_over_build_hours", in: with(noRounds, overTime, withOverride("c1", ChecksGreen, false))},
			{name: "N6_open_size_recap_covered", in: with(noRounds, overCap, sizeRecap, withOverride("c1", ChecksGreen, false))},
			{name: "N6_open_review_recap_covered", in: with(streakRecap, openRecap, withOverride("c1", ChecksGreen, false))},
			{name: "N6_open_ci_recap_covered", in: with(noRounds, ciRecap, withOverride("c1", ChecksPending, false), pending)},
			{name: "N6_open_time_recap_covered", in: with(noRounds, timeRecap, withOverride("c1", ChecksGreen, false))},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				r, d := FirstMatch(TaskRows, tc.in)
				if r.ID != "N19" || !isAcceptAndMergeAt(d.Asked, "c1") {
					t.Fatalf("FirstMatch = %s (asked=%v, why=%q), want N19 accept+merge@c1", r.ID, d.Asked, d.Why)
				}
			})
		}
	})

	t.Run("spent_or_uncovered_override_still_enforces_N6_N12_N13_N14", func(t *testing.T) {
		movedToC2 := func(in *In) {
			a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"})
			in.Facts.Tip, in.Facts.PR.Head = "c2", "c2"
		}
		cases := []struct {
			name    string
			in      In
			wantRow string
		}{
			{name: "spent_override_still_hits_N12", in: with(noRounds, overCap, withOverride("c1", ChecksGreen, true)), wantRow: "N12"},
			{name: "spent_override_still_hits_N13", in: with(noRounds, ciFailed3, withOverride("c1", ChecksGreen, true)), wantRow: "N13"},
			{name: "spent_override_still_hits_N14", in: with(noRounds, overTime, withOverride("c1", ChecksGreen, true)), wantRow: "N14"},
			{name: "uncovered_c2_still_hits_N6_open_recap", in: with(noRounds, openRecap, withOverride("c1", ChecksGreen, false), movedToC2), wantRow: "N6"},
			{name: "uncovered_c2_still_hits_N12_over_cap", in: with(noRounds, overCap, withOverride("c1", ChecksGreen, false), movedToC2), wantRow: "N12"},
			{name: "uncovered_c2_still_hits_N13_ci_fails", in: with(noRounds, ciFailed3, withOverride("c1", ChecksGreen, false), movedToC2), wantRow: "N13"},
			{name: "uncovered_c2_still_hits_N14_over_time", in: with(noRounds, overTime, withOverride("c1", ChecksGreen, false), movedToC2), wantRow: "N14"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				r, d := FirstMatch(TaskRows, tc.in)
				if r.ID != tc.wantRow {
					t.Fatalf("FirstMatch = %s (asked=%v, why=%q), want %s", r.ID, d.Asked, d.Why, tc.wantRow)
				}
			})
		}
	})
}

// TestOverrideRowsN15N16N17AndRowOrder verifies design.md 4.3b N15–N17,
// review-2.md §2 bullet 4 (N1–N5 and N7 sit above N15; N15–N17 sit above
// N19/N19b and N20–N23), and plan.md 7.2.
func TestOverrideRowsN15N16N17AndRowOrder(t *testing.T) {
	ovr := withOverride("c1", ChecksPending, false)

	cases := []struct {
		name      string
		in        In
		wantRow   string
		wantStage string
		wantAsked []string
	}{
		{
			name:      "N15_no_pr_pushes_H_even_with_unreported_tip_ahead",
			in:        with(noRounds, prGone, tipAheadF, ovr),
			wantRow:   "N15",
			wantStage: "ci",
			wantAsked: []string{"push_and_open_pr@c1"},
		},
		{
			name:      "N15_pr_behind_H_pushes_H",
			in:        with(noRounds, prBehind, ovr),
			wantRow:   "N15",
			wantStage: "ci",
			wantAsked: []string{"push_and_open_pr@c1"},
		},
		{
			name:      "N15_unanswered_pr_waits_without_blind_push",
			in:        with(noRounds, prGone, noAnswer, ovr),
			wantRow:   "N15",
			wantStage: "ci",
			wantAsked: nil,
		},
		{
			name:      "N16_conflict_tells_worker_once_never_merges",
			in:        with(noRounds, conflict, ovr),
			wantRow:   "N16",
			wantStage: "working",
			wantAsked: []string{"tell_worker_conflict@c1"},
		},
		{
			name: "N16_conflict_after_tell_waits_without_repeating",
			in: with(noRounds, conflict, ovr, func(in *In) {
				a0(in).Acts = []Act{{Key: "tell_worker_conflict@c1", Result: "done", At: now}}
			}),
			wantRow:   "N16",
			wantStage: "working",
			wantAsked: nil,
		},
		{
			name:      "N17_open_attempt_unmet_dep_waits",
			in:        with(noRounds, pending, waitDep, ovr),
			wantRow:   "N17",
			wantStage: "waiting_on_dep",
			wantAsked: nil,
		},
		{
			name:      "N17_open_attempt_other_merge_in_flight_waits_one_at_a_time",
			in:        with(noRounds, pending, ovr, func(in *In) { in.Facts.OtherMergeInFlight = true }),
			wantRow:   "N17",
			wantStage: "waiting_on_dep",
			wantAsked: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, d := FirstMatch(TaskRows, tc.in)
			if r.ID != tc.wantRow || !slices.Equal(d.Asked, tc.wantAsked) {
				t.Fatalf("FirstMatch = (%s, asked=%v, why=%q), want (%s, asked=%v)",
					r.ID, d.Asked, d.Why, tc.wantRow, tc.wantAsked)
			}
			if r.Stage != tc.wantStage {
				t.Errorf("%s Stage = %q, want %q", r.ID, r.Stage, tc.wantStage)
			}
		})
	}

	// Row ordering guards (review-2.md §2 bullet 4): N1, N2, N3, N4, N5, N7 all beat N15–N19b;
	// N15 beats N16/N17/N19; N16 beats N17/N19/N19b; N17 beats N19/N19b.
	orderCases := []struct {
		name string
		in   In
		want string
	}{
		{name: "N1_done_beats_override", in: with(done, ovr), want: "N1"},
		{name: "N2_merged_beats_override", in: with(merged, ovr), want: "N2"},
		{name: "N3_parked_beats_override", in: with(parked, ovr), want: "N3"},
		{name: "N4_closed_pr_beats_override", in: with(closedPR, ovr), want: "N4"},
		{
			name: "N5_exhausted_merge_failure_beats_override",
			in: with(noRounds, ovr, func(in *In) {
				a0(in).Acts = []Act{{Key: "merge@c1", Result: "failed", Class: ClassSettled, Err: "HTTP 405", At: now}}
			}),
			want: "N5",
		},
		{name: "N7_diverged_tip_beats_override", in: with(noRounds, diverged, ovr), want: "N7"},
		{
			name: "N7_foreign_pr_head_beats_override",
			in:   with(noRounds, ovr, func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "f8", RelAhead }),
			want: "N7",
		},
		{
			name: "N16_conflict_beats_N17_and_N19b",
			in: with(noRounds, conflict, red, waitDep, withOverride("c1", ChecksGreen, false), func(in *In) {
				in.Facts.OtherMergeInFlight = true
			}),
			want: "N16",
		},
		{
			name: "N17_other_merge_in_flight_beats_N19",
			in:   with(noRounds, ovr, func(in *In) { in.Facts.OtherMergeInFlight = true }),
			want: "N17",
		},
	}
	for _, tc := range orderCases {
		t.Run(tc.name, func(t *testing.T) {
			r, d := FirstMatch(TaskRows, tc.in)
			if r.ID != tc.want {
				t.Fatalf("FirstMatch = %s (asked=%v), want %s", r.ID, d.Asked, tc.want)
			}
			if slices.ContainsFunc(d.Asked, func(k string) bool { return strings.HasPrefix(k, AskMerge+"@") }) {
				t.Fatalf("%s unexpectedly asked merge: %v", r.ID, d.Asked)
			}
		})
	}
}

// TestOverrideN19AndN19bSnapshotGuardsAndChecksOrder verifies plan.md 7.2 and
// design.md 4.3b / 7.3 (KB-MRG-9, KB-MRG-10, KB-MRG-15):
//   - N19 and N19b hold ONLY on an answered snapshot with head H and GitHub
//     reachable (answered, githubAvail, prHeadIsH) so an outage or unread
//     snapshot never merges and never spends an override.
//   - Checks order is green < pending < red, with absent, unreadable, and
//     stopped_* counted as red.
//   - N19 merges when checks are no worse than shown (waiving reviews, pending
//     CI, behind base, red base, review_required, gate, and unreported tip).
//   - N19b asks spend_override and never merges when checks are worse than shown.
func TestOverrideN19AndN19bSnapshotGuardsAndChecksOrder(t *testing.T) {
	t.Run("snapshot_and_outage_guards_hold_without_merging_or_spending", func(t *testing.T) {
		guards := map[string]func(*In){
			"unanswered_snapshot": noAnswer,
			"github_unavailable":  func(in *In) { in.Facts.GitHubUnavailable = true },
			"pr_head_not_H":       func(in *In) { in.Facts.PR.Head, in.Facts.PR.HeadRel = "ext2", "" },
		}
		for name, mutate := range guards {
			for _, checks := range []Checks{ChecksGreen, ChecksUnreadable, ChecksRed} {
				t.Run(name+"_"+string(checks), func(t *testing.T) {
					in := with(noRounds, withOverride("c1", ChecksGreen, false), func(in *In) {
						in.Facts.PR.Checks = checks
					}, mutate)
					r, d := FirstMatch(TaskRows, in)
					if r.ID == "N19" || r.ID == "N19b" {
						t.Fatalf("FirstMatch = %s (asked=%v), want neither N19 nor N19b during %s", r.ID, d.Asked, name)
					}
					for _, k := range d.Asked {
						if strings.HasPrefix(k, AskMerge+"@") || strings.HasPrefix(k, AskSpendOverride) {
							t.Fatalf("%s asked %q (%v), must neither merge nor spend override", name, k, d.Asked)
						}
					}
				})
			}
		}
	})

	t.Run("checks_ordering_green_lt_pending_lt_red_classes", func(t *testing.T) {
		redLikes := []Checks{
			ChecksRed,
			ChecksAbsent,
			ChecksUnreadable,
			ChecksStoppedTimeout,
			ChecksStoppedExternal,
			ChecksStoppedUnknown,
		}

		// Shown green: only green merges (N19); pending and every red-like class hit N19b.
		inGreen := with(noRounds, withOverride("c1", ChecksGreen, false))
		if r, d := FirstMatch(TaskRows, inGreen); r.ID != "N19" || !isAcceptAndMergeAt(d.Asked, "c1") {
			t.Fatalf("shown=green current=green: got (%s, %v), want N19 accept+merge@c1", r.ID, d.Asked)
		}
		for _, worse := range append([]Checks{ChecksPending}, redLikes...) {
			in := with(noRounds, withOverride("c1", ChecksGreen, false), func(in *In) { in.Facts.PR.Checks = worse })
			r, d := FirstMatch(TaskRows, in)
			if r.ID != "N19b" || !isSpendOverrideAt(d.Asked, "c1") {
				t.Fatalf("shown=green current=%s: got (%s, %v), want N19b spend_override", worse, r.ID, d.Asked)
			}
		}

		// Shown pending: green and pending merge (N19); every red-like class hits N19b.
		for _, okChk := range []Checks{ChecksGreen, ChecksPending} {
			in := with(noRounds, withOverride("c1", ChecksPending, false), func(in *In) { in.Facts.PR.Checks = okChk })
			r, d := FirstMatch(TaskRows, in)
			if r.ID != "N19" || !isAcceptAndMergeAt(d.Asked, "c1") {
				t.Fatalf("shown=pending current=%s: got (%s, %v), want N19 accept+merge@c1", okChk, r.ID, d.Asked)
			}
		}
		for _, worse := range redLikes {
			in := with(noRounds, withOverride("c1", ChecksPending, false), func(in *In) { in.Facts.PR.Checks = worse })
			r, d := FirstMatch(TaskRows, in)
			if r.ID != "N19b" || !isSpendOverrideAt(d.Asked, "c1") {
				t.Fatalf("shown=pending current=%s: got (%s, %v), want N19b spend_override", worse, r.ID, d.Asked)
			}
		}

		// Shown red (or absent): green, pending, and red-like classes are no worse -> N19 merges (D10).
		for _, shown := range []Checks{ChecksRed, ChecksAbsent} {
			for _, cur := range []Checks{ChecksGreen, ChecksPending, ChecksRed, ChecksAbsent, ChecksStoppedExternal} {
				in := with(noRounds, withOverride("c1", shown, false), func(in *In) { in.Facts.PR.Checks = cur })
				r, d := FirstMatch(TaskRows, in)
				if r.ID != "N19" || !isAcceptAndMergeAt(d.Asked, "c1") {
					t.Fatalf("shown=%s current=%s: got (%s, %v), want N19 accept+merge@c1", shown, cur, r.ID, d.Asked)
				}
			}
		}
	})

	t.Run("N19_waives_reviews_pending_behind_base_red_gate_working_and_unreported_tip", func(t *testing.T) {
		in := with(
			noRounds,
			pending,
			behind,
			baseRedF,
			reviewReq,
			gateOwnerTask,
			workingF,
			tipAheadF,
			withOverride("c1", ChecksPending, false),
		)
		r, d := FirstMatch(TaskRows, in)
		if r.ID != "N19" || !isAcceptAndMergeAt(d.Asked, "c1") {
			t.Fatalf("N19 with waived landing holds = (%s, %v, %q), want N19 accept+merge@c1", r.ID, d.Asked, d.Why)
		}

		// Once accept and merge@c1 are recorded done while PR is still open, next pass waits without re-asking or hitting N19b.
		for _, k := range d.Asked {
			a0(&in).Acts = append(a0(&in).Acts, Act{Key: k, Result: "done", At: now})
		}
		a0(&in).Accepts = append(a0(&in).Accepts, Accept{Head: "c1", By: "override", Waived: []string{"review", "checks"}, ProofID: "prf-ovr-1"})
		r2, d2 := FirstMatch(TaskRows, in)
		if r2.ID == "N19b" || len(d2.Asked) != 0 {
			t.Fatalf("after merge@c1 asked while PR still open: got (%s, %v), want wait with nil asks (not N19b)", r2.ID, d2.Asked)
		}

		// Once PR snapshot reads merged, N2 marks done without merged_outside_bar wake.
		in.Facts.PR.State, in.Facts.PR.MergedCommit = PRMerged, "m-squash"
		r3, d3 := FirstMatch(TaskRows, in)
		if r3.ID != "N2" || !slices.Equal(d3.Asked, []string{AskMarkDone}) {
			t.Fatalf("after override PR merged: got (%s, %v), want (N2, [%s])", r3.ID, d3.Asked, AskMarkDone)
		}
	})
}

// TestFlowHistoriesReview2Section2AndVerifyReview2M1 walks the pass-by-pass
// histories required by plan.md slice 7 done-when and row 7.2:
//  1. review-2.md §2 bullet 1 & m2: card made at c2, worker reports ready at c3 ->
//     c2 override never covers c3; c3 goes through normal review & CI.
//  2. review-2.md §2 bullet 2 & m2: 0 rounds, unreported commit in worktree,
//     CI pending -> N15 pushes c1, N19 merges c1 leaving unreported work out.
//  3. review-2.md §2 bullet 4: conflicting PR hits N16 (tells worker once);
//     hand-resolved merge commit c2 is changes, so not covered -> normal review.
//  4. notes/verify-review2.md M1 first half (green -> red on reviewed PR):
//     hits N19b once (spend_override), marks spent, next pass reaches N33
//     instead of looping on N19b.
//  5. notes/verify-review2.md M1 + outage guard (pending -> outage -> red on
//     unreviewed PR): outage holds without spending; recovery at red hits N19b
//     once, then returns to N29 -> N33.
func TestFlowHistoriesReview2Section2AndVerifyReview2M1(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)

	t.Run("card_at_c2_worker_ready_at_c3_never_merges_c3_under_override", func(t *testing.T) {
		w := &world{
			task: Task{ID: "T1", Title: "feat: commit bound", Attempts: []Attempt{{
				BaseCommit: "b0", Branch: "t1", PR: 7,
				Ready:     []Ready{{Head: "c2"}, {Head: "c3"}},
				Overrides: []Override{{Head: "c2", ChecksShown: ChecksPending, Spent: false, ProofID: "prf-1"}},
				Acts:      []Act{{Key: "new_attempt", Result: "done"}, {Key: "start_worker", Result: "done"}, {Key: "push_and_open_pr@c2", Result: "done"}},
			}}},
			facts: Facts{
				Tip: "c3", TipRel: RelSame, Worker: Run{State: RunIdle},
				PR: PR{Answered: true, Number: 7, State: PROpen, Head: "c2", HeadRel: RelBehind, Checks: ChecksPending, Mergeable: MergeableClean},
			},
		}
		steps := []step{
			{"N26", []string{"push_and_open_pr@c3"}, func(w *world) {
				w.facts.PR.Head, w.facts.PR.HeadRel = "c3", RelSame
			}},
			{"N29", []string{"start_reviewer@1@c3"}, func(w *world) {
				w.att().Rounds = append(w.att().Rounds, Round{N: 1, Kind: RoundFull, Head: "c3", Verdict: VerdictPass})
				w.facts.Reviewer = Run{}
			}},
			{"N29", []string{"start_reviewer@2@c3"}, func(w *world) {
				w.att().Rounds = append(w.att().Rounds, Round{N: 2, Kind: RoundFull, Head: "c3", Verdict: VerdictPass})
				w.facts.Reviewer = Run{State: RunGone}
			}},
			{"N36", nil, func(w *world) {
				w.facts.PR.Checks = ChecksGreen
			}},
			{"N42", []string{"record_accept@c3", "merge@c3"}, func(w *world) {
				w.att().Accepts = append(w.att().Accepts, Accept{Head: "c3", By: "bar", Rounds: []int{1, 2}})
				w.facts.PR.State, w.facts.PR.MergedCommit = PRMerged, "m3"
			}},
			{"N2", []string{"mark_done"}, nil},
		}
		for i, s := range steps {
			in := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0.Add(time.Duration(i) * PassInterval)}
			r, d := FirstMatch(TaskRows, in)
			if r.ID != s.row || !slices.Equal(d.Asked, s.asked) {
				t.Fatalf("pass %d: got (%s, %v), want (%s, %v)", i+1, r.ID, d.Asked, s.row, s.asked)
			}
			for _, k := range d.Asked {
				w.att().Acts = append(w.att().Acts, Act{Key: k, Result: "done", At: in.Now})
			}
			if s.then != nil {
				s.then(w)
			}
		}
	})

	t.Run("zero_rounds_unreported_tip_and_pending_ci_merges_H_by_override", func(t *testing.T) {
		w := &world{
			task: Task{ID: "T1", Title: "feat: ship H now", Attempts: []Attempt{{
				BaseCommit: "b0", Branch: "t1",
				Ready:     []Ready{{Head: "c1"}},
				Overrides: []Override{{Head: "c1", ChecksShown: ChecksAbsent, Spent: false, ProofID: "prf-2"}},
				Acts:      []Act{{Key: "new_attempt", Result: "done"}, {Key: "start_worker", Result: "done"}},
			}}},
			facts: Facts{
				Tip: "c-unreported", TipRel: RelAhead, Worker: Run{State: RunIdle},
				PR: PR{Answered: true},
			},
		}

		// Pass 1: N15 pushes c1 and opens PR #7 (leaving unreported work in worktree out).
		in1 := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0}
		r1, d1 := FirstMatch(TaskRows, in1)
		if r1.ID != "N15" || !slices.Equal(d1.Asked, []string{"push_and_open_pr@c1"}) {
			t.Fatalf("pass 1: got (%s, %v), want (N15, [push_and_open_pr@c1])", r1.ID, d1.Asked)
		}
		w.att().Acts = append(w.att().Acts, Act{Key: d1.Asked[0], Result: "done", At: in1.Now})
		w.att().PR = 7
		w.facts.PR = PR{Answered: true, Number: 7, State: PROpen, Head: "c1", HeadRel: RelSame, Checks: ChecksPending, Mergeable: MergeableClean}

		// Pass 2: N19 records override accept and merges c1 without waiting for CI or review.
		in2 := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0.Add(PassInterval)}
		r2, d2 := FirstMatch(TaskRows, in2)
		if r2.ID != "N19" || !isAcceptAndMergeAt(d2.Asked, "c1") {
			t.Fatalf("pass 2: got (%s, %v), want N19 accept+merge@c1", r2.ID, d2.Asked)
		}
		for _, k := range d2.Asked {
			w.att().Acts = append(w.att().Acts, Act{Key: k, Result: "done", At: in2.Now})
		}
		w.att().Accepts = append(w.att().Accepts, Accept{Head: "c1", By: "override", Waived: []string{"review", "checks"}, ProofID: "prf-2"})

		// Pass 3: PR still open -> waits without re-asking merge or falling into N19b.
		in3 := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0.Add(2 * PassInterval)}
		r3, d3 := FirstMatch(TaskRows, in3)
		if r3.ID == "N19b" || r3.ID == "N43" || len(d3.Asked) != 0 {
			t.Fatalf("pass 3: got (%s, %v), want wait with nil asks", r3.ID, d3.Asked)
		}

		// Pass 4: PR reads merged -> N2 marks done, no merged_outside_bar wake.
		w.facts.PR.State, w.facts.PR.MergedCommit = PRMerged, "m1"
		in4 := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0.Add(3 * PassInterval)}
		r4, d4 := FirstMatch(TaskRows, in4)
		if r4.ID != "N2" || !slices.Equal(d4.Asked, []string{"mark_done"}) {
			t.Fatalf("pass 4: got (%s, %v), want (N2, [mark_done])", r4.ID, d4.Asked)
		}
	})

	t.Run("conflict_hits_N16_and_hand_resolved_merge_is_not_covered", func(t *testing.T) {
		w := &world{
			task: Task{ID: "T1", Title: "feat: conflicted override", Attempts: []Attempt{{
				BaseCommit: "b0", Branch: "t1", PR: 7,
				Ready:     []Ready{{Head: "c1"}},
				Overrides: []Override{{Head: "c1", ChecksShown: ChecksGreen, Spent: false, ProofID: "prf-3"}},
				Acts:      []Act{{Key: "new_attempt", Result: "done"}, {Key: "start_worker", Result: "done"}, {Key: "push_and_open_pr@c1", Result: "done"}},
			}}},
			facts: Facts{
				Tip: "c1", TipRel: RelSame, Worker: Run{State: RunIdle},
				PR: PR{Answered: true, Number: 7, State: PROpen, Head: "c1", HeadRel: RelSame, Checks: ChecksGreen, Mergeable: MergeableConflict},
			},
		}

		// Pass 1: N16 tells worker once to merge base.
		in1 := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0}
		r1, d1 := FirstMatch(TaskRows, in1)
		if r1.ID != "N16" || !slices.Equal(d1.Asked, []string{"tell_worker_conflict@c1"}) {
			t.Fatalf("pass 1: got (%s, %v), want (N16, [tell_worker_conflict@c1])", r1.ID, d1.Asked)
		}
		w.att().Acts = append(w.att().Acts, Act{Key: "tell_worker_conflict@c1", Result: "done", At: in1.Now})

		// Pass 2: still conflicted -> N16 waits without re-telling.
		in2 := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0.Add(PassInterval)}
		if r2, d2 := FirstMatch(TaskRows, in2); r2.ID != "N16" || len(d2.Asked) != 0 {
			t.Fatalf("pass 2: got (%s, %v), want (N16, nil)", r2.ID, d2.Asked)
		}

		// Worker resolves conflict with non-merge_only commit c2 and reports ready -> c1 override no longer covers c2.
		w.att().Ready = append(w.att().Ready, Ready{Head: "c2"})
		w.facts.Tip, w.facts.TipRel = "c2", RelSame
		w.facts.PR.HeadRel, w.facts.PR.Mergeable = RelBehind, MergeableClean

		in3 := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0.Add(2 * PassInterval)}
		if r3, d3 := FirstMatch(TaskRows, in3); r3.ID != "N26" || !slices.Equal(d3.Asked, []string{"push_and_open_pr@c2"}) {
			t.Fatalf("pass 3 (hand-resolved c2): got (%s, %v), want (N26, [push_and_open_pr@c2])", r3.ID, d3.Asked)
		}
		w.att().Acts = append(w.att().Acts, Act{Key: "push_and_open_pr@c2", Result: "done", At: in3.Now})
		w.facts.PR.Head, w.facts.PR.HeadRel = "c2", RelSame

		in4 := In{Task: w.task, Settings: DefaultSettings(), Facts: w.facts, Now: t0.Add(3 * PassInterval)}
		if r4, d4 := FirstMatch(TaskRows, in4); r4.ID != "N29" || !slices.Equal(d4.Asked, []string{"start_reviewer@1@c2"}) {
			t.Fatalf("pass 4 (hand-resolved c2 enters review): got (%s, %v), want (N29, [start_reviewer@1@c2])", r4.ID, d4.Asked)
		}
	})

	t.Run("verify_review2_M1_green_to_red_spends_override_and_reaches_N33", func(t *testing.T) {
		in := with(
			red,
			func(in *In) { in.Facts.PR.Failed = []Failure{{Run: "42", Job: "100"}} },
			withOverride("c1", ChecksGreen, false),
		)

		// Pass 1: checks worse than shown (red > green) -> N19b asks spend_override, never merges.
		r1, d1 := FirstMatch(TaskRows, in)
		if r1.ID != "N19b" || !isSpendOverrideAt(d1.Asked, "c1") {
			t.Fatalf("pass 1: got (%s, %v), want N19b spend_override", r1.ID, d1.Asked)
		}
		a0(&in).Acts = append(a0(&in).Acts, Act{Key: d1.Asked[0], Result: "done", At: now})
		a0(&in).Overrides[0].Spent = true

		// Pass 2: override is spent -> does NOT stick on N19b; reaches N33!
		in.Now = now.Add(PassInterval)
		r2, d2 := FirstMatch(TaskRows, in)
		if r2.ID != "N33" || !slices.Equal(d2.Asked, []string{"rerun_ci@42@100@c1"}) {
			t.Fatalf("pass 2 after spend_override: got (%s, %v), want (N33, [rerun_ci@42@100@c1])", r2.ID, d2.Asked)
		}
		a0(&in).Acts = append(a0(&in).Acts, Act{Key: d2.Asked[0], Result: "done", At: in.Now})

		// Pass 3: rerun also fails -> N33 tells worker once.
		in.Facts.PR.Failed = []Failure{{Run: "42", Job: "200"}}
		in.Now = now.Add(2 * PassInterval)
		r3, d3 := FirstMatch(TaskRows, in)
		if r3.ID != "N33" || !slices.Equal(d3.Asked, []string{"tell_worker_ci@42@c1"}) {
			t.Fatalf("pass 3: got (%s, %v), want (N33, [tell_worker_ci@42@c1])", r3.ID, d3.Asked)
		}
	})

	t.Run("verify_review2_M1_pending_to_outage_to_red_unreviewed_spends_only_after_recovery", func(t *testing.T) {
		in := with(
			noRounds,
			func(in *In) {
				in.Settings.FlakeReruns = 0
				in.Facts.PR.Answered = false
				in.Facts.PR.Checks = ChecksUnreadable
				in.Facts.GitHubUnavailable = true
			},
			withOverride("c1", ChecksPending, false),
		)

		// Pass 1 (GitHub outage): must hold at N36 without spending override.
		r1, d1 := FirstMatch(TaskRows, in)
		if r1.ID != "N36" || len(d1.Asked) != 0 {
			t.Fatalf("pass 1 (outage): got (%s, %v), want (N36, nil)", r1.ID, d1.Asked)
		}

		// Pass 2 (GitHub recovers, CI finished red): N19b spends override.
		in.Facts.PR.Answered = true
		in.Facts.GitHubUnavailable = false
		in.Facts.PR.Checks = ChecksRed
		in.Facts.PR.Failed = []Failure{{Run: "88", Job: "10"}}
		in.Now = now.Add(PassInterval)
		r2, d2 := FirstMatch(TaskRows, in)
		if r2.ID != "N19b" || !isSpendOverrideAt(d2.Asked, "c1") {
			t.Fatalf("pass 2 (recovered red): got (%s, %v), want N19b spend_override", r2.ID, d2.Asked)
		}
		a0(&in).Acts = append(a0(&in).Acts, Act{Key: d2.Asked[0], Result: "done", At: in.Now})
		a0(&in).Overrides[0].Spent = true

		// Pass 3: spent override no longer passes L1 -> returns to N29 for review, then N33 once passed.
		in.Now = now.Add(2 * PassInterval)
		r3, d3 := FirstMatch(TaskRows, in)
		if r3.ID != "N29" || !slices.Equal(d3.Asked, []string{"start_reviewer@1@c1"}) {
			t.Fatalf("pass 3 (after spend, 0 rounds): got (%s, %v), want (N29, [start_reviewer@1@c1])", r3.ID, d3.Asked)
		}
		a0(&in).Rounds = []Round{
			{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictPass},
			{N: 2, Kind: RoundFull, Head: "c1", Verdict: VerdictPass},
		}
		in.Facts.Reviewer = Run{State: RunGone}
		in.Now = now.Add(3 * PassInterval)
		r4, d4 := FirstMatch(TaskRows, in)
		if r4.ID != "N33" || !slices.Equal(d4.Asked, []string{"tell_worker_ci@88@c1"}) {
			t.Fatalf("pass 4 (after 2 passes, red CI): got (%s, %v), want (N33, [tell_worker_ci@88@c1])", r4.ID, d4.Asked)
		}
	})
}

// TestCheckInvariantsI1AndI4WithOverride checks design.md 8 (I1, I4) and
// plan.md 7.2: when L1 passes via an unspent override covering H, any live
// reviewer run on that task violates I1 ("ladder says passed"), and an
// override merge with Accept{By: "override", Waived, ProofID} satisfies I4.
func TestCheckInvariantsI1AndI4WithOverride(t *testing.T) {
	t.Run("I1_flags_live_reviewer_when_L1_passes", func(t *testing.T) {
		snap := cleanSnapshot()
		a := &snap.State.Tasks[0].Attempts[0]
		a.Overrides = []Override{{Head: "c1", ChecksShown: ChecksPending, Spent: false, ProofID: "prf-1"}}
		a.Review = &ReviewRun{
			Round: 1, Head: "c1", RunID: "rev-1",
			Run: &RunRecord{Address: "rev-1", Role: RoleReviewer, State: PhaseWorking},
		}
		vs := CheckInvariants(snap)
		if !hasViolation(vs, "I1", "T1", "ladder says passed") {
			t.Fatalf("CheckInvariants with live reviewer under L1 override = %+v, want I1 'ladder says passed'", vs)
		}
	})

	t.Run("I4_accept_by_override_with_waived_and_proof_satisfies_merge", func(t *testing.T) {
		snap := cleanSnapshot()
		a := &snap.State.Tasks[0].Attempts[0]
		a.Overrides = []Override{{Head: "c1", ChecksShown: ChecksPending, Spent: false, ProofID: "prf-1"}}
		a.Accepts = []Accept{{Head: "c1", By: "override", Waived: []string{"review", "checks"}, ProofID: "prf-1"}}
		a.Acts = []Act{{Key: AskMerge + "@c1", Result: "done", At: snap.Now}}
		if vs := CheckInvariants(snap); len(vs) != 0 {
			t.Fatalf("CheckInvariants with override Accept = %+v, want none", vs)
		}
	})
}

// TestOverrideResultOwnerLineOR1ToOR6AndOF1ToOF4 tests flow.OverrideResult
// against every clause of O26 settled text in design.md 4.3b (OR1–OR6, OF1–OF5)
// and 12.1.
func TestOverrideResultOwnerLineOR1ToOR6AndOF1ToOF4(t *testing.T) {
	ansAt := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	grace := time.Duration(DefaultSettings().ChecksGraceMinutes) * time.Minute

	mkAsk := func(id string, targets ...OverrideTarget) Ask {
		return Ask{
			ID:         id,
			Project:    "proj-1",
			Kind:       KindOverride,
			Gated:      true,
			Question:   "Merge now?",
			Options:    []string{OverrideOptionMerge, OverrideOptionCancel},
			Recommend:  OverrideOptionCancel,
			Targets:    targets,
			AskedBy:    RoleCoordinator,
			At:         ansAt.Add(-time.Minute),
			Status:     AskStatusAnswered,
			Answer:     OverrideOptionMerge,
			AnsweredBy: "owner",
			AnsweredAt: ansAt,
			ProofID:    "prf-" + id,
			DeviceID:   "dev-phone-1",
		}
	}

	t.Run("OR1_OF2_two_merged_tasks_grouped_first", func(t *testing.T) {
		st := State{
			Settings: DefaultSettings(),
			Tasks: []Task{
				{
					ID: "T1", Done: &Merged{MergedCommit: "m1"}, LastRow: "N2",
					Attempts: []Attempt{{BaseCommit: "b0", Ready: []Ready{{Head: "c1"}}, Overrides: []Override{{Head: "c1", ChecksShown: ChecksPending, Spent: true, ProofID: "prf-a1"}}}},
				},
				{
					ID: "T2", Done: &Merged{MergedCommit: "m2"}, LastRow: "N2",
					Attempts: []Attempt{{BaseCommit: "b0", Ready: []Ready{{Head: "c2"}}, Overrides: []Override{{Head: "c2", ChecksShown: ChecksGreen, Spent: true, ProofID: "prf-a1"}}}},
				},
			},
			Asks: []Ask{mkAsk("a1", OverrideTarget{Task: "T1", Head: "c1", Checks: ChecksPending}, OverrideTarget{Task: "T2", Head: "c2", Checks: ChecksGreen})},
		}
		got := OverrideResult(st, ansAt.Add(time.Minute))
		want := []OverrideResultEvent{{AskID: "a1", Task: "T1", Rule: "OF2", Text: "T1, T2 merged"}}
		if !slices.Equal(got, want) {
			t.Fatalf("OverrideResult = %+v, want %+v", got, want)
		}

		// OF1: once override_result@a1 is done in T1.Acts, even after a retry act, never fires again.
		st.Tasks[0].Acts = []Act{
			{Key: "override_result@a1", Result: "done", At: ansAt.Add(time.Minute)},
			{Key: AskRetry, Result: "done", At: ansAt.Add(2 * time.Minute)},
		}
		if again := OverrideResult(st, ansAt.Add(10*time.Minute)); len(again) != 0 {
			t.Fatalf("OF1 failed to suppress already-fired card across retry: %+v", again)
		}
	})

	t.Run("OR2_OF2_spent_unmerged_override_reports_checks_got_worse", func(t *testing.T) {
		st := State{
			Settings: DefaultSettings(),
			Tasks: []Task{{
				ID: "T1", LastRow: "N19b",
				Attempts: []Attempt{{BaseCommit: "b0", Ready: []Ready{{Head: "c1"}}, Overrides: []Override{{Head: "c1", ChecksShown: ChecksPending, Spent: true, ProofID: "prf-a2"}}}},
			}},
			Asks: []Ask{mkAsk("a2", OverrideTarget{Task: "T1", Head: "c1", Checks: ChecksPending})},
		}
		got := OverrideResult(st, ansAt.Add(30*time.Second))
		want := []OverrideResultEvent{{AskID: "a2", Task: "T1", Rule: "OF2", Text: "T1 not merged: checks got worse after the card was made"}}
		if !slices.Equal(got, want) {
			t.Fatalf("OverrideResult(OR2) = %+v, want %+v", got, want)
		}
	})

	t.Run("OR1_OR3_OR4_OR5_mixed_settled_card_preserves_grouping_and_order", func(t *testing.T) {
		st := State{
			Settings: DefaultSettings(),
			Tasks: []Task{
				{
					// OR3: conflict tell at covered head at/after answered_at, even after worker committed c1b.
					ID: "T1", LastRow: "N29",
					Attempts: []Attempt{{
						BaseCommit: "b0", Ready: []Ready{{Head: "c1"}, {Head: "c1b"}},
						Overrides: []Override{{Head: "c1", ChecksShown: ChecksGreen, ProofID: "prf-a3"}},
						Acts:      []Act{{Key: "tell_worker_conflict@c1", Result: "done", At: ansAt}},
					}},
				},
				{
					// OR1: merged (grouped first with T4).
					ID: "T2", Done: &Merged{MergedCommit: "m2"}, LastRow: "N1",
					Attempts: []Attempt{{BaseCommit: "b0", Ready: []Ready{{Head: "c2"}}, Overrides: []Override{{Head: "c2", ChecksShown: ChecksGreen, Spent: true, ProofID: "prf-a3"}}}},
				},
				{
					// OR4: newer commit (H moved from c3 to c3-new without conflict).
					ID: "T3", LastRow: "N29",
					Attempts: []Attempt{{BaseCommit: "b0", Ready: []Ready{{Head: "c3"}, {Head: "c3-new"}}, Overrides: []Override{{Head: "c3", ChecksShown: ChecksPending, ProofID: "prf-a3"}}}},
				},
				{
					// OR1: merged (grouped first with T2 -> "T2, T4 merged").
					ID: "T4", Done: &Merged{MergedCommit: "m4"}, LastRow: "N2",
					Attempts: []Attempt{{BaseCommit: "b0", Ready: []Ready{{Head: "c4"}}, Overrides: []Override{{Head: "c4", ChecksShown: ChecksGreen, Spent: true, ProofID: "prf-a3"}}}},
				},
				{
					// OR5: parked / N4 / N5 / N7 / closed attempt -> stopped.
					ID: "T5", Parked: true, LastRow: "N3",
					Attempts: []Attempt{{BaseCommit: "b0", Ready: []Ready{{Head: "c5"}}, Overrides: []Override{{Head: "c5", ChecksShown: ChecksGreen, ProofID: "prf-a3"}}}},
				},
			},
			Asks: []Ask{mkAsk("a3",
				OverrideTarget{Task: "T1", Head: "c1", Checks: ChecksGreen},
				OverrideTarget{Task: "T2", Head: "c2", Checks: ChecksGreen},
				OverrideTarget{Task: "T3", Head: "c3", Checks: ChecksPending},
				OverrideTarget{Task: "T4", Head: "c4", Checks: ChecksGreen},
				OverrideTarget{Task: "T5", Head: "c5", Checks: ChecksGreen},
			)},
		}
		got := OverrideResult(st, ansAt.Add(time.Minute))
		want := []OverrideResultEvent{{
			AskID: "a3",
			Task:  "T1",
			Rule:  "OF2",
			Text:  "T2, T4 merged; T1 not merged: conflict with base; T3 not merged: newer commit; T5 not merged: stopped",
		}}
		if !slices.Equal(got, want) {
			t.Fatalf("OverrideResult(mixed) = %+v, want %+v", got, want)
		}
	})

	t.Run("holds_while_in_flight_then_fires_OF3_or_OF4", func(t *testing.T) {
		// T1 waiting at N19, T2 (After: T1) waiting at N17 -> T2's unmet dep T1 IS a waiting target of this card,
		// so neither OF2 nor OF3 nor OF4 holds before grace.
		st := State{
			Settings: DefaultSettings(),
			Tasks: []Task{
				{
					ID: "T1", LastRow: "N19",
					Attempts: []Attempt{{BaseCommit: "b0", Ready: []Ready{{Head: "c1"}}, Overrides: []Override{{Head: "c1", ChecksShown: ChecksPending, ProofID: "prf-a4"}}}},
				},
				{
					ID: "T2", After: []string{"T1"}, LastRow: "N17",
					Attempts: []Attempt{{BaseCommit: "b0", Ready: []Ready{{Head: "c2"}}, Overrides: []Override{{Head: "c2", ChecksShown: ChecksGreen, ProofID: "prf-a4"}}}},
				},
			},
			Asks: []Ask{mkAsk("a4", OverrideTarget{Task: "T1", Head: "c1", Checks: ChecksPending}, OverrideTarget{Task: "T2", Head: "c2", Checks: ChecksGreen})},
		}
		if got := OverrideResult(st, ansAt.Add(grace-time.Second)); len(got) != 0 {
			t.Fatalf("OverrideResult before settle/grace = %+v, want nil", got)
		}

		// OF4: at checks_grace_minutes from answered_at, fires waiting line without spending overrides.
		gotOF4 := OverrideResult(st, ansAt.Add(grace))
		wantOF4 := []OverrideResultEvent{{AskID: "a4", Task: "T1", Rule: "OF4", Text: "T1 waiting; T2 waiting on T1"}}
		if !slices.Equal(gotOF4, wantOF4) {
			t.Fatalf("OverrideResult(OF4) = %+v, want %+v", gotOF4, wantOF4)
		}

		// OF3: if T1 settles without merging (OR2), T2's unmet dep T1 is no longer a waiting target -> fires OF3 immediately before grace!
		st.Tasks[0].LastRow = "N19b"
		st.Tasks[0].Attempts[0].Overrides[0].Spent = true
		gotOF3 := OverrideResult(st, ansAt.Add(30*time.Second))
		wantOF3 := []OverrideResultEvent{{
			AskID: "a4",
			Task:  "T1",
			Rule:  "OF3",
			Text:  "T1 not merged: checks got worse after the card was made; T2 waiting on T1",
		}}
		if !slices.Equal(gotOF3, wantOF3) {
			t.Fatalf("OverrideResult(OF3) = %+v, want %+v", gotOF3, wantOF3)
		}
	})

	t.Run("ignores_open_cancelled_negative_and_non_override_asks", func(t *testing.T) {
		noAsk := mkAsk("a-no", OverrideTarget{Task: "T1", Head: "c1", Checks: ChecksGreen})
		noAsk.Answer = OverrideOptionCancel
		openAsk := mkAsk("a-open", OverrideTarget{Task: "T1", Head: "c1", Checks: ChecksGreen})
		openAsk.Status, openAsk.Answer = AskStatusOpen, ""
		st := State{
			Settings: DefaultSettings(),
			Tasks:    []Task{{ID: "T1", Done: &Merged{MergedCommit: "m1"}}},
			Asks:     []Ask{noAsk, openAsk},
		}
		if got := OverrideResult(st, ansAt.Add(grace)); len(got) != 0 {
			t.Fatalf("OverrideResult on non-Yes/open asks = %+v, want nil", got)
		}
	})
}
