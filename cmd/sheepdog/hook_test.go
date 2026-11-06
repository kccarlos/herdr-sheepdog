package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

func TestHookCommandBehaviourAndDeadline(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "h")
	dataDir := filepath.Join(dir, "d")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHEEPDOG_DATA_DIR", dataDir)

	realStdin := `{"session_id":"b0c8a978-2086-4b5c-8783-a873aae6f99a","cwd":"/tmp/repo","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"echo hi\ntouch ok.txt","description":"Create ok"}}`

	runSub := func(stdin string, args ...string) int {
		return runHook(args, strings.NewReader(stdin))
	}

	// 1. Top-level CLI dispatch, outside a herdr pane, or unknown subcommand: exits 0 with no output.
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("HC_TERMINAL_ID", "")
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"hook", "claude-permission"}, &out, &errb); code != 0 || out.Len() != 0 || errb.Len() != 0 {
		t.Fatalf("outside pane: code=%d out=%q err=%q", code, out.String(), errb.String())
	}

	// 2. In a herdr pane, daemon stopped (no pid lock) or unknown subcommand: exits 0.
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr.sock")
	if code := runSub(realStdin, "claude-permission"); code != 0 {
		t.Fatalf("daemon stopped: code=%d", code)
	}
	if code := run(context.Background(), []string{"hook", "unknown"}, &out, &errb); code != 0 || out.Len() != 0 || errb.Len() != 0 {
		t.Fatalf("unknown subcommand: code=%d out=%q err=%q", code, out.String(), errb.String())
	}

	// 3. Running daemon on loopback HTTP: receives POST with pane + originating socket headers and raw JSON, zero output.
	type gotReq struct {
		pane, sock, ctype, body string
	}
	recv := make(chan gotReq, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		recv <- gotReq{r.Header.Get("X-Sheepdog-Pane"), r.Header.Get("X-Sheepdog-Socket"), r.Header.Get("Content-Type"), string(b)}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	lock, err := daemon.Acquire(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := lock.Write(daemon.Info{PID: os.Getpid(), URL: srv.URL}); err != nil {
		t.Fatal(err)
	}

	if code := runSub(realStdin, "claude-permission"); code != 0 {
		t.Fatalf("with daemon: code=%d", code)
	}
	select {
	case got := <-recv:
		if got.pane != "w1:p1" || got.sock != "/tmp/herdr.sock" || got.ctype != "application/json" || !strings.Contains(got.body, `"PermissionRequest"`) {
			t.Fatalf("unexpected HTTP hook request (herdr): %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for HTTP hook payload")
	}

	// HC_TERMINAL_ID reports the hc socket instead of HERDR_SOCKET_PATH.
	t.Setenv("HC_TERMINAL_ID", "t-9")
	t.Setenv("HC_SOCKET", "/tmp/hc-test.sock")
	if code := runSub(realStdin, "claude-permission"); code != 0 {
		t.Fatalf("with hc daemon: code=%d", code)
	}
	select {
	case got := <-recv:
		if got.pane != "t-9" || got.sock != "/tmp/hc-test.sock" {
			t.Fatalf("unexpected HTTP hook request (hc): %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hc HTTP hook payload")
	}

	// 4. Stalled server + never-ending stdin: hard ~300 ms ceiling, exits 0 with no output.
	stallDone := make(chan struct{})
	defer close(stallDone)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-stallDone
	}))
	defer srv2.Close()
	_ = lock.Write(daemon.Info{PID: os.Getpid(), URL: srv2.URL})

	r, w, _ := os.Pipe()
	defer func() { _ = r.Close(); _ = w.Close() }()
	start := time.Now()
	code := runHook([]string{"claude-permission"}, r)
	dur := time.Since(start)
	if code != 0 || dur > 600*time.Millisecond {
		t.Fatalf("stalled hook: code=%d dur=%v (want <= ~300ms)", code, dur)
	}
}

