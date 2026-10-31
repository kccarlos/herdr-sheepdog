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

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/observe"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/update"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// Modes for Options.Voice, Options.Herdr and Options.CLI.
const (
	// VoiceOn (the default) runs the voice on the provider the state file
	// or voice.provider names; VoiceGemini and VoiceOpenAI force the
	// initial provider for this run; VoiceOff runs without a voice model.
	VoiceOn     = "on"
	VoiceGemini = "gemini"
	VoiceOpenAI = "openai"
	VoiceOff    = "off"
	HerdrSocket = "socket"
	HerdrMock   = "mock"
	CLIOn       = "on"
	CLIOff      = "off"
	BrainOn     = "on"
	BrainOff    = "off"
	// BrainFake runs the orchestrator loop over braintest.Reflex, a model that
	// answers from what it sees (a question → read the screen, an
	// instruction → prompt)
	// without any network call: a developer mode for the /debug
	// Supervisor tab, like --herdr=mock. Needs no Gemini key.
	BrainFake = "fake"
	// FakeBrainModel is the model name --brain=fake reports in brain.step
	// and /api/brain/context; FakeBrainProvider and FakeBrainProvider2
	// are its two providers (each a reflex model with a static catalog),
	// so provider switching can be exercised without a network.
	FakeBrainModel     = braintest.FakeModel
	FakeBrainProvider  = "fake"
	FakeBrainProvider2 = "fake2"
)

// Options select the daemon's mode; zero values take the defaults noted.
type Options struct {
	// Voice is VoiceOn (default), VoiceGemini, VoiceOpenAI or VoiceOff.
	Voice string
	// Herdr is HerdrSocket (default) or HerdrMock.
	Herdr string
	// CLI is CLIOn (default) or CLIOff (`--cli=off` takes the orchestrator's
	// run_cli tool away; it then answers from the screen alone).
	CLI string
	// Brain is BrainOn (default), BrainOff (`--brain=off`: the daemon
	// speaks its code sentences and sends nothing) or BrainFake (the
	// reflex fake model, dev only). On needs config brain.enabled and a
	// Gemini key; without a key the orchestrator is silently off.
	Brain string
	// Port overrides config web.port when > 0; 0 with an empty config
	// port picks a free port (tests).
	Port int
	// Dev turns on the developer-only web routes (config web.dev; the
	// `serve --dev` flag). See config.Web.
	Dev bool
	// Version is reported in daemon.start and /api/state.
	Version string
	// Update, when set, is the update notice /api/state reports.
	Update func() update.Status
	// Stderr receives one-line warnings; nil means os.Stderr.
	Stderr io.Writer
	// Drain bounds the HTTP shutdown (default 5 s).
	Drain time.Duration
	// Echo, when set, also receives every event (for --verbose).
	Echo logging.Logger
	// WatcherRetry is the wait before re-running a Watcher whose initial
	// snapshot failed (default 5 s).
	WatcherRetry time.Duration
	// Querier, when set, answers run_cli and (when it implements
	// supervisor.Investigator) investigations instead of the codex/claude
	// CLIs found on PATH; CLI is then ignored. The voice bench's scratch
	// daemons use it to give scripted findings (internal/bench), so a
	// benchmark never runs a real coding agent.
	Querier supervisor.Querier
	// HeardEcho, with VoiceOff, reports every spoken push and every text
	// router answer back to the supervisor as a voice session would
	// (sent, spoken in full, turn complete), so the heard bookkeeping —
	// what a "yes" answers, the relay gate — works without a voice
	// model. For harnesses that stand in for the user (`sheepdog
	// bench run --tier 3`); `serve --heard-echo`.
	HeardEcho bool
	// Setup, when set, serves the setup page (/setup, loopback only)
	// that writes these files (sheepdog serve sets it; the bench and the
	// demo do not).
	Setup *SetupOptions
}

// SetupOptions are what the setup page needs from the process that runs
// the daemon: the files it writes and the hooks that outlive one build
// of the daemon (the login service, the demo).
type SetupOptions struct {
	ConfigPath, EnvPath string
	Endpoints           onboard.Endpoints
	Service             func(ctx context.Context) web.ServiceState
	InstallService      func() error
	Demo                web.DemoControl
}

