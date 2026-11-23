package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
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

// factoryAsker is the optional half of Factory backing open asks and owner answers.
type factoryAsker interface {
	Asks(open bool) []flow.Ask
	MintProof(askID, option, inputKind, deviceID string) (factory.AnswerProof, error)
	Answer(askID, option string, proof factory.AnswerProof) error
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

func (s *Server) handleFactoryAsks(w http.ResponseWriter, _ *http.Request) {
	asker, ok := s.deps.Factory.(factoryAsker)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "factory is not available")
		return
	}
	asks := []flow.Ask{}
	for _, a := range asker.Asks(true) {
		if a.Gated && a.Kind != flow.KindDecision {
			asks = append(asks, a)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"protocol": 1, "asks": asks})
}

func (s *Server) handleFactoryAskAnswer(w http.ResponseWriter, r *http.Request) {
	dev := deviceOf(r)
	if dev == "" || dev == localDevice {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "proof_required", "error": "proof_required"})
		return
	}
	asker, ok := s.deps.Factory.(factoryAsker)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "factory is not available")
		return
	}
	var req struct{ Option string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Option) == "" {
		writeError(w, http.StatusBadRequest, `want {"option": "…"}`)
		return
	}
	askID := strings.TrimSpace(r.PathValue("id"))
	proof, err := asker.MintProof(askID, req.Option, factory.ProofInputTap, dev)
	if err == nil {
		err = asker.Answer(askID, req.Option, proof)
	}
	if err != nil {
		writeFactoryRefusal(w, err)
		return
	}
	s.log.Log(logSource, "web.factory.ask_answered", "", map[string]any{"ask_id": askID, "device_id": dev})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ask_id": askID, "device_id": dev})
}

func writeFactoryRefusal(w http.ResponseWriter, err error) {
	var ref *factory.Refusal
	if !errors.As(err, &ref) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := map[string]int{"not_found": http.StatusNotFound, flow.RefuseClosed: http.StatusConflict, "proof_required": http.StatusForbidden, "proof_rejected": http.StatusForbidden}[ref.Code]
	if status == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"code": ref.Code, "error": ref.Error()})
}
