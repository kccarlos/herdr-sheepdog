package factory

// Tests for two factory engine bugs found on the iPhone 13 e2e pass, plus their
// pinned edge cases (state table: briefs/fix-engine-state-table.md S1–S10):
//
//  1. Dropping a task (`task drop`, `recap decide ... drop`, rule N1) while it
//     has an open task-scoped gate ask leaves the ask open, keeping
//     Counts["owner_gate"] == 1, the stale approval card in Asks(true), and the
//     dropped task under stage "owner_gate" in GraphView.
//  2. Giving a running (or reopened) project a new goal sets Project.NewGoal,
//     which currently clears only when len(Tasks) > NewGoalTasks with a
//     non-split task; when the coordinator ends all remaining tasks (or all
//     tasks are already ended) and adds no new tasks, ProjectFinished never
//     becomes true and the project stays running forever.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func lastCoordStartupPrompt(t *testing.T, runs []hc.RunParams) string {
	t.Helper()
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Labels["sheepdog.role"] == flow.RoleCoordinator && len(runs[i].Argv) > 0 {
			return strings.TrimPrefix(runs[i].Argv[len(runs[i].Argv)-1], "-i=")
		}
	}
	t.Fatalf("no coordinator launch with startup prompt in runs %+v", runs)
	return ""
}

func taskViewByRef(t *testing.T, gv GraphView, ref string) TaskView {
	t.Helper()
	for _, tv := range gv.Tasks {
		if tv.Ref == ref {
			return tv
		}
	}
	t.Fatalf("task %q not found in GraphView %+v", ref, gv.Tasks)
	return TaskView{}
}

func hasCancelChangeForAsk(changes []Change, askID, taskID string) bool {
	return slices.ContainsFunc(changes, func(c Change) bool {
		return c.Kind == flow.EvAskCancel && c.AskID == askID && c.Task == taskID
	})
}

// driveRestartWorldJ4Finish advances an idle coordinator after NewGoal should
// have cleared through J4's final_journal turn, coordinator stop, and Finished=true.
func driveRestartWorldJ4Finish(t *testing.T, w *restartWorld) {
	t.Helper()
	st := w.state()
	if st.Project.NewGoal {
		t.Fatalf("Project.NewGoal stayed true after coordinator completed new_goal with all tasks ended (NewGoalTasks=%d, wakes=%+v, inFlight=%+v)",
			st.Project.NewGoalTasks, st.Project.Wakes, st.Project.CoordWakesInFlight)
	}
	coordAddr := st.Project.RunID
	if !flow.Done(st.Project.Acts, flow.FinalJournalKey(st.Tasks)) {
		w.now = w.now.Add(15 * time.Second)
		w.pass() // J4 asks coordinator for final journal line
	}
	st = w.state()
	if !flow.Done(st.Project.Acts, flow.FinalJournalKey(st.Tasks)) {
		t.Fatalf("J4 did not ask coordinator for final journal line: acts=%+v j4=%+v",
			st.Project.Acts, logRules(t, w.paths.Log(w.id), "J4"))
	}
	if coordAddr != "" {
		w.srv.SetStatus(coordAddr, &hc.Status{App: "claude-code", State: "working"})
		w.now = w.now.Add(5 * time.Second)
		w.pass()
		w.waitIdle(coordAddr) // J4 stops coordinator after final journal turn
	}
	w.now = w.now.Add(15 * time.Second)
	w.pass() // J4 records Project.Finished = true
}

