package factory

// PR 6.2, TDD step 1: engine history tests and view stage tests for the ask
// gates (N24, N30, N31), stale-gate cancellation when H moves past ask.Head,
// N1 cancelling decision asks while keeping owner asks alive (KB-ASK-10),
// N42 noOpenOwnerAsk, slots.go owner-answered priority and extra slot, and
// view.go owner_answer / owner_gate stages.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func findOptionPrefix(t *testing.T, opts []string, prefix string) string {
	t.Helper()
	low := strings.ToLower(prefix)
	for _, o := range opts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(o)), low) {
			return o
		}
	}
	t.Fatalf("no option starting with %q in %v", prefix, opts)
	return ""
}

func findOptionContains(t *testing.T, opts []string, sub string) string {
	t.Helper()
	low := strings.ToLower(sub)
	for _, o := range opts {
		if strings.Contains(strings.ToLower(o), low) {
			return o
		}
	}
	t.Fatalf("no option containing %q in %v", sub, opts)
	return ""
}

func answerOwnerAsk(t *testing.T, eng *Engine, askID, option string) {
	t.Helper()
	proof, err := eng.MintProof(askID, option, ProofInputTap, "dev-phone-1")
	if err != nil {
		t.Fatalf("MintProof(%s, %q): %v", askID, option, err)
	}
	if err := eng.Answer(askID, option, proof); err != nil {
		t.Fatalf("Engine.Answer(%s, %q): %v", askID, option, err)
	}
}

func openAsksForTask(st flow.State, taskID string) []flow.Ask { //nolint:unparam // test helper kept general
	var out []flow.Ask
	for _, a := range st.Asks {
		if a.Task == taskID && a.Status == flow.AskStatusOpen {
			out = append(out, a)
		}
	}
	return out
}

