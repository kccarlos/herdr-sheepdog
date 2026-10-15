package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/app"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/daemon"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/redact"
	"github.com/kccarlos/herdr-sheepdog/internal/service"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
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
	fprintln(w, "                    read (default ./.env if it exists, else ~/.config/sheepdog/.env;")
	fprintln(w, "                    no error if neither does;")
	fprintln(w, "                    an explicitly given path that is missing is an error). Real")
	fprintln(w, "                    env vars always win over the file; values are never logged.")
	fprintln(w, "  --port N          web port (default from config: web.port / SHEEPDOG_WEB_PORT, 7777)")
	fprintln(w, "  --voice MODE      on | gemini | openai | off (default on: the provider in the state")
	fprintln(w, "                    file or voice.provider; gemini/openai force it for this run;")
	fprintln(w, "                    off logs pushes as skipped)")
	fprintln(w, "  --herdr MODE      socket | mock (default socket; mock needs no running herdr)")
	fprintln(w, "  --cli MODE        on | off (default on; off takes the orchestrator's run_cli tool")
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
	fprintln(w, "  --background      run as a background daemon (what sheepdog start and the login")
	fprintln(w, "                    service use): write only to the runtime log, not the terminal")
	fprintln(w, "  --heard-echo      with --voice=off: report every spoken push and text-router")
	fprintln(w, "                    answer back as heard, as a voice session would (harnesses")
	fprintln(w, "                    that stand in for the user, e.g. sheepdog bench run)")
}

// serveOpts are the parsed flags.
type serveOpts struct {
	configPath, envFile, voice, herdr, cli, brain, dataDir string
	port                                                   int
	verbose, dev, heardEcho, background                    bool
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
	fs.StringVar(&o.brain, "brain", app.BrainOn, "orchestrator mode")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory")
	fs.BoolVar(&o.verbose, "verbose", false, "echo events to stderr")
	fs.BoolVar(&o.dev, "dev", false, "developer routes")
	fs.BoolVar(&o.heardEcho, "heard-echo", false, "with --voice=off, report pushes and answers back as heard")
	fs.BoolVar(&o.background, "background", false, "background daemon: log to the runtime log only")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	return o, 0, true
}

// configEnvPath is the secret file `sheepdog init` writes
// (config.DefaultEnvPath; tests point it elsewhere).
var configEnvPath = config.DefaultEnvPath

