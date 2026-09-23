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
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// StateResponse is the body of GET /api/state.
type StateResponse struct {
	Agents        []state.Agent     `json:"agents"`
	PendingAction *PendingView      `json:"pending_action"`
	Session       state.Session     `json:"session"`
	Voice         VoiceInfo         `json:"voice"`
	Settings      *SettingsResponse `json:"settings,omitempty"`
	// Brain is the supervisor brain's provider and current model; omitted
	// when the brain is off.
	Brain   *BrainInfo `json:"brain,omitempty"`
	Herdr   string     `json:"herdr"`
	Version string     `json:"version"`
	Now     time.Time  `json:"now"`
	// VoiceProvider and VoiceModel are the voice model behind /ws
	// ("off" and "" with --voice=off).
	VoiceProvider string `json:"voice_provider"`
	VoiceModel    string `json:"voice_model"`
}

// SettingsResponse is the body of GET/PUT /api/settings (and the
// settings field of /api/state).
type SettingsResponse struct {
	Chattiness supervisor.Chattiness   `json:"chattiness"`
	Levels     []supervisor.Chattiness `json:"levels"`
}

func (s *Server) settings() *SettingsResponse {
	if s.deps.Supervisor == nil {
		return nil
	}
	return &SettingsResponse{Chattiness: s.deps.Supervisor.Settings().Chattiness, Levels: supervisor.Levels}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st := s.settings()
	if st == nil {
		writeError(w, http.StatusServiceUnavailable, "no supervisor")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handlePutSettings changes the runtime settings ({"chattiness": …});
// the supervisor validates, persists and logs the change.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "no supervisor")
		return
	}
	var req supervisor.Settings
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if err := s.deps.Supervisor.SetSettings(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.settings())
}

