package flow

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

// TestPR2bProjectGuards pins the flow/project.go and flow/records.go requirements
// of the accepted PR 2b wake state table (docs/dev/engine-v2/design.md §10):
//   - J12 and J13 require len(CoordWakesInFlight) == 0.
//   - ProjectFinished (J4) requires len(CoordWakesInFlight) == 0 when !p.Finished,
//     while keeping its already-finished (p.Finished == true) branch and priority.
//   - J8 and J9 still fire when a batch is in flight.
//   - CoordWakeActed is removed from Project.
func TestPR2bProjectGuards(t *testing.T) {
	def := DefaultSettings()
	openTask := Task{ID: "T1", Title: "feat: open"}
	doneTask := Task{ID: "T1", Title: "feat: done", Done: &Merged{MergedCommit: "m1"}}
	idleRun := &RunRecord{ID: "r1", State: PhaseIdle, StartedAt: now.Add(-2 * time.Hour), LastTurnEnd: now, Turn: 1}
	inFlight := []Wake{{Key: "worker_failed", Task: "T1", At: now.Add(-time.Minute)}}

	t.Run("J12_waits_while_batch_in_flight", func(t *testing.T) {
		in := ProjectIn{
			Project: Project{
				Started:            true,
				Run:                idleRun,
				Wakes:              []Wake{{Key: "main_red", At: now}},
				CoordWakesInFlight: inFlight,
				CoordLastWakeAt:    now.Add(-time.Minute),
			},
			Tasks: []Task{openTask}, Settings: def, Now: now,
		}
		if r, d := FirstMatch(ProjectRows, in); r.ID != "J14" || len(d.Asked) != 0 {
			t.Fatalf("with CoordWakesInFlight non-empty: got (%s, asked=%v, why=%q), want J14 (J12 must wait)", r.ID, d.Asked, d.Why)
		}
	})

	t.Run("J13_waits_while_batch_in_flight", func(t *testing.T) {
		s := def
		s.HeartbeatMinutes = 10
		s.CoordinatorSilentMinutes = 30
		in := ProjectIn{
			Project: Project{
				Started:            true,
				Run:                idleRun,
				CoordWakesInFlight: inFlight,
				CoordWakeAt:        now.Add(-15 * time.Minute),
				CoordWakeTurn:      1,
				CoordLastWakeAt:    now.Add(-15 * time.Minute),
			},
			Tasks: []Task{openTask}, Settings: s, Now: now,
		}
		if r, d := FirstMatch(ProjectRows, in); r.ID != "J14" || len(d.Asked) != 0 {
			t.Fatalf("with CoordWakesInFlight non-empty and heartbeat due: got (%s, asked=%v, why=%q), want J14 (J13 must wait)", r.ID, d.Asked, d.Why)
		}
	})

	t.Run("J4_ProjectFinished_waits_while_batch_in_flight", func(t *testing.T) {
		p := Project{
			Started:            true,
			Run:                idleRun,
			CoordWakesInFlight: inFlight,
			CoordWakeAt:        now.Add(-time.Minute),
			CoordWakeTurn:      1,
			CoordLastWakeAt:    now.Add(-time.Minute),
		}
		if ProjectFinished(p, []Task{doneTask}) {
			t.Fatal("ProjectFinished returned true while CoordWakesInFlight is non-empty and !p.Finished")
		}
		in := ProjectIn{Project: p, Tasks: []Task{doneTask}, Settings: def, Now: now}
		if r, d := FirstMatch(ProjectRows, in); r.ID != "J14" || len(d.Asked) != 0 {
			t.Fatalf("all tasks ended with batch in flight: got (%s, asked=%v, why=%q), want J14 (J4 must wait)", r.ID, d.Asked, d.Why)
		}
	})

	t.Run("J4_ProjectFinished_keeps_already_finished_branch", func(t *testing.T) {
		p := Project{
			Started:            true,
			Finished:           true,
			CoordWakesInFlight: inFlight,
		}
		if !ProjectFinished(p, []Task{doneTask}) {
			t.Fatal("ProjectFinished returned false when p.Finished is already true")
		}
		in := ProjectIn{Project: p, Tasks: []Task{doneTask}, Settings: def, Now: now}
		if r, _ := FirstMatch(ProjectRows, in); r.ID != "J4" {
			t.Fatalf("already-finished project matched %s, want J4", r.ID)
		}
	})

	t.Run("J9_and_J8_still_match_with_batch_in_flight", func(t *testing.T) {
		j9In := ProjectIn{
			Project: Project{
				Started:            true,
				Run:                idleRun,
				Wakes:              []Wake{{Key: "main_red", At: now}},
				CoordWakesInFlight: inFlight,
				CoordWakeAt:        now.Add(-31 * time.Minute),
				CoordWakeTurn:      1,
			},
			Tasks: []Task{doneTask}, Settings: def, Now: now,
		}
		if r, d := FirstMatch(ProjectRows, j9In); r.ID != "J9" || !slices.Equal(d.Asked, []string{AskStopCoordinator}) {
			t.Fatalf("silent delivery turn with all tasks ended and batch in flight: got (%s, asked=%v), want J9 stop_coordinator", r.ID, d.Asked)
		}

		j8In := ProjectIn{
			Project: Project{
				Started:            true,
				Run:                &RunRecord{ID: "r1", State: PhaseIdle, Status: "error", Turn: 2, StartedAt: now.Add(-time.Hour)},
				Wakes:              []Wake{{Key: "worker_failed", Task: "T1", Retried: true, At: now}},
				CoordWakesInFlight: inFlight,
				CoordWakeAt:        now.Add(-31 * time.Minute),
				CoordWakeTurn:      1,
			},
			Tasks: []Task{openTask}, Settings: def, Now: now,
		}
		if r, d := FirstMatch(ProjectRows, j8In); r.ID != "J8" || !slices.Equal(d.Asked, []string{AskCoordGoOn}) {
			t.Fatalf("error turn with batch in flight: got (%s, asked=%v), want J8 coord_go_on", r.ID, d.Asked)
		}
	})

	t.Run("CoordWakeActed_removed_from_Project", func(t *testing.T) {
		if _, has := reflect.TypeFor[Project]().FieldByName("CoordWakeActed"); has {
			t.Errorf("Project.CoordWakeActed must be removed in PR 2b (design.md §10)")
		}
	})

	t.Run("RetryCoordWake_removed_from_RunEvent", func(t *testing.T) {
		if _, has := reflect.TypeFor[RunEvent]().FieldByName("RetryCoordWake"); has {
			t.Errorf("RunEvent.RetryCoordWake must be removed in PR 2b (design.md §10)")
		}
	})
}
