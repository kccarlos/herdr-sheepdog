package factory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	eng := newGitTestEngine(t, paths, client)
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
	eng2 := newGitTestEngine(t, l.paths, l.client)
	eng2.Log = logging.NewJSONLLogger(&restartLog)
	eng2.Now = func() time.Time { return now }
	eng2.passProject(ctx, l.id, true)
	if len(l.state().Project.Wakes) != 0 {
		t.Fatalf("restarted daemon re-woke coordinator for already-woken invariant: %+v", l.state().Project.Wakes)
	}
	if withoutFactoryChanged(restartLog.String()) != "" {
		t.Fatalf("restarted daemon re-logged active violations:\n%s", withoutFactoryChanged(restartLog.String()))
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
	eng2 := newGitTestEngine(t, l.paths, l.client)
	eng2.Log = logging.NewJSONLLogger(&restartLog)
	eng2.Now = func() time.Time { return now }
	eng2.passProject(ctx, l.id, true)
	if withoutFactoryChanged(restartLog.String()) != "" {
		t.Errorf("restarted daemon re-logged unchanged violation:\n%s", withoutFactoryChanged(restartLog.String()))
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
		if !actOutage(verb, flow.Facts{HCUnavailable: true}, nil) {
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

// TestInvariantPerSubjectSkipAndRecurrence verifies that stored invariant
// violations are keyed per (invariant, subject): skipping one task keeps its
// stored violation without disturbing another task, clearing and re-breaking
// one subject while another holds the same invariant open wakes again, and
// A->B->A on one subject wakes at each start while restarts stay quiet.
func TestInvariantPerSubjectSkipAndRecurrence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := newLanding(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	l.eng.onInvariant = nil

	// Second task T2 with its own worktree (at l.c1 so view.json matches H),
	// worker, and unwanted live reviewer.
	wt2, err := fixtureGit(t).NewWorktree(ctx, l.repo, l.paths.Worktree(l.id, "T2", 1), "T2-1", "feat: y")
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt2.Path, "reset", "--hard", l.c1)
	w2, err := startRun(ctx, l.client, world.RunSpec{Name: "worker-t2", Cwd: wt2.Path, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setIdle(t, l.srv, l.client, w2, "idle")
	rev1, err := startRun(ctx, l.client, world.RunSpec{Name: "rev-t1-i1", Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(rev1, &hc.Status{App: "claude-code", State: "working"})
	rev2, err := startRun(ctx, l.client, world.RunSpec{Name: "rev-t2-i1", Cwd: wt2.Path, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(rev2, &hc.Status{App: "claude-code", State: "working"})

	// Both T1 and T2 have passed the ladder and asked merge (N41 wait), yet both
	// carry an unwanted live reviewer (I1 on T1 and I1 on T2).
	l.edit(func(s *flow.State) {
		a1 := &s.Tasks[0].Attempts[0]
		a1.Accepts = []flow.Accept{{Head: l.c1, By: "bar"}}
		a1.Acts = []flow.Act{{Key: flow.AskMerge + "@" + l.c1, Result: "done", At: now}}
		a1.Review = &flow.ReviewRun{Round: 3, Head: l.c1, Code: "r1", RunID: rev1, Run: &flow.RunRecord{Address: rev1, Role: flow.RoleReviewer, State: flow.PhaseWorking}}
		s.Tasks = append(s.Tasks, flow.Task{
			ID: "T2", Title: "feat: y", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: wt2.BaseCommit, Branch: wt2.Branch, Worktree: wt2.Path, PR: 7, RunID: w2,
				Ready:   []flow.Ready{{Head: l.c1}},
				Rounds:  []flow.Round{{N: 1, Kind: "full", Head: l.c1, Verdict: flow.VerdictPass}, {N: 2, Kind: "full", Head: l.c1, Verdict: flow.VerdictPass}},
				Accepts: []flow.Accept{{Head: l.c1, By: "bar"}},
				Acts:    []flow.Act{{Key: flow.AskMerge + "@" + l.c1, Result: "done", At: now}},
				Review:  &flow.ReviewRun{Round: 3, Head: l.c1, Code: "r2", RunID: rev2, Run: &flow.RunRecord{Address: rev2, Role: flow.RoleReviewer, State: flow.PhaseWorking}},
			}},
		})
	})

	deliverWakes := func() {
		t.Helper()
		if len(l.state().Project.CoordWakesInFlight) > 0 {
			l.srv.SetScreen("coord-elsewhere", completedWakeScreen("claude", lastSendTo(t, l.srv.Requests(), "coord-elsewhere"), "Handled invariant wakes."))
		}
		l.srv.SetStatus("coord-elsewhere", &hc.Status{App: "claude-code", State: "idle"})
		now = now.Add(5 * time.Second)
		l.pass()
		now = now.Add(25 * time.Second)
		l.pass()
		l.srv.SetStatus("coord-elsewhere", &hc.Status{App: "claude-code", State: "working"})
		if ws := l.state().Project.Wakes; len(ws) != 0 {
			t.Fatalf("expected wakes delivered, still held: %+v", ws)
		}
	}

	// Pass 1: both I1@T1 and I1@T2 start -> logged once each, 2 wakes held.
	l.pass()
	if got := logRules(t, l.paths.Log(l.id), "I1"); len(got) != 2 {
		t.Fatalf("pass 1 I1 log lines = %d, want 2: %+v", len(got), got)
	}
	if ws := l.state().Project.Wakes; len(ws) != 2 {
		t.Fatalf("pass 1 wakes = %+v, want 2 (T1 and T2)", ws)
	}
	deliverWakes()

	// Skip T1 (hc error on T1's worker) while T2 still violates I1:
	// T1 keeps its stored state (neither cleared nor re-logged), T2 is unchanged.
	var failW1 atomic.Bool
	failW1.Store(true)
	l.eng.HC = hc.NewClient(methodFailProxy(t, l.srv.Path, l.worker, &failW1))
	now = now.Add(15 * time.Second)
	l.pass()
	if got := logRules(t, l.paths.Log(l.id), "I1"); len(got) != 2 {
		t.Fatalf("skip T1 manufactured I1 log change: len=%d, want 2", len(got))
	}
	if ws := l.state().Project.Wakes; len(ws) != 0 {
		t.Fatalf("skip T1 queued spurious wake: %+v", ws)
	}

	// T1 recovers: both still violate I1 -> still no re-log and no re-wake.
	failW1.Store(false)
	l.eng.HC = l.client
	now = now.Add(15 * time.Second)
	l.pass()
	if got := logRules(t, l.paths.Log(l.id), "I1"); len(got) != 2 {
		t.Fatalf("recover T1 re-logged I1: len=%d, want 2", len(got))
	}
	if ws := l.state().Project.Wakes; len(ws) != 0 {
		t.Fatalf("recover T1 queued spurious wake: %+v", ws)
	}

	// Restart daemon with both stored violations active -> no re-log, no re-wake.
	var restartLog bytes.Buffer
	eng2 := newGitTestEngine(t, l.paths, l.client)
	eng2.Log = logging.NewJSONLLogger(&restartLog)
	eng2.Now = func() time.Time { return now }
	eng2.passProject(ctx, l.id, true)
	if withoutFactoryChanged(restartLog.String()) != "" || len(logRules(t, l.paths.Log(l.id), "I1")) != 2 || len(l.state().Project.Wakes) != 0 {
		t.Fatalf("restart re-logged or re-woke: log=%q i1=%d wakes=%+v", withoutFactoryChanged(restartLog.String()), len(logRules(t, l.paths.Log(l.id), "I1")), l.state().Project.Wakes)
	}

	// Fix T2 while T1 remains broken, then re-break T2 -> T2 wakes again!
	l.edit(func(s *flow.State) { s.Tasks[1].Attempts[0].Review = nil })
	now = now.Add(15 * time.Second)
	l.pass()
	l.edit(func(s *flow.State) {
		s.Tasks[1].Attempts[0].Review = &flow.ReviewRun{Round: 3, Head: l.c1, Code: "r2", RunID: rev2, Run: &flow.RunRecord{Address: rev2, Role: flow.RoleReviewer, State: flow.PhaseWorking}}
	})
	now = now.Add(15 * time.Second)
	l.pass()
	if got := logRules(t, l.paths.Log(l.id), "I1"); len(got) != 3 {
		t.Fatalf("T2 recurrence I1 log lines = %d, want 3", len(got))
	}
	if ws := l.state().Project.Wakes; len(ws) != 1 || !strings.Contains(ws[0].Why, "T2") {
		t.Fatalf("T2 recurrence wakes = %+v, want 1 naming T2", ws)
	}
	deliverWakes()

	// A -> B -> A on one subject (I4@T1) without clearing in between wakes at each start.
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Accepts = nil // A: unaccepted merge@c1
	})
	now = now.Add(15 * time.Second)
	l.pass()
	if got := logRules(t, l.paths.Log(l.id), "I4"); len(got) != 1 || len(l.state().Project.Wakes) != 1 {
		t.Fatalf("I4 A start: logs=%d wakes=%+v, want 1 and 1", len(got), l.state().Project.Wakes)
	}
	deliverWakes()

	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Acts = append(s.Tasks[0].Attempts[0].Acts, flow.Act{Key: flow.AskMerge + "@deadbeef", Result: "done", At: now}) // B: c1 + deadbeef
	})
	now = now.Add(15 * time.Second)
	l.pass()
	if got := logRules(t, l.paths.Log(l.id), "I4"); len(got) != 2 || len(l.state().Project.Wakes) != 1 {
		t.Fatalf("I4 B start: logs=%d wakes=%+v, want 2 and 1", len(got), l.state().Project.Wakes)
	}
	deliverWakes()

	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Acts = []flow.Act{{Key: flow.AskMerge + "@" + l.c1, Result: "done", At: now}} // back to A
	})
	now = now.Add(15 * time.Second)
	l.pass()
	if got := logRules(t, l.paths.Log(l.id), "I4"); len(got) != 3 {
		t.Fatalf("I4 return to A logs = %d, want 3", len(got))
	}
	if ws := l.state().Project.Wakes; len(ws) != 1 {
		t.Fatalf("I4 return to A wakes = %+v, want 1", ws)
	}
}

