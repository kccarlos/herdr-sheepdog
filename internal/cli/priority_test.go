package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestRunnerExecutesAtUtilityPriority proves CLI sub-agents (run_cli,
// repository investigations, and worker-fork investigations) run at OS
// utility priority (QOS_CLASS_UTILITY = 0x11 on macOS, never background 0x09;
// positive oom_score_adj on Linux) while keeping logged argv and stderr clean.
func TestRunnerExecutesAtUtilityPriority(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "prio-probe.sh")

	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat("/usr/sbin/taskpolicy"); err != nil {
			if _, lerr := exec.LookPath("taskpolicy"); lerr != nil {
				t.Skip("taskpolicy not present on this darwin host")
			}
		}
		py, err := exec.LookPath("python3")
		if err != nil {
			t.Skip("python3 not present to inspect qos_class_self()")
		}
		script := "#!/bin/sh\nset -eu\n" +
			"qos=$(\"" + py + "\" -c 'import ctypes; print(hex(ctypes.CDLL(\"/usr/lib/libSystem.B.dylib\").qos_class_self()))')\n" +
			"printf 'QOS:%s;ARGS:%s\\n' \"$qos\" \"$*\"\n"
		if err := os.WriteFile(probe, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	case "linux":
		script := "#!/bin/sh\nset -eu\n" +
			"oom=$(cat /proc/self/oom_score_adj)\n" +
			"printf 'OOM:%s;ARGS:%s\\n' \"$oom\" \"$*\"\n"
		if err := os.WriteFile(probe, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	default:
		t.Skipf("priority inspection not supported on %s", runtime.GOOS)
	}

	log := &recLog{}
	cfg := Config{
		Prefer:          "codex",
		CodexBin:        probe,
		ClaudeBin:       probe,
		CodexCmd:        []string{probe, "{question}"},
		ClaudeCmd:       []string{probe, "{question}"},
		CodexResumeCmd:  []string{probe, "resume", "{session}", "{question}"},
		ClaudeResumeCmd: []string{probe, "fork", "{session}", "{question}"},
		Log:             log,
	}
	r := newTestRunner(t, cfg, Detection{Codex: true, Claude: true})

	checkOutput := func(label, raw string) {
		t.Helper()
		switch runtime.GOOS {
		case "darwin":
			// QOS_CLASS_UTILITY is 0x11 (QOS_CLASS_DEFAULT is 0x15, QOS_CLASS_BACKGROUND is 0x9).
			if got := fieldOf(t, strings.TrimSpace(raw), "QOS"); got != "0x11" {
				t.Errorf("%s: qos_class_self = %q, want 0x11 (QOS_CLASS_UTILITY)", label, got)
			}
		case "linux":
			got := fieldOf(t, strings.TrimSpace(raw), "OOM")
			n, err := strconv.Atoi(got)
			if err != nil || n < 500 {
				t.Errorf("%s: oom_score_adj = %q, want >= 500", label, got)
			}
		}
	}

	// 1. Direct CLI query (run_cli).
	raw, err := r.Query(context.Background(), dir, "check-query")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	checkOutput("Query", raw)

	// 2. Repository investigation sub-agent (and unwrapped Result.Argv check).
	invRepo, err := r.Run(context.Background(), Query{
		Workspace: dir,
		Question:  "check-repo",
		Slot:      "investigate-repo",
	})
	if err != nil {
		t.Fatalf("Run repo investigation: %v", err)
	}
	checkOutput("InvestigateRepo", invRepo.Raw)
	if len(invRepo.Argv) == 0 || invRepo.Argv[0] != probe {
		t.Errorf("Result.Argv = %v, want %s first (unwrapped)", invRepo.Argv, probe)
	}

	// 3. Worker session fork investigation sub-agent.
	invFork, err := r.Run(context.Background(), Query{
		Backend:   Claude,
		Workspace: dir,
		Session:   "sess-123",
		Fork:      true,
		Question:  "check-fork",
		Slot:      "investigate-fork",
	})
	if err != nil {
		t.Fatalf("Run fork investigation: %v", err)
	}
	checkOutput("InvestigateFork", invFork.Raw)

	for _, ev := range log.ofType("cli.run") {
		payload := ev.Payload.(map[string]any)
		if cmd, _ := payload["cmd"].(string); strings.Contains(cmd, "taskpolicy") {
			t.Errorf("cli.run cmd leaked wrapper binary: %q", cmd)
		}
	}
}

func TestRunnerMissingBinaryStillReportsStartError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	log := &recLog{}
	cfg := Config{
		Prefer:   "codex",
		CodexBin: missing,
		CodexCmd: []string{missing, "{question}"},
		Log:      log,
	}
	r := newTestRunner(t, cfg, Detection{Codex: true})
	res, err := r.Run(context.Background(), Query{
		Backend:   Codex,
		Workspace: t.TempDir(),
		Question:  "hello",
	})
	if err == nil {
		t.Fatal("expected start error for missing binary")
	}
	if res.Exit != -1 {
		t.Errorf("Exit = %d, want -1 (start error, not wrapper exit 1)", res.Exit)
	}
}
