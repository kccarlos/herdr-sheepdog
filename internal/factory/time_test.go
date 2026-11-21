package factory

import (
	"context"
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
	l.eng.PassInterval = 5 * time.Minute // 10m per-pass cap so 10m steps run in a few passes
	l.edit(func(s *flow.State) {
		s.Settings.BuildHours = 1
		s.Tasks[0].Attempts[0].Ready = nil
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseStarting, StartedAt: now, LastActivity: now}
	})

	coordStep := 0
	pass := func() {
		coordStep++
		l.srv.SetScreen("coord-elsewhere", "coord "+strings.Repeat("z", coordStep))
		l.pass()
	}

	// Worker works 40m (with periodic activity so R21 quiet never fires), then goes idle for 3h:
	// idle time must not count toward the 1h build_hours limit.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	pass() // R8 -> working
	for i := 1; i <= 4; i++ {
		now = now.Add(10 * time.Minute)
		l.srv.SetScreen(l.worker, "working "+strings.Repeat("a", i))
		pass()
	}
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "done"})
	pass()
	now = now.Add(25 * time.Second)
	pass() // R20 -> idle (~40m25s working)
	if st := l.state().Tasks[0].Attempts[0].Run.State; st != flow.PhaseIdle {
		t.Fatalf("worker state = %s, want idle", st)
	}
	now = now.Add(3 * time.Hour)
	pass() // 3h idle: must not trigger N14
	if rc := l.state().Tasks[0].Recaps; len(rc) != 0 {
		t.Fatalf("idle time opened recap: %+v", rc)
	}

	// Worker resumes working and stays busy past 1h total working time (20m more):
	// N14 opens a time recap; N6 wakes coordinator once and interrupts+tells worker once.
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	pass() // R27 -> working
	for i := 1; i <= 2; i++ {
		now = now.Add(10 * time.Minute)
		l.srv.SetScreen(l.worker, "busy "+strings.Repeat("b", i))
		pass()
	}
	pass() // N6: wake once + tell stop once
	pass() // repeat pass: no second wake or tell

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
	if st.Tasks[0].Attempts[0].SizeCap != 100 || flow.OpenRecap(st.Tasks[0]) != nil || flow.WorkingTime(st.Tasks[0]) != 0 {
		t.Fatalf("after continue: cap=%d open=%+v working=%v", st.Tasks[0].Attempts[0].SizeCap, flow.OpenRecap(st.Tasks[0]), flow.WorkingTime(st.Tasks[0]))
	}
	pass()
	if flow.OpenRecap(l.state().Tasks[0]) != nil {
		t.Fatalf("reopened time recap immediately after continue: %+v", l.state().Tasks[0].Recaps)
	}

	// Another 1h of working opens a second time recap without continue.
	for i := 1; i <= 6; i++ {
		now = now.Add(10 * time.Minute)
		l.srv.SetScreen(l.worker, "second "+strings.Repeat("c", i))
		pass()
	}
	rc := l.state().Tasks[0].Recaps
	if len(rc) != 2 || rc[1].Trigger != flow.TriggerTime || slices.Contains(rc[1].Allowed, flow.ChoiceContinue) {
		t.Fatalf("second time recap = %+v, want no continue", rc)
	}
}

func setStatusAt(t *testing.T, l *landing, id, state string, at time.Time) {
	t.Helper()
	l.srv.SetStatus(id, &hc.Status{App: "claude-code", State: state})
	term, err := l.client.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ms := uint64(at.UnixMilli())
	term.StatusMS = &ms
	l.srv.Add(term)
}

