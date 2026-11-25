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

func TestCheckInvariantsPostVerdictJ15DoesNotMaskJ10I2Violation(t *testing.T) {
	snap := cleanSnapshot()
	snap.State.Project.RunID, snap.State.Project.Run = "", &RunRecord{Role: RoleCoordinator, State: PhaseEnded, Result: EndStuck, BlockedSince: snap.Now}
	snap.State.Project.PlanReviews = []PlanReview{{N: 1, Tasks: []string{"T1"}, Verdict: VerdictPass, At: snap.Now, Accepted: snap.Now, Run: &ReviewRun{Kind: RoundPlan, Round: 1, RunID: "plan-1"}}}
	if vs := CheckInvariants(snap); !hasViolation(vs, "I2", "project", "project should have a coordinator") {
		t.Fatalf("post-verdict J15 masked un-acted J10 I2 violation, got %+v", vs)
	}
}

func TestCheckInvariantsPostVerdictJ15AllCoordRows(t *testing.T) {
	for _, tc := range []struct {
		name, row string
		wantI2    bool
		setup     func(*InvariantSnapshot)
	}{
		{"J3", "J3", false, func(s *InvariantSnapshot) {
			s.State.Project.RunID, s.State.Project.Run = "", nil
			s.State.Project.Alerts = map[string]Alert{AlertDisk: {Kind: AlertDisk, Cause: "low disk", At: s.Now}}
		}},
		{"J4", "J4", false, func(s *InvariantSnapshot) {
			s.State.Project.RunID, s.State.Project.Run = "", nil
			s.State.Tasks[0].Done = &Merged{MergedCommit: "m1"}
			s.State.Tasks[0].Attempts[0].RunID, s.State.Tasks[0].Attempts[0].Run = "", nil
		}},
		{"J5", "J5", false, func(s *InvariantSnapshot) {
			s.State.Project.RunID, s.State.Project.Run = "", nil
			s.State.Project.Alerts = map[string]Alert{AlertAgentsDown: {Kind: AlertAgentsDown, Scope: "signed_out:claude", Cause: "signed out", At: s.Now}}
		}},
		{"J6", "J6", false, func(s *InvariantSnapshot) {
			s.State.Project.RunID, s.State.Project.Run = "", &RunRecord{Role: RoleCoordinator, State: PhaseEnded, Result: EndExited}
			s.State.Project.Acts = []Act{{Key: AskStartCoordinator, Result: "done", At: s.Now.Add(-time.Minute)}}
		}},
		{"J7", "J7", false, func(s *InvariantSnapshot) { s.State.Project.RunID, s.State.Project.Run = "", nil }},
		{"J8", "J8", false, func(s *InvariantSnapshot) { s.State.Project.Run.Status = "error" }},
		{"J9", "J9", false, func(s *InvariantSnapshot) { s.State.Project.CoordWakeAt = s.Now.Add(-time.Hour) }},
		{"J10 acted", "J7", false, func(s *InvariantSnapshot) {
			r := &RunRecord{ID: "c1", Role: RoleCoordinator, State: PhaseEnded, Result: EndStuck, BlockedSince: s.Now}
			s.State.Project.RunID, s.State.Project.Run = "", r
			s.State.Project.Acts = []Act{{Key: CoordStuckKey(r), Result: "done", At: s.Now}}
		}},
		{"J10 un-acted", "J10", true, func(s *InvariantSnapshot) {
			s.State.Project.RunID, s.State.Project.Run = "", &RunRecord{Role: RoleCoordinator, State: PhaseEnded, Result: EndStuck, BlockedSince: s.Now}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := cleanSnapshot()
			tc.setup(&snap)
			if got, _ := FirstMatch(ProjectRows[1:], ProjectIn{Project: snap.State.Project, Tasks: snap.State.Tasks, Settings: snap.State.Settings, Now: snap.Now}); got.ID != tc.row {
				t.Fatalf("setup matched %s, want %s", got.ID, tc.row)
			}
			snap.State.Project.PlanReviews = []PlanReview{{
				N: 1, Tasks: []string{"T1"}, Verdict: VerdictPass, At: snap.Now, Accepted: snap.Now,
				Run: &ReviewRun{Kind: RoundPlan, Round: 1, RunID: "plan-1"},
			}}
			if vs := CheckInvariants(snap); hasViolation(vs, "I2", "project", "project should have a coordinator") != tc.wantI2 {
				t.Fatalf("%s + post-verdict J15: want I2=%v, got %+v", tc.name, tc.wantI2, vs)
			}
		})
	}
}