// TestUnansweredPRReadHoldsReviewer verifies that when a diverged PR head stops
// the reviewer and the next PR read fails (Answered=false), N29 holds without
// relaunching the reviewer until a fresh answered PR snapshot returns, at which
// point N7 holds if still diverged or N29 resumes if the PR matches H.
func TestUnansweredPRReadHoldsReviewer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := newLanding(t)
	revName := reviewerName(l.id, "T1", 1, 1)
	revID, err := startRun(ctx, l.client, world.RunSpec{Name: revName, Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(revID, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{Round: 1, Head: l.c1, Code: "div", RunID: revID}
	})

	// Pass 1: diverged PR head stops the reviewer (EndStopped) and N7 matches.
	ext := pushExternalToBranch(t, l, true, "force.txt", "diverged\n")
	writePRJSON(t, l.gh, "view.json", ext, greenRollup)
	l.pass()
	if st := l.state(); st.Tasks[0].LastRow != "N7" || st.Tasks[0].Attempts[0].Review == nil ||
		st.Tasks[0].Attempts[0].Review.RunID != "" || st.Tasks[0].Attempts[0].Review.Run == nil ||
		st.Tasks[0].Attempts[0].Review.Run.Result != flow.EndStopped {
		t.Fatalf("pass 1 diverged PR: row=%s review=%+v, want N7 EndStopped", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].Review)
	}

	// Pass 2: PR read fails (Answered=false) -> N29 must hold, not relaunch.
	if err := os.WriteFile(filepath.Join(l.gh, "fail"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	l.pass()
	if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || rv.RunID != "" || rv.Run == nil || rv.Run.Result != flow.EndStopped {
		t.Fatalf("unanswered PR read relaunched reviewer: %+v", rv)
	}
	if rs, _ := termState(ctx, l.client, revName); rs != flow.RunGone {
		t.Fatalf("reviewer terminal started during unanswered PR read: %s", rs)
	}

	// Pass 3: fresh answered read returns still diverged -> N7 applies, no relaunch.
	if err := os.Remove(filepath.Join(l.gh, "fail")); err != nil {
		t.Fatal(err)
	}
	l.pass()
	if st := l.state(); st.Tasks[0].LastRow != "N7" || st.Tasks[0].Attempts[0].Review.RunID != "" {
		t.Fatalf("answered still-diverged PR: row=%s review=%+v, want N7 no reviewer", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].Review)
	}

	// Pass 4: fresh answered read returns PR at H -> N29 resumes reviewer uncounted.
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	l.pass()
	if st := l.state(); st.Tasks[0].LastRow != "N29" || st.Tasks[0].Attempts[0].Review.RunID == "" || st.Tasks[0].Attempts[0].Review.Retries != 0 {
		t.Fatalf("answered PR back at H: row=%s review=%+v, want N29 resumed reviewer retries=0", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].Review)
	}
}

