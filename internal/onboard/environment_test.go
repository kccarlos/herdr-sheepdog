package onboard

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

type fakeFSInfo struct {
	name string
	dir  bool
}

func (f fakeFSInfo) Name() string { return f.name }
func (f fakeFSInfo) Size() int64  { return 0 }
func (f fakeFSInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (f fakeFSInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFSInfo) IsDir() bool        { return f.dir }
func (f fakeFSInfo) Sys() any           { return nil }

type fakeProbe struct {
	goos    string
	home    string
	env     map[string]string
	paths   map[string]bool
	dirs    map[string]bool
	files   map[string]string
	sockets map[string]bool
}

func (f *fakeProbe) probe() Probe {
	return Probe{
		GOOS: f.goos,
		Home: f.home,
		Getenv: func(k string) string {
			return f.env[k]
		},
		LookPath: func(name string) (string, error) {
			if f.paths[name] {
				return "/fake/bin/" + name, nil
			}
			return "", fs.ErrNotExist
		},
		Stat: func(name string) (fs.FileInfo, error) {
			if f.dirs[name] {
				return fakeFSInfo{name: filepath.Base(name), dir: true}, nil
			}
			if _, ok := f.files[name]; ok {
				return fakeFSInfo{name: filepath.Base(name)}, nil
			}
			return nil, fs.ErrNotExist
		},
		ReadFile: func(name string) ([]byte, error) {
			body, ok := f.files[name]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return []byte(body), nil
		},
		SocketAnswers: func(name string) bool {
			return f.sockets[name]
		},
	}
}

func testPanel(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for name, body := range files {
		out[name] = &fstest.MapFile{Data: []byte(body), Mode: 0o644}
	}
	return out
}

func testHerdr() config.Herdr {
	return config.Herdr{
		Backend:  config.HerdrBackendAuto,
		Socket:   "/fake/old.sock",
		HCSocket: "/fake/hc.sock",
	}
}

func TestDetect(t *testing.T) {
	panel := testPanel(map[string]string{
		"mods/sheepdog/main.js":       "main",
		"mods/sheepdog/model/view.js": "view",
	})
	for _, tc := range []struct {
		name  string
		probe *fakeProbe
		want  Environment
	}{
		{
			name: "old herdr only on linux",
			probe: &fakeProbe{
				goos: "linux", home: "/fake/home",
				paths:   map[string]bool{"herdr": true},
				sockets: map[string]bool{"/fake/old.sock": true},
			},
			want: Environment{
				OS: "linux", OldHerdr: Presence{Installed: true, Answers: true},
				HerdrAppHome: "/fake/home/.config/herdr-app", ShownLayout: "main", Panel: PanelNotApplicable,
			},
		},
		{
			name: "herdr-core only with a custom app home",
			probe: &fakeProbe{
				goos: "linux", home: "/fake/home",
				env:     map[string]string{"HERDR_APP_HOME": "/custom/herdr-app"},
				paths:   map[string]bool{"hc": true, "herdr-app": true},
				dirs:    map[string]bool{"/custom/herdr-app/mods/sheepdog": true},
				sockets: map[string]bool{"/fake/hc.sock": true},
			},
			want: Environment{
				OS: "linux", HerdrCore: Presence{Installed: true, Answers: true},
				HerdrApp: true, HerdrAppHome: "/custom/herdr-app", ShownLayout: "main", Panel: PanelOutdated,
			},
		},
		{
			name: "herdr-app without a panel",
			probe: &fakeProbe{
				goos: "linux", home: "/fake/home",
				paths: map[string]bool{"herdr-app": true},
			},
			want: Environment{
				OS: "linux", HerdrApp: true, HerdrAppHome: "/fake/home/.config/herdr-app", ShownLayout: "main", Panel: PanelMissing,
			},
		},
		{
			name: "both on darwin, apps and current panel",
			probe: &fakeProbe{
				goos: "darwin", home: "/fake/home",
				env:   map[string]string{"HERDR_APP_HOME": "/fake/app-home"},
				paths: map[string]bool{"herdr": true, "herdr-core": true},
				dirs: map[string]bool{
					"/Applications/Herdr.app": true, "/Applications/Sheepdog.app": true,
					"/fake/app-home/mods/sheepdog": true,
				},
				files: map[string]string{
					"/fake/app-home/mods/sheepdog/main.js":       "main",
					"/fake/app-home/mods/sheepdog/model/view.js": "view",
					"/fake/app-home/mods/sheepdog/extra.txt":     "kept",
				},
				sockets: map[string]bool{"/fake/old.sock": true, "/fake/hc.sock": true},
			},
			want: Environment{
				OS:       "darwin",
				OldHerdr: Presence{Installed: true, Answers: true}, HerdrCore: Presence{Installed: true, Answers: true},
				HerdrApp: true, HerdrAppHome: "/fake/app-home", ShownLayout: "main", Panel: PanelCurrent, SheepdogApp: true,
			},
		},
		{
			name: "outdated panel is not fooled by extras",
			probe: &fakeProbe{
				goos: "darwin", home: "/fake/home",
				paths: map[string]bool{"herdr-app": true},
				dirs:  map[string]bool{"/fake/home/.config/herdr-app/mods/sheepdog": true},
				files: map[string]string{
					"/fake/home/.config/herdr-app/mods/sheepdog/main.js":       "main",
					"/fake/home/.config/herdr-app/mods/sheepdog/model/view.js": "old",
					"/fake/home/.config/herdr-app/mods/sheepdog/gpui-kit.d.ts": "extra",
				},
			},
			want: Environment{
				OS: "darwin", HerdrApp: true, HerdrAppHome: "/fake/home/.config/herdr-app", ShownLayout: "main", Panel: PanelOutdated,
			},
		},
		{
			name: "linux ignores app bundles",
			probe: &fakeProbe{
				goos: "linux", home: "/fake/home",
				dirs: map[string]bool{
					"/Applications/Herdr.app": true, "/Applications/Sheepdog.app": true,
				},
			},
			want: Environment{
				OS: "linux", HerdrAppHome: "/fake/home/.config/herdr-app", ShownLayout: "main", Panel: PanelNotApplicable,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(config.Config{Herdr: testHerdr()}, tc.probe.probe(), panel, nil)
			if got != tc.want {
				t.Errorf("Detect() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDetectHerdrAppLayout(t *testing.T) {
	const home = "/fake/app-home"
	docked := `{"id":"root","type":"row","children":[{"id":"panel","type":"block","content":{"mod":"sheepdog"}}]}`
	notDocked := `{"id":"root","type":"row","children":[{"id":"main","type":"block","content":{"mod":"tabs"}}]}`
	wantLayout := []byte(docked)
	for _, tc := range []struct {
		name   string
		files  map[string]string
		shown  string
		docked bool
		layout LayoutState
	}{
		{name: "shared absent", shown: "main"},
		{name: "shared present", files: map[string]string{
			home + "/shared.json": `{"layout":"work"}`,
		}, shown: "work"},
		{name: "shown layout without the panel", files: map[string]string{
			home + "/shared.json":       `{"layout":"work"}`,
			home + "/layouts/work.json": notDocked,
		}, shown: "work"},
		{name: "shown layout docks the panel", files: map[string]string{
			home + "/shared.json":         `{"layout":"custom"}`,
			home + "/layouts/custom.json": docked,
		}, shown: "custom", docked: true},
		{name: "sheepdog layout missing", files: map[string]string{
			home + "/shared.json":       `{"layout":"work"}`,
			home + "/layouts/work.json": notDocked,
		}, shown: "work"},
		{name: "sheepdog layout current", files: map[string]string{
			home + "/shared.json":           `{"layout":"sheepdog"}`,
			home + "/layouts/sheepdog.json": string(wantLayout),
		}, shown: "sheepdog", docked: true, layout: LayoutCurrent},
		{name: "sheepdog layout different", files: map[string]string{
			home + "/shared.json":           `{"layout":"work"}`,
			home + "/layouts/work.json":     notDocked,
			home + "/layouts/sheepdog.json": `{"id":"root","type":"row"}`,
		}, shown: "work", layout: LayoutDifferent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &fakeProbe{
				goos: "linux", home: "/fake/home",
				env:   map[string]string{"HERDR_APP_HOME": home},
				paths: map[string]bool{"herdr-app": true},
				files: tc.files,
			}
			got := Detect(config.Config{Herdr: testHerdr()}, probe.probe(), testPanel(nil), wantLayout)
			if got.ShownLayout != tc.shown || got.PanelDocked != tc.docked || got.Layout != tc.layout {
				t.Errorf("layout detection = shown %q docked %v %v, want %q %v %v",
					got.ShownLayout, got.PanelDocked, got.Layout, tc.shown, tc.docked, tc.layout)
			}
		})
	}
}

func TestAdviseBackend(t *testing.T) {
	old := Presence{Installed: true, Answers: true}
	core := Presence{Installed: true, Answers: true}
	installedOld := Presence{Installed: true}
	installedCore := Presence{Installed: true}
	for _, tc := range []struct {
		name    string
		backend string
		env     Environment
		want    BackendAdvice
	}{
		{name: "already herdr", backend: config.HerdrBackendHerdr, env: Environment{OldHerdr: old, HerdrCore: core}},
		{name: "already hc", backend: config.HerdrBackendHC, env: Environment{OldHerdr: old, HerdrCore: core}},
		{name: "auto both default old", env: Environment{OldHerdr: old, HerdrCore: core}, want: BackendAdvice{Ask: true, Default: config.HerdrBackendHerdr, Why: "Both are running, and auto would pick old herdr."}},
		{name: "auto both with app default core", env: Environment{OldHerdr: old, HerdrCore: core, HerdrApp: true}, want: BackendAdvice{Ask: true, Default: config.HerdrBackendHC, Why: "Both are running, and auto would pick old herdr."}},
		{name: "auto core answers", env: Environment{HerdrCore: core}, want: BackendAdvice{Set: config.HerdrBackendHC, Why: "Only herdr-core is running, so pinning it avoids waiting for old herdr after a restart."}},
		{name: "auto old answers for app", env: Environment{OldHerdr: old, HerdrCore: installedCore, HerdrApp: true}, want: BackendAdvice{Ask: true, Default: config.HerdrBackendHC, Why: "herdr-app uses herdr-core, but only old herdr is running."}},
		{name: "auto old answers otherwise"},
		{name: "auto neither core installed", env: Environment{HerdrCore: installedCore}, want: BackendAdvice{Set: config.HerdrBackendHC, Why: "Only herdr-core is installed, so Sheepdog should wait for herdr-core."}},
		{name: "auto neither both default old", env: Environment{OldHerdr: installedOld, HerdrCore: installedCore}, want: BackendAdvice{Ask: true, Default: config.HerdrBackendHerdr, Why: "Both are installed, so choose the one Sheepdog should start with."}},
		{name: "auto neither both with app", env: Environment{OldHerdr: installedOld, HerdrCore: installedCore, HerdrApp: true}, want: BackendAdvice{Ask: true, Default: config.HerdrBackendHC, Why: "Both are installed, so choose the one Sheepdog should start with."}},
		{name: "auto nothing"},
		{name: "empty both is auto", env: Environment{OldHerdr: old, HerdrCore: core}, want: BackendAdvice{Ask: true, Default: config.HerdrBackendHerdr, Why: "Both are running, and auto would pick old herdr."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Herdr: config.Herdr{Backend: tc.backend}}
			if got := AdviseBackend(cfg, tc.env); got != tc.want {
				t.Errorf("AdviseBackend() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestInstallPanel(t *testing.T) {
	panel := testPanel(map[string]string{
		"mods/sheepdog/main.js":     "version one",
		"mods/sheepdog/ui/panel.js": "panel one",
		"mods/sheepdog/mod.json":    `{"place":"right"}`,
		"mods/sheepdog/logo.svg":    "<svg/>",
	})
	home := t.TempDir()
	if err := InstallPanel(home, panel); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"mods/sheepdog/main.js":     "version one",
		"mods/sheepdog/ui/panel.js": "panel one",
	} {
		got, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	for _, name := range []string{"mods", "mods/sheepdog", "mods/sheepdog/ui"} {
		st, err := os.Stat(filepath.Join(home, filepath.FromSlash(name)))
		if err != nil || st.Mode().Perm() != 0o755 {
			t.Errorf("%s mode = %v, %v; want 0755", name, st.Mode().Perm(), err)
		}
	}
	st, err := os.Stat(filepath.Join(home, "mods/sheepdog/main.js"))
	if err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("main.js mode = %v, %v; want 0644", st.Mode().Perm(), err)
	}

	extra := filepath.Join(home, "mods/sheepdog/README.md")
	if err := os.WriteFile(extra, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	panel["mods/sheepdog/main.js"].Data = []byte("version two")
	if err := InstallPanel(home, panel); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "mods/sheepdog/main.js")); string(got) != "version two" {
		t.Errorf("updated main.js = %q", got)
	}
	if got, _ := os.ReadFile(extra); string(got) != "mine" {
		t.Errorf("extra file = %q", got)
	}

	p := Probe{
		GOOS: "linux", Home: home,
		Getenv: func(k string) string {
			if k == "HERDR_APP_HOME" {
				return home
			}
			return ""
		},
		LookPath: func(name string) (string, error) {
			if name == "herdr-app" {
				return "/fake/herdr-app", nil
			}
			return "", fs.ErrNotExist
		},
		Stat:          os.Stat,
		ReadFile:      os.ReadFile,
		SocketAnswers: func(string) bool { return false },
	}
	if got := Detect(config.Config{Herdr: testHerdr()}, p, panel, nil).Panel; got != PanelCurrent {
		t.Errorf("panel after install = %v, want current", got)
	}
}

func TestDoctorEnvironmentResults(t *testing.T) {
	cfg := config.Default()
	cfg.Herdr = testHerdr()
	cfg.Push.Mode = config.PushModeOff
	cfg.Gemini.APIKeys = nil
	cfg.Web.Port = -1 // make the port check fail locally without dialing a real daemon
	env := Environment{
		OS: "darwin", OldHerdr: Presence{Installed: true, Answers: true},
		HerdrCore: Presence{Installed: true, Answers: true}, HerdrApp: true, ShownLayout: "main", Panel: PanelMissing,
	}
	byName := map[string]Result{}
	for _, r := range Doctor(context.Background(), cfg, DoctorOptions{Environment: &env}) {
		byName[r.Name] = r
	}
	if r := byName["herdr-app panel"]; r.Status != Warn || r.Fix != "sheepdog setup installs it" {
		t.Errorf("missing panel = %+v", r)
	}
	if r, ok := byName["herdr-app layout"]; ok {
		t.Errorf("missing panel produced a layout check: %+v", r)
	}
	if r := byName["Sheepdog.app"]; r.Status != Warn || r.Fix != "brew install --cask kccarlos/tap/sheepdog-app" {
		t.Errorf("missing app = %+v", r)
	}
	if r := byName["herdr backend"]; r.Status != Warn || r.Fix != "run sheepdog setup to choose" {
		t.Errorf("backend advice = %+v", r)
	}

	env.Panel, env.SheepdogApp = PanelCurrent, true
	cfg.Herdr.Backend = config.HerdrBackendHC
	byName = map[string]Result{}
	for _, r := range Doctor(context.Background(), cfg, DoctorOptions{Environment: &env}) {
		byName[r.Name] = r
	}
	if r := byName["herdr-app panel"]; r.Status != OK || !strings.Contains(r.Detail, "matches") {
		t.Errorf("current panel = %+v", r)
	}
	if r := byName["Sheepdog.app"]; r.Status != OK {
		t.Errorf("installed app = %+v", r)
	}
	if r := byName["herdr-app layout"]; r.Status != Warn ||
		r.Detail != "the panel is installed but not docked in the layout herdr-app shows (main)" ||
		r.Fix != "sheepdog setup switches to the Sheepdog layout, or open the panel and choose Copy my layout" {
		t.Errorf("undocked panel = %+v", r)
	}
	if r, ok := byName["herdr backend"]; ok {
		t.Errorf("explicit backend produced advice: %+v", r)
	}

	env.PanelDocked = true
	byName = map[string]Result{}
	for _, r := range Doctor(context.Background(), cfg, DoctorOptions{Environment: &env}) {
		byName[r.Name] = r
	}
	if r, ok := byName["herdr-app layout"]; ok {
		t.Errorf("docked panel produced a layout warning: %+v", r)
	}

	env = Environment{OS: "linux", Panel: PanelNotApplicable}
	byName = map[string]Result{}
	for _, r := range Doctor(context.Background(), cfg, DoctorOptions{Environment: &env}) {
		byName[r.Name] = r
	}
	if r := byName["herdr-app panel"]; r.Status != Skip || r.Detail != "herdr-app not found" {
		t.Errorf("not applicable panel = %+v", r)
	}
	if r, ok := byName["Sheepdog.app"]; ok {
		t.Errorf("linux produced Sheepdog.app result: %+v", r)
	}
}
