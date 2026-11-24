package world

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// TestClaudeArgvGolden checks KB-LCH-2, KB-LCH-4, KB-LCH-9, KB-LCH-11 for
// coordinator and worker launches (reviewers use ClaudeReviewerArgv).
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

	// KB-LCH-2, KB-LCH-4: never launch with a missing model, effort, permission mode, or non-worker/coordinator role.
	for _, bad := range [][4]string{
		{"unknown", "opus", "high", "plan"},
		{flow.RoleCoordinator, "", "high", "bypassPermissions"},
		{flow.RoleWorker, "sonnet", "", "bypassPermissions"},
		{flow.RoleWorker, "sonnet", "high", ""},
		{flow.RoleReviewer, "opus", "high", "dontAsk"},
	} {
		if got := ClaudeArgv(bad[0], bad[1], bad[2], bad[3]); got != nil {
			t.Errorf("ClaudeArgv(%v) = %v, want nil", bad, got)
		}
	}
}

func TestClaudeSessionArgvAndNewUUID(t *testing.T) {
	u1, u2 := NewUUID(), NewUUID()
	if len(u1) != 36 || u1 == u2 || u1[14] != '4' {
		t.Fatalf("NewUUID() = (%q, %q), want distinct v4 UUIDs", u1, u2)
	}
	fresh := ClaudeSessionArgv(flow.RoleWorker, "sonnet", "high", "bypassPermissions", u1, false)
	if !slices.Equal(fresh[len(fresh)-2:], []string{"--session-id", u1}) || !slices.Contains(fresh, "sonnet") || !slices.Contains(fresh, "high") {
		t.Fatalf("fresh ClaudeSessionArgv = %q", fresh)
	}
	resumed := ClaudeSessionArgv(flow.RoleWorker, "opus", "high", "bypassPermissions", u1, true)
	if !slices.Equal(resumed[len(resumed)-2:], []string{"--resume", u1}) || !slices.Contains(resumed, "opus") || !slices.Contains(resumed, "high") {
		t.Fatalf("resumed ClaudeSessionArgv = %q (must keep explicit --model and --effort)", resumed)
	}
}

func startRun(ctx context.Context, client *hc.Client, spec RunSpec) (string, error) {
	t, _, err := StartTerminal(ctx, client, spec, nil)
	return t.Address, err
}

func observeRunState(ctx context.Context, client *hc.Client, id string) (flow.RunState, error) {
	evs, err := ObserveRun(ctx, client, flow.RunRecord{Address: id, State: flow.PhaseWorking})
	if err != nil {
		return "", err
	}
	rec, s, now := flow.RunRecord{Address: id, State: flow.PhaseWorking}, flow.DefaultSettings(), time.Now()
	for _, ev := range evs {
		rec, _, _ = flow.StepRun(rec, ev, s, now)
	}
	return rec.TaskRun().State, nil
}

