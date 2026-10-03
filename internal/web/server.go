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
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
	"github.com/kccarlos/herdr-sheepdog/prompts"
	static "github.com/kccarlos/herdr-sheepdog/web"
)

// logSource is the source of every event this package logs.
const logSource = "web"

// Supervisor is what the web layer needs from the supervisor: the tool
// handler (cancel goes through it like every other tool), the runtime
// settings behind /api/settings, the shepherd's typed inputs the one rail
// reads (docs/roles.md "The one rail") and the tap behind
// POST /api/pending/answer.
type Supervisor interface {
	Handler() voice.ToolHandler
	Settings() supervisor.Settings
	SetSettings(supervisor.Settings) error
	ShepherdInput(kind supervisor.InputKind, text string)
	AnswerTap(ctx context.Context, id string, option int) (voice.ToolResult, error)
	// ConversationStartedAt and ResetConversation back GET /api/state's
	// conversation_started_at and POST /api/conversation/reset ("New
	// conversation", docs/roles.md).
	ConversationStartedAt() time.Time
	ResetConversation(ctx context.Context, via string) (id string, at time.Time, err error)
	// Screen, WindowPrompt and WindowKeys back the agent window: GET
	// /api/agents/{pane_id}/screen and POST /api/agents/{pane_id}/input
	// (docs/ws-contract.md "Agent window: screen and input").
	Screen(ctx context.Context, paneID string, lines int) (supervisor.Screen, error)
	WindowPrompt(ctx context.Context, paneID, text string) (supervisor.WindowResult, error)
	WindowKeys(ctx context.Context, paneID string, keys []string) (supervisor.WindowResult, error)
}

// EventInjector is the Watcher's optional second input channel (POST
// /api/herdr/event), beside the herdr socket subscription.
type EventInjector interface {
	Inject(ev herdr.StatusEvent)
}