// TestDropClosesTaskGateAsks covers state-table rows S1, S2a, and S2b (Bug 1):
// dropping a task while an engine gate ask (N31 or N24) or a coordinator task
// gate ask is open on that task cancels the ask via A4, emits EvAskCancel on
// Changes(), clears Asks(true) and Counts["owner_gate"], moves the task to
// stage "done" in GraphView, and refuses a late owner Answer with already_closed.
func TestDropClosesTaskGateAsks(t *testing.T) {
	t.Parallel()

	t.Run("n31_gate_ask_closed_counts_and_graph_move_to_done", func(t *testing.T) {
		t.Parallel()
		w := setupPassedGateWorld(t, `,"gate":"owner"`, nil)
		w.pass() // N31 opens Merge/Hold gate ask for T1

		pre := w.state()
		openPre := openAsksForTask(pre, "T1")
		if len(openPre) != 1 || openPre[0].Kind != flow.KindGate || openPre[0].AskedBy != flow.AskedByN31 {
			t.Fatalf("precondition open asks = %+v, want 1 N31 gate ask", openPre)
		}
		askID := openPre[0].ID
		if pv := w.eng.Projects()[0]; pv.Counts["owner_gate"] != 1 {
			t.Fatalf("precondition Counts = %v, want owner_gate=1", pv.Counts)
		}

		sub := w.eng.Changes()
		if err := w.eng.Report(w.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"superseded by new direction"}`)); err != nil {
			t.Fatalf("task drop T1: %v", err)
		}
		w.pass() // N1 cleanup + A4 ask cancel

		changes := drainChanges(sub)
		st, _ := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
		if st.Tasks[0].Dropped == nil || st.Tasks[0].LastRow != "N1" {
			t.Fatalf("T1 after drop = (dropped=%+v, lastRow=%q), want dropped at N1", st.Tasks[0].Dropped, st.Tasks[0].LastRow)
		}
		idx := slices.IndexFunc(st.Asks, func(a flow.Ask) bool { return a.ID == askID })
		if idx < 0 || st.Asks[idx].Status != flow.AskStatusCancelled {
			t.Errorf("gate ask %s after drop = %+v, want status=%q", askID, st.Asks, flow.AskStatusCancelled)
		}
		if !hasCancelChangeForAsk(changes, askID, "T1") {
			t.Errorf("Changes() missing EvAskCancel for (%s, T1): got %+v", askID, changes)
		}
		if gotOpen := w.eng.Asks(true); len(gotOpen) != 0 {
			t.Errorf("Engine.Asks(true) after drop = %+v, want empty", gotOpen)
		}
		if pv := w.eng.Projects()[0]; pv.Counts["owner_gate"] != 0 || pv.Counts["done"] != 1 {
			t.Errorf("Projects()[0].Counts = %v, want owner_gate=0 done=1", pv.Counts)
		}
		if ph := w.eng.ProjectHeads()[0]; ph.Counts["owner_gate"] != 0 || ph.Counts["done"] != 1 {
			t.Errorf("ProjectHeads()[0].Counts = %v, want owner_gate=0 done=1", ph.Counts)
		}
		gv, err := w.eng.Graph(w.id)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		if tv := taskViewByRef(t, gv, "T1"); tv.Stage != "done" || tv.WhyWaiting != "" {
			t.Errorf("Graph T1 = (stage=%q, why=%q), want (\"done\", \"\")", tv.Stage, tv.WhyWaiting)
		}

		// A5: answering the cancelled gate ask afterwards must be refused already_closed.
		prf, err := w.eng.MintProof(askID, flow.GateOptionMerge, ProofInputTap, "dev-phone-1")
		if err != nil {
			t.Fatalf("MintProof: %v", err)
		}
		if err := w.eng.Answer(askID, flow.GateOptionMerge, prf); refusalCode(err) != flow.RefuseClosed {
			t.Errorf("Answer on dropped task's gate ask = %v, want %q", err, flow.RefuseClosed)
		}
	})

	t.Run("n24_review_recap_gate_ask_closed_on_coordinator_drop", func(t *testing.T) {
		t.Parallel()
		w := setupSplitChildRetroRecap(t)
		w.pass() // N24 opens Drop/Continue gate ask on split child T1

		openPre := openAsksForTask(w.state(), "T1")
		if len(openPre) != 1 || openPre[0].AskedBy != flow.AskedByN24 {
			t.Fatalf("precondition open asks = %+v, want 1 N24 gate ask", openPre)
		}
		askID := openPre[0].ID

		sub := w.eng.Changes()
		if err := w.eng.Report(w.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"coordinator dropped split child"}`)); err != nil {
			t.Fatalf("task drop T1: %v", err)
		}
		w.pass() // N1 cleanup

		changes := drainChanges(sub)
		st, _ := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
		idx := slices.IndexFunc(st.Asks, func(a flow.Ask) bool { return a.ID == askID })
		if idx < 0 || st.Asks[idx].Status != flow.AskStatusCancelled {
			t.Errorf("N24 gate ask %s after drop = %+v, want status=%q", askID, st.Asks, flow.AskStatusCancelled)
		}
		if !hasCancelChangeForAsk(changes, askID, "T1") {
			t.Errorf("Changes() missing EvAskCancel for (%s, T1): got %+v", askID, changes)
		}
		if pv := w.eng.Projects()[0]; pv.Counts["owner_gate"] != 0 || pv.Counts["done"] != 2 {
			t.Errorf("Projects()[0].Counts = %v, want owner_gate=0 done=2 (P1 split + T1 dropped)", pv.Counts)
		}
		gv, err := w.eng.Graph(w.id)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		if tv := taskViewByRef(t, gv, "T1"); tv.Stage != "done" {
			t.Errorf("Graph T1.Stage = %q, want \"done\"", tv.Stage)
		}
	})

	t.Run("coordinator_gate_ask_closed_on_task_drop_and_recap_drop", func(t *testing.T) {
		t.Parallel()

		t.Run("task_drop", func(t *testing.T) {
			t.Parallel()
			r := newAskRig(t)
			payload := `{"kind":"gate","task":"T1","question":"Approve T1 storage schema?","options":["Approve","Revise"],"recommend":"Approve"}`
			if err := r.eng.Report(r.coordTok, "ask", json.RawMessage(payload)); err != nil {
				t.Fatalf("coordinator gate ask: %v", err)
			}
			askID := r.state().Asks[0].ID

			sub := r.eng.Changes()
			if err := r.eng.Report(r.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"schema dropped"}`)); err != nil {
				t.Fatalf("task drop T1: %v", err)
			}
			r.pass()

			changes := drainChanges(sub)
			st, _ := assertLogInvariants(t, r.paths.State(r.id), r.paths.Log(r.id))
			if st.Asks[0].Status != flow.AskStatusCancelled {
				t.Errorf("coordinator gate ask status = %q, want %q", st.Asks[0].Status, flow.AskStatusCancelled)
			}
			if !hasCancelChangeForAsk(changes, askID, "T1") {
				t.Errorf("Changes() missing EvAskCancel for (%s, T1): got %+v", askID, changes)
			}
			if pv := r.eng.Projects()[0]; pv.Counts["owner_gate"] != 0 || pv.Counts["done"] != 1 {
				t.Errorf("Projects()[0].Counts = %v, want owner_gate=0 done=1", pv.Counts)
			}
		})

		t.Run("recap_decide_drop", func(t *testing.T) {
			t.Parallel()
			r := newAskRig(t)
			// Add a gate ask with empty Head (e.g. opened before any attempt) and open a size recap.
			r.edit(func(s *flow.State) {
				s.Tasks[0].Recaps = []flow.Recap{{Trigger: flow.TriggerSize, Allowed: []string{flow.ChoiceContinue, "split", "new", "drop"}}}
				s.Asks = append(s.Asks, flow.Ask{
					ID: "ask-coord-gate", Project: r.id, Folder: r.repo, Kind: flow.KindGate, Gated: true,
					Question: "Approve T1 scope?", Options: []string{"Approve", "Hold"}, Recommend: "Approve",
					Task: "T1", AskedBy: flow.RoleCoordinator, RunID: r.coordAddr, Status: flow.AskStatusOpen, At: r.now,
				})
			})

			sub := r.eng.Changes()
			if err := r.eng.Report(r.coordTok, "recap decide", json.RawMessage(`{"task":"T1","choice":"drop","why":"out of scope"}`)); err != nil {
				t.Fatalf("recap decide drop T1: %v", err)
			}
			r.pass()

			changes := drainChanges(sub)
			st, _ := assertLogInvariants(t, r.paths.State(r.id), r.paths.Log(r.id))
			if st.Asks[0].Status != flow.AskStatusCancelled {
				t.Errorf("gate ask after recap drop status = %q, want %q", st.Asks[0].Status, flow.AskStatusCancelled)
			}
			if !hasCancelChangeForAsk(changes, "ask-coord-gate", "T1") {
				t.Errorf("Changes() missing EvAskCancel for ask-coord-gate: got %+v", changes)
			}
			if pv := r.eng.Projects()[0]; pv.Counts["owner_gate"] != 0 || pv.Counts["done"] != 1 {
				t.Errorf("Projects()[0].Counts = %v, want owner_gate=0 done=1", pv.Counts)
			}
		})
	})
}

// TestDropOneOfTwoTasksWithOnlyOneGated covers state-table rows S4a (RED on
// origin/main) and S4b (GREEN pin): in a two-task plan where T1 is gated at N31
// and T2 is ungated and working, dropping T1 closes T1's gate ask without
// disturbing T2 or finishing the project, while dropping T2 leaves T1's gate
// ask open and T1 in owner_gate.
func TestDropOneOfTwoTasksWithOnlyOneGated(t *testing.T) {
	t.Parallel()

	setupTwo := func(t *testing.T) (*restartWorld, string) {
		t.Helper()
		w := setupPassedGateWorld(t, `,"gate":"owner"`, nil)
		amend := `{"why":"add ungated sibling T2","tasks":[{"id":"T2","estimate_lines":50,"title":"feat: sibling task",` + testPlanJSON + `}]}`
		if err := w.eng.Report(w.coordTok, "plan amend", []byte(amend)); err != nil {
			t.Fatalf("plan amend add T2: %v", err)
		}
		w.pass() // N31 opens gate ask on T1; N11 creates attempt for T2
		w.pass() // N23 starts worker for T2 (updates w.workTok / w.workerID)
		w.waitIdle(w.workerID)

		t1Asks := openAsksForTask(w.state(), "T1")
		if len(t1Asks) != 1 || t1Asks[0].Kind != flow.KindGate {
			t.Fatalf("precondition T1 open asks = %+v, want 1 gate ask", t1Asks)
		}
		return w, t1Asks[0].ID
	}

	t.Run("drop_gated_task_closes_its_gate_and_keeps_sibling_working", func(t *testing.T) {
		t.Parallel()
		w, t1GateID := setupTwo(t)
		if err := w.eng.Report(w.workTok, "ask", json.RawMessage(`{"kind":"decision","question":"Use helper A or B?","options":["Helper A","Helper B"],"recommend":"Helper A"}`)); err != nil {
			t.Fatalf("T2 worker decision ask: %v", err)
		}
		t2Asks := openAsksForTask(w.state(), "T2")
		if len(t2Asks) != 1 {
			t.Fatalf("T2 open asks = %+v, want 1 decision ask", t2Asks)
		}

		sub := w.eng.Changes()
		if err := w.eng.Report(w.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"drop gated T1 only"}`)); err != nil {
			t.Fatalf("task drop T1: %v", err)
		}
		w.pass() // N1 cleans up T1; T2 stays at N23

		changes := drainChanges(sub)
		st, _ := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
		if !hasCancelChangeForAsk(changes, t1GateID, "T1") {
			t.Errorf("Changes() missing EvAskCancel for (%s, T1): got %+v", t1GateID, changes)
		}
		if hasCancelChangeForAsk(changes, t2Asks[0].ID, "T2") {
			t.Errorf("Changes() unexpectedly cancelled T2 ask %s: got %+v", t2Asks[0].ID, changes)
		}
		for _, a := range st.Asks {
			switch a.ID {
			case t1GateID:
				if a.Status != flow.AskStatusCancelled {
					t.Errorf("T1 gate ask %s status = %q, want %q", a.ID, a.Status, flow.AskStatusCancelled)
				}
			case t2Asks[0].ID:
				if a.Status != flow.AskStatusOpen {
					t.Errorf("T2 decision ask %s status = %q, want %q", a.ID, a.Status, flow.AskStatusOpen)
				}
			}
		}
		pv := w.eng.Projects()[0]
		if pv.State != "running" {
			t.Errorf("project state = %q, want \"running\" while T2 is open", pv.State)
		}
		if pv.Counts["owner_gate"] != 0 || pv.Counts["done"] != 1 || pv.Counts["working"] != 1 {
			t.Errorf("Counts = %v, want owner_gate=0 done=1 working=1", pv.Counts)
		}
		gv, err := w.eng.Graph(w.id)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		if tv1 := taskViewByRef(t, gv, "T1"); tv1.Stage != "done" {
			t.Errorf("T1 stage = %q, want \"done\"", tv1.Stage)
		}
		if tv2 := taskViewByRef(t, gv, "T2"); tv2.Stage != "working" {
			t.Errorf("T2 stage = %q, want \"working\"", tv2.Stage)
		}
	})

	t.Run("drop_ungated_task_preserves_gated_sibling_ask", func(t *testing.T) {
		t.Parallel()
		w, t1GateID := setupTwo(t)

		if err := w.eng.Report(w.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T2","why":"drop ungated T2 only"}`)); err != nil {
			t.Fatalf("task drop T2: %v", err)
		}
		w.pass() // N1 cleans up T2; T1 stays held at N31

		st, _ := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
		idx := slices.IndexFunc(st.Asks, func(a flow.Ask) bool { return a.ID == t1GateID })
		if idx < 0 || st.Asks[idx].Status != flow.AskStatusOpen {
			t.Fatalf("T1 gate ask %s after dropping T2 = %+v, want still open", t1GateID, st.Asks)
		}
		pv := w.eng.Projects()[0]
		if pv.State != "running" || pv.Counts["owner_gate"] != 1 || pv.Counts["done"] != 1 {
			t.Fatalf("after dropping T2: state=%q counts=%v, want running with owner_gate=1 done=1", pv.State, pv.Counts)
		}
		gv, err := w.eng.Graph(w.id)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		if tv1 := taskViewByRef(t, gv, "T1"); tv1.Stage != "owner_gate" {
			t.Errorf("T1 stage = %q, want \"owner_gate\"", tv1.Stage)
		}
		if tv2 := taskViewByRef(t, gv, "T2"); tv2.Stage != "done" {
			t.Errorf("T2 stage = %q, want \"done\"", tv2.Stage)
		}
	})
}

