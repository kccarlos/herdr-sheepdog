package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/feedback"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/identity"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/update"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
	"github.com/kccarlos/herdr-sheepdog/prompts"
	static "github.com/kccarlos/herdr-sheepdog/web"
)

// logSource is the source of every event this package logs.
const logSource = "web"

// Supervisor is what the web layer needs from the supervisor: the tool
// handler (cancel goes through it like every other tool), the runtime
// settings behind /api/settings, the user's typed inputs the one rail
// reads (docs/dev/roles.md "The one rail") and the tap behind
// POST /api/pending/answer.
type Supervisor interface {
	Handler() voice.ToolHandler
	Settings() supervisor.Settings
	SetSettings(supervisor.Settings) error
	ShepherdInput(kind supervisor.InputKind, text string)
	AnswerTap(ctx context.Context, id string, option int) (voice.ToolResult, error)
	// ConversationStartedAt and ResetConversation back GET /api/state's
	// conversation_started_at and POST /api/conversation/reset ("New
	// conversation", docs/dev/roles.md).
	ConversationStartedAt() time.Time
	ResetConversation(ctx context.Context, via string) (id string, at time.Time, err error)
	// Screen, WindowPrompt and WindowKeys back the agent window: GET
	// /api/agents/{pane_id}/screen and POST /api/agents/{pane_id}/input
	// (docs/dev/ws-contract.md "Agent window: screen and input").
	Screen(ctx context.Context, paneID string, lines int) (supervisor.Screen, error)
	// ScreenANSI backs the coloured snapshot (?ansi=1, screen-subscribe
	// ansi): the visible screen with SGR colours, feature screen-ansi.
	ScreenANSI(ctx context.Context, paneID string) (supervisor.Screen, error)
	WindowPrompt(ctx context.Context, paneID, text string) (supervisor.WindowResult, error)
	WindowKeys(ctx context.Context, paneID string, keys []string) (supervisor.WindowResult, error)
}

// AgentActions is the optional half of Supervisor behind the user's own
// agent actions (agents_actions.go, feature agent-actions).
type AgentActions interface {
	UserNewAgent(ctx context.Context, req supervisor.NewAgentRequest) (supervisor.NewAgentResult, error)
	Spaces(ctx context.Context) (supervisor.SpacesView, error)
	NewSpace(ctx context.Context, dir, name, branch string) (string, string, error)
	RenameSpace(ctx context.Context, id, name string) (string, error)
	CloseSpace(ctx context.Context, id string) (string, error)
	SpaceWorktrees(ctx context.Context, id string) (herdr.WorktreeList, error)
	OpenWorktree(ctx context.Context, id, path, branch string) (string, error)
	RemoveWorktree(ctx context.Context, id string, force bool) (string, error)
	SpaceCommands(ctx context.Context, id string) ([]supervisor.WrapUp, error)
	SaveCommand(ctx context.Context, id, cmdID, name, command string) (supervisor.WrapUp, error)
	DeleteCommand(ctx context.Context, id, cmdID string) error
	RunCommand(ctx context.Context, id, cmdID string) (string, error)
	UserRename(ctx context.Context, paneID, name string) (string, error)
	UserInterrupt(ctx context.Context, paneID string) (string, error)
	UserClose(ctx context.Context, paneID string) (string, error)
}

// EventInjector is the Watcher's optional second input channel (POST
// /api/herdr/event), beside the herdr socket subscription.
type EventInjector interface {
	Inject(ev herdr.StatusEvent)
}

