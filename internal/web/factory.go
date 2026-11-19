package web

import (
	"encoding/json"
	"net/http"
)

// Factory is the factory engine's surface: owner commands and the reports
// runs send with their token.
type Factory interface {
	Start(folder, goal string) error
	Stop(project string) error
	Report(token, verb string, args json.RawMessage) error
}

// handleFactory serves POST /api/factory/{start,stop,report}. A start or stop
// carrying a run's token goes to the engine's acceptance step, which refuses
// it (design.md 7.2).
func (s *Server) handleFactory(cmd string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Folder, Goal, Project, Token, Verb string
			Args                               json.RawMessage
		}
		if s.deps.Factory == nil {
			writeError(w, http.StatusServiceUnavailable, "factory is not available")
			return
		}
		err := readJSON(r, &req)
		switch {
		case err != nil:
		case cmd == "report":
			err = s.deps.Factory.Report(req.Token, req.Verb, req.Args)
		case req.Token != "":
			err = s.deps.Factory.Report(req.Token, cmd, nil)
		case cmd == "start":
			err = s.deps.Factory.Start(req.Folder, req.Goal)
		default:
			err = s.deps.Factory.Stop(req.Project)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}