// TestClockSleepIdleOnTimeAndRestartNoInflation verifies design.md 2.5:
//   - a simulated 6h sleep (wall advances 6h, monotonic advances 0) adds 0 to
//     working time and review age;
//   - because the engine stays in wall time, a run reporting done with wall
//     StatusMS after wake still goes idle after idle_grace_seconds;
//   - a daemon restart after sleep keeps the stored totals without inflating them.
func TestClockSleepIdleOnTimeAndRestartNoInflation(t *testing.T) {
	l := newLanding(t)
	wall := time.Now().UTC().Truncate(time.Second)
	var monoDelta time.Duration
	l.eng.Now = func() time.Time { return wall }
	l.eng.Since = func(time.Time) time.Duration { return monoDelta }
	l.edit(func(s *flow.State) {
		s.Settings.BuildHours = 1
		s.Settings.ReviewMinutes = 10
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseWorking, StartedAt: wall, LastActivity: wall}
	})
	setStatusAt(t, l, l.worker, "working", wall)

	// Pass 0: starts reviewer round 1.
	l.pass()
	revID := l.state().Tasks[0].Attempts[0].Review.RunID
	if revID == "" {
		t.Fatal("expected round 1 reviewer to start")
	}
	setStatusAt(t, l, revID, "working", wall)

	// Normal 15s pass: adds 15s to working time and review age.
	wall, monoDelta = wall.Add(15*time.Second), 15*time.Second
	l.pass()
	st := l.state()
	if got := flow.WorkingTime(st.Tasks[0]); got != 15*time.Second {
		t.Fatalf("WorkingTime after 15s = %v, want 15s", got)
	}
	if got := st.Tasks[0].Attempts[0].Review.Age; got != 15*time.Second {
		t.Fatalf("Review.Age after 15s = %v, want 15s", got)
	}

	// Simulated 6h machine sleep: wall advances 6h, monotonic advances 0s.
	wall, monoDelta = wall.Add(6*time.Hour), 0
	l.pass()
	st = l.state()
	if len(st.Tasks[0].Recaps) != 0 {
		t.Fatalf("sleep triggered N14 recap: %+v", st.Tasks[0].Recaps)
	}
	if st.Tasks[0].Attempts[0].Review.RunID != revID || st.Tasks[0].Attempts[0].Review.Age != 15*time.Second {
		t.Fatalf("sleep inflated/stopped reviewer: %+v", st.Tasks[0].Attempts[0].Review)
	}
	if got := flow.WorkingTime(st.Tasks[0]); got != 15*time.Second {
		t.Fatalf("WorkingTime after 6h sleep = %v, want 15s", got)
	}

	// After wake, worker finishes with wall-clock StatusMS -> must go idle on time.
	setStatusAt(t, l, l.worker, "done", wall)
	monoDelta = 0
	l.pass()
	wall, monoDelta = wall.Add(25*time.Second), 25*time.Second
	l.pass()
	if got := l.state().Tasks[0].Attempts[0].Run.State; got != flow.PhaseIdle {
		t.Fatalf("after wake + 25s done (wall StatusMS): worker state = %s, want idle", got)
	}

	// Daemon restart after sleep keeps accumulated totals without inflating any age.
	eng2 := New(l.paths, l.client)
	eng2.Now = func() time.Time { return wall }
	eng2.passProject(context.Background(), l.id, false)
	st = l.state()
	if len(st.Tasks[0].Recaps) != 0 {
		t.Fatalf("daemon restart after sleep triggered N14 recap: %+v", st.Tasks[0].Recaps)
	}
	if st.Tasks[0].Attempts[0].Review.RunID != revID || st.Tasks[0].Attempts[0].Review.Retries != 0 || st.Tasks[0].Attempts[0].Review.Age != 40*time.Second {
		t.Fatalf("daemon restart after sleep inflated/stopped reviewer: %+v", st.Tasks[0].Attempts[0].Review)
	}
	if got := flow.WorkingTime(st.Tasks[0]); got != 40*time.Second {
		t.Fatalf("WorkingTime after restart = %v, want 40s", got)
	}
}

