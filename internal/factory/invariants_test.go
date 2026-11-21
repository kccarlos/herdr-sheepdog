package factory

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func newTestEngine(t *testing.T, paths Paths, client *hc.Client) *Engine {
	t.Helper()
	eng := New(paths, client)
	eng.onInvariant = func(vs []flow.InvariantViolation) {
		t.Helper()
		t.Fatalf("invariant broken after pass: %+v", vs)
	}
	return eng
}

// TestInvariantProductionLogOnceAndWakeCoordinator verifies design.md section 8:
// in production, a broken invariant is logged once per change as an error line
// (rule I1..I5, reason), wakes the coordinator once (held and delivered via J12),
// does not attempt repair, neither re-logs nor re-wakes across restarts for the
// same reason, and logs/wakes again if the violation changes.
func TestInvariantProductionLogOnceAndWakeCoordinator(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	// Production behavior: log and wake, never fail the test.
	l.eng.onInvariant = nil

	// Inject two invariant violations: I4 (merge asked without accept) and I5 (two open attempts).
	l.edit(func(s *flow.State) {
		a0 := &s.Tasks[0].Attempts[0]
		a0.RunID, a0.Run = "", nil
		a0.Acts = append(a0.Acts, flow.Act{Key: flow.AskMerge + "@" + l.c1, Result: "done", At: now})
		s.Tasks[0].Attempts = append(s.Tasks[0].Attempts, flow.Attempt{
			SizeCap: 100, BaseCommit: a0.BaseCommit, Branch: a0.Branch + "-2", Worktree: l.wt, RunID: l.worker,
			Ready: a0.Ready, Rounds: a0.Rounds, Accepts: []flow.Accept{{Head: l.c1, By: "bar"}},
			Acts: []flow.Act{{Key: flow.AskMerge + "@" + l.c1, Result: "done", At: now}},
		})
	})

	// Pass 1: logs I4 and I5 once each, holds coordinator wakes, and makes no repair.
	l.pass()
	i4Lines := logRules(t, l.paths.Log(l.id), "I4")
	i5Lines := logRules(t, l.paths.Log(l.id), "I5")
	if len(i4Lines) != 1 || !strings.Contains(i4Lines[0].Why, "without an accept record") {
		t.Fatalf("I4 log lines = %+v, want 1", i4Lines)
	}
	if len(i5Lines) != 1 || !strings.Contains(i5Lines[0].Why, "has 2 open attempts") {
		t.Fatalf("I5 log lines = %+v, want 1", i5Lines)
	}
	if strings.Count(l.log.String(), "factory.invariant_error") != 2 {
		t.Fatalf("daemon log invariant events = %d, want 2:\n%s", strings.Count(l.log.String(), "factory.invariant_error"), l.log.String())
	}
	st := l.state()
	if len(st.Tasks[0].Attempts) != 2 || len(st.Tasks[0].Attempts[0].Accepts) != 0 {
		t.Fatalf("invariant check repaired state instead of only telling: %+v", st.Tasks[0].Attempts)
	}
	if len(st.Project.Wakes) != 2 {
		t.Fatalf("held wakes = %+v, want 2 (I4 and I5)", st.Project.Wakes)
	}

	// Pass 2 (unchanged violations): no duplicate log line, no duplicate event, no duplicate wake.
	now = now.Add(15 * time.Second)
	l.pass()
	if len(logRules(t, l.paths.Log(l.id), "I4")) != 1 || len(logRules(t, l.paths.Log(l.id), "I5")) != 1 ||
		strings.Count(l.log.String(), "factory.invariant_error") != 2 || len(l.state().Project.Wakes) != 2 {
		t.Fatalf("pass 2 duplicated invariant log or wake: wakes=%+v", l.state().Project.Wakes)
	}

	// Coordinator goes idle -> J12 delivers both invariant wakes as one batch.
	l.srv.SetStatus("coord-elsewhere", &hc.Status{App: "claude-code", State: "idle"})
	now = now.Add(5 * time.Second)
	l.pass()
	now = now.Add(25 * time.Second)
	l.pass()
	sends := sendsTo(t, l.srv.Requests(), "coord-elsewhere")
	if len(sends) != 1 || !strings.Contains(sends[0], "I4:") || !strings.Contains(sends[0], "I5:") {
		t.Fatalf("coordinator wakes = %q, want 1 batched wake naming I4 and I5", sends)
	}

	// Restarted daemon with the same violations still present must neither re-log nor re-wake.
	var restartLog bytes.Buffer
	eng2 := New(l.paths, l.client)
	eng2.Log = logging.NewJSONLLogger(&restartLog)
	eng2.Now = func() time.Time { return now }
	eng2.passProject(ctx, l.id, true)
	if len(l.state().Project.Wakes) != 0 {
		t.Fatalf("restarted daemon re-woke coordinator for already-woken invariant: %+v", l.state().Project.Wakes)
	}
	if restartLog.String() != "" {
		t.Fatalf("restarted daemon re-logged active violations:\n%s", restartLog.String())
	}

	// Fix violations, pass cleanly, then break I4 with a different commit -> logs & wakes again.
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts = s.Tasks[0].Attempts[:1]
		s.Tasks[0].Attempts[0].Accepts = []flow.Accept{{Head: l.c1, By: "bar"}}
	})
	now = now.Add(15 * time.Second)
	l.pass()
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Acts = append(s.Tasks[0].Attempts[0].Acts, flow.Act{Key: flow.AskMerge + "@deadbeef", Result: "done", At: now})
	})
	now = now.Add(15 * time.Second)
	l.pass()
	if len(logRules(t, l.paths.Log(l.id), "I4")) != 2 { // initial + new violation
		t.Fatalf("I4 log lines after new violation = %+v, want 2", logRules(t, l.paths.Log(l.id), "I4"))
	}
	if ws := l.state().Project.Wakes; len(ws) != 1 || !strings.Contains(ws[0].Why, "deadbeef") {
		t.Fatalf("wakes after new I4 violation = %+v, want 1 naming deadbeef", ws)
	}
}

