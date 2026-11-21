package factory

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func testCoordLabels(id, run string) map[string]string {
	return map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.run": run}
}

type projectHarness struct {
	t      *testing.T
	srv    *hctest.Server
	client *hc.Client
	paths  Paths
	repo   string
	eng    *Engine
	logBuf *bytes.Buffer
	id     string
	now    time.Time
	runs   []hc.RunParams
}

func newProjectHarness(t *testing.T, goal string) *projectHarness {
	t.Helper()
	srv := hctest.New(t)
	h := &projectHarness{
		t: t, srv: srv, client: hc.NewClient(srv.Path),
		paths: testPaths(t), repo: initGitRepo(t),
		logBuf: &bytes.Buffer{},
		now:    time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC),
	}
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { h.runs = append(h.runs, p) }
	h.eng = New(h.paths, h.client)
	h.eng.Log = logging.NewJSONLLogger(h.logBuf)
	h.eng.Now = func() time.Time { return h.now }
	if err := h.eng.Start(h.repo, goal, 2); err != nil {
		t.Fatal(err)
	}
	h.id = h.eng.projectDirs()[0].Name()
	return h
}

func (h *projectHarness) pass() { h.eng.passProject(context.Background(), h.id, false) }

func (h *projectHarness) state() flow.State {
	h.t.Helper()
	s, err := LoadState(h.paths.State(h.id))
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

func (h *projectHarness) edit(fn func(*flow.State)) {
	h.t.Helper()
	s := h.state()
	fn(&s)
	if err := SaveState(h.paths.State(h.id), s); err != nil {
		h.t.Fatal(err)
	}
}

func (h *projectHarness) coordIdleWithStatus(addr, status string) {
	h.srv.SetStatus(addr, &hc.Status{App: "claude-code", State: status})
	h.now = h.now.Add(5 * time.Second)
	h.pass() // R9/R17 records StatusSince
	h.now = h.now.Add(25 * time.Second)
	h.pass() // R20 -> PhaseIdle
}

// TestJ4NoRestartAfterLastTaskEnds verifies J4: once a plan exists and every
// task has ended, an idle coordinator is asked for a final journal line, then
// stopped and never restarted; a coordinator already gone is not restarted; and
// a new owner goal resumes J7.
func TestJ4NoRestartAfterLastTaskEnds(t *testing.T) {
	h := newProjectHarness(t, "Goal 1")
	h.pass() // J7 starts coord 1
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: done", Done: &flow.Merged{MergedCommit: "m1"}}}
	})

	// Idle coordinator -> J4 asks for final journal line.
	h.coordIdleWithStatus(coord1, "idle")
	sends := sendsTo(t, h.srv.Requests(), coord1)
	if len(sends) != 1 || !strings.Contains(strings.ToLower(sends[0]), "final journal") {
		t.Fatalf("J4 final journal send = %q, want 1 prompt asking for final journal line", sends)
	}
	if _, err := h.client.Get(context.Background(), coord1); err != nil {
		t.Fatalf("coordinator stopped before finishing final journal turn: %v", err)
	}

	// Coordinator finishes its final journal turn and goes idle -> J4 stops it.
	h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
	h.now = h.now.Add(5 * time.Second)
	h.pass()
	h.coordIdleWithStatus(coord1, "idle")
	if _, err := h.client.Get(context.Background(), coord1); !errors.Is(err, hc.ErrNotFound) {
		t.Fatalf("coordinator not stopped after final journal line: err = %v", err)
	}

	// Even well past coordinator_gap_minutes, J4 never restarts the coordinator.
	h.now = h.now.Add(2 * time.Hour)
	for range 3 {
		h.pass()
	}
	if len(h.runs) != 1 || h.state().Project.RunID != "" {
		t.Fatalf("J4 restarted coordinator after all tasks ended: runs=%d runID=%q", len(h.runs), h.state().Project.RunID)
	}
	if j4 := logRules(t, h.paths.Log(h.id), "J4"); len(j4) != 3 || j4[2].Decision != "finished" {
		t.Fatalf("J4 log lines = %+v, want 3 (ask final journal, stop, finished)", j4)
	}

	// New owner goal clears J4's finished hold and J7 starts a coordinator.
	if err := h.eng.Start(h.repo, "Goal 2", 2); err != nil {
		t.Fatal(err)
	}
	h.pass()
	if len(h.runs) != 2 || h.state().Project.RunID == "" {
		t.Fatalf("new goal did not start coordinator: runs=%d runID=%q", len(h.runs), h.state().Project.RunID)
	}
}

