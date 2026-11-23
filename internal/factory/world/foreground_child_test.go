package world

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// Realistic macOS `ps -ax -o pid=,ppid=,stat=,etime=` output shapes where every
// child's elapsed time is at most its parent's elapsed time.
const (
	psOutputLiveChild = "" +
		"    1     0 Ss    5-00:00:00\n" +
		" 1285     1 S        1:00:00\n" +
		"25392  1285 Ss+        25:00\n" +
		"25393 25392 S+         15:00\n"
	psOutputLiveGrandchild = "" +
		"    1     0 Ss    5-00:00:00\n" +
		" 1285     1 S        1:00:00\n" +
		"25392  1285 Ss+        25:00\n" +
		"25393 25392 S+         24:58\n" +
		"25394 25393 S+         15:00\n"
	psOutputNoChild = "" +
		"    1     0 Ss    5-00:00:00\n" +
		" 1285     1 S        1:00:00\n" +
		"25392  1285 Ss+        25:00\n" +
		"26000  1285 S          10:00\n"
	psOutputZombieOnly = "" +
		"    1     0 Ss    5-00:00:00\n" +
		" 1285     1 S        1:00:00\n" +
		"25392  1285 Ss+        25:00\n" +
		"25393 25392 Z+         15:00\n"
)

// TestObserveRunLiveChildActivity verifies PR 5b.7b (O24) at the world observer
// boundary: on a working run with an unchanged screen, a bounded hc exec ps
// check emits EvActivity iff the agent process (foreground.pid) has at least
// one live non-zombie descendant started at least start_minutes after the agent;
// an unreadable or timed-out check is unknown and changes nothing.
func TestObserveRunLiveChildActivity(t *testing.T) {
	ctx := context.Background()
	zero, one := 0, 1
	cases := []struct {
		name         string
		execFn       func(hc.ExecParams) (hc.ExecResult, error)
		wantActivity bool
	}{
		{
			name: "live direct child emits activity",
			execFn: func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{Code: &zero, Stdout: []byte(psOutputLiveChild)}, nil
			},
			wantActivity: true,
		},
		{
			name: "live grandchild emits activity",
			execFn: func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{Code: &zero, Stdout: []byte(psOutputLiveGrandchild)}, nil
			},
			wantActivity: true,
		},
		{
			name: "no descendant emits no activity",
			execFn: func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{Code: &zero, Stdout: []byte(psOutputNoChild)}, nil
			},
			wantActivity: false,
		},
		{
			name: "zombie child only emits no activity",
			execFn: func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{Code: &zero, Stdout: []byte(psOutputZombieOnly)}, nil
			},
			wantActivity: false,
		},
		{
			name: "timed out exec is unknown and emits no activity",
			execFn: func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{TimedOut: true}, nil
			},
			wantActivity: false,
		},
		{
			name: "failed exec exit code is unknown and emits no activity",
			execFn: func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{Code: &one, Stderr: []byte("ps: failed\n")}, nil
			},
			wantActivity: false,
		},
		{
			name: "rpc error is unknown and emits no activity",
			execFn: func(hc.ExecParams) (hc.ExecResult, error) {
				return hc.ExecResult{}, errors.New("hc exec unavailable")
			},
			wantActivity: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := hctest.New(t)
			var execCalls atomic.Int32
			var bounded atomic.Bool
			srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
				if len(p.Argv) == 0 || filepath.Base(p.Argv[0]) != "ps" {
					t.Errorf("unexpected hc exec argv = %v, want ps", p.Argv)
				}
				if p.Timeout != nil && *p.Timeout > 0 {
					bounded.Store(true)
				}
				execCalls.Add(1)
				return tc.execFn(p)
			}

			pid := 25392
			srv.Add(hc.Terminal{
				Address:   "t1",
				State:     "live",
				CreatedMS: 100,
				PID:       &pid,
				Foreground: &hc.Foreground{
					PID:  pid,
					Name: "claude",
					Argv: []string{"claude"},
					Cwd:  "/tmp",
				},
				Status: &hc.Status{App: "claude-code", State: "working"},
				Labels: map[string]string{"sheepdog.run": "r1"},
			})
			srv.SetScreen("t1", "✢ Running go test ./... (12s · esc to interrupt)\n")

			client := hc.NewClient(srv.Path)
			rec := flow.RunRecord{
				ID:             "r1",
				Address:        "t1",
				CLI:            "claude",
				Role:           flow.RoleWorker,
				State:          flow.PhaseWorking,
				Status:         "working",
				StatusReported: true,
				StartedAt:      time.Now().Add(-25*time.Minute - time.Second),
				LastActivity:   time.Now().Add(-25 * time.Minute),
			}
			// Establish baseline screen hash.
			baseEvs, err := ObserveRun(ctx, client, rec)
			if err != nil || len(baseEvs) == 0 {
				t.Fatalf("baseline ObserveRun = %+v, %v", baseEvs, err)
			}
			rec.ScreenHash = baseEvs[len(baseEvs)-1].ScreenHash
			if rec.ScreenHash == "" {
				t.Fatalf("baseline tick missing ScreenHash: %+v", baseEvs)
			}

			// Advance only the stripped spinner/timer so the screen hash stays identical.
			execCalls.Store(0)
			bounded.Store(false)
			srv.SetScreen("t1", "✽ Running go test ./... (19m 50s · esc to interrupt)\n")

			evs, err := ObserveRun(ctx, client, rec)
			if err != nil {
				t.Fatalf("ObserveRun returned error on child check: %v", err)
			}
			if execCalls.Load() == 0 {
				t.Fatalf("ObserveRun did not probe child processes via hc exec")
			}
			if !bounded.Load() {
				t.Fatalf("child process hc exec was not bounded with a positive Timeout")
			}
			gotActivity := slices.ContainsFunc(evs, func(ev flow.RunEvent) bool {
				return ev.Kind == flow.EvActivity
			})
			if gotActivity != tc.wantActivity {
				t.Fatalf("EvActivity = %t, want %t (events=%+v)", gotActivity, tc.wantActivity, evs)
			}
			if len(evs) == 0 || evs[len(evs)-1].Kind != flow.EvTick || evs[len(evs)-1].ScreenHash == "" {
				t.Fatalf("expected final EvTick with non-empty ScreenHash, got %+v", evs)
			}
		})
	}
}

