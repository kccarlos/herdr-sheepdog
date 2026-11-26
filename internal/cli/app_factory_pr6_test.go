package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Pins app-factory.md §6.5, §6.6 & §12 (Codex loopback check): default Codex
// templates enforce read-only sandbox, Claude has no shell, and inside real
// Codex read-only sandbox POST /api/factory/stop cannot reach 127.0.0.1 while
// external project log.jsonl stays readable.
func TestCodexReadOnlySandboxBlocksLoopbackFactoryStop(t *testing.T) {
	if !slices.Contains(defaultCodexCmd, "read-only") || !slices.Contains(defaultCodexResumeCmd, "sandbox_mode=read-only") {
		t.Fatalf("codex templates lost read-only: %v / %v", defaultCodexCmd, defaultCodexResumeCmd)
	}
	if !slices.Contains(defaultClaudeCmd, "Read,Grep,Glob,WebSearch") || slices.Contains(defaultClaudeCmd, "Bash") {
		t.Fatalf("claude template lost read-only: %v", defaultClaudeCmd)
	}
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex binary not on PATH")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stopHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/factory/stop" {
			stopHits.Add(1)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	ws, logPath := t.TempDir(), filepath.Join(t.TempDir(), "factory", "projects", "proj-1a2b3c4d", "log.jsonl")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	const logLine = `{"seq":1,"by":"coordinator","task":"T8","decision":"split","why":"two changes over 400 lines"}`
	if err := os.WriteFile(logPath, []byte(logLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, codexBin, "sandbox", "-c", "sandbox_mode=read-only", "--", exe, "-test.run=^TestHelperCodexLoopbackProbe$", "-test.v")
	cmd.Dir, cmd.Env = ws, append(os.Environ(), "SHEEPDOG_TEST_STOP_URL="+srv.URL+"/api/factory/stop", "SHEEPDOG_TEST_LOG_PATH="+logPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("codex sandbox probe failed: %v\n%s", err, out)
	}
	if n := stopHits.Load(); n != 0 {
		t.Fatalf("Codex read-only sandbox reached loopback /api/factory/stop (%d hits); per app-factory.md §6.5 pin project investigations to Claude", n)
	}
	if !strings.Contains(string(out), "LOG_READ_OK:"+logLine) {
		t.Errorf("sandbox could not read log.jsonl:\n%s", out)
	}
}

func TestHelperCodexLoopbackProbe(t *testing.T) {
	stopURL := os.Getenv("SHEEPDOG_TEST_STOP_URL")
	if stopURL == "" {
		return
	}
	if resp, err := (&http.Client{Timeout: 2 * time.Second}).Post(stopURL, "application/json", strings.NewReader(`{"project":"proj-1a2b3c4d"}`)); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("loopback POST %s succeeded (%d)", stopURL, resp.StatusCode)
	}
	raw, err := os.ReadFile(os.Getenv("SHEEPDOG_TEST_LOG_PATH"))
	if err != nil {
		t.Fatalf("read log.jsonl inside sandbox: %v", err)
	}
	fmt.Printf("LOG_READ_OK:%s\n", strings.TrimSpace(string(raw)))
}