// TestJ6WaitsForGapAndHourlyCap verifies J6: a restart waits for
// coordinator_gap_minutes since the last start, and waits when
// coordinator_starts_per_hour is reached until the oldest start leaves the hour.
func TestJ6WaitsForGapAndHourlyCap(t *testing.T) {
	h := newProjectHarness(t, "J6 goal")
	h.pass() // start #1 at t=0
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open"}}
	})

	// Kill start #1 at t=2m; before 10m gap elapses, J6 waits.
	h.srv.Finish(h.state().Project.RunID, "killed")
	h.now = h.now.Add(2 * time.Minute)
	h.pass()                                          // R33 -> ended, J6 waits
	h.now = h.now.Add(7*time.Minute + 59*time.Second) // t=9m59s
	h.pass()
	if len(h.runs) != 1 || h.state().Project.RunID != "" {
		t.Fatalf("restarted before coordinator_gap_minutes: runs=%d runID=%q", len(h.runs), h.state().Project.RunID)
	}

	// At t=10m, J7 starts #2.
	h.now = h.now.Add(time.Second)
	h.pass()
	if len(h.runs) != 2 || h.state().Project.RunID == "" {
		t.Fatalf("did not restart at coordinator_gap_minutes: runs=%d runID=%q", len(h.runs), h.state().Project.RunID)
	}

	// Kill #2 at t=12m -> waits until t=20m -> J7 starts #3.
	h.srv.Finish(h.state().Project.RunID, "killed")
	h.now = h.now.Add(2 * time.Minute)
	h.pass()
	h.now = h.now.Add(8 * time.Minute) // t=20m
	h.pass()
	if len(h.runs) != 3 {
		t.Fatalf("expected 3 starts at t=20m, got %d", len(h.runs))
	}

	// Kill #3 at t=35m (gap 15m >= 10m, but 3 starts in last hour = coordinator_starts_per_hour).
	h.srv.Finish(h.state().Project.RunID, "killed")
	h.now = h.now.Add(15 * time.Minute) // t=35m
	h.pass()
	h.now = h.now.Add(24*time.Minute + 59*time.Second) // t=59m59s
	h.pass()
	if len(h.runs) != 3 || h.state().Project.RunID != "" {
		t.Fatalf("restarted while at coordinator_starts_per_hour cap: runs=%d", len(h.runs))
	}

	// At t=60m, start #1 leaves the 1h window -> J7 starts #4.
	h.now = h.now.Add(time.Second) // t=60m
	h.pass()
	if len(h.runs) != 4 || h.state().Project.RunID == "" {
		t.Fatalf("did not restart after hourly window cleared: runs=%d runID=%q", len(h.runs), h.state().Project.RunID)
	}
}

