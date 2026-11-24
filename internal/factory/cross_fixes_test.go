package factory

import (
	"context"
	"errors"
	"os"
	"slices"
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

func TestJ7RestartStopRecoversAndDeliversWake(t *testing.T) {
	h, failKill := restartStopHarness(t)
	const wakeKey = "failed@" + flow.AskStopCoordinator
	stopAttempts := func() int { return strings.Count(h.logBuf.String(), "terminal.kill failed") }
	backoffs := []time.Duration{1, 2, 4, 8, 16, 30}
	for i, minutes := range backoffs {
		h.pass()
		if got := stopAttempts(); got != i+1 {
			t.Errorf("try %d at back-off deadline: got %d failed stops in total, want %d", i+1, got, i+1)
		}
		if i == 5 {
			failKill.Store(false)
		}
		delay := minutes * time.Minute
		h.now = h.now.Add(delay - time.Second)
		h.pass()
		if got := stopAttempts(); got > i+1 || len(h.runs) != 0 {
			t.Errorf("try %d before %s back-off elapsed: got %d failed stops and %d launches", i+1, delay, got, len(h.runs))
		}
		if i >= 4 {
			wakes := slices.DeleteFunc(h.state().Project.Wakes, func(w flow.Wake) bool { return w.Key != wakeKey })
			if len(wakes) != 1 {
				t.Errorf("after %d failed stops: got %d %s wakes, want one", i+1, len(wakes), wakeKey)
			}
		}
		h.now = h.now.Add(time.Second)
	}
	if kills := countRPC(h.srv, "terminal.kill"); kills != 0 {
		t.Errorf("before capped back-off elapsed: kills=%d, want 0", kills)
	}
	h.pass()
	coord := h.state().Project.RunID
	if len(h.runs) != 1 || coord == "" {
		t.Fatalf("after six failed stops and recovery: launches=%d run_id=%q, want a replacement", len(h.runs), coord)
	}
	h.coordIdleWithStatus(coord, "idle")
	h.pass()
	if sends := sendsTo(t, h.srv.Requests(), coord); len(sends) != 1 || !strings.Contains(sends[0], wakeKey) {
		t.Errorf("replacement coordinator sends = %q, want one %s", sends, wakeKey)
	}
	if wakes := h.state().Project.Wakes; len(wakes) != 0 {
		t.Errorf("wakes still held after delivery: %+v", wakes)
	}
}

func TestCoordinatorStopsDoNotCountOutages(t *testing.T) {
	for _, row := range []string{"J4", "J7", "J8", "J9"} {
		t.Run(row, func(t *testing.T) {
			h, _ := restartStopHarness(t)
			s := h.state()
			if row != "J7" {
				s.Project.Run.State, s.Project.RunID = flow.PhaseIdle, s.Project.Run.Address
				switch row {
				case "J4":
					s.Tasks = []flow.Task{{ID: "T1", Done: &flow.Merged{}}}
					s.Project.Acts = append(s.Project.Acts, flow.Act{Key: flow.FinalJournalKey(s.Tasks), Result: "done"})
				case "J8":
					s.Project.Run.Status, s.Project.Run.Turn = "error", 1
					s.Project.CoordErrorAt = h.now.Add(-time.Second)
				case "J9":
					s.Project.CoordWakeAt = h.now.Add(-time.Hour)
				}
			}
			h.eng.hcUnavail = map[string]bool{h.id: true}
			const key = flow.AskStopCoordinator
			for i := range 20 {
				r, d := flow.FirstMatch(flow.ProjectRows, flow.ProjectIn{Project: s.Project, Tasks: s.Tasks, Settings: s.Settings, Now: h.now})
				if r.ID != row {
					t.Errorf("try %d: row=%s why=%q, want %s", i+1, r.ID, d.Why, row)
					break
				}
				h.eng.passProjectRulesLocked(context.Background(), h.id, &s, h.now)
				if got := flow.Count(s.Project.Acts, key); got != i+1 {
					t.Errorf("try %d: %d failed stops, want %d", i+1, got, i+1)
					break
				}
				h.now = h.now.Add(flow.EnsureBackoff(s.Settings, i+1))
				if row == "J8" {
					s.Project.CoordErrorAt = h.now.Add(-time.Second)
				}
			}
			last := flow.LastAct(s.Project.Acts, key)
			if last == nil || !last.Uncounted || flow.Exhausted(s.Project.Acts, key, s.Settings) || len(s.Project.Wakes) != 0 {
				t.Errorf("outage stops: last=%+v exhausted=%v wakes=%+v, want uncounted failures and no give-up", last, flow.Exhausted(s.Project.Acts, key, s.Settings), s.Project.Wakes)
			}
		})
	}
}

func TestJ7RestartStopHoldWhyLogsOncePerChange(t *testing.T) {
	h, fail := restartStopHarness(t)
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Parked: true}}
	})
	h.pass()
	s := h.state()
	in := flow.ProjectIn{Project: s.Project, Tasks: s.Tasks, Settings: s.Settings, Now: h.now}
	for _, row := range []string{"J7", "J6"} {
		if row == "J6" {
			in.Acts = append(in.Acts, flow.Act{Key: flow.AskStartCoordinator, Result: "done", At: h.now})
		}
		r, d := flow.FirstMatch(flow.ProjectRows, in)
		if r.ID != row || !strings.Contains(d.Why, "could not stop the old coordinator") || !strings.Contains(d.Why, "terminal.kill failed") {
			t.Errorf("project why = (%s, %q), want %s with the old coordinator stop error", r.ID, d.Why, row)
		}
	}
	h.pass()
	h.now = h.now.Add(time.Minute)
	h.pass()
	h.pass()
	reportPath := h.paths.Log(h.id)
	if lines := append(logRules(t, reportPath, "J7"), logRules(t, reportPath, "J6")...); len(lines) != 1 ||
		!strings.Contains(lines[0].Why, "could not stop the old coordinator") || !strings.Contains(lines[0].Why, "terminal.kill failed") {
		t.Errorf("repeated stop error log = %+v, want one line with the stop error", lines)
	}
	h.eng.HC = hc.NewClient(methodFailProxy(t, h.srv.Path, "terminal.get", fail))
	h.now = h.now.Add(2 * time.Minute)
	h.pass()
	h.pass()
	if lines := append(logRules(t, reportPath, "J7"), logRules(t, reportPath, "J6")...); len(lines) != 2 ||
		!strings.Contains(lines[1].Why, "terminal.get failed") {
		t.Errorf("changed stop error log = %+v, want a second line with the new error", lines)
	}
}

