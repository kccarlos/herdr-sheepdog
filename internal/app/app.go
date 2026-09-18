package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/cli"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
)

// Modes for Options.Voice, Options.Herdr and Options.CLI.
const (
	VoiceGemini = "gemini"
	VoiceOff    = "off"
	HerdrSocket = "socket"
	HerdrMock   = "mock"
	CLIOn       = "on"
	CLIOff      = "off"
)

// Options select the daemon's mode; zero values take the defaults noted.
type Options struct {
	// Voice is VoiceGemini (default) or VoiceOff.
	Voice string
	// Herdr is HerdrSocket (default) or HerdrMock.
	Herdr string
	// CLI is CLIOn (default) or CLIOff (`--cli=off` disables the deep-ask
	// path; ask() then answers from the screen alone).
	CLI string
	// Port overrides config web.port when > 0; 0 with an empty config
	// port picks a free port (tests).
	Port int
	// Dev turns on the developer-only web routes (config web.dev; the
	// `serve --dev` flag). See config.Web.
	Dev bool
	// Version is reported in daemon.start and /api/state.
	Version string
	// Stderr receives one-line warnings; nil means os.Stderr.
	Stderr io.Writer
	// Drain bounds the HTTP shutdown (default 5 s).
	Drain time.Duration
	// Echo, when set, also receives every event (for --verbose).
	Echo logging.Logger
	// WatcherRetry is the wait before re-running a Watcher whose initial
	// snapshot failed (default 5 s).
	WatcherRetry time.Duration
}

