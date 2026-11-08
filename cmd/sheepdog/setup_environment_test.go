package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/panelsync"
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
			if err := os.WriteFile(filepath.Join(appHome, "shared.json"), []byte(`{"layout":"sheepdog"}`), 0o644); err != nil {
				t.Fatal(err)
			}
			return "{\n  \"ok\": true\n}\n", nil
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
		"herdr-tui               installed, running",
		"herdr-core              installed, running",
		"herdr-app               installed",
		"Sheepdog.app            not installed",
		"herdr-core (hc) — herdr-app",
		"herdr-tui",
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
	// Setup says what it installed, so the daemon can update it later.
	if m, err := panelsync.ReadMarker(filepath.Join(appHome, "mods", "sheepdog")); err != nil || m.Version != version || m.Layout == "" || len(m.Files) == 0 {
		t.Errorf("install marker = %+v, %v", m, err)
	}
	text := out.String()
	for _, want := range []string{
		"Both are running, and auto would pick herdr-tui.",
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
	tries, delay := layoutConfirmTries, layoutConfirmDelay
	layoutConfirmTries, layoutConfirmDelay = 2, 0
	t.Cleanup(func() { layoutConfirmTries, layoutConfirmDelay = tries, delay })
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
		answer2     string // herdr-app's answer; "" is {"ok": true}
		noSwitch    bool   // herdr-app answers but does not show the layout
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
		{
			name: "an ok answer without the switch is a warning", answer: "y\n", interactive: true,
			noSwitch: true, wantWritten: true, wantSwitch: 1,
			wantText: []string{"Warning: could not switch herdr-app to the Sheepdog layout: herdr-app did not show it"},
		},
		{
			name: "herdr-app's error is said", answer: "y\n", interactive: true,
			answer2: "{\n  \"error\": \"no layout \\\"sheepdog\\\"\"\n}\n", noSwitch: true, wantWritten: true, wantSwitch: 1,
			wantText: []string{`Warning: could not switch herdr-app to the Sheepdog layout: no layout "sheepdog"`},
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
				// Like herdr-app: a JSON answer, and the shown layout in
				// shared.json changes when the switch really happened.
				LayoutSwitch: func(context.Context) (string, error) {
					switches++
					if tc.switchErr == nil && !tc.noSwitch {
						if err := os.WriteFile(filepath.Join(appHome, "shared.json"), []byte(`{"layout":"sheepdog"}`), 0o644); err != nil {
							t.Fatal(err)
						}
					}
					if tc.answer2 != "" {
						return tc.answer2, tc.switchErr
					}
					return "{\n  \"ok\": true\n}\n", tc.switchErr
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

func TestSetupEnvironmentKeybindingOptInAndConflict(t *testing.T) {
	tries, delay := layoutConfirmTries, layoutConfirmDelay
	layoutConfirmTries, layoutConfirmDelay = 1, 0
	t.Cleanup(func() { layoutConfirmTries, layoutConfirmDelay = tries, delay })
	panel := setupTestPanel()
	cfg := setupTestConfig()
	cfg.Herdr.Backend = config.HerdrBackendHC

	// 1. Existing conflict declined: existing binding stays untouched.
	dir := t.TempDir()
	appHome, envPath := filepath.Join(dir, "herdr-app"), filepath.Join(dir, ".env")
	if err := os.MkdirAll(appHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appHome, "shared.json"), []byte(`{"layout":"work"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appHome, "keys.json"), []byte(`{"global":{"cmd-shift-s":"app.palette"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	// Answers: install panel (y), switch layout (y), add shortcut (y), overwrite existing binding (n)
	prompt := newPrompter(strings.NewReader("y\ny\ny\nn\n"), &out)
	_, err := setupEnvironment(context.Background(), cfg, envPath, setupEnvironmentOptions{
		Probe:       setupTestProbe(appHome, false, true),
		Panel:       panel,
		Layout:      []byte(`{"id":"root","type":"row","children":[{"type":"block","content":{"mod":"sheepdog"}}]}`),
		Prompt:      prompt,
		Interactive: true,
		LayoutSwitch: func(context.Context) (string, error) {
			_ = os.WriteFile(filepath.Join(appHome, "shared.json"), []byte(`{"layout":"sheepdog"}`), 0o644)
			return `{"ok":true}`, nil
		},
		Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	gotKeys, _ := os.ReadFile(filepath.Join(appHome, "keys.json"))
	if !strings.Contains(string(gotKeys), "app.palette") || strings.Contains(string(gotKeys), "sheepdog.focus") {
		t.Fatalf("declined conflict still overwrote keys.json: %s", gotKeys)
	}

	// 2. Confirming overwrite backs up keys.json and records Previous + PreviousLayout in marker.
	out.Reset()
	// Panel & layout already docked now; only shortcut question + overwrite confirmation run!
	prompt2 := newPrompter(strings.NewReader("y\ny\n"), &out)
	_, err = setupEnvironment(context.Background(), cfg, envPath, setupEnvironmentOptions{
		Probe:       setupTestProbe(appHome, false, true),
		Panel:       panel,
		Layout:      []byte(`{"id":"root","type":"row","children":[{"type":"block","content":{"mod":"sheepdog"}}]}`),
		Prompt:      prompt2,
		Interactive: true,
		Out:         &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	gotKeys, _ = os.ReadFile(filepath.Join(appHome, "keys.json"))
	if !strings.Contains(string(gotKeys), "sheepdog.focus") {
		t.Fatalf("confirmed shortcut not written: %s", gotKeys)
	}
	mk, err := panelsync.ReadMarker(filepath.Join(appHome, "mods", "sheepdog"))
	if err != nil || mk.PreviousLayout != "work" || mk.Keybinding == nil || mk.Keybinding.Previous != "app.palette" {
		t.Fatalf("marker = %+v (keybinding=%+v), %v", mk, mk.Keybinding, err)
	}

	// 3. Symlinked dev checkout (PanelCurrent without a marker): no spurious marker warning and no unrecorded keys.json mutation.
	symDir := t.TempDir()
	symAppHome, symEnvPath := filepath.Join(symDir, "herdr-app"), filepath.Join(symDir, ".env")
	checkout := filepath.Join(symDir, "checkout")
	for rel, f := range panel {
		relMod := strings.TrimPrefix(rel, "mods/sheepdog/")
		dst := filepath.Join(checkout, filepath.FromSlash(relMod))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(symAppHome, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(checkout, filepath.Join(symAppHome, "mods", "sheepdog")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(symAppHome, "shared.json"), []byte(`{"layout":"work"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(symAppHome, "keys.json"), []byte(`{"global":{"cmd-shift-s":"app.palette"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	prompt3 := newPrompter(strings.NewReader("y\ny\ny\n"), &out)
	_, err = setupEnvironment(context.Background(), cfg, symEnvPath, setupEnvironmentOptions{
		Probe:       setupTestProbe(symAppHome, false, true),
		Panel:       panel,
		Layout:      []byte(`{"id":"root","type":"row","children":[{"type":"block","content":{"mod":"sheepdog"}}]}`),
		Prompt:      prompt3,
		Interactive: true,
		LayoutSwitch: func(context.Context) (string, error) {
			_ = os.WriteFile(filepath.Join(symAppHome, "shared.json"), []byte(`{"layout":"sheepdog"}`), 0o644)
			return `{"ok":true}`, nil
		},
		Out: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "could not record the previous layout") {
		t.Fatalf("spurious previous-layout warning for symlinked panel:\n%s", out.String())
	}
	symKeys, _ := os.ReadFile(filepath.Join(symAppHome, "keys.json"))
	if strings.Contains(string(symKeys), "sheepdog.focus") {
		t.Fatalf("symlinked checkout without marker still mutated keys.json: %s", symKeys)
	}
}

// TestSetupEnvironmentRemembersNoAndAutoUpdatesPanel: a "no" to the
// shortcut question is not asked again, even after the panel is updated;
// with panel.auto_update an outdated, unchanged panel is updated without a
// question, and one the user changed is still asked about.
func TestSetupEnvironmentRemembersNoAndAutoUpdatesPanel(t *testing.T) {
	appHome := filepath.Join(t.TempDir(), "herdr-app")
	cfg := setupTestConfig()
	cfg.Herdr.Backend = config.HerdrBackendHC
	cfg.Panel.AutoUpdate = true
	const shortcutQ, panelQ = "Add a keyboard shortcut", "Install the Sheepdog panel for herdr-app?"
	run := func(panel fstest.MapFS, input string) string {
		t.Helper()
		var out bytes.Buffer
		_, err := setupEnvironment(context.Background(), cfg, filepath.Join(appHome, ".env"), setupEnvironmentOptions{
			Probe: setupTestProbe(appHome, false, false), Panel: panel, Layout: []byte("layout"),
			Prompt: newPrompter(strings.NewReader(input), &out), Interactive: true, Out: &out,
		})
		if err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	newer := func(main string) fstest.MapFS {
		p := setupTestPanel()
		p["mods/sheepdog/main.js"] = &fstest.MapFile{Data: []byte(main), Mode: 0o644}
		return p
	}
	main := filepath.Join(appHome, "mods", "sheepdog", "main.js")

	// Install the panel, skip the layout, say no to the shortcut.
	if out := run(setupTestPanel(), "y\nn\nn\n"); !strings.Contains(out, shortcutQ) {
		t.Fatalf("first run did not offer the shortcut:\n%s", out)
	}
	if out := run(setupTestPanel(), "n\n"); strings.Contains(out, shortcutQ) {
		t.Errorf("the declined shortcut was asked again:\n%s", out)
	}
	// A newer, unchanged panel is updated without the panel question, and
	// the "no" survives the update.
	out := run(newer("main2"), "n\n")
	if got, _ := os.ReadFile(main); strings.Contains(out, panelQ) || strings.Contains(out, shortcutQ) || string(got) != "main2" || !strings.Contains(out, "Updated the Sheepdog panel for herdr-app (panel.auto_update).") {
		t.Errorf("auto-update run: main.js=%q\n%s", got, out)
	}
	if mk, err := panelsync.ReadMarker(filepath.Dir(main)); err != nil || !mk.KeyDeclined {
		t.Errorf("marker after the update = %+v, %v", mk, err)
	}
	// A panel the user changed is still asked about.
	if err := os.WriteFile(main, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := run(newer("main3"), "n\n"); !strings.Contains(out, panelQ) {
		t.Errorf("a changed panel was not asked about:\n%s", out)
	}
	if got, _ := os.ReadFile(main); string(got) != "mine" {
		t.Errorf("the changed panel was replaced: %q", got)
	}
}

func setupBackendProbe(appHome string, oldInstalled, oldRunning, coreInstalled, coreRunning, appInstalled bool) onboard.Probe {
	p := setupTestProbe(appHome, oldRunning, coreRunning)
	p.LookPath = func(name string) (string, error) {
		switch name {
		case "herdr":
			if oldInstalled {
				return "/fake/herdr", nil
			}
		case "hc":
			if coreInstalled {
				return "/fake/hc", nil
			}
		case "herdr-app":
			if appInstalled {
				return "/fake/herdr-app", nil
			}
		}
		return "", fs.ErrNotExist
	}
	return p
}

func TestSetupEnvironmentBackendChoice(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		backend                    string
		oldInstalled, oldRunning   bool
		coreInstalled, coreRunning bool
		appInstalled               bool
		interactive                bool
		input                      string
		preEnv                     string
		wantChosen                 string
		wantChanged                bool
		wantEnvBackend             string // "" means env file untouched / key absent
		wantText                   []string
		forbidText                 []string
	}{
		{
			name:          "pinned herdr with both running and herdr-app never asks",
			backend:       config.HerdrBackendHerdr,
			oldInstalled:  true,
			oldRunning:    true,
			coreInstalled: true,
			coreRunning:   true,
			appInstalled:  true,
			interactive:   true,
			input:         "n\n",
			wantChosen:    config.HerdrBackendHerdr,
			wantText:      []string{"herdr backend: using herdr-tui (pinned)."},
			forbidText:    []string{"Which herdr", "choice [", "Set SHEEPDOG_HERDR_BACKEND="},
		},
		{
			name:           "auto with both running asks, default herdr-core with herdr-app, and pins the answer",
			backend:        config.HerdrBackendAuto,
			oldInstalled:   true,
			oldRunning:     true,
			coreInstalled:  true,
			coreRunning:    true,
			appInstalled:   true,
			interactive:    true,
			input:          "\nn\n",
			preEnv:         "KEEP_ME=1\n",
			wantChosen:     config.HerdrBackendHC,
			wantChanged:    true,
			wantEnvBackend: config.HerdrBackendHC,
			wantText: []string{
				"Both are running, and auto would pick herdr-tui.",
				"Agents running in the other herdr will not appear in Sheepdog.",
				"Which herdr should Sheepdog use:",
				"1) herdr-core (hc) — herdr-app",
				"2) herdr-tui (current)",
				"choice [1]:",
				"Set SHEEPDOG_HERDR_BACKEND=hc in ",
				"herdr backend: using herdr-core (pinned).",
			},
		},
		{
			name:           "auto with both running pins herdr-tui when picked",
			backend:        config.HerdrBackendAuto,
			oldInstalled:   true,
			oldRunning:     true,
			coreInstalled:  true,
			coreRunning:    true,
			interactive:    true,
			input:          "2\n",
			wantChosen:     config.HerdrBackendHerdr,
			wantChanged:    true,
			wantEnvBackend: config.HerdrBackendHerdr,
			wantText:       []string{"1) herdr-core (hc)\n", "choice [2]:", "Set SHEEPDOG_HERDR_BACKEND=herdr in ", "herdr backend: using herdr-tui (pinned)."},
		},
		{
			name:           "auto with only herdr-tui running pins it without asking, even with herdr-core and herdr-app installed",
			backend:        config.HerdrBackendAuto,
			oldInstalled:   true,
			oldRunning:     true,
			coreInstalled:  true,
			appInstalled:   true,
			interactive:    true,
			input:          "n\n",
			wantChosen:     config.HerdrBackendHerdr,
			wantChanged:    true,
			wantEnvBackend: config.HerdrBackendHerdr,
			wantText:       []string{"Only herdr-tui is running, so Sheepdog uses it.", "Set SHEEPDOG_HERDR_BACKEND=herdr in ", "herdr backend: using herdr-tui (pinned)."},
			forbidText:     []string{"Which herdr", "choice ["},
		},
		{
			name:           "auto with only herdr-core running pins it without asking",
			backend:        config.HerdrBackendAuto,
			oldInstalled:   true,
			coreInstalled:  true,
			coreRunning:    true,
			interactive:    true,
			wantChosen:     config.HerdrBackendHC,
			wantChanged:    true,
			wantEnvBackend: config.HerdrBackendHC,
			wantText:       []string{"Only herdr-core is running, so Sheepdog uses it.", "herdr backend: using herdr-core (pinned)."},
			forbidText:     []string{"Which herdr", "choice ["},
		},
		{
			name:          "auto with both installed and neither running neither asks nor pins",
			backend:       config.HerdrBackendAuto,
			oldInstalled:  true,
			coreInstalled: true,
			interactive:   true,
			preEnv:        "KEEP_ME=1\n",
			wantChosen:    config.HerdrBackendHerdr,
			wantText:      []string{"Both are installed and neither is running: Sheepdog uses whichever starts first.", "herdr backend: neither is running; Sheepdog uses whichever starts first."},
			forbidText:    []string{"Which herdr", "choice [", "Set SHEEPDOG_HERDR_BACKEND="},
		},
		{
			name:           "auto with only herdr-tui installed pins it",
			backend:        config.HerdrBackendAuto,
			oldInstalled:   true,
			interactive:    true,
			wantChosen:     config.HerdrBackendHerdr,
			wantChanged:    true,
			wantEnvBackend: config.HerdrBackendHerdr,
			wantText:       []string{"Only herdr-tui is installed, so Sheepdog uses it.", "herdr backend: using herdr-tui (pinned; not running)."},
		},
		{
			name:          "non-interactive pinned leaves env file untouched and never prompts",
			backend:       config.HerdrBackendHerdr,
			oldInstalled:  true,
			oldRunning:    true,
			coreInstalled: true,
			coreRunning:   true,
			interactive:   false,
			preEnv:        "KEEP_ME=1\n",
			wantChosen:    config.HerdrBackendHerdr,
			wantChanged:   false,
			wantText:      []string{"herdr backend: using herdr-tui (pinned)."},
			forbidText:    []string{"choice [", "Which herdr", "Set SHEEPDOG_HERDR_BACKEND=", "herdr.backend="},
		},
		{
			name:          "auto with both running non-interactive names resolved backend and keeps advice",
			backend:       config.HerdrBackendAuto,
			oldInstalled:  true,
			oldRunning:    true,
			coreInstalled: true,
			coreRunning:   true,
			appInstalled:  true,
			interactive:   false,
			wantChosen:    config.HerdrBackendHerdr,
			wantChanged:   false,
			wantText: []string{
				"Both are running, and auto would pick herdr-tui.",
				"SHEEPDOG_HERDR_BACKEND=hc",
				"herdr backend: using herdr-tui (auto: both herdr-tui and herdr-core are running).",
			},
			forbidText: []string{"choice ["},
		},
		{
			name:          "pinned but not running says so",
			backend:       config.HerdrBackendHC,
			coreInstalled: true,
			coreRunning:   false,
			interactive:   true,
			wantChosen:    config.HerdrBackendHC,
			wantChanged:   false,
			wantText:      []string{"herdr backend: using herdr-core (pinned; not running)."},
			forbidText:    []string{"choice [", "herdr.backend="},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			appHome, envPath := filepath.Join(dir, "herdr-app"), filepath.Join(dir, ".env")
			if tc.preEnv != "" {
				if err := os.WriteFile(envPath, []byte(tc.preEnv), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := setupTestConfig()
			cfg.Herdr.Backend = tc.backend
			var out bytes.Buffer
			var setenvVal string
			var chosen string
			changed, err := setupEnvironment(context.Background(), cfg, envPath, setupEnvironmentOptions{
				Probe:       setupBackendProbe(appHome, tc.oldInstalled, tc.oldRunning, tc.coreInstalled, tc.coreRunning, tc.appInstalled),
				Panel:       setupTestPanel(),
				Layout:      []byte("layout"),
				Prompt:      newPrompter(strings.NewReader(tc.input), &out),
				Interactive: tc.interactive,
				Setenv: func(k, v string) error {
					if k != "SHEEPDOG_HERDR_BACKEND" {
						t.Errorf("unexpected Setenv key %q", k)
					}
					setenvVal = v
					return nil
				},
				Out:    &out,
				Chosen: &chosen,
			})
			if err != nil {
				t.Fatalf("setupEnvironment: %v\n%s", err, out.String())
			}
			if changed != tc.wantChanged || chosen != tc.wantChosen {
				t.Errorf("changed=%v chosen=%q, want changed=%v chosen=%q\n%s", changed, chosen, tc.wantChanged, tc.wantChosen, out.String())
			}
			if setenvVal != tc.wantEnvBackend {
				t.Errorf("Setenv = %q, want %q", setenvVal, tc.wantEnvBackend)
			}
			if tc.wantEnvBackend == "" {
				got, err := os.ReadFile(envPath)
				if tc.preEnv == "" {
					if !errors.Is(err, os.ErrNotExist) {
						t.Errorf("env file unexpectedly created: %q (%v)", got, err)
					}
				} else if err != nil || string(got) != tc.preEnv {
					t.Errorf("env file mutated = %q (%v), want %q", got, err, tc.preEnv)
				}
			} else {
				env, err := onboard.ReadEnvFile(envPath)
				if err != nil || env["SHEEPDOG_HERDR_BACKEND"] != tc.wantEnvBackend {
					t.Errorf("env = %v, %v; want SHEEPDOG_HERDR_BACKEND=%s", env, err, tc.wantEnvBackend)
				}
				if tc.preEnv != "" && env["KEEP_ME"] != "1" {
					t.Errorf("existing env key lost: %v", env)
				}
			}
			text := out.String()
			for _, want := range tc.wantText {
				if !strings.Contains(text, want) {
					t.Errorf("output missing %q:\n%s", want, text)
				}
			}
			for _, bad := range tc.forbidText {
				if strings.Contains(text, bad) {
					t.Errorf("output unexpectedly contains %q:\n%s", bad, text)
				}
			}
		})
	}
}

func TestSetupDaemonHerdrMessage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stateJSON  string
		chosenKind string
		wantLines  int
		wantText   []string
		forbidText []string
	}{
		{
			name:       "daemon agrees with chosen backend",
			stateJSON:  `{"herdr":"socket","herdr_backend":"hc"}`,
			chosenKind: config.HerdrBackendHC,
			wantText:   []string{"Daemon herdr: herdr-core NOT reachable:", "/fake/hc.sock"},
			forbidText: []string{"differs from your choice", "Fix:"},
		},
		{
			name:       "daemon disagrees: running herdr-tui when choice is herdr-core",
			stateJSON:  `{"herdr":"socket","herdr_backend":"herdr"}`,
			chosenKind: config.HerdrBackendHC,
			wantText: []string{
				"Daemon herdr: herdr-tui NOT reachable:",
				"differs from your choice (herdr-core).",
				"Fix: run `sheepdog restart` (or check SHEEPDOG_HERDR_BACKEND in your shell or login service).",
			},
		},
		{
			name:       "daemon disagrees: running herdr-core when choice is herdr-tui",
			stateJSON:  `{"herdr":"socket","herdr_backend":"hc"}`,
			chosenKind: config.HerdrBackendHerdr,
			wantText: []string{
				"Daemon herdr: herdr-core NOT reachable:",
				"differs from your choice (herdr-tui).",
				"Fix: run `sheepdog restart`",
			},
		},
		{
			name:       "older daemon without herdr_backend prints single restart line without claiming chosen backend",
			stateJSON:  `{"herdr":"socket"}`,
			chosenKind: config.HerdrBackendHC,
			wantLines:  1,
			wantText:   []string{"Daemon herdr: the running daemon is too old to report its herdr; run `sheepdog restart`."},
			forbidText: []string{"herdr-core", "herdr-tui", "differs from your choice"},
		},
		{
			name:       "mock daemon prints nothing",
			stateJSON:  `{"herdr":"mock"}`,
			chosenKind: config.HerdrBackendHC,
			forbidText: []string{"Daemon herdr:"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/state" {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(tc.stateJSON))
			}))
			defer srv.Close()

			l := &lifecycle{cfg: setupTestConfig()}
			var out bytes.Buffer
			l.printRunningHerdr(context.Background(), srv.URL, tc.chosenKind, &out)
			text := out.String()
			if tc.wantLines > 0 && strings.Count(text, "\n") != tc.wantLines {
				t.Errorf("lines = %d, want %d:\n%s", strings.Count(text, "\n"), tc.wantLines, text)
			}
			for _, want := range tc.wantText {
				if !strings.Contains(text, want) {
					t.Errorf("output missing %q:\n%s", want, text)
				}
			}
			for _, bad := range tc.forbidText {
				if strings.Contains(text, bad) {
					t.Errorf("output unexpectedly contains %q:\n%s", bad, text)
				}
			}
		})
	}

	// Also verify the reachable formatting directly without needing a live unix socket.
	got := formatDaemonHerdr(statusHerdr{Reachable: true, Program: "herdr-tui", Socket: "/fake/old.sock"}, config.HerdrBackendHC)
	for _, want := range []string{
		"Daemon herdr: herdr-tui reachable (/fake/old.sock) — differs from your choice (herdr-core).",
		"Fix: run `sheepdog restart`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reachable mismatch message missing %q:\n%s", want, got)
		}
	}
}

func TestStatusHerdrOlderAndCurrentDaemon(t *testing.T) {
	var stateJSON string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/hello":
			_, _ = w.Write([]byte(`{"name":"sheepdog","version":"v0.1.27"}`))
		case "/api/state":
			_, _ = w.Write([]byte(stateJSON))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf("data_dir = %q\n[web]\nport = %d\n[herdr]\nbackend = %q\nsocket = %q\nhc_socket = %q\n",
		filepath.Join(dir, "data"), port, config.HerdrBackendHC, "/fake/old.sock", "/fake/hc.sock")
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// Older daemon omits herdr_backend: status keeps its previous output without claiming daemon source.
	stateJSON = `{"herdr":"socket"}`
	code, out, errs := runCLI("status", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "  herdr         herdr-core NOT reachable:") || !strings.Contains(out, "/fake/hc.sock") {
		t.Fatalf("older daemon status: exit %d err=%q:\n%s", code, errs, out)
	}
	for _, bad := range []string{"Daemon herdr", "too old", "from daemon"} {
		if strings.Contains(out, bad) {
			t.Errorf("older daemon status unexpectedly contains %q:\n%s", bad, out)
		}
	}

	// Current daemon reports herdr_backend: status reports the daemon's backend over local config.
	stateJSON = `{"herdr":"socket","herdr_backend":"herdr"}`
	code, out, errs = runCLI("status", "--config", cfgPath)
	if code != 0 || !strings.Contains(out, "  herdr         herdr-tui NOT reachable:") || !strings.Contains(out, "/fake/old.sock") {
		t.Fatalf("current daemon status: exit %d err=%q:\n%s", code, errs, out)
	}
	code, rawJSON, _ := runCLI("status", "--json", "--config", cfgPath)
	var rep statusReport
	if err := json.Unmarshal([]byte(rawJSON), &rep); err != nil || code != 0 || rep.Herdr.Program != "herdr-tui" {
		t.Fatalf("current daemon json: exit %d err=%v herdr=%+v", code, err, rep.Herdr)
	}
}

func TestSetupRestartGuard(t *testing.T) {
	fakeKeyAPI(t)
	noOnboardKeys(t)
	oldExe := osExecutable
	osExecutable = func() (string, error) { return "/usr/local/bin/sheepdog", nil }
	t.Cleanup(func() { osExecutable = oldExe })

	// Probe with only herdr-core answering under auto so setupEnvironment writes SHEEPDOG_HERDR_BACKEND=hc.
	oldProbe := setupEnvironmentProbe
	setupEnvironmentProbe = func() onboard.Probe {
		return setupBackendProbe(filepath.Join(t.TempDir(), "herdr-app"), false, false, true, true, false)
	}
	t.Cleanup(func() { setupEnvironmentProbe = oldProbe })

	t.Run("stopped daemon with installed login service is not started", func(t *testing.T) {
		unsetEnv(t, "SHEEPDOG_HERDR_BACKEND")
		t.Cleanup(func() { _ = os.Unsetenv("SHEEPDOG_HERDR_BACKEND") })
		_, calls := fakeService(t)
		dir := t.TempDir()
		cfgPath, envPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, ".env")
		cfgBody := fmt.Sprintf("data_dir = %q\n[web]\nport = %d\n[herdr]\nsocket = %q\nhc_socket = %q\n",
			filepath.Join(dir, "data"), freePort(t), "/fake/old.sock", "/fake/hc.sock")
		if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
			t.Fatal(err)
		}
		var iOut, iErr strings.Builder
		if code := run(context.Background(), []string{"service", "install", "--binary", "/usr/local/bin/sheepdog", "--config", cfgPath, "--env-file", envPath}, &iOut, &iErr); code != 0 {
			t.Fatalf("service install exit %d: %s", code, iErr.String())
		}
		*calls = nil

		var out, errb strings.Builder
		code := runSetup(context.Background(), []string{"--terminal", "--config", cfgPath, "--env-file", envPath}, strings.NewReader("3\n"+initGeminiKey+"\n\n\n\n"), &out, &errb)
		if code != 0 {
			t.Fatalf("setup --terminal exit %d:\nout:\n%s\nerr:\n%s", code, out.String(), errb.String())
		}
		env, err := onboard.ReadEnvFile(envPath)
		if err != nil || env["SHEEPDOG_HERDR_BACKEND"] != config.HerdrBackendHC {
			t.Fatalf("env = %v, %v; want SHEEPDOG_HERDR_BACKEND=hc", env, err)
		}
		for _, bad := range []string{"Restarting the running daemon", "sheepdog is not running"} {
			if strings.Contains(out.String(), bad) {
				t.Errorf("output unexpectedly contains %q:\n%s", bad, out.String())
			}
		}
		for _, call := range *calls {
			for _, verb := range []string{"start", "stop", "restart", "bootstrap", "bootout", "kickstart"} {
				if strings.Contains(call, verb) {
					t.Errorf("stopped service got lifecycle command %q (all calls: %q)", call, *calls)
				}
			}
		}
	})

	t.Run("running daemon without pid file or service does not abort setup", func(t *testing.T) {
		_, _ = fakeService(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/hello":
				_, _ = w.Write([]byte(`{"name":"sheepdog","version":"v0.1.27"}`))
			case "/api/state":
				_, _ = w.Write([]byte(`{"herdr":"socket","herdr_backend":"herdr"}`))
			default:
				_, _ = w.Write([]byte(`{}`))
			}
		}))
		defer srv.Close()
		port := srv.Listener.Addr().(*net.TCPAddr).Port

		origDisplay := hasDisplay
		hasDisplay = func() bool { return true }
		t.Cleanup(func() { hasDisplay = origDisplay })

		for _, mode := range []struct {
			name  string
			args  []string
			stdin string
		}{
			{name: "browser", args: []string{"--no-open"}, stdin: ""},
			{name: "terminal", args: []string{"--terminal"}, stdin: "3\n" + initGeminiKey + "\n\n\n\nn\n"},
		} {
			t.Run(mode.name, func(t *testing.T) {
				unsetEnv(t, "SHEEPDOG_HERDR_BACKEND")
				t.Cleanup(func() { _ = os.Unsetenv("SHEEPDOG_HERDR_BACKEND") })
				dir := t.TempDir()
				cfgPath, envPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, ".env")
				cfgBody := fmt.Sprintf("data_dir = %q\n[web]\nport = %d\n[herdr]\nsocket = %q\nhc_socket = %q\n",
					filepath.Join(dir, "data"), port, "/fake/old.sock", "/fake/hc.sock")
				if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
					t.Fatal(err)
				}
				var out, errb strings.Builder
				args := append(append([]string{}, mode.args...), "--config", cfgPath)
				code := runSetup(context.Background(), args, strings.NewReader(mode.stdin), &out, &errb)
				if code != 0 {
					t.Fatalf("setup (%s) exit %d:\nout:\n%s\nerr:\n%s", mode.name, code, out.String(), errb.String())
				}
				env, err := onboard.ReadEnvFile(envPath)
				if err != nil || env["SHEEPDOG_HERDR_BACKEND"] != config.HerdrBackendHC {
					t.Fatalf("env = %v, %v; want SHEEPDOG_HERDR_BACKEND=hc", env, err)
				}
				combined := out.String() + errb.String()
				for _, bad := range []string{"Restarting the running daemon", "left no pid file", "cannot stop"} {
					if strings.Contains(combined, bad) {
						t.Errorf("output unexpectedly contains %q:\n%s", bad, combined)
					}
				}
				for _, want := range []string{
					"Daemon herdr: herdr-tui NOT reachable:",
					"differs from your choice (herdr-core).",
					"Fix: run `sheepdog restart`",
				} {
					if !strings.Contains(out.String(), want) {
						t.Errorf("output missing %q:\n%s", want, out.String())
					}
				}
			})
		}
	})
}

func TestEnvBackendOverridesConfigPin(t *testing.T) {
	unsetEnv(t, "SHEEPDOG_HERDR_BACKEND")
	dir := t.TempDir()
	cfgPath, envPath := filepath.Join(dir, "config.toml"), filepath.Join(dir, ".env")
	if err := os.WriteFile(cfgPath, []byte("data_dir = "+`"`+filepath.Join(dir, "data")+`"`+"\n[herdr]\nbackend = \"herdr\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte("SHEEPDOG_HERDR_BACKEND=hc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	l, code, ok := loadLifecycle(context.Background(), "setup", cfgPath, envPath, &out, &errb)
	if !ok || code != 0 {
		t.Fatalf("loadLifecycle: code=%d err=%s", code, errb.String())
	}
	if l.cfg.Herdr.Backend != config.HerdrBackendHC {
		t.Fatalf("Herdr.Backend = %q, want %q (SHEEPDOG_HERDR_BACKEND must override config.toml)", l.cfg.Herdr.Backend, config.HerdrBackendHC)
	}
}

func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if newPrompter(f, io.Discard).tty != nil {
		t.Fatal("/dev/null counted as a terminal, so setup would prompt with no one to answer")
	}
}