// TestDropTaskEdgeCasesPreserved pins state-table rows S3 and S5 (GREEN now):
// dropping a task while a reviewer's decision ask is open cancels that ask and
// stops the reviewer cleanly; dropping a task preserves already-answered asks,
// project-wide asks (Task == ""), and KB-ASK-10 non-gate owner asks.
func TestDropTaskEdgeCasesPreserved(t *testing.T) {
	t.Parallel()

	t.Run("drop_while_reviewer_decision_ask_open_cancels_ask_and_stops_reviewer", func(t *testing.T) {
		t.Parallel()
		rr := newReviewerAskRig(t, 1)
		revID, revTok := rr.currentReviewer()
		rr.srv.SetStatus(revID, &hc.Status{App: "claude-code", State: "working"})
		rr.pass()

		if err := rr.eng.Report(revTok, "ask", json.RawMessage(`{"kind":"decision","question":"Is nil slice return intentional?","options":["Yes","No"],"recommend":"Yes"}`)); err != nil {
			t.Fatalf("reviewer report ask: %v", err)
		}
		askID := rr.state().Asks[0].ID
		workerBefore := len(sendsTo(t, rr.srv.Requests(), rr.workerID))

		sub := rr.eng.Changes()
		if err := rr.eng.Report(rr.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"feature cut during review"}`)); err != nil {
			t.Fatalf("task drop T1: %v", err)
		}
		rr.pass() // N1 stops worker & reviewer, closes PR, cancels decision ask

		changes := drainChanges(sub)
		st, _ := assertLogInvariants(t, rr.paths.State(rr.id), rr.paths.Log(rr.id))
		if st.Asks[0].Status != flow.AskStatusCancelled {
			t.Fatalf("reviewer decision ask status = %q, want %q", st.Asks[0].Status, flow.AskStatusCancelled)
		}
		if !hasCancelChangeForAsk(changes, askID, "T1") {
			t.Errorf("Changes() missing EvAskCancel for reviewer ask %s: got %+v", askID, changes)
		}
		for _, addr := range []string{rr.workerID, revID} {
			if rs, err := termState(rr.ctx, rr.client, addr); err != nil || rs != flow.RunGone {
				t.Errorf("run %s state after drop = (%s, %v), want RunGone", addr, rs, err)
			}
		}
		if err := rr.eng.Report(rr.coordTok, "answer", json.RawMessage(fmt.Sprintf(`{"ask":%q,"option":"Yes","why":"too late"}`, askID))); refusalCode(err) != flow.RefuseClosed {
			t.Errorf("late coordinator answer after drop = %v, want %q", err, flow.RefuseClosed)
		}
		if got := sendsTo(t, rr.srv.Requests(), rr.workerID)[workerBefore:]; len(got) != 0 {
			t.Errorf("worker received message after drop: %q", got)
		}
		gv, err := rr.eng.Graph(rr.id)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		if tv := taskViewByRef(t, gv, "T1"); tv.Stage != "done" {
			t.Errorf("T1 stage = %q, want \"done\"", tv.Stage)
		}
	})

	t.Run("drop_preserves_answered_asks_and_project_wide_asks", func(t *testing.T) {
		t.Parallel()
		r := newAskRig(t)
		// Open a project-wide gate ask (Task == "") and a KB-ASK-10 release ask on T1,
		// seed an already-answered N31 gate ask on T1, and keep T2 open so J4 does not fire.
		if err := r.eng.Report(r.coordTok, "ask", json.RawMessage(`{"kind":"gate","question":"Approve overall v2 rollout?","options":["Approve","Hold"],"recommend":"Approve"}`)); err != nil {
			t.Fatalf("project-wide gate ask: %v", err)
		}
		if err := r.eng.Report(r.coordTok, "ask", json.RawMessage(`{"kind":"release","task":"T1","question":"Publish v2.0.0?","options":["Publish","Wait"],"recommend":"Publish"}`)); err != nil {
			t.Fatalf("release ask: %v", err)
		}
		projGateID := r.state().Asks[0].ID
		relID := r.state().Asks[1].ID
		r.edit(func(s *flow.State) {
			s.Tasks = append(s.Tasks, flow.Task{ID: "T2", Title: "feat: keep project open", Parked: true})
			s.Asks = append(s.Asks, flow.Ask{
				ID: "ask-t1-answered", Project: r.id, Folder: r.repo, Kind: flow.KindGate, Gated: true,
				Question: "Approve T1 at c0?", Options: []string{flow.GateOptionMerge, flow.GateOptionHold},
				Recommend: flow.GateOptionMerge, Task: "T1", Head: "c0", AskedBy: flow.AskedByN31,
				Status: flow.AskStatusAnswered, Answer: flow.GateOptionMerge, AnsweredBy: "owner",
				ProofID: "prf-answered", At: r.now.Add(-time.Minute), AnsweredAt: r.now,
			})
		})

		if err := r.eng.Report(r.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"replaced"}`)); err != nil {
			t.Fatalf("task drop T1: %v", err)
		}
		r.pass()

		byID := map[string]flow.Ask{}
		for _, a := range r.state().Asks {
			byID[a.ID] = a
		}
		if got := byID["ask-t1-answered"]; got.Status != flow.AskStatusAnswered || got.Answer != flow.GateOptionMerge || got.ProofID != "prf-answered" {
			t.Errorf("answered gate ask mutated on drop: %+v", got)
		}
		if got := byID[projGateID]; got.Status != flow.AskStatusOpen {
			t.Errorf("project-wide gate ask (Task==\"\") status = %q, want open", got.Status)
		}
		if got := byID[relID]; got.Status != flow.AskStatusOpen {
			t.Errorf("KB-ASK-10 release ask status = %q, want open", got.Status)
		}
	})
}

