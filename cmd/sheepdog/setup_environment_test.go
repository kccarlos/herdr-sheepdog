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
	prompt := newPrompter(strings.NewReader("2\n\ny\n"), &out)
	var checked string
	var restartedBackend string
	changed, err := setupEnvironment(context.Background(), setupTestConfig(), envPath, setupEnvironmentOptions{
		Probe:       setupTestProbe(appHome, true, true),
		Panel:       panel,
		Layout:      []byte("layout"),
		Prompt:      prompt,
		Interactive: true,
		HerdrAppCheck: func(_ context.Context, modDir string) (string, error) {
			checked = modDir
			return "loaded sheepdog\n", nil
		},
		LayoutSwitch: func(context.Context) (string, error) {
			return "switched\n", nil
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
		"Switched herdr-app to the Sheepdog layout.",
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
		Layout: []byte("layout"),
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
		"Installed the Sheepdog layout for herdr-app.",
		`herdr-app do layout.switch '{"name":"sheepdog"}'`,
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
		Probe:  setupTestProbe(appHome, true, true),
		Panel:  setupTestPanel(),
		Layout: []byte("layout"),
		Out:    &out,
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

func TestSetupSheepdogLayout(t *testing.T) {
	panel := setupTestPanel()
	baseConfig := setupTestConfig()
	baseConfig.Herdr.Backend = config.HerdrBackendHC // no backend question in these layout cases
	for _, tc := range []struct {
		name        string
		answer      string
		interactive bool
		existing    string
		shownDocked bool
		switchErr   error
		wantWritten bool
		wantSwitch  int
		wantText    []string
	}{
		{
			name: "yes writes and switches", answer: "y\n", interactive: true,
			wantWritten: true, wantSwitch: 1,
			wantText: []string{"Installed the Sheepdog layout for herdr-app.", "Switched herdr-app to the Sheepdog layout."},
		},
		{
			name: "no writes nothing and says how to dock later", answer: "n\n", interactive: true,
			wantText: []string{"Skipped the Sheepdog layout.", "choose Copy my layout"},
		},
		{
			name:        "no tty installs but does not switch",
			wantWritten: true,
			wantText:    []string{"Installed the Sheepdog layout for herdr-app.", `herdr-app do layout.switch '{"name":"sheepdog"}'`},
		},
		{
			name: "different file is kept", answer: "y\n", interactive: true,
			existing: "mine", wantSwitch: 1,
			wantText: []string{"differs from this version, so setup kept it.", "Switched herdr-app to the Sheepdog layout."},
		},
		{
			name: "already docked does nothing", existing: "mine", shownDocked: true,
			wantText: []string{"docked in herdr-app's main layout"},
		},
		{
			name: "switch failure is a warning", answer: "y\n", interactive: true,
			switchErr: errors.New("exit status 1"), wantWritten: true, wantSwitch: 1,
			wantText: []string{
				"Warning: could not switch herdr-app to the Sheepdog layout: exit status 1",
				`herdr-app do layout.switch '{"name":"sheepdog"}'`,
				"or press Ctrl+Shift+L in herdr-app.",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			appHome, envPath := filepath.Join(dir, "herdr-app"), filepath.Join(dir, ".env")
			if err := onboard.InstallPanel(appHome, panel); err != nil {
				t.Fatal(err)
			}
			shown := `{"id":"root","type":"row","children":[]}`
			if tc.shownDocked {
				shown = `{"id":"root","type":"row","children":[{"type":"block","content":{"mod":"sheepdog"}}]}`
			}
			if err := os.MkdirAll(filepath.Join(appHome, "layouts"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(appHome, "layouts", "main.json"), []byte(shown), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.existing != "" {
				if err := os.WriteFile(filepath.Join(appHome, "layouts", "sheepdog.json"), []byte(tc.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			var out bytes.Buffer
			prompt := newPrompter(strings.NewReader(tc.answer), &out)
			switches := 0
			_, err := setupEnvironment(context.Background(), baseConfig, envPath, setupEnvironmentOptions{
				Probe:       setupTestProbe(appHome, false, false),
				Panel:       panel,
				Layout:      []byte("layout"),
				Prompt:      prompt,
				Interactive: tc.interactive,
				LayoutSwitch: func(context.Context) (string, error) {
					switches++
					return "switched\n", tc.switchErr
				},
				Out: &out,
			})
			if err != nil {
				t.Fatal(err)
			}
			if switches != tc.wantSwitch {
				t.Errorf("layout switches = %d, want %d\n%s", switches, tc.wantSwitch, out.String())
			}
			got, readErr := os.ReadFile(filepath.Join(appHome, "layouts", "sheepdog.json"))
			if tc.wantWritten {
				if readErr != nil || string(got) != "layout" {
					t.Errorf("written layout = %q, %v", got, readErr)
				}
				if st, err := os.Stat(filepath.Join(appHome, "layouts", "sheepdog.json")); err != nil || st.Mode().Perm() != 0o644 {
					t.Errorf("layout mode = %v, %v; want 0644", st.Mode().Perm(), err)
				}
			} else if tc.existing == "" {
				if readErr == nil {
					t.Errorf("layout unexpectedly written as %q", got)
				}
			} else if readErr != nil || string(got) != tc.existing {
				t.Errorf("existing layout = %q, %v; want %q kept", got, readErr, tc.existing)
			}
			for _, want := range tc.wantText {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}
