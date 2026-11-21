package factory

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

// TestN14BuildHoursRecapResetAndIdleExcluded verifies N14 end to end:
//   - time spent idle does not count toward build_hours;
//   - a worker staying in working (never ending its turn) past build_hours
//     opens a time recap once, wakes the coordinator once, and urgently
//     interrupts+tells the worker once to stop;
//   - a recap decision (continue) resets the working-time count without
//     changing size_cap, and another build_hours opens a second recap without continue.
func TestN14BuildHoursRecapResetAndIdleExcluded(t *testing.T) {
	l := newLanding(t).withTokens()
	now := time.Now().UTC().Truncate(time.Second)
	l.eng.Now = func() time.Time { return now }
	l.edit(func(s *flow.State) {
		s.Settings.BuildHours = 1
		s.Tasks[0].Attempts[0].Ready = nil
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseStarting, StartedAt: now, LastActivity: now}
	})

	// Worker works 40m (with periodic activity so R21 quiet never fires), then goes idle for 3h:
	// idle time must not count toward the 1h build_hours limit.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	l.pass() // R8 -> working at 10:00
	for i := 1; i <= 4; i++ {
		now = now.Add(10 * time.Minute)
		l.srv.SetScreen(l.worker, "working "+strings.Repeat("a", i))
		l.pass()
	}
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "done"})
	l.pass()
	now = now.Add(25 * time.Second)
	l.pass() // R20 -> idle (~40m25s working)
	if st := l.state().Tasks[0].Attempts[0].Run.State; st != flow.PhaseIdle {
		t.Fatalf("worker state = %s, want idle", st)
	}
	now = now.Add(3 * time.Hour)
	l.pass() // 3h idle: must not trigger N14
	if rc := l.state().Tasks[0].Recaps; len(rc) != 0 {
		t.Fatalf("idle time opened recap: %+v", rc)
	}

	// Worker resumes working and stays busy past 1h total working time (20m more):
	// N14 opens a time recap; N6 wakes coordinator once and interrupts+tells worker once.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	l.pass() // R27 -> working
	for i := 1; i <= 2; i++ {
		now = now.Add(10 * time.Minute)
		l.srv.SetScreen(l.worker, "busy "+strings.Repeat("b", i))
		l.pass()
	}
	l.pass() // N6: wake once + tell stop once
	l.pass() // repeat pass: no second wake or tell

	st := l.state()
	if len(st.Tasks[0].Recaps) != 1 || st.Tasks[0].Recaps[0].Trigger != flow.TriggerTime ||
		!slices.Equal(st.Tasks[0].Recaps[0].Allowed, []string{"continue", "split", "new", "drop"}) {
		t.Fatalf("recaps = %+v, want 1 time recap with continue", st.Tasks[0].Recaps)
	}
	if w := st.Project.Wakes; len(w) != 1 || w[0].Key != "recap@1" || !strings.Contains(w[0].Facts, "recap trigger time") {
		t.Fatalf("wakes = %+v, want 1 time recap wake", w)
	}
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 1 || !strings.Contains(sends[0], "working time limit") {
		t.Fatalf("worker sends = %q, want 1 stop at working time limit", sends)
	}

	// Coordinator decides continue: resets working time count without changing SizeCap.
	l.report("tok-coord", "recap decide", `{"task":"T1","choice":"continue","why":"keep going"}`, "")
	st = l.state()
	if st.Tasks[0].Attempts[0].SizeCap != 100 || flow.OpenRecap(st.Tasks[0]) != nil || flow.WorkingTime(st.Tasks[0], now) != 0 {
		t.Fatalf("after continue: cap=%d open=%+v working=%v", st.Tasks[0].Attempts[0].SizeCap, flow.OpenRecap(st.Tasks[0]), flow.WorkingTime(st.Tasks[0], now))
	}
	l.pass()
	if flow.OpenRecap(l.state().Tasks[0]) != nil {
		t.Fatalf("reopened time recap immediately after continue: %+v", l.state().Tasks[0].Recaps)
	}

	// Another 1h of working opens a second time recap without continue.
	for i := 1; i <= 6; i++ {
		now = now.Add(10 * time.Minute)
		l.srv.SetScreen(l.worker, "second "+strings.Repeat("c", i))
		l.pass()
	}
	rc := l.state().Tasks[0].Recaps
	if len(rc) != 2 || rc[1].Trigger != flow.TriggerTime || slices.Contains(rc[1].Allowed, flow.ChoiceContinue) {
		t.Fatalf("second time recap = %+v, want no continue", rc)
	}
}

// TestClockSleepGapNotCounted verifies design.md 2.5: a gap over 60 s between
// ticks (the Mac slept) is subtracted from the engine clock so it counts
// neither as worker working time (N14) nor as reviewer age (N29).
func TestClockSleepGapNotCounted(t *testing.T) {
	l := newLanding(t)
	raw := time.Now().UTC().Truncate(time.Second)
	l.eng.Now = func() time.Time { return raw }
	l.edit(func(s *flow.State) {
		s.Settings.BuildHours = 1
		s.Settings.ReviewMinutes = 10
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseWorking, StartedAt: raw, WorkingSince: raw, LastActivity: raw}
	})
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})

	// Tick 0: starts reviewer round 1 at raw=12:00:00.
	l.eng.tickClock()
	l.pass()
	revID := l.state().Tasks[0].Attempts[0].Review.RunID
	if revID == "" {
		t.Fatal("expected round 1 reviewer to start")
	}
	l.srv.SetStatus(revID, &hc.Status{App: "claude-code", State: "working"})

	// Normal 30s tick: counted (30s <= 60s).
	raw = raw.Add(30 * time.Second)
	l.eng.tickClock()
	l.pass()

	// Mac sleeps for 6 hours (gap > 60s): subtracted from the clock!
	raw = raw.Add(6 * time.Hour)
	l.eng.tickClock()
	l.pass()

	st := l.state()
	if len(st.Tasks[0].Recaps) != 0 {
		t.Fatalf("sleep gap triggered N14 recap: %+v", st.Tasks[0].Recaps)
	}
	if st.Tasks[0].Attempts[0].Review.RunID != revID || st.Tasks[0].Attempts[0].Review.Retries != 0 {
		t.Fatalf("sleep gap stopped reviewer: %+v", st.Tasks[0].Attempts[0].Review)
	}
	if wt := flow.WorkingTime(st.Tasks[0], l.eng.now()); wt != 30*time.Second {
		t.Fatalf("WorkingTime after 30s + 6h sleep = %v, want 30s", wt)
	}
}