// Deps are the components the server exposes. Log and Store are
// required; the rest may be nil (the matching endpoints then answer 503).
type Deps struct {
	// Computer is this computer's id and name for GET /api/hello.
	Computer identity.Computer
	// Log is the event log the SSE endpoint tails.
	Log *eventlog.Log
	// Logger is where this package writes its own events (default Log;
	// the daemon passes a tee when --verbose echoes events to stderr).
	Logger     logging.Logger
	Store      *state.Store
	Supervisor Supervisor
	Injector   *inject.Injector
	// Router is the text-only tool router used for simulated utterances
	// when Voice is nil.
	Router *inject.Router
	// HeardEcho, when set (--voice=off with `serve --heard-echo`), gets
	// the voice events a voice session would report around a routed
	// utterance: the to_supervisor call, then the answer spoken in full
	// and the end of the turn.
	HeardEcho func(voice.Event)
	// Voice is the live voice session; nil means --voice=off.
	Voice voice.Session
	// VoiceSwitch backs GET /api/voice/providers and PUT
	// /api/voice/provider (the runtime voice provider switch, persisted
	// in Store's session.voice_provider); nil (--voice=off) answers the
	// GET with provider "off" and the PUT with 503.
	VoiceSwitch VoiceSwitcher
	// Watcher receives POST /api/herdr/event status events; nil means
	// --herdr=mock.
	Watcher EventInjector
	// Brain backs GET /api/brain/{transcript,context,system_prompt,models}
	// and PUT /api/brain/model (the runtime provider/model switch,
	// persisted in Store's session.brain_provider / brain_model); nil
	// means the orchestrator is off and those answer 503 with BrainOffReason
	// ("--brain=off", "brain.enabled=false", "no Gemini key").
	Brain          *brain.Brain
	BrainOffReason string
	// Accounts is the daemon's set of account problems (an invalid key,
	// a used-up quota or balance) shared with the orchestrator; the hub
	// shows each one to the client once and again to a client that
	// connects later, and adds the voice provider's own (accounts.go).
	// nil gets an empty set.
	Accounts *brain.AccountAlerts
	// BrainProviders lists the providers the picker offers, available
	// or not (with the reason: "no DEEPSEEK_API_KEY"); nil lists the
	// orchestrator's registered providers, all available.
	BrainProviders []ProviderInfo
	// HerdrMode and VoiceMode are reported by /api/state ("socket"|"mock",
	// "gemini"|"off").
	HerdrMode, VoiceMode string
	// SystemPrompt and Aliases are what GET /api/voice/system_prompt
	// answers: the voice system prompt the session runs with (or would,
	// with --voice=off) and the aliases rendered into it. SystemPrompt is
	// a func, not a string, so a prompts.Loader reload (SIGHUP, POST
	// /api/prompts/reload) is reflected without rebuilding Deps; nil
	// means no system prompt configured (404).
	SystemPrompt func() string
	Aliases      []string
	// Names, when set, returns the running agents' current spoken names
	// (docs/dev/roles.md "Agent names"): the list the system prompt is
	// rendered with at the next (re)connect. GET /api/voice/system_prompt
	// answers it in place of Aliases (the static start-up list) whenever
	// it returns any.
	Names func() []string
	// VoiceName is the voice's own name (config voice.name), served on
	// /api/state so the pages can label its side of the conversation.
	VoiceName string
	Version   string
	// Update, when set, backs /api/state's update field.
	Update func() update.Status
	// ReloadPrompts, when set, backs POST /api/prompts/reload: it
	// rescans the prompt override directory and returns the resulting
	// file list for the response and the prompts.reloaded log line.
	ReloadPrompts func() []prompts.File
	// DevTestdataDir, when set, serves GET /testdata/<name>.wav from that
	// directory (the /voice page's file-source test mode). Empty (the
	// default) registers no route: this is a developer feature.
	DevTestdataDir string
	// DevBrainDir, when set with DevTestdataDir, serves GET
	// /testdata/brain/<name>.jsonl from that directory: the sample orchestrator
	// transcripts the /debug Supervisor tab's console handle loads.
	DevBrainDir string

	// VoiceContext backs GET /api/context/voice and the voice half of
	// /api/context/timeline (the /debug Context tab); nil answers 503.
	VoiceContext *ctxcap.VoiceLog
	// RouterContext records the text router's utterances, tool calls and
	// results into VoiceContext (--voice=off: the router stands in for
	// the voice model); nil records nothing.
	RouterContext *ctxcap.VoiceSession

	// Push backs POST/DELETE /api/push/register and GET /api/push/status
	// (the iOS app's device registration, docs/dev/push.md); nil answers 503.
	Push PushService

	// Heartbeat is the SSE keepalive period (default 15 s).
	Heartbeat time.Duration
	// OutQueue is the per-client outbound frame limit (default 256).
	OutQueue int
	// StatsInterval is how often a web.client.stats event reports the
	// attached client's frame counters (default 5 s).
	StatsInterval time.Duration
	// ToolTimeout bounds one tool call or injected chain (default 90 s).
	ToolTimeout time.Duration
	// HandshakeTimeout is how long /ws waits for a client-ready before
	// it treats the client as a legacy protocol-1 client (default 2 s;
	// docs/dev/ws-contract.md "Protocol version and handshake").
	HandshakeTimeout time.Duration
	// MinProtocol is the oldest /ws client protocol served (default
	// wsproto.MinProtocolVersion); a lower one is refused with
	// status closed/protocol_too_old.
	MinProtocol int
	// ScreenInterval is how often a /ws screen subscription reads the
	// agent's window (default 1 s); ScreenMinGap is the least time between
	// two reads, when a transition or an input asks for one sooner
	// (default 250 ms). docs/dev/ws-contract.md "Agent window over /ws".
	ScreenInterval time.Duration
	ScreenMinGap   time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time

	// VoiceIssue, when set, says why the voice is not connected (setup
	// mode, a failed connect being retried; "" when nothing is wrong):
	// GET /api/state voice.problem.
	VoiceIssue func() string
	// SetupNeeded, when set, reports whether the voice has no
	// credentials yet (GET /api/state voice.setup_needed).
	SetupNeeded func() bool
	// Setup, when set, serves the setup page (/setup, /api/setup/*) to
	// loopback clients (setup.go).
	Setup *SetupDeps
	// Restart, when set, makes the daemon reload its env file and config
	// and rebuild itself in place (the setup page, after saving keys or
	// changing the bind).
	Restart func(reason string)

	// Term backs GET /term, the terminal socket of the app's live agent
	// windows (docs/dev/term-contract.md), and the `term` feature; nil
	// (herdr mock mode) serves neither.
	Term *TermDeps
	// ShellLAN is config web.shell_lan: a plain-LAN client may open the
	// phone shell (loopback and Tailscale always may).
	ShellLAN bool

	// DataDir is the daemon's data directory; attachments are kept in its
	// attachments/ folder. Empty answers the attachments route with 503.
	DataDir string
	// GitBin is the program the changes view runs (default "git").
	GitBin string

	// TLS is the daemon's key and self-signed certificate (tlsid.go),
	// served on the same port as plain HTTP; nil makes a fresh one in
	// memory (tests).
	TLS *TLSIdentity

	// Token, when non-empty, is the bearer token (config web.token) every
	// request to /ws, /events and /api/* must present (docs/dev/ws-contract.md
	// "Auth"): Authorization: Bearer <token>, ?token=<token> for /ws and
	// /events, or the sheepdog_token cookie POST /api/login sets. Empty
	// means no auth: every route is public, as in v1.
	Token string
}