// TestGHShimFailsHardWithoutFakeGHDir verifies that the TestMain gh shim never
// falls back to the real gh CLI when run from a directory with no fake-gh-dir.
func TestGHShimFailsHardWithoutFakeGHDir(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("gh", "--version")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("gh succeeded in unbound dir (fell back to real gh): %s", out)
	}
	if !strings.Contains(string(out), "no fake-gh-dir") {
		t.Fatalf("gh in unbound dir failed with %v (%q), want hard shim failure naming no fake-gh-dir", err, string(out))
	}
}

func failOnceProxy(t *testing.T, target, method string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sdp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var failed atomic.Bool
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				var req struct {
					ID     uint64 `json:"id"`
					Method string `json:"method"`
				}
				_ = json.Unmarshal(line, &req)
				if req.Method == method && failed.CompareAndSwap(false, true) {
					_, _ = fmt.Fprintf(c, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"%s failed once"}}`+"\n", req.ID, method)
					return
				}
				up, err := net.Dial("unix", target)
				if err != nil {
					return
				}
				defer func() { _ = up.Close() }()
				if _, err := up.Write(line); err == nil {
					_, _ = io.Copy(c, up)
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// TestCoordReadErrorSkipsProjectRules verifies fix 1: when the coordinator's
// status read fails (coordErr != nil), project rules do not act on the stale
// record (no J9 stop even past coordinator_silent_minutes).
func TestCoordReadErrorSkipsProjectRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newProjectHarness(t, "J9 coordErr guard")
	h.pass() // J7 starts coord1
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open"}}
		s.Project.Wakes = []flow.Wake{{Key: "worker_failed", Task: "T1", Why: "N23", Facts: "title x", At: h.now}}
	})
	h.coordIdleWithStatus(coord1, "idle") // J12 delivers wake, records CoordWakeAt
	// Bring coordinator record back to PhaseIdle before timeout, then advance past coordinator_silent_minutes.
	h.now = h.now.Add(15 * time.Minute)
	h.pass()
	h.now = h.now.Add(14*time.Minute + 59*time.Second)
	h.pass() // PhaseIdle at 29m59s
	if st := h.state(); st.Project.Run == nil || st.Project.Run.State != flow.PhaseIdle || st.Project.CoordWakeAt.IsZero() {
		t.Fatalf("expected idle coordinator with delivered wake, got run=%+v wakeAt=%v", st.Project.Run, st.Project.CoordWakeAt)
	}

	// Coordinator begins working just as its next status read fails (once), while
	// subsequent list/stop RPCs would succeed. Past coordinator_silent_minutes, J9
	// must not act on the stale idle record.
	h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
	h.now = h.now.Add(time.Minute)
	h.eng.HC = hc.NewClient(failOnceProxy(t, h.srv.Path, "terminal.get"))
	h.pass()
	if st := h.state(); st.Project.RunID != coord1 {
		t.Fatalf("J9 stopped coordinator on failed status read: runID=%q j9=%+v", st.Project.RunID, logRules(t, h.paths.Log(h.id), "J9"))
	}
	if j9 := logRules(t, h.paths.Log(h.id), "J9"); len(j9) != 0 {
		t.Fatalf("unexpected J9 log lines on failed status read: %+v", j9)
	}
	if tm, err := h.client.Get(ctx, coord1); err != nil || tm.State != "live" {
		t.Fatalf("coordinator terminal not live after failed status read: tm=%+v err=%v", tm, err)
	}

	// Next pass reads working status cleanly, clears CoordWakeAt, and keeps running.
	h.now = h.now.Add(15 * time.Second)
	h.pass()
	if st := h.state(); st.Project.RunID != coord1 || !st.Project.CoordWakeAt.IsZero() {
		t.Fatalf("coordinator did not recover cleanly: runID=%q wakeAt=%v", st.Project.RunID, st.Project.CoordWakeAt)
	}
}

// TestUnclassifiedPRHeadHoldsReviewer verifies fix 2: after N7 stops round 1 on
// a force-push, a second force-push whose commit is missing locally and cannot
// be fetched leaves HeadRel empty; N29 must hold with no reviewer relaunch.
func TestUnclassifiedPRHeadHoldsReviewer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := newLanding(t)
	revName := reviewerName(l.id, "T1", 1, 1)
	revID, err := startRun(ctx, l.client, world.RunSpec{Name: revName, Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(revID, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{Round: 1, Head: l.c1, Code: "div", RunID: revID}
	})

	// Pass 1: first force-push stops round 1 via N7 (EndStopped).
	ext1 := pushExternalToBranch(t, l, true, "force1.txt", "diverged 1\n")
	writePRJSON(t, l.gh, "view.json", ext1, greenRollup)
	l.pass()
	if st := l.state(); st.Tasks[0].LastRow != "N7" || st.Tasks[0].Attempts[0].Review == nil ||
		st.Tasks[0].Attempts[0].Review.RunID != "" || st.Tasks[0].Attempts[0].Review.Run == nil ||
		st.Tasks[0].Attempts[0].Review.Run.Result != flow.EndStopped {
		t.Fatalf("pass 1 diverged PR: row=%s review=%+v, want N7 EndStopped", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].Review)
	}

	// Pass 2: second force-push arrives on GitHub, but remote cannot be fetched so
	// HeadRel is empty (unclassified). N29 must hold without relaunching round 1.
	ext2 := pushExternalToBranch(t, l, true, "force2.txt", "diverged 2\n")
	writePRJSON(t, l.gh, "view.json", ext2, greenRollup)
	bare := gitIn(t, l.repo, "remote", "get-url", "--push", "origin")
	if err := os.Rename(bare, bare+".offline"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(bare+".offline", bare) })
	l.pass()
	if rv := l.state().Tasks[0].Attempts[0].Review; rv == nil || rv.RunID != "" || rv.Run == nil || rv.Run.Result != flow.EndStopped {
		t.Fatalf("unclassified PR head relaunched reviewer: %+v", rv)
	}
	if rs, _ := termState(ctx, l.client, revName); rs != flow.RunGone {
		t.Fatalf("reviewer terminal started for unclassified PR head: %s", rs)
	}

	// Pass 3: remote comes back, fetch classifies ext2 as diverged -> N7, still no relaunch.
	if err := os.Rename(bare+".offline", bare); err != nil {
		t.Fatal(err)
	}
	l.pass()
	if st := l.state(); st.Tasks[0].LastRow != "N7" || st.Tasks[0].Attempts[0].Review.RunID != "" {
		t.Fatalf("after fetch classified ext2: row=%s review=%+v, want N7 no reviewer", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].Review)
	}
}

// TestInvariantCleanupRetryWithStaleReviewerNotI1 verifies fix 3: send-back
// then drop with a reviewer in flight, followed by a transient cleanup stop
// failure, records no I1 line and queues no wake while cleanup is retrying.
func TestInvariantCleanupRetryWithStaleReviewerNotI1(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	l := newLanding(t).withTokens()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	var violations []flow.InvariantViolation
	l.eng.onInvariant = func(vs []flow.InvariantViolation) { violations = append(violations, vs...) }

	revID, err := startRun(ctx, l.client, world.RunSpec{Name: reviewerName(l.id, "T1", 1, 1), Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(revID, &hc.Status{App: "claude-code", State: "working"})
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].Rounds = nil
		s.Tasks[0].Attempts[0].Review = &flow.ReviewRun{Round: 1, Head: l.c1, Code: "sb", RunID: revID}
	})

	// Send-back advances the round, then drop routes straight to N1 cleanup.
	l.report("tok-coord", "task", `{"action":"send-back","task":"T1","why":"rethink approach"}`, "")
	l.report("tok-coord", "task", `{"action":"drop","task":"T1","why":"no longer needed"}`, "")

	// Transient cleanup stop failure leaves reviewer live within back-off.
	var failKill atomic.Bool
	failKill.Store(true)
	l.eng.HC = hc.NewClient(methodFailProxy(t, l.srv.Path, "terminal.kill", &failKill))
	l.pass()
	now = now.Add(30 * time.Second)
	l.pass() // still within 1m back-off

	if len(violations) != 0 {
		t.Fatalf("unexpected invariant violations during cleanup retry: %+v", violations)
	}
	if i1 := logRules(t, l.paths.Log(l.id), "I1"); len(i1) != 0 {
		t.Fatalf("unexpected I1 log lines during cleanup retry: %+v", i1)
	}
	if ws := l.state().Project.Wakes; len(ws) != 0 {
		t.Fatalf("unexpected coordinator wakes during cleanup retry: %+v", ws)
	}
	if act := flow.LastAct(l.state().Tasks[0].Attempts[0].Acts, flow.AskCleanup); act == nil || act.Result != "failed" {
		t.Fatalf("expected recorded failed cleanup act, got %+v", act)
	}

	// After hc recovers and back-off elapses, cleanup stops the reviewer cleanly.
	failKill.Store(false)
	now = now.Add(2 * time.Minute)
	l.pass()
	if len(violations) != 0 || len(logRules(t, l.paths.Log(l.id), "I1")) != 0 || len(l.state().Project.Wakes) != 0 {
		t.Fatalf("unexpected I1 after cleanup recovery: vs=%+v wakes=%+v", violations, l.state().Project.Wakes)
	}
	if rs, _ := termState(ctx, l.client, revID); rs != flow.RunGone {
		t.Fatalf("reviewer after cleanup recovery = %s, want gone", rs)
	}
}

// withoutFactoryChanged drops factory.changed lines: a restart pass commits
// a pr_snapshot observation log line (empty in-memory PR cache), which
// legitimately emits factory.changed (app-factory.md §4); these tests pin
// that violations are not re-logged.
func withoutFactoryChanged(log string) string {
	var keep []string
	for _, l := range strings.SplitAfter(log, "\n") {
		if l != "" && !strings.Contains(l, `"factory.changed"`) {
			keep = append(keep, l)
		}
	}
	return strings.Join(keep, "")
}