// TestRunLifecycleAndProcessGroupStop checks StartRun, SendRun, ObserveRun and StopRun (KB-COO-12).
func TestRunLifecycleAndProcessGroupStop(t *testing.T) {
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	ctx := context.Background()
	cwd := t.TempDir()
	claudeCfg := filepath.Join(t.TempDir(), ".claude.json")

	argv := ClaudeArgv(flow.RoleCoordinator, "opus", "high", "bypassPermissions")
	id, err := startRun(ctx, client, RunSpec{
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

	if st, err := observeRunState(ctx, client, id); err != nil || st != flow.RunWorking {
		t.Fatalf("fresh observeRunState = %q, %v; want working", st, err)
	}
	srv.SetStatus(id, &hc.Status{App: "claude-code", State: "done"})
	term, _ = client.Get(ctx, id)
	pastMS := uint64(time.Now().Add(-time.Minute).UnixMilli())
	term.StatusMS = &pastMS
	srv.Add(term)
	if st, err := observeRunState(ctx, client, id); err != nil || st != flow.RunIdle {
		t.Fatalf("done observeRunState = %q, %v; want idle", st, err)
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
	if st, err := observeRunState(ctx, client, id); err != nil || st != flow.RunGone {
		t.Fatalf("after StopRun observeRunState = %q, %v; want gone", st, err)
	}
	if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		t.Fatalf("process %d still alive after StopRun", pid)
	}
}

// TestStartRunOmitsDuplicateDisableAutoupdaterEnv checks item 5: DISABLE_AUTOUPDATER
// lives only in ClaudeArgv and is not duplicated in RunParams.Env.
func TestStartRunOmitsDuplicateDisableAutoupdaterEnv(t *testing.T) {
	srv := hctest.New(t)
	var got hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { got = p }
	client := hc.NewClient(srv.Path)

	_, err := startRun(context.Background(), client, RunSpec{
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

// TestStartTerminalRespectsHerdrSpaceLabel checks that StartTerminal sends a
// caller-supplied herdr.space while still generating a fresh herdr.tab per
// terminal and preserving herdr.seen=0, NoParent and NoNotify.
func TestStartTerminalRespectsHerdrSpaceLabel(t *testing.T) {
	srv := hctest.New(t)
	var runs []hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) { runs = append(runs, p) }
	client := hc.NewClient(srv.Path)
	ctx := context.Background()
	cwd := t.TempDir()
	claudeCfg := filepath.Join(t.TempDir(), ".claude.json")
	argv := ClaudeArgv(flow.RoleWorker, "sonnet", "high", "bypassPermissions")

	for _, name := range []string{"worker-space-1", "worker-space-2"} {
		_, _, err := StartTerminal(ctx, client, RunSpec{
			Name:         name,
			Cwd:          cwd,
			ClaudeConfig: claudeCfg,
			Argv:         argv,
			Labels:       map[string]string{"herdr.space": "proj-1a2b3c4d", "sheepdog.role": flow.RoleWorker},
		}, nil)
		if err != nil {
			t.Fatalf("StartTerminal(%s): %v", name, err)
		}
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 terminal.run requests, got %d", len(runs))
	}
	for i, got := range runs {
		if got.Labels["herdr.space"] != "proj-1a2b3c4d" {
			t.Errorf("run %d: herdr.space = %q, want %q", i, got.Labels["herdr.space"], "proj-1a2b3c4d")
		}
		if got.Labels["herdr.tab"] == "" {
			t.Errorf("run %d: herdr.tab is empty", i)
		}
		if got.Labels["herdr.seen"] != "0" {
			t.Errorf("run %d: herdr.seen = %q, want 0", i, got.Labels["herdr.seen"])
		}
		if !got.NoParent || !got.NoNotify {
			t.Errorf("run %d: NoParent=%v NoNotify=%v, want both true", i, got.NoParent, got.NoNotify)
		}
	}
	if runs[0].Labels["herdr.tab"] == runs[1].Labels["herdr.tab"] {
		t.Errorf("both runs got the same herdr.tab %q, want distinct tabs", runs[0].Labels["herdr.tab"])
	}
}

// TestRunStateOutageAndChildProcessGroupStop checks Neon M8 (d) and (e):
// closed hc socket returns an error (never RunGone), and StopRun kills a child
// spawned in the same process group (`sh -c 'sleep 60 & wait'`).
func TestRunStateOutageAndChildProcessGroupStop(t *testing.T) {
	ctx := context.Background()

	// (d) Closed hc socket: ObserveRun returns error, never RunGone.
	deadSrv := hctest.New(t)
	deadClient := hc.NewClient(deadSrv.Path)
	deadSrv.Close()
	if st, err := observeRunState(ctx, deadClient, "t1"); err == nil || st == flow.RunGone {
		t.Fatalf("closed-socket observeRunState = (%q, %v), want non-nil error and not gone", st, err)
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
	// StopRun returns once no process in the group is alive, but a killed child
	// stays a zombie until launchd/init (its new parent) reaps it, and the
	// leader until cmd.Wait does: poll, bounded, for both pids to vanish.
	for deadline := time.Now().Add(5 * time.Second); !errors.Is(syscall.Kill(leaderPID, 0), syscall.ESRCH) || !errors.Is(syscall.Kill(childPID, 0), syscall.ESRCH); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("process group not fully dead: leader=%v child=%v", syscall.Kill(leaderPID, 0), syscall.Kill(childPID, 0))
		}
	}
}

func TestObserveRunScreensActivityAndExit(t *testing.T) {
	realAgyTrust, err := os.ReadFile("../../../testdata/herdr/agy-trust-dialog.screen.txt")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)

	for _, tc := range []struct {
		cli, screen, wantClass string
		wantKeys               []string
	}{
		{"agy", string(realAgyTrust), flow.ScreenTrust, []string{"enter"}},
		{"claude", "Is this a project you created or one you trust?\n❯ No, exit\n  Yes, I trust this folder", flow.ScreenTrust, nil},
		{CodexCLI, "Hooks need review\n1. Yes\n3. Continue without trusting", flow.ScreenConfirm, nil},
		{"claude", "Not logged in · Please run /login", flow.ScreenSignedOut, nil},
		{"claude", "You've hit your limit · resets 2pm", flow.ScreenLimit, nil},
		{"claude", "", flow.ScreenNone, nil},
	} {
		if got := ScreenClass(tc.cli, tc.screen); got != tc.wantClass {
			t.Errorf("ScreenClass(%q, %q) = %q, want %q", tc.cli, tc.screen, got, tc.wantClass)
		}
		if got := flow.ScreenKeys(tc.cli, tc.wantClass); !slices.Equal(got, tc.wantKeys) {
			t.Errorf("ScreenKeys(%q, %q) = %v, want %v", tc.cli, tc.wantClass, got, tc.wantKeys)
		}
	}

	srv.Add(hc.Terminal{Address: "t1", State: "live", CreatedMS: 100, Labels: map[string]string{"sheepdog.run": "r1"}})
	srv.SetScreen("t1", "line 1\n")
	rec := flow.RunRecord{ID: "r1", Address: "t1", CLI: "claude", State: flow.PhaseStarting, StartedAt: time.Now()}
	evs, err := ObserveRun(ctx, client, rec)
	if err != nil || len(evs) != 2 || evs[0].Kind != flow.EvScreen || evs[1].Kind != flow.EvTick || evs[0].ScreenHash == "" {
		t.Fatalf("first ObserveRun = %+v, %v", evs, err)
	}
	rec.ScreenHash = evs[0].ScreenHash
	evs, _ = ObserveRun(ctx, client, rec)
	if slices.ContainsFunc(evs, func(e flow.RunEvent) bool { return e.Kind == flow.EvActivity }) {
		t.Fatalf("unchanged screen emitted activity: %+v", evs)
	}
	srv.SetScreen("t1", "line 1\nline 2\n")
	evs, _ = ObserveRun(ctx, client, rec)
	if !slices.ContainsFunc(evs, func(e flow.RunEvent) bool { return e.Kind == flow.EvActivity }) {
		t.Fatalf("changed screen did not emit activity: %+v", evs)
	}

	if evs, _ = ObserveRun(ctx, client, flow.RunRecord{ID: "other", Address: "t1", State: flow.PhaseWorking}); len(evs) != 1 || evs[0].Kind != flow.EvLost {
		t.Fatalf("mismatched run label = %+v, want [lost]", evs)
	}
	srv.Finish("t1", "killed")
	if evs, _ = ObserveRun(ctx, client, rec); len(evs) != 1 || evs[0].Kind != flow.EvExit || evs[0].Reason != "killed" {
		t.Fatalf("finished terminal = %+v, want [exit killed]", evs)
	}
}

func TestObserveRunScreenEvidence(t *testing.T) {
	dir, err := os.MkdirTemp("", "hcs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "hc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	replies := []map[string]any{
		{"result": hc.ReadResult{Text: "first line", Complete: true}},
		{"result": hc.ReadResult{Text: "first line", Complete: false}},
		{"result": hc.ReadResult{Text: "second line", Complete: false}},
		{"error": map[string]any{"code": -32000, "message": "read failed"}},
		{"result": hc.ReadResult{Complete: true}},
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer func() { _ = conn.Close() }()
				var req struct {
					ID     uint64 `json:"id"`
					Method string `json:"method"`
				}
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					return
				}
				reply := map[string]any{"result": hc.Terminal{Address: "t1", State: "live"}}
				if req.Method == "terminal.read" {
					reply, replies = replies[0], replies[1:]
				}
				reply["jsonrpc"], reply["id"] = "2.0", req.ID
				_ = json.NewEncoder(conn).Encode(reply)
			}()
		}
	}()

	client := hc.NewClient(sock)
	rec := flow.RunRecord{Address: "t1", State: flow.PhaseWorking}
	baseline, err := ObserveRun(t.Context(), client, rec)
	if err != nil || len(baseline) != 1 || baseline[0].Kind != flow.EvTick || baseline[0].ScreenHash == "" {
		t.Fatalf("baseline = %+v, %v, want a tick with a screen hash", baseline, err)
	}
	rec.ScreenHash = baseline[0].ScreenHash
	for _, tc := range []struct {
		name     string
		evidence bool
		activity bool
	}{
		{"unchanged incomplete", true, false},
		{"changed incomplete", true, true},
		{"failed", false, false},
		{"empty", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evs, err := ObserveRun(t.Context(), client, rec)
			if err != nil || len(evs) == 0 {
				t.Fatalf("ObserveRun = %+v, %v, want events ending in a tick", evs, err)
			}
			tick := evs[len(evs)-1]
			activity := slices.ContainsFunc(evs, func(ev flow.RunEvent) bool {
				return ev.Kind == flow.EvActivity && ev.ScreenHash == tick.ScreenHash
			})
			wantEvents := 1
			if tc.activity {
				wantEvents++
			}
			if tick.Kind != flow.EvTick || (tick.ScreenHash != "") != tc.evidence ||
				activity != tc.activity || len(evs) != wantEvents {
				t.Errorf("screen events = %+v, want evidence=%t activity=%t", evs, tc.evidence, tc.activity)
			}
		})
	}
}

