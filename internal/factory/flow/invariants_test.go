package flow

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func cleanSnapshot() InvariantSnapshot {
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	return InvariantSnapshot{
		State: State{
			Project: Project{
				ID: "proj-1", Folder: "/repo", Started: true, RunID: "coord-1",
				Run: &RunRecord{Address: "coord-1", Role: RoleCoordinator, State: PhaseIdle},
			},
			Settings: DefaultSettings(),
			Tasks: []Task{{
				ID: "T1", Title: "feat: demo",
				Attempts: []Attempt{{
					BaseCommit: "b0", Branch: "task/1", Worktree: "/wt/1", PR: 7,
					RunID: "w1", Run: &RunRecord{Address: "w1", Role: RoleWorker, State: PhaseIdle},
					Ready: []Ready{{Head: "c1"}},
				}},
			}},
		},
		Now: t0,
	}
}

func hasViolation(vs []InvariantViolation, id, subj, substr string) bool {
	return slices.ContainsFunc(vs, func(v InvariantViolation) bool {
		return v.ID == id && v.Subject == subj && strings.Contains(v.Why, substr)
	})
}

func TestCheckInvariantsCleanStates(t *testing.T) {
	t.Run("active task with live worker and reviewer on same worktree", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Tasks[0].Attempts[0].Review = &ReviewRun{
			Round: 1, Head: "c1", RunID: "rev-1",
			Run: &RunRecord{Address: "rev-1", Role: RoleReviewer, State: PhaseWorking},
		}
		if vs := CheckInvariants(snap); len(vs) != 0 {
			t.Fatalf("expected no violations (reviewer is read-only), got %+v", vs)
		}
	})

	t.Run("ended task before cleanup is being stopped", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Tasks[0].Done = &Merged{MergedCommit: "m1"}
		if vs := CheckInvariants(snap); len(vs) != 0 {
			t.Fatalf("expected no violations while cleanup pending, got %+v", vs)
		}
	})

	t.Run("parked task before stop_runs exhausts is being stopped", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Tasks[0].Parked = true
		if vs := CheckInvariants(snap); len(vs) != 0 {
			t.Fatalf("expected no violations while stop_runs pending, got %+v", vs)
		}
	})

	t.Run("missing coordinator allowed under J6 gap, J7 pending start, and J4 finished", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Project.RunID, snap.State.Project.Run = "", &RunRecord{Role: RoleCoordinator, State: PhaseEnded, Result: EndExited}
		snap.State.Project.Acts = []Act{{Key: AskStartCoordinator, Result: "done", At: snap.Now.Add(-time.Minute)}}
		if vs := CheckInvariants(snap); len(vs) != 0 {
			t.Fatalf("J6 gap should satisfy I2, got %+v", vs)
		}

		snap2 := cleanSnapshot()
		snap2.State.Project.RunID, snap2.State.Project.Run = "", nil
		if vs := CheckInvariants(snap2); len(vs) != 0 {
			t.Fatalf("J7 pending start should satisfy I2, got %+v", vs)
		}

		snap3 := cleanSnapshot()
		snap3.State.Project.RunID, snap3.State.Project.Run = "", nil
		snap3.State.Tasks[0].Done = &Merged{MergedCommit: "m1"}
		snap3.State.Tasks[0].Attempts[0].RunID, snap3.State.Tasks[0].Attempts[0].Run = "", nil
		if vs := CheckInvariants(snap3); len(vs) != 0 {
			t.Fatalf("J4 finished should not require coordinator, got %+v", vs)
		}
	})

	t.Run("skipped task is not judged on stale live reviewer", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Tasks[0].Recaps = []Recap{{Trigger: TriggerSize, Allowed: []string{"drop"}}}
		snap.State.Tasks[0].Attempts[0].Review = &ReviewRun{
			Round: 1, Head: "c1", RunID: "rev-1",
			Run: &RunRecord{Address: "rev-1", Role: RoleReviewer, State: PhaseWorking},
		}
		snap.Skipped = map[string]bool{"T1": true}
		if vs := CheckInvariants(snap); len(vs) != 0 {
			t.Fatalf("skipped task should not trigger I1 on stale reviewer, got %+v", vs)
		}
	})

	t.Run("merge asked with matching accept record", func(t *testing.T) {
		snap := cleanSnapshot()
		a := &snap.State.Tasks[0].Attempts[0]
		a.Accepts = []Accept{{Head: "c1", By: "bar", Rounds: []int{1, 2}}}
		a.Acts = []Act{{Key: AskMerge + "@c1", Result: "done", At: snap.Now}}
		if vs := CheckInvariants(snap); len(vs) != 0 {
			t.Fatalf("accepted merge should satisfy I4, got %+v", vs)
		}
	})

	t.Run("ended run with run_id still set is not live for I1", func(t *testing.T) {
		snap := cleanSnapshot()
		a := &snap.State.Tasks[0].Attempts[0]
		a.Run = &RunRecord{Address: "w1", Role: RoleWorker, State: PhaseEnded, Result: EndExited}
		if vs := CheckInvariants(snap); len(vs) != 0 {
			t.Fatalf("observed ended worker with stale RunID should not trigger I1, got %+v", vs)
		}
	})
}

