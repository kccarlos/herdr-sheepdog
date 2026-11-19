package web

import "net/http"

// Factory is the owner-command surface of the factory engine.
type Factory interface {
	Start(folder, goal string) error
	Stop(project string) error
}

func (s *Server) handleFactory(start bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Folder, Goal, Project string }
		if s.deps.Factory == nil {
			writeError(w, http.StatusServiceUnavailable, "factory is not available")
			return
		}
		err := readJSON(r, &req)
		if err == nil && start {
			err = s.deps.Factory.Start(req.Folder, req.Goal)
		} else if err == nil {
			err = s.deps.Factory.Stop(req.Project)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