// TestJ8ErrorTellThenStopAndRestart verifies J8: a turn-end error tells the
// coordinator to go on the first time, does not re-fire on the same turn, and
// stops it on a second error within quiet_minutes so J7 restarts it.
func TestJ8ErrorTellThenStopAndRestart(t *testing.T) {
	h := newProjectHarness(t, "J8 goal")
	h.pass() // J7 start #1
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) { s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open"}} })

	// Turn 1 ends with error -> J8 tells coordinator to go on.
	h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
	h.now = h.now.Add(5 * time.Second)
	h.pass()
	h.coordIdleWithStatus(coord1, "error")
	sends := sendsTo(t, h.srv.Requests(), coord1)
	if len(sends) != 1 || !strings.Contains(strings.ToLower(sends[0]), "go on") {
		t.Fatalf("J8 1st error sends = %q, want 1 go-on tell", sends)
	}
	if h.state().Project.RunID != coord1 {
		t.Fatalf("J8 stopped coordinator on first error")
	}

	// Another pass with no new turn must not treat the same error as a second error.
	h.now = h.now.Add(30 * time.Second)
	h.pass()
	if h.state().Project.RunID != coord1 || len(sendsTo(t, h.srv.Requests(), coord1)) != 1 {
		t.Fatalf("J8 re-fired on same error turn: runID=%q sends=%d", h.state().Project.RunID, len(sendsTo(t, h.srv.Requests(), coord1)))
	}

	// Turn 2 ends with error within quiet_minutes (5m later) -> J8 stops coordinator.
	h.now = h.now.Add(5 * time.Minute)
	h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
	h.pass()
	h.coordIdleWithStatus(coord1, "error")
	if h.state().Project.RunID != "" {
		t.Fatalf("J8 did not stop coordinator on second error within quiet_minutes")
	}
	if _, err := h.client.Get(context.Background(), coord1); !errors.Is(err, hc.ErrNotFound) {
		t.Fatalf("stopped coordinator terminal still present: err = %v", err)
	}
	if j8 := logRules(t, h.paths.Log(h.id), "J8"); len(j8) != 2 {
		t.Fatalf("J8 log lines = %+v, want 2 (tell, stop)", j8)
	}

	// Past coordinator_gap_minutes -> J7 restarts coordinator.
	h.now = h.now.Add(10 * time.Minute)
	h.pass()
	if len(h.runs) != 2 || h.state().Project.RunID == "" || h.state().Project.RunID == coord1 {
		t.Fatalf("J7 did not restart after J8 stop: runs=%d runID=%q", len(h.runs), h.state().Project.RunID)
	}
}

// TestJ9SilenceStopsAndJ7Restarts verifies J9: delivered wakes with no turn
// while the coordinator remains idle for coordinator_silent_minutes stop the
// coordinator so J7 restarts it.
func TestJ9SilenceStopsAndJ7Restarts(t *testing.T) {
	h := newProjectHarness(t, "J9 wakes")
	h.pass() // J7 start #1
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open"}}
		s.Project.Wakes = []flow.Wake{{Key: "worker_failed", Task: "T1", Why: "N23", Facts: "title x", At: h.now}}
	})
	h.coordIdleWithStatus(coord1, "idle") // J12 delivers wake, records CoordWakeAt
	if len(sendsTo(t, h.srv.Requests(), coord1)) != 1 || h.state().Project.CoordWakeAt.IsZero() {
		t.Fatalf("J12 did not deliver wake and arm J9: wakeAt=%v", h.state().Project.CoordWakeAt)
	}
	// Coordinator stays idle in hc (never starts working) until 30m elapses.
	h.now = h.now.Add(15 * time.Minute)
	h.pass() // R17 sees idle status
	h.now = h.now.Add(14*time.Minute + 59*time.Second)
	h.pass() // R20 -> PhaseIdle, 29m59s < 30m
	if h.state().Project.RunID != coord1 {
		t.Fatalf("J9 stopped coordinator before coordinator_silent_minutes")
	}
	h.now = h.now.Add(time.Second) // 30m since wake delivered
	h.pass()                       // J9 stops coordinator
	if h.state().Project.RunID != "" || len(logRules(t, h.paths.Log(h.id), "J9")) != 1 {
		t.Fatalf("J9 did not stop silent coordinator: runID=%q j9=%+v", h.state().Project.RunID, logRules(t, h.paths.Log(h.id), "J9"))
	}
	h.pass() // 30m >= gap 10m -> J7 restarts immediately
	if len(h.runs) != 2 || h.state().Project.RunID == "" {
		t.Fatalf("J7 did not restart after J9: runs=%d runID=%q", len(h.runs), h.state().Project.RunID)
	}
}

