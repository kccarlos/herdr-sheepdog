package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// stateVoice reads /api/state's voice object.
func stateVoice(t *testing.T, base string) (v struct {
	Status      string `json:"status"`
	Problem     string `json:"problem"`
	SetupNeeded bool   `json:"setup_needed"`
}) {
	t.Helper()
	resp, err := http.Get(base + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var st struct {
		Voice json.RawMessage `json:"voice"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(st.Voice, &v)
	return v
}

func runApp(t *testing.T, a *App) (cancel func() error) {
	t.Helper()
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	return func() error {
		stop()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return")
			return nil
		}
	}
}

// TestVoiceConnectFailureKeepsTheDaemonUp: a voice that cannot connect
// at startup (here: nothing listens where the Gemini API should be)
// does not end Run; the web server answers, /api/state says why the
// voice is off and when it retries, and the retries go on.
func TestVoiceConnectFailureKeepsTheDaemonUp(t *testing.T) {
	cfg := testConfig(t)
	cfg.Gemini.APIKeys = []string{"test-key-not-real-0123456789"}
	cfg.Brain.Enabled = false
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + ln.Addr().String()
	_ = ln.Close() // connection refused from now on
	t.Setenv("GOOGLE_GEMINI_BASE_URL", dead)
	defer SetVoiceRetry(50*time.Millisecond, 100*time.Millisecond)()

	a, err := Build(cfg, Options{Herdr: HerdrMock, Drain: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stop := runApp(t, a)
	deadline := time.Now().Add(10 * time.Second)
	var v = stateVoice(t, a.URL())
	for v.Problem == "" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		v = stateVoice(t, a.URL())
	}
	if !strings.Contains(v.Problem, "The voice cannot connect") || !strings.Contains(v.Problem, "Retrying in") || v.SetupNeeded || v.Status != "closed" {
		t.Fatalf("voice = %+v", v)
	}
	time.Sleep(400 * time.Millisecond) // a few retries
	if err := stop(); err != nil {
		t.Errorf("Run: %v", err)
	}
}

// TestSetupModeWithoutAKey: no key at all builds and runs (setup mode)
// with the reason on /api/state; Close ends cleanly though the voice
// never started (its channels are closed for the consumers).
func TestSetupModeWithoutAKey(t *testing.T) {
	cfg := testConfig(t)
	a, err := Build(cfg, Options{Herdr: HerdrMock, Drain: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !a.SetupNeeded() || a.BrainMode() != BrainOff {
		t.Fatalf("setup needed %v, orchestrator %s", a.SetupNeeded(), a.BrainMode())
	}
	stop := runApp(t, a)
	deadline := time.Now().Add(5 * time.Second)
	v := stateVoice(t, a.URL())
	for v.Problem == "" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		v = stateVoice(t, a.URL())
	}
	if !v.SetupNeeded || v.Problem != "Setup needed: no Gemini API key yet. Run `sheepdog setup`." {
		t.Fatalf("voice = %+v", v)
	}
	if err := stop(); err != nil {
		t.Errorf("Run: %v", err)
	}
}

// TestRequestRestart: Run returns ErrRestart, once, however often it is
// asked.
func TestRequestRestart(t *testing.T) {
	cfg := testConfig(t)
	a, err := Build(cfg, Options{Voice: VoiceOff, Herdr: HerdrMock, Drain: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Listen(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	a.RequestRestart("test")
	a.RequestRestart("again")
	select {
	case err := <-done:
		if err != ErrRestart {
			t.Fatalf("Run = %v, want ErrRestart", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}

// TestHangingVoiceDialDoesNotBlockThePages: with no network a dial can
// hang (a blackholed route, a DNS timeout); the pages that read the
// voice's provider and status must answer meanwhile.
func TestHangingVoiceDialDoesNotBlockThePages(t *testing.T) {
	cfg := testConfig(t)
	cfg.Gemini.APIKeys = []string{"test-key-not-real-0123456789"}
	cfg.Brain.Enabled = false
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c) // accepted, never answered
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	})
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "http://"+ln.Addr().String())

	a, err := Build(cfg, Options{Herdr: HerdrMock, Drain: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stop := runApp(t, a)
	time.Sleep(300 * time.Millisecond) // the dial is hanging now
	client := &http.Client{Timeout: time.Second}
	for _, path := range []string{"/api/state", "/api/hello"} {
		start := time.Now()
		resp, err := client.Get(a.URL() + path)
		if err != nil {
			t.Fatalf("%s while the voice dials: %v", path, err)
		}
		_ = resp.Body.Close()
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Errorf("%s took %s", path, d)
		}
	}
	mu.Lock()
	for _, c := range held {
		_ = c.Close() // let the dial fail so Run can end
	}
	mu.Unlock()
	_ = stop()
}
