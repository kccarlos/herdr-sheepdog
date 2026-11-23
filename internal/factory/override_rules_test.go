package factory

// PR 7.2, TDD step 1: engine history and view stage/why tests for the override
// rules (design.md 4.1 N2/N6, 4.3 N12–N14, 4.3b N15–N19b & O26 OR1–OR6/OF1–OF5,
// 4.6 L1, 4.9, 4.11, 8 I1/I4; review-2.md §2 except m3; notes/verify-review2.md
// M1 first half; plan.md 7.2; KB-ASK-15, KB-ASK-16, KB-ASK-19, KB-MRG-7,
// KB-MRG-9, KB-MRG-10).

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// TestOverrideHistoryTwoPRsMergeOneAtATimeAndEmitResultLine checks plan.md 7.2
// done-when: two open PRs (T1 pending checks with a live round-1 reviewer, T2
// green checks with 0 reviews) on one override card merge ONE AT A TIME after a
// paired tap Yes without waiting for CI or review (L1, I1 reviewer stop, N17
// OtherMergeInFlight, N19 Accept{By:"override", Waived, ProofID} + AskMerge, N2
// spent mark, KB-MRG-9, KB-MRG-10), and fire O26's owner result line once
// ("T1, T2 merged") when both settle (OF2, OF1 across restart).
func TestOverrideHistoryTwoPRsMergeOneAtATimeAndEmitResultLine(t *testing.T) {
	t.Parallel()
	w, _, c1, c2 := setupTwoTaskOverrideWorld(t)

	var invViolations []flow.InvariantViolation
	w.eng.onInvariant = func(vs []flow.InvariantViolation) {
		invViolations = append(invViolations, vs...)
	}

	// Start round-1 reviewer on T1 so T1 has a live reviewer when L1 activates (tests I1 stop).
	w.pass() // N29 starts reviewers for T1 and T2
	if st := w.state(); st.Tasks[0].Attempts[0].Review == nil || st.Tasks[0].Attempts[0].Review.RunID == "" {
		t.Fatalf("expected live reviewer on T1 before override, got %+v", st.Tasks[0].Attempts[0].Review)
	}

	if err := w.eng.Report(w.coordTok, "ask", json.RawMessage(`{"kind":"override","tasks":["T1","T2"]}`)); err != nil {
		t.Fatalf("coordinator override ask: %v", err)
	}
	ask := w.state().Asks[0]

	changes := w.eng.Changes()
	proof, err := w.eng.MintProof(ask.ID, flow.OverrideOptionMerge, ProofInputTap, "dev-phone-1")
	if err != nil {
		t.Fatalf("MintProof: %v", err)
	}
	if err := w.eng.Answer(ask.ID, flow.OverrideOptionMerge, proof); err != nil {
		t.Fatalf("Engine.Answer(Yes): %v", err)
	}
	_ = drainChanges(changes) // clear ask_answer change before pass 1

	// Pass 1: T1 stops its live reviewer (no I1 violation) and merges via N19;
	// T2 waits at N17 (OtherMergeInFlight) so only 1 merge runs on Pass 1.
	w.pass()
	if len(invViolations) != 0 {
		t.Fatalf("invariant violations on override pass = %+v, want none (I1 must stop live reviewer when L1 passes)", invViolations)
	}
	st := w.state()
	if st.Tasks[0].LastRow != "N19" {
		t.Fatalf("pass 1 T1 LastRow = %q, want N19", st.Tasks[0].LastRow)
	}
	if st.Tasks[1].LastRow != "N17" {
		t.Fatalf("pass 1 T2 LastRow = %q, want N17 (OtherMergeInFlight)", st.Tasks[1].LastRow)
	}
	merges1 := ghCalls(t, w.gh, "pr merge")
	if len(merges1) != 1 || !strings.Contains(merges1[0], " 7 ") || !strings.Contains(merges1[0], "--match-head-commit "+c1) {
		t.Fatalf("pass 1 pr merge calls = %v, want exactly 1 merge for PR #7 pinned to %s", merges1, c1)
	}
	// Accept record at H keeps I4, records By="override", ProofID, and non-empty Waived; override is NOT spent until N2.
	ac1 := st.Tasks[0].Attempts[0].Accepts
	if len(ac1) != 1 || ac1[0].Head != c1 || ac1[0].By != "override" || ac1[0].ProofID != proof.ID || len(ac1[0].Waived) == 0 {
		t.Fatalf("T1 Accepts = %+v, want [{Head:%s By:override Waived:non-empty ProofID:%s}]", ac1, c1, proof.ID)
	}
	if st.Tasks[0].Attempts[0].Overrides[0].Spent {
		t.Fatalf("T1 override marked Spent at N19 merge ask; must wait until N2 done (design.md 4.3b)")
	}
	if got := drainChanges(changes); slices.ContainsFunc(got, func(c Change) bool { return c.Kind == "override_result" }) {
		t.Fatalf("override_result emitted before both targets settled: %+v", got)
	}

	// PR #7 merges on GitHub -> Pass 2 marks T1 done + Spent at N2, and merges T2's PR #8 via N19.
	writeGHFile(t, w.gh, "view-7.json", `{"number":7,"state":"MERGED","headRefOid":"`+c1+`","mergeable":"UNKNOWN","statusCheckRollup":`+pendingRollup+`,"baseRefName":"main","mergeCommit":{"oid":"merged1111"}}`)
	w.pass()
	st = w.state()
	if st.Tasks[0].Done == nil || st.Tasks[0].Done.MergedCommit != "merged1111" {
		t.Fatalf("pass 2 T1 Done = %+v, want merged1111", st.Tasks[0].Done)
	}
	if !st.Tasks[0].Attempts[0].Overrides[0].Spent {
		t.Fatalf("pass 2 T1 override Spent = false, want true at N2")
	}
	if slices.ContainsFunc(st.Tasks[0].Attempts[0].Acts, func(a flow.Act) bool { return strings.Contains(a.Key, "merged_outside_bar") }) {
		t.Fatalf("T1 woke merged_outside_bar despite override Accept: %+v", st.Tasks[0].Attempts[0].Acts)
	}
	if st.Tasks[1].LastRow != "N19" {
		t.Fatalf("pass 2 T2 LastRow = %q, want N19 once T1 finished merging", st.Tasks[1].LastRow)
	}
	merges2 := ghCalls(t, w.gh, "pr merge")
	if len(merges2) != 2 || !strings.Contains(merges2[1], " 8 ") || !strings.Contains(merges2[1], "--match-head-commit "+c2) {
		t.Fatalf("pass 2 pr merge calls = %v, want 2nd merge for PR #8 pinned to %s", merges2, c2)
	}

	// PR #8 merges on GitHub -> Pass 3 marks T2 done + Spent at N2 and fires OF2 "T1, T2 merged" once.
	writeGHFile(t, w.gh, "view-8.json", `{"number":8,"state":"MERGED","headRefOid":"`+c2+`","mergeable":"UNKNOWN","statusCheckRollup":`+greenRollup+`,"baseRefName":"main","mergeCommit":{"oid":"merged2222"}}`)
	w.pass()
	st = w.state()
	if st.Tasks[1].Done == nil || !st.Tasks[1].Attempts[0].Overrides[0].Spent {
		t.Fatalf("pass 3 T2 Done=%+v Overrides=%+v, want done and spent", st.Tasks[1].Done, st.Tasks[1].Attempts[0].Overrides)
	}
	if !flow.Done(st.Tasks[0].Acts, "override_result@"+ask.ID) {
		t.Fatalf("T1.Acts missing done override_result@%s: %+v", ask.ID, st.Tasks[0].Acts)
	}
	gotChanges := drainChanges(changes)
	var resChanges []Change
	for _, c := range gotChanges {
		if c.Kind == "override_result" {
			resChanges = append(resChanges, c)
		}
	}
	if len(resChanges) != 1 || resChanges[0].Text != "T1, T2 merged" {
		t.Fatalf("override_result Changes = %+v, want 1 with Text %q", resChanges, "T1, T2 merged")
	}
	logBytes, err := os.ReadFile(w.paths.Log(w.id))
	if err != nil {
		t.Fatalf("read log.jsonl: %v", err)
	}
	if !strings.Contains(string(logBytes), `"rule":"OF2"`) || !strings.Contains(string(logBytes), "T1, T2 merged") {
		t.Errorf("log.jsonl missing OF2 override result 'T1, T2 merged':\n%s", logBytes)
	}

	// Restart + extra pass must not emit a duplicate override_result (OF1).
	w.restart()
	changesAfter := w.eng.Changes()
	w.pass()
	if dup := drainChanges(changesAfter); slices.ContainsFunc(dup, func(c Change) bool { return c.Kind == "override_result" }) {
		t.Fatalf("duplicate override_result emitted after restart: %+v", dup)
	}
}

