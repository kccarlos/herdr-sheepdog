package service

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records the commands instead of running them: no test
// touches launchctl or systemctl.
type fakeRunner struct {
	calls []string
	out   map[string]string
	fail  map[string]bool
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, cmd)
	if f.fail[cmd] {
		return "boom", errors.New("exit status 1")
	}
	return f.out[cmd], nil
}

func spec(t *testing.T, goos string) Spec {
	home := t.TempDir()
	return Spec{OS: goos, Binary: "/opt/homebrew/bin/sheepdog", EnvFile: filepath.Join(home, ".config", "sheepdog", ".env"), Home: home, UID: 501, PATH: "/opt/homebrew/bin:/usr/bin:/bin"}
}

func TestPlist(t *testing.T) {
	s := spec(t, "darwin")
	s.ConfigPath = "/Users/a b/cfg & co.toml"
	data, err := s.Render()
	if err != nil {
		t.Fatal(err)
	}
	// Well-formed XML whatever the paths hold.
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	dec.Strict = true
	for {
		if _, err := dec.Token(); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("plist is not XML: %v\n%s", err, data)
		}
	}
	text := string(data)
	for _, want := range []string{
		"<string>" + Label + "</string>",
		"<string>/opt/homebrew/bin/sheepdog</string>\n\t\t<string>serve</string>\n\t\t<string>--env-file</string>\n\t\t<string>" + s.EnvFile + "</string>\n\t\t<string>--config</string>\n\t\t<string>/Users/a b/cfg &amp; co.toml</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<string>/opt/homebrew/bin:/usr/bin:/bin</string>",
		"<string>" + filepath.Join(s.Home, "Library", "Logs", "sheepdog", "serve.log") + "</string>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist lacks %q:\n%s", want, text)
		}
	}
	if s.UnitPath() != filepath.Join(s.Home, "Library", "LaunchAgents", Label+".plist") {
		t.Errorf("unit path = %s", s.UnitPath())
	}
}

func TestSystemdUnit(t *testing.T) {
	s := spec(t, "linux")
	s.Binary = "/home/u/my bin/sheepdog"
	data, err := s.Render()
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`ExecStart="/home/u/my bin/sheepdog" serve --env-file ` + s.EnvFile,
		"Environment=PATH=/opt/homebrew/bin:/usr/bin:/bin",
		"Restart=on-failure",
		"WantedBy=default.target",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit lacks %q:\n%s", want, text)
		}
	}
	if s.UnitPath() != filepath.Join(s.Home, ".config", "systemd", "user", Unit) {
		t.Errorf("unit path = %s", s.UnitPath())
	}
	if got := systemdQuote("50%$x"); got != `"50%%$$x"` {
		t.Errorf("quote = %s", got)
	}
}

func TestRenderRefuses(t *testing.T) {
	s := spec(t, "windows")
	if _, err := s.Render(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("windows: %v", err)
	}
	s = spec(t, "darwin")
	s.Binary = "sheepdog"
	if _, err := s.Render(); err == nil {
		t.Error("a relative binary was accepted")
	}
}

func TestInstallDarwin(t *testing.T) {
	s := spec(t, "darwin")
	f := &fakeRunner{out: map[string]string{
		"launchctl print gui/501/" + Label: "gui/501/io.github.kccarlos.sheepdog = {\n\tstate = running\n\tpid = 4242\n\tlast exit code = 0\n}",
	}}
	var out strings.Builder
	m := Manager{Spec: s, Run: f.run, Out: &out}
	if st, _ := m.Status(context.Background()); !strings.HasPrefix(st, "not installed") {
		t.Errorf("status before = %q", st)
	}
	if err := m.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.UnitPath()); err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(s.LogPath())); err != nil {
		t.Errorf("log dir not made: %v", err)
	}
	want := []string{"launchctl bootout gui/501/" + Label, "launchctl bootstrap gui/501 " + s.UnitPath()}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q", f.calls)
	}
	st, err := m.Status(context.Background())
	if err != nil || !strings.HasSuffix(st, "state running, pid 4242, last exit code 0") {
		t.Errorf("status = %q, %v", st, err)
	}
	f.calls = nil
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.UnitPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("plist still there: %v", err)
	}
	if len(f.calls) != 1 || f.calls[0] != "launchctl bootout gui/501/"+Label {
		t.Errorf("uninstall calls = %q", f.calls)
	}
	if err := m.Uninstall(context.Background()); err != nil {
		t.Errorf("uninstall when not installed: %v", err)
	}
}

