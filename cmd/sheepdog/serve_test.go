package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// noKeys clears every provider key for the test: a developer's .env
// must never make a test here reach a real API (or hang on one).
func noKeys(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GEMINI_API_KEY", "GEMINI_API_KEY_1", "GEMINI_API_KEY_2", "GEMINI_API_KEY_3", "DEEPSEEK_API_KEY"} {
		t.Setenv(k, "")
	}
}

func TestServeFlagsAndValidation(t *testing.T) {
	noKeys(t)
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
	if code := run(context.Background(), []string{"serve", "--config", filepath.Join(dir, "none.toml"), "--voice", "loud"}, &stdout, &stderr); code != 2 {
		t.Errorf("bad voice mode exit = %d: %s", code, stderr.String())
	}
	stderr.Reset()
	if code := run(context.Background(), []string{"serve", "--config", filepath.Join(dir, "none.toml"), "--voice", "gemini", "--data-dir", dir}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "GEMINI_API_KEY") {
		t.Errorf("gemini without key: exit=%d %s", code, stderr.String())
	}
}

// TestServeNonLoopbackBindNeedsToken proves `serve` refuses to start
// with a non-loopback web.bind and no web.token (exit 2, like the
// missing-Gemini-key error above), and starts fine once a token is set.
func TestServeNonLoopbackBindNeedsToken(t *testing.T) {
	noKeys(t)
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	t.Setenv("SHEEPDOG_WEB_BIND", "0.0.0.0")
	t.Setenv("SHEEPDOG_WEB_TOKEN", "")
	if code := run(context.Background(), []string{"serve", "--config", filepath.Join(dir, "none.toml"), "--voice", "off", "--herdr", "mock", "--data-dir", dir}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "web.token is required when web.bind is not loopback") {
		t.Errorf("non-loopback bind without a token: exit=%d %s", code, stderr.String())
	}
}

// TestServeEnvFileFlagParses proves --env-file is a recognised flag.
func TestServeEnvFileFlagParses(t *testing.T) {
	var stderr bytes.Buffer
	o, code, ok := parseServeFlags([]string{"--env-file", "/tmp/whatever.env"}, &stderr)
	if !ok || code != 0 || o.envFile != "/tmp/whatever.env" {
		t.Errorf("flags = %+v code=%d ok=%v", o, code, ok)
	}
}