// TestOverrideHistoryPendingToRedSpendsReturnsToN33AndTellsOwnerOnce checks
// plan.md 7.2 done-when and notes/verify-review2.md M1:
//   - A GitHub outage holds without spending the override.
//   - When CI shown as pending finishes red before merge, N19b marks the
//     override spent without merging and fires O26's owner result line once:
//     "T1 not merged: checks got worse after the card was made".
//   - The following pass returns to normal rules (N33 tells the worker about
//     the failing check) instead of dead-ending on N19b.
func TestOverrideHistoryPendingToRedSpendsReturnsToN33AndTellsOwnerOnce(t *testing.T) {
	t.Parallel()
	w := setupPassedGateWorld(t, "", func(s *flow.State) {
		s.Settings.FlakeReruns = 0
	})
	c1 := w.commits[0]

	// Switch PR #7 to pending checks, open override card, and answer Yes.
	writePRJSON(t, w.gh, "view.json", c1, pendingRollup)
	if err := w.eng.Report(w.coordTok, "ask", json.RawMessage(`{"kind":"override","tasks":["T1"]}`)); err != nil {
		t.Fatalf("coordinator override ask: %v", err)
	}
	ask := w.state().Asks[0]
	if ask.Targets[0].Checks != flow.ChecksPending {
		t.Fatalf("card Checks = %q, want pending", ask.Targets[0].Checks)
	}
	changes := w.eng.Changes()
	answerOwnerAsk(t, w.eng, ask.ID, flow.OverrideOptionMerge)
	_ = drainChanges(changes)

	// 1. Transient GitHub 503 outage must NOT spend the override or merge.
	writeGHFile(t, w.gh, "fail", "1") // the fake gh fails every call while this file exists (pr_test.go)
	w.pass()
	if err := os.Remove(w.gh + "/fail"); err != nil {
		t.Fatal(err)
	}
	if st := w.state(); st.Tasks[0].Attempts[0].Overrides[0].Spent {
		t.Fatalf("GitHub outage spent the override (LastRow=%q)", st.Tasks[0].LastRow)
	}
	if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 0 {
		t.Fatalf("merged during GitHub outage: %v", merges)
	}

	// 2. GitHub recovers and checks finished red -> Pass hits N19b, spends override, and emits OR2 result line once.
	redRollup := `[{"__typename":"CheckRun","id":101,"name":"unit","workflowName":"CI","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/example/repo/actions/runs/555/job/101"}]`
	writePRJSON(t, w.gh, "view.json", c1, redRollup)
	writeGHFile(t, w.gh, "workflow-runs.json", `{"workflow_runs":[{"id":555,"status":"completed","conclusion":"failure"}]}`)
	workerBefore := len(sendsTo(t, w.srv.Requests(), w.workerID))

	w.pass()
	st := w.state()
	if st.Tasks[0].LastRow != "N19b" {
		t.Fatalf("LastRow on pending->red = %q, want N19b", st.Tasks[0].LastRow)
	}
	if !st.Tasks[0].Attempts[0].Overrides[0].Spent {
		t.Fatalf("N19b did not mark override Spent: %+v", st.Tasks[0].Attempts[0].Overrides)
	}
	if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 0 {
		t.Fatalf("N19b merged red checks: %v", merges)
	}
	gv := GraphViewOf(st, nil)
	if strings.Contains(gv.Tasks[0].WhyWaiting, "Merging now") || !strings.Contains(strings.ToLower(gv.Tasks[0].WhyWaiting), "worse") {
		t.Errorf("GraphView on N19b WhyWaiting = %q, want 'worse' and never 'Merging now'", gv.Tasks[0].WhyWaiting)
	}
	wantLine := "T1 not merged: checks got worse after the card was made"
	gotChanges := drainChanges(changes)
	if !slices.ContainsFunc(gotChanges, func(c Change) bool { return c.Kind == "override_result" && c.Text == wantLine }) {
		t.Fatalf("Changes after N19b = %+v, want override_result %q", gotChanges, wantLine)
	}

	// 3. Next pass returns to normal rules (N33 tells worker about failing CI), never stuck on N19b.
	w.pass()
	st = w.state()
	if st.Tasks[0].LastRow != "N33" {
		t.Fatalf("pass after N19b LastRow = %q, want N33", st.Tasks[0].LastRow)
	}
	workerSends := strings.Join(sendsTo(t, w.srv.Requests(), w.workerID)[workerBefore:], "\n")
	if !strings.Contains(workerSends, "CI failed") {
		t.Errorf("N33 did not tell worker about failing CI:\n%s", workerSends)
	}
	if dup := drainChanges(changes); slices.ContainsFunc(dup, func(c Change) bool { return c.Kind == "override_result" }) {
		t.Fatalf("duplicate override_result on N33 pass: %+v", dup)
	}
}