// VoiceInfo is the daemon's view of the voice side.
type VoiceInfo struct {
	Mode string `json:"mode"` // gemini | off
	// Name is what the dog calls itself (config voice.name), so the
	// /debug views can label its side of the conversation.
	Name            string `json:"name,omitempty"`
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	Muted           bool   `json:"muted"`
	Stalled         bool   `json:"stalled"`
	ClientConnected bool   `json:"client_connected"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	snap := s.deps.Store.Snapshot()
	agents := s.deps.Store.List()
	hs := s.hub.state()
	writeJSON(w, http.StatusOK, StateResponse{
		Agents:        agents,
		PendingAction: s.pendingView(snap.PendingAction),
		Session:       snap.Session,
		Voice:         VoiceInfo{Mode: s.deps.VoiceMode, Name: s.deps.VoiceName, Status: hs.status.String(), Reason: hs.reason, Muted: hs.muted, Stalled: hs.stalled, ClientConnected: hs.client},
		Settings:      s.settings(),
		Brain:         s.brainInfo(),
		Herdr:         s.deps.HerdrMode,
		Version:       s.deps.Version,
		Now:           s.deps.Now().UTC(),
		VoiceProvider: s.voiceProvider(),
		VoiceModel:    s.voiceModel(),
	})
}

// PendingView is /api/state's pending action: the stored action plus
// what a page needs to show it and answer it by tap — the agent's alias,
// the summary, and each option with its 1-based index (what
// POST /api/pending/answer takes) and whether it is the prompt's
// default. question and command come from the action itself.
type PendingView struct {
	state.PendingAction
	Agent   string                     `json:"agent,omitempty"`
	Summary string                     `json:"summary,omitempty"`
	Options []supervisor.PendingOption `json:"options,omitempty"`
}

// pendingView builds the view of p (nil when there is none).
func (s *Server) pendingView(p *state.PendingAction) *PendingView {
	if p == nil {
		return nil
	}
	v := &PendingView{PendingAction: *p, Summary: p.SpokenSummary, Options: supervisor.PendingOptions(*p)}
	if row, ok := s.deps.Store.Get(p.PaneID); ok {
		v.Agent = row.Alias
	}
	return v
}

// pendingAnswerRequest is the body of POST /api/pending/answer.
type pendingAnswerRequest struct {
	ID     string `json:"id"`
	Option int    `json:"option"`
}

// handlePendingAnswer answers the open question with the option the
// shepherd tapped: {"id", "option"} (1-based, the index /api/state and
// pending.created list). It is a shepherd input like a spoken answer and
// passes the same rail; a question that is no longer open is 409, keys
// that could not be sent 502.
func (s *Server) handlePendingAnswer(w http.ResponseWriter, r *http.Request) {
	if s.deps.Supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "no supervisor")
		return
	}
	var req pendingAnswerRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.ID) == "" || req.Option < 1 {
		writeError(w, http.StatusBadRequest, "id and a 1-based option are required")
		return
	}
	ctx, cancel := s.toolCtx()
	defer cancel()
	res, err := s.deps.Supervisor.AnswerTap(ctx, req.ID, req.Option)
	switch {
	case errors.Is(err, supervisor.ErrPendingGone):
		writeError(w, http.StatusConflict, supervisor.ErrPendingGone.Error())
		return
	case errors.Is(err, supervisor.ErrNoSuchOption):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, supervisor.ErrNotSent):
		writeError(w, http.StatusBadGateway, res.Spoken)
		return
	case err != nil:
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "spoken": res.Spoken})
}

// SystemPromptResponse is the body of GET /api/voice/system_prompt.
type SystemPromptResponse struct {
	Text    string   `json:"text"`
	Aliases []string `json:"aliases"`
	Mode    string   `json:"mode"`
}

// handleSystemPrompt answers the current voice system prompt (404 when
// the daemon was built without one). Computed on every request (not
// cached) so a prompts.Loader reload shows up immediately, even though
// the running voice session only picks it up on its next reconnect.
func (s *Server) handleSystemPrompt(w http.ResponseWriter, r *http.Request) {
	if s.deps.SystemPrompt == nil {
		writeError(w, http.StatusNotFound, "no system prompt configured")
		return
	}
	aliases := s.deps.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	writeJSON(w, http.StatusOK, SystemPromptResponse{Text: s.deps.SystemPrompt(), Aliases: aliases, Mode: s.deps.VoiceMode})
}

// ReloadPromptsResponse is the body of POST /api/prompts/reload.
type ReloadPromptsResponse struct {
	Files []prompts.File `json:"files"`
}

// handleReloadPrompts rescans the prompt override directory (mirrors
// SIGHUP) and reports the resulting file list.
func (s *Server) handleReloadPrompts(w http.ResponseWriter, r *http.Request) {
	if s.deps.ReloadPrompts == nil {
		writeError(w, http.StatusServiceUnavailable, "no prompt workspace configured")
		return
	}
	files := s.deps.ReloadPrompts()
	s.log.Log(logSource, "prompts.reloaded", "", map[string]any{"files": files, "via": "api"})
	writeJSON(w, http.StatusOK, ReloadPromptsResponse{Files: files})
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
	Pending    bool           `json:"pending,omitempty"`
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
	if res.Pending {
		// An answer whose real result follows on its own (an
		// investigation): the Conversation view links it to the push.
		payload["pending"] = true
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	s.log.Log(logSource, "voice.tool_result", "", payload)
	return res, err
}

func toolResponse(call voice.ToolCall, res voice.ToolResult, via string) ToolResponse {
	return ToolResponse{Tool: call.Name, Args: call.Args, Spoken: res.Spoken, Clarify: res.Clarify, Pending: res.Pending, Scheduling: res.Scheduling.String(), Via: via}
}

// handleCancel runs the stop tool (the debug page's Cancel button; the
// route keeps its name, the tool behind it is the stop procedure).
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.toolCtx()
	defer cancel()
	call := voice.ToolCall{ID: "web-stop", Name: supervisor.ToolStop}
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
	// Screen, when given, replaces the pane text before the transition
	// (what the supervisor reads on a done).
	Screen string `json:"screen,omitempty"`
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
	row, err := s.deps.Injector.SetState(ctx, req.PaneID, to, req.Screen)
	if err != nil {
		writeError(w, injectStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, injectResponse{OK: true, Agent: row, DurMS: s.deps.Now().Sub(start).Milliseconds()})
}

// handleInjectScreen replaces a synthetic pane's screen text without
// moving its state (the counterpart of the mock herdr's own screen
// update): what the post-answer poll and the reconciler see next,
// without a transition to trigger them itself.
func (s *Server) handleInjectScreen(w http.ResponseWriter, r *http.Request) {
	if s.deps.Injector == nil {
		writeError(w, http.StatusServiceUnavailable, "injection not available")
		return
	}
	var req paneRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	row, err := s.deps.Injector.Screen(req.PaneID, req.Screen)
	if err != nil {
		writeError(w, injectStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, injectResponse{OK: true, Agent: row})
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
		s.typed(text)
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

// typed records text as something the shepherd typed, before it reaches
// the voice model or the text router: the one rail reads these inputs,
// never the tool argument the model relays (docs/roles.md "The one
// rail").
func (s *Server) typed(text string) {
	if s.deps.Supervisor != nil {
		s.deps.Supervisor.ShepherdInput(supervisor.InputTyped, text)
	}
}

// errUnrouted is returned when the text router has nothing to relay
// (only an empty utterance, since D1-revised).
var errUnrouted = errors.New("text router (debug path, --voice=off): nothing to relay")

// routeUtterance runs text through the text-only router and the tool it
// picks. Callers hold no locks.
func (s *Server) routeUtterance(ctx context.Context, text string) (voice.ToolCall, voice.ToolResult, error) {
	if s.deps.Router == nil {
		return voice.ToolCall{}, voice.ToolResult{}, errors.New("no text router")
	}
	s.log.Log(logSource, "web.utterance", "", map[string]any{"text": text, "via": "text_router"})
	s.typed(text)
	call, ok := s.deps.Router.Route(text)
	if !ok {
		s.log.Log(logSource, "web.utterance_unrouted", "", map[string]any{"text": text})
		return voice.ToolCall{}, voice.ToolResult{}, errUnrouted
	}
	res, err := s.runTool(ctx, call, "text_router")
	if err != nil {
		return call, res, err
	}
	if call.Name == supervisor.ToolStop {
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