// TestPRDivergedVoidsLiveReviewer verifies that a force-pushed PR head stops an
// in-flight reviewer in the same pass so I1 holds under N7.
func TestPRDivergedVoidsLiveReviewer(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	ctx := context.Background()
	revID, err := startRun(ctx, l.client, world.RunSpec{Name: "rev-t1-div", Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(revID, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{Round: 1, Head: l.c1, Code: "div", RunID: revID}
	})
	ext := pushExternalToBranch(t, l, true, "force.txt", "diverged\n")
	writePRJSON(t, l.gh, "view.json", ext, greenRollup)
	l.pass()
	if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || rv.RunID != "" || rv.Run == nil || rv.Run.Result != flow.EndStopped {
		t.Fatalf("live reviewer not stopped on diverged PR head: %+v", rv)
	}
}

// TestInvariantNoFalseAlarmOnSkippedTaskPass verifies fix 1(a): a task that
// skips its pass (void failed to stop or worker hc error) is not judged on
// stale reviewer state.
func TestInvariantNoFalseAlarmOnSkippedTaskPass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := newLanding(t).withTokens()
	revID, err := startRun(ctx, l.client, world.RunSpec{Name: reviewerName(l.id, "T1", 1, 1), Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(revID, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{Round: 1, Head: l.c1, Code: "skip", RunID: revID}
	})
	// Coordinator sends task back between passes -> rev.Round (1) != len(Rounds)+1 (2).
	l.report("tok-coord", "task", `{"action":"send-back","task":"T1","why":"add edge case"}`, "")
	// Fail terminal.kill so observeReviewLocked's voidRound fails and passTaskLocked skips.
	var failKill atomic.Bool
	failKill.Store(true)
	l.eng.HC = hc.NewClient(methodFailProxy(t, l.srv.Path, "terminal.kill", &failKill))
	l.pass()
	// Also test workerErr skipping passTaskLocked.
	var failWorker atomic.Bool
	failWorker.Store(true)
	l.eng.HC = hc.NewClient(methodFailProxy(t, l.srv.Path, l.worker, &failWorker))
	l.pass()
	// Once hc recovers, the next pass voids the reviewer cleanly and I1 holds.
	l.eng.HC = l.client
	l.pass()
	if rs, _ := termState(ctx, l.client, revID); rs != flow.RunGone {
		t.Fatalf("reviewer after recovery = %s, want gone", rs)
	}
}

// TestInvariantEndedWorkerDuringMergeNotLive verifies fix 1(b): a worker that
// exits between initial observation and N42's fresh merge check persists
// PhaseEnded while RunID is still populated until the next pass, and is not
// live for I1.
func TestInvariantEndedWorkerDuringMergeNotLive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := newLanding(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Run = &flow.RunRecord{Address: l.worker, Role: flow.RoleWorker, CLI: "claude", State: flow.PhaseIdle, Status: "idle"}
	})
	baseReqs := len(l.srv.Requests())
	var killed atomic.Bool
	l.eng.Now = func() time.Time {
		// Kill l.worker after passProject's initial advanceRunLocked observed it idle,
		// before execMerge refreshes it.
		if !killed.Load() {
			for _, req := range l.srv.Requests()[baseReqs:] {
				if req.Method == "terminal.get" && strings.Contains(string(req.Params), l.worker) {
					if killed.CompareAndSwap(false, true) {
						_, _ = l.client.Kill(ctx, l.worker)
					}
					break
				}
			}
		}
		return now
	}
	l.pass()
	a := l.state().Tasks[0].Attempts[0]
	if !flow.Done(a.Acts, flow.AskMerge+"@"+l.c1) {
		t.Fatalf("expected N42 merge to succeed, acts=%+v", a.Acts)
	}
	if a.Run == nil || a.Run.State != flow.PhaseEnded {
		t.Fatalf("expected worker Run to be PhaseEnded after merge refresh, got %+v", a.Run)
	}
}

