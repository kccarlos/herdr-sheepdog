package factory

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func TestPlanReviewJ15PostReportStopAlongsideDialogStuckCoordNoFalseI2(t *testing.T) {
	plan := `{"tasks":[{"id":"T1","title":"feat: one","estimate_lines":40,` + testPlanJSON + `}]}`
	w, _, prTok, code, _ := launchPlanReview(t, plan)
	perm := "permission"
	w.srv.SetStatus("coord1", &hc.Status{App: "claude-code", State: "working"})
	w.pass() // R27 -> PhaseWorking
	w.srv.SetStatus("coord1", &hc.Status{App: "claude-code", State: "blocked", Kind: &perm, Msg: "Allow tool?"})
	w.now = w.now.Add(5 * time.Second)
	w.pass() // R15 -> PhaseBlocked
	if err := w.eng.Report(prTok, "plan-review", planReviewJSON(t, map[string]any{"verdict": flow.VerdictPass, "code": code, "findings": ""})); err != nil {
		t.Fatalf("plan-review pass: %v", err)
	}
	w.now = w.now.Add(10 * time.Minute)
	w.pass() // R29 ends coord1 (EndStuck); J15 stops plan reviewer; checkInvariantsLocked runs
	st := w.state()
	if len(st.Project.Invariants) != 0 {
		t.Fatalf("J15 post-report stop alongside R29 coord recorded false I2: %+v", st.Project.Invariants)
	}
	if i2 := logRules(t, w.paths.Log(w.id), "I2"); len(i2) != 0 {
		t.Fatalf("J15 post-report stop alongside R29 coord logged false I2: %+v", i2)
	}
}
