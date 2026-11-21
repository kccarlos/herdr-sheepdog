package flow

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestR18NeedsStatusNewerThanHeldSend(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	s := DefaultSettings()
	grace := time.Duration(s.IdleGraceSeconds) * time.Second
	rec := RunRecord{State: PhaseWorking, Role: RoleWorker, Status: "idle", StatusSince: now.Add(-grace), Held: []string{"first", "second"}}
	rec, row, _ := StepRun(rec, RunEvent{Kind: EvTick}, s, now)
	if row.ID != "R18" || len(rec.Held) != 1 {
		t.Fatalf("first tick = %s, held %v, want R18 and one left", row.ID, rec.Held)
	}
	for _, at := range []time.Time{now.Add(-grace), now, {}} {
		rec, _, _ = StepRun(rec, RunEvent{Kind: EvStatus, Status: "idle", StatusAt: at}, s, now.Add(grace))
		var d Decision
		rec, row, d = StepRun(rec, RunEvent{Kind: EvTick, ScreenHash: "same"}, s, now.Add(2*grace))
		if slices.Contains(d.Asked, RunActSendHeld) || len(rec.Held) != 1 {
			t.Fatalf("without fresh status (at %s), tick = %s and held %v, want second message still held", at, row.ID, rec.Held)
		}
	}
	if _, row, _ = StepRun(rec, RunEvent{Kind: EvTick, ScreenHash: "same"}, s, now.Add(time.Duration(s.QuietMinutes)*time.Minute)); row.ID != "R21" {
		t.Fatalf("no fresh status through quiet bound: row = %s, want R21", row.ID)
	}
	rec, _, _ = StepRun(rec, RunEvent{Kind: EvStatus, Status: "idle", StatusAt: now.Add(time.Second)}, s, now.Add(grace))
	if _, row, _ := StepRun(rec, RunEvent{Kind: EvTick}, s, now.Add(2*grace)); row.ID != "R18" {
		t.Fatalf("after fresh status and grace, row = %s, want R18", row.ID)
	}
}

func TestQuietRowsRequireScreenEvidence(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	s := DefaultSettings()
	rec := RunRecord{State: PhaseWorking, LastActivity: now.Add(-time.Hour)}
	if _, row, _ := StepRun(rec, RunEvent{Kind: EvTick}, s, now); row.ID != "R23" {
		t.Errorf("unknown screen at quiet deadline matched %s, want R23", row.ID)
	}
	rec.InterruptedAt = now.Add(-time.Minute * 5)
	rec.ScreenHash = "same"
	if _, row, _ := StepRun(rec, RunEvent{Kind: EvTick}, s, now); row.ID != "R23" {
		t.Errorf("unknown screen at interrupt deadline matched %s, want R23", row.ID)
	}
	if _, row, _ := StepRun(rec, RunEvent{Kind: EvTick, ScreenHash: "same"}, s, now); row.ID != "R22" {
		t.Errorf("unchanged screen at interrupt deadline matched %s, want R22", row.ID)
	}
}

func TestJ6AllowedTimeUsesLocalWallClock(t *testing.T) {
	prev := time.Local
	time.Local = time.FixedZone("test local time", -8*3600)
	t.Cleanup(func() { time.Local = prev })
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	in := ProjectIn{Project: Project{Started: true, Acts: []Act{
		{Key: AskStartCoordinator, At: now},
		{Key: AskStopCoordinator, Result: "failed", At: now, Err: "temporary stop failure"},
	}},
		Settings: DefaultSettings(), Now: now}
	_, d := FirstMatch(ProjectRows, in)
	if !strings.Contains(d.Why, "2026-11-21T04:10:00-08:00") || !strings.Contains(d.Why, "temporary stop failure") {
		t.Fatalf("J6 why = %q, want local time 2026-11-21T04:10:00-08:00 and stop failure", d.Why)
	}
}

func TestN42RetriesLostMergeOutcomeAfterBackoff(t *testing.T) {
	in := landing()
	a := a0(&in)
	a.Accepts = []Accept{{Head: "c1", By: "bar"}}
	a.Acts = []Act{
		{Key: "record_accept@c1", Result: "done", At: in.Now},
		{Key: "merge@c1", Result: "asked", At: in.Now},
	}
	backoff := EnsureBackoff(in.Settings, 1)
	in.Now = in.Now.Add(backoff - time.Second)
	if row, d := FirstMatch(TaskRows, in); row.ID != "N42" || len(d.Asked) != 0 {
		t.Fatalf("before back-off = %s %v, want N42 hold", row.ID, d.Asked)
	}
	in.Now = in.Now.Add(time.Second)
	if row, d := FirstMatch(TaskRows, in); row.ID != "N42" || !slices.Equal(d.Asked, []string{"merge@c1"}) {
		t.Fatalf("at back-off = %s %v, want N42 merge for still-open PR", row.ID, d.Asked)
	}
}

func TestN23ReportsEnsureRunBackoff(t *testing.T) {
	in := with(noRounds, func(in *In) {
		in.Facts.Worker.State = RunNone
		a := a0(in)
		a.Ready = nil
		a.Run = &RunRecord{State: PhaseEnded, Result: EndExited, EndedAt: in.Now}
	})
	if got := EnsureRun(in, RoleWorker); len(got) != 0 {
		t.Fatalf("EnsureRun during back-off = %v, want no start", got)
	}
	if r, d := FirstMatch(TaskRows, in); r.ID != "N23" || !d.Hold || d.Why != "waiting for worker restart back-off" || len(d.Asked) != 0 {
		t.Errorf("N23 during back-off = %s %+v, want a restart back-off hold", r.ID, d)
	}
	in.Now = in.Now.Add(time.Duration(in.Settings.EnsureBackoffMinutes) * time.Minute)
	if r, d := FirstMatch(TaskRows, in); r.ID != "N23" || d.Hold || !slices.Equal(d.Asked, []string{"start_worker@handoff@1"}) {
		t.Errorf("N23 at restart deadline = %s %+v, want a worker start", r.ID, d)
	}
}

func TestN36UsesPendingSpellForPRHead(t *testing.T) {
	in := with(pending, func(in *In) {
		in.Now = in.Now.Add(time.Duration(in.Settings.CIStalledMinutes) * time.Minute)
	})
	wantWake := []string{"wake_coordinator@ci_stalled@c1"}
	for _, tc := range []struct {
		name  string
		spell *PendingChecks
		want  []string
	}{
		{"missing", nil, nil},
		{"different head", &PendingChecks{Head: "c0", Since: now}, nil},
		{"zero time", &PendingChecks{Head: "c1"}, nil},
		{"current head", &PendingChecks{Head: "c1", Since: now}, wantWake},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a0(&in).PendingChecks = tc.spell
			if r, d := FirstMatch(TaskRows, in); r.ID != "N36" || !slices.Equal(d.Asked, tc.want) {
				t.Errorf("pending spell %+v: %s %v, want N36 %v", tc.spell, r.ID, d.Asked, tc.want)
			}
		})
	}
}