// TestOverrideHistoryNewerCommitNotMergedAndRecapClosedOnlyOnCoveredTask checks
// review-2.md §2 bullet 1, O29, design.md 4.9 recap closing on covered tasks,
// and O26 OR4:
//   - T1 (at c1) and T2 (at c2) both have open size recaps when the card is made.
//   - Before the owner taps Yes, T2's worker reports ready at c3 (moving H(T2)
//     past c2 while leaving the card open per O29).
//   - Tapping Yes closes T1's open recap (covered), leaves T2's open recap open
//     (uncovered), merges T1 at c1 via N19 (bypassing N12), leaves T2 unmerged
//     on normal rules, and reports "T1 merged; T2 not merged: newer commit".
func TestOverrideHistoryNewerCommitNotMergedAndRecapClosedOnlyOnCoveredTask(t *testing.T) {
	t.Parallel()
	w, tokByTask, c1, c2 := setupTwoTaskOverrideWorld(t)

	// Open size recaps on both T1 and T2 and lower SizeCap so both are over cap.
	w.edit(func(s *flow.State) {
		for i := range s.Tasks {
			s.Tasks[i].Attempts[0].SizeCap = 1
			s.Tasks[i].Recaps = []flow.Recap{{Trigger: flow.TriggerSize, Allowed: []string{flow.ChoiceContinue, "split", "new", "drop"}}}
		}
	})

	if err := w.eng.Report(w.coordTok, "ask", json.RawMessage(`{"kind":"override","tasks":["T1","T2"]}`)); err != nil {
		t.Fatalf("coordinator override ask: %v", err)
	}
	askID := w.state().Asks[0].ID

	// T2 commits c3 and reports ready before owner answers Yes.
	wt2 := w.state().Tasks[1].Attempts[0].Worktree
	c3 := commitFile(t, wt2, "two.go", "package demo\n// two v2 newer commit\n")
	if err := w.eng.Report(tokByTask["T2"], "ready", []byte(`{"head":"`+c3+`","proofs":[{"outcome":"two exists","artifact":"two.go"}],"touched":"two.go","skipped":""}`)); err != nil {
		t.Fatalf("ready T2 at c3: %v", err)
	}
	writeGHFile(t, w.gh, "view-8.json", `{"number":8,"state":"OPEN","headRefOid":"`+c3+`","mergeable":"MERGEABLE","statusCheckRollup":`+greenRollup+`,"baseRefName":"main"}`)
	_ = c2

	changes := w.eng.Changes()
	answerOwnerAsk(t, w.eng, askID, flow.OverrideOptionMerge)
	_ = drainChanges(changes)

	st := w.state()
	if flow.OpenRecap(st.Tasks[0]) != nil {
		t.Fatalf("covered T1 open recap was not closed on override Yes: %+v", st.Tasks[0].Recaps)
	}
	if flow.OpenRecap(st.Tasks[1]) == nil {
		t.Fatalf("uncovered T2 (at c3 != card c2) open recap was wrongly closed: %+v", st.Tasks[1].Recaps)
	}

	// Pass 1: T1 merges c1 via N19 (bypassing N12); T2 stays at N6 (never merges c3).
	w.pass()
	st = w.state()
	if st.Tasks[0].LastRow != "N19" {
		t.Fatalf("T1 LastRow = %q, want N19", st.Tasks[0].LastRow)
	}
	if st.Tasks[1].LastRow == "N19" || st.Tasks[1].LastRow == "N42" {
		t.Fatalf("uncovered T2 matched merge row %q", st.Tasks[1].LastRow)
	}
	merges := ghCalls(t, w.gh, "pr merge")
	if len(merges) != 1 || !strings.Contains(merges[0], " 7 ") {
		t.Fatalf("pr merge calls = %v, want only PR #7", merges)
	}

	// PR #7 merges -> Pass 2 settles T1 (OR1) while T2 is already settled as newer commit (OR4) -> OF2 fires.
	writeGHFile(t, w.gh, "view-7.json", `{"number":7,"state":"MERGED","headRefOid":"`+c1+`","mergeable":"UNKNOWN","statusCheckRollup":`+pendingRollup+`,"baseRefName":"main","mergeCommit":{"oid":"merged7777"}}`)
	w.pass()

	wantLine := "T1 merged; T2 not merged: newer commit"
	gotChanges := drainChanges(changes)
	if !slices.ContainsFunc(gotChanges, func(c Change) bool { return c.Kind == "override_result" && c.Text == wantLine }) {
		t.Fatalf("Changes = %+v, want override_result %q", gotChanges, wantLine)
	}
}

