package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/cli"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/identity"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/pair"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

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
	computer := identity.Computer{Name: identity.Name(cfg.Computer.Name)}
	if id, err := identity.DaemonID(cfg.DataDir); err != nil {
		a.log.Log("daemon", "daemon.identity_error", "", map[string]any{"error": err.Error()})
	} else {
		computer.ID = id
	}
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
		Computer:       computer,
		ReloadPrompts:  a.ReloadPrompts,
		Brain:          a.brain,
		BrainOffReason: a.brainOff,
		Accounts:       accountsOf(a.brain),
		BrainProviders: a.brainProviders,
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
	deps.Shell, deps.Advertise = cfg.Web.Shell, cfg.Web.Advertise
	deps.TailscaleHTTPS, deps.TailscaleCert = cfg.Web.TailscaleHTTPS, web.TailscaleCertCLI(cfg.DataDir)
	tlsID, err := web.LoadTLSIdentity(cfg.DataDir, nil)
	if err != nil {
		return err
	}
	deps.TLS = tlsID
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
