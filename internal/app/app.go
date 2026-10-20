package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/cli"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/observe"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/pair"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/update"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gptlive"
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

	elog     *eventlog.Log
	log      logging.Logger
	store    *state.Store
	herdr    *inject.Herdr
	hc       *herdr.Client // nil in mock mode
	wref     *watcherRef   // nil in mock mode
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

// VoiceIssue says why the voice is not connected ("" when nothing is
// wrong).
func (a *App) VoiceIssue() string {
	a.issueMu.Lock()
	defer a.issueMu.Unlock()
	return a.voiceIssue
}

func (a *App) setVoiceIssue(s string) {
	a.issueMu.Lock()
	a.voiceIssue = s
	a.issueMu.Unlock()
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
	if !cfg.Web.IsLoopback() && cfg.Web.Token == "" {
		return errors.New("app: web.token is required when web.bind is not loopback")
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

func (a *App) wire() error {
	cfg, opts := a.cfg, a.opts
	a.prompts = prompts.NewLoader(cfg.Prompts.Dir, a.log)
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

	// The supervisor is built before the voice session (its Handler is the
	// session's tool handler); the pusher is filled in below. The querier
	// (WP4) is detected and built in Run, after daemon.start is logged, so
	// cli.detected never races ahead of it; a nil *lazyQuerier still
	// satisfies supervisor.Querier so the orchestrator's run_cli can route to it
	// once ready.
	pusher := &lazyPusher{}
	lazyQ := &lazyQuerier{}
	var querier supervisor.Querier // nil (not lazyQ) when --cli=off, so run_cli is unavailable entirely
	switch {
	case opts.Querier != nil:
		querier = opts.Querier
	case opts.CLI == CLIOn:
		querier = lazyQ
		a.queryLazy = lazyQ
	}
	switch {
	case opts.Brain == BrainOff:
		a.brainOff = "--brain=off"
	case opts.Brain == BrainFake:
		// An explicit dev flag, like --herdr=mock: brain.enabled is not
		// consulted.
		bcfg := BrainConfig(cfg)
		bcfg.Prompts = a.prompts
		// So no brain.step claims a real provider or model (unless the
		// state file's brain_provider / brain_model say so).
		bcfg.Provider, bcfg.Model = FakeBrainProvider, FakeBrainModel
		model, model2 := braintest.Reflex(), braintest.Reflex()
		model2.Models = braintest.Catalog2()
		if v := os.Getenv("SHEEPDOG_FAKE_BRAIN_DELAY_MS"); v != "" {
			// A dev knob for watching turns overlap (CONFIG.md [brain]):
			// every fake model call takes this long.
			ms, err := strconv.Atoi(v)
			if err != nil || ms < 0 {
				return fmt.Errorf("app: SHEEPDOG_FAKE_BRAIN_DELAY_MS=%q: want a non-negative integer", v)
			}
			model.Delay, model2.Delay = time.Duration(ms)*time.Millisecond, time.Duration(ms)*time.Millisecond
		}
		b, err := brain.New(bcfg, model, a.log)
		if err != nil {
			return fmt.Errorf("app: brain: %w", err)
		}
		if err := b.AddProvider(FakeBrainProvider2, model2); err != nil {
			return fmt.Errorf("app: brain: %w", err)
		}
		a.brain = b
		a.brainProviders = []web.ProviderInfo{{ID: FakeBrainProvider, Label: "Fake", Available: true}, {ID: FakeBrainProvider2, Label: "Fake 2", Available: true}}
	case !cfg.Brain.Enabled:
		a.brainOff = "brain.enabled=false"
	case cfg.Brain.Provider == brain.ProviderGemini && len(cfg.Gemini.APIKeys) == 0:
		a.brainOff = "no Gemini key"
	case cfg.Brain.Provider == brain.ProviderDeepSeek && cfg.DeepSeek.APIKey == "":
		a.brainOff = "no DEEPSEEK_API_KEY"
	case cfg.Brain.Provider == brain.ProviderAnthropic && cfg.Anthropic.APIKey == "":
		a.brainOff = "no ANTHROPIC_API_KEY"
	case cfg.Brain.Provider == brain.ProviderVertex && VertexBrainMissing(cfg.Gemini.Vertex) != "":
		a.brainOff = "no " + VertexBrainMissing(cfg.Gemini.Vertex)
	case compatMissing(cfg, cfg.Brain.Provider) != "":
		a.brainOff = "no " + compatMissing(cfg, cfg.Brain.Provider)
	default:
		b, err := buildBrain(cfg, a.prompts, a.log)
		if err != nil {
			return err
		}
		a.brain = b
		a.brainProviders = BrainProviders(cfg)
	}
	a.push, a.pushErr = push.New(push.Config{
		Mode: cfg.Push.EffectiveMode(), RelayURL: cfg.Push.Relay.URL,
		KeyPath: cfg.Push.APNs.KeyPath, KeyID: cfg.Push.APNs.KeyID, TeamID: cfg.Push.APNs.TeamID, Topic: cfg.Push.APNs.Topic,
		DataDir: cfg.DataDir,
	}, a.log)
	var notifier supervisor.Notifier
	if a.push != nil {
		notifier = a.push
	}
	sup, err := supervisor.New(store, a.herdr, querier, pusher, a.log, supervisor.Config{
		PendingTTL:       time.Duration(cfg.PendingTTLS) * time.Second,
		DataDir:          cfg.DataDir,
		Chattiness:       cfg.Supervisor.Chattiness,
		RecentWindow:     time.Duration(cfg.Supervisor.RecentMinutes) * time.Minute,
		PromiseTTL:       time.Duration(cfg.Supervisor.PromiseTTLS) * time.Second,
		CancelWindow:     time.Duration(cfg.Supervisor.CancelWindowS) * time.Second,
		RepromptPoll:     time.Duration(cfg.Supervisor.RepromptPollS) * time.Second,
		SnapshotInterval: time.Duration(cfg.SnapshotIntervalS) * time.Second,
		AnnounceWait:     time.Duration(cfg.Brain.AnnounceWaitS) * time.Second,
		ReplyWait:        replyWait(cfg, opts),
		RepeatWindow:     time.Duration(cfg.Supervisor.RepeatWindowS) * time.Second,
		AuthoriseWindow:  time.Duration(cfg.Supervisor.AuthoriseWindowS) * time.Second,
		ProjectRoots:     cfg.Supervisor.ProjectRoots,
		ProtectedDirs:    ownDir(),
		OwnPane:          os.Getenv("HERDR_PANE_ID"),

		InvestigateMax:        time.Duration(cfg.Brain.InvestigateMaxS) * time.Second,
		InvestigateSessionTTL: time.Duration(cfg.Brain.InvestigateSessionTTLS) * time.Second,
		Brain:                 a.brain,
		Notifier:              notifier,
	})
	if err != nil {
		if a.brain != nil {
			_ = a.brain.Close()
		}
		return err
	}
	a.sup = sup

	// The voice context log (the /debug Context tab): every voice session
	// records what the daemon sent its model; context.step tells the tab.
	a.voiceCtx = ctxcap.NewVoiceLog(func(t ctxcap.VoiceTurn, updated bool) {
		a.log.Log("voice", "context.step", "", map[string]any{"pane": "voice", "turn": t.Turn, "kind": t.Kind, "session": t.Session, "updated": updated})
	})
	liveKeys, vertex, _ := GeminiLive(cfg.Gemini)
	gcfg := gemini.Config{
		Context: a.voiceCtx,
		APIKeys: liveKeys,
		Vertex:  vertex,
		Model:   cfg.Gemini.Model,
		Voice:   cfg.Gemini.Voice,
		Aliases: a.aliases(),
		// The running agents' spoken names, read at every (re)connect
		// (docs/dev/roles.md "Agent names").
		Names: sup.SpokenNames,
		// Every briefing about an agent names it by its spoken name and
		// says its state as of delivery (information, prompt v5).
		AgentNow: sup.AgentNow,
		// Every spoken announcement says whether a decision is open
		// as it reaches the model (information).
		DecisionsNow: sup.DecisionsNow,
		// The user's language setting (PUT /api/settings), read at
		// every (re)connect; a change mid-session goes as silent context.
		Language: sup.LanguageLine,
		Name:     cfg.Voice.Name,
		Handler:  sup.Handler(),
		Prompts:  a.prompts,

		StallAfter:       time.Duration(cfg.Gemini.StallAfterS) * time.Second,
		StartSensitivity: cfg.Gemini.StartSensitivity,
		EndSensitivity:   cfg.Gemini.EndSensitivity,
		BriefingChannel:  cfg.Gemini.BriefingChannel,
	}
	if _, err := gemini.SystemPrompt(gcfg); err != nil {
		return err
	}
	if opts.Voice != VoiceOff {
		sw, err := voice.NewSwitcher(a.voiceProviders(gcfg, sup.Handler()), initialVoiceProvider(cfg, opts), a.log)
		if err != nil {
			return fmt.Errorf("app: voice: %w", err)
		}
		sw.SetContextLog(a.voiceCtx)
		sw.SetOpenCheck(sup.OpenCheck) // unheard pushes and session refresh ask the supervisor
		a.voice = sw
		a.voiceSwitch = sw
		pusher.set(sw)
		sup.SetVoiceConnected(false) // the session's status events maintain it
	} else {
		// No voice model: pushes are logged as skipped so the blocked chain
		// stays visible, and the session counts as connected so the
		// supervisor produces them at all.
		// The text router stands in for the voice model: its session in
		// the voice context log shows what it relayed and what would have
		// been pushed.
		a.routerCtx = a.voiceCtx.Session("text-router", "")
		a.routerCtx.EpochOnFirstUse("text router (--voice=off)", "No voice model is running: the /debug text router stands in for it. Nothing here reaches a model; it shows what the router relayed and what a voice model would have been pushed.")
		skipped := inject.SkippedPusher{Log: a.log, Context: a.routerCtx}
		if opts.HeardEcho {
			skipped.Heard = func(text string, u voice.Urgency) { heardEcho(sup, voice.EventPush{Text: text, Urgency: u}, text) }
		}
		pusher.set(skipped)
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
		Update:     opts.Update,
		HerdrMode:  opts.Herdr,
		VoiceMode:  opts.Voice,

		SystemPrompt:   func() string { s, _ := gemini.SystemPrompt(gcfg); return s },
		Aliases:        gcfg.Aliases,
		Names:          sup.SpokenNames,
		VoiceName:      cfg.Voice.Name,
		ReloadPrompts:  a.ReloadPrompts,
		Brain:          a.brain,
		BrainOffReason: a.brainOff,
		Accounts:       accountsOf(a.brain),
		BrainProviders: a.brainProviders,
		Token:          cfg.Web.Token,
		VoiceContext:   a.voiceCtx,
		RouterContext:  a.routerCtx,
		VoiceIssue:     a.VoiceIssue,
		SetupNeeded:    a.SetupNeeded,
		Restart:        a.RequestRestart,
		DataDir:        cfg.DataDir,
	}
	if opts.HeardEcho && opts.Voice == VoiceOff {
		deps.HeardEcho = sup.OnVoiceEvent
	}
	if so := opts.Setup; so != nil {
		deps.Setup = &web.SetupDeps{
			ConfigPath: so.ConfigPath, EnvPath: so.EnvPath, Config: cfg, Endpoints: so.Endpoints,
			Service: so.Service, InstallService: so.InstallService, Demo: so.Demo,
			ClearOverrides: func() error {
				return store.UpdateSession(func(s *state.Session) { s.VoiceProvider, s.BrainProvider, s.BrainModel = "", "", "" })
			},
			SysAddrs: pair.SystemAddrs, TailscaleCLI: web.TailscaleInstalled, Boot: time.Now(),
		}
	}
	if opts.Dev || a.cfg.Web.Dev {
		deps.DevTestdataDir = a.cfg.Web.DevTestdataDir
		if deps.DevTestdataDir == "" {
			deps.DevTestdataDir = config.DefaultDevTestdataDir
		}
		// The sample orchestrator transcript sits next to the audio fixtures
		// (testdata/brain/), for the Supervisor tab's console handle.
		deps.DevBrainDir = filepath.Join(filepath.Dir(deps.DevTestdataDir), "brain")
	}
	if a.voice != nil {
		deps.Voice = a.voice
		deps.VoiceSwitch = a.voiceSwitch
	}
	if a.wref != nil {
		deps.Watcher = a.wref
	}
	if a.hc != nil {
		hc := a.hc
		deps.Term = &web.TermDeps{
			Bin: cfg.Herdr.Bin, Socket: cfg.Herdr.Socket,
			Version: func(ctx context.Context) (string, error) {
				p, err := hc.Ping(ctx)
				return p.Version, err
			},
			Shells: hc, ProjectRoots: cfg.Supervisor.ProjectRoots,
		}
	}
	deps.ShellLAN = cfg.Web.ShellLAN
	if a.push != nil {
		deps.Push = a.push
	}
	srv, err := web.New(deps)
	if err != nil {
		return err
	}
	a.web = srv
	return nil
}

// initialVoiceProvider is the provider the voice starts on before the
// state file is consulted: the --voice flag when it names one, else
// voice.provider.
func initialVoiceProvider(cfg config.Config, opts Options) string {
	if opts.Voice == VoiceGemini || opts.Voice == VoiceOpenAI {
		return opts.Voice
	}
	return cfg.Voice.Provider
}

// replyWait is supervisor.Config.ReplyWait: brain.reply_wait_s, except
// with --voice=off, where it is negative (always wait for the reply). No
// voice said "on it" there and nothing fills the dead air; the /debug
// text router shows the tool result it gets, and a late reply would only
// reach the event log as a skipped push.
func replyWait(cfg config.Config, opts Options) time.Duration {
	if opts.Voice == VoiceOff {
		return -1
	}
	return time.Duration(cfg.Brain.ReplyWaitS) * time.Second
}

// voiceProviders lists both voice providers for the switcher, the one
// without a key unavailable with the reason.
func (a *App) voiceProviders(gcfg gemini.Config, handler voice.ToolHandler) []voice.Provider {
	cfg := a.cfg
	_, _, missing := GeminiLive(cfg.Gemini)
	g := voice.Provider{ProviderInfo: voice.ProviderInfo{ID: VoiceGemini, Label: geminiLabel(gcfg.Vertex), Model: gcfg.Model, Available: missing == ""}}
	if gcfg.Model == "" {
		g.Model = gemini.DefaultModel
	}
	if g.Available {
		g.New = func() (voice.Session, error) { return gemini.New(gcfg, a.log) }
	} else {
		g.Reason = "no " + missing
	}
	oc := cfg.Voice.OpenAI
	o := voice.Provider{ProviderInfo: voice.ProviderInfo{ID: VoiceOpenAI, Label: "GPT-Live", Model: oc.Model, Available: oc.APIKey != "", Billing: voice.BillingPerMinute}}
	if o.Available {
		router := &inject.Router{}
		name, aliases, names, pl := cfg.Voice.Name, gcfg.Aliases, gcfg.Names, a.prompts
		language := gcfg.Language
		if language == nil {
			language = func() string { return "" }
		}
		ocfg := gptlive.Config{
			APIKey: oc.APIKey, Model: oc.Model, Voice: oc.Voice,
			IdleClose: time.Duration(oc.IdleCloseS) * time.Second,
			Handler:   handler,
			Classify:  router.Route,
			Context:   a.voiceCtx,
			// The spoken names are key terms of the heard check.
			Names: names,
			Instructions: func() (string, error) {
				list := aliases
				if names != nil {
					if n := names(); len(n) > 0 {
						list = n
					}
				}
				text, _ := pl.Render(prompts.AssistantGPTLive, prompts.AssistantVars(name, list))
				return prompts.WithLanguage(text, language()), nil
			},
		}
		o.New = func() (voice.Session, error) { return gptlive.New(ocfg, a.log) }
	} else {
		o.Reason = "no OPENAI_API_KEY"
	}
	return []voice.Provider{g, o}
}

// restoreVoiceProvider applies the provider picked at runtime in an
// earlier run (the state file's session.voice_provider, kept by PUT
// /api/voice/provider) over the configured one, unless --voice named a
// provider for this run. One that is unavailable now (its key is gone)
// is logged as daemon.voice_provider_error and the configured one kept.
// Called from Run before the voice starts.
func (a *App) restoreVoiceProvider() {
	if a.voiceSwitch == nil || a.opts.Voice != VoiceOn {
		return
	}
	id := a.store.Snapshot().Session.VoiceProvider
	if id == "" {
		return
	}
	if err := a.voiceSwitch.SetProvider(id, "state"); err != nil {
		a.log.Log("daemon", "daemon.voice_provider_error", "", map[string]any{"provider": id, "error": err.Error()})
	}
}

// RecycleVoice forces the voice session to reconnect with its resumption
// handle (a Gemini resume), reporting whether the provider supports it.
// For the bench and debugging; the daemon never calls it on its own.
func (a *App) RecycleVoice(reason string) bool {
	if a.voiceSwitch == nil {
		return false
	}
	return a.voiceSwitch.Recycle(reason)
}

// VoiceProvider reports the voice provider ("off" without a voice).
func (a *App) VoiceProvider() string {
	if a.voiceSwitch == nil {
		return VoiceOff
	}
	return a.voiceSwitch.Provider()
}

// buildBrain builds the orchestrator with the [brain] config
// (BrainConfig) and a transcript under the data dir, over every provider
// that has a key: gemini (the key pool), deepseek ([deepseek]),
// anthropic ([anthropic]),
// vertex ([gemini.vertex]) and each [[brain.openai_compat]] entry,
// starting on brain.provider. The caller has checked that the initial
// provider has a key.
func buildBrain(cfg config.Config, pl *prompts.Loader, log logging.Logger) (*brain.Brain, error) {
	models, err := BrainModels(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("app: brain: %w", err)
	}
	bcfg := BrainConfig(cfg)
	bcfg.Prompts = pl
	initial, ok := models[cfg.Brain.Provider]
	if !ok {
		return nil, fmt.Errorf("app: brain: provider %q has no key", cfg.Brain.Provider)
	}
	b, err := brain.New(bcfg, initial, log)
	if err != nil {
		return nil, fmt.Errorf("app: brain: %w", err)
	}
	for id, m := range models {
		if id == cfg.Brain.Provider {
			continue
		}
		if err := b.AddProvider(id, m); err != nil {
			return nil, fmt.Errorf("app: brain: %w", err)
		}
	}
	return b, nil
}

// BrainModels builds the orchestrator's Model of every provider the config
// has a key for (shared with `sheepdog brain-test`): gemini over the
// key pool, deepseek over the OpenAI-compatible adapter, anthropic over
// the native Messages API adapter, vertex over
// the Gemini adapter on Vertex AI (VertexBrainMissing), and each
// [[brain.openai_compat]] entry over the same adapter without DeepSeek's
// extensions (a loopback one even without a key). An empty map
// when there is no key at all.
func BrainModels(cfg config.Config, log logging.Logger) (map[string]brain.Model, error) {
	models := map[string]brain.Model{}
	if len(cfg.Gemini.APIKeys) > 0 {
		m, err := brain.NewGenaiModel(cfg.Gemini.APIKeys, log)
		if err != nil {
			return nil, err
		}
		models[brain.ProviderGemini] = m
	}
	if cfg.DeepSeek.APIKey != "" {
		thinking := cfg.DeepSeek.Thinking
		m, err := brain.NewOpenAICompatModel(brain.OpenAICompatConfig{Provider: brain.ProviderDeepSeek, BaseURL: cfg.DeepSeek.BaseURL, APIKey: cfg.DeepSeek.APIKey, Thinking: &thinking, ReasoningEffort: cfg.DeepSeek.ReasoningEffort, Log: log})
		if err != nil {
			return nil, err
		}
		models[brain.ProviderDeepSeek] = m
	}
	if cfg.Anthropic.APIKey != "" {
		m, err := brain.NewAnthropicModel(AnthropicConfig(cfg, log))
		if err != nil {
			return nil, err
		}
		models[brain.ProviderAnthropic] = m
	}
	if VertexBrainMissing(cfg.Gemini.Vertex) == "" {
		m, err := brain.NewVertexModel(cfg.Gemini.Vertex.APIKey, cfg.Gemini.Vertex.Project, log)
		if err != nil {
			return nil, err
		}
		models[brain.ProviderVertex] = m
	}
	for _, o := range cfg.Brain.OpenAICompat {
		if !o.Usable() {
			continue
		}
		m, err := brain.NewOpenAICompatModel(brain.OpenAICompatConfig{Provider: o.Name, BaseURL: o.BaseURL, APIKey: o.APIKey, NoKey: o.KeyOptional(), DefaultModel: o.Model, Log: log})
		if err != nil {
			return nil, err
		}
		models[o.Name] = m
	}
	return models, nil
}

// AnthropicConfig maps the [anthropic] config onto the adapter's.
func AnthropicConfig(cfg config.Config, log logging.Logger) brain.AnthropicConfig {
	return brain.AnthropicConfig{
		APIKey: cfg.Anthropic.APIKey, BaseURL: cfg.Anthropic.BaseURL,
		Effort: cfg.Anthropic.Effort, EventEffort: cfg.Anthropic.EventEffort,
		NoFallbacks: !cfg.Anthropic.Fallbacks, PruneAfterTurns: cfg.Brain.PruneAfterTurns, Log: log,
	}
}

// compatMissing names the key variable the [[brain.openai_compat]]
// provider named provider lacks ("" when it has one, needs none, or
// provider is not such an entry).
func compatMissing(cfg config.Config, provider string) string {
	o, ok := cfg.Brain.CompatProvider(provider)
	if !ok || o.Usable() {
		return ""
	}
	return o.APIKeyEnv
}

// VertexBrainMissing names the variable the vertex orchestrator provider lacks
// ("" when it has both). It reuses the voice's [gemini.vertex] key and
// project but not gemini.backend (the orchestrator may run on Vertex while the
// voice stays on AI Studio, or the other way round) nor gemini.vertex.
// location (the orchestrator is always at global: brain.VertexLocation).
func VertexBrainMissing(v config.GeminiVertex) string {
	switch {
	case v.APIKey == "":
		return "VERTEX_API_KEY"
	case v.Project == "":
		return "VERTEX_PROJECT"
	}
	return ""
}

// BrainProviders lists every provider for the /debug picker, with why
// the ones without a key are unavailable.
func BrainProviders(cfg config.Config) []web.ProviderInfo {
	gemini := web.ProviderInfo{ID: brain.ProviderGemini, Label: web.ProviderLabel(brain.ProviderGemini), Available: len(cfg.Gemini.APIKeys) > 0}
	if !gemini.Available {
		gemini.Reason = "no GEMINI_API_KEY"
	}
	deepseek := web.ProviderInfo{ID: brain.ProviderDeepSeek, Label: web.ProviderLabel(brain.ProviderDeepSeek), Available: cfg.DeepSeek.APIKey != ""}
	if !deepseek.Available {
		deepseek.Reason = "no DEEPSEEK_API_KEY"
	}
	anthropic := web.ProviderInfo{ID: brain.ProviderAnthropic, Label: web.ProviderLabel(brain.ProviderAnthropic), Available: cfg.Anthropic.APIKey != ""}
	if !anthropic.Available {
		anthropic.Reason = "no ANTHROPIC_API_KEY"
	}
	vertex := web.ProviderInfo{ID: brain.ProviderVertex, Label: web.ProviderLabel(brain.ProviderVertex), Available: VertexBrainMissing(cfg.Gemini.Vertex) == ""}
	if !vertex.Available {
		vertex.Reason = "no " + VertexBrainMissing(cfg.Gemini.Vertex)
	}
	out := []web.ProviderInfo{gemini, deepseek, anthropic, vertex}
	for _, o := range cfg.Brain.OpenAICompat {
		label := o.Label
		if label == "" {
			label = web.ProviderLabel(o.Name)
		}
		p := web.ProviderInfo{ID: o.Name, Label: label, Available: o.Usable()}
		if !p.Available {
			p.Reason = "no " + o.APIKeyEnv
		}
		out = append(out, p)
	}
	return out
}

// BrainConfig maps the [brain] config onto brain.Config (shared with
// `sheepdog brain-test`).
func BrainConfig(cfg config.Config) brain.Config {
	return brain.Config{
		Provider:            cfg.Brain.Provider,
		Model:               cfg.Brain.Model,
		MaxSteps:            cfg.Brain.MaxSteps,
		UtteranceTimeout:    time.Duration(cfg.Brain.UtteranceTimeoutS) * time.Second,
		WindowTokens:        cfg.Brain.WindowTokens,
		PruneAfterTurns:     cfg.Brain.PruneAfterTurns,
		StepReserve:         time.Duration(cfg.Brain.StepReserveS) * time.Second,
		FinalGrace:          time.Duration(cfg.Brain.FinalGraceS) * time.Second,
		AnnounceTurnTimeout: time.Duration(cfg.Brain.AnnounceTurnTimeoutS) * time.Second,
		AnnounceTimeout:     time.Duration(cfg.Brain.AnnounceTimeoutS) * time.Second,
		DataDir:             cfg.DataDir,
		KeyVars:             compatKeyVars(cfg),
	}
}

// compatKeyVars maps each [[brain.openai_compat]] entry to the variable
// its key is read from, for the orchestrator's account-problem fix lines.
func compatKeyVars(cfg config.Config) map[string]string {
	m := map[string]string{}
	for _, o := range cfg.Brain.OpenAICompat {
		if o.APIKeyEnv != "" {
			m[o.Name] = o.APIKeyEnv
		}
	}
	return m
}

// BrainMode reports the effective orchestrator mode: BrainOn, BrainFake, or
// BrainOff (with a.brainOff saying why).
func (a *App) BrainMode() string {
	switch {
	case a.brain == nil:
		return BrainOff
	case a.opts.Brain == BrainFake:
		return BrainFake
	default:
		return BrainOn
	}
}

// BrainModel reports the orchestrator's current model ("" when it is off): the
// configured brain.model until a runtime switch. BrainProvider likewise
// for the provider.
func (a *App) BrainModel() string {
	if a.brain == nil {
		return ""
	}
	return a.brain.Model()
}

// BrainProvider reports the orchestrator's current provider ("" when it is
// off).
func (a *App) BrainProvider() string {
	if a.brain == nil {
		return ""
	}
	return a.brain.Provider()
}

// restoreBrainModel applies the provider and model chosen at runtime in
// an earlier run (the state file's session.brain_provider /
// brain_model, kept by PUT /api/brain/model) over the configured
// initial values; a provider that is not registered in this run (its
// key is gone) is logged as daemon.brain_model_error and the configured
// pair kept. A model without a provider was kept by a run from before
// providers existed, when Gemini was the only one: it is Gemini's, never
// the configured provider's. Called from Run right after daemon.start,
// before any turn: the brain.model_changed {via: state} it logs belongs
// to this run, not before it.
func (a *App) restoreBrainModel() {
	if a.brain == nil {
		return
	}
	ss := a.store.Snapshot().Session
	if ss.BrainModel == "" {
		return
	}
	provider := ss.BrainProvider
	if provider == "" {
		provider = brain.ProviderGemini
	}
	if err := a.brain.SetModel(provider, ss.BrainModel, "state"); err != nil {
		a.log.Log("daemon", "daemon.brain_model_error", "", map[string]any{"provider": provider, "model": ss.BrainModel, "error": err.Error()})
	}
}

// ReloadPrompts rescans the prompt override directory (SIGHUP, POST
// /api/prompts/reload). The orchestrator and the voice session share this
// Loader, so both pick up the change: the orchestrator on its next turn, the
// voice session on its next (re)connect. Callers log prompts.reloaded
// themselves (the "via" differs: sighup vs api).
func (a *App) ReloadPrompts() []prompts.File { return a.prompts.Reload() }

// buildQuerier detects codex/claude on PATH (logging cli.detected) and, if
// either is found, builds the Runner that backs the orchestrator's run_cli and
// investigations. Called
// from Run, once, right after daemon.start. Neither found returns
// (nil, nil): the deep path is simply unavailable, per plan §3.5 ("if
// absent, the orchestrator simply has no run_cli tool).
func (a *App) buildQuerier() (*cli.Runner, error) {
	cfg := cli.Config{
		Prefer:          a.cfg.CLI.Prefer,
		CodexBin:        a.cfg.CLI.CodexBin,
		ClaudeBin:       a.cfg.CLI.ClaudeBin,
		CodexCmd:        a.cfg.CLI.CodexCmd,
		ClaudeCmd:       a.cfg.CLI.ClaudeCmd,
		CodexResumeCmd:  a.cfg.CLI.CodexResumeCmd,
		ClaudeResumeCmd: a.cfg.CLI.ClaudeResumeCmd,
		ClaudeModel:     a.cfg.CLI.ClaudeModel,
		CodexReasoning:  a.cfg.CLI.CodexReasoning,
		Timeout:         time.Duration(a.cfg.CLI.TimeoutS) * time.Second,
		MaxOutputBytes:  a.cfg.CLI.MaxOutputKB << 10,
		Log:             a.log,
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
// "web.bind"), "http://<ip>:<port>" for every non-loopback IPv4 address
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

// lanURLs returns "http://<ip>:<port>" for every non-loopback IPv4
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
		out = append(out, "http://"+net.JoinHostPort(ip4.String(), port))
	}
	sort.Strings(out)
	return out
}

// Config is the config the daemon was built with.
func (a *App) Config() config.Config { return a.cfg }

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
	brainMode := a.BrainMode()
	a.log.Log("daemon", "daemon.start", "", map[string]any{
		"version": a.opts.Version, "voice": a.opts.Voice, "herdr": a.opts.Herdr, "cli": a.opts.CLI, "brain": brainMode, "url": a.URL(), "urls": a.URLs(),
		"data_dir": a.cfg.DataDir, "brain_provider": a.BrainProvider(), "brain_model": a.BrainModel(), "voice_provider": a.VoiceProvider(),
	})
	if notes := a.cfg.Deprecations(); len(notes) > 0 {
		// Once per run, right after daemon.start: an old config file or
		// environment still loads, but says what to rename (CONFIG.md
		// "Deprecated keys").
		a.log.Log("daemon", "daemon.config_deprecated", "", map[string]any{"notes": notes})
	}
	a.restoreBrainModel()
	a.restoreVoiceProvider()
	if a.pushErr != nil {
		a.log.Log("push", "push.disabled", "", map[string]any{"reason": a.pushErr.Error()})
	} else {
		a.push.LogStartup()
	}
	// Logged here, not in wire(), so it never precedes daemon.start (the
	// Loader itself is built in wire(), since the orchestrator and the voice
	// session need it before Run).
	a.log.Log("daemon", "prompts.loaded", "", map[string]any{"dir": a.cfg.Prompts.Dir, "files": a.prompts.Files()})
	if a.queryLazy != nil {
		if runner, err := a.buildQuerier(); err != nil {
			a.log.Log("daemon", "daemon.cli_error", "", map[string]any{"error": err.Error()})
		} else if runner != nil {
			a.queryLazy.set(runner)
		}
	}
	start := time.Now()
	defer func() {
		// A notification in flight finishes (its push.sent/failed line
		// logged) before daemon.stop; each is bounded by its own timeout.
		a.push.Wait()
		a.log.Log("daemon", "daemon.stop", "", map[string]any{"version": a.opts.Version, "uptime_s": int(time.Since(start).Seconds()), "error": errString(err)})
		// Flush any debounced hardware-durable fsync (state.full_fsync)
		// before the event log closes; harmless no-op otherwise.
		_ = a.store.Close()
		if a.brain != nil {
			_ = a.brain.Close()
		}
		if cerr := a.elog.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	inner, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { defer wg.Done(); a.observer.Run(inner) }()

	if a.wref != nil {
		wg.Add(1)
		go func() { defer wg.Done(); a.runWatcher(inner) }()
	}
	if a.voice != nil {
		// The voice starts in the background and never stops the
		// daemon: without a key it stays off (setup mode); a failed
		// connect (a bad key, no network) is retried with a backoff
		// while the web server, herdr and the supervisor run, so a
		// login service's KeepAlive / Restart never loops on it.
		wg.Add(3)
		go func() { defer wg.Done(); a.startVoice(inner) }()
		go func() {
			defer wg.Done()
			for ev := range a.voice.Events() {
				a.watchdog.OnVoiceEvent(ev)
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

	restart := false
	select {
	case <-ctx.Done():
	case <-a.restart:
		restart = true
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
	if restart {
		return ErrRestart
	}
	return nil
}

// Voice start backoff: the first retry after voiceRetryMin, doubling up
// to voiceRetryMax. Variables so tests can shorten them.
var (
	voiceRetryMin = 5 * time.Second
	voiceRetryMax = 5 * time.Minute
)

// startVoice starts the voice session, retrying a failed connect with a
// backoff until it succeeds or ctx ends. With no credentials for the
// provider it does not retry: the daemon is in setup mode until
// `sheepdog setup` saves a key (and restarts it). Every failure is
// logged (daemon.voice_error) and reported to the pages as a closed
// status with the reason, and an account problem (a rejected key, a
// used-up quota) as the voice's account alert.
func (a *App) startVoice(ctx context.Context) {
	wait := voiceRetryMin
	for attempt := 1; ; attempt++ {
		err := a.voice.Start(ctx)
		if err == nil {
			a.setVoiceIssue("")
			if attempt > 1 {
				a.log.Log("daemon", "daemon.voice_recovered", "", map[string]any{"attempts": attempt})
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		provider := a.VoiceProvider()
		if errors.Is(err, voice.ErrUnavailableProvider) {
			issue := SetupIssue(provider)
			a.setVoiceIssue(issue)
			a.log.Log("daemon", "daemon.setup_mode", "", map[string]any{"voice_provider": provider, "reason": issue})
			a.voiceSwitch.Report(voice.EventStatus{Status: voice.StatusClosed, Reason: "setup_needed"})
			return
		}
		issue := voiceIssueText(provider, err, wait)
		a.setVoiceIssue(issue)
		a.log.Log("daemon", "daemon.voice_error", "", map[string]any{"error": err.Error(), "attempt": attempt, "retry_in_s": int(wait.Seconds())})
		a.voiceSwitch.Report(voice.EventError{Err: err})
		a.voiceSwitch.Report(voice.EventStatus{Status: voice.StatusClosed, Reason: issue})
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait *= 2
		if wait > voiceRetryMax {
			wait = voiceRetryMax
		}
	}
}

// SetupIssue is the voice issue of setup mode: the provider has no key.
func SetupIssue(provider string) string {
	if provider == VoiceOpenAI {
		return "Setup needed: the GPT-Live voice has no OpenAI key yet. Run `sheepdog setup`."
	}
	return "Setup needed: no Gemini API key yet. Run `sheepdog setup`."
}

// voiceIssueText words a failed voice connect for a screen: the account
// problem when it is one, else the error, and when the next try is.
func voiceIssueText(provider string, err error, wait time.Duration) string {
	retry := fmt.Sprintf(" Retrying in %s.", wait.Round(time.Second))
	svc := provider
	if svc == VoiceOpenAI {
		svc = "openai"
	}
	if p := brain.AccountProblemOfErr(err); p != brain.ProblemNone {
		al := brain.NewAccountAlert(brain.RoleVoice, svc, p, false)
		return al.Message + " Fix: " + al.Fix + "." + retry
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return "The voice cannot connect (" + msg + ")." + retry
}

// observers are the handlers the observer feeds (internal/observe).
func (a *App) observers() []observe.Handler {
	a.watchdog = observe.NewWatchdog(a.log, a.watchdogProbes())
	return []observe.Handler{observe.NewLatencyObserver(a.log), a.watchdog}
}

// watchdogProbes are the read-only accessors the watchdog polls; a
// component the daemon runs without (no orchestrator, --voice=off,
// --herdr=mock) has none.
func (a *App) watchdogProbes() observe.Probes {
	var p observe.Probes
	if a.brain != nil {
		p.Brain = a.brain.RunningTurn
	}
	if pb, ok := a.voice.(voice.PushBacklogger); ok {
		p.PushBacklog = pb.PushBacklog
	}
	if li, ok := a.voice.(supervisor.Listener); ok {
		p.Listener = li.HasListener
	}
	if a.wref != nil && a.hc != nil {
		p.HerdrBeat = a.wref.beat
		p.HerdrPing = func(ctx context.Context) error {
			_, err := a.hc.Ping(ctx)
			return err
		}
		// Three missed snapshots (and no event meanwhile).
		iv := time.Duration(a.cfg.SnapshotIntervalS) * time.Second
		if iv <= 0 {
			iv = 30 * time.Second
		}
		p.HerdrStale = 3*iv + 10*time.Second
	}
	return p
}

// runWatcher keeps a Watcher running: transitions are handled serialised
// per pane; a Watcher whose initial snapshot fails (herdr not running) is
// retried after WatcherRetry.
func (a *App) runWatcher(ctx context.Context) {
	var panes paneLocks
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		// Seed the watcher with the persisted rows so the first snapshot
		// closes panes that went away while the daemon was down (WP7b: a
		// closed workspace's agent survived a restart and was offered in
		// clarify questions).
		known := map[string]herdr.State{}
		for _, row := range a.store.List() {
			if !row.Synthetic {
				known[row.PaneID] = row.State
			}
		}
		w := herdr.NewWatcher(a.hc, herdr.WatcherConfig{
			Debounce:         time.Duration(a.cfg.DebounceMS) * time.Millisecond,
			SnapshotInterval: time.Duration(a.cfg.SnapshotIntervalS) * time.Second,
			Known:            known,
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

// beat is the current Watcher's LastBeat; running is false while none
// runs (the watchdog's herdr probe).
func (r *watcherRef) beat() (last time.Time, running bool) {
	r.mu.Lock()
	w := r.w
	r.mu.Unlock()
	if w == nil {
		return time.Time{}, false
	}
	return w.LastBeat(), true
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

// heardEcho reports a line as a voice session reports one it spoke in
// full: first (when set) the event that starts it, then the assistant's
// output transcript and the end of the turn (Options.HeardEcho). On its
// own goroutine: a push may come with supervisor locks held.
func heardEcho(sup *supervisor.Supervisor, start voice.Event, text string) {
	go func() {
		if start != nil {
			sup.OnVoiceEvent(start)
		}
		sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: text, Final: true})
		sup.OnVoiceEvent(voice.EventTurnComplete{})
	}()
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
	return l.PushTagged(text, u, nil)
}

// PushTagged implements supervisor.TagPusher, so a tagged push
// (answers_job) keeps its tag through the lazy indirection — both the
// voice session and the --voice=off pusher take them.
func (l *lazyPusher) PushTagged(text string, u voice.Urgency, tags map[string]any) error {
	l.mu.Lock()
	p := l.p
	l.mu.Unlock()
	if p == nil {
		return errors.New("app: no pusher")
	}
	if tp, ok := p.(supervisor.TagPusher); ok && len(tags) > 0 {
		return tp.PushTagged(text, u, tags)
	}
	return p.Push(text, u)
}

// HasListener implements supervisor.Listener through the indirection:
// the voice session's attach state, or true with --voice=off or before
// the session exists (nothing is spoken there anyway).
func (l *lazyPusher) HasListener() bool {
	l.mu.Lock()
	p := l.p
	l.mu.Unlock()
	if li, ok := p.(supervisor.Listener); ok {
		return li.HasListener()
	}
	return true
}

// Reset forwards to the voice session's Reset ("New conversation",
// supervisor.ResetConversation) once one is attached (the voice.Switcher
// implements it); a no-op before then or with --voice=off, where there
// is nothing for a reset to touch.
func (l *lazyPusher) Reset(reason string) error {
	l.mu.Lock()
	p := l.p
	l.mu.Unlock()
	if r, ok := p.(interface{ Reset(string) error }); ok {
		return r.Reset(reason)
	}
	return nil
}

// lazyQuerier lets the supervisor be built (with a non-nil Querier, so
// run_cli is enabled) before CLI detection has run: detection
// happens in Run, after daemon.start is logged, so cli.detected/cli.run
// never precede it in the event log.
type lazyQuerier struct {
	mu sync.Mutex
	q  supervisor.Querier
	// runner is the same object when the querier is the real Runner: it
	// also backs the investigate tool (supervisor.Investigator).
	runner *cli.Runner
}

func (l *lazyQuerier) set(q supervisor.Querier) {
	l.mu.Lock()
	l.q = q
	if r, ok := q.(*cli.Runner); ok {
		l.runner = r
	}
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

// Investigate implements supervisor.Investigator: the same runner, with
// the investigation's own ceiling and slot and the sub-agent's session
// kept so a follow-up can resume it.
func (l *lazyQuerier) Investigate(ctx context.Context, q supervisor.InvestigationQuery) (supervisor.InvestigationResult, error) {
	l.mu.Lock()
	runner := l.runner
	l.mu.Unlock()
	if runner == nil {
		return supervisor.InvestigationResult{}, cli.ErrUnavailable
	}
	res, err := runner.Run(ctx, cli.Query{Backend: cli.Backend(q.Backend), Workspace: q.Workspace, Question: q.Question, Session: q.Session, Fork: q.Fork, Timeout: q.Timeout, Slot: q.Slot})
	if err != nil {
		return supervisor.InvestigationResult{}, err
	}
	return supervisor.InvestigationResult{Backend: string(res.Backend), Answer: res.Raw, SessionID: res.SessionID}, nil
}

// HasBackend reports whether backend was detected, so asking a worker's
// own session is refused at once on a machine without that worker's
// CLI. Before detection ran it says yes and the run itself reports
// cli.ErrUnavailable.
func (l *lazyQuerier) HasBackend(backend string) bool {
	l.mu.Lock()
	runner := l.runner
	l.mu.Unlock()
	return runner == nil || runner.Has(cli.Backend(backend))
}

// ResolveBackend names the backend investigations will use ("" before
// detection ran), for the investigation.started event.
func (l *lazyQuerier) ResolveBackend() (string, error) {
	l.mu.Lock()
	runner := l.runner
	l.mu.Unlock()
	if runner == nil {
		return "", cli.ErrUnavailable
	}
	b, err := runner.ResolveBackend()
	return string(b), err
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

// ownDir is the daemon's working directory, whose agents are closed only
// when the user named them (supervisor.Config.ProtectedDirs): the user's
// orchestrator session works there.
func ownDir() []string {
	if wd, err := os.Getwd(); err == nil {
		return []string{wd}
	}
	return nil
}

// accountsOf is the orchestrator's set of account problems, shared with
// the web layer; nil without an orchestrator (the web layer then keeps
// the voice's alone).
func accountsOf(b *brain.Brain) *brain.AccountAlerts {
	if b == nil {
		return nil
	}
	return b.Accounts()
}