// TestJ10DialogStuckRestartThenPlainLogSecondTime verifies J10: R29 dialog-stuck
// ends are restarted by J7; the second time in a row writes a plain owner log
// line (daemon event + log.jsonl); a normal exit resets the streak.
func TestJ10DialogStuckRestartThenPlainLogSecondTime(t *testing.T) {
	h := newProjectHarness(t, "J10 goal")
	h.pass() // J7 start #1
	h.edit(func(s *flow.State) { s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open"}} })

	perm := "permission"
	// 1st R29 dialog stuck -> J10 notes count=1, J7 restarts (gap 10m == blocked_minutes 10m).
	c1 := h.state().Project.RunID
	h.srv.SetStatus(c1, &hc.Status{App: "claude-code", State: "blocked", Kind: &perm, Msg: "Allow tool?"})
	h.now = h.now.Add(5 * time.Second)
	h.pass() // R15 -> PhaseBlocked
	h.now = h.now.Add(10 * time.Minute)
	h.pass() // R29 -> ended(stuck) -> J10 (1st) -> J7 starts #2
	if len(h.runs) != 2 || h.state().Project.CoordDialogStucks != 1 {
		t.Fatalf("after 1st R29: runs=%d stucks=%d", len(h.runs), h.state().Project.CoordDialogStucks)
	}
	if strings.Contains(h.logBuf.String(), "factory.coordinator_stuck") {
		t.Fatalf("logged owner alert on 1st R29: %s", h.logBuf.String())
	}

	// 2nd R29 dialog stuck in a row -> J10 logs plainly for owner, J7 restarts #3.
	c2 := h.state().Project.RunID
	h.srv.SetStatus(c2, &hc.Status{App: "claude-code", State: "blocked", Kind: &perm, Msg: "Allow tool again?"})
	h.now = h.now.Add(5 * time.Second)
	h.pass()
	h.now = h.now.Add(10 * time.Minute)
	h.pass() // R29 -> J10 (2nd: plain owner log) -> J7 starts #3
	if len(h.runs) != 3 || h.state().Project.CoordDialogStucks != 2 {
		t.Fatalf("after 2nd R29: runs=%d stucks=%d", len(h.runs), h.state().Project.CoordDialogStucks)
	}
	if !strings.Contains(h.logBuf.String(), "factory.coordinator_stuck") {
		t.Fatalf("missing plain owner log on 2nd R29: %s", h.logBuf.String())
	}
	j10 := logRules(t, h.paths.Log(h.id), "J10")
	if len(j10) != 2 || !strings.Contains(j10[1].Why, "twice in a row") {
		t.Fatalf("J10 log lines = %+v, want 2 with 2nd naming twice in a row", j10)
	}

	// A non-R29 exit resets the consecutive dialog-stuck counter.
	h.srv.Finish(h.state().Project.RunID, "exited")
	h.now = h.now.Add(time.Minute)
	h.pass()
	if h.state().Project.CoordDialogStucks != 0 {
		t.Fatalf("CoordDialogStucks not reset after non-R29 exit: %d", h.state().Project.CoordDialogStucks)
	}
}

// TestJ13HeartbeatAfterIntervalOnlyWhenIdle verifies J13: no heartbeat fires
// while the coordinator is working or before heartbeat_minutes; once idle past
// heartbeat_minutes, a heartbeat wake carries open task status and non-default settings.
func TestJ13HeartbeatAfterIntervalOnlyWhenIdle(t *testing.T) {
	h := newProjectHarness(t, "J13 goal")
	h.pass() // J7 start #1 at t=0
	coord := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Settings.HeartbeatMinutes = 60
		s.Settings.ReviewRounds = 3
		s.Tasks = []flow.Task{
			{ID: "T0", Title: "feat: already merged", Done: &flow.Merged{MergedCommit: "m0"}},
			{ID: "T1", Title: "feat: in progress", LastRow: "N23", Attempts: []flow.Attempt{{BaseCommit: "b0", RunID: "w1"}}},
		}
	})

	// Working past heartbeat_minutes -> no heartbeat.
	h.srv.SetStatus(coord, &hc.Status{App: "claude-code", State: "working"})
	h.now = h.now.Add(70 * time.Minute)
	h.pass()
	if got := sendsTo(t, h.srv.Requests(), coord); len(got) != 0 {
		t.Fatalf("heartbeat sent while coordinator working: %q", got)
	}

	// Goes idle -> J13 fires with open task status and non-default setting.
	h.coordIdleWithStatus(coord, "idle")
	sends := sendsTo(t, h.srv.Requests(), coord)
	if len(sends) != 1 {
		t.Fatalf("sends after idle past heartbeat_minutes = %q, want 1", sends)
	}
	for _, want := range []string{"heartbeat", "1 open, 1 ended", "T1", "N23", "review_rounds 3"} {
		if !strings.Contains(sends[0], want) {
			t.Errorf("heartbeat wake missing %q:\n%s", want, sends[0])
		}
	}
	if strings.Contains(sends[0], "- T0") {
		t.Errorf("heartbeat listed ended task T0 as open:\n%s", sends[0])
	}

	// Take a turn (clears J9 silence), stay idle 59m -> no second heartbeat; at 60m -> second heartbeat.
	h.srv.SetStatus(coord, &hc.Status{App: "claude-code", State: "working"})
	h.now = h.now.Add(time.Minute)
	h.pass()
	h.coordIdleWithStatus(coord, "idle")
	h.now = h.now.Add(58 * time.Minute) // < 60m since last heartbeat
	h.pass()
	if len(sendsTo(t, h.srv.Requests(), coord)) != 1 {
		t.Fatalf("second heartbeat fired early: %d", len(sendsTo(t, h.srv.Requests(), coord)))
	}
	h.now = h.now.Add(2 * time.Minute) // >= 60m since last heartbeat
	h.pass()
	if len(sendsTo(t, h.srv.Requests(), coord)) != 2 || len(logRules(t, h.paths.Log(h.id), "J13")) != 2 {
		t.Fatalf("second heartbeat did not fire at interval: sends=%d j13=%d", len(sendsTo(t, h.srv.Requests(), coord)), len(logRules(t, h.paths.Log(h.id), "J13")))
	}
}