func runObserveChildActivityCase(t *testing.T, rec flow.RunRecord, psOut string) bool {
	t.Helper()
	ctx := context.Background()
	srv := hctest.New(t)
	zero := 0
	var psCalls atomic.Int32
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if len(p.Argv) == 0 || filepath.Base(p.Argv[0]) != "ps" {
			t.Errorf("unexpected hc exec argv = %v, want ps", p.Argv)
		}
		psCalls.Add(1)
		return hc.ExecResult{Code: &zero, Stdout: []byte(psOut)}, nil
	}

	pid := 25392
	srv.Add(hc.Terminal{
		Address:   "t1",
		State:     "live",
		CreatedMS: 100,
		PID:       &pid,
		Foreground: &hc.Foreground{
			PID:  pid,
			Name: "claude",
			Argv: []string{"claude"},
			Cwd:  "/tmp",
		},
		Status: &hc.Status{App: "claude-code", State: "working"},
		Labels: map[string]string{"sheepdog.run": "r1"},
	})
	srv.SetScreen("t1", "✢ Running go test ./... (12s · esc to interrupt)\n")

	client := hc.NewClient(srv.Path)
	if rec.ScreenHash == "" {
		baseEvs, err := ObserveRun(ctx, client, rec)
		if err != nil || len(baseEvs) == 0 {
			t.Fatalf("baseline ObserveRun = %+v, %v", baseEvs, err)
		}
		rec.ScreenHash = baseEvs[len(baseEvs)-1].ScreenHash
		if rec.ScreenHash == "" {
			t.Fatalf("baseline tick missing ScreenHash: %+v", baseEvs)
		}
	}

	psCalls.Store(0)
	srv.SetScreen("t1", "✽ Running go test ./... (19m 50s · esc to interrupt)\n")
	evs, err := ObserveRun(ctx, client, rec)
	if err != nil {
		t.Fatalf("ObserveRun error: %v", err)
	}
	if psCalls.Load() == 0 {
		t.Fatalf("ObserveRun did not probe child processes via hc exec")
	}
	return slices.ContainsFunc(evs, func(ev flow.RunEvent) bool {
		return ev.Kind == flow.EvActivity
	})
}