// TestNewGoalClearsWhenAllTasksEndWithoutNewTasks covers state-table rows S6,
// S7, S8a, and S8b (Bug 2, plus combined Bugs 1+2 in S7): when a project has
// NewGoal == true and the coordinator handles the new goal by ending all
// remaining tasks without adding new ones (via task drop or task close) or
// finishes its turn when all tasks are already ended without adding tasks,
// NewGoal clears so J4 asks for the final journal line, stops the coordinator,
// and marks the project Finished.
func TestNewGoalClearsWhenAllTasksEndWithoutNewTasks(t *testing.T) {
	t.Parallel()

	t.Run("drop_all_open_tasks_without_adding_tasks_finishes_project", func(t *testing.T) {
		t.Parallel()
		w := newRestartWorld(t, "Goal 1: initial feature")
		w.pass() // J7 starts coordinator
		coord1 := w.state().Project.RunID
		w.applyPlan() // adds T1
		w.waitIdle(coord1)
		w.pass() // N11 creates worktree for T1
		w.pass() // N23 starts worker for T1

		if err := w.eng.Start(w.repo, "Goal 2: drop T1 and add nothing", 2); err != nil {
			t.Fatalf("Start(Goal 2): %v", err)
		}
		if st := w.state(); !st.Project.NewGoal || st.Project.NewGoalTasks != 1 || len(st.Project.Wakes) != 1 {
			t.Fatalf("after Start(Goal 2): NewGoal=%v NewGoalTasks=%d Wakes=%+v", st.Project.NewGoal, st.Project.NewGoalTasks, st.Project.Wakes)
		}

		w.now = w.now.Add(15 * time.Second)
		w.pass() // J12 delivers new_goal wake to idle coord1
		sentWake := lastSendTo(t, w.srv.Requests(), coord1)
		if !strings.Contains(sentWake, "Goal 2: drop T1 and add nothing") {
			t.Fatalf("delivered wake missing Goal 2:\n%s", sentWake)
		}

		// Coordinator handles new_goal: drops T1, adds zero new tasks, and completes its turn.
		w.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
		if err := w.eng.Report(w.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"Goal 2 needs no code changes"}`)); err != nil {
			t.Fatalf("task drop T1: %v", err)
		}
		w.srv.SetScreen(coord1, completedWakeScreen("claude", sentWake, "Dropped T1; no new tasks needed."))
		w.now = w.now.Add(5 * time.Second)
		w.pass() // N1 cleans up T1 while coord1 finishes working
		w.waitIdle(coord1)

		driveRestartWorldJ4Finish(t, w)

		st, _ := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
		if !st.Project.Finished || st.Project.NewGoal || st.Project.RunID != "" {
			t.Fatalf("final project = (Finished=%v, NewGoal=%v, RunID=%q), want (true, false, \"\")",
				st.Project.Finished, st.Project.NewGoal, st.Project.RunID)
		}
		if pv := w.eng.Projects()[0]; pv.State != "finished" || pv.Live {
			t.Errorf("Projects()[0] = (state=%q, live=%v), want (\"finished\", false)", pv.State, pv.Live)
		}
		if ph := w.eng.ProjectHeads()[0]; ph.State != "finished" {
			t.Errorf("ProjectHeads()[0].State = %q, want \"finished\"", ph.State)
		}
	})

	t.Run("e2e_new_goal_drops_gated_task_closes_gate_and_finishes_project", func(t *testing.T) {
		t.Parallel()
		w := setupPassedGateWorld(t, `,"gate":"owner"`, nil)
		w.pass() // N31 opens gate ask on T1
		coord1 := w.state().Project.RunID

		gateAsks := openAsksForTask(w.state(), "T1")
		if len(gateAsks) != 1 {
			t.Fatalf("precondition gate asks = %+v, want 1", gateAsks)
		}
		gateAskID := gateAsks[0].ID

		if err := w.eng.Start(w.repo, "Goal 2: drop gated T1 and stop", 2); err != nil {
			t.Fatalf("Start(Goal 2): %v", err)
		}
		w.now = w.now.Add(15 * time.Second)
		w.pass() // J12 delivers new_goal wake
		sentWake := lastSendTo(t, w.srv.Requests(), coord1)

		w.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
		if err := w.eng.Report(w.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"dropped for Goal 2"}`)); err != nil {
			t.Fatalf("task drop T1: %v", err)
		}
		w.srv.SetScreen(coord1, completedWakeScreen("claude", sentWake, "Dropped gated T1."))
		w.now = w.now.Add(5 * time.Second)
		w.pass() // N1 cleans up T1 and cancels gate ask
		w.waitIdle(coord1)

		// Bug 1 check before J4 finish cancels remaining asks: N1 itself must already have closed T1's gate ask.
		mid := w.state()
		if idx := slices.IndexFunc(mid.Asks, func(a flow.Ask) bool { return a.ID == gateAskID }); idx < 0 || mid.Asks[idx].Status != flow.AskStatusCancelled {
			t.Errorf("gate ask %s after N1 drop = %+v, want cancelled before J4 finish", gateAskID, mid.Asks)
		}
		if pv := w.eng.Projects()[0]; pv.Counts["owner_gate"] != 0 || pv.Counts["done"] != 1 {
			t.Errorf("Counts after N1 drop = %v, want owner_gate=0 done=1", pv.Counts)
		}
		if gv, err := w.eng.Graph(w.id); err != nil {
			t.Fatalf("Graph: %v", err)
		} else if tv := taskViewByRef(t, gv, "T1"); tv.Stage != "done" {
			t.Errorf("Graph T1.Stage after N1 drop = %q, want \"done\"", tv.Stage)
		}

		// Bug 2 check: NewGoal clears and J4 finishes the project.
		driveRestartWorldJ4Finish(t, w)
		if pv := w.eng.Projects()[0]; pv.State != "finished" {
			t.Errorf("Projects()[0].State = %q, want \"finished\"", pv.State)
		}
	})

	t.Run("coordinator_explicit_task_close_on_new_goal_finishes_project", func(t *testing.T) {
		t.Parallel()
		w := newRestartWorld(t, "Goal 1: check helper")
		w.pass() // J7 starts coordinator
		coord1 := w.state().Project.RunID
		w.applyPlan()
		w.waitIdle(coord1)
		w.pass() // N11 creates worktree
		w.pass() // N23 starts worker
		w.waitIdle(w.workerID)

		base := w.state().Tasks[0].Attempts[0].BaseCommit
		if err := w.eng.Report(w.workTok, "ready", json.RawMessage(fmt.Sprintf(`{"head":%q,"nothing_to_change":"already exists"}`, base))); err != nil {
			t.Fatalf("worker nothing-to-change: %v", err)
		}
		if err := w.eng.Start(w.repo, "Goal 2: close T1 and finish", 2); err != nil {
			t.Fatalf("Start(Goal 2): %v", err)
		}

		w.now = w.now.Add(15 * time.Second)
		w.pass() // N8 queues nothing_to_change wake; J12 delivers batch [nothing_to_change, new_goal]
		sentWake := lastSendTo(t, w.srv.Requests(), coord1)

		w.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
		if err := w.eng.Report(w.coordTok, "task", json.RawMessage(`{"action":"close","task":"T1","why":"Goal 2 confirmed nothing to change"}`)); err != nil {
			t.Fatalf("task close T1: %v", err)
		}
		w.srv.SetScreen(coord1, completedWakeScreen("claude", sentWake, "Closed T1; nothing else needed."))
		w.now = w.now.Add(5 * time.Second)
		w.pass() // N1 cleans up closed T1
		w.waitIdle(coord1)

		driveRestartWorldJ4Finish(t, w)
		if st := w.state(); !st.Project.Finished || st.Project.NewGoal {
			t.Fatalf("after task close on NewGoal: Finished=%v NewGoal=%v, want (true, false)", st.Project.Finished, st.Project.NewGoal)
		}
	})

	t.Run("coordinator_completes_new_goal_turn_with_all_tasks_already_done_and_adds_none", func(t *testing.T) {
		t.Parallel()

		t.Run("live_coordinator_wake", func(t *testing.T) {
			t.Parallel()
			h := newProjectHarness(t, "Goal 1")
			h.pass() // J7 starts coord1
			coord1 := h.state().Project.RunID
			h.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{{ID: "T1", Title: "feat: goal 1"}}
			})
			h.setCoordWorking(coord1)

			if err := h.eng.Start(h.repo, "Goal 2: already covered", 2); err != nil {
				t.Fatalf("Start(Goal 2): %v", err)
			}
			// T1 merges before coord1 receives the new_goal wake.
			h.edit(func(s *flow.State) {
				s.Tasks[0].Done = &flow.Merged{MergedCommit: "m1"}
			})
			h.coordIdleWithStatus(coord1, "idle") // J12 delivers new_goal wake; must NOT finish yet
			if st := h.state(); !st.Project.NewGoal || st.Project.Finished {
				t.Fatalf("before new_goal wake turn completes: NewGoal=%v Finished=%v, want (true, false)", st.Project.NewGoal, st.Project.Finished)
			}

			// Coordinator completes the new_goal wake turn without adding any tasks.
			h.completeWakeTurn(coord1)
			h.coordIdleWithStatus(coord1, "idle")
			if h.state().Project.NewGoal {
				t.Fatalf("NewGoal stayed true after coordinator completed new_goal wake turn with 0 new tasks")
			}
			h.finishThroughJ4(coord1)
			if st := h.state(); !st.Project.Finished || st.Project.RunID != "" {
				t.Fatalf("project did not finish through J4: Finished=%v RunID=%q", st.Project.Finished, st.Project.RunID)
			}
		})

		t.Run("reopened_finished_project_fresh_coordinator_adds_no_tasks", func(t *testing.T) {
			t.Parallel()
			h := newProjectHarness(t, "Goal 1")
			h.pass() // J7 starts coord1
			coord1 := h.state().Project.RunID
			h.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{{ID: "T1", Title: "feat: goal 1", Done: &flow.Merged{MergedCommit: "m1"}}}
			})
			h.finishThroughJ4(coord1)

			// Reopen finished project with Goal 2; J7 starts fresh coord2.
			if err := h.eng.Start(h.repo, "Goal 2: verify already satisfied", 2); err != nil {
				t.Fatalf("Start(reopen Goal 2): %v", err)
			}
			h.now = h.now.Add(15 * time.Second)
			h.pass() // J7 starts coord2
			coord2 := h.state().Project.RunID
			if coord2 == "" || !h.state().Project.NewGoal {
				t.Fatalf("after J7 start on reopen: coord2=%q NewGoal=%v, want non-empty and NewGoal=true", coord2, h.state().Project.NewGoal)
			}

			// While coord2 is working on its initial turn, NewGoal stays true so J4 cannot cut it off.
			startupPrompt := lastCoordStartupPrompt(t, h.runs)
			h.setCoordWorking(coord2)
			if !h.state().Project.NewGoal {
				t.Fatal("NewGoal cleared while fresh coordinator was still working on its initial turn")
			}

			// coord2 finishes its initial turn positively without adding tasks -> NewGoal clears and J4 finishes.
			h.srv.SetScreen(coord2, completedWakeScreen("claude", startupPrompt, "Goal 2 is already satisfied by T1."))
			h.coordIdleWithStatus(coord2, "idle")
			if h.state().Project.NewGoal {
				t.Fatal("NewGoal stayed true after fresh coordinator completed its Goal 2 turn with 0 new tasks")
			}
			h.finishThroughJ4(coord2)
			if st := h.state(); !st.Project.Finished || st.Project.RunID != "" {
				t.Fatalf("reopened project with 0 new tasks did not finish: Finished=%v RunID=%q", st.Project.Finished, st.Project.RunID)
			}
		})
	})
}

