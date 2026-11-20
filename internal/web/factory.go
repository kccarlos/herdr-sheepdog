package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
)

// Factory is the factory engine's surface: read-only views, owner commands
// and the reports runs send with their token.
type Factory interface {
	Projects() []factory.ProjectView
	Graph(project string) (factory.GraphView, error)
	LogLines(project string, n int) ([]factory.LogEntry, error)
	Start(folder, goal string, maxRunning int) error
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
			MaxRunning                         int `json:"max_running"`
			Args                               json.RawMessage
		}
		if s.deps.Factory == nil {
			writeError(w, http.StatusServiceUnavailable, "factory is not available")
			return
		}
		err := readJSON(r, &req)
		status, ref := http.StatusBadRequest, (*factory.Refusal)(nil)
		switch {
		case err != nil:
		case cmd == "report":
			if err = s.deps.Factory.Report(req.Token, req.Verb, req.Args); err != nil && !errors.As(err, &ref) {
				status = http.StatusInternalServerError // a daemon fault, not a refusal
			}
		case req.Token != "":
			err = s.deps.Factory.Report(req.Token, cmd, nil)
		case cmd == "start":
			err = s.deps.Factory.Start(req.Folder, req.Goal, req.MaxRunning)
		default:
			err = s.deps.Factory.Stop(req.Project)
		}
		if err != nil {
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func (s *Server) handleFactoryView(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Factory == nil {
			writeError(w, http.StatusServiceUnavailable, "factory is not available")
			return
		}
		id, v, err := r.PathValue("id"), any(nil), error(nil)
		switch kind {
		case "projects":
			v = map[string]any{"protocol": 1, "projects": append([]factory.ProjectView{}, s.deps.Factory.Projects()...)}
		case "graph":
			v, err = s.deps.Factory.Graph(id)
		default:
			n := 0
			if raw := r.URL.Query().Get("n"); raw != "" {
				if n, err = strconv.Atoi(raw); err != nil || n < 0 {
					writeError(w, http.StatusBadRequest, "invalid n")
					return
				}
			}
			lines, lerr := s.deps.Factory.LogLines(id, n)
			err, v = lerr, map[string]any{"protocol": 1, "project": id, "lines": append([]factory.LogEntry{}, lines...)}
		}
		if errors.Is(err, factory.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
		} else {
			writeJSON(w, http.StatusOK, v)
		}
	}
}