// Server is the HTTP server. Create with New, then Listen + Serve.
type Server struct {
	deps Deps
	log  logging.Logger
	mux  *http.ServeMux
	hub  *hub
	term *termServer
	// shellLAN is config web.shell_lan, live: the setup page switches it
	// without a restart (shell.go). kindHook, when set, classifies a
	// request's peer in place of remoteKind (tests).
	shellLAN atomic.Bool
	kindHook func(remote string) string
	// audio records the voice's output chunks for user.feedback
	// snapshots (POST /api/feedback).
	audio *feedback.AudioLog

	// closing is closed by Serve on shutdown so long-lived handlers (SSE)
	// end before http.Server.Shutdown waits for them.
	closing chan struct{}

	// phones remembers the last authorized request from another machine
	// (the setup page's "Connected").
	phones phoneTracker

	mu sync.Mutex
	ln net.Listener
}

// New builds the server and its routes.
func New(deps Deps) (*Server, error) {
	if deps.Log == nil || deps.Store == nil {
		return nil, errors.New("web: Log and Store are required")
	}
	if deps.Heartbeat <= 0 {
		deps.Heartbeat = 15 * time.Second
	}
	if deps.OutQueue <= 0 {
		deps.OutQueue = 256
	}
	if deps.StatsInterval <= 0 {
		deps.StatsInterval = 5 * time.Second
	}
	if deps.ToolTimeout <= 0 {
		deps.ToolTimeout = 90 * time.Second
	}
	if deps.HandshakeTimeout <= 0 {
		deps.HandshakeTimeout = 2 * time.Second
	}
	if deps.MinProtocol <= 0 {
		deps.MinProtocol = wsproto.MinProtocolVersion
	}
	if deps.ScreenInterval <= 0 {
		deps.ScreenInterval = time.Second
	}
	if deps.ScreenMinGap <= 0 {
		deps.ScreenMinGap = 250 * time.Millisecond
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.HerdrMode == "" {
		deps.HerdrMode = "socket"
		if deps.Watcher == nil {
			deps.HerdrMode = "mock"
		}
	}
	if deps.VoiceMode == "" {
		deps.VoiceMode = "gemini"
		if deps.Voice == nil {
			deps.VoiceMode = "off"
		}
	}
	if deps.Logger == nil {
		deps.Logger = deps.Log
	}
	if deps.Accounts == nil {
		deps.Accounts = brain.NewAccountAlerts()
	}
	if deps.TLS == nil {
		id, err := LoadTLSIdentity("", deps.Now)
		if err != nil {
			return nil, err
		}
		deps.TLS = id
	}
	s := &Server{deps: deps, log: deps.Logger, mux: http.NewServeMux(), closing: make(chan struct{}), audio: feedback.NewAudioLog()}
	s.hub = newHub(s)
	if ch, ok := s.cardHost(); ok {
		ch.SetCardSink(cardSink{s.hub})
	}
	s.term = newTermServer(s, deps.Term)
	s.shellLAN.Store(deps.ShellLAN)
	deps.Accounts.Subscribe(s.hub.onAccount)
	if err := s.routes(); err != nil {
		return nil, err
	}
	s.pruneAttachments()
	return s, nil
}

// access is who may use a route (docs/dev/design/security.md "Loopback-only
// routes"). Every route is registered through routeTable with one, so a
// new route without a rule cannot exist.
type access int

const (
	// accessPublic: anyone who reaches the port (the pages and
	// /api/hello).
	accessPublic access = iota
	// accessDevice: a paired device, or this computer itself.
	accessDevice
	// accessUpgrade: accessDevice for /ws, /events and /term, which
	// also take ?token= (a browser cannot set a header there).
	accessUpgrade
	// accessLoopback: this computer only (debug and internal routes).
	accessLoopback
	// accessSetup: this computer only, and a change only from this
	// origin as JSON (the setup page, setup.go localOnly).
	accessSetup
)

// route is one entry of the route table.
type route struct {
	pattern string
	access  access
	h       http.HandlerFunc
}

func (s *Server) routes() error {
	table, err := s.routeTable()
	if err != nil {
		return err
	}
	for _, rt := range table {
		s.mux.HandleFunc(rt.pattern, s.wrap(rt.access, rt.h))
	}
	return nil
}

// wrap puts the access check for a in front of h.
func (s *Server) wrap(a access, h http.HandlerFunc) http.HandlerFunc {
	switch a {
	case accessDevice:
		return s.authFunc(h, false)
	case accessUpgrade:
		return s.authFunc(h, true)
	case accessLoopback:
		return s.loopbackOnly(h)
	case accessSetup:
		return s.localOnly(h)
	}
	return h
}

// loopbackOnly refuses any caller but this computer with 403
// {"error":"loopback_only"} (the guard has already checked the Host).
func (s *Server) loopbackOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.kindOf(r.RemoteAddr) != "loopback" {
			s.log.Log(logSource, "web.loopback_only", "", map[string]any{"path": r.URL.Path, "remote": r.RemoteAddr})
			writeError(w, http.StatusForbidden, "loopback_only")
			return
		}
		h(w, r)
	}
}