func (o Options) withDefaults() Options {
	if o.Voice == "" {
		o.Voice = VoiceGemini
	}
	if o.Herdr == "" {
		o.Herdr = HerdrSocket
	}
	if o.CLI == "" {
		o.CLI = CLIOn
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Drain <= 0 {
		o.Drain = 5 * time.Second
	}
	if o.WatcherRetry <= 0 {
		o.WatcherRetry = 5 * time.Second
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	return o
}

// App is the assembled daemon.
type App struct {
	cfg  config.Config
	opts Options

	elog  *eventlog.Log
	log   logging.Logger
	store *state.Store
	herdr *inject.Herdr
	hc    *herdr.Client // nil in mock mode
	wref  *watcherRef   // nil in mock mode
	sup   *supervisor.Supervisor
	voice voice.Session // nil when off
	web   *web.Server
	port  int

	queryLazy *lazyQuerier // nil when --cli=off; filled in by Run after daemon.start
}

// Build validates the options and wires every component. Nothing is
// started; the state table is loaded and the event log opened.
func Build(cfg config.Config, opts Options) (*App, error) {
	opts = opts.withDefaults()
	if opts.Voice != VoiceGemini && opts.Voice != VoiceOff {
		return nil, fmt.Errorf("app: --voice must be %s or %s", VoiceGemini, VoiceOff)
	}
	if opts.Herdr != HerdrSocket && opts.Herdr != HerdrMock {
		return nil, fmt.Errorf("app: --herdr must be %s or %s", HerdrSocket, HerdrMock)
	}
	if opts.CLI != CLIOn && opts.CLI != CLIOff {
		return nil, fmt.Errorf("app: --cli must be %s or %s", CLIOn, CLIOff)
	}
	if opts.Voice == VoiceGemini && len(cfg.Gemini.APIKeys) == 0 {
		return nil, errors.New("app: --voice=gemini needs GEMINI_API_KEY (or gemini.api_keys); use --voice=off to run without it")
	}
	port := cfg.Web.Port
	if opts.Port > 0 {
		port = opts.Port
	}

	elog, err := eventlog.Open(cfg.DataDir, eventlog.Options{Stderr: opts.Stderr})
	if err != nil {
		return nil, err
	}
	var log logging.Logger = elog
	if opts.Echo != nil {
		log = teeLogger{elog, opts.Echo}
	}
	a := &App{cfg: cfg, opts: opts, elog: elog, log: log, port: port}
	if err := a.wire(); err != nil {
		_ = elog.Close()
		return nil, err
	}
	return a, nil
}

func (a *App) wire() error {
	cfg, opts := a.cfg, a.opts
	store, err := state.Open(cfg.State.Path, state.Options{Aliases: cfg.Aliases, Warn: opts.Stderr, FullFsync: cfg.State.FullFsync})
	if err != nil {
		return err
	}
	a.store = store

	var inner supervisor.Herdr
	if opts.Herdr == HerdrSocket {
		a.hc = herdr.NewClient(cfg.Herdr.Socket)
		a.hc.Log = a.log
		inner = a.hc
		a.wref = &watcherRef{}
	}
	a.herdr = inject.NewHerdr(inner, a.log)

	keys := cfg.LLMKeys()
	if len(keys) == 0 {
		if config.IsGeminiBaseURL(cfg.LLM.BaseURL) {
			return errors.New("app: the cheap LLM needs a key (GEMINI_API_KEY or SHEEPDOG_LLM_API_KEY), or point SHEEPDOG_LLM_BASE_URL at a local endpoint")
		}
		keys = []string{"local"} // local endpoints (Ollama) ignore the key
	}
	llmClient, err := llm.New(llm.Config{BaseURL: cfg.LLM.BaseURL, Model: cfg.LLM.Model, APIKeys: keys, Log: a.log})
	if err != nil {
		return err
	}

	// The supervisor is built before the voice session (its Handler is the
	// session's tool handler); the pusher is filled in below. The querier
	// (WP4) is detected and built in Run, after daemon.start is logged, so
	// cli.detected never races ahead of it; a nil *lazyQuerier still
	// satisfies supervisor.Querier so ask() can route to it once ready.
	pusher := &lazyPusher{}
	lazyQ := &lazyQuerier{}
	var querier supervisor.Querier // nil (not lazyQ) when --cli=off, so ask() skips the deep path entirely
	if opts.CLI == CLIOn {
		querier = lazyQ
		a.queryLazy = lazyQ
	}
	sup, err := supervisor.New(store, a.herdr, llmClient, querier, pusher, a.log, supervisor.Config{
		PendingTTL: time.Duration(cfg.PendingTTLS) * time.Second,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return err
	}
	a.sup = sup

	if opts.Voice == VoiceGemini {
		sess, err := gemini.New(gemini.Config{
			APIKeys: cfg.Gemini.APIKeys,
			Model:   cfg.Gemini.Model,
			Voice:   cfg.Gemini.Voice,
			Aliases: a.aliases(),
			Handler: sup.Handler(),
		}, a.log)
		if err != nil {
			return err
		}
		a.voice = sess
		pusher.set(sess)
		sup.SetVoiceConnected(false) // the session's status events maintain it
	} else {
		// No voice model: pushes are logged as skipped so the blocked chain
		// stays visible, and the session counts as connected so the
		// supervisor produces them at all.
		pusher.set(inject.SkippedPusher{Log: a.log})
		sup.SetVoiceConnected(true)
	}

	injector := inject.NewInjector(store, a.herdr, sup, a.log, nil)
	deps := web.Deps{
		Log:        a.elog,
		Logger:     a.log,
		Store:      store,
		Supervisor: sup,
		Injector:   injector,
		Router:     &inject.Router{},
		Version:    opts.Version,
		HerdrMode:  opts.Herdr,
		VoiceMode:  opts.Voice,
	}
	if opts.Dev || a.cfg.Web.Dev {
		deps.DevTestdataDir = a.cfg.Web.DevTestdataDir
		if deps.DevTestdataDir == "" {
			deps.DevTestdataDir = config.DefaultDevTestdataDir
		}
	}
	if a.voice != nil {
		deps.Voice = a.voice
	}
	if a.wref != nil {
		deps.Watcher = a.wref
	}
	srv, err := web.New(deps)
	if err != nil {
		return err
	}
	a.web = srv
	return nil
}

// buildQuerier detects codex/claude on PATH (logging cli.detected) and, if
// either is found, builds the Runner that backs ask()'s deep path. Called
// from Run, once, right after daemon.start. Neither found returns
// (nil, nil): the deep path is simply unavailable, per plan §3.5 ("if
// absent, ask() still works with the cheap LLM alone").
func (a *App) buildQuerier() (*cli.Runner, error) {
	cfg := cli.Config{
		Prefer:         a.cfg.CLI.Prefer,
		CodexBin:       a.cfg.CLI.CodexBin,
		ClaudeBin:      a.cfg.CLI.ClaudeBin,
		CodexCmd:       a.cfg.CLI.CodexCmd,
		ClaudeCmd:      a.cfg.CLI.ClaudeCmd,
		ClaudeModel:    a.cfg.CLI.ClaudeModel,
		CodexReasoning: a.cfg.CLI.CodexReasoning,
		Timeout:        time.Duration(a.cfg.CLI.TimeoutS) * time.Second,
		MaxOutputBytes: a.cfg.CLI.MaxOutputKB << 10,
		Log:            a.log,
	}
	det := cli.Detect(cfg, a.log)
	if !det.Codex && !det.Claude {
		return nil, nil
	}
	runner, err := cli.NewRunner(cfg, det)
	if err != nil {
		return nil, fmt.Errorf("app: cli: %w", err)
	}
	return runner, nil
}

// aliases returns the spoken names known at start (config + state table)
// for the voice model's vocabulary.
func (a *App) aliases() []string {
	seen := map[string]bool{}
	for _, v := range a.cfg.Aliases {
		seen[v] = true
	}
	for _, row := range a.store.List() {
		if row.Alias != "" {
			seen[row.Alias] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Listen binds 127.0.0.1:<port>. URL is valid afterwards.
func (a *App) Listen() error {
	return a.web.Listen(net.JoinHostPort("127.0.0.1", strconv.Itoa(a.port)))
}

// URL returns the base URL of the web server ("" before Listen).
func (a *App) URL() string {
	addr := a.web.Addr()
	if addr == "" {
		return ""
	}
	return "http://" + addr
}

// Log returns the daemon's logger (the event log, plus the echo).
func (a *App) Log() logging.Logger { return a.log }

// Run starts every goroutine and blocks until ctx ends, then shuts down
// in order. It calls Listen when it has not been called yet. The event
// log is closed last.
func (a *App) Run(ctx context.Context) (err error) {
	if a.web.Addr() == "" {
		if err := a.Listen(); err != nil {
			return err
		}
	}
	a.log.Log("daemon", "daemon.start", "", map[string]any{
		"version": a.opts.Version, "voice": a.opts.Voice, "herdr": a.opts.Herdr, "cli": a.opts.CLI, "url": a.URL(),
		"data_dir": a.cfg.DataDir, "llm_model": a.cfg.LLM.Model, "llm_base_url": a.cfg.LLM.BaseURL,
	})
	if a.queryLazy != nil {
		if runner, err := a.buildQuerier(); err != nil {
			a.log.Log("daemon", "daemon.cli_error", "", map[string]any{"error": err.Error()})
		} else if runner != nil {
			a.queryLazy.set(runner)
		}
	}
	start := time.Now()
	defer func() {
		a.log.Log("daemon", "daemon.stop", "", map[string]any{"version": a.opts.Version, "uptime_s": int(time.Since(start).Seconds()), "error": errString(err)})
		// Flush any debounced hardware-durable fsync (state.full_fsync)
		// before the event log closes; harmless no-op otherwise.
		_ = a.store.Close()
		if cerr := a.elog.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	inner, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup

	if a.wref != nil {
		wg.Add(1)
		go func() { defer wg.Done(); a.runWatcher(inner) }()
	}
	if a.voice != nil {
		if err := a.voice.Start(inner); err != nil {
			a.log.Log("daemon", "daemon.voice_error", "", map[string]any{"error": err.Error()})
			cancel()
			wg.Wait()
			return fmt.Errorf("app: voice: %w", err)
		}
		wg.Add(2)
		go func() {
			defer wg.Done()
			for ev := range a.voice.Events() {
				a.sup.OnVoiceEvent(ev)
				a.web.OnVoiceEvent(ev)
			}
		}()
		go func() { defer wg.Done(); a.web.Run(inner) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); a.sup.Run(inner) }()

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.web.Serve(inner, a.opts.Drain) }()

	select {
	case <-ctx.Done():
	case err = <-serveErr:
		cancel()
		if a.voice != nil {
			_ = a.voice.Close()
		}
		wg.Wait()
		return err
	}

	// Shutdown: stop accepting, drain HTTP, then the session and the rest.
	cancel()
	if serr := <-serveErr; serr != nil && !errors.Is(serr, context.Canceled) {
		a.log.Log("daemon", "daemon.web_error", "", map[string]any{"error": serr.Error()})
	}
	if a.voice != nil {
		_ = a.voice.Close()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(a.opts.Drain):
		a.log.Log("daemon", "daemon.drain_timeout", "", nil)
	}
	return nil
}

// runWatcher keeps a Watcher running: transitions are handled serialised
// per pane; a Watcher whose initial snapshot fails (herdr not running) is
// retried after WatcherRetry.
func (a *App) runWatcher(ctx context.Context) {
	var panes paneLocks
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		w := herdr.NewWatcher(a.hc, herdr.WatcherConfig{
			Debounce:         time.Duration(a.cfg.DebounceMS) * time.Millisecond,
			SnapshotInterval: time.Duration(a.cfg.SnapshotIntervalS) * time.Second,
		}, a.log)
		a.wref.set(w)
		errc := make(chan error, 1)
		go func() { errc <- w.Run(ctx) }()
		for t := range w.Transitions() {
			wg.Add(1)
			go func(t herdr.Transition) {
				defer wg.Done()
				unlock := panes.lock(t.PaneID)
				defer unlock()
				a.sup.HandleTransition(ctx, t)
			}(t)
		}
		err := <-errc
		a.wref.set(nil)
		if ctx.Err() != nil {
			return
		}
		a.log.Log("daemon", "daemon.herdr_unavailable", "", map[string]any{"error": errString(err), "retry_s": int(a.opts.WatcherRetry.Seconds())})
		select {
		case <-ctx.Done():
			return
		case <-time.After(a.opts.WatcherRetry):
		}
	}
}

// watcherRef forwards Inject to the current Watcher (there is a new one
// after every retry); events arriving while none runs are dropped.
type watcherRef struct {
	mu sync.Mutex
	w  *herdr.Watcher
}

func (r *watcherRef) set(w *herdr.Watcher) {
	r.mu.Lock()
	r.w = w
	r.mu.Unlock()
}

// Inject implements web.EventInjector.
func (r *watcherRef) Inject(ev herdr.StatusEvent) {
	r.mu.Lock()
	w := r.w
	r.mu.Unlock()
	if w != nil {
		w.Inject(ev)
	}
}

// paneLocks serialises transition handling per pane while letting
// different panes proceed in parallel.
type paneLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (p *paneLocks) lock(pane string) func() {
	p.mu.Lock()
	if p.locks == nil {
		p.locks = map[string]*sync.Mutex{}
	}
	m, ok := p.locks[pane]
	if !ok {
		m = &sync.Mutex{}
		p.locks[pane] = m
	}
	p.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// lazyPusher lets the supervisor be built before the voice session.
type lazyPusher struct {
	mu sync.Mutex
	p  supervisor.Pusher
}

func (l *lazyPusher) set(p supervisor.Pusher) {
	l.mu.Lock()
	l.p = p
	l.mu.Unlock()
}

// Push implements supervisor.Pusher.
func (l *lazyPusher) Push(text string, u voice.Urgency) error {
	l.mu.Lock()
	p := l.p
	l.mu.Unlock()
	if p == nil {
		return errors.New("app: no pusher")
	}
	return p.Push(text, u)
}

// lazyQuerier lets the supervisor be built (with a non-nil Querier, so
// ask()'s deep path is enabled) before CLI detection has run: detection
// happens in Run, after daemon.start is logged, so cli.detected/cli.run
// never precede it in the event log.
type lazyQuerier struct {
	mu sync.Mutex
	q  supervisor.Querier
}

func (l *lazyQuerier) set(q supervisor.Querier) {
	l.mu.Lock()
	l.q = q
	l.mu.Unlock()
}

// Query implements supervisor.Querier.
func (l *lazyQuerier) Query(ctx context.Context, workspace, question string) (string, error) {
	l.mu.Lock()
	q := l.q
	l.mu.Unlock()
	if q == nil {
		return "", cli.ErrUnavailable
	}
	return q.Query(ctx, workspace, question)
}

// teeLogger writes every event to both loggers.
type teeLogger struct{ a, b logging.Logger }

func (t teeLogger) Log(source, typ, paneID string, payload any) {
	t.a.Log(source, typ, paneID, payload)
	t.b.Log(source, typ, paneID, payload)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
