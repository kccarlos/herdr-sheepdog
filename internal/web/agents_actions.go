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
	case errors.Is(err, supervisor.ErrNoSuchSpace), errors.Is(err, supervisor.ErrNoSuchCommand):
		return http.StatusNotFound
	case errors.Is(err, supervisor.ErrSpaceBusy), errors.Is(err, supervisor.ErrDirtyWorktree):
		return http.StatusConflict
	case errors.Is(err, brain.ErrNotPermitted):
		return http.StatusForbidden
	default:
		return windowStatus(err)
	}
}

// newAgentRequest is the body of POST /api/agents: an open space, or a
// new space's folder (with an optional fresh worktree).
type newAgentRequest struct {
	Kind     string `json:"kind"`
	Space    string `json:"space"`
	NewSpace *struct {
		Dir      string `json:"dir"`
		Worktree *struct {
			Branch string `json:"branch"`
		} `json:"worktree"`
	} `json:"new_space"`
	Text string `json:"text"`
}

// handleNewAgent is POST /api/agents: it answers once the agent is ready.
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
	in := supervisor.NewAgentRequest{Kind: req.Kind, Space: strings.TrimSpace(req.Space), Text: strings.TrimSpace(req.Text)}
	if req.NewSpace != nil {
		in.Dir = strings.TrimSpace(req.NewSpace.Dir)
		if req.NewSpace.Worktree != nil {
			in.Worktree = strings.TrimSpace(req.NewSpace.Worktree.Branch)
			if in.Worktree == "" {
				writeError(w, http.StatusBadRequest, "a new worktree needs a branch")
				return
			}
		}
	}
	if (in.Space == "") == (in.Dir == "") {
		writeError(w, http.StatusBadRequest, "give space, or new_space with its dir")
		return
	}
	ctx, cancel := s.toolCtx()
	defer cancel()
	res, err := a.UserNewAgent(ctx, in)
	fields := map[string]any{"action": "new", "kind": req.Kind, "space": in.Space, "dir": filepath.Base(in.Dir), "worktree": in.Worktree != "", "text_len": len(in.Text)}
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
