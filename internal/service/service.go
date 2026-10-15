// Package service installs `sheepdog serve` as a user service, so the
// daemon runs at login and restarts after a crash without a terminal or
// tmux: a launchd user agent on macOS, a systemd --user unit on Linux.
//
// The unit runs `sheepdog serve --env-file <config.DefaultEnvPath()>`
// (plus --config when one was given), with the PATH of the shell that
// installed it, so herdr and the agents' CLIs resolve as they do there.
// Everything that touches the system goes through Runner, and every path
// through Spec, so tests generate the files in a temp dir and never call
// launchctl or systemctl.
package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// Label is the launchd label and the plist's base name.
	Label = "io.github.kccarlos.sheepdog"
	// Unit is the systemd user unit's name.
	Unit = "sheepdog.service"
)

// ErrUnsupported is returned on a system with neither launchd nor
// systemd.
var ErrUnsupported = errors.New("service: only macOS (launchd) and Linux (systemd --user) are supported")

// Runner runs a system command (launchctl, systemctl, journalctl, tail)
// and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// ExecRunner runs the command for real.
func ExecRunner(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// Spec is what the service runs and where its files go.
type Spec struct {
	// OS is "darwin" or "linux" (runtime.GOOS).
	OS string
	// Binary is the sheepdog executable to run (absolute).
	Binary string
	// EnvFile is passed as serve --env-file (config.DefaultEnvPath()).
	EnvFile string
	// ConfigPath, when set, is passed as serve --config.
	ConfigPath string
	// Home is the user's home directory; the unit file and the log go
	// under it.
	Home string
	// UID is the user's id, for launchctl's gui/<uid> domain.
	UID int
	// PATH is the PATH the daemon gets (the installing shell's).
	PATH string
}

// Args is the command line the service runs.
func (s Spec) Args() []string {
	args := []string{s.Binary, "serve", "--env-file", s.EnvFile}
	if s.ConfigPath != "" {
		args = append(args, "--config", s.ConfigPath)
	}
	return args
}

// UnitPath is the plist (macOS) or the unit file (Linux).
func (s Spec) UnitPath() string {
	if s.OS == "darwin" {
		return filepath.Join(s.Home, "Library", "LaunchAgents", Label+".plist")
	}
	return filepath.Join(s.Home, ".config", "systemd", "user", Unit)
}

// LogPath is where launchd writes the daemon's stdout and stderr (macOS;
// on Linux the journal has them).
func (s Spec) LogPath() string {
	return filepath.Join(s.Home, "Library", "Logs", "sheepdog", "serve.log")
}

func (s Spec) check() error {
	if s.OS != "darwin" && s.OS != "linux" {
		return ErrUnsupported
	}
	if !filepath.IsAbs(s.Binary) {
		return fmt.Errorf("service: binary %q is not an absolute path", s.Binary)
	}
	if s.Home == "" {
		return errors.New("service: no home directory")
	}
	return nil
}

// Render is the unit file's content.
func (s Spec) Render() ([]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if s.OS == "darwin" {
		return s.plist(), nil
	}
	return s.systemdUnit(), nil
}

func xmlText(v string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}

// plist is the launchd agent: started at login (RunAtLoad), restarted
// when it exits (KeepAlive) at most every 10 s, output to LogPath.
func (s Spec) plist() []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range s.Args() {
		b.WriteString("\t\t<string>" + xmlText(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>WorkingDirectory</key>
	<string>` + xmlText(s.Home) + `</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>` + xmlText(s.PATH) + `</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>` + xmlText(s.LogPath()) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlText(s.LogPath()) + `</string>
</dict>
</plist>
`)
	return []byte(b.String())
}

// systemdQuote quotes one ExecStart word when it needs it.
func systemdQuote(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\"'\\$%;") {
		return a
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`)
	return `"` + r.Replace(a) + `"`
}

// systemdUnit is the user unit: started with the user's session
// (default.target), restarted on failure after 5 s.
func (s Spec) systemdUnit() []byte {
	words := make([]string, 0, len(s.Args()))
	for _, a := range s.Args() {
		words = append(words, systemdQuote(a))
	}
	return []byte(`[Unit]
Description=sheepdog: manage coding agents by voice
After=network-online.target

[Service]
Type=simple
ExecStart=` + strings.Join(words, " ") + `
WorkingDirectory=` + systemdQuote(s.Home) + `
Environment=` + systemdQuote("PATH="+s.PATH) + `
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`)
}

// Manager installs, removes and inspects the service.
type Manager struct {
	Spec Spec
	Run  Runner
	// Out receives what each step did.
	Out io.Writer
}

func (m Manager) say(format string, a ...any) {
	if m.Out != nil {
		_, _ = fmt.Fprintf(m.Out, format+"\n", a...)
	}
}

func (m Manager) domain() string { return fmt.Sprintf("gui/%d", m.Spec.UID) }

// Install writes the unit file and starts the service (replacing a
// running one, so a reinstall picks up a new binary or flags).
func (m Manager) Install(ctx context.Context) error {
	data, err := m.Spec.Render()
	if err != nil {
		return err
	}
	path := m.Spec.UnitPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	m.say("wrote %s", path)
	if m.Spec.OS == "darwin" {
		if err := os.MkdirAll(filepath.Dir(m.Spec.LogPath()), 0o755); err != nil {
			return err
		}
		// bootout fails when it is not loaded: that is the normal case.
		_, _ = m.Run(ctx, "launchctl", "bootout", m.domain()+"/"+Label)
		if out, err := m.Run(ctx, "launchctl", "bootstrap", m.domain(), path); err != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(out))
		}
		m.say("started %s (logs: %s)", Label, m.Spec.LogPath())
		return nil
	}
	if out, err := m.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %v: %s", err, strings.TrimSpace(out))
	}
	if out, err := m.Run(ctx, "systemctl", "--user", "enable", Unit); err != nil {
		return fmt.Errorf("systemctl enable: %v: %s", err, strings.TrimSpace(out))
	}
	if out, err := m.Run(ctx, "systemctl", "--user", "restart", Unit); err != nil {
		return fmt.Errorf("systemctl restart: %v: %s", err, strings.TrimSpace(out))
	}
	m.say("started %s (logs: journalctl --user -u %s)", Unit, Unit)
	return nil
}

