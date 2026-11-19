package world

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// TestClaudeArgvGolden checks KB-LCH-2, KB-LCH-4, KB-LCH-9, KB-LCH-11 across the three roles.
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
	claudeCfg := filepath.Join(t.TempDir(), ".claude.json")

	argv := ClaudeArgv(flow.RoleCoordinator, "opus", "high", "bypassPermissions")
	id, err := StartRun(ctx, client, RunSpec{
		Name:         "coord-test",
		Cwd:          cwd,
		ClaudeConfig: claudeCfg,
		Argv:         argv,
		Prompt:       "Plan one task.",
		Labels:       map[string]string{"sheepdog.role": flow.RoleCoordinator},
	})
	if err != nil {
		t.Fatal(err)
	}
	// KB-LCH-24: repeated StartRun for the same live name adopts the running terminal.
	if id2, err := StartRun(ctx, client, RunSpec{Name: "coord-test", Cwd: cwd, ClaudeConfig: claudeCfg, Argv: argv}); err != nil || id2 != id {
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

// TestStartRunRemovesFinishedSameNameTerminal checks Neon M1: when Get(spec.Name)
// finds a non-live terminal, StartRun removes it before launching the new run.
func TestStartRunRemovesFinishedSameNameTerminal(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	ctx := context.Background()
	spec := RunSpec{
		Name:         "coord-reuse",
		Cwd:          t.TempDir(),
		ClaudeConfig: filepath.Join(t.TempDir(), ".claude.json"),
		Argv:         ClaudeArgv(flow.RoleCoordinator, "opus", "high", "bypassPermissions"),
	}
	id1, err := StartRun(ctx, client, spec)
	if err != nil {
		t.Fatal(err)
	}
	srv.Finish(id1, "exited")

	id2, err := StartRun(ctx, client, spec)
	if err != nil {
		t.Fatal(err)
	}
	if id2 == id1 {
		t.Fatalf("expected new terminal address after finish, got %q", id2)
	}
	if _, err := client.Get(ctx, id1); !errors.Is(err, hc.ErrNotFound) {
		t.Fatalf("finished terminal %s was not removed before Run: err=%v", id1, err)
	}
}

// TestStartRunOmitsDuplicateDisableAutoupdaterEnv checks item 5: DISABLE_AUTOUPDATER
// lives only in ClaudeArgv and is not duplicated in RunParams.Env.
func TestStartRunOmitsDuplicateDisableAutoupdaterEnv(t *testing.T) {
	srv := hctest.New(t)
	var got hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { got = p }
	client := hc.NewClient(srv.Path)

	_, err := StartRun(context.Background(), client, RunSpec{
		Name:         "coord-env",
		Cwd:          t.TempDir(),
		ClaudeConfig: filepath.Join(t.TempDir(), ".claude.json"),
		Argv:         ClaudeArgv(flow.RoleCoordinator, "opus", "high", "bypassPermissions"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Env) != 0 {
		t.Fatalf("RunParams.Env = %v, want empty (DISABLE_AUTOUPDATER belongs only in ClaudeArgv)", got.Env)
	}
}

// TestRunStateOutageAndChildProcessGroupStop checks Neon M8 (d) and (e):
// closed hc socket returns an error (never RunGone), and StopRun kills a child
// spawned in the same process group (`sh -c 'sleep 60 & wait'`).
func TestRunStateOutageAndChildProcessGroupStop(t *testing.T) {
	ctx := context.Background()

	// (d) Closed hc socket: RunState returns error, never RunGone.
	deadSrv := hctest.New(t)
	deadClient := hc.NewClient(deadSrv.Path)
	deadSrv.Close()
	if st, err := RunState(ctx, deadClient, "t1"); err == nil || st == flow.RunGone {
		t.Fatalf("closed-socket RunState = (%q, %v), want non-nil error and not gone", st, err)
	}

	// (e) StopRun kills both leader and background child in the same process group.
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.Command("sh", "-c", `sleep 60 & echo $! > "$1"; wait`, "sh", pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	leaderPID := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = syscall.Kill(-leaderPID, syscall.SIGKILL) })

	var childPID int
	deadline := time.Now().Add(2 * time.Second)
	for {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && n > 1 {
				childPID = n
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for child pid file")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(childPID, syscall.SIGKILL) })

	srv.Add(hc.Terminal{Address: "t1", Name: "coord-pg", State: "live", PID: &leaderPID})
	if err := StopRun(ctx, client, "t1"); err != nil {
		t.Fatalf("StopRun: %v", err)
	}
	if !errors.Is(syscall.Kill(leaderPID, 0), syscall.ESRCH) || !errors.Is(syscall.Kill(childPID, 0), syscall.ESRCH) {
		t.Fatalf("process group not fully dead: leader=%v child=%v", syscall.Kill(leaderPID, 0), syscall.Kill(childPID, 0))
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

// TestRepoProjectRejectsPathSeparatorsAndDots checks item 6: RepoProject rejects
// any sheepdog.projectId containing '/', '\', '.' or '..'.
func TestRepoProjectRejectsPathSeparatorsAndDots(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "repo")
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for _, bad := range []string{"/", `\`, ".", "..", "a/b", `a\b`, "../escape"} {
		if out, err := exec.Command("git", "-C", repo, "config", "--local", "sheepdog.projectId", bad).CombinedOutput(); err != nil {
			t.Fatalf("git config %q: %v: %s", bad, err, out)
		}
		if _, id, err := RepoProject(ctx, repo, false); err != nil || id != "" {
			t.Fatalf("RepoProject(%q, init=false) = %q, %v; want empty id", bad, id, err)
		}
	}
	if _, id, err := RepoProject(ctx, repo, true); err != nil || !strings.HasPrefix(id, "proj-") {
		t.Fatalf("RepoProject(init=true after bad id) = %q, %v; want fresh proj-*", id, err)
	}
}

// TestClaudeFolderTrustAndTestGuard checks item 8 (KB-LCH-9): pre-accepting
// folder trust in Claude's config file, preserving 0600 and all other fields,
// honouring CLAUDE_CONFIG_DIR, leaving malformed files untouched, and refusing
// non-temp paths under `go test`.
func TestClaudeFolderTrustAndTestGuard(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	ctx := context.Background()
	argv := ClaudeArgv(flow.RoleCoordinator, "opus", "high", "bypassPermissions")

	// 1. Existing config file keeps mode 0600 and every other field.
	cwd := t.TempDir()
	cfgFile := filepath.Join(t.TempDir(), ".claude.json")
	seed := `{"numStartups":9007199254740993,"theme":"dark","projects":{"/other":{"hasTrustDialogAccepted":false},"` + cwd + `":{"allowedTools":["Bash"],"hasTrustDialogAccepted":false}}}`
	if err := os.WriteFile(cfgFile, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StartRun(ctx, client, RunSpec{Name: "coord-t1", Cwd: cwd, ClaudeConfig: cfgFile, Argv: argv}); err != nil {
		t.Fatalf("StartRun with existing config: %v", err)
	}
	info, err := os.Stat(cfgFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cfgFile stat = (%v, %v), want mode 0600", info, err)
	}
	raw, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&parsed); err != nil {
		t.Fatalf("decode updated config: %v", err)
	}
	projs, _ := parsed["projects"].(map[string]any)
	cur, _ := projs[cwd].(map[string]any)
	other, _ := projs["/other"].(map[string]any)
	if cur["hasTrustDialogAccepted"] != true || parsed["hasCompletedOnboarding"] != true {
		t.Fatalf("folder trust / onboarding not set in %s:\n%s", cfgFile, string(raw))
	}
	if parsed["theme"] != "dark" || parsed["numStartups"] != json.Number("9007199254740993") || other["hasTrustDialogAccepted"] != false || len(cur["allowedTools"].([]any)) != 1 {
		t.Fatalf("other fields not preserved:\n%s", string(raw))
	}

	// 2. CLAUDE_CONFIG_DIR is honoured when ClaudeConfig is empty.
	cfgDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfgDir)
	cwd2 := t.TempDir()
	if _, err := StartRun(ctx, client, RunSpec{Name: "coord-t2", Cwd: cwd2, Argv: argv}); err != nil {
		t.Fatalf("StartRun with CLAUDE_CONFIG_DIR: %v", err)
	}
	envCfg := filepath.Join(cfgDir, ".claude.json")
	envRaw, err := os.ReadFile(envCfg)
	if err != nil || !strings.Contains(string(envRaw), `"hasTrustDialogAccepted": true`) {
		t.Fatalf("CLAUDE_CONFIG_DIR/.claude.json missing trust (%v):\n%s", err, string(envRaw))
	}

	// 3. Malformed file is left untouched and StartRun still succeeds.
	badFile := filepath.Join(t.TempDir(), "malformed.json")
	const broken = "{broken json\n"
	if err := os.WriteFile(badFile, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StartRun(ctx, client, RunSpec{Name: "coord-t3", Cwd: cwd, ClaudeConfig: badFile, Argv: argv}); err != nil {
		t.Fatalf("StartRun with malformed config should still proceed: %v", err)
	}
	if after, err := os.ReadFile(badFile); err != nil || string(after) != broken {
		t.Fatalf("malformed file changed: got %q (%v), want %q", string(after), err, broken)
	}

	// 4. Path outside os.TempDir() is refused under go test.
	if _, err := StartRun(ctx, client, RunSpec{Name: "coord-t4", Cwd: cwd, ClaudeConfig: "/nonexistent-sheepdog-test-guard/.claude.json", Argv: argv}); err == nil {
		t.Fatal("expected test-path guard error for ClaudeConfig outside os.TempDir()")
	}
}