func TestSetupAndUninstallClaudeHook(t *testing.T) {
	home, _ := fakeService(t)
	t.Setenv("HOME", home)
	_ = fakeExe(t)
	cfgPath, data, _ := lifeSetup(t, "")
	_ = os.MkdirAll(data, 0o700)
	envPath := filepath.Join(home, ".env")

	claudeSettings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(claudeSettings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeSettings, []byte("{\"theme\":\"dark\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	probe := onboard.Probe{
		GOOS: "darwin", Home: home,
		Getenv: func(string) string { return "" },
		LookPath: func(name string) (string, error) {
			if name == "claude" {
				return "/fake/claude", nil
			}
			return "", fs.ErrNotExist
		},
		Stat: func(name string) (fs.FileInfo, error) {
			if strings.HasPrefix(name, home) {
				return os.Stat(name)
			}
			return nil, fs.ErrNotExist
		},
		ReadFile:      os.ReadFile,
		SocketAnswers: func(string) bool { return false },
	}
	cfg := config.Default()
	cfg.Herdr = config.Herdr{Backend: config.HerdrBackendHC, Socket: "/fake/old.sock", HCSocket: "/fake/hc.sock"}
	cfg.Hooks.ClaudePermission = true

	var out bytes.Buffer
	if _, err := setupEnvironment(context.Background(), cfg, envPath, setupEnvironmentOptions{
		Probe:  probe,
		Panel:  setupTestPanel(),
		Layout: []byte("layout"),
		Out:    &out,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "settings.json") || !strings.Contains(out.String(), ".bak") {
		t.Errorf("setup did not plainly report settings.json + backup:\n%s", out.String())
	}
	raw, err := os.ReadFile(claudeSettings)
	if err != nil || !strings.Contains(string(raw), "hook claude-permission") || !strings.Contains(string(raw), `"theme": "dark"`) {
		t.Fatalf("settings.json after setup:\n%s (%v)", raw, err)
	}

	// Opt-out via Hooks.ClaudePermission=false removes the hook; re-run with true restores it.
	out.Reset()
	offCfg := cfg
	offCfg.Hooks.ClaudePermission = false
	if _, err := setupEnvironment(context.Background(), offCfg, envPath, setupEnvironmentOptions{
		Probe: probe, Panel: setupTestPanel(), Layout: []byte("layout"), Out: &out,
	}); err != nil {
		t.Fatal(err)
	}
	if rawOptOut, _ := os.ReadFile(claudeSettings); strings.Contains(string(rawOptOut), "hook claude-permission") {
		t.Fatalf("opt-out left hook in settings.json:\n%s", rawOptOut)
	}
	if _, err := setupEnvironment(context.Background(), cfg, envPath, setupEnvironmentOptions{
		Probe: probe, Panel: setupTestPanel(), Layout: []byte("layout"), Out: &out,
	}); err != nil {
		t.Fatal(err)
	}

	// Uninstall --dry-run reports without modifying; uninstall --keep-data removes only Sheepdog's entry.
	code, uout, uerr := runCLI("uninstall", "--dry-run", "--keep-data", "--config", cfgPath)
	if code != 0 || !strings.Contains(uout, "would remove") || !strings.Contains(uout, "settings.json") {
		t.Fatalf("dry-run uninstall: code=%d err=%q out=\n%s", code, uerr, uout)
	}
	if raw2, _ := os.ReadFile(claudeSettings); !strings.Contains(string(raw2), "hook claude-permission") {
		t.Fatal("dry-run removed the hook")
	}

	code, uout, uerr = runCLI("uninstall", "--keep-data", "--config", cfgPath)
	if code != 0 || !strings.Contains(uout, "settings.json") {
		t.Fatalf("uninstall --keep-data: code=%d err=%q out=\n%s", code, uerr, uout)
	}
	after, err := os.ReadFile(claudeSettings)
	if err != nil || strings.Contains(string(after), "hook claude-permission") || !strings.Contains(string(after), `"dark"`) {
		t.Fatalf("settings.json after uninstall:\n%s (%v)", after, err)
	}
}
