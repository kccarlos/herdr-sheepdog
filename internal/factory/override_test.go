package factory

// PR 7.1, TDD step 1: engine history tests for KindOverride, the engine card
// filler, Attempt.Overrides binding to the card snapshot (head and checks
// shown when the card was filled), exact option equality (no word list), role
// and proof refusals (KB-ASK-15, KB-ASK-16, KB-ASK-19), O29 (open override
// card not auto-cancelled on H move), and coordinator.md's override line (M3).
//
// Report contract pinned by these tests (design.md 4.3b, 7.2; plan.md 7.1):
//   - Coordinator raises an override card with verb "ask":
//       {"kind":"override","tasks":["T1","T2"]}
//   - The engine fills the card itself: Targets (PR, Head, Checks, Unreported),
//     Options [flow.OverrideOptionMerge, flow.OverrideOptionCancel],
//     Recommend flow.OverrideOptionCancel (never the merging option).
//   - Verified tap AnswerProof with exact flow.OverrideOptionMerge writes
//     Attempt.Overrides[{Head, ChecksShown, Spent:false, ProofID}] for each
//     target using the card's stored Head and Checks (never re-read at answer
//     time). Inert until 7.2 (no recap closing, no override merge yet).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

const pendingRollup = `[{"__typename":"CheckRun","name":"ci","status":"IN_PROGRESS","conclusion":""}]`

// setupTwoTaskOverrideWorld plans T1 and T2 (max_running 2), starts both
// workers, commits and reports ready for both tasks, and records open PR
// snapshots (T1: PR #7 pending, T2: PR #8 green) in state and memory.
func setupTwoTaskOverrideWorld(t *testing.T) (w *restartWorld, tokByTask map[string]string, c1, c2 string) { //nolint:unparam // the token map is kept for later 7.x histories
	t.Helper()
	w = newRestartWorld(t, "Ship two stacked PRs")
	tokByTask = map[string]string{}
	w.srv.OnRun = func(term hc.Terminal, p hc.RunParams) {
		switch p.Labels["sheepdog.role"] {
		case flow.RoleCoordinator:
			w.coordTok = p.Env["SHEEPDOG_RUN_TOKEN"]
		case flow.RoleWorker:
			w.workTok = p.Env["SHEEPDOG_RUN_TOKEN"]
			w.workerID = term.Address
			if tid := p.Labels["sheepdog.task"]; tid != "" {
				tokByTask[tid] = p.Env["SHEEPDOG_RUN_TOKEN"]
			}
		}
	}
	w.pass() // J7 start coordinator
	st := w.state()
	w.srv.SetStatus(st.Project.RunID, &hc.Status{App: "claude-code", State: "working"})
	planBody := `{"tasks":[` +
		`{"id":"T1","estimate_lines":50,"title":"feat: first change","brief":"Add one.go.",` + testScopeJSON + `,"outcomes":[{"text":"one exists","proved_by":"manual: one.go"}]},` +
		`{"id":"T2","estimate_lines":50,"title":"feat: second change","brief":"Add two.go.",` + testScopeJSON + `,"outcomes":[{"text":"two exists","proved_by":"manual: two.go"}]}` +
		`]}`
	if err := w.eng.Report(w.coordTok, "plan apply", []byte(planBody)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	w.waitIdle(st.Project.RunID)
	w.pass() // N11 creates attempts for T1 and T2
	w.pass() // N23 starts workers for T1 and T2

	st = w.state()
	wt1 := st.Tasks[0].Attempts[0].Worktree
	wt2 := st.Tasks[1].Attempts[0].Worktree
	w.waitIdle(st.Tasks[0].Attempts[0].RunID)
	w.waitIdle(st.Tasks[1].Attempts[0].RunID)

	c1 = commitFile(t, wt1, "one.go", "package demo\n// one v1\n")
	c2 = commitFile(t, wt2, "two.go", "package demo\n// two v1\n")
	w.commits = append(w.commits, c1, c2)

	if err := w.eng.Report(tokByTask["T1"], "ready", []byte(`{"head":"`+c1+`","proofs":[{"outcome":"one exists","artifact":"one.go"}],"touched":"one.go","skipped":""}`)); err != nil {
		t.Fatalf("ready T1: %v", err)
	}
	if err := w.eng.Report(tokByTask["T2"], "ready", []byte(`{"head":"`+c2+`","proofs":[{"outcome":"two exists","artifact":"two.go"}],"touched":"two.go","skipped":""}`)); err != nil {
		t.Fatalf("ready T2: %v", err)
	}

	w.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].PR = 7
		s.Tasks[1].Attempts[0].PR = 8
	})
	w.eng.mu.Lock()
	if w.eng.prs == nil {
		w.eng.prs = map[string]flow.PR{}
	}
	w.eng.prs[wt1] = flow.PR{State: flow.PROpen, Number: 7, Head: c1, Mergeable: flow.MergeableClean, Checks: flow.ChecksPending, Answered: true}
	w.eng.prs[wt2] = flow.PR{State: flow.PROpen, Number: 8, Head: c2, Mergeable: flow.MergeableClean, Checks: flow.ChecksGreen, Answered: true}
	w.eng.mu.Unlock()
	return w, tokByTask, c1, c2
}

