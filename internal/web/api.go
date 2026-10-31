package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/lang"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/update"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// StateResponse is the body of GET /api/state.
type StateResponse struct {
	Agents        []state.Agent `json:"agents"`
	PendingAction *PendingView  `json:"pending_action"`
	// Questions are every open question, oldest first: one per blocked
	// agent whose prompt was read, the voice's pending_action among
	// them. Each has its options for POST /api/pending/answer.
	Questions []PendingView     `json:"questions"`
	Session   state.Session     `json:"session"`
	Voice     VoiceInfo         `json:"voice"`
	Settings  *SettingsResponse `json:"settings,omitempty"`
	// Brain is the orchestrator's provider and current model; omitted
	// when the orchestrator is off.
	Brain   *BrainInfo `json:"brain,omitempty"`
	Herdr   string     `json:"herdr"`
	Version string     `json:"version"`
	Now     time.Time  `json:"now"`
	// VoiceProvider and VoiceModel are the voice model behind /ws
	// ("off" and "" with --voice=off).
	VoiceProvider string `json:"voice_provider"`
	VoiceModel    string `json:"voice_model"`
	// ConversationStartedAt is when the current conversation began: the
	// last "New conversation" reset, or daemon start.
	ConversationStartedAt time.Time `json:"conversation_started_at"`
	// Update is the update notice: the newest release, whether it is
	// newer, how to upgrade, and whether an upgrade is installed but not
	// running yet. Omitted when the daemon does not track updates. The
	// voice never speaks it.
	Update *update.Status `json:"update,omitempty"`
}

// SettingsResponse is the body of GET/PUT /api/settings (and the
// settings field of /api/state).
type SettingsResponse struct {
	Chattiness supervisor.Chattiness   `json:"chattiness"`
	Levels     []supervisor.Chattiness `json:"levels"`
	// Languages is the user's language preference, most preferred
	// first (codes from LanguageOptions); [] when none is set.
	Languages []string `json:"languages"`
	// LanguageOptions is the catalog a client offers (internal/lang):
	// languages both voice providers support, each with the providers.
	LanguageOptions []lang.Language `json:"language_options"`
}