// TestNewGoalEdgeCasesPreserved pins state-table rows S9b, S9c, and S10 (GREEN
// now, must keep working when Bug 2 is fixed):
//   - Dropping an old task mid-turn while the coordinator is still working on
//     new_goal must not finish the project before plan amend adds T2.
//   - Splitting an old task (SplitFrom != "") or requeuing an interrupted
//     new_goal wake must keep NewGoal true until non-split work is added or the
//     retried wake turn completes.
//   - Stop clears NewGoal/NewGoalTasks and same-goal Start resumes cleanly.
func TestNewGoalEdgeCasesPreserved(t *testing.T) {
	t.Parallel()

	t.Run("drop_old_task_mid_turn_then_add_new_task_does_not_finish_early", func(t *testing.T) {
		t.Parallel()
		w := newRestartWorld(t, "Goal 1")
		w.pass() // J7 starts coord1
		coord1 := w.state().Project.RunID
		w.applyPlan()
		w.waitIdle(coord1)
		w.pass() // N11 T1
		w.pass() // N23 T1 worker

		if err := w.eng.Start(w.repo, "Goal 2: replace T1 with T2", 2); err != nil {
			t.Fatalf("Start(Goal 2): %v", err)
		}
		w.now = w.now.Add(15 * time.Second)
		w.pass() // J12 delivers new_goal wake
		sentWake := lastSendTo(t, w.srv.Requests(), coord1)

		// Step 1 of coordinator turn (still working, wake still in flight): drop T1.
		w.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
		if err := w.eng.Report(w.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"replacing with T2"}`)); err != nil {
			t.Fatalf("task drop T1: %v", err)
		}
		w.now = w.now.Add(5 * time.Second)
		w.pass() // N1 cleans up T1 while coord1 is still working on new_goal

		if st := w.state(); st.Project.Finished || st.Project.RunID != coord1 || len(logRules(t, w.paths.Log(w.id), "J4")) != 0 {
			t.Fatalf("project finished mid-turn before coordinator added T2: Finished=%v RunID=%q j4=%+v",
				st.Project.Finished, st.Project.RunID, logRules(t, w.paths.Log(w.id), "J4"))
		}

		// Step 2 of coordinator turn: amend plan to add T2, then complete turn.
		amend := `{"why":"Goal 2 task","tasks":[{"id":"T2","estimate_lines":50,"title":"feat: goal 2 work",` + testPlanJSON + `}]}`
		if err := w.eng.Report(w.coordTok, "plan amend", []byte(amend)); err != nil {
			t.Fatalf("plan amend add T2: %v", err)
		}
		w.srv.SetScreen(coord1, completedWakeScreen("claude", sentWake, "Dropped T1 and added T2."))
		w.waitIdle(coord1)

		st := w.state()
		if st.Project.NewGoal || st.Project.Finished {
			t.Fatalf("after adding T2: NewGoal=%v Finished=%v, want (false, false)", st.Project.NewGoal, st.Project.Finished)
		}
		if len(st.Tasks[1].Attempts) == 0 {
			w.pass() // N11 starts attempt for T2
		}
		if len(w.state().Tasks[1].Attempts) != 1 {
			t.Fatalf("T2 did not start an attempt: %+v", w.state().Tasks[1])
		}
	})

	t.Run("split_child_and_interrupted_wake_keep_new_goal_until_settled", func(t *testing.T) {
		t.Parallel()
		h := newProjectHarness(t, "Goal 1")
		h.pass() // J7 starts coord1
		coord1 := h.state().Project.RunID
		h.edit(func(s *flow.State) {
			s.Tasks = []flow.Task{{
				ID: "T1", Title: "feat: goal 1", Parked: true,
			}}
		})
		h.coordIdleWithStatus(coord1, "idle")

		if err := h.eng.Start(h.repo, "Goal 2", 2); err != nil {
			t.Fatalf("Start(Goal 2): %v", err)
		}
		// T1 splits into T1a (SplitFrom != "") and T1a merges: len(s.Tasks)=2 > NewGoalTasks=1,
		// but SplitFrom child must NOT clear NewGoal while Goal 2 wake has not completed.
		h.edit(func(s *flow.State) {
			s.Tasks[0].Parked = false
			s.Tasks[0].Split = &flow.Split{Children: []string{"T1a"}}
			s.Tasks = append(s.Tasks, flow.Task{ID: "T1a", Title: "feat: split child", SplitFrom: "T1", Done: &flow.Merged{MergedCommit: "m1a"}})
		})
		h.now = h.now.Add(15 * time.Second)
		h.pass() // J12 delivers new_goal wake
		if !h.state().Project.NewGoal {
			t.Fatal("SplitFrom child cleared NewGoal before coordinator handled Goal 2")
		}

		// Coordinator's wake turn is Esc-interrupted -> J12 requeues new_goal (Retried: true);
		// even with all tasks ended, NewGoal must stay true while the retried wake is still queued!
		send1 := lastSendTo(t, h.srv.Requests(), coord1)
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, formatPromptEcho("❯", send1)+"\n  ⎿  Interrupted · What should Claude do instead?\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")
		if st := h.state(); !st.Project.NewGoal || st.Project.Finished || len(logRules(t, h.paths.Log(h.id), "J4")) != 0 {
			t.Fatalf("interrupted new_goal wake cleared NewGoal or triggered J4: NewGoal=%v Finished=%v j4=%+v",
				st.Project.NewGoal, st.Project.Finished, logRules(t, h.paths.Log(h.id), "J4"))
		}

		// Coordinator adds non-split T2 -> NewGoal clears.
		h.edit(func(s *flow.State) {
			s.Tasks = append(s.Tasks, flow.Task{ID: "T2", Title: "feat: goal 2"})
		})
		h.now = h.now.Add(15 * time.Second)
		h.pass()
		if h.state().Project.NewGoal {
			t.Fatal("NewGoal did not clear when non-split task T2 was added")
		}
	})

	t.Run("stop_and_same_goal_resume_clears_new_goal", func(t *testing.T) {
		t.Parallel()

		t.Run("with_open_task", func(t *testing.T) {
			t.Parallel()
			h := newProjectHarness(t, "Goal 1")
			h.pass()
			h.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open", Parked: true}}
			})
			if err := h.eng.Start(h.repo, "Goal 2", 2); err != nil {
				t.Fatalf("Start(Goal 2): %v", err)
			}
			if !h.state().Project.NewGoal {
				t.Fatal("precondition: expected NewGoal=true")
			}

			if err := h.eng.Stop(h.id); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if st := h.state(); st.Project.Started || st.Project.NewGoal || st.Project.NewGoalTasks != 0 || h.eng.Projects()[0].State != "stopped" {
				t.Fatalf("after Stop: Started=%v NewGoal=%v NewGoalTasks=%d state=%q",
					st.Project.Started, st.Project.NewGoal, st.Project.NewGoalTasks, h.eng.Projects()[0].State)
			}

			if err := h.eng.Start(h.repo, "Goal 2", 2); err != nil {
				t.Fatalf("Start(resume same Goal 2): %v", err)
			}
			if st := h.state(); !st.Project.Started || st.Project.NewGoal || st.Project.NewGoalTasks != 0 || h.eng.Projects()[0].State != "running" {
				t.Fatalf("after same-goal resume: Started=%v NewGoal=%v NewGoalTasks=%d state=%q",
					st.Project.Started, st.Project.NewGoal, st.Project.NewGoalTasks, h.eng.Projects()[0].State)
			}
		})

		t.Run("with_all_tasks_ended_and_wakes_drained", func(t *testing.T) {
			t.Parallel()
			h := newProjectHarness(t, "Goal 1")
			h.pass() // J7 starts coord1
			coord1 := h.state().Project.RunID
			h.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open", Parked: true}}
			})
			h.coordIdleWithStatus(coord1, "idle")

			if err := h.eng.Start(h.repo, "Goal 2", 2); err != nil {
				t.Fatalf("Start(Goal 2): %v", err)
			}
			h.now = h.now.Add(15 * time.Second)
			h.pass() // J12 delivers new_goal wake
			h.completeWakeTurn(coord1)
			h.edit(func(s *flow.State) {
				s.Tasks[0].Parked = false
				s.Tasks[0].Done = &flow.Merged{MergedCommit: "m1"}
			})
			h.coordIdleWithStatus(coord1, "idle")

			// Stop clears NewGoal; same-goal Start with all tasks ended and no wakes finishes via J4.
			if err := h.eng.Stop(h.id); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			if err := h.eng.Start(h.repo, "Goal 2", 2); err != nil {
				t.Fatalf("Start(same Goal 2): %v", err)
			}
			h.now = h.now.Add(15 * time.Second)
			h.pass()
			if st := h.state(); !st.Project.Finished || st.Project.NewGoal || h.eng.Projects()[0].State != "finished" {
				t.Fatalf("after Stop/Resume with all tasks ended: Finished=%v NewGoal=%v state=%q",
					st.Project.Finished, st.Project.NewGoal, h.eng.Projects()[0].State)
			}
		})
	})
}

// TestDropCleanupPRCloseFailureStillLogsAskCancellation covers state-table row
// S1b (PR #853 r2 Medium): when N1 cancels an ended task's open gate ask and a
// later cleanup step (PR close) fails, the committed ask cancellation and its
// EvAskCancel notification must also be recorded in the durable log rather than
// leaving the N1 log line with an empty Decision.
func TestDropCleanupPRCloseFailureStillLogsAskCancellation(t *testing.T) {
	t.Parallel()
	w := setupPassedGateWorld(t, `,"gate":"owner"`, nil)
	w.pass() // N31 opens Merge/Hold gate ask for T1

	openPre := openAsksForTask(w.state(), "T1")
	if len(openPre) != 1 || openPre[0].Kind != flow.KindGate || openPre[0].AskedBy != flow.AskedByN31 {
		t.Fatalf("precondition open asks = %+v, want 1 N31 gate ask", openPre)
	}
	askID := openPre[0].ID

	sub := w.eng.Changes()
	if err := w.eng.Report(w.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"superseded while GitHub close fails"}`)); err != nil {
		t.Fatalf("task drop T1: %v", err)
	}
	preLogLen := len(readRawLogLines(t, w.paths.Log(w.id)))
	writeGHFile(t, w.gh, "close_fail", "")

	w.pass() // N1 cancels askID, then ClosePR fails

	changes := drainChanges(sub)
	st, lines := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
	idx := slices.IndexFunc(st.Asks, func(a flow.Ask) bool { return a.ID == askID })
	if idx < 0 || st.Asks[idx].Status != flow.AskStatusCancelled {
		t.Fatalf("gate ask %s after drop with close_fail = %+v, want status=%q", askID, st.Asks, flow.AskStatusCancelled)
	}
	if !hasCancelChangeForAsk(changes, askID, "T1") {
		t.Errorf("Changes() missing EvAskCancel for (%s, T1): got %+v", askID, changes)
	}

	newLines := lines[preLogLen:]
	hasCleanupErr := slices.ContainsFunc(newLines, func(l LogLine) bool {
		return l.Task == "T1" && l.Rule == "N1" && strings.Contains(l.Why, "close failed")
	})
	if !hasCleanupErr {
		t.Fatalf("expected N1 log entry recording PR close failure, got %+v", extractLogDecisions(newLines))
	}
	hasCancelDecision := slices.ContainsFunc(newLines, func(l LogLine) bool {
		return l.Task == "T1" && (l.Rule == "N1" || l.Rule == "A4") && strings.Contains(l.Decision, "cancelled")
	})
	if !hasCancelDecision {
		t.Errorf("durable log lost committed ask cancellation when PR close failed: new log lines = %+v", extractLogDecisions(newLines))
	}

	// Once PR close recovers after EnsureBackoff, N1 finishes cleanup without re-cancelling askID.
	_ = os.Remove(filepath.Join(w.gh, "close_fail"))
	retryLogLen := len(lines)
	w.now = w.now.Add(2 * time.Minute)
	w.pass()

	retryChanges := drainChanges(sub)
	stAfter, linesAfter := assertLogInvariants(t, w.paths.State(w.id), w.paths.Log(w.id))
	if !flow.Done(stAfter.Tasks[0].Attempts[0].Acts, flow.AskCleanup) {
		t.Errorf("cleanup did not finish after close_fail cleared: acts=%+v", stAfter.Tasks[0].Attempts[0].Acts)
	}
	if hasCancelChangeForAsk(retryChanges, askID, "T1") {
		t.Errorf("retry pass emitted duplicate EvAskCancel for %s: %+v", askID, retryChanges)
	}
	for _, l := range linesAfter[retryLogLen:] {
		if l.Task == "T1" && strings.Contains(l.Decision, "cancelled") {
			t.Errorf("retry pass logged duplicate ask cancellation: %+v", extractLogDecisions(linesAfter[retryLogLen:]))
		}
	}
}

