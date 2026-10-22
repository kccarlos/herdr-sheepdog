package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
)

// lifeSetup writes a config with its own data dir and a free port (never
// 7777) and returns the config path, the data dir and the port.
func lifeSetup(t *testing.T, extra string) (cfgPath, dataDir string, port int) {
	t.Helper()
	dir := t.TempDir()
	dataDir = filepath.Join(dir, "data")
	port = freePort(t)
	cfgPath = filepath.Join(dir, "config.toml")
	body := fmt.Sprintf("data_dir = %q\n[web]\nport = %d\n%s", dataDir, port, extra)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, dataDir, port
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func runCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestHelpListsUserCommandsAndHidesDeveloperOnes(t *testing.T) {
	code, out, _ := runCLI("--help")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	order := []string{"init", "doctor", "pair", "start", "stop", "restart", "status", "logs", "open", "serve", "service", "config", "version"}
	last := -1
	for _, c := range order {
		i := strings.Index(out, "\n  "+c+" ")
		if i < 0 {
			t.Errorf("help lacks %q", c)
			continue
		}
		if i < last {
			t.Errorf("%q is out of order", c)
		}
		last = i
	}
	for _, dev := range devOrder {
		if strings.Contains(out, "\n  "+dev+" ") {
			t.Errorf("developer command %q is listed as a user command", dev)
		}
	}
	_, devOut, _ := runCLI("dev", "--help")
	for _, dev := range devOrder {
		if !strings.Contains(devOut, "  "+dev+" ") {
			t.Errorf("dev help lacks %q", dev)
		}
	}
}

// TestDeveloperAliasesStillDispatch: `sheepdog eval …` keeps working.
func TestDeveloperAliasesStillDispatch(t *testing.T) {
	for _, name := range devOrder {
		code, _, errOld := runCLI(name, "--no-such-flag-xyz")
		codeDev, _, errDev := runCLI("dev", name, "--no-such-flag-xyz")
		if strings.Contains(errOld, "unknown command") || code != codeDev {
			t.Errorf("%s: alias exit %d (%q) vs dev %d (%q)", name, code, errOld, codeDev, errDev)
		}
	}
}

func TestStatusWhenStopped(t *testing.T) {
	cfg, _, port := lifeSetup(t, "")
	code, out, _ := runCLI("status", "--config", cfg)
	if code != 3 || !strings.Contains(out, "sheepdog is not running (start it with: sheepdog start)") || !strings.Contains(out, fmt.Sprint(port)) {
		t.Errorf("exit %d:\n%s", code, out)
	}
	code, out, _ = runCLI("status", "--json", "--config", cfg)
	var r statusReport
	if err := json.Unmarshal([]byte(out), &r); err != nil || code != 3 || r.Running || r.Port != port || r.Voice.Source != "config" {
		t.Errorf("json exit %d err %v: %s", code, err, out)
	}
}

func TestLogsBeforeFirstRun(t *testing.T) {
	cfg, data, _ := lifeSetup(t, "")
	code, out, errs := runCLI("logs", "--config", cfg)
	if code != 0 || out != "" || !strings.Contains(errs, "No log yet: "+daemon.LogFile(data)) || !strings.Contains(errs, "sheepdog start") {
		t.Errorf("exit %d out %q err %q", code, out, errs)
	}
}

func TestLogsTail(t *testing.T) {
	cfg, data, _ := lifeSetup(t, "")
	if err := os.MkdirAll(daemon.LogDir(data), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(daemon.LogFile(data), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI("logs", "-n", "3", "--config", cfg)
	if code != 0 || out != "line 48\nline 49\nline 50\n" {
		t.Errorf("exit %d: %q", code, out)
	}
}

func TestLogsFollowSeesAppendsAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sheepdog.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	followPoll = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}
	done := make(chan struct{})
	go func() { followFile(ctx, path, out); close(done) }()
	time.Sleep(50 * time.Millisecond)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("new 1\n")
	_ = f.Close()
	time.Sleep(50 * time.Millisecond)
	_ = os.Rename(path, path+".1")
	_ = os.WriteFile(path, []byte("after rotate\n"), 0o600)
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done
	if got := out.String(); got != "new 1\nafter rotate\n" {
		t.Errorf("followed %q", got)
	}
}

func TestStopWhenNotRunning(t *testing.T) {
	cfg, _, _ := lifeSetup(t, "")
	code, out, _ := runCLI("stop", "--config", cfg)
	if code != 0 || out != "sheepdog is not running\n" {
		t.Errorf("exit %d: %q", code, out)
	}
}

// TestStartAndServeWhenAlreadyRunning holds the data dir's lock as a
// running daemon would.
func TestStartAndServeWhenAlreadyRunning(t *testing.T) {
	cfg, data, port := lifeSetup(t, "")
	lock, err := daemon.Acquire(data)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	_ = lock.Write(daemon.Info{PID: 4242, URL: url})
	want := fmt.Sprintf("sheepdog is already running (pid 4242, %s)\n", url)
	if code, out, _ := runCLI("start", "--config", cfg); code != 0 || out != want {
		t.Errorf("start: exit %d %q", code, out)
	}
	if code, _, errs := runCLI("serve", "--config", cfg, "--voice", "off", "--herdr", "mock"); code != 1 || errs != want {
		t.Errorf("serve: exit %d %q", code, errs)
	}
}

// TestPortTakenByAnotherProgram: a port error names the program's port
// and the fix instead of "address already in use".
func TestPortTakenByAnotherProgram(t *testing.T) {
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	port := other.Listener.Addr().(*net.TCPAddr).Port
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfg, []byte(fmt.Sprintf("data_dir = %q\n[web]\nport = %d\n", filepath.Join(dir, "data"), port)), 0o600)
	want := fmt.Sprintf("port %d is in use by another program", port)
	code, _, errs := runCLI("serve", "--config", cfg, "--voice", "off", "--herdr", "mock")
	if code != 1 || !strings.Contains(errs, want) || !strings.Contains(errs, "lsof -nP -iTCP:") {
		t.Errorf("serve: exit %d %q", code, errs)
	}
	t.Setenv("GEMINI_API_KEY", "AIzaFakeKeyForTheStartCheckOnly000")
	code, _, errs = runCLI("start", "--config", cfg)
	if code != 1 || !strings.Contains(errs, want) {
		t.Errorf("start: exit %d %q", code, errs)
	}
}

