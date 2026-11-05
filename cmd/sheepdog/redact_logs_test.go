package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

func TestCLIRedactLogs(t *testing.T) {
	unsetEnv(t, "GEMINI_API_KEY", "GEMINI_API_KEY_1", "GEMINI_API_KEY_2", "GEMINI_API_KEY_3", "DEEPSEEK_API_KEY")
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "sheepdog")
	dataDir := filepath.Join(home, ".local", "share", "sheepdog")
	brainDir := filepath.Join(dataDir, brain.TranscriptDir)
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(brainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	envSecret := "custom-env-secret-value-998877"
	cfgSecret := "custom-cfg-secret-value-112233"
	bareToken := "gh" + "p_" + strings.Repeat("kM7", 12)
	envPath := filepath.Join(cfgDir, ".env")
	if err := os.WriteFile(envPath, []byte("DEEPSEEK_API_KEY="+envSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.toml")
	cfgTOML := fmt.Sprintf("data_dir = %q\n\n[web]\nport = 59999\n\n[anthropic]\napi_key = %q\n", dataDir, cfgSecret)
	if err := os.WriteFile(cfgPath, []byte(cfgTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	trFile := filepath.Join(brainDir, "20260918T162804Z.jsonl")
	trLine := fmt.Sprintf(`{"ts":"2026-09-18T16:28:04Z","role":"user","kind":"request","content":"keys %s, %s and %s","tokens":12}`+"\n", envSecret, cfgSecret, bareToken)
	if err := os.WriteFile(trFile, []byte(trLine), 0o600); err != nil {
		t.Fatal(err)
	}
	reqFile := filepath.Join(brainDir, "20260918T162804Z.requests.jsonl")
	reqLine := `{"step":1,"run":"20260918T162804Z","segment":"20260918T162804Z","turn":1,"turn_step":1,"kind":"utterance","provider":"gemini","model":"m","ts":"2026-09-18T16:28:04Z","format":"gemini","window_tokens":1000,"tokens":8,"text":"hello sk-live-abcdef1234567890\n","sections":[{"key":"s","label":"s","class":"system","start":0,"end":31,"tokens":8}],"body":{"key":"` + envSecret + `"}}` + "\n"
	if err := os.WriteFile(reqFile, []byte(reqLine), 0o600); err != nil {
		t.Fatal(err)
	}

	// While a daemon holds the lock, redact-logs refuses.
	lock, err := daemon.Acquire(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Write(daemon.Info{PID: os.Getpid(), URL: "http://127.0.0.1:59999"})
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"redact-logs", "--config", cfgPath, "--env-file", envPath}, &out, &errs); code != 1 {
		lock.Release()
		t.Fatalf("while running: exit %d, want 1 (stderr=%q)", code, errs.String())
	}
	lock.Release()
	if raw, _ := os.ReadFile(trFile); !strings.Contains(string(raw), envSecret) {
		t.Fatalf("modified transcript while daemon running")
	}

	out.Reset()
	errs.Reset()
	if code := run(context.Background(), []string{"redact-logs", "--config", cfgPath, "--env-file", envPath}, &out, &errs); code != 0 {
		t.Fatalf("redact-logs: exit %d, stderr=%q", code, errs.String())
	}
	if !strings.Contains(out.String(), "redacted 2 files") {
		t.Errorf("stdout = %q, want 'redacted 2 files'", out.String())
	}
	for _, p := range []string{trFile, reqFile} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(bytes.TrimSpace(raw)) {
			t.Errorf("%s invalid JSON: %s", p, raw)
		}
		for _, s := range []string{envSecret, cfgSecret, bareToken, "sk-live-abcdef1234567890"} {
			if strings.Contains(string(raw), s) {
				t.Errorf("%s still contains %q: %s", p, s, raw)
			}
		}
		if !strings.Contains(string(raw), redact.Mask) {
			t.Errorf("%s missing %s: %s", p, redact.Mask, raw)
		}
	}

	// Running a second time reports nothing left to redact.
	out.Reset()
	errs.Reset()
	if code := run(context.Background(), []string{"redact-logs", "--config", cfgPath, "--env-file", envPath}, &out, &errs); code != 0 {
		t.Fatalf("second redact-logs: exit %d, stderr=%q", code, errs.String())
	}
	if !strings.Contains(out.String(), "nothing to redact") {
		t.Errorf("second stdout = %q, want 'nothing to redact'", out.String())
	}
}