func TestJ7RestartStopBackoffResetsAfterSuccess(t *testing.T) {
	h, _ := restartStopHarness(t)
	const key = flow.AskStopCoordinator
	h.edit(func(s *flow.State) {
		s.Project.Acts = []flow.Act{
			{Key: key, Result: "failed", At: h.now.Add(-3 * time.Hour)},
			{Key: key, Result: "done", At: h.now.Add(-2 * time.Hour)},
			{Key: key, Result: "failed", At: h.now},
		}
	})
	h.now = h.now.Add(time.Minute)
	h.pass()
	if got := strings.Count(h.logBuf.String(), "terminal.kill failed"); got != 1 {
		t.Errorf("at the first retry deadline after success: got %d stop attempts, want 1", got)
	}
}

func restartStopHarness(t *testing.T) (*projectHarness, *atomic.Bool) {
	t.Helper()
	h := newProjectHarness(t, "Goal")
	h.srv.Add(hc.Terminal{Address: "old-coord", Name: coordName(h.id), State: "live", Labels: testCoordLabels(h.id, "old-run")})
	failKill := new(atomic.Bool)
	failKill.Store(true)
	h.eng.HC = hc.NewClient(methodFailProxy(t, h.srv.Path, "terminal.kill", failKill))
	h.edit(func(s *flow.State) {
		s.Settings.EnsureFailures, s.Settings.EnsureBackoffMinutes = 5, 1
		s.Project.Run = &flow.RunRecord{ID: "old-run", Address: "old-coord", State: flow.PhaseEnded, Result: flow.EndLost}
	})
	return h, failKill
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
	if fails := checkTasksLaunch(s.Tasks, s.Settings); len(fails) != 1 || !strings.Contains(fails[0], "the reviewer must differ from the worker") {
		t.Errorf("launch check with only the stored worker's model available for review = %v, want independence error", fails)
	}
}

