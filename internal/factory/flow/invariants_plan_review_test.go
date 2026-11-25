package flow

import (
	"testing"
	"time"
)

func TestCheckInvariantsPostVerdictJ15DoesNotMaskI2(t *testing.T) {
	pr := []PlanReview{{
		N: 1, Tasks: []string{"T1"}, Verdict: VerdictPass,
		Run: &ReviewRun{Kind: RoundPlan, Round: 1, RunID: "plan-1"},
	}}
	for _, tc := range []struct {
		name  string
		setup func(*InvariantSnapshot)
	}{
		{"J7 pending start", func(snap *InvariantSnapshot) {
			snap.State.Project.RunID, snap.State.Project.Run = "", nil
		}},
		{"J6 coordinator gap", func(snap *InvariantSnapshot) {
			snap.State.Project.RunID = ""
			snap.State.Project.Run = &RunRecord{Role: RoleCoordinator, State: PhaseEnded, Result: EndExited}
			snap.State.Project.Acts = []Act{{Key: AskStartCoordinator, Result: "done", At: snap.Now.Add(-time.Minute)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := cleanSnapshot()
			pr[0].At, pr[0].Accepted = snap.Now, snap.Now
			snap.State.Project.PlanReviews = pr
			tc.setup(&snap)
			if vs := CheckInvariants(snap); len(vs) != 0 {
				t.Fatalf("post-verdict J15 without live coordinator fired false invariant: %+v", vs)
			}
		})
	}
}
