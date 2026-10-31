package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	herdrsheepdog "github.com/kccarlos/herdr-sheepdog"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

// reportOptions are doctor --report's inputs.
type reportOptions struct {
	configPath, envPath string
	// cfg is the loaded config; loadErr why it did not load (cfg is then
	// the defaults, so paths still resolve).
	cfg     config.Config
	loadErr error
	lines   int
	now     time.Time
}

// commandOutput runs a short command for the report ("" on failure).
var commandOutput = func(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// reTOMLSecret masks the value of a secret-looking key in a config file
// line (token = "…", api_key = "…", api_keys = [ … ]).
var reTOMLSecret = regexp.MustCompile(`(?im)^(\s*[a-z0-9_]*(?:token|key|keys|secret|password)\s*=\s*).+$`)

// buildReport is the diagnostic bundle: versions, OS, the effective
// config, the config file and the env file's key names, the doctor
// results, the status and the end of the logs. Every value that could
// be a secret is masked, and the whole text goes through the redactor
// once more at the end, so a secret that reached a log line before
// redaction existed does not leave the machine either.
func buildReport(ctx context.Context, o reportOptions) string {
	red := redact.ForConfig(o.cfg)
	var b strings.Builder
	h := func(title string) { fmt.Fprintf(&b, "\n### %s\n\n", title) }
	code := func(s string) {
		b.WriteString("```\n")
		b.WriteString(strings.TrimRight(s, "\n"))
		b.WriteString("\n```\n")
	}
	b.WriteString("## sheepdog diagnostic report\n\n")
	fmt.Fprintf(&b, "Generated %s by `sheepdog doctor --report`. Secrets, keys and tokens are replaced by %s; review before posting.\n", o.now.UTC().Format(time.RFC3339), redact.Mask)

	h("Versions")
	exe, _ := osExecutable()
	m := detectInstall(exe)
	osName := runtime.GOOS
	if v := commandOutput("sw_vers", "-productVersion"); v != "" {
		osName += " " + v
	} else if v := commandOutput("uname", "-sr"); v != "" {
		osName += " (" + v + ")"
	}
	herdrV := commandOutput("herdr", "--version")
	if herdrV == "" {
		herdrV = "not on PATH"
	}
	code(strings.Join([]string{
		versionString(version, commit, date),
		"installed as: " + m.Kind + " (" + m.Path + ")",
		"os: " + osName + " " + runtime.GOARCH,
		"go: " + runtime.Version(),
		"herdr: " + herdrV,
	}, "\n"))

	h("Config")
	if o.loadErr != nil {
		code("config did not load: " + o.loadErr.Error())
	} else {
		code(strings.ReplaceAll(o.cfg.String(), " ", "\n"))
	}
	if raw, err := os.ReadFile(o.configPath); err == nil {
		b.WriteString("\n" + o.configPath + " (secret values masked):\n\n")
		code(reTOMLSecret.ReplaceAllString(string(raw), "${1}"+redact.Mask))
	} else {
		b.WriteString("\nno config file at " + o.configPath + " (defaults)\n")
	}
	if names := envFileKeys(o.envPath); names != nil {
		b.WriteString("\n" + o.envPath + " sets (names only): " + strings.Join(names, ", ") + "\n")
	} else {
		b.WriteString("\nno env file at " + o.envPath + "\n")
	}

	if o.loadErr == nil {
		h("Doctor")
		var lines []string
		environment := onboard.Detect(o.cfg, onboard.SystemProbe(), herdrsheepdog.Panel)
		for _, r := range onboard.Doctor(ctx, o.cfg, onboard.DoctorOptions{
			Endpoints: onboardEndpoints(), Probe: false, ConfigPath: o.configPath, EnvPath: o.envPath,
			Environment: &environment,
		}) {
			lines = append(lines, r.Line())
		}
		code(strings.Join(lines, "\n"))

		h("Status")
		l := &lifecycle{cfg: o.cfg}
		if spec, err := serviceSpecFor(o.cfg, "", "", ""); err == nil {
			l.mgr.Spec, l.mgr.Run = spec, serviceRunner
		}
		var sb strings.Builder
		printStatus(&sb, l.status(ctx), o.now)
		code(sb.String())
	}

	logFile := daemon.LogFile(o.cfg.DataDir)
	h(fmt.Sprintf("Runtime log (last %d lines of %s)", o.lines, logFile))
	if tail := tailLines(logFile, o.lines); len(tail) > 0 {
		code(string(tail))
	} else {
		b.WriteString("(no runtime log)\n")
	}
	if stdio := daemon.StdioFile(o.cfg.DataDir); fileSize(stdio) > 0 {
		h("Output outside the log (" + stdio + ")")
		code(string(tailLines(stdio, 50)))
	}
	if old := oldServiceLog(); old != "" {
		h(fmt.Sprintf("Older service log (last %d lines of %s)", o.lines, old))
		code(string(tailLines(old, o.lines)))
	}

	out := red.String(b.String())
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		out = strings.ReplaceAll(out, home, "~")
	}
	return out
}

// envFileKeys lists the variable names an env file sets (never values);
// nil when there is no file.
func envFileKeys(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	names := []string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, _, ok := strings.Cut(line, "="); ok {
			names = append(names, strings.TrimSpace(k))
		}
	}
	return names
}
