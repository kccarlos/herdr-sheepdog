package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

func TestDemoRefuses(t *testing.T) {
	for _, k := range []string{"GEMINI_API_KEY", "GEMINI_API_KEY_1", "OPENAI_API_KEY", "SHEEPDOG_GEMINI_BACKEND"} {
		t.Setenv(k, "")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	env := filepath.Join(dir, "none.env")
	var out, errb strings.Builder
	if code := run(context.Background(), []string{"demo", "--port", "7777"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "7777 is the daemon's own port") {
		t.Errorf("7777: exit %d, %q", code, errb.String())
	}
	errb.Reset()
	if code := run(context.Background(), []string{"demo", "--config", cfg, "--env-file", env}, &out, &errb); code == 0 {
		t.Errorf("a missing --env-file was accepted")
	}
}

func TestDemoKeyProblem(t *testing.T) {
	c := config.Default()
	c.Gemini.APIKeys = nil
	if msg := demoKeyProblem(c); !strings.Contains(msg, "GEMINI_API_KEY") {
		t.Errorf("no key: %q", msg)
	}
	c.Gemini.APIKeys = []string{"k"}
	if msg := demoKeyProblem(c); msg != "" {
		t.Errorf("with a key: %q", msg)
	}
	c.Gemini.APIKeys = nil
	c.Gemini.Backend = "vertex"
	if msg := demoKeyProblem(c); msg != "" {
		t.Errorf("vertex: %q", msg)
	}
}