// routeTable is every route the daemon serves, with who may use it.
func (s *Server) routeTable() ([]route, error) {
	t := []route{
		{"GET /{$}", accessPublic, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/voice", http.StatusFound)
		}},
	}
	for _, p := range []struct {
		name string
		a    access
	}{{"voice", accessPublic}, {"static", accessPublic}, {"debug", accessLoopback}} {
		pages, err := s.staticRoutes(p.name, p.a)
		if err != nil {
			return nil, err
		}
		t = append(t, pages...)
	}
	t = append(t,
		// Public: a client has to reach /api/hello to learn it found a
		// sheepdog daemon before it can authenticate at all.
		route{"GET /api/hello", accessPublic, s.handleHello},
		route{"POST /api/login", accessPublic, s.handleLogin},

		// A paired device: the app and the voice page. /ws, /events and
		// /term authenticate before the upgrade like any other request.
		route{"GET /events", accessUpgrade, s.handleEvents},
		route{"GET /ws", accessUpgrade, s.hub.handle},
		route{"GET /term", accessUpgrade, s.handleTerm},
		route{"POST /api/shells/{pane_id}/close", accessDevice, s.handleShellClose},
		route{"GET /api/state", accessDevice, s.handleState},
		route{"GET /api/agents", accessDevice, s.handleAgents},
		route{"GET /api/agents/{pane_id}/screen", accessDevice, s.handleAgentScreen},
		route{"POST /api/agents/{pane_id}/input", accessDevice, s.handleAgentInput},
		route{"GET /api/spaces", accessDevice, s.handleSpaces},
		route{"POST /api/spaces", accessDevice, s.handleNewSpace},
		route{"POST /api/spaces/{id}/rename", accessDevice, s.handleSpaceRename},
		route{"POST /api/spaces/{id}/close", accessDevice, s.handleSpaceClose},
		route{"GET /api/spaces/{id}/worktrees", accessDevice, s.handleSpaceWorktrees},
		route{"POST /api/spaces/{id}/worktrees/open", accessDevice, s.handleWorktreeOpen},
		route{"POST /api/spaces/{id}/worktree/remove", accessDevice, s.handleWorktreeRemove},
		route{"GET /api/spaces/{id}/commands", accessDevice, s.handleSpaceCommands},
		route{"POST /api/spaces/{id}/commands", accessDevice, s.handleSaveCommand},
		route{"PUT /api/spaces/{id}/commands/{cid}", accessDevice, s.handleSaveCommand},
		route{"DELETE /api/spaces/{id}/commands/{cid}", accessDevice, s.handleDeleteCommand},
		route{"POST /api/spaces/{id}/commands/{cid}/run", accessDevice, s.handleRunCommand},
		route{"POST /api/agents", accessDevice, s.handleNewAgent},
		route{"POST /api/agents/{pane_id}/rename", accessDevice, s.handleAgentRename},
		route{"POST /api/agents/{pane_id}/interrupt", accessDevice, s.handleAgentInterrupt},
		route{"POST /api/agents/{pane_id}/close", accessDevice, s.handleAgentClose},
		route{"POST /api/agents/{pane_id}/attachments", accessDevice, s.handleAttachment},
		route{"GET /api/agents/{pane_id}/changes", accessDevice, s.handleChanges},
		route{"GET /api/agents/{pane_id}/changes/{path...}", accessDevice, s.handleChangeDiff},
		route{"GET /api/events", accessDevice, s.handleEventsTail},
		route{"GET /api/conversation", accessDevice, s.handleConversation},
		route{"POST /api/conversation/reset", accessDevice, s.handleConversationReset},
		route{"POST /api/utterance", accessDevice, s.handleUtterance},
		route{"GET /api/voice/providers", accessDevice, s.handleVoiceProviders},
		route{"PUT /api/voice/provider", accessDevice, s.handleSetVoiceProvider},
		route{"GET /api/brain/models", accessDevice, s.handleBrainModels},
		route{"PUT /api/brain/model", accessDevice, s.handleSetBrainModel},
		route{"GET /api/settings", accessDevice, s.handleGetSettings},
		route{"PUT /api/settings", accessDevice, s.handlePutSettings},
		route{"POST /api/pending/cancel", accessDevice, s.handleCancel},
		route{"POST /api/pending/answer", accessDevice, s.handlePendingAnswer},
		route{"POST /api/push/register", accessDevice, s.handlePushRegister},
		route{"DELETE /api/push/register", accessDevice, s.handlePushUnregister},
		route{"GET /api/push/status", accessDevice, s.handlePushStatus},
		route{"POST /api/feedback", accessDevice, s.handleFeedback},

		// This computer only: pairing, debug and internal routes.
		route{"GET /api/pair", accessLoopback, s.handlePair},
		route{"GET /api/pair/qr.svg", accessLoopback, s.handlePairQR},
		route{"POST /api/inject/blocked", accessLoopback, s.handleInjectBlocked},
		route{"POST /api/inject/pane", accessLoopback, s.handleInjectPane},
		route{"POST /api/inject/done", accessLoopback, s.handleInjectDone},
		route{"POST /api/inject/state", accessLoopback, s.handleInjectState},
		route{"POST /api/inject/screen", accessLoopback, s.handleInjectScreen},
		route{"POST /api/inject/remove", accessLoopback, s.handleInjectRemove},
		route{"POST /api/herdr/event", accessLoopback, s.handleHerdrEvent},
		route{"POST /api/prompts/reload", accessLoopback, s.handleReloadPrompts},
		route{"GET /api/voice/system_prompt", accessLoopback, s.handleSystemPrompt},
		route{"GET /api/brain/transcript", accessLoopback, s.handleBrainTranscript},
		route{"GET /api/brain/context", accessLoopback, s.handleBrainContext},
		route{"GET /api/brain/system_prompt", accessLoopback, s.handleBrainSystemPrompt},
		route{"GET /api/context/brain", accessLoopback, s.handleContextBrain},
		route{"GET /api/context/voice", accessLoopback, s.handleContextVoice},
		route{"GET /api/context/timeline", accessLoopback, s.handleContextTimeline},
	)
	setup, err := s.setupRoutes()
	if err != nil {
		return nil, err
	}
	t = append(t, setup...)
	if s.deps.DevTestdataDir != "" {
		t = append(t, route{"GET /testdata/{name}", accessLoopback, func(w http.ResponseWriter, r *http.Request) {
			s.serveFixture(w, r, s.deps.DevTestdataDir, ".wav", "audio/wav")
		}})
		if s.deps.DevBrainDir != "" {
			t = append(t, route{"GET /testdata/brain/{name}", accessLoopback, func(w http.ResponseWriter, r *http.Request) {
				s.serveFixture(w, r, s.deps.DevBrainDir, ".jsonl", "application/x-ndjson")
			}})
		}
	}
	return t, nil
}

