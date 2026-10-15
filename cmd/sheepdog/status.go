package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

// statusReport is `sheepdog status --json`.
type statusReport struct {
	Running bool `json:"running"`
	// PID is 0 when the daemon left no pid file (an older version).
	PID       int        `json:"pid,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	// StartedBy is "login service", "background" (sheepdog start) or
	// "terminal" (sheepdog serve).
	StartedBy string `json:"started_by,omitempty"`
	Version   string `json:"version,omitempty"`
	// CLIVersion is this sheepdog binary's version.
	CLIVersion string `json:"cli_version"`
	Port       int    `json:"port"`
	URL        string `json:"url"`
	// Service is the login service: "not installed" or the manager's state.
	Service      string             `json:"service"`
	Voice        statusVoice        `json:"voice"`
	Orchestrator statusOrchestrator `json:"orchestrator"`
	Herdr        statusHerdr        `json:"herdr"`
	// Phones is known only while the daemon runs.
	Phones    *statusPhones    `json:"phones,omitempty"`
	LastError *statusLastError `json:"last_error,omitempty"`
	LogFile   string           `json:"log_file"`
	DataDir   string           `json:"data_dir"`
}

type statusVoice struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	// Source is "daemon" (what the running daemon reports) or "config".
	Source string `json:"source"`
}

type statusOrchestrator struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Source   string `json:"source"`
}

type statusHerdr struct {
	Reachable bool   `json:"reachable"`
	Socket    string `json:"socket"`
	Detail    string `json:"detail,omitempty"`
}

type statusPhones struct {
	// PushDevices is how many phones are registered for notifications.
	PushDevices int `json:"push_devices"`
	// VoiceClientConnected is true while a phone or browser holds the
	// voice connection.
	VoiceClientConnected bool `json:"voice_client_connected"`
}

type statusLastError struct {
	Time    time.Time `json:"time"`
	What    string    `json:"what"`
	Message string    `json:"message,omitempty"`
}

func printStatusUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog status [--json] [--config PATH]")
	fprintln(w, "")
	fprintln(w, "Says whether the daemon runs, and its pid, version, URL, voice and orchestrator")
	fprintln(w, "models, whether herdr answers, the phones it knows and its last error.")
	fprintln(w, "Exit status: 0 running, 3 not running, 1 error.")
}

// runStatus implements `sheepdog status`.
func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printStatusUsage(stderr) }
	configPath, envFile := lifecycleFlags(fs)
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	l, code, ok := loadLifecycle(ctx, "status", *configPath, *envFile, stdout, stderr)
	if !ok {
		return code
	}
	r := l.status(ctx)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
	} else {
		printStatus(stdout, r, time.Now())
	}
	if !r.Running {
		return 3
	}
	return 0
}

// status gathers the report: from the pid file and the running daemon's
// API when it runs, from config otherwise.
func (l *lifecycle) status(ctx context.Context) statusReport {
	cfg := l.cfg
	r := statusReport{
		CLIVersion: version, Port: cfg.Web.Port, URL: l.url(),
		LogFile: daemon.LogFile(cfg.DataDir), DataDir: cfg.DataDir, Service: "not installed",
		Voice:        statusVoice{Provider: cfg.Voice.Provider, Source: "config"},
		Orchestrator: statusOrchestrator{Enabled: cfg.Brain.Enabled, Provider: cfg.Brain.Provider, Model: cfg.Brain.Model, Source: "config"},
	}
	if l.mgr.Run != nil {
		if st, err := l.mgr.Status(ctx); err == nil {
			r.Service = st
		}
	}
	h := onboard.CheckHerdr(cfg.Herdr.Socket)
	r.Herdr = statusHerdr{Reachable: h.Status == onboard.OK, Socket: cfg.Herdr.Socket}
	if !r.Herdr.Reachable {
		r.Herdr.Detail = h.Detail
	}
	info, pidKnown, running := l.running(ctx)
	r.Running = running
	if running {
		r.URL, r.Version = info.URL, info.Version
		if pidKnown {
			r.PID = info.PID
			if !info.StartedAt.IsZero() {
				t := info.StartedAt
				r.StartedAt = &t
			}
			r.StartedBy = "terminal"
			if info.Background {
				r.StartedBy = "background"
				if strings.HasPrefix(r.Service, "installed") {
					r.StartedBy = "login service"
				}
			}
		}
		if hello, err := daemon.GetHello(ctx, r.URL); err == nil {
			r.Version = hello.Version
			r.Voice = statusVoice{Provider: hello.VoiceProvider, Model: hello.VoiceModel, Source: "daemon"}
		}
		var st struct {
			Brain *struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			} `json:"brain"`
			Voice struct {
				ClientConnected bool `json:"client_connected"`
			} `json:"voice"`
		}
		if err := getJSON(ctx, r.URL, "/api/state", cfg.Web.Token, &st); err == nil {
			r.Orchestrator = statusOrchestrator{Source: "daemon"}
			if st.Brain != nil {
				r.Orchestrator.Enabled, r.Orchestrator.Provider, r.Orchestrator.Model = true, st.Brain.Provider, st.Brain.Model
			}
			var push struct {
				Devices int `json:"devices"`
			}
			_ = getJSON(ctx, r.URL, "/api/push/status", cfg.Web.Token, &push)
			r.Phones = &statusPhones{PushDevices: push.Devices, VoiceClientConnected: st.Voice.ClientConnected}
		}
	}
	r.LastError = lastError(cfg.DataDir, redact.ForConfig(cfg))
	return r
}

func ago(now, t time.Time) string {
	d := now.Sub(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm ago", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

func printStatus(w io.Writer, r statusReport, now time.Time) {
	row := func(k, v string) { fprintf(w, "  %-13s %s\n", k, v) }
	if r.Running {
		fprintln(w, "sheepdog is running")
		switch {
		case r.PID == 0:
			row("pid", "unknown (no pid file: an older sheepdog, or another --data-dir)")
		case r.StartedAt != nil:
			row("pid", fmt.Sprintf("%d (started %s, %s)", r.PID, ago(now, *r.StartedAt), r.StartedBy))
		default:
			row("pid", fmt.Sprint(r.PID))
		}
		row("version", r.Version)
		row("url", r.URL+"/voice")
	} else {
		fprintln(w, "sheepdog is not running (start it with: sheepdog start)")
		row("url", r.URL+"/voice (when running)")
	}
	row("login service", r.Service)
	voice := r.Voice.Provider
	if r.Voice.Model != "" {
		voice += " (" + r.Voice.Model + ")"
	}
	if r.Voice.Source == "config" {
		voice += ", from config"
	}
	row("voice", voice)
	orch := "off"
	if r.Orchestrator.Enabled {
		orch = r.Orchestrator.Provider + " / " + r.Orchestrator.Model
	}
	if r.Orchestrator.Source == "config" {
		orch += ", from config"
	}
	row("orchestrator", orch)
	if r.Herdr.Reachable {
		row("herdr", "reachable ("+r.Herdr.Socket+")")
	} else {
		row("herdr", "NOT reachable: "+r.Herdr.Detail)
	}
	if r.Phones != nil {
		conn := "no voice client connected"
		if r.Phones.VoiceClientConnected {
			conn = "a voice client is connected"
		}
		row("phones", fmt.Sprintf("%d registered for push; %s", r.Phones.PushDevices, conn))
	}
	if r.LastError != nil {
		msg := r.LastError.What
		if r.LastError.Message != "" {
			msg += ": " + r.LastError.Message
		}
		row("last error", ago(now, r.LastError.Time)+": "+msg)
	} else {
		row("last error", "none recorded")
	}
	row("log", r.LogFile)
	if !r.Running && fileSize(daemon.StdioFile(r.DataDir)) > 0 {
		row("crash output", daemon.StdioFile(r.DataDir))
	}
}

// maxErrorMsg bounds the last error's message.
const maxErrorMsg = 200

// lastError is the newest error event in the event log (the newest day
// file that has one), redacted. The event log is written by every
// sheepdog version, so this works for an older daemon too.
func lastError(dataDir string, red *redact.Redactor) *statusLastError {
	files, err := eventlog.DayFiles(dataDir)
	if err != nil {
		return nil
	}
	for i := len(files) - 1; i >= 0 && i >= len(files)-3; i-- {
		if e := lastErrorIn(files[i], red); e != nil {
			return e
		}
	}
	return nil
}

func lastErrorIn(path string, red *redact.Redactor) *statusLastError {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var last *statusLastError
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte("error")) && !bytes.Contains(line, []byte("failed")) {
			continue
		}
		var ev struct {
			TS      time.Time       `json:"ts"`
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &ev) != nil || !logging.IsErrorEvent(ev.Type) {
			continue
		}
		e := &statusLastError{Time: ev.TS, What: ev.Type}
		var p map[string]any
		if json.Unmarshal(ev.Payload, &p) == nil {
			for _, k := range []string{"error", "err", "reason", "message"} {
				if s, ok := p[k].(string); ok && s != "" {
					e.Message = s
					break
				}
			}
		}
		e.Message = red.String(e.Message)
		if len(e.Message) > maxErrorMsg {
			e.Message = e.Message[:maxErrorMsg] + "…"
		}
		last = e
	}
	return last
}