// TestOverrideCardFillerTwoPRsAndUnreportedWork checks plan.md 7.1 done-when:
// coordinator raises one override card for two tasks (one with unreported work
// in its worktree); the engine fills Targets (PR, short H, checks state,
// unreported), sets exact Yes/No options with the negative one recommended,
// emits Engine.Changes, and a paired tap Yes writes Attempt.Overrides for both
// tasks bound to those shown heads and logs device_id.
func TestOverrideCardFillerTwoPRsAndUnreportedWork(t *testing.T) {
	t.Parallel()
	w, _, c1, c2 := setupTwoTaskOverrideWorld(t)

	// Add unreported work (an extra commit ahead of H) in T1's worktree.
	wt1 := w.state().Tasks[0].Attempts[0].Worktree
	_ = commitFile(t, wt1, "unreported.go", "package demo\n// unreported\n")

	changes := w.eng.Changes()
	if err := w.eng.Report(w.coordTok, "ask", json.RawMessage(`{"kind":"override","tasks":["T1","T2"]}`)); err != nil {
		t.Fatalf("coordinator override ask: %v", err)
	}

	st := w.state()
	if len(st.Asks) != 1 {
		t.Fatalf("State.Asks len = %d, want 1", len(st.Asks))
	}
	ask := st.Asks[0]
	if ask.Kind != flow.KindOverride || !ask.Gated || ask.Status != flow.AskStatusOpen {
		t.Fatalf("override ask header = %+v, want open gated override", ask)
	}
	if !slices.Equal(ask.Options, []string{flow.OverrideOptionMerge, flow.OverrideOptionCancel}) || ask.Recommend != flow.OverrideOptionCancel {
		t.Fatalf("override options/recommend = (%v, %q), want ([%q %q], %q)",
			ask.Options, ask.Recommend, flow.OverrideOptionMerge, flow.OverrideOptionCancel, flow.OverrideOptionCancel)
	}
	wantTargets := []flow.OverrideTarget{
		{Task: "T1", PR: 7, Head: c1, Checks: flow.ChecksPending, Unreported: true},
		{Task: "T2", PR: 8, Head: c2, Checks: flow.ChecksGreen, Unreported: false},
	}
	if !slices.Equal(ask.Targets, wantTargets) {
		t.Fatalf("override ask.Targets = %+v, want %+v", ask.Targets, wantTargets)
	}
	gotChanges := drainChanges(changes)
	if !slices.ContainsFunc(gotChanges, func(c Change) bool { return c.Kind == flow.EvAskOpen && c.AskID == ask.ID }) {
		t.Fatalf("Engine.Changes() = %+v, want EvAskOpen for %s", gotChanges, ask.ID)
	}

	// Paired device tap Yes writes Attempt.Overrides for both tasks and logs device_id.
	proof, err := w.eng.MintProof(ask.ID, flow.OverrideOptionMerge, ProofInputTap, "dev-phone-1")
	if err != nil {
		t.Fatalf("MintProof: %v", err)
	}
	if err := w.eng.Answer(ask.ID, flow.OverrideOptionMerge, proof); err != nil {
		t.Fatalf("Engine.Answer(override Yes): %v", err)
	}

	st = w.state()
	if st.Asks[0].Status != flow.AskStatusAnswered || st.Asks[0].AnsweredBy != "owner" || st.Asks[0].DeviceID != "dev-phone-1" || st.Asks[0].ProofID != proof.ID {
		t.Fatalf("answered override ask = %+v", st.Asks[0])
	}
	wantT1 := []flow.Override{{Head: c1, ChecksShown: flow.ChecksPending, Spent: false, ProofID: proof.ID}}
	wantT2 := []flow.Override{{Head: c2, ChecksShown: flow.ChecksGreen, Spent: false, ProofID: proof.ID}}
	if !slices.Equal(st.Tasks[0].Attempts[0].Overrides, wantT1) {
		t.Fatalf("T1 Attempt.Overrides = %+v, want %+v", st.Tasks[0].Attempts[0].Overrides, wantT1)
	}
	if !slices.Equal(st.Tasks[1].Attempts[0].Overrides, wantT2) {
		t.Fatalf("T2 Attempt.Overrides = %+v, want %+v", st.Tasks[1].Attempts[0].Overrides, wantT2)
	}

	logBytes, err := os.ReadFile(w.paths.Log(w.id))
	if err != nil {
		t.Fatalf("read log.jsonl: %v", err)
	}
	if !strings.Contains(string(logBytes), "dev-phone-1") {
		t.Errorf("log.jsonl missing answering device_id dev-phone-1:\n%s", logBytes)
	}
}