// TestOneCoordinatorAndOldProcessGroupGoneBeforeRestart verifies KB-COO-1,
// KB-COO-4, and KB-COO-12: a project never has two live coordinators, a
// replacement starts in the repo folder with live workers named in its handoff,
// and the old coordinator's whole process group is gone before the new one starts.
func TestOneCoordinatorAndOldProcessGroupGoneBeforeRestart(t *testing.T) {
	ctx := context.Background()
	h := newProjectHarness(t, "KB-COO-1/4/12 goal")
	h.pass() // J7 start #1
	coord1 := h.state().Project.RunID

	// Spawn a real process group (leader + background child) and bind its PGID to coord1 in hctest.
	cmd := exec.Command("sh", "-c", "sleep 60 & sleep 60 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	oldPGID := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-oldPGID, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	// Reap the leader asynchronously so its zombie clears when killed.
	go func() { _ = cmd.Wait() }()

	// Attach oldPGID to coord1 and also inject a stray second coordinator terminal for the same project.
	cur, err := h.client.Get(ctx, coord1)
	if err != nil {
		t.Fatal(err)
	}
	cur.PID = &oldPGID
	h.srv.Add(cur)
	h.srv.Add(hc.Terminal{
		Address: "w1", Name: "w1", State: "live",
		Status: &hc.Status{App: "claude-code", State: "idle"},
		Labels: map[string]string{"sheepdog.project": h.id, "sheepdog.role": flow.RoleWorker, "sheepdog.task": "SHOP-1"},
	})
	h.srv.Add(hc.Terminal{
		Address: "coord-stray", Name: "coord-stray", State: "live",
		Labels: testCoordLabels(h.id, "run-stray"),
	})
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{
			ID: "SHOP-1", Title: "feat: auth",
			Attempts: []flow.Attempt{{BaseCommit: "b0", RunID: "w1"}},
		}}
		s.Project.Wakes = []flow.Wake{{Key: "worker_failed", Task: "SHOP-1", Why: "N23", Facts: "title auth", At: h.now}}
	})

	// Check KB-COO-12 and KB-COO-1 at the exact moment the replacement starts!
	realRepo, _ := filepath.EvalSymlinks(h.repo)
	var checkedReplacement bool
	h.srv.OnRun = func(term hc.Terminal, p hc.RunParams) {
		h.runs = append(h.runs, p)
		checkedReplacement = true
		if syscall.Kill(-oldPGID, 0) == nil {
			t.Errorf("KB-COO-12: old process group %d still alive when replacement coordinator started", oldPGID)
		}
		if p.Cwd != realRepo {
			t.Errorf("KB-COO-4: replacement cwd = %q, want repo folder %q", p.Cwd, realRepo)
		}
		if prompt := p.Argv[len(p.Argv)-1]; !strings.Contains(prompt, "w1 (SHOP-1 feat: auth)") {
			t.Errorf("KB-COO-4: replacement handoff missing live worker:\n%s", prompt)
		}
		list, lerr := h.client.List(ctx, false)
		var coords []string
		for _, lt := range list.Terminals {
			if lt.Labels["sheepdog.role"] == flow.RoleCoordinator {
				coords = append(coords, lt.Address)
			}
		}
		if lerr != nil || len(coords) != 1 || coords[0] != term.Address {
			t.Errorf("KB-COO-1: live coordinators at replacement start = %v (err=%v), want only [%s]", coords, lerr, term.Address)
		}
	}

	// Trigger J9 silence -> stops coord1 (and its whole process group + stray) -> next pass J7 starts replacement.
	h.coordIdleWithStatus(coord1, "idle") // J12 delivers wake
	h.now = h.now.Add(time.Minute)
	h.pass() // R17 sees idle status
	h.now = h.now.Add(29 * time.Minute)
	h.pass() // R20 -> PhaseIdle; J9 stops old coordinator & process group & stray
	if syscall.Kill(-oldPGID, 0) == nil {
		t.Fatalf("KB-COO-12: old process group %d survived J9 stop", oldPGID)
	}
	h.pass() // J7 starts replacement
	if !checkedReplacement {
		t.Fatal("replacement coordinator did not start")
	}
	// Owner kills coordinator -> past gap, exactly one replacement starts in repo folder.
	coord2 := h.state().Project.RunID
	if _, err := h.client.Kill(ctx, coord2); err != nil {
		t.Fatal(err)
	}
	checkedReplacement = false
	h.now = h.now.Add(10 * time.Minute)
	h.pass() // R33 + J7 replacement
	if !checkedReplacement || h.state().Project.RunID == "" || h.state().Project.RunID == coord2 {
		t.Fatalf("owner-killed coordinator not replaced cleanly: checked=%v runID=%q", checkedReplacement, h.state().Project.RunID)
	}
}

