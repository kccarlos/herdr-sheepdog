package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// Real macOS `hc exec --timeout 5 -- /bin/ps -ax -o pid=,ppid=,stat=` output
// shapes recorded in the PR 5b.7b (O24) spike against a scratch hc terminal.
const (
	o24PSLiveTree = "" +
		"    1     0 Ss  \n" +
		" 1285     1 S   \n" +
		"25392  1285 Ss+ \n" +
		"25393 25392 S+  \n" +
		"25394 25393 S+  \n"
	o24PSNoChild = "" +
		"    1     0 Ss  \n" +
		" 1285     1 S   \n" +
		"25392  1285 Ss+ \n" +
		"26000  1285 S   \n"
	o24PSZombieOnly = "" +
		"    1     0 Ss  \n" +
		" 1285     1 S   \n" +
		"25392  1285 Ss+ \n" +
		"25393 25392 Z+  \n"
)

type quietWorkerFixture struct {
	srv    *hctest.Server
	client *hc.Client
	paths  Paths
	eng    *Engine
	id     string
	worker string
	now    time.Time
}

func newQuietWorkerFixture(t *testing.T, start time.Time) *quietWorkerFixture {
	t.Helper()
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)

	f := &quietWorkerFixture{
		srv:    srv,
		client: client,
		paths:  paths,
		now:    start,
	}
	f.eng = newGitTestEngine(t, paths, client)
	f.eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	f.eng.Now = func() time.Time { return f.now }
	if err := f.eng.Start(repo, "Goal", 1); err != nil {
		t.Fatal(err)
	}
	f.id = f.eng.projectDirs()[0].Name()
	f.eng.passProject(ctx, f.id, false) // J7 coordinator
	st, _ := LoadState(paths.State(f.id))
	srv.SetStatus(st.Project.RunID, &hc.Status{App: "claude-code", State: "idle"})
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(f.id), st); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.Report("tok-coord", "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","title":"feat: o24 quiet","estimate_lines":50,`+testPlanJSON+`}]}`)); err != nil {
		t.Fatal(err)
	}
	f.eng.passProject(ctx, f.id, false) // N11 worktree
	f.eng.passProject(ctx, f.id, false) // N23 launches worker
	st, _ = LoadState(paths.State(f.id))
	f.worker = st.Tasks[0].Attempts[0].RunID

	// Attach a foreground PID to the worker terminal, matching real `hc get --json`.
	term, err := client.Get(ctx, f.worker)
	if err != nil {
		t.Fatal(err)
	}
	pid := 25392
	term.PID = &pid
	term.Foreground = &hc.Foreground{PID: pid, Name: "claude", Argv: []string{"claude"}, Cwd: term.Cwd}
	srv.Add(term)

	srv.SetStatus(f.worker, &hc.Status{App: "claude-code", State: "working"})
	srv.SetScreen(f.worker, "✢ Running go test ./... (12s · esc to interrupt)\n")
	f.now = f.now.Add(5 * time.Second)
	f.eng.passProject(ctx, f.id, false) // R8 -> working, baseline screen hash
	return f
}

func (f *quietWorkerFixture) interruptsSince(before int) int {
	n := 0
	for _, r := range f.srv.Requests()[before:] {
		if r.Method == "terminal.interrupt" && strings.Contains(string(r.Params), f.worker) {
			n++
		}
	}
	return n
}

func (f *quietWorkerFixture) run(t *testing.T) *flow.RunRecord {
	t.Helper()
	st, err := LoadState(f.paths.State(f.id))
	if err != nil {
		t.Fatal(err)
	}
	return st.Tasks[0].Attempts[0].Run
}

func (f *quietWorkerFixture) onPSExec(t *testing.T, fn func(hc.ExecParams) (hc.ExecResult, error)) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	one := 1
	f.srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if len(p.Argv) == 0 || filepath.Base(p.Argv[0]) != "ps" {
			return hc.ExecResult{Code: &one}, nil
		}
		if p.Timeout == nil || *p.Timeout <= 0 {
			t.Errorf("child probe hc exec missing positive Timeout: %+v", p)
		}
		calls.Add(1)
		return fn(p)
	}
	return &calls
}

// TestQuietWorkerWithLiveChildSkipsR21AndR22 verifies PR 5b.7b (O24): when a
// working agent's screen only ticks a stripped timer while its foreground
// process has a live descendant (e.g. folded `go test`), neither R21 nor R22
// fires.
func TestQuietWorkerWithLiveChildSkipsR21AndR22(t *testing.T) {
	ctx := context.Background()
	f := newQuietWorkerFixture(t, time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))

	zero := 0
	execCalls := f.onPSExec(t, func(hc.ExecParams) (hc.ExecResult, error) {
		return hc.ExecResult{Code: &zero, Stdout: []byte(o24PSLiveTree)}, nil
	})

	reqsBefore := len(f.srv.Requests())
	// Advance well past quiet_minutes (20m) and quiet_minutes + interrupt_seconds (21m30s).
	for _, step := range []struct {
		dt     time.Duration
		screen string
	}{
		{10 * time.Minute, "✻ Running go test ./... (10m 05s · esc to interrupt)\n"},
		{10 * time.Minute, "✽ Running go test ./... (20m 05s · esc to interrupt)\n"},
		{2 * time.Minute, "✢ Running go test ./... (22m 05s · esc to interrupt)\n"},
		{3 * time.Minute, "✻ Running go test ./... (25m 05s · esc to interrupt)\n"},
	} {
		f.srv.SetScreen(f.worker, step.screen)
		f.now = f.now.Add(step.dt)
		f.eng.passProject(ctx, f.id, false)
	}

	if n := f.interruptsSince(reqsBefore); n != 0 {
		t.Fatalf("interrupts while live child running = %d, want 0", n)
	}
	if execCalls.Load() == 0 {
		t.Fatalf("expected child process probe via hc exec, got 0 calls")
	}
	if r21 := logRules(t, f.paths.Log(f.id), "R21"); len(r21) != 0 {
		t.Fatalf("R21 logged while live child running: %+v", r21)
	}
	if r22 := logRules(t, f.paths.Log(f.id), "R22"); len(r22) != 0 {
		t.Fatalf("R22 logged while live child running: %+v", r22)
	}
	got := f.run(t)
	if got == nil || got.State != flow.PhaseWorking || !got.InterruptedAt.IsZero() {
		t.Fatalf("worker run = %+v, want working and not interrupted", got)
	}
	if !got.LastActivity.Equal(f.now) {
		t.Fatalf("LastActivity = %v, want %v (updated by live child activity)", got.LastActivity, f.now)
	}
}

// TestQuietWorkerWithoutChildTriggersR21AndR22 verifies PR 5b.7b (O24): the
// same quiet screen without a live descendant (none, or zombie only) still
// triggers R21 at quiet_minutes and R22 after interrupt_seconds.
func TestQuietWorkerWithoutChildTriggersR21AndR22(t *testing.T) {
	ctx := context.Background()
	zero := 0
	for _, tc := range []struct {
		name  string
		psOut string
	}{
		{"no children", o24PSNoChild},
		{"zombie child only", o24PSZombieOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQuietWorkerFixture(t, time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC))
			execCalls := f.onPSExec(t, func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{Code: &zero, Stdout: []byte(tc.psOut)}, nil
			})

			f.srv.SetScreen(f.worker, "✻ Running go test ./... (10m 05s · esc to interrupt)\n")
			f.now = f.now.Add(10 * time.Minute)
			f.eng.passProject(ctx, f.id, false)

			f.srv.SetScreen(f.worker, "✽ Running go test ./... (20m 05s · esc to interrupt)\n")
			f.now = f.now.Add(10 * time.Minute) // 20m = quiet_minutes -> R21
			reqsBefore := len(f.srv.Requests())
			f.eng.passProject(ctx, f.id, false)

			if n := f.interruptsSince(reqsBefore); n != 1 {
				t.Fatalf("interrupts at quiet_minutes = %d, want 1", n)
			}

			f.srv.SetScreen(f.worker, "■ Conversation interrupted - tell the model what to do differently.\n")
			f.now = f.now.Add(15 * time.Second)
			f.eng.passProject(ctx, f.id, false)
			f.now = f.now.Add(75 * time.Second) // 90s = interrupt_seconds -> R22
			f.eng.passProject(ctx, f.id, false)

			got := f.run(t)
			if got == nil || got.State != flow.PhaseEnded || got.Result != flow.EndStuck {
				t.Fatalf("after interrupt_seconds run = %+v, want ended(stuck) via R22", got)
			}
			if r22 := logRules(t, f.paths.Log(f.id), "R22"); len(r22) != 1 {
				t.Fatalf("R22 log lines = %+v, want 1", r22)
			}
			if execCalls.Load() == 0 {
				t.Fatalf("expected child process probe via hc exec, got 0 calls")
			}
		})
	}
}

// TestQuietWorkerFailedOrTimedOutChildReadChangesNothing verifies PR 5b.7b
// (O24): a timed-out or failed child process read is treated as unknown and
// leaves the quiet timer working exactly as before (R21 then R22).
func TestQuietWorkerFailedOrTimedOutChildReadChangesNothing(t *testing.T) {
	ctx := context.Background()
	one := 1
	for _, tc := range []struct {
		name   string
		execFn func(hc.ExecParams) (hc.ExecResult, error)
	}{
		{
			name: "timed out",
			execFn: func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{TimedOut: true}, nil
			},
		},
		{
			name: "non-zero exit",
			execFn: func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{Code: &one, Stderr: []byte("ps: permission denied\n")}, nil
			},
		},
		{
			name: "rpc error",
			execFn: func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{}, errors.New("hc exec error")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newQuietWorkerFixture(t, time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
			execCalls := f.onPSExec(t, tc.execFn)

			f.srv.SetScreen(f.worker, "✻ Running go test ./... (10m 05s · esc to interrupt)\n")
			f.now = f.now.Add(10 * time.Minute)
			f.eng.passProject(ctx, f.id, false)

			f.srv.SetScreen(f.worker, "✽ Running go test ./... (20m 05s · esc to interrupt)\n")
			f.now = f.now.Add(10 * time.Minute) // 20m = quiet_minutes -> R21
			reqsBefore := len(f.srv.Requests())
			f.eng.passProject(ctx, f.id, false)

			if n := f.interruptsSince(reqsBefore); n != 1 {
				t.Fatalf("interrupts at quiet_minutes = %d, want 1", n)
			}

			f.srv.SetScreen(f.worker, "■ Conversation interrupted - tell the model what to do differently.\n")
			f.now = f.now.Add(15 * time.Second)
			f.eng.passProject(ctx, f.id, false)
			f.now = f.now.Add(75 * time.Second) // 90s = interrupt_seconds -> R22
			f.eng.passProject(ctx, f.id, false)

			got := f.run(t)
			if got == nil || got.State != flow.PhaseEnded || got.Result != flow.EndStuck {
				t.Fatalf("after interrupt_seconds run = %+v, want ended(stuck) via R22", got)
			}
			if execCalls.Load() == 0 {
				t.Fatalf("expected child process probe via hc exec, got 0 calls")
			}
		})
	}
}

// TestQuietWorkerLiveChildSurvivesRestart verifies PR 5b.7b (O24) is
// restart-safe: LastActivity updated by live child activity persists in
// state.json across an Engine restart, so a daemon restart neither falsely
// interrupts an active child nor resets the quiet clock once the child exits.
func TestQuietWorkerLiveChildSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	f := newQuietWorkerFixture(t, time.Date(2026, 11, 22, 13, 0, 0, 0, time.UTC))

	zero := 0
	var liveChild atomic.Bool
	liveChild.Store(true)
	f.onPSExec(t, func(hc.ExecParams) (hc.ExecResult, error) {
		if liveChild.Load() {
			return hc.ExecResult{Code: &zero, Stdout: []byte(o24PSLiveTree)}, nil
		}
		return hc.ExecResult{Code: &zero, Stdout: []byte(o24PSNoChild)}, nil
	})

	// Child runs quietly for 18 minutes before daemon restart.
	f.srv.SetScreen(f.worker, "✻ Running go test ./... (18m 00s · esc to interrupt)\n")
	f.now = f.now.Add(18 * time.Minute)
	f.eng.passProject(ctx, f.id, false)

	// Daemon restarts with a fresh Engine reading state.json.
	eng2 := newGitTestEngine(t, f.paths, f.client)
	eng2.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng2.Now = func() time.Time { return f.now }

	// At t=21m (past quiet_minutes from initial R8), child is still alive -> no R21.
	reqsBefore := len(f.srv.Requests())
	f.srv.SetScreen(f.worker, "✽ Running go test ./... (21m 00s · esc to interrupt)\n")
	f.now = f.now.Add(3 * time.Minute)
	eng2.passProject(ctx, f.id, true)
	if n := f.interruptsSince(reqsBefore); n != 0 {
		t.Fatalf("interrupts after restart with live child = %d, want 0", n)
	}
	if got := f.run(t); got == nil || got.State != flow.PhaseWorking || !got.LastActivity.Equal(f.now) {
		t.Fatalf("after restart with live child run = %+v, want working with LastActivity=%v", got, f.now)
	}

	// Child exits; quiet clock starts from the persisted LastActivity (t=21m).
	liveChild.Store(false)
	f.now = f.now.Add(10 * time.Minute) // t=31m: only 10m since last child activity -> still no R21
	eng2.passProject(ctx, f.id, false)
	if n := f.interruptsSince(reqsBefore); n != 0 {
		t.Fatalf("interrupts 10m after child exit = %d, want 0", n)
	}

	// At t=41m (20m = quiet_minutes after child exited at t=21m) -> R21 fires once.
	f.now = f.now.Add(10 * time.Minute)
	eng2.passProject(ctx, f.id, false)
	if n := f.interruptsSince(reqsBefore); n != 1 {
		t.Fatalf("interrupts 20m after child exit = %d, want 1", n)
	}
}

// TestQuietWorkerOldHelperOnlyTriggersR21 verifies O24 round 2: a worker whose
// only descendant is a long-lived helper (etime longer than the current working
// turn, both on the first turn and on a second turn after idle -> working) is
// not kept alive and still hits R21 and R22.
func TestQuietWorkerOldHelperOnlyTriggersR21(t *testing.T) {
	ctx := context.Background()
	zero := 0

	t.Run("first turn", func(t *testing.T) {
		f := newQuietWorkerFixture(t, time.Date(2026, 11, 22, 14, 0, 0, 0, time.UTC))
		psOldHelper := "" +
			"    1     0 Ss    5-00:00:00\n" +
			" 1285     1 S        1:00:00\n" +
			"25392  1285 Ss+        20:05\n" +
			"25393 25392 S+         25:00\n"
		execCalls := f.onPSExec(t, func(hc.ExecParams) (hc.ExecResult, error) {
			return hc.ExecResult{Code: &zero, Stdout: []byte(psOldHelper)}, nil
		})

		f.srv.SetScreen(f.worker, "✻ Running go test ./... (10m 05s · esc to interrupt)\n")
		f.now = f.now.Add(10 * time.Minute)
		f.eng.passProject(ctx, f.id, false)

		f.srv.SetScreen(f.worker, "✽ Running go test ./... (20m 05s · esc to interrupt)\n")
		f.now = f.now.Add(10 * time.Minute) // 20m = quiet_minutes -> R21
		reqsBefore := len(f.srv.Requests())
		f.eng.passProject(ctx, f.id, false)

		if n := f.interruptsSince(reqsBefore); n != 1 {
			t.Fatalf("interrupts at quiet_minutes with old helper only = %d, want 1", n)
		}
		f.srv.SetScreen(f.worker, "■ Conversation interrupted - tell the model what to do differently.\n")
		f.now = f.now.Add(15 * time.Second)
		f.eng.passProject(ctx, f.id, false)
		f.now = f.now.Add(75 * time.Second) // 90s = interrupt_seconds -> R22
		f.eng.passProject(ctx, f.id, false)

		got := f.run(t)
		if got == nil || got.State != flow.PhaseEnded || got.Result != flow.EndStuck {
			t.Fatalf("after interrupt_seconds run = %+v, want ended(stuck) via R22", got)
		}
		if execCalls.Load() == 0 {
			t.Fatalf("expected child process probe via hc exec, got 0 calls")
		}
	})

	t.Run("second turn after idle", func(t *testing.T) {
		f := newQuietWorkerFixture(t, time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC))
		psOldHelper := "" +
			"    1     0 Ss    5-00:00:00\n" +
			" 1285     1 S        1:00:00\n" +
			"25392  1285 Ss+        50:35\n" +
			"25393 25392 S+         45:00\n"
		f.onPSExec(t, func(hc.ExecParams) (hc.ExecResult, error) {
			return hc.ExecResult{Code: &zero, Stdout: []byte(psOldHelper)}, nil
		})

		// Finish turn 1 (R20 -> idle).
		f.srv.SetStatus(f.worker, &hc.Status{App: "claude-code", State: "idle"})
		f.now = f.now.Add(5 * time.Second)
		f.eng.passProject(ctx, f.id, false)
		f.now = f.now.Add(25 * time.Second)
		f.eng.passProject(ctx, f.id, false)
		if got := f.run(t); got == nil || got.State != flow.PhaseIdle {
			t.Fatalf("after R20 run = %+v, want idle", got)
		}

		// Sit idle 30m, then start turn 2 (idle -> working via R27).
		f.now = f.now.Add(30 * time.Minute)
		f.srv.SetStatus(f.worker, &hc.Status{App: "claude-code", State: "working"})
		f.srv.SetScreen(f.worker, "✢ Second turn running (01s · esc to interrupt)\n")
		f.eng.passProject(ctx, f.id, false)

		// Helper etime (45m) is shorter than total run age (~50m), but longer
		// than turn 2 (20m) -> must not count as activity, so R21 fires.
		f.srv.SetScreen(f.worker, "✻ Second turn running (10m 00s · esc to interrupt)\n")
		f.now = f.now.Add(10 * time.Minute)
		f.eng.passProject(ctx, f.id, false)

		f.srv.SetScreen(f.worker, "✽ Second turn running (20m 00s · esc to interrupt)\n")
		f.now = f.now.Add(10 * time.Minute)
		reqsBefore := len(f.srv.Requests())
		f.eng.passProject(ctx, f.id, false)

		if n := f.interruptsSince(reqsBefore); n != 1 {
			t.Fatalf("turn 2 interrupts at quiet_minutes with old helper only = %d, want 1", n)
		}
	})
}

// TestQuietWorkerFreshChildAndGrandchildUnderOldHelperSkipR21 verifies O24
// round 2: a fresh child started in the current turn, or a fresh grandchild
// under an old helper started in the current turn, counts as activity and keeps
// R21 from firing.
func TestQuietWorkerFreshChildAndGrandchildUnderOldHelperSkipR21(t *testing.T) {
	ctx := context.Background()
	zero := 0
	cases := []struct {
		name  string
		psOut string
	}{
		{
			name: "fresh direct child",
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				" 1285     1 S        1:00:00\n" +
				"25392  1285 Ss+        25:05\n" +
				"25393 25392 S+         05:03\n",
		},
		{
			name: "fresh grandchild under old helper",
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				" 1285     1 S        1:00:00\n" +
				"25392  1285 Ss+      1:30:00\n" +
				"25393 25392 S+       1:02:03\n" +
				"25394 25393 S+         05:03\n",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newQuietWorkerFixture(t, time.Date(2026, 11, 22, 16+i, 0, 0, 0, time.UTC))
			execCalls := f.onPSExec(t, func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{Code: &zero, Stdout: []byte(tc.psOut)}, nil
			})

			reqsBefore := len(f.srv.Requests())
			for _, step := range []struct {
				dt     time.Duration
				screen string
			}{
				{10 * time.Minute, "✻ Running go test ./... (10m 05s · esc to interrupt)\n"},
				{10 * time.Minute, "✽ Running go test ./... (20m 05s · esc to interrupt)\n"},
				{2 * time.Minute, "✢ Running go test ./... (22m 05s · esc to interrupt)\n"},
				{3 * time.Minute, "✻ Running go test ./... (25m 05s · esc to interrupt)\n"},
			} {
				f.srv.SetScreen(f.worker, step.screen)
				f.now = f.now.Add(step.dt)
				f.eng.passProject(ctx, f.id, false)
			}

			if n := f.interruptsSince(reqsBefore); n != 0 {
				t.Fatalf("interrupts while fresh descendant running = %d, want 0", n)
			}
			if execCalls.Load() == 0 {
				t.Fatalf("expected child process probe via hc exec, got 0 calls")
			}
			got := f.run(t)
			if got == nil || got.State != flow.PhaseWorking || !got.InterruptedAt.IsZero() {
				t.Fatalf("worker run = %+v, want working and not interrupted", got)
			}
			if !got.LastActivity.Equal(f.now) {
				t.Fatalf("LastActivity = %v, want %v", got.LastActivity, f.now)
			}
		})
	}
}