// TestNewGoalRequiresPositiveHandlingBeforeJ4Finish covers state-table rows S9d,
// S9e, and S8b (PR #853 r2 High): with all old tasks ended, neither exhausting
// an unconfirmed new_goal wake retry via J12 nor interrupting the J7 startup
// prompt of a reopened project may clear NewGoal or let J4 finish the project
// (or cancel open project-wide asks); positive handling of the new_goal wake or
// reopened startup prompt must clear NewGoal and finish through J4.
func TestNewGoalRequiresPositiveHandlingBeforeJ4Finish(t *testing.T) {
	t.Parallel()

	t.Run("exhausted_new_goal_wake_retry_does_not_clear_new_goal_or_finish", func(t *testing.T) {
		t.Parallel()
		h := newProjectHarness(t, "Goal 1")
		h.pass() // J7 starts coord1
		coord1 := h.state().Project.RunID
		h.edit(func(s *flow.State) {
			s.Tasks = []flow.Task{{ID: "T1", Title: "feat: goal 1", Done: &flow.Merged{MergedCommit: "m1"}}}
		})
		h.setCoordWorking(coord1)

		if err := h.eng.Start(h.repo, "Goal 2: check edge case", 2); err != nil {
			t.Fatalf("Start(Goal 2): %v", err)
		}
		h.edit(func(s *flow.State) {
			s.Asks = append(s.Asks, flow.Ask{
				ID: "ask-proj-gate", Project: h.id, Folder: h.repo, Kind: flow.KindGate, Gated: true,
				Question: "Approve rollout window?", Options: []string{"Approve", "Hold"}, Recommend: "Approve",
				AskedBy: flow.RoleCoordinator, RunID: coord1, Status: flow.AskStatusOpen, At: h.now,
			})
		})

		h.coordIdleWithStatus(coord1, "idle") // J12 delivers new_goal wake #1
		send1 := lastSendTo(t, h.srv.Requests(), coord1)
		if !strings.Contains(send1, "Goal 2: check edge case") {
			t.Fatalf("delivered wake #1 missing Goal 2:\n%s", send1)
		}

		// 1st delivery of new_goal is Esc-interrupted -> J12 retries new_goal (#2).
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, formatPromptEcho("❯", send1)+"\n  ⎿ \u00a0Interrupted · What should Claude do instead?\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")
		sends := sendsTo(t, h.srv.Requests(), coord1)
		if len(sends) != 2 {
			t.Fatalf("after 1st interrupted new_goal wake: sends=%d, want 2", len(sends))
		}
		send2 := sends[1]

		// 2nd delivery of new_goal is also Esc-interrupted -> J12 exhausts retry.
		// NewGoal was never positively handled, so NewGoal must stay true and J4 must not run.
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, formatPromptEcho("❯", send2)+"\n  ⎿ \u00a0Interrupted · What should Claude do instead?\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")
		h.now = h.now.Add(15 * time.Second)
		h.pass()

		st, _ := assertLogInvariants(t, h.paths.State(h.id), h.paths.Log(h.id))
		if !st.Project.NewGoal {
			t.Errorf("Project.NewGoal = false after exhausting interrupted new_goal wake retry, want true")
		}
		if st.Project.Finished {
			t.Errorf("Project.Finished = true before coordinator positively handled Goal 2, want false")
		}
		if pv := h.eng.Projects()[0]; pv.State != "running" {
			t.Errorf("Projects()[0].State = %q, want \"running\"", pv.State)
		}
		if st.Project.RunID != coord1 {
			t.Errorf("Project.RunID = %q, want %q", st.Project.RunID, coord1)
		}
		if j4 := logRules(t, h.paths.Log(h.id), "J4"); len(j4) != 0 {
			t.Errorf("J4 ran after exhausted new_goal wake retry: %+v", extractLogDecisions(j4))
		}
		for _, s := range sendsTo(t, h.srv.Requests(), coord1) {
			if strings.Contains(strings.ToLower(s), "final journal") {
				t.Errorf("J4 sent final journal prompt after exhausted new_goal retry: %q", s)
			}
		}
		if len(st.Asks) != 1 || st.Asks[0].Status != flow.AskStatusOpen {
			t.Errorf("project-wide ask after exhausted new_goal retry = %+v, want open", st.Asks)
		}
	})

	t.Run("interrupted_reopen_startup_prompt_does_not_clear_new_goal_or_finish", func(t *testing.T) {
		t.Parallel()
		h := newProjectHarness(t, "Goal 1")
		h.pass() // J7 starts coord1
		coord1 := h.state().Project.RunID
		h.edit(func(s *flow.State) {
			s.Tasks = []flow.Task{{ID: "T1", Title: "feat: goal 1", Done: &flow.Merged{MergedCommit: "m1"}}}
		})
		h.finishThroughJ4(coord1)
		j4Before := len(logRules(t, h.paths.Log(h.id), "J4"))

		if err := h.eng.Start(h.repo, "Goal 2: audit reopened project", 2); err != nil {
			t.Fatalf("Start(reopen Goal 2): %v", err)
		}
		h.now = h.now.Add(15 * time.Second)
		h.pass() // J7 starts fresh coord2 with Goal 2 startup handoff prompt
		coord2 := h.state().Project.RunID
		if coord2 == "" || coord2 == coord1 || !h.state().Project.NewGoal {
			t.Fatalf("after J7 start on reopen: coord2=%q (coord1=%q) NewGoal=%v, want fresh coord2 and NewGoal=true",
				coord2, coord1, h.state().Project.NewGoal)
		}
		h.edit(func(s *flow.State) {
			s.Asks = append(s.Asks, flow.Ask{
				ID: "ask-reopen-gate", Project: h.id, Folder: h.repo, Kind: flow.KindGate, Gated: true,
				Question: "Approve reopened rollout?", Options: []string{"Approve", "Hold"}, Recommend: "Approve",
				AskedBy: flow.RoleCoordinator, RunID: coord2, Status: flow.AskStatusOpen, At: h.now,
			})
		})

		startupPrompt := lastCoordStartupPrompt(t, h.runs)
		if !strings.Contains(startupPrompt, "Goal 2: audit reopened project") {
			t.Fatalf("J7 startup prompt missing Goal 2:\n%s", startupPrompt)
		}

		// coord2's initial startup prompt turn is Esc-interrupted and goes idle.
		// NewGoal must stay true and J4 must not run or cancel open project asks.
		h.setCoordWorking(coord2)
		h.srv.SetScreen(coord2, formatPromptEcho("❯", startupPrompt)+"\n  ⎿ \u00a0Interrupted · What should Claude do instead?\n❯ \n")
		h.coordIdleWithStatus(coord2, "idle")
		h.now = h.now.Add(15 * time.Second)
		h.pass()

		st, _ := assertLogInvariants(t, h.paths.State(h.id), h.paths.Log(h.id))
		if !st.Project.NewGoal {
			t.Errorf("Project.NewGoal = false after interrupted J7 startup prompt, want true")
		}
		if st.Project.Finished {
			t.Errorf("Project.Finished = true after interrupted J7 startup prompt, want false")
		}
		if pv := h.eng.Projects()[0]; pv.State != "running" {
			t.Errorf("Projects()[0].State = %q, want \"running\"", pv.State)
		}
		if st.Project.RunID != coord2 {
			t.Errorf("Project.RunID = %q, want %q", st.Project.RunID, coord2)
		}
		if j4After := logRules(t, h.paths.Log(h.id), "J4")[j4Before:]; len(j4After) != 0 {
			t.Errorf("J4 ran after interrupted J7 startup prompt: %+v", extractLogDecisions(j4After))
		}
		for _, s := range sendsTo(t, h.srv.Requests(), coord2) {
			if strings.Contains(strings.ToLower(s), "final journal") {
				t.Errorf("J4 sent final journal prompt to coord2 after interrupted startup: %q", s)
			}
		}
		if len(st.Asks) != 1 || st.Asks[0].Status != flow.AskStatusOpen {
			t.Errorf("project-wide ask after interrupted startup = %+v, want open", st.Asks)
		}
	})

	t.Run("positive_handling_clears_new_goal_and_finishes", func(t *testing.T) {
		t.Parallel()

		t.Run("confirmed_wake_retry_after_interruption", func(t *testing.T) {
			t.Parallel()
			h := newProjectHarness(t, "Goal 1")
			h.pass() // J7 starts coord1
			coord1 := h.state().Project.RunID
			h.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{{ID: "T1", Title: "feat: goal 1", Done: &flow.Merged{MergedCommit: "m1"}}}
			})
			h.setCoordWorking(coord1)

			if err := h.eng.Start(h.repo, "Goal 2: already covered", 2); err != nil {
				t.Fatalf("Start(Goal 2): %v", err)
			}
			h.coordIdleWithStatus(coord1, "idle") // J12 delivers new_goal wake #1
			send1 := lastSendTo(t, h.srv.Requests(), coord1)

			// 1st delivery is Esc-interrupted -> J12 retries new_goal (#2).
			h.setCoordWorking(coord1)
			h.srv.SetScreen(coord1, formatPromptEcho("❯", send1)+"\n  ⎿ \u00a0Interrupted · What should Claude do instead?\n❯ \n")
			h.coordIdleWithStatus(coord1, "idle")
			send2 := lastSendTo(t, h.srv.Requests(), coord1)
			if !h.state().Project.NewGoal {
				t.Fatal("NewGoal cleared before retried wake completed")
			}

			// 2nd delivery completes positively with 0 new tasks -> NewGoal clears and J4 finishes.
			h.setCoordWorking(coord1)
			h.srv.SetScreen(coord1, completedWakeScreen("claude", send2, "Goal 2 is already covered by T1; no new tasks needed."))
			h.coordIdleWithStatus(coord1, "idle")
			if h.state().Project.NewGoal {
				t.Fatal("NewGoal stayed true after confirmed new_goal retry turn with 0 new tasks")
			}

			h.finishThroughJ4(coord1)
			st, _ := assertLogInvariants(t, h.paths.State(h.id), h.paths.Log(h.id))
			if !st.Project.Finished || st.Project.RunID != "" || h.eng.Projects()[0].State != "finished" {
				t.Fatalf("project did not finish after confirmed new_goal retry: Finished=%v RunID=%q state=%q",
					st.Project.Finished, st.Project.RunID, h.eng.Projects()[0].State)
			}
		})

		t.Run("confirmed_reopen_startup_prompt", func(t *testing.T) {
			t.Parallel()
			h := newProjectHarness(t, "Goal 1")
			h.pass() // J7 starts coord1
			coord1 := h.state().Project.RunID
			h.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{{ID: "T1", Title: "feat: goal 1", Done: &flow.Merged{MergedCommit: "m1"}}}
			})
			h.finishThroughJ4(coord1)

			if err := h.eng.Start(h.repo, "Goal 2: confirm already satisfied", 2); err != nil {
				t.Fatalf("Start(reopen Goal 2): %v", err)
			}
			h.now = h.now.Add(15 * time.Second)
			h.pass() // J7 starts coord2
			coord2 := h.state().Project.RunID
			startupPrompt := lastCoordStartupPrompt(t, h.runs)

			h.setCoordWorking(coord2)
			h.srv.SetScreen(coord2, completedWakeScreen("claude", startupPrompt, "Goal 2 is already satisfied by T1; nothing to add."))
			h.coordIdleWithStatus(coord2, "idle")
			if h.state().Project.NewGoal {
				t.Fatal("NewGoal stayed true after confirmed J7 startup prompt with 0 new tasks")
			}

			h.finishThroughJ4(coord2)
			st, _ := assertLogInvariants(t, h.paths.State(h.id), h.paths.Log(h.id))
			if !st.Project.Finished || st.Project.RunID != "" || h.eng.Projects()[0].State != "finished" {
				t.Fatalf("reopened project did not finish after confirmed startup prompt: Finished=%v RunID=%q state=%q",
					st.Project.Finished, st.Project.RunID, h.eng.Projects()[0].State)
			}
		})
	})
}
