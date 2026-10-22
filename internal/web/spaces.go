package web

import (
	"context"
	"net/http"
	"strings"
)

// Spaces (docs/dev/ws-contract.md "Spaces"): herdr's workspaces with their
// agents, worktrees and saved wrap-up commands, and what the user does
// with them by tap. The supervisor keeps the rails (a busy space is not
// closed, a dirty worktree is not removed unless forced); running a
// wrap-up command is shell-equivalent, so it sits behind the phone
// shell's gate. Each action is logged as web.space_action with metadata
// only.

// handleSpaces is GET /api/spaces.
func (s *Server) handleSpaces(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agentActions(w)
	if !ok {
		return
	}
	v, err := a.Spaces(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// spaceAction runs one space action and answers {"ok": true, …} or the
// error with its status.
func (s *Server) spaceAction(w http.ResponseWriter, r *http.Request, action string, do func(context.Context, AgentActions, string) (map[string]any, error)) {
	a, ok := s.agentActions(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	ctx, cancel := s.toolCtx()
	defer cancel()
	out, err := do(ctx, a, id)
	fields := map[string]any{"action": action, "space": id}
	if err != nil {
		fields["error"] = err.Error()
		s.log.Log(logSource, "web.space_action", "", fields)
		writeError(w, actionStatus(err), err.Error())
		return
	}
	s.log.Log(logSource, "web.space_action", "", fields)
	if out == nil {
		out = map[string]any{}
	}
	out["ok"] = true
	writeJSON(w, http.StatusOK, out)
}

// handleSpaceRename is POST /api/spaces/{id}/rename {"name"}.
func (s *Server) handleSpaceRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	s.spaceAction(w, r, "rename", func(ctx context.Context, a AgentActions, id string) (map[string]any, error) {
		spoken, err := a.RenameSpace(ctx, id, req.Name)
		return map[string]any{"spoken": spoken}, err
	})
}

// handleNewSpace is POST /api/spaces {"dir", "name"?, "worktree"?:
// {"branch"}}: a space with no agent yet.
func (s *Server) handleNewSpace(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agentActions(w)
	if !ok {
		return
	}
	var req struct {
		Dir      string `json:"dir"`
		Name     string `json:"name"`
		Worktree *struct {
			Branch string `json:"branch"`
		} `json:"worktree"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	branch := ""
	if req.Worktree != nil {
		if branch = strings.TrimSpace(req.Worktree.Branch); branch == "" {
			writeError(w, http.StatusBadRequest, "a new worktree needs a branch")
			return
		}
	}
	ctx, cancel := s.toolCtx()
	defer cancel()
	space, pane, err := a.NewSpace(ctx, strings.TrimSpace(req.Dir), req.Name, branch)
	fields := map[string]any{"action": "new", "space": space, "worktree": branch != ""}
	if err != nil {
		fields["error"] = err.Error()
		s.log.Log(logSource, "web.space_action", "", fields)
		writeJSON(w, actionStatus(err), actionErrorBody(err))
		return
	}
	s.log.Log(logSource, "web.space_action", pane, fields)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "space": space, "pane_id": pane})
}

// handleSpaceClose is POST /api/spaces/{id}/close (the app confirmed it).
func (s *Server) handleSpaceClose(w http.ResponseWriter, r *http.Request) {
	s.spaceAction(w, r, "close", func(ctx context.Context, a AgentActions, id string) (map[string]any, error) {
		spoken, err := a.CloseSpace(ctx, id)
		return map[string]any{"spoken": spoken}, err
	})
}

// handleSpaceWorktrees is GET /api/spaces/{id}/worktrees.
func (s *Server) handleSpaceWorktrees(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agentActions(w)
	if !ok {
		return
	}
	list, err := a.SpaceWorktrees(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, actionStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"repo": list.Source.RepoName, "repo_root": list.Source.RepoRoot, "worktrees": list.Worktrees})
}

// handleWorktreeOpen is POST /api/spaces/{id}/worktrees/open {"path"} or
// {"branch"}: a space for an existing checkout of the space's repository.
func (s *Server) handleWorktreeOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Branch string `json:"branch"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	s.spaceAction(w, r, "worktree_open", func(ctx context.Context, a AgentActions, id string) (map[string]any, error) {
		pane, err := a.OpenWorktree(ctx, id, strings.TrimSpace(req.Path), strings.TrimSpace(req.Branch))
		return map[string]any{"pane_id": pane}, err
	})
}

// handleWorktreeRemove is POST /api/spaces/{id}/worktree/remove
// {"force"?}: remove the linked worktree the space works in and close
// the space (the app confirmed it).
func (s *Server) handleWorktreeRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Force bool `json:"force"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	s.spaceAction(w, r, "worktree_remove", func(ctx context.Context, a AgentActions, id string) (map[string]any, error) {
		spoken, err := a.RemoveWorktree(ctx, id, req.Force)
		return map[string]any{"spoken": spoken}, err
	})
}

// handleSpaceCommands is GET /api/spaces/{id}/commands.
func (s *Server) handleSpaceCommands(w http.ResponseWriter, r *http.Request) {
	a, ok := s.agentActions(w)
	if !ok {
		return
	}
	list, err := a.SpaceCommands(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, actionStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": list})
}

// handleSaveCommand is POST /api/spaces/{id}/commands {"name",
// "command"} (a new one) and PUT /api/spaces/{id}/commands/{cid}.
func (s *Server) handleSaveCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Command string `json:"command"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	cid := r.PathValue("cid")
	s.spaceAction(w, r, "command_save", func(ctx context.Context, a AgentActions, id string) (map[string]any, error) {
		c, err := a.SaveCommand(ctx, id, cid, req.Name, req.Command)
		return map[string]any{"command": c}, err
	})
}

// handleDeleteCommand is DELETE /api/spaces/{id}/commands/{cid}.
func (s *Server) handleDeleteCommand(w http.ResponseWriter, r *http.Request) {
	cid := r.PathValue("cid")
	s.spaceAction(w, r, "command_delete", func(ctx context.Context, a AgentActions, id string) (map[string]any, error) {
		return nil, a.DeleteCommand(ctx, id, cid)
	})
}

// handleRunCommand is POST /api/spaces/{id}/commands/{cid}/run: the
// command runs in a new tab of the space; behind the phone shell's gate.
func (s *Server) handleRunCommand(w http.ResponseWriter, r *http.Request) {
	if !s.term.shellAllowed() {
		writeError(w, http.StatusForbidden, "running commands is off on this computer, like the shell")
		return
	}
	cid := r.PathValue("cid")
	s.spaceAction(w, r, "command_run", func(ctx context.Context, a AgentActions, id string) (map[string]any, error) {
		pane, err := a.RunCommand(ctx, id, cid)
		return map[string]any{"pane_id": pane}, err
	})
}
