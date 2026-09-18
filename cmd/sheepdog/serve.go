package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
)

func printServeUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog serve [flags]")
	fprintln(w, "")
	fprintln(w, "Runs the daemon: herdr watcher, supervisor, voice session and the web server")
	fprintln(w, "(/voice, /debug, /ws, /events) on 127.0.0.1. Exits cleanly on SIGINT/SIGTERM.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH     config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --port N          web port (default from config: web.port / SHEEPDOG_WEB_PORT, 7777)")
	fprintln(w, "  --voice MODE      gemini | off (default gemini; off logs pushes as skipped)")
	fprintln(w, "  --herdr MODE      socket | mock (default socket; mock needs no running herdr)")
	fprintln(w, "  --data-dir PATH   data directory for the event log and state (default from config)")
	fprintln(w, "  --verbose         also print every event to stderr as JSON lines")
}

// serveOpts are the parsed flags.
type serveOpts struct {
	configPath, voice, herdr, dataDir string
	port                              int
	verbose                           bool
}

func parseServeFlags(args []string, stderr io.Writer) (serveOpts, int, bool) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServeUsage(stderr) }
	var o serveOpts
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.IntVar(&o.port, "port", 0, "web port")
	fs.StringVar(&o.voice, "voice", app.VoiceGemini, "voice mode")
	fs.StringVar(&o.herdr, "herdr", app.HerdrSocket, "herdr mode")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory")
	fs.BoolVar(&o.verbose, "verbose", false, "echo events to stderr")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	return o, 0, true
}

// runServe implements `sheepdog serve`.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code, run := parseServeFlags(args, stderr)
	if !run {
		return code
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}
	if o.dataDir != "" {
		// --data-dir moves the state file along unless it was set explicitly.
		if cfg.State.Path == filepath.Join(cfg.DataDir, "state.json") {
			cfg.State.Path = filepath.Join(o.dataDir, "state.json")
		}
		cfg.DataDir = o.dataDir
	}
	opts := app.Options{Voice: o.voice, Herdr: o.herdr, Port: o.port, Version: version, Stderr: stderr}
	if o.verbose {
		opts.Echo = logging.NewJSONLLogger(stderr)
	}
	a, err := app.Build(cfg, opts)
	if err != nil {
		fprintf(stderr, "sheepdog serve: %v\n", err)
		return 2
	}
	if err := a.Listen(); err != nil {
		fprintf(stderr, "sheepdog serve: %v\n", err)
		return 1
	}
	fprintf(stderr, "sheepdog serve %s: %s\n", version, cfg)
	fprintf(stdout, "sheepdog %s listening on %s (voice=%s herdr=%s)\n", version, a.URL(), o.voice, o.herdr)
	fprintf(stdout, "  voice page  %s/voice\n", a.URL())
	fprintf(stdout, "  debug page  %s/debug\n", a.URL())
	fprintf(stdout, "  events      %s/events\n", a.URL())
	if err := a.Run(ctx); err != nil {
		fprintf(stderr, "sheepdog serve: %v\n", err)
		if errors.Is(err, gemini.ErrAllKeysExhausted) {
			return 3
		}
		return 1
	}
	return 0
}