// setupPassedGateWorld runs a single task through plan apply, worker commit,
// PR open, and two passing review rounds with green CI, stopping right before
// landing so the next pass hits N31 (or N42 if no gate applies).
func setupPassedGateWorld(t *testing.T, taskExtraJSON string, editState func(*flow.State)) *restartWorld {
	t.Helper()
	w := newRestartWorld(t, "Ship gated task")
	w.edit(func(s *flow.State) {
		s.Settings.ReviewRounds = 2
		if editState != nil {
			editState(s)
		}
	})
	w.pass() // J7 start coordinator
	st := w.state()
	w.srv.SetStatus(st.Project.RunID, &hc.Status{App: "claude-code", State: "working"})
	body := `{"tasks":[{"id":"T1","estimate_lines":50,"title":"feat: gated change","brief":"Add demo.go.",` +
		testScopeJSON + taskExtraJSON +
		`,"outcomes":[{"text":"demo exists","proved_by":"manual: demo.go"}]}]}`
	if err := w.eng.Report(w.coordTok, "plan apply", []byte(body)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	w.waitIdle(st.Project.RunID)
	w.pass() // N11 new attempt
	w.pass() // N23 start worker
	w.waitIdle(w.workerID)
	w.commitAndReady("package demo\n// v1\n", greenRollup, true)
	w.pass() // N26 push & open PR
	w.pass() // N29 start round 1
	w.passReviewRound()
	w.pass() // N29 start round 2
	w.passReviewRound()
	return w
}

// setupSplitChildRetroRecap sets up a split child T1 (SplitFrom P1 where
// P1.HadRetro == true) at the review recap streak (3 changes rounds), ready
// for N24 on the next pass.
func setupSplitChildRetroRecap(t *testing.T) *restartWorld {
	t.Helper()
	w := newRestartWorld(t, "Finish split child after retro")
	w.pass() // J7 start coordinator
	coordAddr := w.state().Project.RunID
	w.applyPlan()
	w.waitIdle(coordAddr)
	w.pass() // N11 new attempt
	w.pass() // N23 start worker
	w.waitIdle(w.workerID)
	w.commitAndReady("package demo\n// v1\n", greenRollup, true)
	c1 := w.commits[0]
	w.edit(func(s *flow.State) {
		parent := flow.Task{
			ID:       "P1",
			Title:    "feat: parent task",
			Split:    &flow.Split{Children: []string{"T1"}},
			HadRetro: true,
		}
		s.Tasks[0].SplitFrom = "P1"
		s.Tasks[0].Attempts[0].Rounds = []flow.Round{
			{N: 1, Kind: flow.RoundFull, Head: "c0", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 2, Kind: flow.RoundDelta, Head: "c0b", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 3, Kind: flow.RoundDelta, Head: c1, Verdict: flow.VerdictChanges, Majors: 1},
		}
		s.Tasks = append([]flow.Task{parent}, s.Tasks...)
	})
	return w
}

// TestN31HistoryOwnerGatePlanToMerge checks plan.md 6.2 "Done when": a task
// planned with gate: owner passes two reviews and green CI, holds at N31
// (owner_gate) with an open gate ask naming H, does not merge before the
// owner answers, and merges once a verified Engine.Answer yes covers H.
func TestN31HistoryOwnerGatePlanToMerge(t *testing.T) {
	t.Parallel()
	w := setupPassedGateWorld(t, `,"gate":"owner"`, nil)
	c1 := w.commits[0]

	// First landing pass matches N31 and opens one gated gate ask naming H.
	w.pass()
	st := w.state()
	if st.Tasks[0].LastRow != "N31" {
		t.Fatalf("LastRow = %q, want N31", st.Tasks[0].LastRow)
	}
	asks := openAsksForTask(st, "T1")
	if len(asks) != 1 {
		t.Fatalf("open asks for T1 = %d (%+v), want 1", len(asks), asks)
	}
	ask := asks[0]
	if ask.Kind != flow.KindGate || !ask.Gated || ask.Head != c1 {
		t.Fatalf("gate ask = %+v, want kind=gate gated=true head=%s", ask, c1)
	}
	if gv := GraphViewOf(st, nil); gv.Tasks[0].Stage != "owner_gate" {
		t.Errorf("GraphView stage = %q, want owner_gate", gv.Tasks[0].Stage)
	}
	if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 0 {
		t.Fatalf("merged before owner gate yes: %v", merges)
	}

	// Second pass must not open a duplicate gate ask or merge.
	w.pass()
	if got := openAsksForTask(w.state(), "T1"); len(got) != 1 {
		t.Fatalf("second pass duplicated gate ask: len=%d", len(got))
	}
	if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 0 {
		t.Fatalf("merged before owner gate yes on second pass: %v", merges)
	}

	// Owner answers yes with verified proof -> next pass merges via N42.
	yesOpt := findOptionPrefix(t, ask.Options, "Yes")
	answerOwnerAsk(t, w.eng, ask.ID, yesOpt)
	w.pass()

	st = w.state()
	if st.Tasks[0].LastRow != "N42" {
		t.Fatalf("after owner yes LastRow = %q, want N42", st.Tasks[0].LastRow)
	}
	if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 1 {
		t.Fatalf("pr merge calls = %d, want 1", len(merges))
	}
}

// TestN31HistoryDesignGateSetting checks design.md 4.7 N31 / 11.4: a design
// task (kind: design) holds at N31 only when Settings.DesignGate == "owner".
func TestN31HistoryDesignGateSetting(t *testing.T) {
	t.Parallel()

	t.Run("design_gate_owner_holds_until_yes", func(t *testing.T) {
		t.Parallel()
		w := setupPassedGateWorld(t, `,"kind":"design"`, func(s *flow.State) {
			s.Settings.DesignGate = flow.GateOwner
		})
		w.pass()
		st := w.state()
		if st.Tasks[0].LastRow != "N31" {
			t.Fatalf("LastRow = %q, want N31", st.Tasks[0].LastRow)
		}
		asks := openAsksForTask(st, "T1")
		if len(asks) != 1 || asks[0].Kind != flow.KindGate {
			t.Fatalf("open asks = %+v, want 1 gate ask", asks)
		}
		yesOpt := findOptionPrefix(t, asks[0].Options, "Yes")
		answerOwnerAsk(t, w.eng, asks[0].ID, yesOpt)
		w.pass()
		if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 1 {
			t.Fatalf("pr merge calls after yes = %d, want 1", len(merges))
		}
	})

	t.Run("design_gate_unset_merges_directly", func(t *testing.T) {
		t.Parallel()
		w := setupPassedGateWorld(t, `,"kind":"design"`, nil)
		w.pass()
		if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 1 {
			t.Fatalf("design task without design_gate=owner: pr merge calls = %d, want 1", len(merges))
		}
	})
}

// TestN30HistoryOwnerGateNoOpensOwnerNoRecap checks design.md 4.7 N30 and
// plan.md 6.2: answering no to a gate ask opens an owner_no recap (allowed:
// split, new, drop) with the owner's words and never merges the PR.
func TestN30HistoryOwnerGateNoOpensOwnerNoRecap(t *testing.T) {
	t.Parallel()
	w := setupPassedGateWorld(t, `,"gate":"owner"`, nil)
	coordAddr := w.state().Project.RunID

	w.pass() // N31 opens gate ask
	asks := openAsksForTask(w.state(), "T1")
	if len(asks) != 1 {
		t.Fatalf("open asks = %d, want 1", len(asks))
	}
	noOpt := findOptionPrefix(t, asks[0].Options, "No")
	coordBefore := len(sendsTo(t, w.srv.Requests(), coordAddr))
	answerOwnerAsk(t, w.eng, asks[0].ID, noOpt)

	// Next pass matches N30 and opens the owner_no recap; following pass wakes coordinator via N6.
	w.pass()
	w.pass()

	st := w.state()
	rec := flow.OpenRecap(st.Tasks[0])
	if rec == nil || rec.Trigger != flow.TriggerOwnerNo {
		t.Fatalf("open recap = %+v, want trigger %q", rec, flow.TriggerOwnerNo)
	}
	if !slices.Equal(rec.Allowed, []string{"split", "new", "drop"}) {
		t.Fatalf("recap allowed = %v, want [split new drop]", rec.Allowed)
	}
	if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 0 {
		t.Fatalf("merged after owner gate no: %v", merges)
	}
	coordSends := strings.Join(sendsTo(t, w.srv.Requests(), coordAddr)[coordBefore:], "\n")
	if !strings.Contains(coordSends, noOpt) {
		t.Errorf("coordinator wake missing owner's words %q:\n%s", noOpt, coordSends)
	}
}

// TestStaleGateAskCancelledWhenHMoves checks design.md 7.1 and plan.md 6.2:
// an open gate ask whose Head no longer covers H is auto-cancelled on the next
// pass, while a merge_only advance keeps the gate ask open.
func TestStaleGateAskCancelledWhenHMoves(t *testing.T) {
	t.Parallel()

	t.Run("new_commit_cancels_stale_gate_ask", func(t *testing.T) {
		t.Parallel()
		w := setupPassedGateWorld(t, `,"gate":"owner"`, nil)
		c1 := w.commits[0]

		w.pass() // N31 opens gate ask at c1
		asks := openAsksForTask(w.state(), "T1")
		if len(asks) != 1 || asks[0].Head != c1 {
			t.Fatalf("open asks at c1 = %+v, want 1 at %s", asks, c1)
		}
		firstAskID := asks[0].ID

		// Worker commits c2 and reports ready -> H moves past c1.
		w.commitAndReady("package demo\n// v2\n", greenRollup, true)
		c2 := w.commits[1]
		w.pass() // cancels stale gate ask and pushes c2

		st := w.state()
		idx := slices.IndexFunc(st.Asks, func(a flow.Ask) bool { return a.ID == firstAskID })
		if idx < 0 || st.Asks[idx].Status != flow.AskStatusCancelled {
			t.Fatalf("stale gate ask %s status = %+v, want cancelled", firstAskID, st.Asks)
		}

		// After 2 passing rounds at c2, N31 opens a fresh gate ask naming c2.
		w.pass() // N29 round 3
		w.passReviewRound()
		w.pass() // N29 round 4
		w.passReviewRound()
		w.pass() // N31 opens new gate ask at c2

		openNow := openAsksForTask(w.state(), "T1")
		if len(openNow) != 1 || openNow[0].ID == firstAskID || openNow[0].Head != c2 {
			t.Fatalf("open asks at c2 = %+v, want 1 new gate ask at %s", openNow, c2)
		}
	})

	t.Run("merge_only_advance_keeps_gate_ask_open", func(t *testing.T) {
		t.Parallel()
		w := setupPassedGateWorld(t, `,"gate":"owner"`, nil)
		c1 := w.commits[0]

		w.pass() // N31 opens gate ask at c1
		asks := openAsksForTask(w.state(), "T1")
		if len(asks) != 1 {
			t.Fatalf("open asks = %d, want 1", len(asks))
		}
		firstAskID := asks[0].ID

		// Simulate a clean merge_only advance from c1 to m2: Covers(a, c1, m2) is true.
		w.edit(func(s *flow.State) {
			s.Tasks[0].Attempts[0].MergeOnly = append(s.Tasks[0].Attempts[0].MergeOnly, flow.Advance{From: c1, To: "m2"})
		})
		writePRJSON(t, w.gh, "pr.json", "m2", greenRollup)
		w.pass()

		st := w.state()
		idx := slices.IndexFunc(st.Asks, func(a flow.Ask) bool { return a.ID == firstAskID })
		if idx < 0 || st.Asks[idx].Status != flow.AskStatusOpen {
			t.Fatalf("gate ask %s after merge_only advance = %+v, want still open", firstAskID, st.Asks)
		}
	})
}

// TestN31HistoryOpenReleaseAskHoldsBeforeMerge checks KB-ASK-13 and design.md
// 4.7 N31/N42: an open gated owner ask (e.g. release) naming a task holds it
// before merge even when Task.Gate is empty, and answering it unblocks N42.
func TestN31HistoryOpenReleaseAskHoldsBeforeMerge(t *testing.T) {
	t.Parallel()
	w := setupPassedGateWorld(t, "", nil)

	relPayload := `{"kind":"release","task":"T1","question":"Tag v1.2.0 for T1?","options":["Yes, tag v1.2.0","Wait"],"recommend":"Yes, tag v1.2.0"}`
	if err := w.eng.Report(w.coordTok, "ask", json.RawMessage(relPayload)); err != nil {
		t.Fatalf("coordinator open release ask: %v", err)
	}
	askID := w.state().Asks[0].ID

	// Pass holds at N31 and does not merge.
	w.pass()
	st := w.state()
	if st.Tasks[0].LastRow != "N31" {
		t.Fatalf("LastRow with open release ask = %q, want N31", st.Tasks[0].LastRow)
	}
	if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 0 {
		t.Fatalf("merged while release ask open: %v", merges)
	}

	// Owner answers release ask -> next pass merges via N42.
	answerOwnerAsk(t, w.eng, askID, "Yes, tag v1.2.0")
	w.pass()
	if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 1 {
		t.Fatalf("pr merge calls after release ask answered = %d, want 1", len(merges))
	}
}

// TestN42HistoryFreshMergeCheckRejectsOpenOwnerAsk verifies execMerge's fresh
// N42 re-check (design.md 4.7, 8) calls off merge if an open gated owner ask
// exists for the task.
func TestN42HistoryFreshMergeCheckRejectsOpenOwnerAsk(t *testing.T) {
	t.Parallel()
	w := setupPassedGateWorld(t, "", nil)
	relPayload := `{"kind":"release","task":"T1","question":"Hold T1 merge?","options":["Yes, merge","Wait"],"recommend":"Yes, merge"}`
	if err := w.eng.Report(w.coordTok, "ask", json.RawMessage(relPayload)); err != nil {
		t.Fatalf("open release ask: %v", err)
	}

	st := w.state()
	dec, calledOff, err := w.eng.execMerge(w.ctx, w.id, &st, 0, flow.Facts{
		Tip: w.commits[0], TipRel: flow.RelSame, Worker: flow.Run{State: flow.RunIdle},
		PR: flow.PR{Answered: true, Number: 7, State: flow.PROpen, Head: w.commits[0], HeadRel: flow.RelSame, Mergeable: flow.MergeableClean, Checks: flow.ChecksGreen},
	})
	if err != nil {
		t.Fatalf("execMerge err = %v", err)
	}
	if !calledOff {
		t.Fatalf("execMerge calledOff = false (dec=%q), want true when open owner ask names T1", dec)
	}
	if merges := ghCalls(t, w.gh, "pr merge"); len(merges) != 0 {
		t.Fatalf("execMerge merged despite open owner ask: %v", merges)
	}
}

// TestN24HistorySplitChildAfterRetroDrop checks design.md 4.5 N24 and plan.md
// 6.2: a split child whose parent had a retro hits review recap -> N24 opens
// one owner ask (no recap yet) -> owner picks Drop -> task is marked Dropped
// and cleaned up via N1.
func TestN24HistorySplitChildAfterRetroDrop(t *testing.T) {
	t.Parallel()
	w := setupSplitChildRetroRecap(t)

	w.pass() // N24 opens owner ask; no recap yet
	st := w.state()
	t1 := st.Tasks[1]
	if t1.LastRow != "N24" {
		t.Fatalf("T1 LastRow = %q, want N24", t1.LastRow)
	}
	if flow.OpenRecap(t1) != nil {
		t.Fatalf("recap opened before owner answered N24: %+v", t1.Recaps)
	}
	asks := openAsksForTask(st, "T1")
	if len(asks) != 1 || !asks[0].Gated {
		t.Fatalf("open owner asks for T1 = %+v, want 1 gated ask", asks)
	}

	dropOpt := findOptionContains(t, asks[0].Options, "drop")
	answerOwnerAsk(t, w.eng, asks[0].ID, dropOpt)
	w.pass() // N1 marks/cleans up dropped task

	st = w.state()
	t1 = st.Tasks[1]
	if t1.Dropped == nil {
		t.Fatalf("T1 not marked Dropped after owner answered %q: %+v", dropOpt, t1)
	}
	if flow.OpenRecap(t1) != nil {
		t.Fatalf("recap should not open when owner chose Drop: %+v", t1.Recaps)
	}
}

// TestN24HistorySplitChildAfterRetroContinueToN25 checks design.md 4.5 N24->N25:
// when the owner answers the non-drop option on N24's ask, N25 opens a review
// recap (allowed: split, new, drop) carrying the owner's words to the coordinator.
func TestN24HistorySplitChildAfterRetroContinueToN25(t *testing.T) {
	t.Parallel()
	w := setupSplitChildRetroRecap(t)
	coordAddr := w.state().Project.RunID

	w.pass() // N24 opens owner ask
	asks := openAsksForTask(w.state(), "T1")
	if len(asks) != 1 {
		t.Fatalf("open asks for T1 = %d, want 1", len(asks))
	}
	dropOpt := findOptionContains(t, asks[0].Options, "drop")
	var goOnOpt string
	for _, o := range asks[0].Options {
		if o != dropOpt {
			goOnOpt = o
			break
		}
	}
	if goOnOpt == "" {
		t.Fatalf("no non-drop option in %v", asks[0].Options)
	}

	coordBefore := len(sendsTo(t, w.srv.Requests(), coordAddr))
	answerOwnerAsk(t, w.eng, asks[0].ID, goOnOpt)
	w.pass() // N25 opens review recap
	w.pass() // N6 wakes coordinator

	st := w.state()
	t1 := st.Tasks[1]
	if t1.Dropped != nil {
		t.Fatalf("T1 unexpectedly dropped: %+v", t1.Dropped)
	}
	rec := flow.OpenRecap(t1)
	if rec == nil || rec.Trigger != flow.TriggerReview {
		t.Fatalf("open recap = %+v, want trigger %q", rec, flow.TriggerReview)
	}
	if !slices.Equal(rec.Allowed, []string{"split", "new", "drop"}) {
		t.Fatalf("recap allowed = %v, want [split new drop]", rec.Allowed)
	}
	coordSends := strings.Join(sendsTo(t, w.srv.Requests(), coordAddr)[coordBefore:], "\n")
	if !strings.Contains(coordSends, goOnOpt) {
		t.Errorf("coordinator wake missing owner's words %q:\n%s", goOnOpt, coordSends)
	}
}

// TestN1HistoryCancelsDecisionAsksKeepsOwnerAsks checks design.md 4.1 N1,
// 7.1, KB-ASK-10, and plan.md 6.2: when a task ends, N1 cancels its open
// decision asks while keeping its open gated owner asks alive (and leaving
// other tasks' decision asks untouched).
func TestN1HistoryCancelsDecisionAsksKeepsOwnerAsks(t *testing.T) {
	t.Parallel()
	r := newAskRig(t)

	// Worker opens a decision ask on T1; coordinator opens a gated release ask on T1.
	decPayload := `{"kind":"decision","question":"Use map or slice?","options":["map","slice"],"recommend":"map"}`
	if err := r.eng.Report(r.workTok, "ask", json.RawMessage(decPayload)); err != nil {
		t.Fatalf("worker decision ask: %v", err)
	}
	relPayload := `{"kind":"release","task":"T1","question":"Publish v2.0.0 after T1?","options":["Yes, publish","Wait"],"recommend":"Yes, publish"}`
	if err := r.eng.Report(r.coordTok, "ask", json.RawMessage(relPayload)); err != nil {
		t.Fatalf("coordinator release ask: %v", err)
	}
	// Add a second open task T2 with its own open decision ask.
	r.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks, flow.Task{ID: "T2", Title: "feat: second task"})
		s.Asks = append(s.Asks, flow.Ask{
			ID: "ask-t2-dec", Project: r.id, Kind: flow.KindDecision, Gated: false,
			Question: "Keep T2 open?", Options: []string{"Yes", "No"}, Recommend: "Yes",
			Task: "T2", AskedBy: flow.RoleWorker, Status: flow.AskStatusOpen, At: r.now,
		})
	})

	// Drop T1 so N1 runs cleanup.
	if err := r.eng.Report(r.coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"superseded"}`)); err != nil {
		t.Fatalf("drop T1: %v", err)
	}
	r.pass() // N1 cleanup

	byKindTask := map[string]flow.Ask{}
	for _, a := range r.state().Asks {
		byKindTask[a.Task+":"+a.Kind] = a
	}
	if a := byKindTask["T1:decision"]; a.Status != flow.AskStatusCancelled {
		t.Errorf("T1 decision ask status = %q, want cancelled (N1)", a.Status)
	}
	if a := byKindTask["T1:release"]; a.Status != flow.AskStatusOpen {
		t.Errorf("T1 release (owner) ask status = %q, want open (KB-ASK-10)", a.Status)
	}
	if a := byKindTask["T2:decision"]; a.Status != flow.AskStatusOpen {
		t.Errorf("T2 decision ask status = %q, want open (other task untouched)", a.Status)
	}
}

// TestSlotsHistoryOwnerAnsweredPriorityAndExtraSlot checks design.md 4.10 and
// plan.md 6.2 in the full engine pass: at MaxRunning == 1 with T1 running,
// an owner-answered normal task T2 beats an urgent task T3 for the 1 extra slot.
func TestSlotsHistoryOwnerAnsweredPriorityAndExtraSlot(t *testing.T) {
	t.Parallel()
	w := newRestartWorld(t, "Owner-answered slot priority")
	w.edit(func(s *flow.State) { s.Settings.MaxRunning = 1 })
	w.pass() // J7 start coordinator
	coordAddr := w.state().Project.RunID
	w.applyPlan() // plans T1
	w.waitIdle(coordAddr)
	w.pass() // N11 creates attempt for T1
	w.pass() // N23 starts worker for T1
	w.srv.SetStatus(w.workerID, &hc.Status{App: "claude-code", State: "working"})

	// Add queued T2 (normal, owner-answered gated ask) and T3 (urgent) while T1 holds the 1 slot.
	w.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks,
			flow.Task{ID: "T2", EstimateLines: 50, Title: "feat: owner answered task", Brief: "Start on extra slot."},
			flow.Task{ID: "T3", EstimateLines: 50, Priority: flow.PriorityUrgent, Title: "feat: urgent task", Brief: "Wait behind owner answered."},
		)
		s.Asks = append(s.Asks, flow.Ask{
			ID: "ask-t2-rel", Project: w.id, Kind: flow.KindRelease, Gated: true,
			Question: "Start T2?", Options: []string{"Yes", "No"}, Recommend: "Yes",
			Task: "T2", AskedBy: flow.RoleCoordinator, Status: flow.AskStatusAnswered,
			Answer: "Yes", AnsweredBy: "owner", At: w.now.Add(-time.Minute), AnsweredAt: w.now,
		})
	})
	w.pass()

	st := w.state()
	if len(st.Tasks[1].Attempts) == 0 {
		t.Fatalf("owner-answered T2 did not get the extra slot (LastRow=%q, T3 attempts=%d)", st.Tasks[1].LastRow, len(st.Tasks[2].Attempts))
	}
	if len(st.Tasks[2].Attempts) != 0 {
		t.Fatalf("urgent T3 should have waited at N10 while owner-answered T2 took the single extra slot")
	}
}

// TestViewOwnerAnswerAndOwnerGateStages checks design.md 4.11 and plan.md 6.2:
// GraphViewOf, StatusViewOf, and ProjectsView emit owner_gate for open gate
// asks and owner_answer for other open gated owner asks (even on a done task),
// with the ask's question as WhyWaiting/Why, while ignoring non-gated decision
// asks and closed asks.
func TestViewOwnerAnswerAndOwnerGateStages(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	st := flow.State{
		Settings: flow.DefaultSettings(),
		Project:  flow.Project{ID: "proj-gates", Folder: "/tmp/gates", Started: true},
		Tasks: []flow.Task{
			{ID: "T-gate", Title: "feat: gated", LastRow: "N31", Attempts: []flow.Attempt{{BaseCommit: "b0", Ready: []flow.Ready{{Head: "c1"}}}}},
			{ID: "T-rel", Title: "feat: release", LastRow: "N31", Attempts: []flow.Attempt{{BaseCommit: "b0", Ready: []flow.Ready{{Head: "c1"}}}}},
			{ID: "T-done-ask", Title: "feat: done with open ask", Done: &flow.Merged{MergedCommit: "m1"}, LastRow: "N1"},
			{ID: "T-dec", Title: "feat: decision ask", LastRow: "N23", Attempts: []flow.Attempt{{BaseCommit: "b0", RunID: "w-dec"}}},
			{ID: "T-answered", Title: "feat: answered gate", Done: &flow.Merged{MergedCommit: "m2"}, LastRow: "N1"},
		},
		Asks: []flow.Ask{
			{ID: "a1", Kind: flow.KindGate, Gated: true, Task: "T-gate", Head: "c1", Question: "Approve T-gate at c1?", Status: flow.AskStatusOpen, At: t0},
			{ID: "a2", Kind: flow.KindRelease, Gated: true, Task: "T-rel", Head: "c1", Question: "Ship release v1.0?", Status: flow.AskStatusOpen, At: t0},
			{ID: "a3", Kind: flow.KindRelease, Gated: true, Task: "T-done-ask", Question: "Tag v1.1 after merge?", Status: flow.AskStatusOpen, At: t0},
			{ID: "a4", Kind: flow.KindDecision, Gated: false, Task: "T-dec", Question: "SQLite or JSON?", Status: flow.AskStatusOpen, At: t0},
			{ID: "a5", Kind: flow.KindGate, Gated: true, Task: "T-answered", Question: "Approve T-answered?", Status: flow.AskStatusAnswered, Answer: "Yes", AnsweredBy: "owner", At: t0, AnsweredAt: t0},
		},
	}

	gv := GraphViewOf(st, nil)
	byRef := map[string]TaskView{}
	for _, tv := range gv.Tasks {
		byRef[tv.Ref] = tv
	}
	sv := StatusViewOf(st, nil)
	statusByID := map[string]TaskStatus{}
	for _, ts := range sv {
		statusByID[ts.ID] = ts
	}

	want := map[string]struct{ stage, why string }{
		"T-gate":     {"owner_gate", "Approve T-gate at c1?"},
		"T-rel":      {"owner_answer", "Ship release v1.0?"},
		"T-done-ask": {"owner_answer", "Tag v1.1 after merge?"},
		"T-dec":      {"working", "Worker is working"},
		"T-answered": {"done", ""},
	}
	for id, w := range want {
		if tv := byRef[id]; tv.Stage != w.stage || tv.WhyWaiting != w.why {
			t.Errorf("GraphView[%s] = (stage=%q, why=%q), want (%q, %q)", id, tv.Stage, tv.WhyWaiting, w.stage, w.why)
		}
		if ts := statusByID[id]; ts.Stage != w.stage {
			t.Errorf("StatusView[%s].Stage = %q, want %q", id, ts.Stage, w.stage)
		}
	}

	pv := ProjectsView([]flow.State{st})[0]
	if pv.Counts["owner_gate"] != 1 || pv.Counts["owner_answer"] != 2 {
		t.Errorf("ProjectsView Counts = %v, want owner_gate=1 owner_answer=2", pv.Counts)
	}
}
