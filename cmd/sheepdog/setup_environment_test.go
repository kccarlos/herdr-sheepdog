package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

func setupTestPanel() fstest.MapFS {
	return fstest.MapFS{
		"mods/sheepdog/main.js":       &fstest.MapFile{Data: []byte("main"), Mode: 0o644},
		"mods/sheepdog/model/view.js": &fstest.MapFile{Data: []byte("view"), Mode: 0o644},
	}
}

func setupTestProbe(appHome string, oldRunning, coreRunning bool) onboard.Probe {
	return onboard.Probe{
		GOOS: "darwin",
		Home: filepath.Join(appHome, "home"),
		Getenv: func(k string) string {
			if k == "HERDR_APP_HOME" {
				return appHome
			}
			return ""
		},
		LookPath: func(name string) (string, error) {
			if name == "herdr-app" {
				return "/fake/herdr-app", nil
			}
			return "", fs.ErrNotExist
		},
		Stat: func(name string) (fs.FileInfo, error) {
			if strings.HasPrefix(name, appHome+string(filepath.Separator)) {
				return os.Stat(name)
			}
			return nil, fs.ErrNotExist
		},
		ReadFile: func(name string) ([]byte, error) {
			if !strings.HasPrefix(name, appHome+string(filepath.Separator)) {
				return nil, fs.ErrNotExist
			}
			return os.ReadFile(name)
		},
		SocketAnswers: func(name string) bool {
			switch name {
			case "/fake/old.sock":
				return oldRunning
			case "/fake/hc.sock":
				return coreRunning
			default:
				return false
			}
		},
	}
}

func setupTestConfig() config.Config {
	return config.Config{Herdr: config.Herdr{
		Backend:  config.HerdrBackendAuto,
		Socket:   "/fake/old.sock",
		HCSocket: "/fake/hc.sock",
	}}
}

func TestSetupEnvironmentInteractive(t *testing.T) {
	dir := t.TempDir()
	appHome, envPath := filepath.Join(dir, "herdr-app"), filepath.Join(dir, ".env")
	panel := setupTestPanel()
	var out bytes.Buffer
	prompt := newPrompter(strings.NewReader("2\n\n"), &out)
	var checked string
	var restartedBackend string
	changed, err := setupEnvironment(context.Background(), setupTestConfig(), envPath, setupEnvironmentOptions{
		Probe:       setupTestProbe(appHome, true, true),
		Panel:       panel,
		Prompt:      prompt,
		Interactive: true,
		HerdrAppCheck: func(_ context.Context, modDir string) (string, error) {
			checked = modDir
			return "loaded sheepdog\n", nil
		},
		Setenv: func(key, value string) error {
			if key != "SHEEPDOG_HERDR_BACKEND" {
				t.Errorf("unexpected environment key %q", key)
			}
			restartedBackend = value
			return nil
		},
		Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("the backend choice was not reported as written")
	}
	env, err := onboard.ReadEnvFile(envPath)
	if err != nil || env["SHEEPDOG_HERDR_BACKEND"] != config.HerdrBackendHerdr {
		t.Errorf("env = %v, %v", env, err)
	}
	if checked != filepath.Join(appHome, "mods", "sheepdog") {
		t.Errorf("herdr-app check directory = %q", checked)
	}
	if restartedBackend != config.HerdrBackendHerdr {
		t.Errorf("restart backend = %q, want herdr", restartedBackend)
	}
	if got, err := os.ReadFile(filepath.Join(appHome, "mods", "sheepdog", "main.js")); err != nil || string(got) != "main" {
		t.Errorf("installed panel = %q, %v", got, err)
	}
	text := out.String()
	for _, want := range []string{
		"Your setup:",
		"herdr (old, terminal)   installed, running",
		"herdr-core              installed, running",
		"herdr-app               installed",
		"Sheepdog.app            not installed",
		"herdr-core (hc) — herdr-app",
		"old herdr (terminal)",
		"herdr-app check: loaded sheepdog",
		"herdr-app do mod.open --name sheepdog",
		"brew install --cask kccarlos/tap/sheepdog-app",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
}

func TestSetupEnvironmentNonInteractive(t *testing.T) {
	dir := t.TempDir()
	appHome, envPath := filepath.Join(dir, "herdr-app"), filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("KEEP_ME=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	checks := 0
	changed, err := setupEnvironment(context.Background(), setupTestConfig(), envPath, setupEnvironmentOptions{
		Probe:  setupTestProbe(appHome, true, true),
		Panel:  setupTestPanel(),
		Prompt: newPrompter(strings.NewReader(""), &out),
		Out:    &out,
		HerdrAppCheck: func(context.Context, string) (string, error) {
			checks++
			return "loaded sheepdog\n", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a choice without a TTY was reported as written")
	}
	env, err := onboard.ReadEnvFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["SHEEPDOG_HERDR_BACKEND"]; ok || env["KEEP_ME"] != "yes" {
		t.Errorf("env = %v", env)
	}
	if checks != 1 {
		t.Errorf("herdr-app checks = %d, want 1", checks)
	}
	if _, err := os.Stat(filepath.Join(appHome, "mods", "sheepdog", "main.js")); err != nil {
		t.Errorf("panel was not installed: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Both are running, and auto would pick old herdr.",
		"SHEEPDOG_HERDR_BACKEND=hc",
		"Installed the Sheepdog panel for herdr-app.",
		"herdr-app check: loaded sheepdog",
		"In herdr-app, open Sheepdog from the menu bar app's Open panel",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "choice [") {
		t.Errorf("non-interactive setup prompted:\n%s", text)
	}
}

func TestSetupEnvironmentCheckFailureIsAWarning(t *testing.T) {
	dir := t.TempDir()
	appHome := filepath.Join(dir, "herdr-app")
	var out bytes.Buffer
	_, err := setupEnvironment(context.Background(), setupTestConfig(), filepath.Join(dir, ".env"), setupEnvironmentOptions{
		Probe: setupTestProbe(appHome, true, true),
		Panel: setupTestPanel(),
		Out:   &out,
		HerdrAppCheck: func(context.Context, string) (string, error) {
			return "parse warning", errors.New("exit status 1")
		},
	})
	if err != nil {
		t.Fatalf("a failed load check stopped setup: %v", err)
	}
	if text := out.String(); !strings.Contains(text, "Warning: herdr-app check failed: exit status 1") {
		t.Errorf("warning missing:\n%s", text)
	}
}