func TestCheckInvariantsViolations(t *testing.T) {
	t.Run("I1 live worker on ended task after cleanup", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Tasks[0].Done = &Merged{MergedCommit: "m1"}
		snap.State.Tasks[0].Attempts[0].Acts = []Act{{Key: AskCleanup, Result: "done", At: snap.Now}}
		vs := CheckInvariants(snap)
		if !hasViolation(vs, "I1", "T1", "ended task T1 still has live worker") {
			t.Fatalf("want I1 ended worker violation, got %+v", vs)
		}
	})

	t.Run("I1 live reviewer when recap is open (N6)", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Tasks[0].Recaps = []Recap{{Trigger: TriggerSize, Allowed: []string{"drop"}}}
		snap.State.Tasks[0].Attempts[0].Review = &ReviewRun{
			Round: 1, Head: "c1", RunID: "rev-1",
			Run: &RunRecord{Address: "rev-1", Role: RoleReviewer, State: PhaseWorking},
		}
		vs := CheckInvariants(snap)
		if !hasViolation(vs, "I1", "T1", "recap is open") {
			t.Fatalf("want I1 reviewer-during-recap violation, got %+v", vs)
		}
	})

	t.Run("I1 live reviewer after ladder already passed", func(t *testing.T) {
		snap := cleanSnapshot()
		a := &snap.State.Tasks[0].Attempts[0]
		a.Rounds = []Round{
			{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictPass},
			{N: 2, Kind: RoundFull, Head: "c1", Verdict: VerdictPass},
		}
		a.Review = &ReviewRun{
			Round: 3, Head: "c1", RunID: "rev-3",
			Run: &RunRecord{Address: "rev-3", Role: RoleReviewer, State: PhaseWorking},
		}
		vs := CheckInvariants(snap)
		if !hasViolation(vs, "I1", "T1", "ladder says passed") {
			t.Fatalf("want I1 unwanted reviewer violation, got %+v", vs)
		}
	})

	t.Run("I1 live run on closed attempt", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Tasks[0].Attempts = append([]Attempt{{
			BaseCommit: "b0", Branch: "task/old", Worktree: "/wt/old", Closed: "retried",
			RunID: "w-old", Run: &RunRecord{Address: "w-old", Role: RoleWorker, State: PhaseWorking},
		}}, snap.State.Tasks[0].Attempts...)
		vs := CheckInvariants(snap)
		if !hasViolation(vs, "I1", "T1", "closed attempt 1 has live worker") {
			t.Fatalf("want I1 closed attempt violation, got %+v", vs)
		}
	})

	t.Run("I2 missing coordinator when matched row is neither J6 nor J7", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Project.RunID = ""
		snap.State.Project.Run = &RunRecord{Role: RoleCoordinator, State: PhaseEnded, Result: EndStuck, BlockedSince: snap.Now}
		vs := CheckInvariants(snap)
		if !hasViolation(vs, "I2", "project", "project should have a coordinator") {
			t.Fatalf("want I2 missing coordinator violation, got %+v", vs)
		}
	})

	t.Run("I3 two writable live runs share a worktree", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Tasks = append(snap.State.Tasks, Task{
			ID: "T2", Title: "feat: second",
			Attempts: []Attempt{{
				BaseCommit: "b0", Branch: "task/2", Worktree: "/wt/1",
				RunID: "w2", Run: &RunRecord{Address: "w2", Role: RoleWorker, State: PhaseWorking},
			}},
		})
		vs := CheckInvariants(snap)
		if !hasViolation(vs, "I3", "/wt/1", "/wt/1") {
			t.Fatalf("want I3 shared worktree violation, got %+v", vs)
		}
	})

	t.Run("I4 merge asked without accept record", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Tasks[0].Attempts[0].Acts = []Act{{Key: AskMerge + "@c1", Result: "done", At: snap.Now}}
		vs := CheckInvariants(snap)
		if !hasViolation(vs, "I4", "T1", "without an accept record") {
			t.Fatalf("want I4 unaccepted merge violation, got %+v", vs)
		}
	})

	t.Run("I5 multiple open attempts", func(t *testing.T) {
		snap := cleanSnapshot()
		snap.State.Tasks[0].Attempts = append(snap.State.Tasks[0].Attempts, Attempt{
			BaseCommit: "b0", Branch: "task/1b", Worktree: "/wt/1b", PR: 7,
		})
		vs := CheckInvariants(snap)
		if !hasViolation(vs, "I5", "T1", "has 2 open attempts") {
			t.Fatalf("want I5 multiple open attempts violation, got %+v", vs)
		}
	})
}