// TestJ9NeverStopsWorkingCoordinator verifies that once hc reports working
// after wake delivery, the J9 silence clock is cleared and neither a long
// working turn nor a machine sleep stops the coordinator.
func TestJ9NeverStopsWorkingCoordinator(t *testing.T) {
	h := newProjectHarness(t, "J9 working")
	h.pass() // J7 start #1
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open"}}
		s.Project.Wakes = []flow.Wake{{Key: "worker_failed", Task: "T1", Why: "N23", Facts: "title x", At: h.now}}
	})
	h.coordIdleWithStatus(coord1, "idle") // J12 delivers wake, arms CoordWakeAt
	if h.state().Project.CoordWakeAt.IsZero() {
		t.Fatal("J12 did not arm CoordWakeAt")
	}

	// Coordinator starts a turn: hctest reports working -> clears CoordWakeAt.
	h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
	h.now = h.now.Add(15 * time.Second)
	h.pass()
	if !h.state().Project.CoordWakeAt.IsZero() {
		t.Fatalf("CoordWakeAt not cleared after coordinator reported working: %v", h.state().Project.CoordWakeAt)
	}

	// Long working turn past coordinator_silent_minutes (45m > 30m) with screen activity.
	for _, screen := range []string{"reading repo...", "running tests...", "writing plan..."} {
		h.now = h.now.Add(15 * time.Minute)
		h.srv.SetScreen(coord1, screen)
		h.pass()
	}
	if h.state().Project.RunID != coord1 || len(logRules(t, h.paths.Log(h.id), "J9")) != 0 || len(h.runs) != 1 {
		t.Fatalf("J9 stopped working coordinator: runID=%q runs=%d j9=%+v", h.state().Project.RunID, len(h.runs), logRules(t, h.paths.Log(h.id), "J9"))
	}

	// Machine sleep jump while working must not trigger J9 either.
	h.now = h.now.Add(2 * time.Hour)
	h.srv.SetScreen(coord1, "resumed after sleep")
	h.pass()
	if h.state().Project.RunID != coord1 || len(logRules(t, h.paths.Log(h.id), "J9")) != 0 {
		t.Fatalf("J9 stopped coordinator after machine sleep: runID=%q j9=%+v", h.state().Project.RunID, logRules(t, h.paths.Log(h.id), "J9"))
	}
}