// TestClockRestartAfterSleepNoInflation verifies that restarting the daemon
// right after a 6h sleep (before any post-wake pass on the old Engine) does not
// inflate stored working time or review age.
func TestClockRestartAfterSleepNoInflation(t *testing.T) {
	l := newLanding(t)
	wall := time.Now().UTC().Truncate(time.Second)
	l.eng.Now = func() time.Time { return wall }
	l.edit(func(s *flow.State) {
		s.Settings.BuildHours = 1
		s.Settings.ReviewMinutes = 10
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseWorking, StartedAt: wall, LastActivity: wall}
	})
	setStatusAt(t, l, l.worker, "working", wall)
	l.pass()
	revID := l.state().Tasks[0].Attempts[0].Review.RunID
	setStatusAt(t, l, revID, "working", wall)
	wall = wall.Add(15 * time.Second)
	l.pass()

	// Sleep 6h, then restart daemon immediately.
	wall = wall.Add(6 * time.Hour)
	eng2 := New(l.paths, l.client)
	eng2.Now = func() time.Time { return wall }
	eng2.passProject(context.Background(), l.id, false)
	wall = wall.Add(15 * time.Second)
	eng2.passProject(context.Background(), l.id, false)

	st := l.state()
	if len(st.Tasks[0].Recaps) != 0 {
		t.Fatalf("daemon restart after sleep triggered N14 recap: %+v", st.Tasks[0].Recaps)
	}
	if st.Tasks[0].Attempts[0].Review.RunID != revID || st.Tasks[0].Attempts[0].Review.Retries != 0 || st.Tasks[0].Attempts[0].Review.Age != 30*time.Second {
		t.Fatalf("daemon restart after sleep inflated/stopped reviewer: %+v", st.Tasks[0].Attempts[0].Review)
	}
	if got := flow.WorkingTime(st.Tasks[0]); got != 30*time.Second {
		t.Fatalf("WorkingTime after restart + 15s = %v, want 30s", got)
	}
}

// TestClockSlowPassCappedNotFrozen verifies that a slow pass (90s, default 15s
// tick -> 30s cap) adds at most the cap (30s) rather than 90s or 0s, and never
// freezes the clock for N14 or review_minutes.
func TestClockSlowPassCappedNotFrozen(t *testing.T) {
	l := newLanding(t)
	wall := time.Now().UTC().Truncate(time.Second)
	l.eng.Now = func() time.Time { return wall }
	l.edit(func(s *flow.State) {
		s.Settings.BuildHours = 1
		s.Settings.ReviewMinutes = 10
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseWorking, StartedAt: wall, LastActivity: wall}
	})
	setStatusAt(t, l, l.worker, "working", wall)
	l.pass()
	revID := l.state().Tasks[0].Attempts[0].Review.RunID
	setStatusAt(t, l, revID, "working", wall)

	// Slow pass 1 (90s): capped at 2*15s = 30s (not 0s and not 90s).
	wall = wall.Add(90 * time.Second)
	l.srv.SetScreen(l.worker, "slow pass 1")
	l.srv.SetScreen(revID, "slow rev 1")
	l.pass()
	st := l.state()
	if got := flow.WorkingTime(st.Tasks[0]); got != 30*time.Second {
		t.Fatalf("WorkingTime after 90s slow pass = %v, want 30s", got)
	}
	if got := st.Tasks[0].Attempts[0].Review.Age; got != 30*time.Second {
		t.Fatalf("Review.Age after 90s slow pass = %v, want 30s", got)
	}

	// Slow pass 2 (90s): adds another 30s -> 1m total (clock is not frozen).
	wall = wall.Add(90 * time.Second)
	l.srv.SetScreen(l.worker, "slow pass 2")
	l.srv.SetScreen(revID, "slow rev 2")
	l.pass()
	st = l.state()
	if got := flow.WorkingTime(st.Tasks[0]); got != time.Minute {
		t.Fatalf("WorkingTime after two 90s slow passes = %v, want 1m", got)
	}
	if got := st.Tasks[0].Attempts[0].Review.Age; got != time.Minute {
		t.Fatalf("Review.Age after two 90s slow passes = %v, want 1m", got)
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
	l.eng.PassInterval = 5 * time.Minute // 10m cap so 9m + 1m steps run in two passes
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

	l.pass() // 4.8 starts retry 1 with fresh Age == 0
	rev = l.state().Tasks[0].Attempts[0].Review
	if rev.RunID == "" || rev.Retries != 1 || rev.Age != 0 {
		t.Fatalf("after review_minutes stop: review=%+v, want retry 1 with Age 0", rev)
	}
}

