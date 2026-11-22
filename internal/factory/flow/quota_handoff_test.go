package flow

import (
	"slices"
	"testing"
	"time"
)

func TestQuotaFloorDesignerGetsUrgentHandoff(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	designer := RunRecord{Role: RoleDesigner, State: PhaseWorking, CLI: "claude", StartedAt: now}
	// Designers are not launchable in this slice. Exercise their run-table
	// row directly using the event specified in design section 5.
	_, row, decision := StepRun(designer, RunEvent{Kind: "pool_floor"}, DefaultSettings(), now)
	if row.ID != "R31" || !slices.Equal(decision.Asked, []string{RunActInterrupt, RunActSend}) {
		t.Errorf("working designer at its pool floor: row = %s, acts = %v, want R31 with interrupt then send", row.ID, decision.Asked)
	}
}