// TestJ9DoesNotStopPlanlessCoordinatorWithoutWake verifies that a coordinator
// with no plan yet that finished a turn (e.g. waiting on the owner) is never
// stopped and restarted in a loop by J9 when no wakes were delivered.
func TestJ9DoesNotStopPlanlessCoordinatorWithoutWake(t *testing.T) {
	h := newProjectHarness(t, "J9 planless")
	h.pass() // J7 start #1 (no tasks yet)
	c1 := h.state().Project.RunID
	h.srv.SetStatus(c1, &hc.Status{App: "claude-code", State: "working"})
	h.now = h.now.Add(5 * time.Second)
	h.pass()
	h.coordIdleWithStatus(c1, "idle") // asked owner a question, waiting

	h.now = h.now.Add(30 * time.Minute)
	h.pass()
	h.now = h.now.Add(15 * time.Minute)
	h.pass()
	if h.state().Project.RunID != c1 || len(h.runs) != 1 || len(logRules(t, h.paths.Log(h.id), "J9")) != 0 {
		t.Fatalf("J9 stopped plan-less coordinator without delivered wake: runID=%q runs=%d j9=%+v", h.state().Project.RunID, len(h.runs), logRules(t, h.paths.Log(h.id), "J9"))
	}
}

// TestJ4KeepsNewGoalWhileTasksOpenUntilPlanApplied verifies that giving a new
// goal while tasks are still open keeps NewGoal until a new task is added, so
// J4 never finishes the project when the old tasks end.
func TestJ4KeepsNewGoalWhileTasksOpenUntilPlanApplied(t *testing.T) {
	h := newProjectHarness(t, "Goal 1")
	h.pass() // J7 start #1
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: goal 1"}}
	})
	h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
	h.now = h.now.Add(5 * time.Second)
	h.pass()

	// Owner gives Goal 2 while T1 is still open.
	if err := h.eng.Start(h.repo, "Goal 2", 2); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(15 * time.Second)
	h.pass()
	if !h.state().Project.NewGoal {
		t.Fatal("NewGoal cleared while old task T1 was still open and no new plan was applied")
	}

	// T1 finishes -> J4 must NOT finish the project while Goal 2 is unplanned,
	// and the live coordinator receives the new_goal wake.
	h.edit(func(s *flow.State) {
		s.Tasks[0].Done = &flow.Merged{MergedCommit: "m1"}
	})
	h.coordIdleWithStatus(coord1, "idle")
	if j4 := logRules(t, h.paths.Log(h.id), "J4"); len(j4) != 0 || h.state().Project.RunID != coord1 || !h.state().Project.NewGoal {
		t.Fatalf("J4 finished project despite pending new goal: j4=%+v runID=%q newGoal=%v", j4, h.state().Project.RunID, h.state().Project.NewGoal)
	}
	if sends := sendsTo(t, h.srv.Requests(), coord1); len(sends) != 1 || !strings.Contains(sends[0], "Goal 2") {
		t.Fatalf("live coordinator wakes = %q, want 1 new_goal wake naming Goal 2", sends)
	}

	// Coordinator adds a task for Goal 2 -> clears NewGoal.
	h.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks, flow.Task{ID: "T2", Title: "feat: goal 2"})
	})
	h.now = h.now.Add(15 * time.Second)
	h.pass()
	if h.state().Project.NewGoal {
		t.Fatal("NewGoal still set after new task T2 was added")
	}

	// Once T2 also ends and coordinator finishes its final journal, J4 finishes.
	h.edit(func(s *flow.State) {
		s.Tasks[1].Done = &flow.Merged{MergedCommit: "m2"}
	})
	h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
	h.now = h.now.Add(5 * time.Second)
	h.pass()
	h.coordIdleWithStatus(coord1, "idle") // J4 asks final journal
	h.srv.SetStatus(coord1, &hc.Status{App: "claude-code", State: "working"})
	h.now = h.now.Add(5 * time.Second)
	h.pass()
	h.coordIdleWithStatus(coord1, "idle") // J4 stops coordinator
	h.now = h.now.Add(15 * time.Second)
	h.pass() // J4 logs finished
	if h.state().Project.RunID != "" || len(logRules(t, h.paths.Log(h.id), "J4")) != 3 {
		t.Fatalf("J4 did not finish after Goal 2 tasks ended: runID=%q j4=%+v", h.state().Project.RunID, logRules(t, h.paths.Log(h.id), "J4"))
	}
}

