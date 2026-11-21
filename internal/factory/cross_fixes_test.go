package factory

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func TestVerify1CoordinatorIdentity(t *testing.T) {
	ctx := context.Background()
	t.Run("same name", func(t *testing.T) {
		h := newProjectHarness(t, "Goal")
		h.srv.Add(hc.Terminal{Address: "manual", Name: coordName(h.id), State: "live"})
		h.pass()
		h.pass()
		if p := h.state().Project; p.RunID == "manual" || len(h.runs) != 1 {
			t.Errorf("coordinator address=%q launches=%d, want a fresh launch beside the manual terminal", p.RunID, len(h.runs))
		}
		if got := strings.Count(h.logBuf.String(), "factory.coordinator_name_collision"); got != 1 {
			t.Errorf("name collision logged %d times, want once", got)
		}
		if err := h.eng.Stop(""); err != nil {
			t.Fatal(err)
		}
		if _, err := h.client.Get(ctx, "manual"); err != nil {
			t.Errorf("owner stop touched the manual terminal: %v", err)
		}
	})
	t.Run("lost address", func(t *testing.T) {
		h := newProjectHarness(t, "Goal")
		h.pass()
		address := h.state().Project.RunID
		h.srv.Add(hc.Terminal{Address: address, State: "live", Name: "unrelated", Labels: map[string]string{"sheepdog.run": "another-run"}})
		h.pass()
		if r := h.state().Project.Run; r == nil || r.Result != flow.EndLost {
			t.Fatalf("after address reuse = %+v, want R33 lost", r)
		}
		h.now = h.now.Add(time.Hour)
		h.pass()
		if _, err := h.client.Get(ctx, address); err != nil {
			t.Errorf("restart touched the terminal at the lost address: %v", err)
		}
		if len(h.runs) != 2 {
			t.Errorf("launches=%d, want replacement", len(h.runs))
		}
	})
}

func TestVerify2FailedRunStopStaysLiveAndBacksOff(t *testing.T) {
	h := newProjectHarness(t, "Goal")
	h.pass()
	addr := h.state().Project.RunID
	var failKill atomic.Bool
	failKill.Store(true)
	h.eng.HC = hc.NewClient(methodFailProxy(t, h.srv.Path, "terminal.kill", &failKill))
	h.srv.SetStatus(addr, &hc.Status{State: "blocked", App: "claude-code"})
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Done: &flow.Merged{}}}
	})
	h.pass()
	h.now = h.now.Add(time.Duration(h.state().Settings.BlockedMinutes) * time.Minute)
	h.pass()
	if p := h.state().Project; p.Run.State == flow.PhaseEnded || p.RunID != addr {
		t.Errorf("failed kill persisted state=%s run_id=%q, want a live run", p.Run.State, p.RunID)
	}
	if lines := logRules(t, h.paths.Log(h.id), "J4"); len(lines) != 0 {
		t.Errorf("J4 took a finished action after failed kill: %+v", lines)
	}
	before := countRPC(h.srv, "terminal.get")
	h.pass()
	// Each kill first gets the terminal, in addition to the observer's get.
	if got := countRPC(h.srv, "terminal.get") - before; got != 1 {
		t.Errorf("next pass made %d get calls, want only the observer during back-off", got)
	}
	h.eng.HC = h.client
	h.now = h.now.Add(time.Duration(h.state().Settings.EnsureBackoffMinutes) * time.Minute)
	h.pass()
	if _, err := h.client.Get(context.Background(), addr); !errors.Is(err, hc.ErrNotFound) {
		t.Errorf("stop after back-off did not remove terminal: %v", err)
	}
}

func TestVerify9RestartStopFailureRecordsAct(t *testing.T) {
	l := newLanding(t)
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	l.eng.Now = func() time.Time { return now }
	l.srv.Add(hc.Terminal{Address: "old-worker", Name: workerName(l.id, "T1", 1), State: "live"})
	var failKill atomic.Bool
	failKill.Store(true)
	l.eng.HC = hc.NewClient(methodFailProxy(t, l.srv.Path, "terminal.kill", &failKill))
	l.edit(func(s *flow.State) {
		s.Settings.EnsureFailures = 2
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds, a.RunID = nil, nil, ""
		a.Run = &flow.RunRecord{State: flow.PhaseEnded, Result: flow.EndStopped, Role: flow.RoleWorker}
	})
	const k = "start_worker@handoff"
	l.pass()
	a := l.state().Tasks[0].Attempts[0]
	if f := flow.LastAct(a.Acts, k); f == nil || f.Result != "failed" {
		t.Fatalf("stop before restart: acts=%+v, want failed %s", a.Acts, k)
	}
	l.pass()
	if a := l.state().Tasks[0].Attempts[0]; flow.Count(a.Acts, k) != 1 {
		t.Fatalf("restart during back-off: acts=%+v", a.Acts)
	}
	now = now.Add(time.Duration(l.state().Settings.EnsureBackoffMinutes) * time.Minute)
	l.pass()
	l.pass()
	if task := l.state().Tasks[0]; task.LastRow != "N5" || !flow.Done(task.Attempts[0].Acts, "wake_coordinator@failed@"+k) {
		t.Errorf("after repeated stop failure: row=%s acts=%+v, want N5 wake", task.LastRow, task.Attempts[0].Acts)
	}
}

func TestVerify13ReviewerUsesLaunchedWorkerModel(t *testing.T) {
	l := newLanding(t)
	s := l.state()
	tk := &s.Tasks[0]
	tk.Request = map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "claude", Model: "opus", Effort: "medium"}}}
	a := &tk.Attempts[0]
	a.Run, a.RunID, a.Review = nil, "", nil
	ctx := context.Background()
	if _, _, err := l.eng.execStartWorker(ctx, l.id, &s, 0, flow.Facts{}); err != nil {
		t.Fatal(err)
	}
	worker, err := l.client.Get(ctx, a.RunID)
	if err != nil || worker.Labels["sheepdog.model"] != "opus" {
		t.Fatalf("worker model=%q err=%v, want opus", worker.Labels["sheepdog.model"], err)
	}
	delete(tk.Request, flow.RoleWorker) // Changing the request doesn't change the worker that did the work.
	if _, _, err := l.eng.execStartReviewer(ctx, l.id, &s, 0, flow.Facts{}); err != nil {
		t.Fatal(err)
	}
	if got := a.Review.Reviewer.Model; got != "sonnet" {
		t.Errorf("reviewer model=%q for launched opus worker, want sonnet", got)
	}
	s.Settings.Models = map[string][]flow.Candidate{flow.RoleReviewer: {{CLI: "claude", Model: "opus", Effort: "medium"}}}
	if fails := checkTasksLaunch(s.Tasks, s.Settings, flow.UnprobedFacts); len(fails) != 1 || !strings.Contains(fails[0], "the reviewer must differ from the worker") {
		t.Errorf("launch check with only the stored worker's model available for review = %v, want independence error", fails)
	}
}

func countRPC(s *hctest.Server, method string) int {
	n := 0
	for _, req := range s.Requests() {
		if req.Method == method {
			n++
		}
	}
	return n
}