func TestInstallDarwinBootstrapFails(t *testing.T) {
	s := spec(t, "darwin")
	f := &fakeRunner{fail: map[string]bool{"launchctl bootstrap gui/501 " + s.UnitPath(): true}}
	if err := (Manager{Spec: s, Run: f.run}).Install(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestInstallLinux(t *testing.T) {
	s := spec(t, "linux")
	f := &fakeRunner{out: map[string]string{"systemctl --user is-active " + Unit: "active\n"}}
	m := Manager{Spec: s, Run: f.run}
	if err := m.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable " + Unit, "systemctl --user restart " + Unit}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %q", f.calls)
	}
	if st, _ := m.Status(context.Background()); !strings.HasSuffix(st, ": active") {
		t.Errorf("status = %q", st)
	}
	f.calls = nil
	if err := m.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	want = []string{"systemctl --user disable --now " + Unit, "systemctl --user daemon-reload"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Errorf("uninstall calls = %q", f.calls)
	}
}

func TestLogsCommand(t *testing.T) {
	name, args, _ := Manager{Spec: spec(t, "darwin")}.LogsCommand(50, true)
	if name != "tail" || strings.Join(args[:3], " ") != "-n 50 -F" || !strings.HasSuffix(args[3], "serve.log") {
		t.Errorf("darwin logs = %s %q", name, args)
	}
	name, args, _ = Manager{Spec: spec(t, "linux")}.LogsCommand(20, false)
	if name != "journalctl" || strings.Join(args, " ") != "--user -u sheepdog.service -n 20 --no-pager" {
		t.Errorf("linux logs = %s %q", name, args)
	}
}

func TestStart(t *testing.T) {
	t.Run("not installed leaves everything alone", func(t *testing.T) {
		f := &fakeRunner{}
		m := Manager{Spec: spec(t, "linux"), Run: f.run}
		installed, started, err := m.Start(context.Background())
		if installed || started || err != nil || len(f.calls) != 0 {
			t.Errorf("installed=%v started=%v err=%v calls=%q", installed, started, err, f.calls)
		}
		if _, err := os.Stat(m.Spec.UnitPath()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Start wrote a unit: %v", err)
		}
	})
	t.Run("linux active is not touched", func(t *testing.T) {
		s := spec(t, "linux")
		writeUnit(t, s)
		f := &fakeRunner{out: map[string]string{"systemctl --user is-active " + Unit: "active\n"}}
		installed, started, err := Manager{Spec: s, Run: f.run}.Start(context.Background())
		if !installed || started || err != nil || len(f.calls) != 1 {
			t.Errorf("installed=%v started=%v err=%v calls=%q", installed, started, err, f.calls)
		}
	})
	t.Run("linux inactive is started", func(t *testing.T) {
		s := spec(t, "linux")
		writeUnit(t, s)
		f := &fakeRunner{out: map[string]string{"systemctl --user is-active " + Unit: "inactive\n"}}
		installed, started, err := Manager{Spec: s, Run: f.run}.Start(context.Background())
		want := "systemctl --user is-active " + Unit + "|systemctl --user start " + Unit
		if !installed || !started || err != nil || strings.Join(f.calls, "|") != want {
			t.Errorf("installed=%v started=%v err=%v calls=%q", installed, started, err, f.calls)
		}
	})
	t.Run("linux start fails", func(t *testing.T) {
		s := spec(t, "linux")
		writeUnit(t, s)
		f := &fakeRunner{fail: map[string]bool{"systemctl --user start " + Unit: true}}
		if _, _, err := (Manager{Spec: s, Run: f.run}).Start(context.Background()); err == nil {
			t.Error("a failed start was not reported")
		}
	})
	t.Run("darwin running is not touched", func(t *testing.T) {
		s := spec(t, "darwin")
		writeUnit(t, s)
		f := &fakeRunner{out: map[string]string{"launchctl print gui/501/" + Label: "state = running\n\tpid = 7\n"}}
		_, started, err := Manager{Spec: s, Run: f.run}.Start(context.Background())
		if started || err != nil || len(f.calls) != 1 {
			t.Errorf("started=%v err=%v calls=%q", started, err, f.calls)
		}
	})
	t.Run("darwin loaded but idle is kickstarted", func(t *testing.T) {
		s := spec(t, "darwin")
		writeUnit(t, s)
		f := &fakeRunner{out: map[string]string{"launchctl print gui/501/" + Label: "state = waiting\n"}}
		_, started, err := Manager{Spec: s, Run: f.run}.Start(context.Background())
		if !started || err != nil || f.calls[len(f.calls)-1] != "launchctl kickstart gui/501/"+Label {
			t.Errorf("started=%v err=%v calls=%q", started, err, f.calls)
		}
	})
	t.Run("darwin not loaded is bootstrapped", func(t *testing.T) {
		s := spec(t, "darwin")
		writeUnit(t, s)
		f := &fakeRunner{fail: map[string]bool{"launchctl print gui/501/" + Label: true}}
		_, started, err := Manager{Spec: s, Run: f.run}.Start(context.Background())
		if !started || err != nil || f.calls[len(f.calls)-1] != "launchctl bootstrap gui/501 "+s.UnitPath() {
			t.Errorf("started=%v err=%v calls=%q", started, err, f.calls)
		}
	})
}

// writeUnit makes the service look installed.
func writeUnit(t *testing.T, s Spec) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.UnitPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.UnitPath(), []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
}