// TestJ6AndJ4LogOncePerChange verifies that J6 logs once when holding (gap or
// hourly cap, naming the allow time) and J4 logs once when finishing a project.
func TestJ6AndJ4LogOncePerChange(t *testing.T) {
	h := newProjectHarness(t, "J6 log goal")
	h.pass() // start #1 at 10:00:00Z
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open"}}
	})

	// Gap hold: kill #1 at 10:02 -> 3 passes before 10:10 log J6 exactly once naming 10:10:00Z.
	h.srv.Finish(h.state().Project.RunID, "killed")
	h.now = h.now.Add(2 * time.Minute)
	h.pass()
	h.now = h.now.Add(time.Minute)
	h.pass()
	h.now = h.now.Add(time.Minute)
	h.pass()
	j6 := logRules(t, h.paths.Log(h.id), "J6")
	if len(j6) != 1 || j6[0].Decision != "waiting" || !strings.Contains(j6[0].Why, "2026-11-20T10:10:00Z") {
		t.Fatalf("J6 gap log = %+v, want 1 waiting line naming 2026-11-20T10:10:00Z", j6)
	}

	// Start #2 at 10:10, kill at 10:12, start #3 at 10:20, kill at 10:35 -> hourly cap until 11:00:00Z.
	h.now = h.now.Add(6 * time.Minute) // 10:10
	h.pass()                           // start #2
	h.srv.Finish(h.state().Project.RunID, "killed")
	h.now = h.now.Add(10 * time.Minute) // 10:20
	h.pass()                            // start #3
	h.srv.Finish(h.state().Project.RunID, "killed")
	h.now = h.now.Add(15 * time.Minute) // 10:35
	h.pass()
	h.now = h.now.Add(5 * time.Minute)
	h.pass()
	h.now = h.now.Add(5 * time.Minute)
	h.pass()
	j6 = logRules(t, h.paths.Log(h.id), "J6")
	if len(j6) != 2 || j6[1].Decision != "waiting" || !strings.Contains(j6[1].Why, "2026-11-20T11:00:00Z") {
		t.Fatalf("J6 hourly cap log = %+v, want 2nd waiting line naming 2026-11-20T11:00:00Z", j6)
	}

	// J4 finished when coordinator is already gone logs once across multiple passes.
	h2 := newProjectHarness(t, "J4 silent finish")
	h2.pass() // start #1
	h2.srv.Finish(h2.state().Project.RunID, "exited")
	h2.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: done", Done: &flow.Merged{MergedCommit: "m1"}}}
	})
	for range 3 {
		h2.now = h2.now.Add(time.Minute)
		h2.pass()
	}
	j4 := logRules(t, h2.paths.Log(h2.id), "J4")
	if len(j4) != 1 || j4[0].Decision != "finished" || !strings.Contains(j4[0].Why, "Finished") {
		t.Fatalf("J4 finished log = %+v, want 1 finished line", j4)
	}
}