// TestInvariantI2NoClockRaceAtJ6Expiry verifies fix 1(c): I2 uses the same pass
// decision as J6/J7 with no second clock read, so crossing the J6 gap boundary
// after passProjectRulesLocked does not fire I2.
func TestInvariantI2NoClockRaceAtJ6Expiry(t *testing.T) {
	t.Parallel()
	h := newProjectHarness(t, "I2 clock race")
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h.edit(func(s *flow.State) {
		s.Project.RunID = ""
		s.Project.Run = &flow.RunRecord{Role: flow.RoleCoordinator, State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: t0}
		s.Project.Acts = []flow.Act{
			{Key: flow.AskStartCoordinator, Result: "done", At: t0},
			{Key: "sweep", Result: "done", At: t0},
		}
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: demo", Parked: true}}
	})
	h.eng.Now = func() time.Time {
		if len(logRules(t, h.paths.Log(h.id), "J6")) > 0 {
			return t0.Add(10*time.Minute + time.Second)
		}
		return t0.Add(9*time.Minute + 59*time.Second)
	}
	h.pass()
}

// TestInvariantRestartNoRelogRecurrenceWakesAndErrorEvent verifies fixes 2 and 7:
// the runtime log event passes logging.IsErrorEvent, a daemon restart with the
// same stored violation neither re-logs nor re-wakes, and clearing then
// re-breaking the exact same invariant wakes again.
func TestInvariantRestartNoRelogRecurrenceWakesAndErrorEvent(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	var mirror bytes.Buffer
	l.eng.Now = func() time.Time { return now }
	l.eng.Log = logging.Tee{logging.NewJSONLLogger(l.log), logging.NewEventMirror(&mirror, logging.LevelError)}
	l.eng.onInvariant = nil

	// Break I4 on l.c1.
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Acts = append(a.Acts, flow.Act{Key: flow.AskMerge + "@" + l.c1, Result: "done", At: now})
	})
	l.pass()
	if !strings.Contains(mirror.String(), "ERROR factory.invariant_error") {
		t.Errorf("runtime error log mirror dropped invariant event: %q", mirror.String())
	}
	if len(l.state().Project.Wakes) != 1 {
		t.Fatalf("wakes = %+v, want 1", l.state().Project.Wakes)
	}

	// Deliver wake via J12.
	l.srv.SetStatus("coord-elsewhere", &hc.Status{App: "claude-code", State: "idle"})
	now = now.Add(5 * time.Second)
	l.pass()
	now = now.Add(25 * time.Second)
	l.pass()
	if len(l.state().Project.Wakes) != 0 {
		t.Fatalf("wakes not delivered: %+v", l.state().Project.Wakes)
	}

	// Restarted daemon with the same persisted violation must neither re-log nor re-wake.
	var restartLog bytes.Buffer
	eng2 := New(l.paths, l.client)
	eng2.Log = logging.NewJSONLLogger(&restartLog)
	eng2.Now = func() time.Time { return now }
	eng2.passProject(ctx, l.id, true)
	if restartLog.String() != "" {
		t.Errorf("restarted daemon re-logged unchanged violation:\n%s", restartLog.String())
	}
	if got := logRules(t, l.paths.Log(l.id), "I4"); len(got) != 1 {
		t.Errorf("restarted daemon appended duplicate I4 project log line: len=%d, want 1", len(got))
	}
	if len(l.state().Project.Wakes) != 0 {
		t.Errorf("restarted daemon re-woke coordinator: %+v", l.state().Project.Wakes)
	}

	// Fix I4, pass cleanly -> stored violation and wake@invariant@I4@... act clear.
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Accepts = []flow.Accept{{Head: l.c1, By: "bar"}}
	})
	now = now.Add(15 * time.Second)
	l.pass()

	// Exact same I4 violation recurs (same commit l.c1 -> same why and hash) -> logs and wakes again.
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Accepts = nil
	})
	now = now.Add(15 * time.Second)
	l.pass()
	if got := logRules(t, l.paths.Log(l.id), "I4"); len(got) != 2 {
		t.Errorf("I4 project log lines after recurrence = %d, want 2", len(got))
	}
	if len(l.state().Project.Wakes) != 1 {
		t.Errorf("wakes after identical I4 recurrence = %+v, want 1", l.state().Project.Wakes)
	}
}

