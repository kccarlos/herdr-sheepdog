package world

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// TestClaudeArgvGolden checks KB-LCH-2, KB-LCH-4, KB-LCH-11 across the three roles.
func TestClaudeArgvGolden(t *testing.T) {
	cases := []struct {
		name   string
		role   string
		model  string
		effort string
		perm   string
		golden string
	}{
		{"coordinator", flow.RoleCoordinator, "opus", "high", "bypassPermissions", "testdata/claude_coordinator.golden"},
		{"worker", flow.RoleWorker, "sonnet", "high", "bypassPermissions", "testdata/claude_worker.golden"},
		{"reviewer", flow.RoleReviewer, "opus", "high", "plan", "testdata/claude_reviewer.golden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(ClaudeArgv(tc.role, tc.model, tc.effort, tc.perm), "\n") + "\n"
			wantBytes, err := os.ReadFile(tc.golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(wantBytes) {
				t.Fatalf("argv mismatch:\ngot:\n%s\nwant:\n%s", got, string(wantBytes))
			}
		})
	}

	// KB-LCH-2, KB-LCH-4: never launch with a missing model, effort, permission mode, or unknown role.
	for _, bad := range [][4]string{
		{"unknown", "opus", "high", "plan"},
		{flow.RoleCoordinator, "", "high", "bypassPermissions"},
		{flow.RoleWorker, "sonnet", "", "bypassPermissions"},
		{flow.RoleReviewer, "opus", "high", ""},
	} {
		if got := ClaudeArgv(bad[0], bad[1], bad[2], bad[3]); got != nil {
			t.Errorf("ClaudeArgv(%v) = %v, want nil", bad, got)
		}
	}
}

// TestRunLifecycleAndProcessGroupStop checks StartRun, SendRun, RunState and StopRun (KB-COO-12).
func TestRunLifecycleAndProcessGroupStop(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	ctx := context.Background()
	cwd := t.TempDir()

	argv := ClaudeArgv(flow.RoleCoordinator, "opus", "high", "bypassPermissions")
	id, err := StartRun(ctx, client, RunSpec{
		Name:   "coord-test",
		Cwd:    cwd,
		Argv:   argv,
		Prompt: "Plan one task.",
		Labels: map[string]string{"sheepdog.role": flow.RoleCoordinator},
	})
	if err != nil {
		t.Fatal(err)
	}
	// KB-LCH-24: repeated StartRun for the same live name adopts the running terminal.
	if id2, err := StartRun(ctx, client, RunSpec{Name: "coord-test", Cwd: cwd, Argv: argv}); err != nil || id2 != id {
		t.Fatalf("duplicate StartRun = %q, %v; want %q", id2, err, id)
	}
	term, err := client.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if term.Labels["herdr.space"] == "" || term.Labels["herdr.tab"] == "" || term.Labels["herdr.seen"] != "0" {
		t.Fatalf("missing herdr display labels: %+v", term.Labels)
	}
	if term.Argv[len(term.Argv)-1] != "Plan one task." {
		t.Fatalf("prompt missing from argv: %v", term.Argv)
	}

	if st, err := RunState(ctx, client, id); err != nil || st != flow.RunWorking {
		t.Fatalf("fresh RunState = %q, %v; want working", st, err)
	}
	srv.SetStatus(id, &hc.Status{App: "claude-code", State: "done"})
	if st, err := RunState(ctx, client, id); err != nil || st != flow.RunIdle {
		t.Fatalf("done RunState = %q, %v; want idle", st, err)
	}
	if err := SendRun(ctx, client, id, "continue"); err != nil {
		t.Fatalf("SendRun: %v", err)
	}

	// Attach a real OS process group to verify StopRun leaves no live process.
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	term.PID = &pid
	srv.Add(term)

	if err := StopRun(ctx, client, id); err != nil {
		t.Fatalf("StopRun: %v", err)
	}
	if st, err := RunState(ctx, client, id); err != nil || st != flow.RunGone {
		t.Fatalf("after StopRun RunState = %q, %v; want gone", st, err)
	}
	if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		t.Fatalf("process %d still alive after StopRun", pid)
	}
}

// TestRepoProjectIDSurvivesRename checks KB-TSK-2 (design.md 3).
func TestRepoProjectIDSurvivesRename(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	repo := filepath.Join(parent, "first")
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	root1, id1, err := RepoProject(ctx, repo, true)
	if err != nil || id1 == "" || root1 == "" {
		t.Fatalf("RepoProject init = %q, %q, %v", root1, id1, err)
	}
	renamed := filepath.Join(parent, "renamed")
	if err := os.Rename(repo, renamed); err != nil {
		t.Fatal(err)
	}
	_, id2, err := RepoProject(ctx, renamed, false)
	if err != nil || id2 != id1 {
		t.Fatalf("after rename id = %q (err %v), want %q", id2, err, id1)
	}
}