// TestOverrideYesUsesCardSnapshotNotLaterReady checks plan.md 7.1 unit test (1)
// and O29: card filled at c2 (with pending checks), worker later reports ready
// at c3 (and PR snapshot turns green), owner answers Yes -> Attempt.Overrides
// stores override{head: c2, checks_shown: pending} from the card when it was
// filled, never re-read at answer time.
func TestOverrideYesUsesCardSnapshotNotLaterReady(t *testing.T) {
	t.Parallel()
	w := newRestartWorld(t, "Bind override to card snapshot")
	w.pass() // J7 start coordinator
	w.applyPlan()
	w.waitIdle(w.state().Project.RunID)
	w.pass() // N11 new attempt
	w.pass() // N23 start worker
	w.waitIdle(w.workerID)

	w.commitAndReady("package demo\n// c2\n", pendingRollup, true)
	c2 := w.commits[0]
	w.pass() // N26 pushes c2 and opens PR #7 with pending checks

	// Coordinator opens override card at c2 while checks are pending.
	if err := w.eng.Report(w.coordTok, "ask", json.RawMessage(`{"kind":"override","tasks":["T1"]}`)); err != nil {
		t.Fatalf("coordinator override ask at c2: %v", err)
	}
	ask := w.state().Asks[0]
	if len(ask.Targets) != 1 || ask.Targets[0].Head != c2 || ask.Targets[0].Checks != flow.ChecksPending {
		t.Fatalf("card filled targets = %+v, want head=%s checks=%s", ask.Targets, c2, flow.ChecksPending)
	}

	// Worker commits and reports ready at c3, and PR snapshot moves to c3 green.
	w.commitAndReady("package demo\n// c3\n", greenRollup, false)
	c3 := w.commits[1]
	writePRJSON(t, w.gh, "view.json", c3, greenRollup)
	w.pass() // O29: open override card is not auto-cancelled when H moves past card head

	if got := w.state().Asks[0].Status; got != flow.AskStatusOpen {
		t.Fatalf("override ask status after H moved to c3 = %q, want open (O29)", got)
	}

	// Owner answers Yes -> Attempt.Overrides must record c2 and ChecksPending, not c3 or ChecksGreen.
	proof, err := w.eng.MintProof(ask.ID, flow.OverrideOptionMerge, ProofInputTap, "dev-phone-1")
	if err != nil {
		t.Fatalf("MintProof: %v", err)
	}
	if err := w.eng.Answer(ask.ID, flow.OverrideOptionMerge, proof); err != nil {
		t.Fatalf("Engine.Answer(Yes): %v", err)
	}
	gotOverrides := w.state().Tasks[0].Attempts[0].Overrides
	want := []flow.Override{{Head: c2, ChecksShown: flow.ChecksPending, Spent: false, ProofID: proof.ID}}
	if !slices.Equal(gotOverrides, want) {
		t.Fatalf("Attempt.Overrides = %+v, want %+v (bound to card snapshot, not c3)", gotOverrides, want)
	}
}