// implicitEnvFile is the env file loaded without --env-file: ./.env when
// present, else the config directory's .env (sheepdog init), else
// ./.env (absent: nothing is loaded).
func implicitEnvFile() string {
	if _, err := os.Stat(defaultEnvFile); err == nil {
		return defaultEnvFile
	}
	if p := configEnvPath(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return defaultEnvFile
}

// loadServeEnvFile implements --env-file: an explicitly given path must
// exist; the implicit default (./.env, else ~/.config/sheepdog/.env) is
// silently skipped when absent,
// since most runs (production, launchd) have no .env at all. Values are
// loaded before config.Load so SHEEPDOG_*/GEMINI_API_KEY* from the file
// take effect, and are never logged (see config.LoadEnvFile).
func loadServeEnvFile(path string, stderr io.Writer) (code int, ok bool) {
	explicit := path != ""
	if !explicit {
		path = implicitEnvFile()
	}
	if _, err := os.Stat(path); err != nil {
		// The default secret file is passed explicitly by `sheepdog
		// start` and the login service; before setup has written it, it
		// is simply absent (a fresh install), not an error.
		if explicit && path != configEnvPath() {
			fprintf(stderr, "sheepdog: --env-file %s: %v\n", path, err)
			return 1, false
		}
		return 0, true // no default .env: not an error
	}
	if err := config.LoadEnvFile(path); err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
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
	baseEnv := os.Environ() // before the env file: a reload starts from it
	startedAt := time.Now().UTC()
	if code, ok := loadServeEnvFile(o.envFile, stderr); !ok {
		return code
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		fprintf(stderr, "sheepdog: %v\n", err)
		return 1
	}
	applyDataDir(&cfg, o.dataDir)
	opts := app.Options{Voice: o.voice, Herdr: o.herdr, CLI: o.cli, Brain: o.brain, Port: o.port, Dev: o.dev, HeardEcho: o.heardEcho, Version: version}
	if err := app.Check(cfg, opts); err != nil {
		fprintf(stderr, "sheepdog serve: %v\n", err)
		return 2
	}

	// One daemon per data dir (the lock), and a clear word when the port
	// is held by a daemon that has no lock (an older sheepdog, another
	// --data-dir) or by another program.
	lock, err := daemon.Acquire(cfg.DataDir)
	if err != nil {
		fprintf(stderr, "%v\n", err)
		return 1
	}
	defer func() { lock.Release() }() // lock is re-taken after a failed hand-over
	port := cfg.Web.Port
	if o.port > 0 {
		port = o.port
	}
	if msg := portBusyMessage(ctx, cfg, port); msg != "" {
		fprintln(stderr, msg)
		return 1
	}

	// The runtime log: every line the daemon prints, plus the events
	// log.level selects, secrets redacted, rotated by size.
	rot, err := logging.OpenRotating(daemon.LogFile(cfg.DataDir), int64(cfg.Log.MaxSizeMB)<<20, cfg.Log.MaxFiles)
	if err != nil {
		fprintf(stderr, "sheepdog serve: runtime log: %v\n", err)
		return 1
	}
	defer func() { _ = rot.Close() }()
	logw := redact.ForConfig(cfg).Writer(rot)
	defer logw.Flush()
	if o.background {
		stdout, stderr = logw, logw
	} else {
		stdout, stderr = io.MultiWriter(stdout, logw), io.MultiWriter(stderr, logw)
	}
	opts.Stderr = stderr
	mirror := logging.Logger(logging.NewEventMirror(logw, cfg.Log.Level))
	if o.verbose {
		mirror = logging.Tee{mirror, logging.NewJSONLLogger(stderr)}
	}
	opts.Echo = mirror

	// The daemon runs in a loop: the setup page saves keys or changes
	// the bind and asks for a restart (app.ErrRestart), and the env
	// file and config are read again and the daemon rebuilt in this
	// process (same pid, same lock), so nothing outside notices.
	var cur atomic.Pointer[app.App]
	sdemo := newSetupDemo(stdout)
	defer sdemo.Stop("the daemon stopped")

	// SIGHUP re-reads the prompt override directory (docs/dev/roles.md
	// "Prompt files") so the user's prompt edits take effect without a
	// restart: the orchestrator picks it up on its next turn, the voice session
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
				if a := cur.Load(); a != nil {
					files := a.ReloadPrompts()
					a.Log().Log("daemon", "prompts.reloaded", "", map[string]any{"files": files, "via": "sighup"})
				}
			}
		}
	}()

	for run := 0; ; run++ {
		opts.Setup = setupOptions(cfg, o, sdemo, func() error {
			if a := cur.Load(); a != nil {
				a.RequestRestart(app.ReasonServiceHandoff)
			}
			return nil
		})
		a, err := app.Build(cfg, opts)
		if err != nil {
			fprintf(stderr, "sheepdog serve: %v\n", err)
			return 2
		}
		if err := listenRetry(a, run > 0); err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				if msg := portBusyMessage(ctx, cfg, port); msg != "" {
					fprintln(stderr, msg)
					return 1
				}
			}
			fprintf(stderr, "sheepdog serve: %v\n", err)
			return 1
		}
		cur.Store(a)
		if serveBuilt != nil {
			serveBuilt(a)
		}
		_ = lock.Write(daemon.Info{PID: os.Getpid(), URL: localURLOf(a, cfg), Version: version, StartedAt: startedAt, Background: o.background})
		if run == 0 {
			fprintf(stderr, "sheepdog serve %s: %s\n", version, cfg)
			fprintf(stdout, "sheepdog %s listening on %s (voice=%s herdr=%s cli=%s orchestrator=%s)\n", version, a.URL(), a.VoiceProvider(), o.herdr, o.cli, a.BrainMode())
		} else {
			fprintf(stdout, "sheepdog reloaded its settings: listening on %s (voice=%s orchestrator=%s)\n", a.URL(), a.VoiceProvider(), a.BrainMode())
		}
		if !cfg.Web.IsLoopback() {
			// web.bind is not loopback: print every non-loopback IPv4 of the
			// host, so the operator can read the LAN or Tailscale URL off the
			// log instead of guessing which interface it is (CONFIG.md
			// "web.bind").
			for _, u := range a.URLs() {
				fprintf(stdout, "  lan url     %s\n", u)
			}
		}
		if a.SetupNeeded() {
			fprintf(stdout, "  setup       %s/setup  (no voice key yet: the voice is off until you add one)\n", localURLOf(a, cfg))
		}
		if run == 0 {
			fprintf(stdout, "  voice page  %s/voice\n", a.URL())
			fprintf(stdout, "  debug page  %s/debug\n", a.URL())
			fprintf(stdout, "  events      %s/events\n", a.URL())
			fprintf(stdout, "  log         %s\n", rot.Path())
			// Event retention (events.retention_days): at startup, then daily.
			go pruneEvents(ctx, cfg, func() logging.Logger { return cur.Load().Log() }, stdout)
		}

		err = a.Run(ctx)
		if errors.Is(err, app.ErrRestart) && ctx.Err() == nil && a.RestartReason() == app.ReasonServiceHandoff {
			// "Start at login" on the setup page: this daemon steps
			// aside (lock and port free) and the login service starts its
			// own, which serves the same page.
			sdemo.Stop("the daemon moved to the login service")
			lock.Release()
			herr := handOverToService(ctx, cfg, o)
			if herr == nil {
				fprintln(stdout, "sheepdog handed over to the login service; this process exits")
				return 0
			}
			fprintf(stderr, "sheepdog serve: the login service did not install (%v); carrying on here\n", herr)
			if lock, err = daemon.Acquire(cfg.DataDir); err != nil {
				fprintf(stderr, "%v\n", err)
				return 1
			}
			continue
		}
		if errors.Is(err, app.ErrRestart) && ctx.Err() == nil {
			next, ok := reloadServeConfig(baseEnv, o, stderr)
			if ok {
				cfg = next
				logw.SetRedactor(redact.ForConfig(cfg))
				if err := app.Check(cfg, opts); err != nil {
					fprintf(stderr, "sheepdog serve: the new settings do not start (%v); keeping the old ones\n", err)
					cfg = a.Config()
				}
			}
			port = cfg.Web.Port
			if o.port > 0 {
				port = o.port
			}
			continue
		}
		if err != nil && !errors.Is(err, app.ErrRestart) {
			fprintf(stderr, "sheepdog serve: %v\n", err)
			if errors.Is(err, gemini.ErrAllKeysExhausted) {
				return 3
			}
			return 1
		}
		return 0
	}
}