// TestN29IdleReviewerNudgeThenStopAndRetry verifies N29: an idle reviewer with
// no <report> tag gets one nudge asking for its report tag, then is stopped if
// it goes idle again with no tag, and 4.8 starts the next reviewer try.
func TestN29IdleReviewerNudgeThenStopAndRetry(t *testing.T) {
	l := newLanding(t)
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	l.edit(func(s *flow.State) { s.Tasks[0].Attempts[0].Rounds = nil })
	l.pass() // N29 starts round 1 reviewer (try 0, opus)
	rev0 := l.state().Tasks[0].Attempts[0].Review.RunID
	l.srv.SetScreen(rev0, "❯ prompt\n\n⏺ Reviewed everything, forgot the tag.\n")
	l.srv.SetStatus(rev0, &hc.Status{App: "claude-code", State: "done"})
	now = now.Add(5 * time.Second)
	l.pass() // R9 -> working
	now = now.Add(25 * time.Second)
	l.pass() // R20 -> idle (ReportNotYet) -> N29 nudges once (R24 -> working)

	sends := sendsTo(t, l.srv.Requests(), rev0)
	if len(sends) != 1 || !strings.Contains(sends[0], "<report>") {
		t.Fatalf("nudge sends = %q, want 1 nudge asking for <report> tag", sends)
	}
	if l.state().Tasks[0].Attempts[0].Review.Run.State != flow.PhaseWorking {
		t.Fatalf("after nudge state = %s, want working", l.state().Tasks[0].Attempts[0].Review.Run.State)
	}

	// Reviewer stays silent / goes idle again with no report tag -> N29 stops it.
	now = now.Add(5 * time.Second)
	l.pass() // R17 sees done
	now = now.Add(25 * time.Second)
	l.pass() // R20 -> idle -> N29 stops rev0 (EndStuck)
	rev := l.state().Tasks[0].Attempts[0].Review
	if rev.RunID != "" || rev.Run.State != flow.PhaseEnded || rev.Run.Result != flow.EndStuck {
		t.Fatalf("after second idle: review=%+v run=%+v, want stopped as stuck", rev, rev.Run)
	}

	// Next pass: 4.8 retry path starts try 1 (retries=1, next model).
	l.pass()
	rev = l.state().Tasks[0].Attempts[0].Review
	if rev.RunID == "" || rev.Retries != 1 || rev.Run.State != flow.PhaseStarting {
		t.Fatalf("after 4.8 retry: review=%+v run=%+v, want retry 1 starting", rev, rev.Run)
	}
}

// TestN29ReviewMinutesStopsAndRetries verifies N29: a reviewer run older than
// review_minutes is stopped even while working, and 4.8 starts the next try.
func TestN29ReviewMinutesStopsAndRetries(t *testing.T) {
	l := newLanding(t)
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	l.edit(func(s *flow.State) {
		s.Settings.ReviewMinutes = 10
		s.Tasks[0].Attempts[0].Rounds = nil
	})
	l.pass() // N29 starts try 0
	rev0 := l.state().Tasks[0].Attempts[0].Review.RunID
	l.srv.SetStatus(rev0, &hc.Status{App: "claude-code", State: "working"})
	now = now.Add(9 * time.Minute)
	l.srv.SetScreen(rev0, "still reviewing")
	l.pass() // under review_minutes: still running
	if l.state().Tasks[0].Attempts[0].Review.RunID != rev0 {
		t.Fatal("reviewer stopped before review_minutes")
	}

	now = now.Add(time.Minute) // 10m == review_minutes
	l.srv.SetScreen(rev0, "still reviewing 2")
	l.pass() // N29 stops rev0
	rev := l.state().Tasks[0].Attempts[0].Review
	if rev.RunID != "" || rev.Run.State != flow.PhaseEnded || rev.Run.Result != flow.EndStuck {
		t.Fatalf("at review_minutes: review=%+v run=%+v, want stopped as stuck", rev, rev.Run)
	}

	l.pass() // 4.8 starts retry 1
	rev = l.state().Tasks[0].Attempts[0].Review
	if rev.RunID == "" || rev.Retries != 1 {
		t.Fatalf("after review_minutes stop: review=%+v, want retry 1", rev)
	}
}

// TestS2AndStuckMinutesGone verifies S2 is gone: no row in TaskRows and no
// stuck_minutes field in DefaultSettings JSON.
func TestS2AndStuckMinutesGone(t *testing.T) {
	for _, r := range flow.TaskRows {
		if r.ID == "S2" {
			t.Fatal("S2 row still in flow.TaskRows")
		}
	}
	raw, err := json.Marshal(flow.DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "stuck_minutes") {
		t.Fatalf("stuck_minutes still in DefaultSettings JSON: %s", raw)
	}
}