// TestRecapOpeningUncountedDuringHCOutage verifies fix 3: recap verbs that stop
// runs first are outage-uncounted under hc_unavailable, so an hc outage does
// not exhaust N5 and block opening the recap.
func TestRecapOpeningUncountedDuringHCOutage(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{flow.AskOpenRecap, flow.AskSizeRecap, flow.AskCIRecap, flow.AskTimeRecap} {
		if !actOutage(verb, flow.Facts{HCUnavailable: true}) {
			t.Errorf("actOutage(%q, HCUnavailable=true) = false, want true", verb)
		}
	}
	ctx := context.Background()
	l := newLanding(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	revID, err := startRun(ctx, l.client, world.RunSpec{Name: reviewerName(l.id, "T1", 1, 1), Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(revID, &hc.Status{App: "claude-code", State: "working"})
	c2 := commitFile(t, l.wt, "big.go", strings.Repeat("// line\n", 10))
	gitIn(t, l.wt, "push", "origin", c2+":refs/heads/"+l.branch)
	writePRJSON(t, l.gh, "view.json", c2, greenRollup)
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.SizeCap = 3
		a.Ready = append(a.Ready, flow.Ready{Head: c2})
		a.Rounds = nil
		a.Review = &flow.ReviewRun{Round: 1, Head: c2, Code: "cap", RunID: revID}
	})
	var failCoord, failKill atomic.Bool
	failCoord.Store(true)
	failKill.Store(true)
	pKill := methodFailProxy(t, l.srv.Path, "terminal.kill", &failKill)
	l.eng.HC = hc.NewClient(methodFailProxy(t, pKill, "coord-elsewhere", &failCoord))

	for _, step := range []time.Duration{2 * time.Minute, 3 * time.Minute, 5 * time.Minute, 9 * time.Minute} {
		l.pass()
		now = now.Add(step)
	}
	st := l.state()
	last := flow.LastAct(st.Tasks[0].Attempts[0].Acts, flow.AskSizeRecap+"@0")
	if last == nil || last.Result != "failed" || !last.Uncounted {
		t.Fatalf("last open_size_recap act during hc outage = %+v, want failed Uncounted=true", last)
	}
	if st.Tasks[0].LastRow == "N5" || len(st.Project.Wakes) != 0 {
		t.Fatalf("hc outage exhausted recap into N5: row=%s wakes=%+v", st.Tasks[0].LastRow, st.Project.Wakes)
	}
	failCoord.Store(false)
	failKill.Store(false)
	now = now.Add(31 * time.Minute)
	l.pass()
	if flow.OpenRecap(l.state().Tasks[0]) == nil {
		t.Fatal("recap did not open after hc recovered")
	}
	if rs, _ := termState(ctx, l.client, revID); rs != flow.RunGone {
		t.Fatalf("reviewer after recap opened = %s, want gone", rs)
	}
}

// TestCrashBeforeReviewRunIDStoppedByName verifies fix 4: when a crash leaves a
// starting reviewer with a blank RunID, both the void path and recap stop use
// the deterministic reviewer name as fallback and record EndStopped only after
// the stop succeeds.
func TestCrashBeforeReviewRunIDStoppedByName(t *testing.T) {
	t.Parallel()
	t.Run("void path on diverged PR", func(t *testing.T) {
		ctx := context.Background()
		l := newLanding(t)
		revName := reviewerName(l.id, "T1", 1, 1)
		if _, err := startRun(ctx, l.client, world.RunSpec{Name: revName, Cwd: l.wt, Argv: []string{"sh"}}); err != nil {
			t.Fatal(err)
		}
		l.edit(func(s *flow.State) {
			s.Tasks[0].Attempts[0].Rounds = nil
			s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{
				Round: 1, Head: l.c1, Code: "div",
				Run: &flow.RunRecord{Role: flow.RoleReviewer, State: flow.PhaseStarting},
			}
		})
		ext := pushExternalToBranch(t, l, true, "force.txt", "diverged\n")
		writePRJSON(t, l.gh, "view.json", ext, greenRollup)

		var failKill atomic.Bool
		failKill.Store(true)
		l.eng.HC = hc.NewClient(methodFailProxy(t, l.srv.Path, "terminal.kill", &failKill))
		l.pass()
		if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || (rv.Run != nil && rv.Run.Result == flow.EndStopped) {
			t.Fatalf("recorded EndStopped even though StopRun failed: %+v", rv)
		}
		if rs, _ := termState(ctx, l.client, revName); rs == flow.RunGone {
			t.Fatalf("reviewer terminal should still be live while kill fails")
		}

		failKill.Store(false)
		l.pass()
		if rs, _ := termState(ctx, l.client, revName); rs != flow.RunGone {
			t.Fatalf("named reviewer terminal not stopped on diverged PR: %s", rs)
		}
		if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || rv.Run == nil || rv.Run.Result != flow.EndStopped {
			t.Fatalf("review after void stop = %+v, want EndStopped", rv)
		}
		// Subsequent pass under N7 must not relaunch the reviewer.
		l.pass()
		if rs, _ := termState(ctx, l.client, revName); rs != flow.RunGone {
			t.Fatalf("reviewer relaunched on subsequent N7 pass: %s", rs)
		}
	})

	t.Run("recap stop path on size cap", func(t *testing.T) {
		ctx := context.Background()
		l := newLanding(t)
		now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
		l.eng.Now = func() time.Time { return now }
		revName := reviewerName(l.id, "T1", 1, 1)
		if _, err := startRun(ctx, l.client, world.RunSpec{Name: revName, Cwd: l.wt, Argv: []string{"sh"}}); err != nil {
			t.Fatal(err)
		}
		c2 := commitFile(t, l.wt, "big.go", strings.Repeat("// line\n", 10))
		gitIn(t, l.wt, "push", "origin", c2+":refs/heads/"+l.branch)
		writePRJSON(t, l.gh, "view.json", c2, greenRollup)
		l.edit(func(s *flow.State) {
			a := &s.Tasks[0].Attempts[0]
			a.SizeCap = 3
			a.Ready = append(a.Ready, flow.Ready{Head: c2})
			a.Rounds = nil
			a.Review = &flow.ReviewRun{
				Round: 1, Head: c2, Code: "cap",
				Run: &flow.RunRecord{Role: flow.RoleReviewer, State: flow.PhaseStarting},
			}
		})

		var failKill atomic.Bool
		failKill.Store(true)
		l.eng.HC = hc.NewClient(methodFailProxy(t, l.srv.Path, "terminal.kill", &failKill))
		l.pass()
		if flow.OpenRecap(l.state().Tasks[0]) != nil {
			t.Fatal("recap opened even though stopping the reviewer failed")
		}
		if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || (rv.Run != nil && rv.Run.Result == flow.EndStopped) {
			t.Fatalf("recorded EndStopped even though recap stop failed: %+v", rv)
		}

		failKill.Store(false)
		now = now.Add(2 * time.Minute)
		l.pass()
		if rs, _ := termState(ctx, l.client, revName); rs != flow.RunGone {
			t.Fatalf("named reviewer terminal not stopped by recap: %s", rs)
		}
		if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || rv.Run == nil || rv.Run.Result != flow.EndStopped {
			t.Fatalf("review after recap stop = %+v, want EndStopped", rv)
		}
		if flow.OpenRecap(l.state().Tasks[0]) == nil {
			t.Fatal("recap not opened after stop succeeded")
		}
		// Subsequent pass under N6 must not relaunch the reviewer.
		now = now.Add(time.Minute)
		l.pass()
		if rs, _ := termState(ctx, l.client, revName); rs != flow.RunGone {
			t.Fatalf("reviewer relaunched on subsequent N6 pass: %s", rs)
		}
	})
}
