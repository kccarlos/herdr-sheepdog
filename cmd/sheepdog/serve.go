package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
)

// defaultEnvFile is what `serve --env-file` loads when the flag is not
// given: a .env in the working directory, if there is one. It is not an
// error for it to be absent (a production/launchd run has no .env).
const defaultEnvFile = ".env"

func printServeUsage(w io.Writer) {
	fprintln(w, "usage: sheepdog serve [flags]")
	fprintln(w, "")
	fprintln(w, "Runs the daemon: herdr watcher, supervisor, voice session and the web server")
	fprintln(w, "(/voice, /debug, /ws, /events) on 127.0.0.1. Exits cleanly on SIGINT/SIGTERM.")
	fprintln(w, "")
	fprintln(w, "flags:")
	fprintln(w, "  --config PATH     config file (default ~/.config/sheepdog/config.toml)")
	fprintln(w, "  --env-file PATH   load KEY=VALUE lines into the environment before config is")
	fprintln(w, "                    read (default ./.env if it exists; no error if it does not;")
	fprintln(w, "                    an explicitly given path that is missing is an error). Real")
	fprintln(w, "                    env vars always win over the file; values are never logged.")
	fprintln(w, "  --port N          web port (default from config: web.port / SHEEPDOG_WEB_PORT, 7777)")
	fprintln(w, "  --voice MODE      on | gemini | openai | off (default on: the provider in the state")
	fprintln(w, "                    file or voice.provider; gemini/openai force it for this run;")
	fprintln(w, "                    off logs pushes as skipped)")
	fprintln(w, "  --herdr MODE      socket | mock (default socket; mock needs no running herdr)")
	fprintln(w, "  --cli MODE        on | off (default on; off takes the brain's run_cli tool")
	fprintln(w, "                    away, so it answers from the screen alone)")
	fprintln(w, "  --brain MODE      on | off | fake (default on; the one model that decides and")
	fprintln(w, "                    phrases, needs a Gemini key and config brain.enabled; off")
	fprintln(w, "                    leaves the daemon its code sentences and sends nothing to a")
	fprintln(w, "                    worker; fake runs the loop over a scripted reflex model with")
	fprintln(w, "                    no network call, for the /debug Supervisor tab — a developer")
	fprintln(w, "                    mode like --herdr=mock)")
	fprintln(w, "  --data-dir PATH   data directory for the event log and state (default from config)")
	fprintln(w, "  --verbose         also print every event to stderr as JSON lines")
	fprintln(w, "  --dev             serve testdata/audio/*.wav at /testdata/ so the voice page")
	fprintln(w, "                    can use a fixture as its microphone (config: web.dev)")
}

// serveOpts are the parsed flags.
type serveOpts struct {
	configPath, envFile, voice, herdr, cli, brain, dataDir string
	port                                                   int
	verbose, dev                                           bool
}

func parseServeFlags(args []string, stderr io.Writer) (serveOpts, int, bool) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printServeUsage(stderr) }
	var o serveOpts
	fs.StringVar(&o.configPath, "config", "", "config file")
	fs.StringVar(&o.envFile, "env-file", "", "load KEY=VALUE lines into the environment before config (default ./.env if present)")
	fs.IntVar(&o.port, "port", 0, "web port")
	fs.StringVar(&o.voice, "voice", app.VoiceOn, "voice mode")
	fs.StringVar(&o.herdr, "herdr", app.HerdrSocket, "herdr mode")
	fs.StringVar(&o.cli, "cli", app.CLIOn, "cli query tool mode")
	fs.StringVar(&o.brain, "brain", app.BrainOn, "supervisor brain mode")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory")
	fs.BoolVar(&o.verbose, "verbose", false, "echo events to stderr")
	fs.BoolVar(&o.dev, "dev", false, "developer routes")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	return o, 0, true
}

// loadServeEnvFile implements --env-file: an explicitly given path must
// exist; the implicit default (./.env) is silently skipped when absent,
// since most runs (production, launchd) have no .env at all. Values are
// loaded before config.Load so SHEEPDOG_*/GEMINI_API_KEY* from the file
// take effect, and are never logged (see config.LoadEnvFile).
func loadServeEnvFile(path string, stderr io.Writer) (code int, ok bool) {
	explicit := path != ""
	if !explicit {
		path = defaultEnvFile
	}
	if _, err := os.Stat(path); err != nil {
		if explicit {
			fprintf(stderr, "sheepdog serve: --env-file %s: %v\n", path, err)
			return 1, false
		}
		return 0, true // no default .env: not an error
	}
	if err := config.LoadEnvFile(path); err != nil {
		fprintf(stderr, "sheepdog serve: %v\n", err)
		return 1, false
	}
	return 0, true
}

// runServe implements `sheepdog serve`.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code, run := parseServeFlags(args, stderr)
	if !run {
		return code
	}
	if code, ok := loadServeEnvFile(o.envFile, stderr); !ok {
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
	opts := app.Options{Voice: o.voice, Herdr: o.herdr, CLI: o.cli, Brain: o.brain, Port: o.port, Dev: o.dev, Version: version, Stderr: stderr}
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
	fprintf(stdout, "sheepdog %s listening on %s (voice=%s herdr=%s cli=%s brain=%s)\n", version, a.URL(), a.VoiceProvider(), o.herdr, o.cli, a.BrainMode())
	if !cfg.Web.IsLoopback() {
		// web.bind is not loopback: print every non-loopback IPv4 of the
		// host, so the operator can read the LAN or Tailscale URL off the
		// log instead of guessing which interface it is (CONFIG.md
		// "web.bind").
		for _, u := range a.URLs() {
			fprintf(stdout, "  lan url     %s\n", u)
		}
	}
	fprintf(stdout, "  voice page  %s/voice\n", a.URL())
	fprintf(stdout, "  debug page  %s/debug\n", a.URL())
	fprintf(stdout, "  events      %s/events\n", a.URL())

	// SIGHUP re-reads the prompt override directory (docs/roles.md
	// "Prompt files") so the CEO's prompt edits take effect without a
	// restart: the brain picks it up on its next turn, the voice session
	// on its next (re)connect. Separate from the INT/TERM context so a
	// HUP never triggers shutdown.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				files := a.ReloadPrompts()
				a.Log().Log("daemon", "prompts.reloaded", "", map[string]any{"files": files, "via": "sighup"})
			}
		}
	}()

	if err := a.Run(ctx); err != nil {
		fprintf(stderr, "sheepdog serve: %v\n", err)
		if errors.Is(err, gemini.ErrAllKeysExhausted) {
			return 3
		}
		return 1
	}
	return 0
}