func TestScreenClassUIOnlyAndVerifiedKeys(t *testing.T) {
	agyTrust, err := os.ReadFile("../../../testdata/herdr/agy-trust-dialog.screen.txt")
	if err != nil {
		t.Fatal(err)
	}
	claudeTrust, err := os.ReadFile("../../../testdata/herdr/claude-trust-dialog.screen.txt")
	if err != nil {
		t.Fatal(err)
	}
	claudeIdle, err := os.ReadFile("../../../testdata/herdr/claude-idle.screen.txt")
	if err != nil {
		t.Fatal(err)
	}
	proseAboveTail := "⏺ Checked workerlog: usage limit and quota exceeded and You've hit your limit · resets 2pm.\n" +
		strings.Repeat("  normal test output line\n", 14) + string(claudeIdle)
	proseInTail := "⏺ Handled usage limit and quota exceeded in workerlog/errors.go.\n\n" + string(claudeIdle)

	for _, tc := range []struct {
		name, cli, screen, wantClass string
		wantKeys                     []string
	}{
		{"limit prose above tail", "claude", proseAboveTail, flow.ScreenNone, nil},
		{"limit prose inside tail", "claude", proseInTail, flow.ScreenNone, nil},
		{"claude limit UI in tail", "claude", "⎿  You've hit your limit · resets 2pm (America/Los_Angeles)\n" + string(claudeIdle), flow.ScreenLimit, nil},
		{"cross-cli limit ignored", "claude", "Individual quota reached · resets tomorrow\n" + string(claudeIdle), flow.ScreenNone, nil},
		{"agy trust verified enter", "agy", string(agyTrust), flow.ScreenTrust, []string{"enter"}},
		{"claude trust default is No exit", "claude", string(claudeTrust), flow.ScreenTrust, nil},
		{"claude bypass default is No exit", "claude", "WARNING: Claude Code running in Bypass Permissions mode\n❯ 1. No, exit\n  2. Yes, I accept\n", flow.ScreenConfirm, nil},
		{"codex hooks unverified", CodexCLI, "Hooks need review\n1. Review hooks\n3. Continue without trusting\n", flow.ScreenConfirm, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotClass := ScreenClass(tc.cli, tc.screen)
			if gotClass != tc.wantClass {
				t.Errorf("ScreenClass(%q) = %q, want %q", tc.cli, gotClass, tc.wantClass)
			}
			if gotKeys := flow.ScreenKeys(tc.cli, gotClass); !slices.Equal(gotKeys, tc.wantKeys) {
				t.Errorf("ScreenKeys(%q, %q) = %v, want %v", tc.cli, gotClass, gotKeys, tc.wantKeys)
			}
		})
	}
}

