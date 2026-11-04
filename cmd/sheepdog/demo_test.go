package main

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
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

func TestPickDemoPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_, pStr, _ := net.SplitHostPort(ln.Addr().String())
	busy, _ := strconv.Atoi(pStr)

	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, pStr2, _ := net.SplitHostPort(ln2.Addr().String())
	free, _ := strconv.Atoi(pStr2)
	_ = ln2.Close()

	// Without an explicit --port, pickDemoPort skips the busy port and picks the next free one.
	got, err := pickDemoPort(0, []int{busy, free})
	if err != nil || got != free {
		t.Errorf("pickDemoPort(0, [%d, %d]) = %d, %v; want %d, nil", busy, free, got, err, free)
	}
	// With an explicit --port that is busy, pickDemoPort refuses.
	if _, err := pickDemoPort(busy, []int{busy, free}); err == nil || !strings.Contains(err.Error(), "is in use") {
		t.Errorf("pickDemoPort(%d) = %v, want in-use error", busy, err)
	}
}
