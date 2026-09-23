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
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
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
	// BrainFake runs the brain loop over braintest.Reflex, a model that
	// answers from what it sees (ask → read the screen, propose → prompt)
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
	// CLI is CLIOn (default) or CLIOff (`--cli=off` takes the brain's
	// run_cli tool away; it then answers from the screen alone).
	CLI string
	// Brain is BrainOn (default), BrainOff (`--brain=off`: the daemon
	// speaks its code sentences and sends nothing) or BrainFake (the
	// reflex fake model, dev only). On needs config brain.enabled and a
	// Gemini key; without a key the brain is silently off.
	Brain string
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
	brainOff string       // why the brain is off (web.Deps.BrainOffReason)
	// brainProviders lists the brain's providers for the picker,
	// unavailable ones with the reason (web.Deps.BrainProviders).
	brainProviders []web.ProviderInfo
	voice          voice.Session // nil when off
	voiceSwitch    *voice.Switcher
	web            *web.Server
	prompts        *prompts.Loader
	port           int

	queryLazy *lazyQuerier // nil when --cli=off; filled in by Run after daemon.start
}

// Build validates the options and wires every component. Nothing is
// started; the state table is loaded and the event log opened.
func Build(cfg config.Config, opts Options) (*App, error) {
	opts = opts.withDefaults()
	switch opts.Voice {
	case VoiceOn, VoiceGemini, VoiceOpenAI, VoiceOff:
	default:
		return nil, fmt.Errorf("app: --voice must be %s, %s, %s or %s", VoiceOn, VoiceGemini, VoiceOpenAI, VoiceOff)
	}
	if opts.Herdr != HerdrSocket && opts.Herdr != HerdrMock {
		return nil, fmt.Errorf("app: --herdr must be %s or %s", HerdrSocket, HerdrMock)
	}
	if opts.CLI != CLIOn && opts.CLI != CLIOff {
		return nil, fmt.Errorf("app: --cli must be %s or %s", CLIOn, CLIOff)
	}
	if opts.Brain != BrainOn && opts.Brain != BrainOff && opts.Brain != BrainFake {
		return nil, fmt.Errorf("app: --brain must be %s, %s or %s", BrainOn, BrainOff, BrainFake)
	}
	if opts.Voice != VoiceOff {
		switch initialVoiceProvider(cfg, opts) {
		case VoiceGemini:
			if len(cfg.Gemini.APIKeys) == 0 {
				return nil, errors.New("app: the gemini voice needs GEMINI_API_KEY (or gemini.api_keys); use --voice=off to run without it")
			}
		case VoiceOpenAI:
			if cfg.Voice.OpenAI.APIKey == "" {
				return nil, errors.New("app: the openai voice needs OPENAI_API_KEY (or voice.openai.api_key); use --voice=off to run without it")
			}
		}
	}
	if !cfg.Web.IsLoopback() && cfg.Web.Token == "" {
		return nil, errors.New("app: web.token is required when web.bind is not loopback")
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
	// satisfies supervisor.Querier so ask() can route to it once ready.
	pusher := &lazyPusher{}
	lazyQ := &lazyQuerier{}
	var querier supervisor.Querier // nil (not lazyQ) when --cli=off, so ask() skips the deep path entirely
	if opts.CLI == CLIOn {
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
	default:
		b, err := buildBrain(cfg, a.prompts, a.log)
		if err != nil {
			return err
		}
		a.brain = b
		a.brainProviders = BrainProviders(cfg)
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
		RepeatWindow:     time.Duration(cfg.Supervisor.RepeatWindowS) * time.Second,
		AuthoriseWindow:  time.Duration(cfg.Supervisor.AuthoriseWindowS) * time.Second,

		InvestigateMax:        time.Duration(cfg.Brain.InvestigateMaxS) * time.Second,
		InvestigateSessionTTL: time.Duration(cfg.Brain.InvestigateSessionTTLS) * time.Second,
		Brain:                 a.brain,
	})
	if err != nil {
		if a.brain != nil {
			_ = a.brain.Close()
		}
		return err
	}
	a.sup = sup

	gcfg := gemini.Config{
		APIKeys: cfg.Gemini.APIKeys,
		Model:   cfg.Gemini.Model,
		Voice:   cfg.Gemini.Voice,
		Aliases: a.aliases(),
		Name:    cfg.Voice.Name,
		Handler: sup.Handler(),
		Prompts: a.prompts,

		StallAfter:       time.Duration(cfg.Gemini.StallAfterS) * time.Second,
		StartSensitivity: cfg.Gemini.StartSensitivity,
		EndSensitivity:   cfg.Gemini.EndSensitivity,
	}
	if _, err := gemini.SystemPrompt(gcfg); err != nil {
		return err
	}
	if opts.Voice != VoiceOff {
		sw, err := voice.NewSwitcher(a.voiceProviders(gcfg, sup.Handler()), initialVoiceProvider(cfg, opts), a.log)
		if err != nil {
			return fmt.Errorf("app: voice: %w", err)
		}
		a.voice = sw
		a.voiceSwitch = sw
		pusher.set(sw)
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

		SystemPrompt:   func() string { s, _ := gemini.SystemPrompt(gcfg); return s },
		Aliases:        gcfg.Aliases,
		VoiceName:      cfg.Voice.Name,
		ReloadPrompts:  a.ReloadPrompts,
		Brain:          a.brain,
		BrainOffReason: a.brainOff,
		BrainProviders: a.brainProviders,
		Token:          cfg.Web.Token,
	}
	if opts.Dev || a.cfg.Web.Dev {
		deps.DevTestdataDir = a.cfg.Web.DevTestdataDir
		if deps.DevTestdataDir == "" {
			deps.DevTestdataDir = config.DefaultDevTestdataDir
		}
		// The sample brain transcript sits next to the audio fixtures
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

// voiceProviders lists both voice providers for the switcher, the one
// without a key unavailable with the reason.
func (a *App) voiceProviders(gcfg gemini.Config, handler voice.ToolHandler) []voice.Provider {
	cfg := a.cfg
	g := voice.Provider{ProviderInfo: voice.ProviderInfo{ID: VoiceGemini, Label: "Gemini Live", Model: gcfg.Model, Available: len(cfg.Gemini.APIKeys) > 0}}
	if gcfg.Model == "" {
		g.Model = gemini.DefaultModel
	}
	if g.Available {
		g.New = func() (voice.Session, error) { return gemini.New(gcfg, a.log) }
	} else {
		g.Reason = "no GEMINI_API_KEY"
	}
	oc := cfg.Voice.OpenAI
	o := voice.Provider{ProviderInfo: voice.ProviderInfo{ID: VoiceOpenAI, Label: "GPT-Live", Model: oc.Model, Available: oc.APIKey != "", Billing: voice.BillingPerMinute}}
	if o.Available {
		router := &inject.Router{}
		name, aliases, pl := cfg.Voice.Name, gcfg.Aliases, a.prompts
		ocfg := gptlive.Config{
			APIKey: oc.APIKey, Model: oc.Model, Voice: oc.Voice,
			IdleClose: time.Duration(oc.IdleCloseS) * time.Second,
			Handler:   handler,
			Classify:  router.Route,
			Instructions: func() (string, error) {
				text, _ := pl.Render(prompts.AssistantGPTLive, prompts.AssistantVars(name, aliases))
				return text, nil
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

// VoiceProvider reports the voice provider ("off" without a voice).
func (a *App) VoiceProvider() string {
	if a.voiceSwitch == nil {
		return VoiceOff
	}
	return a.voiceSwitch.Provider()
}

// buildBrain builds the supervisor brain with the [brain] config
// (BrainConfig) and a transcript under the data dir, over every provider
// that has a key: gemini (the key pool) and deepseek ([deepseek]),
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

// BrainModels builds the brain's Model of every provider the config
// has a key for (shared with `sheepdog brain-test`): gemini over the
// key pool, deepseek over the OpenAI-compatible adapter. An empty map
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
	return models, nil
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
	return []web.ProviderInfo{gemini, deepseek}
}

// BrainConfig maps the [brain] config onto brain.Config (shared with
// `sheepdog brain-test`).
func BrainConfig(cfg config.Config) brain.Config {
	return brain.Config{
		Provider:        cfg.Brain.Provider,
		Model:           cfg.Brain.Model,
		MaxSteps:        cfg.Brain.MaxSteps,
		AskTimeout:      time.Duration(cfg.Brain.AskTimeoutS) * time.Second,
		ProposeTimeout:  time.Duration(cfg.Brain.ProposeTimeoutS) * time.Second,
		WindowTokens:    cfg.Brain.WindowTokens,
		PruneAfterTurns: cfg.Brain.PruneAfterTurns,
		StepReserve:     time.Duration(cfg.Brain.StepReserveS) * time.Second,
		FinalGrace:      time.Duration(cfg.Brain.FinalGraceS) * time.Second,
		EnrichTimeout:   time.Duration(cfg.Brain.EnrichTimeoutS) * time.Second,
		DataDir:         cfg.DataDir,
	}
}

// Brain returns the attached brain, or nil when it is off.
func (a *App) Brain() *brain.Brain { return a.brain }

// BrainMode reports the effective brain mode: BrainOn, BrainFake, or
// BrainOff (with BrainOffReason saying why).
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

// BrainOffReason says why the brain is off ("" when it is on).
func (a *App) BrainOffReason() string { return a.brainOff }

// BrainModel reports the brain's current model ("" when it is off): the
// configured brain.model until a runtime switch. BrainProvider likewise
// for the provider.
func (a *App) BrainModel() string {
	if a.brain == nil {
		return ""
	}
	return a.brain.Model()
}

// BrainProvider reports the brain's current provider ("" when it is
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

// Prompts returns the prompt workspace loader.
func (a *App) Prompts() *prompts.Loader { return a.prompts }

// ReloadPrompts rescans the prompt override directory (SIGHUP, POST
// /api/prompts/reload). The brain and the voice session share this
// Loader, so both pick up the change: the brain on its next turn, the
// voice session on its next (re)connect. Callers log prompts.reloaded
// themselves (the "via" differs: sighup vs api).
func (a *App) ReloadPrompts() []prompts.File { return a.prompts.Reload() }

// buildQuerier detects codex/claude on PATH (logging cli.detected) and, if
// either is found, builds the Runner that backs ask()'s deep path. Called
// from Run, once, right after daemon.start. Neither found returns
// (nil, nil): the deep path is simply unavailable, per plan §3.5 ("if
// absent, the brain simply has no run_cli tool).
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
	a.restoreBrainModel()
	a.restoreVoiceProvider()
	// Logged here, not in wire(), so it never precedes daemon.start (the
	// Loader itself is built in wire(), since the brain and the voice
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

// lazyQuerier lets the supervisor be built (with a non-nil Querier, so
// ask()'s deep path is enabled) before CLI detection has run: detection
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
	res, err := runner.Run(ctx, cli.Query{Workspace: q.Workspace, Question: q.Question, Session: q.Session, Timeout: q.Timeout, Slot: q.Slot})
	if err != nil {
		return supervisor.InvestigationResult{}, err
	}
	return supervisor.InvestigationResult{Backend: string(res.Backend), Answer: res.Raw, SessionID: res.SessionID}, nil
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
