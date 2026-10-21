package web

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
)

// The user's own agent actions from the app (docs/dev/ws-contract.md
// "Agent actions", feature agent-actions): New agent, Rename, Stop and
// Close. A tap is the user's, so these do not pass the words rail
// (docs/dev/roles.md "The user's own actions"); the supervisor keeps the
// rails that are not about who asked. Each is logged as web.agent_action
// with metadata only (never a prompt's text).

func (s *Server) agentActions(w http.ResponseWriter) (AgentActions, bool) {
	a, ok := s.deps.Supervisor.(AgentActions)
	if !ok || s.deps.Supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "agent actions are not available here")
		return nil, false
	}
	return a, true
}

// actionStatus maps an action's error to its HTTP status.
func actionStatus(err error) int {
	switch {
	case errors.Is(err, supervisor.ErrUserAction):
		return http.StatusBadRequest
	case errors.Is(err, brain.ErrNotPermitted):
		return http.StatusForbidden
	default:
		return windowStatus(err)
	}
}

// handleProjects is GET /api/projects: the New agent sheet's kinds and
// directories.
func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agentActions(w)
	if !ok {
		return
	}
	opts, err := a.NewAgentOptions(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

type newAgentRequest struct {
	Kind     string `json:"kind"`
	Dir      string `json:"dir"`
	Worktree string `json:"worktree"`
	Text     string `json:"text"`
}

// handleNewAgent is POST /api/agents.
func (s *Server) handleNewAgent(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agentActions(w)
	if !ok {
		return
	}
	var req newAgentRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Dir) == "" {
		writeError(w, http.StatusBadRequest, "dir is required")
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	ctx, cancel := s.toolCtx()
	defer cancel()
	res, err := a.UserNewAgent(ctx, supervisor.NewAgentRequest{Kind: req.Kind, Dir: req.Dir, Worktree: strings.TrimSpace(req.Worktree), Text: req.Text})
	fields := map[string]any{"action": "new", "kind": req.Kind, "dir": filepath.Base(req.Dir), "worktree": req.Worktree != "", "text_len": len(req.Text)}
	if err != nil {
		fields["error"] = err.Error()
		var se *supervisor.StartError
		if errors.As(err, &se) {
			// The pane is left at its shell prompt: the app offers
			// "Open the pane".
			s.log.Log(logSource, "web.agent_action", se.PaneID, fields)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "pane_id": se.PaneID})
			return
		}
		s.log.Log(logSource, "web.agent_action", "", fields)
		writeError(w, actionStatus(err), err.Error())
		return
	}
	s.log.Log(logSource, "web.agent_action", res.PaneID, fields)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pane_id": res.PaneID, "spoken": res.Spoken, "blocked": res.Blocked})
}

// handleAgentRename is POST /api/agents/{pane_id}/rename {"name"}.
func (s *Server) handleAgentRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	s.agentAction(w, r, "rename", func(a AgentActions, pane string) (string, error) {
		ctx, cancel := s.toolCtx()
		defer cancel()
		return a.UserRename(ctx, pane, req.Name)
	})
}

// handleAgentInterrupt is POST /api/agents/{pane_id}/interrupt: stop
// the current turn.
func (s *Server) handleAgentInterrupt(w http.ResponseWriter, r *http.Request) {
	s.agentAction(w, r, "interrupt", func(a AgentActions, pane string) (string, error) {
		ctx, cancel := s.toolCtx()
		defer cancel()
		return a.UserInterrupt(ctx, pane)
	})
}

// handleAgentClose is POST /api/agents/{pane_id}/close: end the agent's
// pane (the app confirmed it with the agent's name).
func (s *Server) handleAgentClose(w http.ResponseWriter, r *http.Request) {
	s.agentAction(w, r, "close", func(a AgentActions, pane string) (string, error) {
		ctx, cancel := s.toolCtx()
		defer cancel()
		return a.UserClose(ctx, pane)
	})
}

func (s *Server) agentAction(w http.ResponseWriter, r *http.Request, action string, do func(AgentActions, string) (string, error)) {
	a, ok := s.agentActions(w)
	if !ok {
		return
	}
	pane := r.PathValue("pane_id")
	spoken, err := do(a, pane)
	fields := map[string]any{"action": action}
	if err != nil {
		fields["error"] = err.Error()
		s.log.Log(logSource, "web.agent_action", pane, fields)
		writeError(w, actionStatus(err), err.Error())
		return
	}
	s.log.Log(logSource, "web.agent_action", pane, fields)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "spoken": spoken})
}