// Uninstall stops the service and removes the unit file. A service that
// is not installed is not an error.
func (m Manager) Uninstall(ctx context.Context) error {
	if err := m.Spec.check(); err != nil {
		return err
	}
	path := m.Spec.UnitPath()
	if m.Spec.OS == "darwin" {
		_, _ = m.Run(ctx, "launchctl", "bootout", m.domain()+"/"+Label)
	} else {
		_, _ = m.Run(ctx, "systemctl", "--user", "disable", "--now", Unit)
	}
	switch err := os.Remove(path); {
	case err == nil:
		m.say("stopped and removed %s", path)
	case errors.Is(err, os.ErrNotExist):
		m.say("not installed (no %s)", path)
		return nil
	default:
		return err
	}
	if m.Spec.OS == "linux" {
		_, _ = m.Run(ctx, "systemctl", "--user", "daemon-reload")
	}
	return nil
}

// Status is the service's state in one line: not installed, or what
// launchd / systemd say about it.
func (m Manager) Status(ctx context.Context) (string, error) {
	if err := m.Spec.check(); err != nil {
		return "", err
	}
	path := m.Spec.UnitPath()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "not installed (run: sheepdog service install)", nil
	}
	if m.Spec.OS == "darwin" {
		out, err := m.Run(ctx, "launchctl", "print", m.domain()+"/"+Label)
		if err != nil {
			return fmt.Sprintf("installed (%s) but not loaded", path), nil
		}
		return "installed (" + path + "): " + launchdState(out), nil
	}
	out, _ := m.Run(ctx, "systemctl", "--user", "is-active", Unit)
	return fmt.Sprintf("installed (%s): %s", path, strings.TrimSpace(out)), nil
}

// Start starts an installed service that is not running and reports
// what it did. It never writes a unit file: a service that is not
// installed is left alone (installed false, no error), and one that
// already runs is not touched.
func (m Manager) Start(ctx context.Context) (installed, started bool, err error) {
	if err := m.Spec.check(); err != nil {
		return false, false, err
	}
	path := m.Spec.UnitPath()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if m.Spec.OS == "darwin" {
		out, perr := m.Run(ctx, "launchctl", "print", m.domain()+"/"+Label)
		if perr != nil {
			// Not loaded (e.g. after a bootout): load it again.
			if out, err := m.Run(ctx, "launchctl", "bootstrap", m.domain(), path); err != nil {
				return true, false, fmt.Errorf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(out))
			}
			return true, true, nil
		}
		if strings.Contains(out, "pid = ") {
			return true, false, nil
		}
		if out, err := m.Run(ctx, "launchctl", "kickstart", m.domain()+"/"+Label); err != nil {
			return true, false, fmt.Errorf("launchctl kickstart: %v: %s", err, strings.TrimSpace(out))
		}
		return true, true, nil
	}
	if out, _ := m.Run(ctx, "systemctl", "--user", "is-active", Unit); strings.TrimSpace(out) == "active" {
		return true, false, nil
	}
	if out, err := m.Run(ctx, "systemctl", "--user", "start", Unit); err != nil {
		return true, false, fmt.Errorf("systemctl start: %v: %s", err, strings.TrimSpace(out))
	}
	return true, true, nil
}

// launchdState picks the state, pid and last exit code out of
// `launchctl print`.
func launchdState(out string) string {
	var parts []string
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch k {
		case "state", "pid", "last exit code":
			parts = append(parts, k+" "+v)
		}
	}
	if len(parts) == 0 {
		return "loaded"
	}
	return strings.Join(parts, ", ")
}

// LogsCommand is the command that shows the last n lines of the
// daemon's output, following it when follow is set.
func (m Manager) LogsCommand(n int, follow bool) (string, []string, error) {
	if err := m.Spec.check(); err != nil {
		return "", nil, err
	}
	if m.Spec.OS == "darwin" {
		args := []string{"-n", fmt.Sprint(n)}
		if follow {
			args = append(args, "-F")
		}
		return "tail", append(args, m.Spec.LogPath()), nil
	}
	args := []string{"--user", "-u", Unit, "-n", fmt.Sprint(n), "--no-pager"}
	if follow {
		args = append(args, "-f")
	}
	return "journalctl", args, nil
}