// TestObserveRunChildAgentRelativeAge verifies O24 round 3: a live non-zombie
// descendant counts as activity iff agentEtime - childEtime >= start_minutes,
// read from the same ps output. Startup helpers (e.g. started 2s after the
// agent) never count on turn 1, later turns, or adopted/restarted runs, while
// commands started later (e.g. 10m after the agent) count both directly and as
// grandchildren under a helper.
func TestObserveRunChildAgentRelativeAge(t *testing.T) {
	now := time.Now()
	firstTurn := flow.RunRecord{
		ID:             "r1",
		Address:        "t1",
		CLI:            "claude",
		Role:           flow.RoleWorker,
		State:          flow.PhaseWorking,
		Status:         "working",
		StatusReported: true,
		StartedAt:      now.Add(-20*time.Minute - time.Second),
		LastActivity:   now.Add(-20 * time.Minute),
	}

	// Build a second-turn RunRecord via flow.StepRun: started 2h0m1s ago, idle 1h ago,
	// delivered new turn 10m ago.
	def := flow.DefaultSettings()
	secondTurn := flow.RunRecord{ID: "r1", Address: "t1", CLI: "claude", Role: flow.RoleWorker, State: flow.PhaseStarting, StartedAt: now.Add(-2*time.Hour - time.Second), LastActivity: now.Add(-2 * time.Hour)}
	secondTurn, _, _ = flow.StepRun(secondTurn, flow.RunEvent{Kind: flow.EvStatus, Status: "working"}, def, now.Add(-2*time.Hour+5*time.Second))
	secondTurn, _, _ = flow.StepRun(secondTurn, flow.RunEvent{Kind: flow.EvStatus, Status: "idle"}, def, now.Add(-1*time.Hour-20*time.Second))
	secondTurn, _, _ = flow.StepRun(secondTurn, flow.RunEvent{Kind: flow.EvTick}, def, now.Add(-1*time.Hour))
	secondTurn, _, _ = flow.StepRun(secondTurn, flow.RunEvent{Kind: flow.EvDeliver, Msg: "next turn"}, def, now.Add(-10*time.Minute))
	secondTurn, _, _ = flow.StepRun(secondTurn, flow.RunEvent{Kind: flow.EvStatus, Status: "working"}, def, now.Add(-9*time.Minute-55*time.Second))

	// Adopted/restarted run record with no StartedAt timestamp.
	noStartedAt := flow.RunRecord{
		ID:             "r1",
		Address:        "t1",
		CLI:            "claude",
		Role:           flow.RoleWorker,
		State:          flow.PhaseWorking,
		Status:         "working",
		StatusReported: true,
	}

	cases := []struct {
		name         string
		rec          flow.RunRecord
		psOut        string
		wantActivity bool
	}{
		{
			name: "first turn helper started 2s after agent is not active",
			rec:  firstTurn,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+        20:00\n" +
				"25393 25392 S+         19:58\n",
			wantActivity: false,
		},
		{
			name: "first turn child started 10m after agent is active",
			rec:  firstTurn,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+        20:00\n" +
				"25393 25392 S+         10:00\n",
			wantActivity: true,
		},
		{
			name: "first turn grandchild started 10m after agent under 2s helper is active",
			rec:  firstTurn,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+        20:00\n" +
				"25393 25392 S+         19:58\n" +
				"25394 25393 S+         10:00\n",
			wantActivity: true,
		},
		{
			name: "first turn grandchild started 5s after agent under 2s helper is not active",
			rec:  firstTurn,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+        20:00\n" +
				"25393 25392 S+         19:58\n" +
				"25394 25393 S+         19:55\n",
			wantActivity: false,
		},
		{
			name: "second turn helper started 2s after agent is not active",
			rec:  secondTurn,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+      2:00:00\n" +
				"25393 25392 S+       1:59:58\n",
			wantActivity: false,
		},
		{
			name: "second turn grandchild started 10m after agent under 2s helper is active",
			rec:  secondTurn,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+      2:00:00\n" +
				"25393 25392 S+       1:59:58\n" +
				"25394 25393 S+       1:50:00\n",
			wantActivity: true,
		},
		{
			name: "boundary child started exactly start_minutes after agent is active",
			rec:  firstTurn,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+        20:00\n" +
				"25393 25392 S+         18:00\n",
			wantActivity: true,
		},
		{
			name: "boundary child started 1s before start_minutes after agent is not active",
			rec:  firstTurn,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+        20:00\n" +
				"25393 25392 S+         18:01\n",
			wantActivity: false,
		},
		{
			name: "grandchild under helper with unreadable etime still counts",
			rec:  firstTurn,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+        20:00\n" +
				"25393 25392 S+             -\n" +
				"25394 25393 S+         10:00\n",
			wantActivity: true,
		},
		{
			name: "missing etime column is unknown and not active",
			rec:  firstTurn,
			psOut: "" +
				"    1     0 Ss\n" +
				"25392     1 Ss+\n" +
				"25393 25392 S+\n",
			wantActivity: false,
		},
		{
			name: "adopted run without StartedAt counts child started 10m after agent",
			rec:  noStartedAt,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+        20:00\n" +
				"25393 25392 S+         10:00\n",
			wantActivity: true,
		},
		{
			name: "adopted run without StartedAt ignores helper started 2s after agent",
			rec:  noStartedAt,
			psOut: "" +
				"    1     0 Ss    5-00:00:00\n" +
				"25392     1 Ss+        20:00\n" +
				"25393 25392 S+         19:58\n",
			wantActivity: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runObserveChildActivityCase(t, tc.rec, tc.psOut); got != tc.wantActivity {
				t.Fatalf("EvActivity = %t, want %t", got, tc.wantActivity)
			}
		})
	}
}