// TestOverrideHistoryConflictHitsN16AndFreshMergeCheckCallsOffWorseOrMoved
// checks review-2.md §2 bullet 4 (conflict -> N16 -> OR3 -> hand-resolved merge
// commit not covered) and plan.md 7.2's fresh-fact check in land.go (execMerge
// allows N19 and calls off merge if fresh facts turn worse into N19b).
func TestOverrideHistoryConflictHitsN16AndFreshMergeCheckCallsOffWorseOrMoved(t *testing.T) {
	t.Parallel()

	t.Run("conflict_hits_N16_emits_OR3_and_resolved_commit_is_not_covered", func(t *testing.T) {
		t.Parallel()
		w := newRestartWorld(t, "Handle override conflict")
		w.pass() // J7
		w.applyPlan()
		w.waitIdle(w.state().Project.RunID)
		w.pass() // N11
		w.pass() // N23
		w.waitIdle(w.workerID)
		w.commitAndReady("package demo\n// c1\n", greenRollup, true)
		c1 := w.commits[0]
		w.pass() // N26 opens PR #7

		if err := w.eng.Report(w.coordTok, "ask", json.RawMessage(`{"kind":"override","tasks":["T1"]}`)); err != nil {
			t.Fatalf("coordinator override ask: %v", err)
		}
		askID := w.state().Asks[0].ID

		// PR #7 becomes CONFLICTING before merge pass.
		writeGHFile(t, w.gh, "view.json", `{"number":7,"state":"OPEN","headRefOid":"`+c1+`","mergeable":"CONFLICTING","statusCheckRollup":`+greenRollup+`,"baseRefName":"main"}`)
		changes := w.eng.Changes()
		workerBefore := len(sendsTo(t, w.srv.Requests(), w.workerID))
		answerOwnerAsk(t, w.eng, askID, flow.OverrideOptionMerge)
		_ = drainChanges(changes)

		w.pass() // N16 tells worker once to merge base and emits OR3
		st := w.state()
		if st.Tasks[0].LastRow != "N16" {
			t.Fatalf("LastRow on conflicting override = %q, want N16", st.Tasks[0].LastRow)
		}
		if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 0 {
			t.Fatalf("conflicting PR merged: %v", merges)
		}
		workerSends := strings.Join(sendsTo(t, w.srv.Requests(), w.workerID)[workerBefore:], "\n")
		if !strings.Contains(workerSends, "conflicts with the base") {
			t.Errorf("worker not told about conflict:\n%s", workerSends)
		}
		wantLine := "T1 not merged: conflict with base"
		if got := drainChanges(changes); !slices.ContainsFunc(got, func(c Change) bool { return c.Kind == "override_result" && c.Text == wantLine }) {
			t.Fatalf("Changes = %+v, want override_result %q", got, wantLine)
		}

		// Worker resolves conflict with commit c2 and reports ready -> c2 is not covered by c1's override.
		w.commitAndReady("package demo\n// c2 conflict resolved\n", greenRollup, false)
		c2 := w.commits[1]
		writeGHFile(t, w.gh, "view.json", `{"number":7,"state":"OPEN","headRefOid":"`+c2+`","mergeable":"MERGEABLE","statusCheckRollup":`+greenRollup+`,"baseRefName":"main"}`)
		w.pass() // must enter review N29 (not N19!)
		if gotRow := w.state().Tasks[0].LastRow; gotRow != "N29" {
			t.Fatalf("after resolving conflict at c2, LastRow = %q, want N29", gotRow)
		}
		if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 0 {
			t.Fatalf("hand-resolved conflict commit c2 merged without review: %v", merges)
		}
	})

	t.Run("execMerge_fresh_check_allows_N19_and_calls_off_when_checks_turn_worse", func(t *testing.T) {
		t.Parallel()
		w := newRestartWorld(t, "Fresh merge check for N19")
		w.pass() // J7
		w.applyPlan()
		w.waitIdle(w.state().Project.RunID)
		w.pass() // N11
		w.pass() // N23
		w.waitIdle(w.workerID)
		w.commitAndReady("package demo\n// c1\n", pendingRollup, true)
		c1 := w.commits[0]
		w.pass() // N26 opens PR #7

		if err := w.eng.Report(w.coordTok, "ask", json.RawMessage(`{"kind":"override","tasks":["T1"]}`)); err != nil {
			t.Fatalf("override ask: %v", err)
		}
		answerOwnerAsk(t, w.eng, w.state().Asks[0].ID, flow.OverrideOptionMerge)

		// 1. Fresh checks turned red (worse than pending) right before execMerge -> calledOff = true, 0 merges.
		redRollup := `[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"FAILURE"}]`
		writePRJSON(t, w.gh, "view.json", c1, redRollup)
		st := w.state()
		dec, calledOff, err := w.eng.execMerge(w.ctx, w.id, &st, 0, flow.Facts{
			Tip: c1, TipRel: flow.RelSame, Worker: flow.Run{State: flow.RunIdle},
			PR: flow.PR{Answered: true, Number: 7, State: flow.PROpen, Head: c1, HeadRel: flow.RelSame, Mergeable: flow.MergeableClean, Checks: flow.ChecksPending},
		})
		if err != nil || !calledOff {
			t.Fatalf("execMerge on freshly-red PR = (dec=%q, calledOff=%v, err=%v), want calledOff=true", dec, calledOff, err)
		}
		if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 0 {
			t.Fatalf("execMerge merged freshly-red PR: %v", merges)
		}

		// 2. Fresh checks still pending (matches N19) -> execMerge calls MergePR (not called off!).
		writePRJSON(t, w.gh, "view.json", c1, pendingRollup)
		st = w.state()
		dec, calledOff, err = w.eng.execMerge(w.ctx, w.id, &st, 0, flow.Facts{
			Tip: c1, TipRel: flow.RelSame, Worker: flow.Run{State: flow.RunIdle},
			PR: flow.PR{Answered: true, Number: 7, State: flow.PROpen, Head: c1, HeadRel: flow.RelSame, Mergeable: flow.MergeableClean, Checks: flow.ChecksPending},
		})
		if err != nil || calledOff {
			t.Fatalf("execMerge on valid N19 override = (dec=%q, calledOff=%v, err=%v), want calledOff=false err=nil", dec, calledOff, err)
		}
		if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 1 {
			t.Fatalf("execMerge on valid N19 pr merge calls = %d, want 1", len(merges))
		}
	})
}