// TestPortHeldByOlderSheepdog: a daemon without a pid file on the port
// (an older version) is recognised, not reported as another program, and
// stop refuses to guess its pid.
func TestPortHeldByOlderSheepdog(t *testing.T) {
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"sheepdog","version":"v0.0.9"}`))
	}))
	defer old.Close()
	port := old.Listener.Addr().(*net.TCPAddr).Port
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfg, []byte(fmt.Sprintf("data_dir = %q\n[web]\nport = %d\n", filepath.Join(dir, "data"), port)), 0o600)
	if code, _, errs := runCLI("serve", "--config", cfg, "--voice", "off", "--herdr", "mock"); code != 1 || !strings.Contains(errs, "sheepdog is already running") || !strings.Contains(errs, "no pid file") {
		t.Errorf("serve: exit %d %q", code, errs)
	}
	if code, out, _ := runCLI("start", "--config", cfg); code != 0 || !strings.Contains(out, "sheepdog is already running") {
		t.Errorf("start: exit %d %q", code, out)
	}
	if code, _, errs := runCLI("stop", "--config", cfg); code != 1 || !strings.Contains(errs, "left no pid file") {
		t.Errorf("stop: exit %d %q", code, errs)
	}
	if code, out, _ := runCLI("status", "--config", cfg); code != 0 || !strings.Contains(out, "pid           unknown") {
		t.Errorf("status: exit %d %q", code, out)
	}
}

// TestStartRefusesAWideBindWithoutAToken: the one setting start still
// refuses (a missing key is setup mode, not an error).
func TestOpen(t *testing.T) {
	cfg, data, port := lifeSetup(t, "")
	var opened []string
	orig := openBrowser
	openBrowser = func(u string) error { opened = append(opened, u); return nil }
	defer func() { openBrowser = orig }()
	if code, _, errs := runCLI("open", "--config", cfg); code != 1 || !strings.Contains(errs, "sheepdog is not running") {
		t.Errorf("not running: exit %d %q", code, errs)
	}
	if code, _, _ := runCLI("open", "nope", "--config", cfg); code != 2 {
		t.Errorf("unknown page: exit %d", code)
	}
	// A daemon (a stub) that has /voice but no /setup.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/setup" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"name":"sheepdog"}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	lock, _ := daemon.Acquire(data)
	defer lock.Release()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	_ = lock.Write(daemon.Info{PID: os.Getpid(), URL: base})
	origDisplay := hasDisplay
	hasDisplay = func() bool { return true }
	defer func() { hasDisplay = origDisplay }()
	if code, out, _ := runCLI("open", "--config", cfg); code != 0 || !strings.Contains(out, "opening "+base+"/voice") {
		t.Errorf("voice: exit %d %q", code, out)
	}
	if code, _, _ := runCLI("open", "debug", "--config", cfg); code != 0 {
		t.Errorf("debug: exit %d", code)
	}
	if code, _, errs := runCLI("open", "setup", "--config", cfg); code != 1 || !strings.Contains(errs, "no setup page yet") {
		t.Errorf("setup: exit %d %q", code, errs)
	}
	if strings.Join(opened, " ") != base+"/voice "+base+"/debug" {
		t.Errorf("opened %q", opened)
	}
	// Over SSH it opens nothing and says how to reach the page.
	hasDisplay = func() bool { return false }
	opened = nil
	if code, out, _ := runCLI("open", "--config", cfg); code != 0 || !strings.Contains(out, "ssh -N -L") || len(opened) != 0 {
		t.Errorf("no display: exit %d %q opened %q", code, out, opened)
	}
}

// TestLifecycleEndToEnd builds the real binary and runs start, start
// again, status, logs, stop and stop again in a throwaway HOME on a free
// port: the detached process, the pid file, the lock and SIGTERM for
// real.
func TestLifecycleEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "sheepdog")
	build := exec.Command("go", "build", "-ldflags", "-X main.version=v0.0.0-e2e", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := filepath.Join(dir, "home")
	port := freePort(t)
	if err := os.MkdirAll(filepath.Join(home, ".config", "sheepdog"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(home, ".config", "sheepdog", "config.toml"), []byte(fmt.Sprintf("[web]\nport = %d\n[brain]\nenabled = false\n[voice]\nprovider = \"openai\"\n", port)), 0o600)
	// GPT-Live opens only for speech or a push, so a fake key is never sent.
	_ = os.WriteFile(filepath.Join(home, ".config", "sheepdog", ".env"), []byte("OPENAI_API_KEY=sk-NotARealKeyEndToEndTest00000\nSHEEPDOG_HERDR_SOCKET="+filepath.Join(dir, "no-herdr.sock")+"\n"), 0o600)
	sd := func(args ...string) (int, string) {
		cmd := exec.Command(bin, args...)
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return code, string(out)
	}
	t.Cleanup(func() { sd("stop") })
	if code, out := sd("start"); code != 0 || !strings.Contains(out, "sheepdog started in the background (pid ") {
		t.Fatalf("start: %d %s", code, out)
	}
	if code, out := sd("start"); code != 0 || !strings.Contains(out, "sheepdog is already running (pid ") {
		t.Errorf("start again: %d %s", code, out)
	}
	if code, out := sd("serve"); code != 1 || !strings.Contains(out, "sheepdog is already running (pid ") {
		t.Errorf("serve while running: %d %s", code, out)
	}
	if code, out := sd("status"); code != 0 || !strings.Contains(out, "sheepdog is running") || !strings.Contains(out, "v0.0.0-e2e") || !strings.Contains(out, "background") {
		t.Errorf("status: %d %s", code, out)
	}
	if code, out := sd("logs", "-n", "50"); code != 0 || !strings.Contains(out, "listening on") || strings.Contains(out, "sk-NotARealKey") {
		t.Errorf("logs: %d %s", code, out)
	}
	if code, out := sd("stop"); code != 0 || !strings.Contains(out, "sheepdog stopped (pid ") {
		t.Errorf("stop: %d %s", code, out)
	}
	if code, out := sd("stop"); code != 0 || out != "sheepdog is not running\n" {
		t.Errorf("stop again: %d %s", code, out)
	}
	_, out := sd("logs", "-n", "200")
	if !strings.Contains(out, "daemon.stop") {
		t.Errorf("no clean shutdown (daemon.stop) in the log:\n%s", out)
	}
}