// TestScreenClassTrustIsOnlyTheBottomDialog: a trust class (and with it a key)
// needs the whole agy dialog at the bottom of the screen with "Yes" selected.
// The same words quoted in agent output with more output after them, a dialog
// whose cursor sits on "No, exit" and a different Yes option are not trust.
func TestScreenClassTrustIsOnlyTheBottomDialog(t *testing.T) {
	agyTrust, err := os.ReadFile("../../../testdata/herdr/agy-trust-dialog.screen.txt")
	if err != nil {
		t.Fatal(err)
	}
	quoted := "⏺ The start screen of the agent said:\n" +
		"  ⏺ Do you trust the contents of this project?\n" +
		"  > Yes, I trust this folder\n" +
		"    No, exit\n" +
		"⏺ I pressed enter by hand earlier, so I carry on with the task now.\n" +
		"⏺ Reading internal/factory/world/world.go\n" +
		"  Type your message or @path\n\n" +
		"                                            Gemini 3.8 Flash · high\n"
	for _, tc := range []struct {
		name, screen, want string
	}{
		{"real dialog at the bottom", string(agyTrust), flow.ScreenTrust},
		{"quoted in agent output with output after it", quoted, flow.ScreenNone},
		{"quoted inside a long answer, output after it", "⏺ Long answer\n" + quoted, flow.ScreenNone},
		{"cursor on No, exit", "Do you trust the contents of this project?\n\n  Yes, I trust this folder\n> No, exit\n\n  ↑/↓ Navigate · enter Confirm\n   Gemini 3.8 Flash · high\n", flow.ScreenNone},
		{"different Yes option", "Do you trust the contents of this project?\n\n> Yes, trust and allow everything\n  No, exit\n\n  ↑/↓ Navigate · enter Confirm\n   Gemini 3.8 Flash · high\n", flow.ScreenNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScreenClass("agy", tc.screen); got != tc.want {
				t.Errorf("ScreenClass(agy) = %q, want %q", got, tc.want)
			}
			if keys := flow.ScreenKeys("agy", ScreenClass("agy", tc.screen)); (tc.want == flow.ScreenTrust) != (len(keys) == 1 && keys[0] == "enter") {
				t.Errorf("keys = %v for class %q", keys, tc.want)
			}
		})
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
	root1, id1, err := fixtureGit(t).RepoProject(ctx, repo, true)
	if err != nil || id1 == "" || root1 == "" {
		t.Fatalf("RepoProject init = %q, %q, %v", root1, id1, err)
	}
	renamed := filepath.Join(parent, "renamed")
	if err := os.Rename(repo, renamed); err != nil {
		t.Fatal(err)
	}
	_, id2, err := fixtureGit(t).RepoProject(ctx, renamed, false)
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
		if _, id, err := fixtureGit(t).RepoProject(ctx, repo, false); err != nil || id != "" {
			t.Fatalf("RepoProject(%q, init=false) = %q, %v; want empty id", bad, id, err)
		}
	}
	if _, id, err := fixtureGit(t).RepoProject(ctx, repo, true); err != nil || !strings.HasPrefix(id, "proj-") {
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
	if _, err := startRun(ctx, client, RunSpec{Name: "coord-t1", Cwd: cwd, ClaudeConfig: cfgFile, Argv: argv}); err != nil {
		t.Fatalf("startRun with existing config: %v", err)
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
	if _, err := startRun(ctx, client, RunSpec{Name: "coord-t2", Cwd: cwd2, Argv: argv}); err != nil {
		t.Fatalf("startRun with CLAUDE_CONFIG_DIR: %v", err)
	}
	envCfg := filepath.Join(cfgDir, ".claude.json")
	envRaw, err := os.ReadFile(envCfg)
	if err != nil || !strings.Contains(string(envRaw), `"hasTrustDialogAccepted": true`) {
		t.Fatalf("CLAUDE_CONFIG_DIR/.claude.json missing trust (%v):\n%s", err, string(envRaw))
	}

	// 3. Malformed file is left untouched and StartTerminal still succeeds.
	badFile := filepath.Join(t.TempDir(), "malformed.json")
	const broken = "{broken json\n"
	if err := os.WriteFile(badFile, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := startRun(ctx, client, RunSpec{Name: "coord-t3", Cwd: cwd, ClaudeConfig: badFile, Argv: argv}); err != nil {
		t.Fatalf("startRun with malformed config should still proceed: %v", err)
	}
	if after, err := os.ReadFile(badFile); err != nil || string(after) != broken {
		t.Fatalf("malformed file changed: got %q (%v), want %q", string(after), err, broken)
	}

	// 4. Path outside os.TempDir() is refused under go test.
	if _, err := startRun(ctx, client, RunSpec{Name: "coord-t4", Cwd: cwd, ClaudeConfig: "/nonexistent-sheepdog-test-guard/.claude.json", Argv: argv}); err == nil {
		t.Fatal("expected test-path guard error for ClaudeConfig outside os.TempDir()")
	}
}

// TestClaudeTrustSkipsWriteWhenTrusted: an already trusted folder leaves the
// config file untouched (live sessions rewrite it), and a symlinked config
// stays a symlink.
func TestClaudeTrustSkipsWriteWhenTrusted(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, ".claude.json")
	cwd := filepath.Join(dir, "repo")
	body := `{"hasCompletedOnboarding":true,"projects":{"` + cwd + `":{"hasTrustDialogAccepted":true}},"zKeep":1}` + "\n"
	if err := os.WriteFile(real, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := preAcceptClaudeTrust(link, cwd); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(real); string(got) != body {
		t.Fatalf("trusted config was rewritten:\n%s", got)
	}
	if err := preAcceptClaudeTrust(link, filepath.Join(dir, "other")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlinked config was replaced: %v %v", fi, err)
	}
	if got, _ := os.ReadFile(real); !strings.Contains(string(got), "other") {
		t.Fatalf("new folder not trusted through the symlink:\n%s", got)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestNewWorktreeFromRemoteBaseAndIdempotent checks KB-WKT-1, KB-WKT-2,
// KB-WKT-7, KB-MRG-78 and KB-TST-1 on a temporary repo with a bare remote.
func TestNewWorktreeFromRemoteBaseAndIdempotent(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}

	repo := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "init", "--initial-branch=main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init repo: %v: %s", err, out)
	}
	runGit(t, repo, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("v0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "chore: initial")
	runGit(t, repo, "push", "-u", "origin", "main")

	// Advance the bare remote from a second clone (b1), and add an unpushed local commit in repo.
	other := filepath.Join(tmp, "other")
	if out, err := exec.Command("git", "clone", bare, other).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("v1-remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "commit", "-am", "feat: remote base advance")
	runGit(t, other, "push", "origin", "main")
	b1 := runGit(t, other, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(repo, "LOCAL.md"), []byte("unpushed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "LOCAL.md")
	runGit(t, repo, "commit", "-m", "chore: local unpushed")
	localTip := runGit(t, repo, "rev-parse", "HEAD")

	wtDir := filepath.Join(tmp, "data", "factory", "projects", "proj-1", "worktrees", "T1")
	wt1, err := fixtureGit(t).NewWorktree(ctx, repo, wtDir, "T1", "feat: Add Greeting Helper!")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if wt1.Branch != "task/T1-feat-add-greeting-helper" || wt1.BaseCommit != b1 {
		t.Fatalf("wt1 = %+v, want branch task/T1-feat-add-greeting-helper at %s", wt1, b1)
	}
	if _, err := os.Stat(filepath.Join(wt1.Path, "LOCAL.md")); !os.IsNotExist(err) {
		t.Fatal("unpushed local commit leaked into task worktree")
	}
	if runGit(t, repo, "rev-parse", "HEAD") != localTip {
		t.Fatal("repo checkout was modified by NewWorktree")
	}
	if out, err := exec.Command("git", "-C", wt1.Path, "config", "--get", "branch."+wt1.Branch+".merge").CombinedOutput(); err == nil {
		t.Fatalf("task branch tracks upstream %q", string(out))
	}

	// Commit c1 and leave untracked work in wt1, advance remote to b2, and verify idempotent adoption.
	if err := os.WriteFile(filepath.Join(wt1.Path, "greet.go"), []byte("package greet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt1.Path, "add", "greet.go")
	runGit(t, wt1.Path, "commit", "-m", "feat: add greet package")
	c1 := runGit(t, wt1.Path, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(wt1.Path, "untracked.txt"), []byte("wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("v2-remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "commit", "-am", "feat: remote v2")
	runGit(t, other, "push", "origin", "main")

	wt2, err := fixtureGit(t).NewWorktree(ctx, repo, wtDir, "T1", "feat: Add Greeting Helper!")
	if err != nil {
		t.Fatalf("second NewWorktree: %v", err)
	}
	if wt2 != wt1 {
		t.Fatalf("adopted worktree = %+v, want %+v", wt2, wt1)
	}
	if _, err := os.Stat(filepath.Join(wt2.Path, "untracked.txt")); err != nil {
		t.Fatalf("adoption lost uncommitted file: %v", err)
	}

	// InspectWorktree: dirty vs clean and commit relations.
	tip, rel, clean, err := fixtureGit(t).InspectWorktree(ctx, wt1.Path, b1)
	if err != nil || tip != c1 || rel != flow.RelAhead || clean {
		t.Fatalf("dirty InspectWorktree = (%s, %s, %v, %v)", tip, rel, clean, err)
	}
	if err := os.Remove(filepath.Join(wt1.Path, "untracked.txt")); err != nil {
		t.Fatal(err)
	}
	if _, rel, clean, err = fixtureGit(t).InspectWorktree(ctx, wt1.Path, c1); err != nil || rel != flow.RelSame || !clean {
		t.Fatalf("clean same InspectWorktree = (%s, %v, %v)", rel, clean, err)
	}

	// Guard: refusing worktree outside os.TempDir() under go test.
	if _, err := fixtureGit(t).NewWorktree(ctx, repo, "/nonexistent-sheepdog-test-guard/wt", "T2", "feat: x"); err == nil {
		t.Fatal("expected test guard error outside os.TempDir()")
	}
}

// TestNewWorktreeUsesRemoteDefaultBranchNotUpstream checks KB-WKT-1: when the
// owner checkout is on a feature branch tracking origin/feature and origin/HEAD
// is unset, NewWorktree branches from origin's default branch, never @{u}.
func TestNewWorktreeUsesRemoteDefaultBranchNotUpstream(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	repo := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "init", "--initial-branch=main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init repo: %v: %s", err, out)
	}
	runGit(t, repo, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "chore: initial main")
	runGit(t, repo, "push", "-u", "origin", "main")
	mainSHA := runGit(t, repo, "rev-parse", "HEAD")

	runGit(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "FEATURE.md"), []byte("wip\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "FEATURE.md")
	runGit(t, repo, "commit", "-m", "feat: feature work")
	runGit(t, repo, "push", "-u", "origin", "feature")
	runGit(t, repo, "config", "remote.origin.followRemoteHead", "never")
	_ = exec.Command("git", "-C", repo, "remote", "set-head", "origin", "-d").Run()

	wt, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(tmp, "wt-1"), "t1", "feat: task on default")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if wt.BaseCommit != mainSHA {
		t.Fatalf("BaseCommit = %s, want remote default branch %s", wt.BaseCommit, mainSHA)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "FEATURE.md")); !os.IsNotExist(err) {
		t.Fatal("feature branch file leaked into task worktree")
	}
}

// TestInspectWorktreeShowsUntrackedDespiteGitConfig checks KB-MRG-78: a repo or
// user config setting status.showUntrackedFiles=no must not hide untracked files.
func TestInspectWorktreeShowsUntrackedDespiteGitConfig(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	repo := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "init", "--initial-branch=main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init repo: %v: %s", err, out)
	}
	runGit(t, repo, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("v0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "chore: initial")
	runGit(t, repo, "push", "-u", "origin", "main")
	runGit(t, repo, "config", "status.showUntrackedFiles", "no")

	wt, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(tmp, "wt-1"), "t1", "feat: dirty check")
	if err != nil {
		t.Fatalf("NewWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "untracked.tmp"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, clean, err := fixtureGit(t).InspectWorktree(ctx, wt.Path, wt.BaseCommit); err != nil || clean {
		t.Fatalf("InspectWorktree with status.showUntrackedFiles=no: clean=%v err=%v, want clean=false", clean, err)
	}
}

// TestReadReportLiveDemoScreen replays the live demo's reviewer screen: the
// findings sit in the tag with unescaped quotes, and a draft waits in the input
// box. The tag's findings field is ignored; the findings are the reply text.
func TestReadReportLiveDemoScreen(t *testing.T) {
	screen, err := os.ReadFile("testdata/demo3-reviewer-screen.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := parseReportText(string(screen), "6knsmxa2re")
	if got.Status != ReportFound || got.Report.Verdict != "changes" || got.Report.Majors != 1 || got.Report.Round != 1 ||
		got.Report.Head != "657ce5090879ae2be234dc0f72bf952084c252e6" {
		t.Fatalf("live screen = %+v, want found changes/1", got)
	}
	if !strings.Contains(got.Body, `Hello("world")`) || !strings.HasPrefix(got.Body, "I found one major issue") ||
		strings.Contains(got.Body, "<report>") || strings.Contains(got.Body, "Ran 5 shell commands") {
		t.Fatalf("live screen body = %q, want the reply text above the tag", got.Body)
	}
	// The prompt echo's template tag alone is never taken.
	echoOnly := string(screen)[:strings.Index(string(screen), "⏺ I found")]
	if rd := parseReportText(echoOnly, "6knsmxa2re"); rd.Status != ReportNotYet {
		t.Fatalf("echo-only screen = %+v, want not_yet", rd)
	}
	// New contract: findings as plain text above a tag without a findings field.
	rd := parseReportText("❯ prompt\n  <report>{\"code\":\"abc123\",\"round\":1,\"head\":\"h1\",\"verdict\":\"pass|changes\",\"majors\":0}</report>\n\n"+
		"⏺ major: Hello(\"\") returns \"hello, \"\n  minor: no doc comment\n\n  <report>{\"code\":\"abc123\",\"round\":2,\"head\":\"h1\",\"verdict\":\"changes\",\"majors\":1}</report>\n", "abc123")
	if rd.Status != ReportFound || rd.Report.Round != 2 || rd.Report.Majors != 1 || rd.Body != "major: Hello(\"\") returns \"hello, \"\n  minor: no doc comment" {
		t.Fatalf("plain-text findings = %+v", rd)
	}
}

// TestClaudeArgvNeverSwallowsPrompt: no argv may end with a flag that takes
// several values, or the prompt StartRun appends becomes one of them (seen
// live: a reviewer started with an empty prompt).
func TestClaudeArgvNeverSwallowsPrompt(t *testing.T) {
	argvs := [][]string{
		ClaudeArgv(flow.RoleCoordinator, "opus", "medium", "bypassPermissions"),
		ClaudeArgv(flow.RoleWorker, "sonnet", "medium", "bypassPermissions"),
		ClaudeReviewerArgv("opus", "medium", "sheepdog", "/tmp/draft.md"),
	}
	for _, argv := range argvs {
		for _, a := range argv {
			if a == "--allowedTools" || a == "--disallowedTools" || a == "--allowed-tools" {
				t.Errorf("%q is a separate argument; use --flag=value so the prompt is not swallowed: %q", a, argv)
			}
		}
	}
}

// TestReviewRangesExcludeMergedBaseChanges checks design.md 9.4, KB-REV-12 and
// KB-REV-13 on a temporary repo where the remote base advances and is merged
// into the task branch: both full (MergeBase..H) and delta (DeltaRanges) diffs
// contain only the task's own changes and leave out what the base brought in.
func TestReviewRangesExcludeMergedBaseChanges(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	repo := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "init", "--initial-branch=main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init repo: %v: %s", err, out)
	}
	runGit(t, repo, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("v0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "chore: b0")
	runGit(t, repo, "push", "-u", "origin", "main")
	b0 := runGit(t, repo, "rev-parse", "HEAD")

	wt, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(tmp, "wt-1"), "t1", "feat: task")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "task.go"), []byte("package demo\nvar V = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt.Path, "add", "task.go")
	runGit(t, wt.Path, "commit", "-m", "feat: c1")
	c1 := runGit(t, wt.Path, "rev-parse", "HEAD")

	// Remote base advances to b1 in another clone (unmerged so far).
	other := filepath.Join(tmp, "other")
	if out, err := exec.Command("git", "clone", bare, other).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(other, "base_only.go"), []byte("package demo\nvar Base = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "base_only.go")
	runGit(t, other, "commit", "-m", "feat: base b1")
	runGit(t, other, "push", "origin", "main")
	b1 := runGit(t, other, "rev-parse", "HEAD")

	// Full round at c1 while base is ahead at b1: merge base is still b0.
	if mb, err := fixtureGit(t).MergeBase(ctx, wt.Path, c1); err != nil || mb != b0 {
		t.Fatalf("MergeBase(c1) = (%s, %v), want %s", mb, err, b0)
	}

	// Worker commits c2a, fetches and merges origin/main (b1) as m1, then commits c2b;
	// meanwhile remote base advances again to b2.
	if err := os.WriteFile(filepath.Join(wt.Path, "pre.go"), []byte("package demo\nvar Pre = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt.Path, "add", "pre.go")
	runGit(t, wt.Path, "commit", "-m", "fix: pre merge")
	c2a := runGit(t, wt.Path, "rev-parse", "HEAD")

	runGit(t, wt.Path, "fetch", "origin")
	runGit(t, wt.Path, "merge", "--no-edit", "origin/main")
	m1 := runGit(t, wt.Path, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(wt.Path, "task.go"), []byte("package demo\nvar V = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt.Path, "commit", "-am", "fix: post merge")
	c2b := runGit(t, wt.Path, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(other, "base_later.go"), []byte("package demo\nvar Later = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "base_later.go")
	runGit(t, other, "commit", "-m", "feat: base b2")
	runGit(t, other, "push", "origin", "main")

	// merge_only (design.md 3.1): a clean base merge is; a normal commit or a
	// diverged pair is not. Changed lines leave out what the base brought in.
	for _, c := range []struct {
		x, h string
		want bool
	}{{c2a, m1, true}, {c1, m1, false}, {m1, c2b, false}, {c2a, b1, false}} {
		if got, err := fixtureGit(t).MergeOnly(ctx, wt.Path, c.x, c.h); err != nil || got != c.want {
			t.Errorf("MergeOnly(%s, %s) = (%v, %v), want %v", c.x, c.h, got, err, c.want)
		}
	}
	for _, c := range []struct {
		tip  string
		want int
	}{{c1, 2}, {m1, 4}, {c2b, 4}} { // new files task.go and pre.go, 2 lines each; base_only.go never counts
		if n, gen, err := fixtureGit(t).ChangedLines(ctx, wt.Path, c.tip); err != nil || n != c.want || gen != 0 {
			t.Errorf("ChangedLines(%s) = (%d, %d, %v), want (%d, 0, nil)", c.tip, n, gen, err, c.want)
		}
	}

	// Delta from c1 to m1 (fix then merge): [c1..c2a], no base_only.go.
	r1, err := fixtureGit(t).DeltaRanges(ctx, wt.Path, c1, m1)
	if err != nil || !slices.Equal(r1, []string{c1 + ".." + c2a}) {
		t.Fatalf("fixtureGit(t).DeltaRanges(c1, m1) = (%v, %v), want [%s..%s]", r1, err, c1, c2a)
	}

	// Delta from c1 to c2b (fix, merge, fix): [c1..c2a, m1..c2b], leaving out b1 and b2.
	ranges, err := fixtureGit(t).DeltaRanges(ctx, wt.Path, c1, c2b)
	if err != nil || !slices.Equal(ranges, []string{c1 + ".." + c2a, m1 + ".." + c2b}) {
		t.Fatalf("fixtureGit(t).DeltaRanges(c1, c2b) = (%v, %v)", ranges, err)
	}
	var deltaDiff strings.Builder
	for _, r := range ranges {
		deltaDiff.WriteString(runGit(t, wt.Path, "diff", r) + "\n")
	}
	for _, bad := range []string{"base_only.go", "base_later.go", "+var V = 1"} {
		if strings.Contains(deltaDiff.String(), bad) {
			t.Errorf("delta diff leaked %q:\n%s", bad, deltaDiff.String())
		}
	}
	for _, want := range []string{"pre.go", "-var V = 1", "+var V = 2"} {
		if !strings.Contains(deltaDiff.String(), want) {
			t.Errorf("delta diff missing %q:\n%s", want, deltaDiff.String())
		}
	}

	// Full at c2b (L8 second pass): MergeBase computed now is b1, leaving out b1 and b2.
	mb, err := fixtureGit(t).MergeBase(ctx, wt.Path, c2b)
	if err != nil || mb != b1 {
		t.Fatalf("MergeBase(c2b) = (%s, %v), want %s", mb, err, b1)
	}
	fullDiff := runGit(t, wt.Path, "diff", mb+".."+c2b)
	for _, bad := range []string{"base_only.go", "base_later.go"} {
		if strings.Contains(fullDiff, bad) {
			t.Errorf("full diff leaked %q:\n%s", bad, fullDiff)
		}
	}
	for _, want := range []string{"pre.go", "+var V = 2"} {
		if !strings.Contains(fullDiff, want) {
			t.Errorf("full diff missing %q:\n%s", want, fullDiff)
		}
	}
}

// TestDeltaRangesIncludesHandEditedBaseMerge checks design.md 3.1: a merge of
// the remote base is merge_only only when its tree equals git's own merge of
// its parents; a conflicting base merge resolved by hand or a merge with extra
// edits stays in the delta range so its edits are reviewed.
func TestDeltaRangesIncludesHandEditedBaseMerge(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	repo := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "init", "--initial-branch=main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init repo: %v: %s", err, out)
	}
	runGit(t, repo, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(repo, "shared.go"), []byte("package demo\nvar Shared = \"v0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "shared.go")
	runGit(t, repo, "commit", "-m", "chore: b0")
	runGit(t, repo, "push", "-u", "origin", "main")

	wt, err := fixtureGit(t).NewWorktree(ctx, repo, filepath.Join(tmp, "wt-1"), "t1", "feat: task")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "shared.go"), []byte("package demo\nvar Shared = \"worker-c1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt.Path, "commit", "-am", "feat: c1")
	c1 := runGit(t, wt.Path, "rev-parse", "HEAD")

	// Remote base makes a conflicting edit to shared.go at b1.
	other := filepath.Join(tmp, "other")
	if out, err := exec.Command("git", "clone", bare, other).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(other, "shared.go"), []byte("package demo\nvar Shared = \"base-b1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "commit", "-am", "feat: base conflict b1")
	runGit(t, other, "push", "origin", "main")

	// Worker merges origin/main, hits conflict, and resolves it by hand as m1.
	runGit(t, wt.Path, "fetch", "origin")
	// Same identity env as runGit: on a machine with no global git identity
	// (CI) a bare merge fails before merging, and the test would then build
	// a plain commit instead of a merge (seen in CI).
	merge := exec.Command("git", "-C", wt.Path, "merge", "origin/main")
	merge.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := merge.CombinedOutput(); err == nil || !strings.Contains(string(out), "CONFLICT") {
		t.Fatalf("expected a merge conflict on shared.go, got err=%v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "shared.go"), []byte("package demo\nvar Shared = \"hand-resolved\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt.Path, "add", "shared.go")
	runGit(t, wt.Path, "commit", "-m", "merge: resolve shared.go")
	m1 := runGit(t, wt.Path, "rev-parse", "HEAD")

	ranges, err := fixtureGit(t).DeltaRanges(ctx, wt.Path, c1, m1)
	if err != nil || !slices.Equal(ranges, []string{c1 + ".." + m1}) {
		t.Fatalf("fixtureGit(t).DeltaRanges(c1, m1) = (%v, %v), want [%s..%s]", ranges, err, c1, m1)
	}
	diff := runGit(t, wt.Path, "diff", ranges[0])
	if !strings.Contains(diff, `+var Shared = "hand-resolved"`) {
		t.Fatalf("delta diff missing conflict resolution:\n%s", diff)
	}

	// Extra hand edit inside an otherwise clean base merge m2 also stays in the delta.
	if err := os.WriteFile(filepath.Join(other, "base2.go"), []byte("package demo\nvar Base2 = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "base2.go")
	runGit(t, other, "commit", "-m", "feat: base b2")
	runGit(t, other, "push", "origin", "main")

	runGit(t, wt.Path, "fetch", "origin")
	runGit(t, wt.Path, "merge", "--no-commit", "--no-ff", "origin/main")
	if err := os.WriteFile(filepath.Join(wt.Path, "smuggled.go"), []byte("package demo\nvar Smuggled = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt.Path, "add", "smuggled.go")
	runGit(t, wt.Path, "commit", "-m", "merge: with extra edit")
	m2 := runGit(t, wt.Path, "rev-parse", "HEAD")

	for _, pair := range [][2]string{{c1, m1}, {m1, m2}} { // hand-resolved, and an extra edit in a merge
		if mo, err := fixtureGit(t).MergeOnly(ctx, wt.Path, pair[0], pair[1]); err != nil || mo {
			t.Errorf("MergeOnly(%s, %s) = (%v, %v), want false", pair[0], pair[1], mo, err)
		}
	}

	ranges2, err := fixtureGit(t).DeltaRanges(ctx, wt.Path, m1, m2)
	if err != nil || !slices.Equal(ranges2, []string{m1 + ".." + m2}) {
		t.Fatalf("fixtureGit(t).DeltaRanges(m1, m2) = (%v, %v), want [%s..%s]", ranges2, err, m1, m2)
	}
	if diff2 := runGit(t, wt.Path, "diff", ranges2[0]); !strings.Contains(diff2, "smuggled.go") {
		t.Fatalf("delta diff missing extra merge edit:\n%s", diff2)
	}
}

// sizeRepo is a repo with a bare origin and one base commit; it returns a
// task worktree from it.
func sizeRepo(t *testing.T) string {
	t.Helper()
	wt := sizeRepoWithBase(t, nil)
	return wt
}

// sizeRepoWithBase builds a repo with a bare origin whose pushed base commit
// holds README.md plus baseFiles, and returns the main checkout and a task
// worktree branched from origin/main.
func sizeRepoWithBase(t *testing.T, baseFiles map[string]string) (wt string) {
	t.Helper()
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	repo := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "init", "--initial-branch=main", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	runGit(t, repo, "remote", "add", "origin", bare)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("v0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range baseFiles {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "chore: b0")
	runGit(t, repo, "push", "-u", "origin", "main")
	w, err := fixtureGit(t).NewWorktree(context.Background(), repo, filepath.Join(tmp, "wt-1"), "t1", "feat: task")
	if err != nil {
		t.Fatal(err)
	}
	return w.Path
}

func commitFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", "feat: "+name)
	return runGit(t, dir, "rev-parse", "HEAD")
}

func numberedLines(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(prefix)
		b.WriteString(strconv.Itoa(i))
		b.WriteByte('\n')
	}
	return b.String()
}

// TestChangedLinesCountsBinaryMarkedFiles: neither a committed -diff
// attribute (on the base or on the branch) nor a NUL byte hides a file's lines
// from the size cap (design.md 7.3).
func TestChangedLinesCountsBinaryMarkedFiles(t *testing.T) {
	wt := sizeRepo(t)
	commitFile(t, wt, ".gitattributes", "*.gen -diff\n")
	commitFile(t, wt, "big.gen", strings.Repeat("x\n", 100))
	tip := commitFile(t, wt, "nul.txt", "a\x00b\nc\n") // a NUL byte makes git call it binary
	if n, gen, err := fixtureGit(t).ChangedLines(context.Background(), wt, tip); err != nil || n != 103 || gen != 0 {
		t.Fatalf("ChangedLines = (%d, %d, %v), want (103, 0, nil)", n, gen, err)
	}

	// A -diff attribute on the base branch itself also stays counted.
	baseWT := sizeRepoWithBase(t, map[string]string{
		".gitattributes": "*.gen -diff\n",
	})
	commitFile(t, baseWT, "big.gen", strings.Repeat("x\n", 100))
	baseTip := commitFile(t, baseWT, "nul.txt", "a\x00b\nc\n")
	if n, gen, err := fixtureGit(t).ChangedLines(context.Background(), baseWT, baseTip); err != nil || n != 102 || gen != 0 {
		t.Fatalf("ChangedLines with base -diff = (%d, %d, %v), want (102, 0, nil)", n, gen, err)
	}
}

// TestChangedLinesLeavesOutBaseGeneratedFiles checks design.md 7.3 and
// KB-CAP-16: files marked linguist-generated (set or =true) in the base
// branch's committed .gitattributes are excluded from ChangedLines and reported
// separately as generated lines.
func TestChangedLinesLeavesOutBaseGeneratedFiles(t *testing.T) {
	wt := sizeRepoWithBase(t, map[string]string{
		".gitattributes":  "gen/project.pbx linguist-generated\ndeps.lock linguist-generated=true\n",
		"gen/project.pbx": numberedLines("old-pbx-", 50),
		"deps.lock":       numberedLines("old-lock-", 10),
	})
	// Rewriting 50 lines -> 50 removed + 50 added = 100; 10 lines -> 20.
	commitFile(t, wt, "gen/project.pbx", numberedLines("new-pbx-", 50))
	commitFile(t, wt, "deps.lock", numberedLines("new-lock-", 10))
	tip := commitFile(t, wt, "src.go", numberedLines("src-", 7))
	if lines, gen, err := fixtureGit(t).ChangedLines(context.Background(), wt, tip); err != nil || lines != 7 || gen != 120 {
		t.Fatalf("ChangedLines = (%d, %d, %v), want (7, 120, nil)", lines, gen, err)
	}
}

// TestChangedLinesIgnoresAttributesTheWorkerWrites checks design.md 7.3:
// attributes committed on the task branch, left uncommitted in the worktree, or
// configured via core.attributesFile in .git/config never exempt any lines.
func TestChangedLinesIgnoresAttributesTheWorkerWrites(t *testing.T) {
	ctx := context.Background()

	t.Run("tip commits .gitattributes", func(t *testing.T) {
		wt := sizeRepo(t)
		commitFile(t, wt, ".gitattributes", "*.swift linguist-generated\n")
		tip := commitFile(t, wt, "app.swift", numberedLines("let x = ", 10))
		if lines, gen, err := fixtureGit(t).ChangedLines(ctx, wt, tip); err != nil || lines != 11 || gen != 0 {
			t.Fatalf("ChangedLines = (%d, %d, %v), want (11, 0, nil)", lines, gen, err)
		}
	})

	t.Run("uncommitted worktree .gitattributes", func(t *testing.T) {
		wt := sizeRepo(t)
		tip := commitFile(t, wt, "app.swift", numberedLines("let x = ", 10))
		if err := os.WriteFile(filepath.Join(wt, ".gitattributes"), []byte("* linguist-generated\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if lines, gen, err := fixtureGit(t).ChangedLines(ctx, wt, tip); err != nil || lines != 10 || gen != 0 {
			t.Fatalf("ChangedLines = (%d, %d, %v), want (10, 0, nil)", lines, gen, err)
		}
	})

	t.Run("core.attributesFile in git config", func(t *testing.T) {
		wt := sizeRepo(t)
		tip := commitFile(t, wt, "app.swift", numberedLines("let x = ", 10))
		attrFile := filepath.Join(t.TempDir(), "custom.gitattributes")
		if err := os.WriteFile(attrFile, []byte("*.swift linguist-generated\n*.swift linguist-generated=true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, wt, "config", "--local", "core.attributesFile", attrFile)
		if lines, gen, err := fixtureGit(t).ChangedLines(ctx, wt, tip); err != nil || lines != 10 || gen != 0 {
			t.Fatalf("ChangedLines = (%d, %d, %v), want (10, 0, nil)", lines, gen, err)
		}
	})
}

// TestChangedLinesCountsUnsetGenerated checks design.md 7.3 and KB-CAP-16:
// a file explicitly unset with -linguist-generated in the base's .gitattributes
// stays counted while its generated sibling is left out.
func TestChangedLinesCountsUnsetGenerated(t *testing.T) {
	wt := sizeRepoWithBase(t, map[string]string{
		".gitattributes": "gen/* linguist-generated\ngen/keep.txt -linguist-generated\n",
	})
	commitFile(t, wt, "gen/skip.txt", numberedLines("skip-", 40))
	tip := commitFile(t, wt, "gen/keep.txt", numberedLines("keep-", 5))
	if lines, gen, err := fixtureGit(t).ChangedLines(context.Background(), wt, tip); err != nil || lines != 5 || gen != 40 {
		t.Fatalf("ChangedLines = (%d, %d, %v), want (5, 40, nil)", lines, gen, err)
	}
}

// TestChangedLinesRenameIntoGeneratedPathStillCounts checks design.md 7.3:
// moving an existing base file into a generated path counts at least the whole
// file's lines (errs high) rather than hiding them.
func TestChangedLinesRenameIntoGeneratedPathStillCounts(t *testing.T) {
	wt := sizeRepoWithBase(t, map[string]string{
		".gitattributes": "gen/* linguist-generated\n",
		"hand.go":        numberedLines("hand-", 300),
	})
	if err := os.MkdirAll(filepath.Join(wt, "gen"), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, wt, "mv", "hand.go", "gen/hand.go")
	runGit(t, wt, "commit", "-m", "refactor: move hand.go into gen")
	tip := runGit(t, wt, "rev-parse", "HEAD")
	if lines, _, err := fixtureGit(t).ChangedLines(context.Background(), wt, tip); err != nil || lines < 300 {
		t.Fatalf("ChangedLines after rename into generated path = (%d, %v), want >= 300", lines, err)
	}
}

// TestChangedLinesAttributeReadFailure checks design.md 7.3 failure classes on
// the base-attributes diff call (carrying GIT_ATTR_SOURCE in env): a clean
// non-zero git exit falls back to (total, 0, nil); an RPC error returns a
// non-ErrChangedLinesOverCap error; a timeout returns ErrChangedLinesOverCap.
func TestChangedLinesAttributeReadFailure(t *testing.T) {
	ctx := context.Background()
	wt := sizeRepoWithBase(t, map[string]string{
		".gitattributes": "gen/big.pbx linguist-generated\n",
	})
	commitFile(t, wt, "gen/big.pbx", numberedLines("gen-", 20))
	tip := commitFile(t, wt, "src.go", numberedLines("src-", 5))
	const total = 25

	isAttrDiff := func(p hc.ExecParams) bool {
		return len(p.Argv) > 1 && p.Argv[0] == "git" && p.Argv[1] == "diff" && p.Env["GIT_ATTR_SOURCE"] != ""
	}

	t.Run("exit 1 falls back to total", func(t *testing.T) {
		srv := hctest.New(t)
		var attrCalls atomic.Int32
		srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			if isAttrDiff(p) {
				attrCalls.Add(1)
				code := 1
				return hc.ExecResult{Code: &code, Stderr: []byte("attr diff failed\n")}, nil
			}
			return hctest.GitExec(p)
		}
		g := NewGit(hc.NewClient(srv.Path), nil, nil)
		lines, gen, err := g.ChangedLines(ctx, wt, tip)
		if attrCalls.Load() != 1 || err != nil || lines != total || gen != 0 {
			t.Fatalf("ChangedLines = (%d, %d, %v) with attrCalls=%d, want (%d, 0, nil) with 1 attr diff call", lines, gen, err, attrCalls.Load(), total)
		}
	})

	t.Run("RPC error returns non-over-cap error", func(t *testing.T) {
		srv := hctest.New(t)
		var attrCalls atomic.Int32
		srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			if isAttrDiff(p) {
				attrCalls.Add(1)
				return hc.ExecResult{}, errors.New("lost machine.exec reply")
			}
			return hctest.GitExec(p)
		}
		g := NewGit(hc.NewClient(srv.Path), nil, nil)
		_, _, err := g.ChangedLines(ctx, wt, tip)
		if attrCalls.Load() != 1 || err == nil || errors.Is(err, ErrChangedLinesOverCap) {
			t.Fatalf("ChangedLines on attr diff RPC error = %v (attrCalls=%d), want non-ErrChangedLinesOverCap error after 1 attr diff call", err, attrCalls.Load())
		}
	})

	t.Run("timeout returns ErrChangedLinesOverCap", func(t *testing.T) {
		srv := hctest.New(t)
		var attrCalls atomic.Int32
		srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
			if isAttrDiff(p) {
				attrCalls.Add(1)
				return hc.ExecResult{TimedOut: true}, nil
			}
			return hctest.GitExec(p)
		}
		g := NewGit(hc.NewClient(srv.Path), nil, nil)
		_, _, err := g.ChangedLines(ctx, wt, tip)
		if attrCalls.Load() != 1 || !errors.Is(err, ErrChangedLinesOverCap) {
			t.Fatalf("ChangedLines on attr diff timeout = %v (attrCalls=%d), want ErrChangedLinesOverCap after 1 attr diff call", err, attrCalls.Load())
		}
	})
}

// TestChangedLinesOldGitCountsEverything checks design.md 7.3: when git ignores
// GIT_ATTR_SOURCE (check-attr probe exits 0), ChangedLines counts every line,
// never passes GIT_ATTR_SOURCE to diff, and runs the probe once across reads.
func TestChangedLinesOldGitCountsEverything(t *testing.T) {
	ctx := context.Background()
	wt := sizeRepoWithBase(t, map[string]string{
		".gitattributes": "gen/big.pbx linguist-generated\n",
	})
	commitFile(t, wt, "gen/big.pbx", numberedLines("gen-", 20))
	tip := commitFile(t, wt, "src.go", numberedLines("src-", 5))

	srv := hctest.New(t)
	var probeCalls, attrDiffCalls atomic.Int32
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if len(p.Argv) > 1 && p.Argv[0] == "git" {
			if p.Argv[1] == "check-attr" {
				probeCalls.Add(1)
				code := 0
				return hc.ExecResult{Code: &code, Stdout: []byte(".: linguist-generated: unspecified\n")}, nil
			}
			if p.Argv[1] == "diff" && p.Env["GIT_ATTR_SOURCE"] != "" {
				attrDiffCalls.Add(1)
			}
		}
		return hctest.GitExec(p)
	}
	g := NewGit(hc.NewClient(srv.Path), nil, nil)
	for i := range 2 {
		lines, gen, err := g.ChangedLines(ctx, wt, tip)
		if err != nil || lines != 25 || gen != 0 {
			t.Fatalf("read %d: ChangedLines = (%d, %d, %v), want (25, 0, nil)", i+1, lines, gen, err)
		}
	}
	if attrDiffCalls.Load() != 0 {
		t.Errorf("diff with GIT_ATTR_SOURCE called %d times on old git, want 0", attrDiffCalls.Load())
	}
	if probeCalls.Load() != 1 {
		t.Errorf("check-attr probe called %d times across 2 reads, want 1", probeCalls.Load())
	}
}

// TestMergeOnlyEdgeCases: unrelated histories, an empty range under two
// spellings of one commit, and a clean merge of a branch that is not the
// base are all "not merge_only", and none is an error.
func TestMergeOnlyEdgeCases(t *testing.T) {
	ctx := context.Background()
	wt := sizeRepo(t)
	base := runGit(t, wt, "rev-parse", "HEAD")
	c1 := commitFile(t, wt, "a.go", "package a\n")

	runGit(t, wt, "checkout", "-q", "-b", "side", base)
	side := commitFile(t, wt, "side.go", "package side\n")
	runGit(t, wt, "checkout", "-q", "-")
	runGit(t, wt, "merge", "--no-ff", "--no-edit", side)
	m := runGit(t, wt, "rev-parse", "HEAD")

	runGit(t, wt, "checkout", "-q", "--orphan", "orphan")
	orphan := commitFile(t, wt, "o.go", "package o\n")

	for _, c := range []struct{ name, x, h string }{
		{"unrelated histories", c1, orphan},
		{"empty range", c1[:12], c1}, // two spellings of one commit
		{"clean merge of a non-base branch", c1, m},
	} {
		if mo, err := fixtureGit(t).MergeOnly(ctx, wt, c.x, c.h); err != nil || mo {
			t.Errorf("%s: MergeOnly = (%v, %v), want (false, nil)", c.name, mo, err)
		}
	}
}

// TestChangedLinesTimeoutReturnsDeadlineExceeded: when a running git diff is
// killed mid-run by its child deadline, ChangedLines returns an error
// matching context.DeadlineExceeded so readTipLocked treats it as over
// size_cap.
func TestChangedLinesTimeoutReturnsDeadlineExceeded(t *testing.T) {
	wt := sizeRepo(t)
	tip := commitFile(t, wt, "a.go", "package a\n")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := t.TempDir()
	script := "#!/bin/sh\nfor arg in \"$@\"; do\n  if [ \"$arg\" = \"diff\" ]; then exec sleep 2; fi\ndone\nexec \"" + realGit + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	srv := hctest.New(t)
	srv.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
		if p.Argv[1] == "diff" {
			limit := 0.15
			p.Timeout = &limit
		}
		return hctest.GitExec(p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	git := NewGit(hc.NewClient(srv.Path), nil, nil)
	_, _, err = git.ChangedLines(ctx, wt, tip)
	if !GitTimedOut(err) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrChangedLinesOverCap) {
		t.Fatalf("ChangedLines under hung git diff = %v, want a confirmed diff timeout treated as over cap", err)
	}
}

func TestStartTerminalDelegatesOwnershipToCaller(t *testing.T) {
	client := hc.NewClient(hctest.New(t).Path)
	ctx := context.Background()
	spec := RunSpec{Name: "review", Cwd: t.TempDir(), Argv: []string{"fixture"}}
	first, _, err := StartTerminal(ctx, client, spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	minted := false
	second, _, err := StartTerminal(ctx, client, spec, func() error {
		minted = true
		return nil
	})
	if err != nil || !minted || first.Address == second.Address {
		t.Fatalf("StartTerminal reused a live terminal: minted=%t first=%q second=%q err=%v", minted, first.Address, second.Address, err)
	}
}
