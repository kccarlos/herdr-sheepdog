package factory

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

// PR 12.1, TDD step 1: engine observation tests for observeTaskLocked
// (SplitFromHadRetro derived from len(parent.Retros) > 0; Facts.RetroUnavailable
// set via non-quota ResolveWithReason(RoleRetro, ...)) and report ready --reject
// refusal on a retro FIX round (plan.md 12.1, design.md 4.6/7.2, KB-RET-1/3/4/7).

// TestObserveTaskSplitFromHadRetroFromParentRetros checks KB-RET-7 and plan.md
// 12.1: observeTaskLocked derives Facts.SplitFromHadRetro from len(parent.Retros) > 0
// (not the deleted legacy Task.HadRetro bool), routing the split child's review
// non-convergence to N24 instead of N28 or N25.
func TestObserveTaskSplitFromHadRetroFromParentRetros(t *testing.T) {
	t.Parallel()
	w := newRestartWorld(t, "Split child after parent retro")
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
			ID:     "P1",
			Title:  "feat: parent task",
			Split:  &flow.Split{Children: []string{"T1"}},
			Retros: []flow.Retro{{Attempt: 1, Round: 4, Cause: "task-too-big", At: w.now}},
		}
		s.Tasks[0].SplitFrom = "P1"
		s.Tasks[0].Attempts[0].Rounds = []flow.Round{
			{N: 1, Kind: flow.RoundFull, Head: "c0", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 2, Kind: flow.RoundDelta, Head: "c0b", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 3, Kind: flow.RoundDelta, Head: c1, Verdict: flow.VerdictChanges, Majors: 1},
		}
		s.Tasks = append([]flow.Task{parent}, s.Tasks...)
	})

	w.pass()
	st := w.state()
	if got := st.Tasks[1].LastRow; got != "N24" {
		t.Fatalf("split child LastRow = %q, want N24 (derived from len(parent.Retros) > 0)", got)
	}
	if asks := openAsksForTask(st, "T1"); len(asks) != 1 || asks[0].AskedBy != flow.AskedByN24 {
		t.Fatalf("open asks for T1 = %+v, want 1 N24 owner ask", asks)
	}
}

// TestObserveTaskSetsRetroUnavailableOnNonQuotaResolveFailure checks plan.md
// 12.1 and B2: observeTaskLocked sets Facts.RetroUnavailable = true when
// ResolveWithReason(RoleRetro, ...) fails for a non-quota reason (no retro
// candidates when models.retro is unset, or launchCapability refusing RoleRetro
// in 12.1 even when models.retro is configured), and Facts.RetroUnavailable
// marshals under json:"retro_unavailable,omitempty".
func TestObserveTaskSetsRetroUnavailableOnNonQuotaResolveFailure(t *testing.T) {
	t.Parallel()

	t.Run("json_tag_retro_unavailable", func(t *testing.T) {
		rawTrue, err := json.Marshal(flow.Facts{RetroUnavailable: true})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(rawTrue), `"retro_unavailable":true`) {
			t.Fatalf("Facts{RetroUnavailable: true} JSON = %s, want \"retro_unavailable\":true", rawTrue)
		}
		rawFalse, err := json.Marshal(flow.Facts{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rawFalse), "retro_unavailable") {
			t.Fatalf("Facts{} JSON = %s, want retro_unavailable omitted", rawFalse)
		}
	})

	for _, tc := range []struct {
		name   string
		models map[string][]flow.Candidate
	}{
		{name: "models_retro_unset", models: nil},
		{name: "models_retro_set_capability_refused_in_12_1", models: map[string][]flow.Candidate{
			flow.RoleRetro: {{CLI: world.AgyCLI, Model: "gemini-4-carbon-high", Effort: "high"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newRestartWorld(t, "Check retro_unavailable observation")
			w.pass() // J7 start coordinator
			coordAddr := w.state().Project.RunID
			w.applyPlan()
			w.waitIdle(coordAddr)
			w.pass() // N11 new attempt
			w.pass() // N23 start worker
			w.waitIdle(w.workerID)
			w.commitAndReady("package demo\n// v1\n", greenRollup, true)
			if tc.models != nil {
				w.edit(func(s *flow.State) { s.Settings.Models = tc.models })
			}

			st := w.state()
			w.eng.mu.Lock()
			facts, ok := w.eng.observeTaskLocked(w.ctx, w.id, &st, 0, true, flow.Run{State: flow.RunIdle}, nil, false, 0)
			w.eng.mu.Unlock()
			if !ok {
				t.Fatal("observeTaskLocked returned ok=false, want true")
			}
			if !facts.RetroUnavailable {
				t.Fatalf("%s: observeTaskLocked facts.RetroUnavailable = false, want true", tc.name)
			}
		})
	}
}

// TestReportReadyRejectOnRetroFixRefused checks design.md 4.6 L10, 7.2, and
// KB-RET-4: when the newest covering round is a retro FIX, the ladder says fix
// (L4) and a worker's `ready --reject` is refused with nothing_to_reject.
func TestReportReadyRejectOnRetroFixRefused(t *testing.T) {
	t.Parallel()
	w := newRestartWorld(t, "Reject on retro FIX refused")
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
		s.Tasks[0].Attempts[0].Rounds = []flow.Round{
			{N: 1, Kind: flow.RoundFull, Head: "c0", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 2, Kind: flow.RoundDelta, Head: "c0b", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 3, Kind: flow.RoundDelta, Head: "c0c", Verdict: flow.VerdictChanges, Majors: 1},
			{N: 4, Kind: flow.RoundRetro, Head: c1, Verdict: flow.VerdictFix, Majors: 1},
		}
		s.Tasks[0].Retros = []flow.Retro{{Attempt: 1, Round: 4, Cause: "patch-shaped-fix", At: w.now}}
	})

	st := w.state()
	if got := flow.Ladder(flow.In{Task: st.Tasks[0], Settings: st.Settings, Now: w.now}); got != flow.Fix {
		t.Fatalf("Ladder on retro FIX = %q, want %q (L4)", got, flow.Fix)
	}
	payload, _ := json.Marshal(map[string]any{"head": c1, "reject": "disagree with retro"})
	err := w.eng.Report(w.workTok, "ready", payload)
	if refusalCode(err) != "nothing_to_reject" {
		t.Fatalf("ready --reject on retro FIX err = %v, want refusal nothing_to_reject", err)
	}
}