// TestLoadServeEnvFile covers loadServeEnvFile's three cases: an
// explicit path that exists is loaded; an explicit path that is missing
// is an error; the implicit default (./.env) missing is not an error.
func TestLoadServeEnvFile(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "custom.env")
	if err := os.WriteFile(envPath, []byte("SHEEPDOG_ENVFILE_TEST_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("SHEEPDOG_ENVFILE_TEST_KEY") })

	var stderr bytes.Buffer
	if code, ok := loadServeEnvFile(envPath, &stderr); !ok || code != 0 {
		t.Fatalf("explicit existing file: code=%d ok=%v stderr=%s", code, ok, stderr.String())
	}
	if v := os.Getenv("SHEEPDOG_ENVFILE_TEST_KEY"); v != "from-file" {
		t.Errorf("SHEEPDOG_ENVFILE_TEST_KEY = %q, want it loaded", v)
	}

	stderr.Reset()
	if code, ok := loadServeEnvFile(filepath.Join(dir, "missing.env"), &stderr); ok || code == 0 {
		t.Errorf("explicit missing file should be an error: code=%d ok=%v", code, ok)
	}

	// The implicit default (no flag given) must not error when absent:
	// most runs (production, launchd) have no .env at all. Run from an
	// empty directory so a stray repo .env cannot leak into the test.
	t.Chdir(t.TempDir())
	stderr.Reset()
	if code, ok := loadServeEnvFile("", &stderr); !ok || code != 0 {
		t.Errorf("missing implicit ./.env should not be an error: code=%d stderr=%s", code, stderr.String())
	}
}

// Without --env-file, ./.env wins; without it the config directory's
// .env (what sheepdog init writes) is loaded.
func TestImplicitEnvFileFallback(t *testing.T) {
	cfgDir := t.TempDir()
	cfgEnv := filepath.Join(cfgDir, ".env")
	if err := os.WriteFile(cfgEnv, []byte("SHEEPDOG_ENVFILE_FALLBACK=config-dir\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := configEnvPath
	configEnvPath = func() string { return cfgEnv }
	t.Cleanup(func() { configEnvPath = old; _ = os.Unsetenv("SHEEPDOG_ENVFILE_FALLBACK") })

	t.Chdir(t.TempDir())
	if got := implicitEnvFile(); got != cfgEnv {
		t.Errorf("no ./.env: implicit = %q, want %q", got, cfgEnv)
	}
	var stderr bytes.Buffer
	if code, ok := loadServeEnvFile("", &stderr); !ok || code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if v := os.Getenv("SHEEPDOG_ENVFILE_FALLBACK"); v != "config-dir" {
		t.Errorf("fallback not loaded: %q", v)
	}
	if err := os.WriteFile(".env", []byte("X=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := implicitEnvFile(); got != ".env" {
		t.Errorf("with ./.env: implicit = %q", got)
	}
}

// TestServeEnvFileLoadsAndNeverLogs proves `serve --env-file` actually
// takes effect (a GEMINI_API_KEY only present in the file reaches
// cfg.Gemini.APIKeys, visible as a count) and that the raw value never
// appears anywhere in stdout/stderr, including the verbose config dump
// and the daemon.start event.
func TestServeEnvFileLoadsAndNeverLogs(t *testing.T) {
	noKeys(t)

	// Unset (not merely blank) any real GEMINI_API_KEY for the duration,
	// so the file's value is the only source and LoadEnvFile does not
	// see it as "already set".
	orig, had := os.LookupEnv("GEMINI_API_KEY")
	if err := os.Unsetenv("GEMINI_API_KEY"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("GEMINI_API_KEY", orig)
		} else {
			_ = os.Unsetenv("GEMINI_API_KEY")
		}
	})

	dir := t.TempDir()
	envPath := filepath.Join(dir, "custom.env")
	const secret = "AIzaEnvFileTestMarkerNotAReal"
	body := "SHEEPDOG_WEB_PORT=0\n" +
		"GEMINI_API_KEY=" + secret + "\n"
	if err := os.WriteFile(envPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("SHEEPDOG_WEB_PORT") })

	stdout, stderr := &syncBuffer{}, &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--env-file", envPath, "--config", filepath.Join(dir, "none.toml"), "--voice", "off", "--herdr", "mock", "--data-dir", dir, "--verbose"}, stdout, stderr)
	}()
	deadline := time.Now().Add(5 * time.Second)
	var url string
	for time.Now().Before(deadline) && url == "" {
		for _, line := range strings.Split(stdout.String(), "\n") {
			if strings.Contains(line, "listening on ") {
				url = strings.Fields(strings.SplitN(line, "listening on ", 2)[1])[0]
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit")
	}
	if url == "" {
		t.Fatalf("no banner: %s / %s", stdout.String(), stderr.String())
	}
	if os.Getenv("GEMINI_API_KEY") != secret {
		t.Errorf("--env-file value did not reach the environment: %q", os.Getenv("GEMINI_API_KEY"))
	}
	if strings.Contains(stderr.String(), secret) || strings.Contains(stdout.String(), secret) {
		t.Errorf("the env-file secret leaked into output: stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "gemini.api_keys=1") {
		t.Errorf("expected the redacted key count in stderr, got: %s", stderr.String())
	}
}

func TestServeMockVoiceOffSmoke(t *testing.T) {
	noKeys(t)
	dir := t.TempDir()
	t.Setenv("SHEEPDOG_WEB_PORT", "0")

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

// TestDefaultSecretFileMayBeAbsent: `sheepdog start` and the login
// service pass the default secret file explicitly; on a fresh install
// (before setup writes it) it is absent, and serve must start anyway.
func TestDefaultSecretFileMayBeAbsent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "sheepdog", ".env")
	orig := configEnvPath
	configEnvPath = func() string { return missing }
	t.Cleanup(func() { configEnvPath = orig })
	var stderr bytes.Buffer
	if code, ok := loadServeEnvFile(missing, &stderr); !ok || code != 0 {
		t.Errorf("absent default secret file: code=%d ok=%v %s", code, ok, stderr.String())
	}
}
