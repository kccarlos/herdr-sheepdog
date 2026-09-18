package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// StateResponse is the body of GET /api/state.
type StateResponse struct {
	Agents        []state.Agent        `json:"agents"`
	PendingAction *state.PendingAction `json:"pending_action"`
	Session       state.Session        `json:"session"`
	Voice         VoiceInfo            `json:"voice"`
	Herdr         string               `json:"herdr"`
	Version       string               `json:"version"`
	Now           time.Time            `json:"now"`
}

// VoiceInfo is the daemon's view of the voice side.
type VoiceInfo struct {
	Mode            string `json:"mode"` // gemini | off
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	Muted           bool   `json:"muted"`
	ClientConnected bool   `json:"client_connected"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	snap := s.deps.Store.Snapshot()
	agents := s.deps.Store.List()
	status, reason, muted, client := s.hub.state()
	writeJSON(w, http.StatusOK, StateResponse{
		Agents:        agents,
		PendingAction: snap.PendingAction,
		Session:       snap.Session,
		Voice:         VoiceInfo{Mode: s.deps.VoiceMode, Status: status.String(), Reason: reason, Muted: muted, ClientConnected: client},
		Herdr:         s.deps.HerdrMode,
		Version:       s.deps.Version,
		Now:           s.deps.Now().UTC(),
	})
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.Store.List())
}

// toolCtx bounds a tool call or an injected chain and detaches it from
// the request so a client that gives up does not abort a half-done chain.
func (s *Server) toolCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.deps.ToolTimeout)
}

// ToolResponse is the body of the endpoints that run a tool.
type ToolResponse struct {
	Tool       string         `json:"tool"`
	Args       map[string]any `json:"args,omitempty"`
	Spoken     string         `json:"spoken"`
	Clarify    bool           `json:"clarify"`
	Scheduling string         `json:"scheduling"`
	Via        string         `json:"via"`
}

// runTool executes one tool call through the supervisor, logging
// voice.tool_call / voice.tool_result the way the voice session does
// (source "web", with `via` naming the debug path).
func (s *Server) runTool(ctx context.Context, call voice.ToolCall, via string) (voice.ToolResult, error) {
	if s.deps.Supervisor == nil {
		return voice.ToolResult{}, errors.New("no supervisor")
	}
	if call.Args == nil {
		call.Args = map[string]any{}
	}
	s.log.Log(logSource, "voice.tool_call", "", map[string]any{"id": call.ID, "name": call.Name, "args": call.Args, "via": via})
	start := s.deps.Now()
	res, err := s.deps.Supervisor.Handler()(ctx, call)
	payload := map[string]any{"id": call.ID, "name": call.Name, "spoken": res.Spoken, "clarify": res.Clarify, "scheduling": res.Scheduling.String(), "via": via, "dur_ms": s.deps.Now().Sub(start).Milliseconds()}
	if err != nil {
		payload["error"] = err.Error()
	}
	s.log.Log(logSource, "voice.tool_result", "", payload)
	return res, err
}

func toolResponse(call voice.ToolCall, res voice.ToolResult, via string) ToolResponse {
	return ToolResponse{Tool: call.Name, Args: call.Args, Spoken: res.Spoken, Clarify: res.Clarify, Scheduling: res.Scheduling.String(), Via: via}
}

// handleCancel runs the cancel tool (the debug page's Cancel button).
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.toolCtx()
	defer cancel()
	call := voice.ToolCall{ID: "web-cancel", Name: supervisor.ToolCancel}
	res, err := s.runTool(ctx, call, "api")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.afterCancel(res)
	writeJSON(w, http.StatusOK, toolResponse(call, res, "api"))
}

// injectResponse is the body of the inject endpoints.
type injectResponse struct {
	OK    bool        `json:"ok"`
	Agent state.Agent `json:"agent"`
	DurMS int64       `json:"dur_ms"`
}

func (s *Server) handleInjectBlocked(w http.ResponseWriter, r *http.Request) {
	if s.deps.Injector == nil {
		writeError(w, http.StatusServiceUnavailable, "injection not available")
		return
	}
	var req inject.BlockedRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	ctx, cancel := s.toolCtx()
	defer cancel()
	start := s.deps.Now()
	row, err := s.deps.Injector.Blocked(ctx, req)
	if err != nil {
		writeError(w, injectStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, injectResponse{OK: true, Agent: row, DurMS: s.deps.Now().Sub(start).Milliseconds()})
}

type paneRequest struct {
	PaneID string `json:"pane_id"`
	State  string `json:"state,omitempty"`
}

func (s *Server) handleInjectDone(w http.ResponseWriter, r *http.Request) {
	s.injectState(w, r, herdr.StateDone)
}

func (s *Server) handleInjectState(w http.ResponseWriter, r *http.Request) {
	s.injectState(w, r, "")
}

// injectState moves a synthetic pane to `to` (or to the body's `state`
// when `to` is empty).
func (s *Server) injectState(w http.ResponseWriter, r *http.Request, to herdr.State) {
	if s.deps.Injector == nil {
		writeError(w, http.StatusServiceUnavailable, "injection not available")
		return
	}
	var req paneRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if to == "" {
		to = herdr.State(strings.ToLower(strings.TrimSpace(req.State)))
	}
	ctx, cancel := s.toolCtx()
	defer cancel()
	start := s.deps.Now()
	row, err := s.deps.Injector.SetState(ctx, req.PaneID, to)
	if err != nil {
		writeError(w, injectStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, injectResponse{OK: true, Agent: row, DurMS: s.deps.Now().Sub(start).Milliseconds()})
}

func (s *Server) handleInjectRemove(w http.ResponseWriter, r *http.Request) {
	if s.deps.Injector == nil {
		writeError(w, http.StatusServiceUnavailable, "injection not available")
		return
	}
	var req paneRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	ctx, cancel := s.toolCtx()
	defer cancel()
	if err := s.deps.Injector.Remove(ctx, req.PaneID); err != nil {
		writeError(w, injectStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pane_id": req.PaneID})
}

func injectStatus(err error) int {
	if errors.Is(err, inject.ErrNotSynthetic) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

// utteranceRequest is the body of POST /api/inject/utterance.
type utteranceRequest struct {
	Text string `json:"text"`
}

// handleUtterance simulates the user saying something. With a voice
// session the text goes to the model as a realtime input (the model then
// decides which tool to call); without one the DEBUG-ONLY text router
// maps trivial phrases straight onto tool calls.
func (s *Server) handleUtterance(w http.ResponseWriter, r *http.Request) {
	var req utteranceRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	if s.deps.Voice != nil {
		s.log.Log(logSource, "web.utterance", "", map[string]any{"text": text, "via": "voice"})
		if err := s.deps.Voice.SendText(text); err != nil {
			writeError(w, http.StatusBadGateway, "voice: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "via": "voice", "text": text})
		return
	}
	ctx, cancel := s.toolCtx()
	defer cancel()
	call, res, err := s.routeUtterance(ctx, text)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toolResponse(call, res, "text_router"))
}

// errUnrouted is returned when the text router does not understand.
var errUnrouted = errors.New("text router (debug path, --voice=off) does not understand this; try yes / no / stop / what's going on / tell … / ask …")

// routeUtterance runs text through the text-only router and the tool it
// picks. Callers hold no locks.
func (s *Server) routeUtterance(ctx context.Context, text string) (voice.ToolCall, voice.ToolResult, error) {
	if s.deps.Router == nil {
		return voice.ToolCall{}, voice.ToolResult{}, errors.New("no text router")
	}
	s.log.Log(logSource, "web.utterance", "", map[string]any{"text": text, "via": "text_router"})
	call, ok := s.deps.Router.Route(text)
	if !ok {
		s.log.Log(logSource, "web.utterance_unrouted", "", map[string]any{"text": text})
		return voice.ToolCall{}, voice.ToolResult{}, errUnrouted
	}
	res, err := s.runTool(ctx, call, "text_router")
	if err != nil {
		return call, res, err
	}
	if call.Name == supervisor.ToolCancel {
		s.hub.afterCancel(res)
	}
	return call, res, nil
}

// handleHerdrEvent is the plugin-shim channel: the body is the
// HERDR_PLUGIN_EVENT_JSON object. Status events are injected into the
// Watcher; lifecycle events are logged and acknowledged (the Watcher
// learns about new panes from its own fleet subscription and snapshot).
func (s *Server) handleHerdrEvent(w http.ResponseWriter, r *http.Request) {
	if s.deps.Watcher == nil {
		writeError(w, http.StatusServiceUnavailable, "no herdr watcher (mock mode)")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	status, fleet, err := herdr.ParseEvent(body, s.deps.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case status != nil:
		status.Source = "http"
		s.deps.Watcher.Inject(*status)
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "injected": "status", "pane_id": status.PaneID, "state": status.State})
	case fleet != nil:
		s.log.Log(logSource, "web.herdr_event", fleet.PaneID, fleet)
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "injected": "none", "kind": fleet.Kind})
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "injected": "none"})
	}
}