func (o Options) withDefaults() Options {
	if o.Voice == "" {
		o.Voice = VoiceOn
	}
	if o.Herdr == "" {
		o.Herdr = HerdrSocket
	}
	if o.CLI == "" {
		o.CLI = CLIOn
	}
	if o.Brain == "" {
		o.Brain = BrainOn
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

	elog    *eventlog.Log
	log     logging.Logger
	store   *state.Store
	herdr   *inject.Herdr
	backend herdr.Backend // nil in mock mode
	wref    *watcherRef   // nil in mock mode
	// term backs /term (the live terminal); nil in mock mode.
	term     *web.TermDeps
	sup      *supervisor.Supervisor
	brain    *brain.Brain // nil when off
	brainOff string       // why the orchestrator is off (web.Deps.BrainOffReason)
	// brainProviders lists the orchestrator's providers for the picker,
	// unavailable ones with the reason (web.Deps.BrainProviders).
	brainProviders []web.ProviderInfo
	voice          voice.Session // nil when off
	voiceSwitch    *voice.Switcher
	voiceCtx       *ctxcap.VoiceLog     // the voice context log (/debug Context tab)
	routerCtx      *ctxcap.VoiceSession // the text router's session in it (--voice=off)
	web            *web.Server
	prompts        *prompts.Loader
	port           int

	queryLazy *lazyQuerier // nil when --cli=off; filled in by Run after daemon.start

	// observer runs the observers (internal/observe) off a lossy tap of
	// the event log.
	observer *observe.Observer
	watchdog *observe.Watchdog

	// push sends phone notifications to the iOS app (nil when its device
	// file could not be read: pushErr says why).
	push    *push.Sender
	pushErr error

	// voiceIssue says why the voice is not connected ("" when it is, or
	// is connecting for the first time): no key yet (setup mode), or a
	// failed connect being retried. /api/state, the pages and
	// `sheepdog status` show it.
	issueMu    sync.Mutex
	voiceIssue string
	// restart asks Run to shut down and return ErrRestart (RequestRestart).
	restart       chan string
	restartOnce   sync.Once
	restartReason string
}

// ReasonUpdate is the restart reason that moves the daemon into a new
// executable after an upgrade: serve re-executes it (or exits for the
// login service to start it).
const ReasonUpdate = "update installed"

// Idle returns "" when nobody is using the daemon, else why not: a voice
// client (browser or phone) is connected, the orchestrator is in a turn,
// or an approval question is open. The update restart waits for "".
func (a *App) Idle() string {
	switch {
	case a.web != nil && a.web.VoiceClientConnected():
		return "a voice client is connected"
	case a.brain.Busy():
		return "the orchestrator is working"
	case a.store != nil && a.store.Snapshot().PendingAction != nil:
		return "an approval question is open"
	}
	return ""
}

// ReasonServiceHandoff is the restart reason that hands the daemon over
// to the login service: serve installs the service and exits instead of
// building the daemon again.
const ReasonServiceHandoff = "setup: start at login"

// RestartReason is the reason RequestRestart was given ("" before).
func (a *App) RestartReason() string {
	a.issueMu.Lock()
	defer a.issueMu.Unlock()
	return a.restartReason
}

// ErrRestart is what Run returns after RequestRestart: the caller
// (sheepdog serve) reloads the env file and the config and builds the
// daemon again in the same process, so new keys or a new bind take
// effect without the user restarting anything.
var ErrRestart = errors.New("app: restart requested")

// RequestRestart makes Run shut down cleanly and return ErrRestart.
// Safe to call more than once and from any goroutine.
func (a *App) RequestRestart(reason string) {
	a.restartOnce.Do(func() {
		a.log.Log("daemon", "daemon.restart_requested", "", map[string]any{"reason": reason})
		a.issueMu.Lock()
		a.restartReason = reason
		a.issueMu.Unlock()
		a.restart <- reason
	})
}

// SetupNeeded reports whether the voice has no credentials (the daemon
// runs in setup mode).
func (a *App) SetupNeeded() bool {
	if a.voiceSwitch == nil {
		return false
	}
	cur := a.voiceSwitch.Provider()
	for _, p := range a.voiceSwitch.Providers() {
		if p.ID == cur {
			return !p.Available
		}
	}
	return false
}

// Check reports the error Build would refuse cfg and opts with (a bad
// mode flag, a missing voice key, a non-loopback bind without a token)
// without building anything, so serve can say so before it takes the
// single-instance lock or opens a file.
func Check(cfg config.Config, opts Options) error {
	opts = opts.withDefaults()
	switch opts.Voice {
	case VoiceOn, VoiceGemini, VoiceOpenAI, VoiceOff:
	default:
		return fmt.Errorf("app: --voice must be %s, %s, %s or %s", VoiceOn, VoiceGemini, VoiceOpenAI, VoiceOff)
	}
	if opts.Herdr != HerdrSocket && opts.Herdr != HerdrMock {
		return fmt.Errorf("app: --herdr must be %s or %s", HerdrSocket, HerdrMock)
	}
	if opts.CLI != CLIOn && opts.CLI != CLIOff {
		return fmt.Errorf("app: --cli must be %s or %s", CLIOn, CLIOff)
	}
	if opts.Brain != BrainOn && opts.Brain != BrainOff && opts.Brain != BrainFake {
		return fmt.Errorf("app: --brain must be %s, %s or %s", BrainOn, BrainOff, BrainFake)
	}
	// A missing voice key is not an error with the default --voice=on:
	// the daemon starts in setup mode (web server up, voice off until
	// `sheepdog setup` saves a key). Only a provider forced by the flag
	// must have its key.
	if opts.Voice == VoiceGemini || opts.Voice == VoiceOpenAI {
		switch initialVoiceProvider(cfg, opts) {
		case VoiceGemini:
			if _, _, missing := GeminiLive(cfg.Gemini); missing != "" {
				return fmt.Errorf("app: the gemini voice (backend %s) needs %s; use --voice=off to run without it", cfg.Gemini.Backend, missing)
			}
		case VoiceOpenAI:
			if cfg.Voice.OpenAI.APIKey == "" {
				return errors.New("app: the openai voice needs OPENAI_API_KEY (or voice.openai.api_key); use --voice=off to run without it")
			}
		}
	}
	return nil
}

// Build validates the options and wires every component. Nothing is
// started; the state table is loaded and the event log opened.
func Build(cfg config.Config, opts Options) (*App, error) {
	opts = opts.withDefaults()
	if err := Check(cfg, opts); err != nil {
		return nil, err
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
	a := &App{cfg: cfg, opts: opts, elog: elog, log: log, port: port, restart: make(chan string, 1)}
	if err := a.wire(); err != nil {
		_ = elog.Close()
		return nil, err
	}
	a.observer = observe.New(a.log, elog.Tap(observe.TapBuffer), a.observers()...)
	return a, nil
}

// Listen binds web.bind:<port> (web.bind defaults to 127.0.0.1). URL is
// valid afterwards.
func (a *App) Listen() error {
	return a.web.Listen(net.JoinHostPort(a.cfg.Web.Bind, strconv.Itoa(a.port)))
}

// URL returns the base URL of the web server ("" before Listen).
func (a *App) URL() string {
	addr := a.web.Addr()
	if addr == "" {
		return ""
	}
	return "http://" + addr
}

// URLs returns every URL the daemon can be reached on: just URL() for a
// loopback bind (unchanged); for a non-loopback bind (CONFIG.md
// "web.bind"), "https://<ip>:<port>" for every non-loopback IPv4 address
// of the host, so the operator can read the LAN or Tailscale URL off the
// log instead of guessing which interface it is — falling back to
// URL() alone when none was found (no network, or interface enumeration
// failed).
func (a *App) URLs() []string {
	addr := a.web.Addr()
	if addr == "" {
		return nil
	}
	if a.cfg.Web.IsLoopback() {
		return []string{a.URL()}
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return []string{a.URL()}
	}
	if urls := lanURLs(port); len(urls) > 0 {
		return urls
	}
	return []string{a.URL()}
}

// lanURLs returns "https://<ip>:<port>" for every non-loopback IPv4
// address of the host, sorted. Best-effort: a failure to enumerate
// interfaces yields nil, never an error (the caller falls back to
// URL()).
func lanURLs(port string) []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		out = append(out, "https://"+net.JoinHostPort(ip4.String(), port))
	}
	sort.Strings(out)
	return out
}

// Config is the config the daemon was built with.
func (a *App) Config() config.Config { return a.cfg }

// Log returns the daemon's logger (the event log, plus the echo).
func (a *App) Log() logging.Logger { return a.log }
