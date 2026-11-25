package factory

import (
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func TestPlanReviewJ15StopFailureWithoutCoordNoFalseI2(t *testing.T) {
	plan := `{"tasks":[{"id":"T1","title":"feat: one","estimate_lines":40,` + testPlanJSON + `}]}`
	w, _, prTok, code, _ := launchPlanReview(t, plan)
	w.srv.Finish("coord1", "exited")
	w.edit(func(s *flow.State) {
		s.Project.Acts = append(s.Project.Acts, flow.Act{Key: flow.AskStartCoordinator, Result: "done", At: w.now})
	})
	w.pass()
	if err := w.eng.Report(prTok, "plan-review", planReviewJSON(t, map[string]any{"verdict": flow.VerdictPass, "code": code, "findings": ""})); err != nil {
		t.Fatalf("plan-review pass: %v", err)
	}
	var failKill atomic.Bool
	failKill.Store(true)
	w.eng.HC = hc.NewClient(methodFailProxy(t, w.srv.Path, "terminal.kill", &failKill))
	w.pass()
	st := w.state()
	if len(st.Project.Invariants) != 0 {
		t.Fatalf("J15 stop failure without coordinator recorded false invariant: %+v", st.Project.Invariants)
	}
	if i2 := logRules(t, w.paths.Log(w.id), "I2"); len(i2) != 0 {
		t.Fatalf("J15 stop failure without coordinator logged false I2: %+v", i2)
	}
	if slices.ContainsFunc(st.Project.Wakes, func(wk flow.Wake) bool { return strings.HasPrefix(wk.Key, "invariant@I2") }) {
		t.Fatalf("queued spurious I2 wake: %+v", st.Project.Wakes)
	}
}

func TestPlanReviewDropLastOpenTaskAcceptsBeforeFinishAndReopen(t *testing.T) {
	plan := `{"tasks":[{"id":"T1","title":"feat: one","estimate_lines":40,` + testPlanJSON + `}]}`
	w, coordTok, prTok, code, _ := launchPlanReview(t, plan)
	w.edit(func(s *flow.State) {
		s.Tasks = append([]flow.Task{{ID: "T0", Title: "feat: base", EstimateLines: 20, Done: &flow.Merged{MergedCommit: "m0"}}}, s.Tasks...)
	})
	if err := w.eng.Report(prTok, "plan-review", planReviewJSON(t, map[string]any{"verdict": flow.VerdictChanges, "code": code, "findings": "delete: T1 already covered by T0\n"})); err != nil {
		t.Fatalf("plan-review changes: %v", err)
	}
	w.pass() // J15 stops plan reviewer
	w.pass() // J12 delivers wake to coord1
	if err := w.eng.Report(coordTok, "task", json.RawMessage(`{"action":"drop","task":"T1","why":"already covered by T0"}`)); err != nil {
		t.Fatalf("task drop T1: %v", err)
	}
	for range 2 {
		w.srv.SetStatus("coord1", &hc.Status{App: "claude-code", State: "working"})
		w.pass()
		setTerminalStatusAt(t, w.srv, w.client, "coord1", "idle", w.now.Add(30*time.Second))
		w.advanceMinute()
		w.pass()
	}
	w.pass() // J4 finishes project
	st := w.state()
	if !st.Project.Finished {
		t.Fatalf("expected project finished, got %+v", st.Project)
	}
	pr := st.Project.PlanReviews[0]
	if pr.Accepted.IsZero() || st.Tasks[1].Unreviewed {
		t.Errorf("finished project left PlanReviews[0] unaccepted or T1 unreviewed: accepted=%v unreviewed=%v", pr.Accepted, st.Tasks[1].Unreviewed)
	}
	accepts := slices.DeleteFunc(logRules(t, w.paths.Log(w.id), "J16"), func(l LogLine) bool { return l.Decision != "plan accepted" })
	if len(accepts) != 1 {
		t.Errorf("expected 1 J16 plan accepted log line before finish, got %d", len(accepts))
	}
	// Reopening with a new goal must not leak Goal 1's accept_plan into Goal 2.
	w.now = w.now.Add(time.Hour)
	if err := w.eng.Start(w.repo, "Goal 2", 0); err != nil {
		t.Fatalf("reopen start: %v", err)
	}
	w.pass() // J7 launches coordinator for Goal 2
	w.pass() // must not fire J16 accept_plan on Goal 1's PlanReviews[0]
	if after := w.state().Project.PlanReviews[0].Accepted; !after.Equal(pr.Accepted) {
		t.Fatalf("reopening leaked accept_plan into Goal 2: accepted changed %v -> %v", pr.Accepted, after)
	}
}

func TestProjectsViewHidesOpenPlanFindingsWhenInactive(t *testing.T) {
	openPR := []flow.PlanReview{{
		N: 1, Tasks: []string{"T1"}, Verdict: flow.VerdictChanges,
		Findings: []flow.PlanFinding{{Kind: "shrink", Task: "T1", Why: "too big"}},
	}}
	for _, tc := range []struct {
		name  string
		state flow.State
	}{
		{"stopped project", flow.State{
			Project: flow.Project{ID: "p1", Started: false, PlanReviews: openPR},
			Tasks:   []flow.Task{{ID: "T1", Title: "feat: one"}},
		}},
		{"finished project", flow.State{
			Project: flow.Project{ID: "p2", Started: true, Finished: true, PlanReviews: openPR},
			Tasks:   []flow.Task{{ID: "T1", Title: "feat: one", Dropped: &flow.Dropped{Why: "obsolete"}}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProjectsView([]flow.State{tc.state})[0].Status; got != "" {
				t.Fatalf("%s reported open plan findings status %q, want empty", tc.name, got)
			}
		})
	}
}
