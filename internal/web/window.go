package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// ScreenResponse is the body of GET /api/agents/{pane_id}/screen. With
// ?hash= equal to the current hash, Unchanged is set and Text is empty
// (omitted).
type ScreenResponse struct {
	supervisor.Screen
	Text      string `json:"text,omitempty"`
	Unchanged bool   `json:"unchanged"`
}

// handleAgentScreen answers a tracked agent's current window
// (docs/dev/ws-contract.md "Agent window: screen and input").
func (s *Server) handleAgentScreen(w http.ResponseWriter, r *http.Request) {
	if s.deps.Supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "no supervisor")
		return
	}
	lines := 0
	if v := r.URL.Query().Get("lines"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "lines must be a positive number")
			return
		}
		lines = n
	}
	scr, err := s.deps.Supervisor.Screen(r.Context(), r.PathValue("pane_id"), lines)
	if err != nil {
		writeError(w, windowStatus(err), err.Error())
		return
	}
	resp := ScreenResponse{Screen: scr, Text: scr.Text}
	if h := r.URL.Query().Get("hash"); h != "" && h == scr.Hash {
		resp.Text, resp.Unchanged = "", true
	}
	writeJSON(w, http.StatusOK, resp)
}

// agentInputRequest is the body of POST /api/agents/{pane_id}/input:
// exactly one of Text (with Submit, default true) or Keys.
type agentInputRequest struct {
	Text   *string  `json:"text"`
	Submit *bool    `json:"submit"`
	Keys   []string `json:"keys"`
}

// handleAgentInput is the user operating a tracked agent's window: a
// prompt or allow-listed keys, logged as web.agent_input (a prompt's
// length, never its text). It does not pass the one rail: the tap is his.
func (s *Server) handleAgentInput(w http.ResponseWriter, r *http.Request) {
	if s.deps.Supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "no supervisor")
		return
	}
	var req agentInputRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	pane := r.PathValue("pane_id")
	ctx, cancel := s.toolCtx()
	defer cancel()
	var (
		res supervisor.WindowResult
		err error
	)
	switch {
	case req.Text != nil && req.Keys != nil, req.Text == nil && req.Keys == nil:
		writeError(w, http.StatusBadRequest, "send either text or keys")
		return
	case req.Text != nil:
		if req.Submit != nil && !*req.Submit {
			writeError(w, http.StatusBadRequest, "submit:false is not supported; a prompt is always submitted")
			return
		}
		s.log.Log(logSource, "web.agent_input", pane, map[string]any{"kind": "prompt", "len": len(strings.TrimSpace(*req.Text))})
		res, err = s.deps.Supervisor.WindowPrompt(ctx, pane, *req.Text)
	default:
		s.log.Log(logSource, "web.agent_input", pane, map[string]any{"kind": "keys", "keys": req.Keys})
		res, err = s.deps.Supervisor.WindowKeys(ctx, pane, req.Keys)
	}
	if err != nil {
		msg := err.Error()
		if res.Spoken != "" {
			msg = res.Spoken
		}
		writeError(w, windowStatus(err), msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kind": res.Kind, "spoken": res.Spoken})
}

// windowStatus maps a window error to its HTTP status.
func windowStatus(err error) int {
	switch {
	case errors.Is(err, supervisor.ErrNoSuchAgent):
		return http.StatusNotFound
	case errors.Is(err, supervisor.ErrWindowInput), errors.Is(err, supervisor.ErrNoSuchOption):
		return http.StatusBadRequest
	case errors.Is(err, herdr.ErrAgentBlocked), errors.Is(err, supervisor.ErrPendingGone):
		return http.StatusConflict
	case errors.Is(err, supervisor.ErrNotSent):
		return http.StatusBadGateway
	case errors.Is(err, supervisor.ErrWindowRefused):
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}