// TestOverrideNoAnswerWritesNoOverrideAndExactOptionMatch checks owner rules:
// answering with the negative option writes no Attempt.Overrides, and option
// matching is exact (no word list or prefix match).
func TestOverrideNoAnswerWritesNoOverrideAndExactOptionMatch(t *testing.T) {
	t.Parallel()
	w, _, _, _ := setupTwoTaskOverrideWorld(t)

	if err := w.eng.Report(w.coordTok, "ask", json.RawMessage(`{"kind":"override","tasks":["T1","T2"]}`)); err != nil {
		t.Fatalf("coordinator override ask: %v", err)
	}
	askID := w.state().Asks[0].ID

	// A fuzzy/word-list answer ("Yes") that does not equal OverrideOptionMerge is refused invalid_option.
	badProof, err := w.eng.MintProof(askID, "Yes", ProofInputTap, "dev-phone-1")
	if err != nil {
		t.Fatalf("MintProof(Yes): %v", err)
	}
	if err := w.eng.Answer(askID, "Yes", badProof); refusalCode(err) != "invalid_option" {
		t.Fatalf("Answer(fuzzy Yes) = %v, want invalid_option", err)
	}

	// Answering with the exact negative option closes the ask and writes zero overrides.
	answerOwnerAsk(t, w.eng, askID, flow.OverrideOptionCancel)
	st := w.state()
	if st.Asks[0].Status != flow.AskStatusAnswered || st.Asks[0].Answer != flow.OverrideOptionCancel {
		t.Fatalf("answered ask = %+v, want %q", st.Asks[0], flow.OverrideOptionCancel)
	}
	for _, task := range st.Tasks {
		if len(task.Attempts[0].Overrides) != 0 {
			t.Fatalf("task %s got overrides after negative answer: %+v", task.ID, task.Attempts[0].Overrides)
		}
	}
}

