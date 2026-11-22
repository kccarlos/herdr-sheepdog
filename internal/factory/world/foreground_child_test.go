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

// Real macOS `hc exec --timeout 5 -- /bin/ps -ax -o pid=,ppid=,stat=` output
// shapes captured in the PR 5b.7b (O24) spike against a scratch hc terminal.
const (
	psOutputLiveChild = "" +
		"    1     0 Ss  \n" +
		" 1285     1 S   \n" +
		"25392  1285 Ss+ \n" +
		"25393 25392 S+  \n"
	psOutputLiveGrandchild = "" +
		"    1     0 Ss  \n" +
		" 1285     1 S   \n" +
		"25392  1285 Ss+ \n" +
		"25393 25392 S+  \n" +
		"25394 25393 S+  \n"
	psOutputNoChild = "" +
		"    1     0 Ss  \n" +
		" 1285     1 S   \n" +
		"25392  1285 Ss+ \n" +
		"26000  1285 S   \n"
	psOutputZombieOnly = "" +
		"    1     0 Ss  \n" +
		" 1285     1 S   \n" +
		"25392  1285 Ss+ \n" +
		"25393 25392 Z+  \n"
)

// TestObserveRunLiveChildActivity verifies PR 5b.7b (O24) at the world observer
// boundary: on a working run with an unchanged screen, a bounded hc exec ps
// check emits EvActivity iff the agent process (foreground.pid) has at least
// one live non-zombie descendant; an unreadable or timed-out check is unknown
// and changes nothing.
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
				StartedAt:      time.Now().Add(-time.Minute),
				LastActivity:   time.Now().Add(-time.Minute),
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
