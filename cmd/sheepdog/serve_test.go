package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeFlagsAndValidation(t *testing.T) {
	var stderr bytes.Buffer
	if _, code, run := parseServeFlags([]string{"--nope"}, &stderr); run || code != 2 {
		t.Errorf("bad flag: code=%d run=%v", code, run)
	}
	if _, code, run := parseServeFlags([]string{"-h"}, &stderr); run || code != 0 {
		t.Errorf("help: code=%d run=%v", code, run)
	}
	o, code, ok := parseServeFlags([]string{"--voice", "off", "--herdr", "mock", "--port", "1234", "--verbose", "--dev"}, &stderr)
	if !ok || code != 0 || o.voice != "off" || o.herdr != "mock" || o.port != 1234 || !o.verbose || !o.dev {
		t.Errorf("flags = %+v code=%d ok=%v", o, code, ok)
	}
	var stdout bytes.Buffer
	dir := t.TempDir()
	t.Setenv("GEMINI_API_KEY", "")
	if code := run(context.Background(), []string{"serve", "--config", filepath.Join(dir, "none.toml"), "--voice", "loud"}, &stdout, &stderr); code != 2 {
		t.Errorf("bad voice mode exit = %d: %s", code, stderr.String())
	}
	stderr.Reset()
	if code := run(context.Background(), []string{"serve", "--config", filepath.Join(dir, "none.toml"), "--voice", "gemini", "--data-dir", dir}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "GEMINI_API_KEY") {
		t.Errorf("gemini without key: exit=%d %s", code, stderr.String())
	}
}

func TestServeMockVoiceOffSmoke(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": `{"spoken":"x","is_question":false}`}}}})
	}))
	defer llm.Close()
	dir := t.TempDir()
	t.Setenv("SHEEPDOG_LLM_BASE_URL", llm.URL+"/v1")
	t.Setenv("SHEEPDOG_LLM_MODEL", "fake")
	t.Setenv("SHEEPDOG_WEB_PORT", "0")
	t.Setenv("GEMINI_API_KEY", "")

	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--config", filepath.Join(dir, "none.toml"), "--voice", "off", "--herdr", "mock", "--data-dir", dir, "--verbose"}, stdout, stderr)
	}()
	// Wait for the banner with the URL.
	var url string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && url == "" {
		for _, line := range strings.Split(stdout.String(), "\n") {
			if strings.Contains(line, "listening on ") {
				url = strings.Fields(strings.SplitN(line, "listening on ", 2)[1])[0]
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if url == "" {
		cancel()
		t.Fatalf("no banner: %s / %s", stdout.String(), stderr.String())
	}
	resp, err := http.Get(url + "/api/state")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/api/state = %d", resp.StatusCode)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit = %d: %s", code, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not exit")
	}
	if !strings.Contains(stderr.String(), `"daemon.start"`) || !strings.Contains(stderr.String(), `"daemon.stop"`) {
		t.Errorf("--verbose did not echo daemon events: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "events")); err != nil {
		t.Errorf("events dir: %v", err)
	}
	if !strings.Contains(stdout.String(), "/debug") {
		t.Errorf("banner lacks the debug URL: %s", stdout.String())
	}
}