// TestOverrideRefusalsRoleProofAndNoReady checks plan.md 7.1 refusals:
// worker/reviewer report ask --kind override -> kind_not_allowed;
// task with no ready -> invalid_ask;
// coordinator answer on override -> proof_required;
// empty or local proof -> proof_required / proof_rejected (KB-ASK-15, KB-ASK-16, KB-ASK-19).
func TestOverrideRefusalsRoleProofAndNoReady(t *testing.T) {
	t.Parallel()
	r := newAskRig(t) // T1 has a commit in worktree, NO ready report yet

	revAddr, err := startRun(r.ctx, r.client, world.RunSpec{Name: "rev-ovr-1", Cwd: r.worktree, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	r.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{
			RunID: revAddr, RunTokenHash: tokenHash("tok-rev-ovr"), Round: 1, Head: r.commits[0], Code: "code1",
		}
	})

	// 1. Worker and reviewer terminals cannot raise an override ask.
	for _, tc := range []struct {
		name string
		tok  string
	}{
		{name: "worker_token", tok: r.workTok},
		{name: "reviewer_token", tok: "tok-rev-ovr"},
	} {
		t.Run(tc.name+"_refused_kind_not_allowed", func(t *testing.T) {
			err := r.eng.Report(tc.tok, "ask", json.RawMessage(`{"kind":"override","tasks":["T1"]}`))
			if refusalCode(err) != "kind_not_allowed" {
				t.Fatalf("%s ask --kind override = %v, want kind_not_allowed", tc.name, err)
			}
		})
	}

	// 2. Coordinator naming a task with no ready report (or empty/unknown tasks) is refused invalid_ask.
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{name: "task_with_no_ready", payload: `{"kind":"override","tasks":["T1"]}`},
		{name: "empty_tasks", payload: `{"kind":"override","tasks":[]}`},
		{name: "unknown_task", payload: `{"kind":"override","tasks":["NO_SUCH"]}`},
	} {
		t.Run(tc.name+"_refused_invalid_ask", func(t *testing.T) {
			err := r.eng.Report(r.coordTok, "ask", json.RawMessage(tc.payload))
			if refusalCode(err) != "invalid_ask" {
				t.Fatalf("%s = %v, want invalid_ask", tc.name, err)
			}
		})
	}
	if len(r.state().Asks) != 0 {
		t.Fatalf("refused override asks mutated State.Asks: %+v", r.state().Asks)
	}

	// Now report ready for T1 so coordinator can open a valid override ask, then test answer refusals.
	if err := os.WriteFile(filepath.Join(r.worktree, "demo.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cReady := commitFile(t, r.worktree, "demo.go", "package demo\n// ready\n")
	if err := r.eng.Report(r.workTok, "ready", []byte(`{"head":"`+cReady+`","proofs":[{"outcome":"demo exists","artifact":"demo.go"}],"touched":"demo.go","skipped":""}`)); err != nil {
		t.Fatalf("ready T1: %v", err)
	}
	if err := r.eng.Report(r.coordTok, "ask", json.RawMessage(`{"kind":"override","tasks":["T1"]}`)); err != nil {
		t.Fatalf("coordinator override ask after ready: %v", err)
	}
	askID := r.state().Asks[0].ID

	// 3. Coordinator answering an override ask is refused proof_required.
	coordAns := fmt.Sprintf(`{"ask":%q,"option":%q,"why":"owner asked"}`, askID, flow.OverrideOptionMerge)
	if err := r.eng.Report(r.coordTok, "answer", json.RawMessage(coordAns)); refusalCode(err) != "proof_required" {
		t.Fatalf("coordinator answering override = %v, want proof_required", err)
	}

	// 4. Empty proof is refused proof_required; local device proof is refused proof_rejected.
	if err := r.eng.Answer(askID, flow.OverrideOptionMerge, AnswerProof{}); refusalCode(err) != "proof_required" {
		t.Fatalf("Answer with empty proof = %v, want proof_required", err)
	}
	localProof := AnswerProof{ID: "prf-000000000001", AskID: askID, Option: flow.OverrideOptionMerge, InputKind: ProofInputTap, DeviceID: "local", At: r.now}
	if err := r.eng.Answer(askID, flow.OverrideOptionMerge, localProof); refusalCode(err) != "proof_rejected" {
		t.Fatalf("Answer with local device proof = %v, want proof_rejected", err)
	}
	if got := r.state().Tasks[0].Attempts[0].Overrides; len(got) != 0 {
		t.Fatalf("refused answers wrote Attempt.Overrides: %+v", got)
	}
}

// TestCoordinatorTemplateNamesOverrideCommand checks plan.md 7.1 (verify-review2 M3):
// coordinator.md tells the coordinator how to raise an override card.
func TestCoordinatorTemplateNamesOverrideCommand(t *testing.T) {
	if !strings.Contains(coordinatorTemplate, "ask --kind override") || !strings.Contains(coordinatorTemplate, "--task") {
		t.Fatalf("coordinator.md missing override ask command (--kind override --task):\n%s", coordinatorTemplate)
	}
}
