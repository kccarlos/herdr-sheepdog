package world

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// Blocking probe (NeonH §2, Carbon Probe 2): StopLabeledRun must neither send
// local SIGTERM/SIGKILL to a local process whose PID collides with a remote
// terminal's PID nor hang/fail for 81 retries when removing a remote terminal.
func TestStopLabeledRunDoesNotSignalLocalPIDOrHangOnRemoteTerminal(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "secret.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	victim := exec.Command("sleep", "60")
	victim.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	localPID := victim.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(-localPID, syscall.SIGKILL)
		_, _ = victim.Process.Wait()
	})

	srv := hctest.New(t)
	srv.Add(hc.Terminal{
		Address:   "box/t4",
		State:     "live",
		CreatedMS: 100,
		PID:       &localPID,
		Cwd:       localDir,
		Labels:    map[string]string{"sheepdog.project": "p1", "sheepdog.run": "run-1"},
	})
	client := hc.NewClient(srv.Path)
	ctx := context.Background()

	list, err := client.List(ctx, false)
	if err != nil || len(list.Terminals) != 1 {
		t.Fatalf("List = (%+v, %v)", list, err)
	}
	if err := StopLabeledRun(ctx, client, list.Terminals[0].Address, "run-1", 100); err != nil {
		t.Errorf("StopLabeledRun(%s) error = %v, want nil", list.Terminals[0].Address, err)
	}
	if err := victim.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("local process PID %d was killed by StopLabeledRun on remote terminal: %v", localPID, err)
	}
}

// Blocking probe (NeonH §2.4, Carbon §2.3): SweepTerminals must ignore remote
// terminals instead of flagging them as stray factory terminals with synthetic
// "box~t4" addresses.
func TestSweepTerminalsIgnoresRemoteTerminals(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "secret.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := hctest.New(t)
	srv.Add(hc.Terminal{
		Address:   "box/t4",
		State:     "live",
		CreatedMS: 100,
		Cwd:       localDir,
		Labels:    map[string]string{"sheepdog.project": "proj-1", "sheepdog.role": "worker"},
	})
	client := hc.NewClient(srv.Path)

	probe := SweepTerminals(context.Background(), client, flow.State{
		Project: flow.Project{ID: "proj-1", Folder: localDir},
	})
	if probe.Unknown != "" {
		t.Fatalf("SweepTerminals unknown = %q", probe.Unknown)
	}
	if len(probe.Items) != 0 {
		t.Fatalf("SweepTerminals flagged remote terminal: %+v", probe.Items)
	}
}

// Blocking probe (NeonH §2.3, Carbon §2.4): ObserveRun must not probe the
// local process table via machine.exec (`ps`) using a remote terminal's PID.
func TestObserveRunDoesNotProbeLocalPIDForRemoteTerminal(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "secret.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var execCalls atomic.Int32
	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		execCalls.Add(1)
		zero := 0
		return hc.ExecResult{Code: &zero}, nil
	}
	localPID := os.Getpid()
	srv.Add(hc.Terminal{
		Address:   "box/t4",
		State:     "live",
		CreatedMS: 100,
		PID:       &localPID,
		Cwd:       localDir,
		Labels:    map[string]string{"sheepdog.run": "run-1"},
	})
	client := hc.NewClient(srv.Path)

	_, err := ObserveRun(context.Background(), client, flow.RunRecord{
		ID:        "run-1",
		Address:   "box~t4",
		CreatedMS: 100,
		State:     flow.PhaseWorking,
	})
	if err != nil {
		t.Fatalf("ObserveRun error = %v", err)
	}
	if n := execCalls.Load(); n != 0 {
		t.Fatalf("ObserveRun made %d local machine.exec calls (ps) for a remote terminal PID, want 0", n)
	}
}
