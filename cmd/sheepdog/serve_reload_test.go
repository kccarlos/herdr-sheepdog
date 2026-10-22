package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
)

// TestServeSetupModeAndReload: with no key at all the daemon starts
// (setup mode: the web server up, the voice off with the reason on
// /api/state), and a restart request reloads the env file in place —
// twice, so a value changed in the file after the first load wins over
// the copy the first load put in the environment.
func TestServeSetupModeAndReload(t *testing.T) {
	noKeys(t)
	dir := t.TempDir()
	port := freePort(t)
	cfgPath := filepath.Join(dir, "config.toml")
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("data_dir = %q\n[web]\nport = %d\n", filepath.Join(dir, "data"), port)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	built := make(chan *app.App, 4)
	serveBuilt = func(a *app.App) { built <- a }
	t.Cleanup(func() { serveBuilt = nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--config", cfgPath, "--env-file", envPath, "--herdr", "mock"}, &stdout, &stderr)
	}()
	next := func() *app.App {
		t.Helper()
		select {
		case a := <-built:
			return a
		case code := <-done:
			t.Fatalf("serve exited %d: %s", code, stderr.String())
		case <-time.After(10 * time.Second):
			t.Fatalf("no daemon built: %s", stderr.String())
		}
		return nil
	}
	a := next()
	base := a.URL()

	var st struct {
		Voice struct {
			Problem     string `json:"problem"`
			SetupNeeded bool   `json:"setup_needed"`
		} `json:"voice"`
	}
	waitFor(t, func() bool {
		resp, err := http.Get(base + "/api/state")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		_ = json.NewDecoder(resp.Body).Decode(&st)
		return st.Voice.Problem != ""
	})
	if !st.Voice.SetupNeeded || !strings.Contains(st.Voice.Problem, "Setup needed: no Gemini API key yet. Run `sheepdog setup`.") {
		t.Fatalf("state voice = %+v", st.Voice)
	}
	if !strings.Contains(stdout.String(), base+"/setup") {
		t.Errorf("serve does not print the setup page:\n%s", stdout.String())
	}

	name := func() string {
		var h struct {
			Computer struct {
				Name string `json:"name"`
			} `json:"computer"`
		}
		resp, err := http.Get(base + "/api/hello")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		_ = json.NewDecoder(resp.Body).Decode(&h)
		return h.Computer.Name
	}
	tokA, tokB := "Studio A", "Studio B"
	for _, want := range []string{tokA, tokB} {
		if err := os.WriteFile(envPath, []byte("SHEEPDOG_COMPUTER_NAME="+want+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		a.RequestRestart("test")
		a = next()
		if a.URL() != base {
			t.Fatalf("reloaded on %s, want %s", a.URL(), base)
		}
		if got := name(); got != want {
			t.Errorf("after the reload the name is %q, want %q", got, want)
		}
	}
	if !strings.Contains(stdout.String(), "sheepdog reloaded its settings") {
		t.Errorf("no reload line:\n%s", stdout.String())
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
}