// TestS2AndStuckMinutesGone verifies S2 is gone and neither stuck_minutes nor
// background_job_minutes appears in DefaultSettings JSON.
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
	if strings.Contains(string(raw), "background_job_minutes") {
		t.Fatalf("background_job_minutes still in DefaultSettings JSON: %s", raw)
	}
}

// TestOldStateWithStuckMinutesAndNoWorkingSince verifies that an old state.json
// carrying stuck_minutes (and lacking build_hours/review_minutes) loads with
// defaults, and a pre-upgrade working worker record counts from the first pass.
func TestOldStateWithStuckMinutesAndNoWorkingSince(t *testing.T) {
	l := newLanding(t)
	now := time.Now().UTC().Truncate(time.Second)
	l.eng.Now = func() time.Time { return now }
	l.eng.PassInterval = 5 * time.Minute
	raw := `{
		"seq": 5,
		"project": {"id": "` + l.id + `", "folder": ` + mustJSON(t, l.repo) + `, "goal": "Goal", "started": true, "run_id": "coord-elsewhere"},
		"settings": {"review_rounds": 2, "stuck_minutes": 30},
		"tasks": [{
			"id": "T1", "title": "feat: x", "estimate_lines": 50,
			"attempts": [{
				"base_commit": ` + mustJSON(t, l.state().Tasks[0].Attempts[0].BaseCommit) + `,
				"branch": ` + mustJSON(t, l.branch) + `,
				"worktree": ` + mustJSON(t, l.wt) + `,
				"pr": 7, "size_cap": 100,
				"run_id": ` + mustJSON(t, l.worker) + `,
				"run": {"Address": ` + mustJSON(t, l.worker) + `, "Role": "worker", "CLI": "claude", "State": "working", "StartedAt": ` + mustJSON(t, now) + `, "LastActivity": ` + mustJSON(t, now) + `},
				"ready": [], "rounds": [], "accepts": [], "acts": []
			}]
		}]
	}`
	if err := writeAtomic(l.paths.State(l.id), []byte(raw)); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(l.paths.State(l.id))
	if err != nil {
		t.Fatalf("LoadState old state with stuck_minutes: %v", err)
	}
	if loaded.Settings.BuildHours != 4 || loaded.Settings.ReviewMinutes != 45 {
		t.Fatalf("old state defaults: BuildHours=%d ReviewMinutes=%d, want 4, 45", loaded.Settings.BuildHours, loaded.Settings.ReviewMinutes)
	}
	if err := loaded.Settings.Validate(); err != nil {
		t.Fatalf("old state settings Validate: %v", err)
	}
	l.edit(func(s *flow.State) { s.Settings.BuildHours = 1 })
	l.srv.SetStatus(l.worker, &hc.Status{App: "claude-code", State: "working"})
	l.pass()
	for i := 1; i <= 6; i++ {
		now = now.Add(10 * time.Minute)
		l.srv.SetScreen(l.worker, "old-state "+strings.Repeat("x", i))
		l.pass()
	}
	if rc := l.state().Tasks[0].Recaps; len(rc) != 1 || rc[0].Trigger != flow.TriggerTime {
		t.Fatalf("old-state working worker after 1h: recaps = %+v, want 1 time recap", rc)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
