package factory

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func init() {
	onInvariantBreak = func(project string, vs []flow.InvariantViolation) {
		panic(fmt.Sprintf("factory invariant broken for %s: %+v", project, vs))
	}
}

// TestInvariantProductionLogOnceAndWakeCoordinator verifies design.md section 8:
// in production, a broken invariant is logged once per change as an error line
// (rule I1..I5, reason), wakes the coordinator once (held and delivered via J12),
// does not attempt repair, does not re-wake across restarts for the same reason,
// and logs/wakes again if the violation changes.
func TestInvariantProductionLogOnceAndWakeCoordinator(t *testing.T) {
	l := newLanding(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	// Production behavior: log and wake, never panic/fail the test.
	l.eng.onInvariant = func([]flow.InvariantViolation) {}

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
	if strings.Count(l.log.String(), "factory.invariant") != 2 {
		t.Fatalf("daemon log invariant events = %d, want 2:\n%s", strings.Count(l.log.String(), "factory.invariant"), l.log.String())
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
		strings.Count(l.log.String(), "factory.invariant") != 2 || len(l.state().Project.Wakes) != 2 {
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

	// Restarted daemon with the same violations still present must NOT re-wake the coordinator.
	var restartLog bytes.Buffer
	eng2 := New(l.paths, l.client)
	eng2.Log = logging.NewJSONLLogger(&restartLog)
	eng2.Now = func() time.Time { return now }
	eng2.onInvariant = func([]flow.InvariantViolation) {}
	eng2.passProject(ctx, l.id, true)
	if len(l.state().Project.Wakes) != 0 {
		t.Fatalf("restarted daemon re-woke coordinator for already-woken invariant: %+v", l.state().Project.Wakes)
	}
	if strings.Count(restartLog.String(), "factory.invariant") != 2 {
		t.Fatalf("restarted daemon did not log active violations once:\n%s", restartLog.String())
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
	if len(logRules(t, l.paths.Log(l.id), "I4")) != 3 { // initial + restart + new violation
		t.Fatalf("I4 log lines after new violation = %+v, want 3", logRules(t, l.paths.Log(l.id), "I4"))
	}
	if ws := l.state().Project.Wakes; len(ws) != 1 || !strings.Contains(ws[0].Why, "deadbeef") {
		t.Fatalf("wakes after new I4 violation = %+v, want 1 naming deadbeef", ws)
	}
}

// TestPRDivergedVoidsLiveReviewer verifies that a force-pushed PR head stops an
// in-flight reviewer in the same pass so I1 holds under N7.
func TestPRDivergedVoidsLiveReviewer(t *testing.T) {
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