// setupOptions are the setup page's files and hooks for this serve: the
// env file serve loads (an explicit --env-file, else the one it finds,
// else the default secret file), the config file and the login service.
func setupOptions(cfg config.Config, o serveOpts, sdemo *setupDemo, handoff func() error) *app.SetupOptions {
	envPath := o.envFile
	if envPath == "" {
		envPath = implicitEnvFile()
		if _, err := os.Stat(envPath); err != nil {
			envPath = configEnvPath()
		}
	}
	if envPath == "" {
		envPath = config.DefaultEnvPath()
	}
	if abs, err := filepath.Abs(envPath); err == nil {
		envPath = abs
	}
	cfgPath := o.configPath
	if cfgPath == "" {
		cfgPath = config.DefaultPath()
	}
	so := &app.SetupOptions{ConfigPath: cfgPath, EnvPath: envPath, Endpoints: onboardEndpoints(), Demo: sdemo}
	if spec, err := serviceSpecFor(cfg, o.configPath, o.envFile, ""); err == nil {
		m := service.Manager{Spec: spec, Run: serviceRunner}
		so.Service = func(context.Context) web.ServiceState {
			return web.ServiceState{Supported: true, Installed: m.Installed()}
		}
		so.InstallService = handoff
	}
	return so
}

// handOverToService installs the login service (it starts its own
// daemon, which takes the port this one has just freed).
var handOverToService = func(ctx context.Context, cfg config.Config, o serveOpts) error {
	spec, err := serviceSpecFor(cfg, o.configPath, o.envFile, "")
	if err != nil {
		return err
	}
	return service.Manager{Spec: spec, Run: serviceRunner}.Install(ctx)
}

// serveBuilt, when set (tests), gets each daemon serve builds, once it
// listens.
var serveBuilt func(*app.App)