// TestViewStagesAndWhysForOverrideRowsN15ToN19b checks design.md 4.11 and
// plan.md 7.2: GraphViewOf and StatusViewOf emit the right stage and non-stale
// why for N15, N16, N17, N19, and N19b, and N19b states checks got worse
// (never "Merging now").
func TestViewStagesAndWhysForOverrideRowsN15ToN19b(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-ovr-view", Folder: "/tmp/ovr-view", Started: true},
		Tasks: []flow.Task{
			{ID: "T0", Title: "feat: unmet dep", LastRow: "N23", Attempts: []flow.Attempt{{BaseCommit: "b0", RunID: "w0"}}},
			{ID: "T-n15", Title: "feat: push override", LastRow: "N15", Attempts: []flow.Attempt{{BaseCommit: "b0", Ready: []flow.Ready{{Head: "c1"}}, Overrides: []flow.Override{{Head: "c1", ChecksShown: flow.ChecksAbsent}}}}},
			{ID: "T-n16", Title: "feat: conflict override", LastRow: "N16", Attempts: []flow.Attempt{{BaseCommit: "b0", RunID: "w16", Ready: []flow.Ready{{Head: "c2"}}, Overrides: []flow.Override{{Head: "c2", ChecksShown: flow.ChecksGreen}}}}},
			{ID: "T-n17", Title: "feat: dep override", After: []string{"T0"}, LastRow: "N17", Attempts: []flow.Attempt{{BaseCommit: "b0", Ready: []flow.Ready{{Head: "c3"}}, Overrides: []flow.Override{{Head: "c3", ChecksShown: flow.ChecksGreen}}}}},
			{ID: "T-n19", Title: "feat: merging override", LastRow: "N19", Attempts: []flow.Attempt{{BaseCommit: "b0", Ready: []flow.Ready{{Head: "c4"}}, Overrides: []flow.Override{{Head: "c4", ChecksShown: flow.ChecksPending}}}}},
			{ID: "T-n19b", Title: "feat: worse checks override", LastRow: "N19b", Attempts: []flow.Attempt{{BaseCommit: "b0", Ready: []flow.Ready{{Head: "c5"}}, Overrides: []flow.Override{{Head: "c5", ChecksShown: flow.ChecksPending, Spent: true}}}}},
		},
	}
	logs := []LogLine{
		{Seq: 1, At: t0, By: "engine", Task: "T-n15", Rule: "N15", Why: "push override"},
		{Seq: 2, At: t0, By: "engine", Task: "T-n16", Rule: "N16", Why: "conflict override"},
		{Seq: 3, At: t0, By: "engine", Task: "T-n17", Rule: "N17", Why: "waiting on T0"},
		{Seq: 4, At: t0, By: "engine", Task: "T-n19", Rule: "N19", Why: "merge override"},
		{Seq: 5, At: t0, By: "engine", Task: "T-n19b", Rule: "N19b", Why: "checks got worse"},
	}

	gv := GraphViewOf(st, logs)
	sv := StatusViewOf(st, logs)
	byRef := map[string]TaskView{}
	for _, tv := range gv.Tasks {
		byRef[tv.Ref] = tv
	}
	byID := map[string]TaskStatus{}
	for _, ts := range sv {
		byID[ts.ID] = ts
	}

	wantStage := map[string]string{
		"T-n15":  "ci",
		"T-n16":  "working",
		"T-n17":  "waiting_on_dep",
		"T-n19":  "ci",
		"T-n19b": "ci",
	}
	for id, stage := range wantStage {
		if byRef[id].Stage != stage || byID[id].Stage != stage || byID[id].Stale {
			t.Errorf("%s: graph=(%q, %q) status=(%q, stale=%v), want stage=%q fresh",
				id, byRef[id].Stage, byRef[id].WhyWaiting, byID[id].Stage, byID[id].Stale, stage)
		}
	}
	for _, id := range []string{"T-n15", "T-n19"} {
		if !strings.Contains(strings.ToLower(byRef[id].WhyWaiting), "override") {
			t.Errorf("%s WhyWaiting = %q, want mention of override", id, byRef[id].WhyWaiting)
		}
	}
	if !strings.Contains(byRef["T-n17"].WhyWaiting, "T0") {
		t.Errorf("T-n17 WhyWaiting = %q, want unmet dependency T0", byRef["T-n17"].WhyWaiting)
	}
	n19bWhy := byRef["T-n19b"].WhyWaiting
	if strings.Contains(n19bWhy, "Merging now") || !strings.Contains(strings.ToLower(n19bWhy), "worse") {
		t.Errorf("T-n19b WhyWaiting = %q, want checks-got-worse reason and never 'Merging now'", n19bWhy)
	}
}