func (s *Server) settings() *SettingsResponse {
	if s.deps.Supervisor == nil {
		return nil
	}
	cur := s.deps.Supervisor.Settings()
	langs := cur.Languages
	if langs == nil {
		langs = []string{}
	}
	return &SettingsResponse{Chattiness: cur.Chattiness, Levels: supervisor.Levels, Languages: langs, LanguageOptions: lang.Catalog}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st := s.settings()
	if st == nil {
		writeError(w, http.StatusServiceUnavailable, "no supervisor")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handlePutSettings changes the runtime settings ({"chattiness": …},
// {"languages": [...]}, or both; a field left out is unchanged); the
// supervisor validates, persists and logs the change.
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
	// Name is what the voice calls itself (config voice.name), so the
	// /debug views can label its side of the conversation.
	Name    string `json:"name,omitempty"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Muted   bool   `json:"muted"`
	Stalled bool   `json:"stalled"`
	// Client is the /ws holder from its accepted client-ready handshake;
	// omitted until the handshake carries a client and after it disconnects.
	Client *VoiceClientInfo `json:"client,omitempty"`
	// Speaking is true while the model's speech is reaching the user.
	Speaking        bool `json:"speaking"`
	ClientConnected bool `json:"client_connected"`
	// Problem says why the voice is not connected, in words for a
	// screen ("Setup needed: no Gemini API key yet…", "Gemini (AI
	// Studio) rejected the API key… Retrying in 20s."); empty when
	// nothing is wrong.
	Problem string `json:"problem,omitempty"`
	// SetupNeeded: the voice has no credentials (run sheepdog setup).
	SetupNeeded bool `json:"setup_needed,omitempty"`
}

// VoiceClientInfo identifies the client holding /ws.
type VoiceClientInfo struct {
	Name     string    `json:"name"`
	Platform string    `json:"platform"`
	Since    time.Time `json:"since"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	snap := s.deps.Store.Snapshot()
	agents := s.deps.Store.List()
	hs := s.hub.state()
	writeJSON(w, http.StatusOK, StateResponse{
		Agents:                agents,
		PendingAction:         s.pendingView(snap.PendingAction),
		Questions:             s.questionViews(),
		Session:               snap.Session,
		Voice:                 s.voiceInfo(hs),
		Settings:              s.settings(),
		Brain:                 s.brainInfo(),
		Herdr:                 s.deps.HerdrMode,
		Version:               s.deps.Version,
		Now:                   s.deps.Now().UTC(),
		VoiceProvider:         s.voiceProvider(),
		VoiceModel:            s.voiceModel(),
		ConversationStartedAt: s.conversationStartedAt(),
		Update:                s.updateStatus(),
	})
}

// MuteRequest is the body of POST /api/voice/mute.
type MuteRequest struct {
	On *bool `json:"on"`
}

// MuteResponse is the body of POST /api/voice/mute.
type MuteResponse struct {
	Muted bool `json:"muted"`
}

// handleVoiceMute changes the voice mute state through the same hub path
// as the /ws mute control message.
func (s *Server) handleVoiceMute(w http.ResponseWriter, r *http.Request) {
	if s.deps.Voice == nil {
		writeError(w, http.StatusServiceUnavailable, "the voice is off (--voice=off)")
		return
	}
	var req MuteRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if req.On == nil {
		writeError(w, http.StatusBadRequest, "on must be a boolean")
		return
	}
	s.hub.setMuted(*req.On, "http")
	writeJSON(w, http.StatusOK, MuteResponse{Muted: s.hub.state().muted})
}

// PTTRequest is the body of POST /api/voice/ptt.
type PTTRequest struct {
	On *bool `json:"on"`
}

// VoiceCommandResponse is the body of POST /api/voice/stop and /ptt.
type VoiceCommandResponse struct {
	OK bool `json:"ok"`
}

// handleVoiceStop cuts the current speech: the stop tool and flush the
// /ws cancel message runs, then a voice-command stop to the holder so a
// client that keeps its own playback or capture state follows. The
// daemon does the cutting itself, so the client message is a notice.
func (s *Server) handleVoiceStop(w http.ResponseWriter, r *http.Request) {
	if s.deps.Voice == nil {
		writeError(w, http.StatusServiceUnavailable, "the voice is off (--voice=off)")
		return
	}
	if s.hub.current() == nil {
		writeError(w, http.StatusConflict, "no voice client")
		return
	}
	if err := s.hub.cancel("http"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.send(wsproto.VoiceStop())
	writeJSON(w, http.StatusOK, VoiceCommandResponse{OK: true})
}

// handleVoicePTT tells the client holding /ws to open or close its
// microphone (hold-to-talk). The daemon has no state to change: the
// client's own mute and speech messages follow as usual.
func (s *Server) handleVoicePTT(w http.ResponseWriter, r *http.Request) {
	if s.deps.Voice == nil {
		writeError(w, http.StatusServiceUnavailable, "the voice is off (--voice=off)")
		return
	}
	var req PTTRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if req.On == nil {
		writeError(w, http.StatusBadRequest, "on must be a boolean")
		return
	}
	if s.hub.current() == nil {
		writeError(w, http.StatusConflict, "no voice client")
		return
	}
	s.hub.send(wsproto.VoicePTT(*req.On))
	s.log.Log(logSource, "web.client.ptt", "", map[string]any{"on": *req.On})
	writeJSON(w, http.StatusOK, VoiceCommandResponse{OK: true})
}

// updateStatus is /api/state's update object (nil without a tracker).
func (s *Server) updateStatus() *update.Status {
	if s.deps.Update == nil {
		return nil
	}
	st := s.deps.Update()
	return &st
}

// VoiceClientConnected reports whether a browser or phone holds the voice
// connection now.
func (s *Server) VoiceClientConnected() bool { return s.hub.state().client }

// conversationStartedAt is StateResponse's conversation_started_at (the
// zero time when there is no supervisor).
func (s *Server) conversationStartedAt() time.Time {
	if s.deps.Supervisor == nil {
		return time.Time{}
	}
	return s.deps.Supervisor.ConversationStartedAt()
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

// questionViews lists the open questions for /api/state.
func (s *Server) questionViews() []PendingView {
	out := []PendingView{}
	for _, q := range s.deps.Store.Questions() {
		out = append(out, *s.pendingView(&q))
	}
	return out
}

// pendingAnswerRequest is the body of POST /api/pending/answer.
type pendingAnswerRequest struct {
	ID     string `json:"id"`
	Option int    `json:"option"`
}

// handlePendingAnswer answers the open question with the option the
// user tapped: {"id", "option"} (1-based, the index /api/state and
// pending.created list). It is a user input like a spoken answer and
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
// Aliases are the agent names the prompt is rendered with: the current
// spoken names (Deps.Names), else the configured aliases.
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
	if s.deps.Names != nil {
		// The current spoken names, as the prompt text itself has them:
		// the start-up alias list goes stale as agents come, go and are
		// renamed.
		if n := s.deps.Names(); len(n) > 0 {
			aliases = n
		}
	}
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
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args,omitempty"`
	Spoken  string         `json:"spoken"`
	Clarify bool           `json:"clarify"`
	Pending bool           `json:"pending,omitempty"`
	// Options are the choices read out with the answer (an open
	// prompt's), as the voice model gets them.
	Options    []string `json:"options,omitempty"`
	Scheduling string   `json:"scheduling"`
	Via        string   `json:"via"`
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
	return ToolResponse{Tool: call.Name, Args: call.Args, Spoken: res.Spoken, Clarify: res.Clarify, Pending: res.Pending, Options: res.Options, Scheduling: res.Scheduling.String(), Via: via}
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

// handleInjectPane registers a synthetic pane in any state (POST
// /api/inject/pane): the voice bench's scripted workers.
func (s *Server) handleInjectPane(w http.ResponseWriter, r *http.Request) {
	if s.deps.Injector == nil {
		writeError(w, http.StatusServiceUnavailable, "injection not available")
		return
	}
	var req inject.PaneRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	ctx, cancel := s.toolCtx()
	defer cancel()
	start := s.deps.Now()
	row, err := s.deps.Injector.Pane(ctx, req)
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

// utteranceRequest is the body of POST /api/utterance.
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

// TurnRequest is the body of POST /api/turn.
type TurnRequest struct {
	Text string `json:"text"`
}

// TurnResponse is the body of the POST /api/turn answer: the
// orchestrator's text reply, the turn's id on its brain.* events and
// whether the turn acted (sent something to a worker or recorded something).
type TurnResponse struct {
	Reply  string `json:"reply"`
	TurnID string `json:"turn_id"`
	Acted  bool   `json:"acted"`
}

// handleTurn sends a typed turn straight to the orchestrator and answers
// with its text reply: the turn a spoken to_supervisor runs, so tools,
// approvals and history work the same, but the reply is not spoken. The
// voice only gets a silent context line (supervisor.TypedTurn).
func (s *Server) handleTurn(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.brainOrUnavailable(w); !ok {
		return
	}
	if s.deps.Supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "no supervisor")
		return
	}
	var req TurnRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	s.log.Log(logSource, "web.utterance", "", map[string]any{"text": text, "via": "typed"})
	s.typed(text)
	ctx, cancel := s.toolCtx()
	defer cancel()
	start := s.deps.Now()
	reply, err := s.deps.Supervisor.TypedTurn(ctx)
	if errors.Is(err, supervisor.ErrNoTypedInput) {
		// A spoken turn claimed the typed words first and answers them.
		writeError(w, http.StatusConflict, "a spoken turn is already answering this")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The reply is a "dog" item with via typed in GET /api/conversation.
	s.log.Log(logSource, "voice.tool_result", "", map[string]any{"id": reply.TurnID, "name": voice.ToolToSupervisor, "spoken": reply.Text, "via": "typed", "dur_ms": s.deps.Now().Sub(start).Milliseconds()})
	writeJSON(w, http.StatusOK, TurnResponse{Reply: reply.Text, TurnID: reply.TurnID, Acted: reply.Acted})
}

// typed records text as something the user typed, before it reaches
// the voice model or the text router: the one rail reads these inputs,
// never the tool argument the model relays (docs/dev/roles.md "The one
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
	rc := s.deps.RouterContext
	rc.Item(ctxcap.VoiceItem{Kind: ctxcap.VKindUserText, Class: ctxcap.ClassShepherd, Role: "user", Label: "user typed", Text: text})
	call, ok := s.deps.Router.Route(text)
	if !ok {
		s.log.Log(logSource, "web.utterance_unrouted", "", map[string]any{"text": text})
		return voice.ToolCall{}, voice.ToolResult{}, errUnrouted
	}
	link := ctxcap.CallLink(call.ID)
	rc.Claim(link)
	rc.Item(ctxcap.VoiceItem{Kind: ctxcap.VKindToolCall, Class: ctxcap.ClassToolCall, Role: "model", Label: "tool call " + call.Name + " (text router)",
		Text: "→ " + call.Name + "(" + string(ctxcap.Raw(call.Args)) + ")", Links: []string{link}, Raw: ctxcap.Raw(map[string]any{"id": call.ID, "name": call.Name, "args": call.Args})})
	if s.deps.HeardEcho != nil {
		s.deps.HeardEcho(voice.EventToolCall{Call: call})
	}
	res, err := s.runTool(ctx, call, "text_router")
	if s.deps.HeardEcho != nil && strings.TrimSpace(res.Spoken) != "" {
		s.deps.HeardEcho(voice.EventTranscript{Dir: voice.DirOut, Text: res.Spoken, Final: true})
		s.deps.HeardEcho(voice.EventTurnComplete{})
	}
	resp := map[string]any{"spoken": res.Spoken, "scheduling": res.Scheduling.SchedulingName()}
	if res.Detail != "" {
		resp["detail"] = res.Detail
	}
	if len(res.Options) > 0 {
		resp["options"] = res.Options
	}
	if err != nil {
		resp["error"] = err.Error()
	}
	rc.Item(ctxcap.VoiceItem{Kind: ctxcap.VKindToolResponse, Class: ctxcap.ClassToolResult, Role: "user", Label: "tool response " + call.Name + " (" + res.Scheduling.SchedulingName() + ")",
		Text: "← " + call.Name + ": " + string(ctxcap.Raw(resp)), Links: []string{link}, Raw: ctxcap.Raw(map[string]any{"id": call.ID, "name": call.Name, "response": resp})})
	if err != nil {
		return call, res, err
	}
	if call.Name == supervisor.ToolStop {
		s.hub.afterCancel(res)
	}
	return call, res, nil
}

// handleHerdrEvent is an optional second way into the Watcher besides
// the herdr socket subscription (another process or a test can feed it;
// herdr itself needs no configuring for it): the body is a herdr event
// object in the HERDR_PLUGIN_EVENT_JSON shape. Status events are
// injected into the Watcher, logged with source "http"; lifecycle events
// are logged and acknowledged (the Watcher learns about new panes from
// its own fleet subscription and snapshot).
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

// voiceInfo is /api/state's voice object.
func (s *Server) voiceInfo(hs hubState) VoiceInfo {
	v := VoiceInfo{Mode: s.deps.VoiceMode, Name: s.deps.VoiceName, Status: hs.status.String(), Reason: hs.reason, Muted: hs.muted, Stalled: hs.stalled, Client: hs.clientInfo, ClientConnected: hs.client}
	if s.deps.Voice != nil {
		v.Speaking = s.deps.Voice.Speaking()
	}
	if s.deps.VoiceIssue != nil {
		v.Problem = s.deps.VoiceIssue()
	}
	if s.deps.SetupNeeded != nil {
		v.SetupNeeded = s.deps.SetupNeeded()
	}
	return v
}
