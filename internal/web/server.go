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
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	static "github.com/kccarlos/herdr-sheepdog/web"
)

// logSource is the source of every event this package logs.
const logSource = "web"

// Supervisor is what the web layer needs from the supervisor: the tool
// handler (cancel goes through it like every other tool).
type Supervisor interface {
	Handler() voice.ToolHandler
}

// EventInjector is the Watcher's second input channel (plugin shim).
type EventInjector interface {
	Inject(ev herdr.StatusEvent)
}

// Deps are the components the server exposes. Log and Store are
// required; the rest may be nil (the matching endpoints then answer 503).
type Deps struct {
	Log        *eventlog.Log
	Store      *state.Store
	Supervisor Supervisor
	Injector   *inject.Injector
	// Router is the text-only tool router used for simulated utterances
	// when Voice is nil.
	Router *inject.Router
	// Voice is the live voice session; nil means --voice=off.
	Voice voice.Session
	// Watcher receives plugin-shim events; nil means --herdr=mock.
	Watcher EventInjector
	// HerdrMode and VoiceMode are reported by /api/state ("socket"|"mock",
	// "gemini"|"off").
	HerdrMode, VoiceMode string
	Version              string

	// Heartbeat is the SSE keepalive period (default 15 s).
	Heartbeat time.Duration
	// OutQueue is the per-client outbound frame limit (default 256).
	OutQueue int
	// ToolTimeout bounds one tool call or injected chain (default 90 s).
	ToolTimeout time.Duration
	// Now overrides the clock (tests).
	Now func() time.Time
}

// Server is the HTTP server. Create with New, then either Handler() for
// tests or Listen + Serve.
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
	if deps.ToolTimeout <= 0 {
		deps.ToolTimeout = 90 * time.Second
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
	s := &Server{deps: deps, log: deps.Log, mux: http.NewServeMux(), closing: make(chan struct{})}
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
	for _, name := range []string{"voice", "debug"} {
		if err := s.static(name); err != nil {
			return err
		}
	}
	s.mux.HandleFunc("GET /events", s.handleEvents)
	s.mux.HandleFunc("GET /ws", s.hub.handle)
	s.mux.HandleFunc("GET /api/state", s.handleState)
	s.mux.HandleFunc("GET /api/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/events", s.handleEventsTail)
	s.mux.HandleFunc("POST /api/pending/cancel", s.handleCancel)
	s.mux.HandleFunc("POST /api/inject/blocked", s.handleInjectBlocked)
	s.mux.HandleFunc("POST /api/inject/done", s.handleInjectDone)
	s.mux.HandleFunc("POST /api/inject/state", s.handleInjectState)
	s.mux.HandleFunc("POST /api/inject/remove", s.handleInjectRemove)
	s.mux.HandleFunc("POST /api/inject/utterance", s.handleUtterance)
	s.mux.HandleFunc("POST /api/herdr/event", s.handleHerdrEvent)
	return nil
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

// Handler returns the routed handler (for httptest).
func (s *Server) Handler() http.Handler { return s.mux }

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
