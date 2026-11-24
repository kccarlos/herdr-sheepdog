package factory

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func seedPlanCoord(t *testing.T, goal string, rounds int, models map[string][]flow.Candidate) (*storeWorld, string) {
	t.Helper()
	w := newStoreWorld(t, goal)
	tok := "tok-coord-pr"
	past := w.now.Add(-time.Minute)
	pastMS := uint64(past.UnixMilli())
	w.srv.Add(hc.Terminal{
		Address: "coord1", Name: coordName(w.id), State: "live",
		Labels: testCoordLabels(w.id, "run-coord1"),
		Status: &hc.Status{App: "claude-code", State: "idle"}, Reported: true, StatusMS: &pastMS,
	})
	w.edit(func(s *flow.State) {
		s.Settings.PlanReviewRounds = rounds
		if models != nil {
			s.Settings.Models = models
		}
		s.Project.RunID, s.Project.RunTokenHash = "coord1", tokenHash(tok)
		s.Project.Run = &flow.RunRecord{
			ID: "run-coord1", Address: "coord1", Role: flow.RoleCoordinator, CLI: "claude", Model: "opus",
			State: flow.PhaseIdle, Status: "idle", StatusReported: true, Turn: 1,
			StartedAt: past, LastActivity: past, LastTurnEnd: past, StatusSince: past,
		}
	})
	return w, tok
}

