package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigInitWritesTemplateAndRefusesOverwrite(t *testing.T) {
	noKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.toml")

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"config", "init", "--path", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("init: code=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	if !strings.Contains(string(data), "full_fsync") || !strings.Contains(string(data), "[cli]") {
		t.Errorf("template missing expected content: %s", data)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("template mode = %v, err %v", fi.Mode(), err)
	}

	// A second init without --force must refuse and leave the file alone.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"config", "init", "--path", path}, &stdout, &stderr); code == 0 {
		t.Error("second init without --force should fail")
	} else if !strings.Contains(stderr.String(), "--force") {
		t.Errorf("stderr should mention --force: %s", stderr.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Error("refused init must not modify the existing file")
	}

	// --force overwrites.
	if err := os.WriteFile(path, []byte("clobbered"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"config", "init", "--path", path, "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("forced init: code=%d stderr=%s", code, stderr.String())
	}
	data, err = os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "full_fsync") {
		t.Errorf("forced init did not overwrite: %s", data)
	}
}

// TestConfigShowRedactsSecrets proves `config show` prints the effective
// config (file values applied) without ever printing the actual Gemini
// key.
func TestConfigShowRedactsSecrets(t *testing.T) {
	noKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[web]\nport = 9191\n\n[gemini]\napi_keys = [\"AIzaNotARealKeyJustForTesting\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"config", "show", "--config", path}, &stdout, &stderr); code != 0 {
		t.Fatalf("show: code=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "NotARealKeyJustForTesting") {
		t.Errorf("config show leaked the secret: %s", out)
	}
	if !strings.Contains(out, "web.port=9191") || !strings.Contains(out, "gemini.api_keys=1") {
		t.Errorf("config show missing expected fields: %s", out)
	}
}

func TestConfigUsageAndErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"config"}, &out, &errb); code != 2 {
		t.Errorf("no subcommand: code=%d", code)
	}
	errb.Reset()
	if code := run(context.Background(), []string{"config", "bogus"}, &out, &errb); code != 2 {
		t.Errorf("unknown subcommand: code=%d", code)
	}
	out.Reset()
	if code := run(context.Background(), []string{"config", "help"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "show") {
		t.Errorf("help: code=%d out=%s", code, out.String())
	}
	errb.Reset()
	if code := run(context.Background(), []string{"config", "show", "--config", filepath.Join(t.TempDir(), "bad.toml"), "--nope"}, &out, &errb); code != 2 {
		t.Errorf("bad flag: code=%d", code)
	}
}