// serveFixture serves one fixture by base name from dir (dev mode only).
// Only plain names with the given extension are accepted, so nothing
// outside the directory is reachable.
func (s *Server) serveFixture(w http.ResponseWriter, r *http.Request, dir, ext, ctype string) {
	name := r.PathValue("name")
	if name != filepath.Base(name) || !strings.HasSuffix(name, ext) || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", ctype)
	http.ServeFile(w, r, filepath.Join(dir, name))
}

// staticRoutes serves /<name> (index.html) and /<name>/* from the
// embedded FS, with access a.
func (s *Server) staticRoutes(name string, a access) ([]route, error) {
	sub, err := fs.Sub(static.FS, name)
	if err != nil {
		return nil, fmt.Errorf("web: embedded %s: %w", name, err)
	}
	files := http.FileServerFS(sub)
	serve := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}
	return []route{
		{"GET /" + name, a, func(w http.ResponseWriter, r *http.Request) {
			r.URL.Path = "/"
			serve(w, r)
		}},
		{"GET /" + name + "/", a, http.StripPrefix("/"+name, http.HandlerFunc(serve)).ServeHTTP},
	}, nil
}

// OnVoiceEvent forwards a voice session event to the connected browser
// (status, transcript, flush, error). The daemon's event fan-out calls it
// for every event; the supervisor gets the same events separately.
func (s *Server) OnVoiceEvent(ev voice.Event) { s.hub.onVoiceEvent(ev) }