func TestTaskAbortRecordsFailureAndBacksOff(t *testing.T) {
	for _, name := range []string{"worker get", "worker state save", "reviewer get"} {
		t.Run(name, func(t *testing.T) {
			l := newLanding(t)
			s := l.state()
			a := &s.Tasks[0].Attempts[0]
			a.Rounds = nil
			now := time.Now()
			l.eng.Now = func() time.Time { return now }
			key, wantErr := flow.AskStartWorker, "terminal.get failed"
			var worker flow.Run
			if name == "reviewer get" {
				key = flow.AskStartReview + "@1@" + l.c1
				worker.State = flow.RunIdle
				// Exercise the uncounted back-off used during a known hc outage.
				l.eng.hcUnavail = map[string]bool{l.id: true}
			} else {
				a.Ready, a.RunID = nil, ""
			}
			if err := SaveState(l.paths.State(l.id), s); err != nil {
				t.Fatal(err)
			}
			if name == "worker state save" {
				path := l.paths.State(l.id)
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				logsBefore := len(l.logLines())
				runCalls := countRPC(l.srv, "terminal.run")
				pass := func() {
					baseTried := false
					if evaluated := l.eng.passTaskLocked(context.Background(), l.id, &s, 0, true, worker, nil, &baseTried, 0); !evaluated {
						t.Error("task evaluated a row but was reported as skipped for I1")
					}
				}
				pass()
				if got := countRPC(l.srv, "terminal.run"); got != runCalls {
					t.Errorf("failed pre-launch save launched a terminal")
				}
				if got := l.log.String(); !strings.Contains(got, "factory.commit_error") || !strings.Contains(got, "rename") {
					t.Errorf("missing factory.commit_error for %s: %q", name, got)
				}
				// S2: after the pre-launch state save failed, no further log or state write happens in that pass.
				if got := len(l.logLines()); got != logsBefore {
					t.Errorf("log.jsonl grew after failed pre-launch save (%d -> %d); S2 forbids writes while dirty", logsBefore, got)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".original", path); err != nil {
					t.Fatal(err)
				}
				if act := flow.LastAct(l.state().Tasks[0].Attempts[0].Acts, key); act != nil {
					t.Errorf("state.json recorded act %+v after failed pre-launch save, want none", act)
				}
				// Next pass recovers the dirty store and launches the worker cleanly.
				l.pass()
				if got := countRPC(l.srv, "terminal.run"); got != runCalls+1 {
					t.Errorf("terminal runs after recovery pass = %d, want %d", got, runCalls+1)
				}
				return
			}
			var failGet atomic.Bool
			failGet.Store(true)
			l.eng.HC = hc.NewClient(methodFailProxy(t, l.srv.Path, "terminal.get", &failGet))

			runCalls := countRPC(l.srv, "terminal.run")
			pass := func() {
				baseTried := false
				if evaluated := l.eng.passTaskLocked(context.Background(), l.id, &s, 0, true, worker, nil, &baseTried, 0); !evaluated {
					t.Error("task evaluated a row but was reported as skipped for I1")
				}
			}
			pass()
			s = l.state()
			act := flow.LastAct(s.Tasks[0].Attempts[0].Acts, key)
			if act == nil || act.Result != "failed" || act.Class != flow.ClassTransient || act.Uncounted != (name == "reviewer get") {
				t.Errorf("persisted %s act = %+v, want transient failure (uncounted=%t)", key, act, name == "reviewer get")
			}
			if got := l.log.String(); !strings.Contains(got, "factory.exec_error") || !strings.Contains(got, wantErr) {
				t.Errorf("missing factory.exec_error for %s", name)
			}
			lines := l.logLines()
			if len(lines) == 0 || !slices.Equal(lines[len(lines)-1].Asked, []string{key}) || !strings.Contains(lines[len(lines)-1].Why, wantErr) {
				t.Errorf("missing committed failure reason for %s", key)
			}
			if got := countRPC(l.srv, "terminal.run"); got != runCalls {
				t.Errorf("failed pass launched a terminal")
			}
			failGet.Store(false)
			now = now.Add(time.Second)
			pass()
			if got := countRPC(l.srv, "terminal.run"); got != runCalls {
				t.Errorf("retried %s before back-off elapsed", key)
			}
			now = now.Add(time.Duration(s.Settings.EnsureBackoffMinutes) * time.Minute)
			pass()
			if got := countRPC(l.srv, "terminal.run"); got != runCalls+1 {
				t.Errorf("terminal runs after back-off = %d, want %d", got, runCalls+1)
			}
		})
	}
}

func TestTaskAbortCommitsEarlierAsk(t *testing.T) {
	l := newLanding(t)
	s := l.state()
	a := &s.Tasks[0].Attempts[0]
	a.Ready, a.Rounds, a.RunID = nil, nil, ""
	var failGet atomic.Bool
	failGet.Store(true)
	l.eng.HC = hc.NewClient(methodFailProxy(t, l.srv.Path, "terminal.get", &failGet))

	const earlier, later = flow.AskWake + "@before_start", flow.AskWake + "@after_start"
	// Give N23 a batch with a wake on either side of the real start executor.
	i := slices.IndexFunc(flow.TaskRows, func(row flow.Row[flow.In]) bool { return row.ID == "N23" })
	then := flow.TaskRows[i].Then
	t.Cleanup(func() { flow.TaskRows[i].Then = then })
	flow.TaskRows[i].Then = func(in flow.In) flow.Decision {
		d := then(in)
		d.Asked = append([]string{earlier}, d.Asked...)
		d.Asked = append(d.Asked, later)
		return d
	}
	baseTried := false
	l.eng.passTaskLocked(context.Background(), l.id, &s, 0, true, flow.Run{}, nil, &baseTried, 0)
	s = l.state()
	wakes := s.Project.Wakes
	if len(wakes) != 1 || wakes[0].Key != "before_start" || !flow.Done(s.Tasks[0].Attempts[0].Acts, earlier) {
		t.Errorf("persisted wakes = %+v, want only the earlier ask's wake and done act", wakes)
	}
	lines := l.logLines()
	if want := []string{earlier, flow.AskStartWorker}; len(lines) == 0 || !slices.Equal(lines[len(lines)-1].Asked, want) {
		t.Errorf("missing committed batch ending at the failed ask: want %v", want)
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
