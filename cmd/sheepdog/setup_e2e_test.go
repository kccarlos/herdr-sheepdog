package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
)

// TestSetupPageFlowEndToEnd drives the setup page's flow against a real
// `sheepdog serve` (in-process, its own port and dirs, a fake key API,
// the Gemini Live endpoint pointed at a closed port): setup mode, the
// page, a rejected key, a good key (the daemon reloads with it), the
// checks, phone access (reload on every address, a token, the QR code)
// and `sheepdog setup` / `open setup` against the running daemon.
func TestSetupPageFlowEndToEnd(t *testing.T) {
	fakeKeyAPI(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "http://"+ln.Addr().String())
	_ = ln.Close()
	t.Cleanup(func() {
		_ = os.Unsetenv("GEMINI_API_KEY")
		_ = os.Unsetenv("SHEEPDOG_WEB_TOKEN")
		_ = os.Unsetenv("SHEEPDOG_WEB_BIND")
	})
	for _, k := range []string{"SHEEPDOG_VOICE_PROVIDER", "SHEEPDOG_BRAIN_PROVIDER"} {
		t.Cleanup(func() { _ = os.Unsetenv(k) })
	}

	dir := t.TempDir()
	port := freePort(t)
	cfgPath, envPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, ".env")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("data_dir = %q\n[web]\nport = %d\n", filepath.Join(dir, "data"), port)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	built := make(chan *app.App, 8)
	serveBuilt = func(a *app.App) { built <- a }
	t.Cleanup(func() { serveBuilt = nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--config", cfgPath, "--env-file", envPath, "--herdr", "mock"}, &stdout, &stderr)
	}()
	next := func() {
		t.Helper()
		select {
		case <-built:
		case code := <-done:
			t.Fatalf("serve exited %d: %s", code, stderr.String())
		case <-time.After(15 * time.Second):
			t.Fatalf("no daemon: %s", stderr.String())
		}
	}
	next()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	get := func(path string) (int, string) {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	post := func(path, body string) (int, string) {
		resp, err := http.Post(base+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	state := func() web.SetupState {
		t.Helper()
		_, b := get("/api/setup/state")
		var st web.SetupState
		if err := json.Unmarshal([]byte(b), &st); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		return st
	}

	// 0. Setup mode: the page is there, the voice needs a key.
	if code, body := get("/setup"); code != 200 || !strings.Contains(body, "Set up sheepdog") {
		t.Fatalf("/setup: %d", code)
	}
	waitFor(t, func() bool { return state().Voice.SetupNeeded })
	boot := state().Boot

	// 1. Models: a rejected key saves nothing, a good one reloads.
	if code, body := post("/api/setup/keys", `{"gemini_key":"AIza-wrong-key-0000000000"}`); code != 422 || !strings.Contains(body, "rejected the key") {
		t.Fatalf("bad key: %d %s", code, body)
	}
	if code, body := post("/api/setup/keys", `{"gemini_key":"`+initGeminiKey+`"}`); code != 200 {
		t.Fatalf("good key: %d %s", code, body)
	}
	next()
	waitFor(t, func() bool { st := state(); return st.Boot != boot && st.Keys.Gemini })
	st := state()
	if st.Voice.SetupNeeded || !st.Orchestrator.On {
		t.Errorf("after the key: voice %+v orchestrator %+v", st.Voice, st.Orchestrator)
	}
	// The voice cannot reach (the fake) Gemini Live: the daemon stays up
	// and says so.
	waitFor(t, func() bool { return strings.Contains(state().Voice.Problem, "Retrying in") })

	// 2. Checks.
	if code, body := get("/api/setup/checks"); code != 200 || !strings.Contains(body, `"gemini key"`) {
		t.Fatalf("checks: %d %s", code, body)
	}

	// 4. Phone: every address, a token, the QR code; "Connected" later.
	boot = state().Boot
	if code, body := post("/api/setup/phone", `{"enable":true}`); code != 200 || !strings.Contains(body, `"token_created": true`) {
		t.Fatalf("phone: %d %s", code, body)
	}
	next()
	waitFor(t, func() bool { st := state(); return st.Boot != boot && st.Phone.Enabled })
	if st := state(); !st.Phone.HasToken || st.Phone.Bind != "0.0.0.0" {
		t.Errorf("phone %+v", st.Phone)
	}
	if code, body := get("/api/setup/qr.svg"); code != 200 && code != 409 || code == 200 && !strings.Contains(body, "<svg") {
		t.Errorf("qr: %d", code)
	}
	// The voice page's API now needs the token; the setup login sets the
	// cookie for this browser.
	if code, _ := get("/api/state"); code != 401 {
		t.Errorf("/api/state without the token: %d", code)
	}

	// `sheepdog setup` and `open setup` with the daemon running.
	var opened []string
	origOpen := openBrowser
	openBrowser = func(u string) error { opened = append(opened, u); return nil }
	defer func() { openBrowser = origOpen }()
	origDisplay := hasDisplay
	hasDisplay = func() bool { return true }
	defer func() { hasDisplay = origDisplay }()
	code, out, errs := runCLI("setup", "--config", cfgPath, "--env-file", envPath)
	if code != 0 || !strings.Contains(out, "http://127.0.0.1:"+fmt.Sprint(port)+"/setup") || len(opened) != 1 {
		t.Errorf("setup: %d %q %q %v", code, out, errs, opened)
	}
	code, out, _ = runCLI("open", "setup", "--config", cfgPath, "--env-file", envPath)
	if code != 0 || strings.Contains(out, "asks once for the web token") {
		t.Errorf("open setup: %d %q", code, out)
	}

	env, _ := os.ReadFile(envPath)
	for _, s := range []string{stdout.String(), stderr.String(), out} {
		if strings.Contains(s, initGeminiKey) {
			t.Error("the key reached the output")
		}
	}
	if !strings.Contains(string(env), "GEMINI_API_KEY="+initGeminiKey) {
		t.Error("the key is not in the env file")
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("serve exit %d: %s", code, stderr.String())
	}
}

// TestSetupNoDisplayUsesTheTerminal: over SSH, setup asks in the
// terminal (init's questions) and says where the page is.
func TestSetupNoDisplayUsesTheTerminal(t *testing.T) {
	fakeKeyAPI(t)
	orig := hasDisplay
	hasDisplay = func() bool { return false }
	defer func() { hasDisplay = orig }()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	var out, errb strings.Builder
	code := runSetup(context.Background(), []string{"--config", cfgPath}, strings.NewReader("1\n"+initGeminiKey+"\nn\n"), &out, &errb)
	if code != 0 || !strings.Contains(out.String(), "No display here (SSH?): setting up in the terminal") || !strings.Contains(out.String(), "wrote "+cfgPath) {
		t.Fatalf("exit %d\n%s\n%s", code, out.String(), errb.String())
	}
	if strings.Contains(out.String(), initGeminiKey) {
		t.Error("the key was echoed")
	}
}