// Run pumps model audio to the browser until ctx ends or the session's
// Audio channel closes. It returns immediately when there is no voice
// session.
func (s *Server) Run(ctx context.Context) {
	if s.deps.Voice == nil {
		return
	}
	audio := s.deps.Voice.Audio()
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-audio:
			if !ok {
				return
			}
			s.hub.sendAudio(chunk)
		}
	}
}

// Listen binds addr (host:port; ":0" picks a free port). Addr() is valid
// afterwards.
func (s *Server) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web: listen %s: %w", addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	return nil
}

// Addr returns the bound address ("" before Listen).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Serve serves on the listener from Listen until ctx ends, then shuts
// down gracefully with the given drain timeout (SSE and WS clients are
// told to go away first).
func (s *Server) Serve(ctx context.Context, drain time.Duration) error {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		return errors.New("web: Serve before Listen")
	}
	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	sl := newSniffListener(ln, s.deps.TLS.TLSConfig(), sniffTimeout)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(sl) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	close(s.closing)
	s.hub.shutdown("shutdown")
	shutCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		_ = srv.Close()
		return err
	}
	return nil
}

// handler is the routed mux behind the guard (sniff.go).
func (s *Server) handler() http.Handler { return s.guard(s.mux) }

// writeJSON encodes v with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// writeError answers {"error": msg}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// readJSON decodes a bounded JSON body into v.
func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return errors.New("empty body")
	}
	return json.Unmarshal(body, v)
}
