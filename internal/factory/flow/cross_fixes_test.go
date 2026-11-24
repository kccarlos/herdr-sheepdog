package flow

import (
	"slices"
	"testing"
	"time"
)

func TestVerify4N7BeatsN14(t *testing.T) {
	s := DefaultSettings()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	a := Attempt{BaseCommit: "b0", Working: 9 * time.Hour, Acts: []Act{{Key: key(AskTellDiverged, "tip", "b0"), Result: "done", At: now.Add(-6 * time.Hour)}}}
	in := In{Task: Task{ID: "t", Attempts: []Attempt{a}}, Settings: s, Now: now, Facts: Facts{TipRel: RelDiverged, Tip: "x1", Worker: Run{State: RunWorking}}}
	r, d := FirstMatch(TaskRows, in)
	if r.ID != "N14" || !slices.Equal(d.Asked, []string{key(AskTimeRecap, 0)}) {
		t.Fatalf("working=%v row %s asks %v why %q, want N14 time recap", WorkingTime(in.Task), r.ID, d.Asked, d.Why)
	}
}

func TestVerify6WakesBeforeFinalJournal(t *testing.T) {
	in := ProjectIn{Project: Project{Started: true, Run: &RunRecord{State: PhaseIdle}, Wakes: []Wake{{Key: "last-task"}}},
		Tasks: []Task{{Done: &Merged{}}}, Settings: DefaultSettings()}
	r, d := FirstMatch(ProjectRows, in)
	if r.ID != "J12" || !slices.Equal(d.Asked, []string{AskDeliverWakes}) {
		t.Errorf("with an idle coordinator and a wake: row %s asks %v, want J12 deliver_wakes", r.ID, d.Asked)
	}
	in.Run.State = PhaseWorking
	r, _ = FirstMatch(ProjectRows, in)
	if r.ID == "J4" {
		t.Errorf("with a working coordinator and a wake: row %s, want J4 to wait", r.ID)
	}
	in.Wakes = nil
	in.Run.State = PhaseIdle
	r, d = FirstMatch(ProjectRows, in)
	if r.ID != "J4" || !slices.Equal(d.Asked, []string{AskFinalJournal}) {
		t.Errorf("with no wakes: row %s asks %v, want J4 final_journal", r.ID, d.Asked)
	}
}

func TestVerify6StaleIdleAfterDeliver(t *testing.T) {
	s := DefaultSettings()
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Minute)
	for _, role := range []string{RoleCoordinator, RoleReviewer} {
		for _, stale := range []time.Time{t0, t1} {
			rec := RunRecord{Address: "t1", Role: role, CLI: "claude", State: PhaseIdle, Status: "idle", StatusSince: t0, StatusReported: true, Turn: 5, StartedAt: t0.Add(-time.Hour)}
			rec, _, _ = StepRun(rec, RunEvent{Kind: EvDeliver, Msg: "take a turn"}, s, t1)
			t2 := t1.Add(time.Duration(s.IdleGraceSeconds) * time.Second)
			rec, _, _ = StepRun(rec, RunEvent{Kind: EvStatus, Status: "idle", Turn: 5, StatusAt: stale}, s, t2)
			rec, r, _ := StepRun(rec, RunEvent{Kind: EvTick}, s, t2)
			if rec.State != PhaseWorking || rec.Status == "idle" {
				t.Errorf("%s status at %s after send at %s: tick %s left state %s status %q, want working without idle evidence", role, stale.Format(time.TimeOnly), t1.Format(time.TimeOnly), r.ID, rec.State, rec.Status)
			}
			rec, _, _ = StepRun(rec, RunEvent{Kind: EvStatus, Status: "idle", Turn: 6, StatusAt: t2}, s, t2)
			rec, r, _ = StepRun(rec, RunEvent{Kind: EvTick}, s, t2.Add(time.Duration(s.IdleGraceSeconds)*time.Second))
			if rec.State != PhaseIdle || rec.Turn != 6 {
				t.Errorf("fresh status: tick %s left %s turn %d, want idle after turn 6", r.ID, rec.State, rec.Turn)
			}
		}
	}
}

func TestVerify8ActFailuresAfterDone(t *testing.T) {
	s := DefaultSettings()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	acts := []Act{
		{Key: AskStopRuns, Result: "failed", Class: ClassSettled, At: now.Add(-2 * time.Hour)},
		{Key: AskStopRuns, Result: "done", At: now.Add(-time.Hour)},
	}
	in := In{Task: Task{ID: "t", Parked: true, Attempts: []Attempt{{RunID: "w1", Acts: acts}}}, Settings: s, Now: now}
	a := OpenAttempt(in.Task)
	a.Acts = append(a.Acts, Act{Key: AskStopRuns, Result: "failed", Class: ClassTransient, At: now.Add(-time.Second)})
	last, _, tries, counted, settled := actFailures(a.Acts, AskStopRuns)
	r, d := FirstMatch(TaskRows, in)
	if last.Result != "failed" || tries != 1 || counted != 1 || settled || len(d.Asked) != 0 {
		t.Errorf("last=%s tries=%d counted=%d settled=%v row=%s asks=%v, want one new failure and back-off", last.Result, tries, counted, settled, r.ID, d.Asked)
	}
	for i := 1; i < s.EnsureFailures; i++ {
		a.Acts = append(a.Acts, Act{Key: AskStopRuns, Result: "failed", Class: ClassTransient, At: now})
	}
	r, d = FirstMatch(TaskRows, in)
	if !Exhausted(a.Acts, AskStopRuns, s) || r.ID != "N5" || !slices.Equal(d.Asked, []string{key(AskWake, "failed", AskStopRuns)}) {
		t.Errorf("exhausted=%v row=%s asks=%v, want failure wake", Exhausted(a.Acts, AskStopRuns, s), r.ID, d.Asked)
	}
}