// TestObserveRunChildEtimeParsing checks ps etime parsing for mm:ss, hh:mm:ss,
// and dd-hh:mm:ss (`05:03`, `1:02:03`, `2-01:02:03`) and malformed inputs using
// realistic parent/child etimes (childEtime <= agentEtime).
func TestObserveRunChildEtimeParsing(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name         string
		agentEtime   string
		agentAge     time.Duration
		childEtime   string
		wantActivity bool
	}{
		{"mm:ss child started 10m after agent", "15:03", 15*time.Minute + 4*time.Second, "05:03", true},
		{"mm:ss helper started 2s after agent", "05:05", 5*time.Minute + 6*time.Second, "05:03", false},
		{"hh:mm:ss child started 10m after agent", "1:12:03", 1*time.Hour + 12*time.Minute + 4*time.Second, "1:02:03", true},
		{"hh:mm:ss helper started 2s after agent", "1:02:05", 1*time.Hour + 2*time.Minute + 6*time.Second, "1:02:03", false},
		{"dd-hh:mm:ss child started 10m after agent", "2-01:12:03", 49*time.Hour + 12*time.Minute + 4*time.Second, "2-01:02:03", true},
		{"dd-hh:mm:ss helper started 2s after agent", "2-01:02:05", 49*time.Hour + 2*time.Minute + 6*time.Second, "2-01:02:03", false},
		{"malformed child etime is not active", "20:00", 20*time.Minute + time.Second, "not-an-etime", false},
		{"out of range seconds is not active", "20:00", 20*time.Minute + time.Second, "05:99", false},
		{"zero-day missing hours is not active", "20:00", 20*time.Minute + time.Second, "0-05:03", false},
		{"malformed agent etime is not active", "not-an-etime", 20*time.Minute + time.Second, "05:03", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := flow.RunRecord{
				ID:             "r1",
				Address:        "t1",
				CLI:            "claude",
				Role:           flow.RoleWorker,
				State:          flow.PhaseWorking,
				Status:         "working",
				StatusReported: true,
				StartedAt:      now.Add(-tc.agentAge),
				LastActivity:   now.Add(-tc.agentAge),
			}
			psOut := "" +
				"    1     0 Ss   10-00:00:00\n" +
				"25392     1 Ss+   " + tc.agentEtime + "\n" +
				"25393 25392 S+    " + tc.childEtime + "\n"
			if got := runObserveChildActivityCase(t, rec, psOut); got != tc.wantActivity {
				t.Fatalf("agent=%q child=%q: EvActivity = %t, want %t", tc.agentEtime, tc.childEtime, got, tc.wantActivity)
			}
		})
	}
}

// TestParseEtime checks ps elapsed-time parsing directly without socket I/O.
func TestParseEtime(t *testing.T) {
	cases := []struct {
		in     string
		want   time.Duration
		wantOK bool
	}{
		{"00:00", 0, true},
		{"05:03", 5*time.Minute + 3*time.Second, true},
		{"1:02:03", time.Hour + 2*time.Minute + 3*time.Second, true},
		{"2-01:02:03", 49*time.Hour + 2*time.Minute + 3*time.Second, true},
		{"0-01:02:03", time.Hour + 2*time.Minute + 3*time.Second, true},
		{"0-05:03", 0, false},
		{"0-99:00:00", 0, false},
		{"05:99", 0, false},
		{"60:00", 0, false},
		{"-05:03", 0, false},
		{"+05:03", 0, false},
		{"not-an-etime", 0, false},
		{"100001-00:00:00", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseEtime(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("parseEtime(%q) = (%v, %t), want (%v, %t)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