// TestPlanReviewMarkingApplyAmendAndSplit checks plan.md 11.1:
// plan_review_rounds=0 marks nothing; plan_review_rounds=1 marks applied tasks
// and only newly added amend tasks; recap split children stay unmarked.
func TestPlanReviewMarkingApplyAmendAndSplit(t *testing.T) {
	t.Run("rounds 0 marks nothing", func(t *testing.T) {
		w, tok := seedPlanCoord(t, "Mark rounds 0", 0, nil)
		if err := w.eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","title":"feat: first","estimate_lines":30,`+testPlanJSON+`}]}`)); err != nil {
			t.Fatalf("plan apply: %v", err)
		}
		if err := w.eng.Report(tok, "plan amend", json.RawMessage(`{"why":"add second","tasks":[{"id":"T2","title":"feat: second","estimate_lines":30,`+testPlanJSON+`}]}`)); err != nil {
			t.Fatalf("plan amend: %v", err)
		}
		for _, task := range w.state().Tasks {
			if task.Unreviewed {
				t.Errorf("task %s marked Unreviewed with PlanReviewRounds=0", task.ID)
			}
		}
	})

	t.Run("rounds 1 marks apply and new amend tasks only, not split children", func(t *testing.T) {
		w, tok := seedPlanCoord(t, "Mark rounds 1", 1, nil)
		if err := w.eng.Report(tok, "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","title":"feat: first","estimate_lines":30,`+testPlanJSON+`}]}`)); err != nil {
			t.Fatalf("plan apply: %v", err)
		}
		if !w.state().Tasks[0].Unreviewed {
			t.Fatal("T1 not marked Unreviewed after plan apply")
		}
		w.edit(func(s *flow.State) { s.Tasks[0].Unreviewed = false })
		amend := `{"why":"refine T1 and add T2","tasks":[{"id":"T1","brief":"updated brief"},{"id":"T2","title":"feat: added","estimate_lines":30,` + testPlanJSON + `}]}`
		if err := w.eng.Report(tok, "plan amend", json.RawMessage(amend)); err != nil {
			t.Fatalf("plan amend: %v", err)
		}
		if st := w.state(); st.Tasks[0].Unreviewed || !st.Tasks[1].Unreviewed {
			t.Fatalf("after amend: T1.Unreviewed=%v T2.Unreviewed=%v, want false/true", st.Tasks[0].Unreviewed, st.Tasks[1].Unreviewed)
		}
		w.edit(func(s *flow.State) {
			s.Tasks[0].Recaps = []flow.Recap{{Trigger: flow.TriggerReview, Allowed: []string{"split", "new", "drop"}}}
		})
		split := `{"task":"T1","choice":"split","why":"smaller parts","children":[{"id":"T1a","title":"feat: a","estimate_lines":20,` + testPlanJSON + `},{"id":"T1b","title":"feat: b","estimate_lines":20,` + testPlanJSON + `}]}`
		if err := w.eng.Report(tok, "recap decide", json.RawMessage(split)); err != nil {
			t.Fatalf("recap decide split: %v", err)
		}
		for _, task := range w.state().Tasks {
			if (task.ID == "T1a" || task.ID == "T1b") && task.Unreviewed {
				t.Errorf("split child %s unexpectedly marked Unreviewed", task.ID)
			}
		}
	})
}

// TestPlanReviewSlotProbeExcludesUnreviewed checks plan.md 11.1's slot rule:
// matchTaskStartLocked does not offer an unreviewed task for a slot, and
// passProject neither counts it as a refused start (no YieldSlots) nor grants it a slot.
func TestPlanReviewSlotProbeExcludesUnreviewed(t *testing.T) {
	w := newStoreWorld(t, "Unreviewed task slot probe")
	gh := bindRepoFakeGH(t, w.repo)
	w.seedIdleCoordAndWarmUp()

	unrevTask := flow.Task{ID: "T2", Title: "feat: unreviewed", EstimateLines: 30, Unreviewed: true}
	w.eng.mu.Lock()
	r, asks := w.eng.matchTaskStartLocked(w.id, w.state(), unrevTask, flow.Facts{}, w.now)
	w.eng.mu.Unlock()
	if r.ID != "N10" || asks {
		t.Fatalf("matchTaskStartLocked(unreviewed) = (%s, asks=%v), want (N10, false)", r.ID, asks)
	}

	wt1, br1, c1 := makeSlotWorktree(t, w.ctx, w.paths, w.repo, w.id, "T1", "t1.go")
	writeOpenPRView(t, gh, 1, c1, pendingRollup)
	w1, err := startRun(w.ctx, w.client, world.RunSpec{Name: "worker-t1-ci", Cwd: wt1, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, w.srv, w.client, w1, "idle", w.now.Add(-time.Minute))

	inFlight := []flow.PlanReview{{N: 1, Tasks: []string{"T2"}, Started: w.now, Run: &flow.ReviewRun{Kind: flow.RoundPlan, Round: 1, RunID: "plan-1"}}}
	w.edit(func(s *flow.State) {
		s.Settings.MaxRunning, s.Settings.PlanReviewRounds = 1, 1
		s.Project.PlanReviews = inFlight
		s.Tasks = []flow.Task{
			{
				ID: "T1", Title: "feat: waits on CI", EstimateLines: 50,
				Attempts: []flow.Attempt{{
					SizeCap: 100, BaseCommit: w.baseSHA, Branch: br1, Worktree: wt1, PR: 1, RunID: w1,
					Run:    &flow.RunRecord{ID: "run-w1", Address: w1, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseIdle, Status: "idle", StatusReported: true, StatusSince: w.now.Add(-time.Minute)},
					Ready:  []flow.Ready{{Head: c1}},
					Rounds: []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass}, {N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass}},
				}},
			},
			unrevTask,
		}
	})
	w.pass()
	if st := w.state(); flow.OpenAttempt(st.Tasks[1]) != nil || st.Tasks[0].Attempts[0].RunID != w1 {
		t.Fatalf("slot/yield broken: T2 attempts=%+v T1.RunID=%q (want nil, %q)", st.Tasks[1].Attempts, st.Tasks[0].Attempts[0].RunID, w1)
	}
	// Free slot: accepted T3 gets the slot while unreviewed T2 stays unstarted.
	w.edit(func(s *flow.State) {
		s.Tasks[0].Done = &flow.Merged{MergedCommit: c1}
		s.Tasks = append(s.Tasks, flow.Task{ID: "T3", Title: "feat: accepted ready", EstimateLines: 30})
	})
	w.pass()
	if st := w.state(); flow.OpenAttempt(st.Tasks[1]) != nil || flow.OpenAttempt(st.Tasks[2]) == nil {
		t.Fatalf("free slot: T2 attempts=%+v T3 attempts=%+v, want T2 nil and T3 started", st.Tasks[1].Attempts, st.Tasks[2].Attempts)
	}
}

// TestStore_AcceptPlanLockedFailedWriteKeepsMarks checks design.md 11.7 & 14
// (S1–S4) in store_commit_test.go style: a failed state write inside
// acceptPlanLocked keeps Unreviewed set, commits no PlanReview, starts no
// attempt, sends nothing while dirty, and recovers cleanly on the next pass.
func TestStore_AcceptPlanLockedFailedWriteKeepsMarks(t *testing.T) {
	w := newStoreWorld(t, "Store commit contract for acceptPlanLocked")
	w.seedIdleCoordAndWarmUp()
	w.edit(func(s *flow.State) {
		s.Settings.PlanReviewRounds = 1
		s.Tasks = append(s.Tasks, flow.Task{ID: "T2", Title: "feat: held", EstimateLines: 30, Unreviewed: true})
	})

	var failedOnce atomic.Bool
	var failedSeq atomic.Int64
	w.setWriteHooks(func(path string, s flow.State) error {
		if len(s.Project.PlanReviews) > 0 && failedOnce.CompareAndSwap(false, true) {
			failedSeq.Store(s.Seq)
			return errors.New("state write failed in acceptPlanLocked")
		}
		return SaveState(path, s)
	}, nil)

	w.pass()
	if !failedOnce.Load() {
		t.Fatal("acceptPlanLocked did not attempt to save a PlanReview on J16")
	}
	st := w.state()
	if !st.Tasks[1].Unreviewed || len(st.Project.PlanReviews) != 0 || len(st.Tasks[1].Attempts) != 0 || st.Seq >= failedSeq.Load() {
		t.Fatalf("failed acceptPlanLocked mutated state: unreviewed=%v reviews=%+v attempts=%+v seq=%d/%d",
			st.Tasks[1].Unreviewed, st.Project.PlanReviews, st.Tasks[1].Attempts, st.Seq, failedSeq.Load())
	}
	if sends := sendsTo(t, w.srv.Requests(), "coord1"); len(sends) != 0 {
		t.Fatalf("sends while dirty = %d, want 0", len(sends))
	}
	if visible, _, err := ProjectLog(w.ctx, w.paths, w.repo, 0); err != nil || slices.ContainsFunc(visible, func(l LogLine) bool { return l.Rule == "J16" }) {
		t.Fatalf("ProjectLog err=%v or exposed uncommitted J16 line: %+v", err, visible)
	}

	w.advanceMinute()
	w.pass()
	st, _ = assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
	if st.Tasks[1].Unreviewed || len(st.Project.PlanReviews) != 1 || st.Project.PlanReviews[0].Verdict != "unavailable" || st.Project.PlanReviews[0].Accepted.IsZero() || len(st.Tasks[1].Attempts) != 0 {
		t.Fatalf("after recovery: unreviewed=%v reviews=%+v attempts=%d", st.Tasks[1].Unreviewed, st.Project.PlanReviews, len(st.Tasks[1].Attempts))
	}
	w.advanceMinute()
	w.pass()
	if len(w.state().Tasks[1].Attempts) != 1 {
		t.Fatalf("T2 did not start N11 after recovery: %+v", w.state().Tasks[1].Attempts)
	}
}

// TestPlanReviewApplyJ16UnavailableThenN11 checks the PR 11.1 hctest sequence:
// plan apply -> N10 hold + J16 fail-open (planReviewerReady + acceptPlanLocked)
// -> N11 only after accept, plus PlanReviewRounds=0 writing no PlanReview.
func TestPlanReviewApplyJ16UnavailableThenN11(t *testing.T) {
	models := map[string][]flow.Candidate{flow.RolePlanReviewer: {{CLI: "claude", Model: "sonnet", Effort: "medium"}}}
	w, tok := seedPlanCoord(t, "Plan review fail-open e2e", 1, models)
	plan := `{"tasks":[{"id":"T1","title":"feat: needed","estimate_lines":40,` + testPlanJSON + `}]}`
	if err := w.eng.Report(tok, "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	if st := w.state(); !st.Tasks[0].Unreviewed || len(st.Tasks[0].Attempts) != 0 {
		t.Fatalf("after apply: unreviewed=%v attempts=%d", st.Tasks[0].Unreviewed, len(st.Tasks[0].Attempts))
	}
	w.pass() // N10 holds T1 (0 attempts); J16 fails open via planReviewerReady + acceptPlanLocked
	st, lines := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
	if len(st.Tasks[0].Attempts) != 0 || st.Tasks[0].Unreviewed || len(st.Project.PlanReviews) != 1 {
		t.Fatalf("after pass 1: attempts=%d unreviewed=%v reviews=%+v", len(st.Tasks[0].Attempts), st.Tasks[0].Unreviewed, st.Project.PlanReviews)
	}
	if pr := st.Project.PlanReviews[0]; pr.Verdict != "unavailable" || strings.TrimSpace(pr.Why) == "" || pr.Accepted.IsZero() || !slices.Equal(pr.Tasks, []string{"T1"}) {
		t.Fatalf("plan_reviews[0] = %+v, want unavailable with why, accepted, tasks [T1]", pr)
	}
	if slices.ContainsFunc(lines, func(l LogLine) bool { return l.Rule == "N11" }) {
		t.Fatalf("N11 logged before J16 accept: %+v", lines)
	}
	w.advanceMinute()
	w.pass() // N11 starts T1
	st, lines = assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
	idx := func(rule string) int { return slices.IndexFunc(lines, func(l LogLine) bool { return l.Rule == rule }) }
	if iApply, iJ16, iN11 := idx("plan apply"), idx("J16"), idx("N11"); len(st.Tasks[0].Attempts) != 1 || iApply < 0 || iJ16 <= iApply || iN11 <= iJ16 {
		t.Fatalf("pass 2: attempts=%d order apply=%d J16=%d N11=%d", len(st.Tasks[0].Attempts), iApply, iJ16, iN11)
	}

	// Default PlanReviewRounds=0 writes no PlanReview and starts N11 on pass 1.
	w0, tok0 := seedPlanCoord(t, "Plan review disabled", 0, nil)
	if err := w0.eng.Report(tok0, "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatalf("plan apply (0): %v", err)
	}
	w0.pass()
	if st0 := w0.state(); len(st0.Tasks[0].Attempts) != 1 || len(st0.Project.PlanReviews) != 0 {
		t.Fatalf("rounds=0 pass 1: attempts=%d reviews=%+v, want 1 and 0", len(st0.Tasks[0].Attempts), st0.Project.PlanReviews)
	}
}

// TestPlanReviewViewLines checks view.go's taskWhy ("Waiting on the plan review")
// and projectViewOf ("Plan review" while a plan review run is in flight).
func TestPlanReviewViewLines(t *testing.T) {
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project: flow.Project{
			ID: "proj-pr-view", Folder: "/tmp/pr-view", Goal: "ship feature", Started: true,
			PlanReviews: []flow.PlanReview{{N: 1, Tasks: []string{"T1", "T2"}, Started: t0, Run: &flow.ReviewRun{Kind: flow.RoundPlan, Round: 1, RunID: "plan-1"}}},
		},
		Tasks: []flow.Task{
			{ID: "T1", Title: "feat: logged N10", LastRow: "N10", Unreviewed: true},
			{ID: "T2", Title: "feat: unreached", Unreviewed: true},
		},
	}
	logs := []LogLine{{Seq: 1, At: t0, By: "engine", Task: "T1", Rule: "N10", Decision: "waiting", Why: "waiting on the plan review"}}
	gv, sv := GraphViewOf(st, logs), StatusViewOf(st, logs)
	for i, id := range []string{"T1", "T2"} {
		if gv.Tasks[i].Stage != "queued" || gv.Tasks[i].WhyWaiting != "Waiting on the plan review" {
			t.Errorf("GraphViewOf[%s] = (%q, %q), want (queued, Waiting on the plan review)", id, gv.Tasks[i].Stage, gv.Tasks[i].WhyWaiting)
		}
	}
	if sv[0].Stage != "queued" || sv[0].Why != "Waiting on the plan review" || sv[0].Stale {
		t.Errorf("StatusViewOf[T1] = %+v, want queued, Waiting on the plan review, Stale=false", sv[0])
	}
	raw, err := json.Marshal(ProjectsView([]flow.State{st})[0])
	if err != nil || !strings.Contains(string(raw), "Plan review") {
		t.Errorf("ProjectsView JSON while in flight = %s (err=%v), want \"Plan review\"", raw, err)
	}
	st.Project.PlanReviews[0].Verdict, st.Project.PlanReviews[0].Accepted, st.Project.PlanReviews[0].Run = "unavailable", t0.Add(time.Minute), nil
	if rawDone, err := json.Marshal(ProjectsView([]flow.State{st})[0]); err != nil || strings.Contains(string(rawDone), "Plan review") {
		t.Errorf("ProjectsView JSON after accept = %s (err=%v), must not show \"Plan review\"", rawDone, err)
	}
}
