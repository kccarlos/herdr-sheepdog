package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// TestEngineLoopStartsOneCoordinatorAndStopsCleanly checks KB-COO-1, KB-COO-4,
// KB-LCH-2, KB-LCH-4, KB-LCH-11 and KB-TSK-2 against hctest and a temp DataDir.
func TestEngineLoopStartsOneCoordinatorAndStopsCleanly(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := Paths{DataDir: t.TempDir()}

	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	eng := New(paths, client)
	eng.PassInterval = 15 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		eng.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Before Start: the loop does nothing and touches neither disk nor hc.
	time.Sleep(40 * time.Millisecond)
	if len(srv.Requests()) != 0 {
		t.Fatalf("idle engine sent hc requests: %+v", srv.Requests())
	}
	if _, err := os.Stat(filepath.Join(paths.DataDir, "factory")); !os.IsNotExist(err) {
		t.Fatalf("idle engine created factory dir: %v", err)
	}

	const goal = "Add a greeting helper with a unit test."
	if err := eng.Start(repo, goal); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the coordinator terminal to appear in hctest.
	var coord hc.Terminal
	deadline := time.Now().Add(2 * time.Second)
	for {
		list, err := client.List(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Terminals) == 1 {
			coord = list.Terminals[0]
			break
		}
		if len(list.Terminals) > 1 {
			t.Fatalf("expected 1 coordinator, got %d", len(list.Terminals))
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for coordinator start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Verify git config project id, cwd, argv and prompt.
	out, err := exec.Command("git", "-C", repo, "config", "--local", "--get", "sheepdog.projectId").Output()
	if err != nil {
		t.Fatalf("git config sheepdog.projectId: %v", err)
	}
	projID := strings.TrimSpace(string(out))
	if projID == "" {
		t.Fatal("empty sheepdog.projectId")
	}
	realRepo, _ := filepath.EvalSymlinks(repo)
	if coord.Cwd != realRepo {
		t.Fatalf("coordinator cwd = %q, want %q", coord.Cwd, realRepo)
	}
	joinedArgv := strings.Join(coord.Argv, " ")
	for _, want := range []string{"DISABLE_AUTOUPDATER=1", "claude", "--model opus", "--effort high", "--permission-mode bypassPermissions", goal, realRepo} {
		if !strings.Contains(joinedArgv, want) {
			t.Errorf("argv %q missing %q", joinedArgv, want)
		}
	}

	// Let several PassInterval ticks run; still exactly one coordinator.
	srv.SetStatus(coord.Address, &hc.Status{App: "claude-code", State: "idle"})
	time.Sleep(60 * time.Millisecond)
	if list, err := client.List(ctx, true); err != nil || len(list.Terminals) != 1 {
		t.Fatalf("after ticks terminals = %+v, err = %v", list.Terminals, err)
	}

	// Stop with empty target stops the single started project and removes its terminal.
	if err := eng.Stop(""); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if list, err := client.List(ctx, true); err != nil || len(list.Terminals) != 0 {
		t.Fatalf("after Stop terminals = %+v, err = %v", list.Terminals, err)
	}

	// Let several ticks pass after Stop; no coordinator is started.
	time.Sleep(60 * time.Millisecond)
	if list, err := client.List(ctx, true); err != nil || len(list.Terminals) != 0 {
		t.Fatalf("after Stop ticks terminals = %+v, err = %v", list.Terminals, err)
	}

	st, err := LoadState(paths.State(projID))
	if err != nil {
		t.Fatal(err)
	}
	if st.Project.Started || st.Project.RunID != "" || st.Seq != 3 {
		t.Fatalf("final state = %+v", st)
	}
	logBytes, err := os.ReadFile(paths.Log(projID))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(logBytes)), "\n"); len(lines) != 3 {
		t.Fatalf("expected 3 log commits (start, J7, stop), got %d:\n%s", len(lines), string(logBytes))
	}
}