// Deps are the components the server exposes. Log and Store are
// required; the rest may be nil (the matching endpoints then answer 503).
type Deps struct {
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
	// means the brain is off and those answer 503 with BrainOffReason
	// ("--brain=off", "brain.enabled=false", "no Gemini key").
	Brain          *brain.Brain
	BrainOffReason string
	// BrainProviders lists the providers the picker offers, available
	// or not (with the reason: "no DEEPSEEK_API_KEY"); nil lists the
	// brain's registered providers, all available.
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
	// VoiceName is the dog's own name (config voice.name), served on
	// /api/state so the pages can label its side of the conversation.
	VoiceName string
	Version   string
	// ReloadPrompts, when set, backs POST /api/prompts/reload: it
	// rescans the prompt override directory and returns the resulting
	// file list for the response and the prompts.reloaded log line.
	ReloadPrompts func() []prompts.File
	// DevTestdataDir, when set, serves GET /testdata/<name>.wav from that
	// directory (the /voice page's file-source test mode). Empty (the
	// default) registers no route: this is a developer feature.
	DevTestdataDir string
	// DevBrainDir, when set with DevTestdataDir, serves GET
	// /testdata/brain/<name>.jsonl from that directory: the sample brain
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
	// (the iOS app's device registration, docs/push.md); nil answers 503.
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
	// docs/ws-contract.md "Protocol version and handshake").
	HandshakeTimeout time.Duration
	// MinProtocol is the oldest /ws client protocol served (default
	// wsproto.MinProtocolVersion); a lower one is refused with
	// status closed/protocol_too_old.
	MinProtocol int
	// Now overrides the clock (tests).
	Now func() time.Time

	// Token, when non-empty, is the bearer token (config web.token) every
	// request to /ws, /events and /api/* must present (docs/ws-contract.md
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

	// closing is closed by Serve on shutdown so long-lived handlers (SSE)
	// end before http.Server.Shutdown waits for them.
	closing chan struct{}

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
	s := &Server{deps: deps, log: deps.Logger, mux: http.NewServeMux(), closing: make(chan struct{})}
	s.hub = newHub(s)
	if err := s.routes(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) routes() error {
	s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/voice", http.StatusFound)
	})
	for _, name := range []string{"voice", "debug", "static"} {
		if err := s.static(name); err != nil {
			return err
		}
	}
	// GET /api/hello and POST /api/login stay public (docs/ws-contract.md
	// "Auth"): a client has to reach them to learn it found a sheepdog
	// daemon and to trade a token for the cookie in the first place.
	// Everything else that reads or acts on daemon state goes through
	// s.authFunc. /ws and /events also accept ?token=: a browser cannot
	// set a header on a WebSocket or EventSource request.
	s.mux.HandleFunc("GET /api/hello", s.handleHello)
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("GET /events", s.authFunc(s.handleEvents, true))
	s.mux.HandleFunc("GET /ws", s.authFunc(s.hub.handle, true))
	s.mux.HandleFunc("GET /api/state", s.authFunc(s.handleState, false))
	s.mux.HandleFunc("GET /api/agents", s.authFunc(s.handleAgents, false))
	s.mux.HandleFunc("GET /api/agents/{pane_id}/screen", s.authFunc(s.handleAgentScreen, false))
	s.mux.HandleFunc("POST /api/agents/{pane_id}/input", s.authFunc(s.handleAgentInput, false))
	s.mux.HandleFunc("GET /api/events", s.authFunc(s.handleEventsTail, false))
	s.mux.HandleFunc("GET /api/conversation", s.authFunc(s.handleConversation, false))
	s.mux.HandleFunc("POST /api/conversation/reset", s.authFunc(s.handleConversationReset, false))
	s.mux.HandleFunc("GET /api/voice/system_prompt", s.authFunc(s.handleSystemPrompt, false))
	s.mux.HandleFunc("GET /api/voice/providers", s.authFunc(s.handleVoiceProviders, false))
	s.mux.HandleFunc("PUT /api/voice/provider", s.authFunc(s.handleSetVoiceProvider, false))
	s.mux.HandleFunc("GET /api/settings", s.authFunc(s.handleGetSettings, false))
	s.mux.HandleFunc("PUT /api/settings", s.authFunc(s.handlePutSettings, false))
	s.mux.HandleFunc("POST /api/pending/cancel", s.authFunc(s.handleCancel, false))
	s.mux.HandleFunc("POST /api/pending/answer", s.authFunc(s.handlePendingAnswer, false))
	s.mux.HandleFunc("POST /api/inject/blocked", s.authFunc(s.handleInjectBlocked, false))
	s.mux.HandleFunc("POST /api/inject/done", s.authFunc(s.handleInjectDone, false))
	s.mux.HandleFunc("POST /api/inject/state", s.authFunc(s.handleInjectState, false))
	s.mux.HandleFunc("POST /api/inject/screen", s.authFunc(s.handleInjectScreen, false))
	s.mux.HandleFunc("POST /api/inject/remove", s.authFunc(s.handleInjectRemove, false))
	s.mux.HandleFunc("POST /api/inject/utterance", s.authFunc(s.handleUtterance, false))
	s.mux.HandleFunc("POST /api/herdr/event", s.authFunc(s.handleHerdrEvent, false))
	s.mux.HandleFunc("POST /api/prompts/reload", s.authFunc(s.handleReloadPrompts, false))
	s.mux.HandleFunc("GET /api/brain/transcript", s.authFunc(s.handleBrainTranscript, false))
	s.mux.HandleFunc("GET /api/brain/context", s.authFunc(s.handleBrainContext, false))
	s.mux.HandleFunc("GET /api/brain/system_prompt", s.authFunc(s.handleBrainSystemPrompt, false))
	s.mux.HandleFunc("GET /api/brain/models", s.authFunc(s.handleBrainModels, false))
	s.mux.HandleFunc("PUT /api/brain/model", s.authFunc(s.handleSetBrainModel, false))
	s.mux.HandleFunc("GET /api/context/brain", s.authFunc(s.handleContextBrain, false))
	s.mux.HandleFunc("GET /api/context/voice", s.authFunc(s.handleContextVoice, false))
	s.mux.HandleFunc("GET /api/context/timeline", s.authFunc(s.handleContextTimeline, false))
	s.mux.HandleFunc("POST /api/push/register", s.authFunc(s.handlePushRegister, false))
	s.mux.HandleFunc("DELETE /api/push/register", s.authFunc(s.handlePushUnregister, false))
	s.mux.HandleFunc("GET /api/push/status", s.authFunc(s.handlePushStatus, false))
	if s.deps.DevTestdataDir != "" {
		s.mux.HandleFunc("GET /testdata/{name}", func(w http.ResponseWriter, r *http.Request) {
			s.serveFixture(w, r, s.deps.DevTestdataDir, ".wav", "audio/wav")
		})
		if s.deps.DevBrainDir != "" {
			s.mux.HandleFunc("GET /testdata/brain/{name}", func(w http.ResponseWriter, r *http.Request) {
				s.serveFixture(w, r, s.deps.DevBrainDir, ".jsonl", "application/x-ndjson")
			})
		}
	}
	return nil
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

// static serves /<name> (index.html) and /<name>/* from the embedded FS.
func (s *Server) static(name string) error {
	sub, err := fs.Sub(static.FS, name)
	if err != nil {
		return fmt.Errorf("web: embedded %s: %w", name, err)
	}
	files := http.FileServerFS(sub)
	serve := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}
	s.mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/"
		serve(w, r)
	})
	s.mux.Handle("GET /"+name+"/", http.StripPrefix("/"+name, http.HandlerFunc(serve)))
	return nil
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
	srv := &http.Server{Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
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