// localURLOf is the URL this computer reaches the daemon on (the pid
// file's): 127.0.0.1 for a bind on every address, not http://[::]:port.
func localURLOf(a *app.App, cfg config.Config) string {
	_, p, err := net.SplitHostPort(strings.TrimPrefix(a.URL(), "http://"))
	port, _ := strconv.Atoi(p)
	if err != nil || port == 0 {
		return a.URL()
	}
	return daemon.LocalURL(cfg.Web.Bind, port)
}

// listenRetry binds the web port. After a reload the old listener has
// just closed; a few short retries cover the moment it takes.
func listenRetry(a *app.App, reload bool) error {
	err := a.Listen()
	for i := 0; reload && err != nil && errors.Is(err, syscall.EADDRINUSE) && i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		err = a.Listen()
	}
	return err
}

// reloadServeConfig reads the env file and the config again for a
// reload: the process environment goes back to what it was before the
// env file was first loaded (config.LoadEnvFile never overrides a set
// variable, so a key changed in the file would otherwise be ignored),
// then the file is loaded over it.
func reloadServeConfig(baseEnv []string, o serveOpts, stderr io.Writer) (config.Config, bool) {
	restoreEnv(baseEnv)
	if _, ok := loadServeEnvFile(o.envFile, stderr); !ok {
		return config.Config{}, false
	}
	cfg, err := config.Load(o.configPath)
	if err != nil {
		fprintf(stderr, "sheepdog serve: reload: %v; keeping the old settings\n", err)
		return config.Config{}, false
	}
	applyDataDir(&cfg, o.dataDir)
	return cfg, true
}

// restoreEnv makes the process environment exactly env.
func restoreEnv(env []string) {
	os.Clearenv()
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			_ = os.Setenv(k, v)
		}
	}
}

// applyDataDir applies --data-dir: it moves the state file along unless
// that was set explicitly.
func applyDataDir(cfg *config.Config, dir string) {
	if dir == "" {
		return
	}
	if cfg.State.Path == filepath.Join(cfg.DataDir, "state.json") {
		cfg.State.Path = filepath.Join(dir, "state.json")
	}
	cfg.DataDir = dir
}

// portBusyMessage says who holds the daemon's port, or "" when it is
// free (or port is 0: a free one is picked).
func portBusyMessage(ctx context.Context, cfg config.Config, port int) string {
	if port <= 0 {
		return ""
	}
	switch st, h := daemon.ProbePort(ctx, cfg.Web.Bind, port); st {
	case daemon.PortSheepdog:
		return fmt.Sprintf("sheepdog is already running (%s, version %s; it has no pid file in %s: started by an older sheepdog or with another --data-dir)", daemon.LocalURL(cfg.Web.Bind, port), h.Version, cfg.DataDir)
	case daemon.PortOther:
		return fmt.Sprintf("port %d is in use by another program, so sheepdog cannot listen on %s.\nFind it with: lsof -nP -iTCP:%d -sTCP:LISTEN\nOr give sheepdog another port: set web.port in %s.", port, daemon.LocalURL(cfg.Web.Bind, port), port, config.DefaultPath())
	}
	return ""
}

// retentionEvery is how often the event retention runs after startup.
var retentionEvery = 24 * time.Hour

// pruneEvents deletes event day files older than events.retention_days
// now and every retentionEvery until ctx ends; each run that deletes
// something logs events.pruned (to the current daemon's log) and one
// line.
func pruneEvents(ctx context.Context, cfg config.Config, logOf func() logging.Logger, out io.Writer) {
	if cfg.Events.RetentionDays <= 0 {
		return
	}
	t := time.NewTicker(retentionEvery)
	defer t.Stop()
	for {
		removed, err := eventlog.Prune(cfg.DataDir, cfg.Events.RetentionDays, time.Now())
		log := logOf()
		switch {
		case err != nil:
			log.Log("daemon", "daemon.retention_error", "", map[string]any{"error": err.Error()})
		case len(removed) > 0:
			log.Log("daemon", "events.pruned", "", map[string]any{"files": len(removed), "retention_days": cfg.Events.RetentionDays})
			fprintf(out, "event retention: deleted %d day file(s) older than %d days\n", len(removed), cfg.Events.RetentionDays)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
