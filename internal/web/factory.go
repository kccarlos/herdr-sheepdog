package web

import (
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
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

// factorySettings is the optional half of Factory backing settings views and proposals.
type factorySettings interface {
	SettingRows(project string) ([]flow.SettingRow, error)
	ProposeSetting(project, name, value, deviceID string) (flow.Ask, error)
}

// factoryOwner is the optional half of Factory backing ticketed page check/start/stop routes.
type factoryOwner interface {
	CheckStart(folder string, set map[string]string) ([]onboard.Result, error)
	StartSet(folder, goal string, set map[string]string) error
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
		case "status":
			e, ok := s.deps.Factory.(*factory.Engine)
			if q := r.URL.Query(); !ok || q.Get("data_dir") != "" && filepath.Clean(q.Get("data_dir")) != filepath.Clean(e.Paths.DataDir) {
				err = factory.ErrProjectNotFound
			} else if tasks, serr := e.ProjectStatusLexical(q.Get("folder")); serr != nil {
				err = serr
			} else {
				v = map[string]any{"tasks": append([]factory.TaskStatus{}, tasks...)}
			}
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

type factoryAskView struct {
	flow.Ask
	Command string `json:"command,omitempty"`
}

func (s *Server) handleFactoryAsks(w http.ResponseWriter, _ *http.Request) {
	asker, ok := s.deps.Factory.(factoryAsker)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "factory is not available")
		return
	}
	asks := []factoryAskView{}
	for _, a := range asker.Asks(true) {
		if a.Gated && a.Kind != flow.KindDecision {
			v := factoryAskView{Ask: a}
			if a.Kind == flow.KindOverride {
				v.Command = flow.FormatAskCommand(a)
			}
			asks = append(asks, v)
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

func (s *Server) handleFactorySettings(w http.ResponseWriter, r *http.Request) {
	id, dev := strings.TrimSpace(r.PathValue("id")), deviceOf(r)
	if r.Method == http.MethodPost && (dev == "" || dev == localDevice) {
		writeJSON(w, http.StatusForbidden, map[string]string{"code": "proof_required", "error": "proof_required"})
		return
	}
	fs, ok := s.deps.Factory.(factorySettings)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "factory is not available")
		return
	}
	if r.Method == http.MethodPost {
		var req struct{ Name, Value string }
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, `want {"name": "…", "value": "…"}`)
			return
		}
		ask, err := fs.ProposeSetting(id, req.Name, req.Value, dev)
		if err != nil {
			writeFactoryRefusal(w, err)
			return
		}
		s.log.Log(logSource, "web.factory.setting_proposed", "", map[string]any{"project": id, "ask_id": ask.ID, "name": ask.Set.Name, "device_id": dev})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ask": ask})
		return
	}
	if r.PathValue("id") != "" && id == "" {
		writeError(w, http.StatusNotFound, factory.ErrProjectNotFound.Error())
	} else if rows, err := fs.SettingRows(id); errors.Is(err, factory.ErrProjectNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"protocol": 1, "rows": append([]flow.SettingRow{}, rows...)})
	}
}

func (s *Server) handleFactoryOwner(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dev, reply := deviceOf(r), func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		if kind != "check" && (dev == "" || dev == localDevice) {
			reply(http.StatusForbidden, map[string]string{"code": "proof_required", "error": "proof_required"})
			return
		}
		fo, ok := s.deps.Factory.(factoryOwner)
		if !ok || s.deps.Factory == nil {
			writeError(w, http.StatusServiceUnavailable, "factory is not available")
			return
		}
		var req struct {
			Folder, Goal, Project, Token string
			Set                          map[string]string
		}
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		res, err, se, sce, sse, ref := []onboard.Result(nil), error(nil), (*factory.SettingError)(nil), (*factory.StartChecksError)(nil), (*factory.StoredSettingsError)(nil), (*factory.Refusal)(nil)
		switch {
		case req.Token != "":
			err = s.deps.Factory.Report(req.Token, kind, nil)
		case kind == "check":
			res, err = fo.CheckStart(req.Folder, req.Set)
		case kind == "start":
			err = fo.StartSet(req.Folder, req.Goal, req.Set)
		default:
			err = s.deps.Factory.Stop(req.Project)
		}
		switch {
		case err == nil && kind == "check":
			reply(http.StatusOK, map[string]any{"results": checksOf(res)})
		case err == nil:
			s.log.Log(logSource, "web.factory.project_"+kind, "", map[string]any{"folder": strings.TrimSpace(req.Folder), "project": strings.TrimSpace(req.Project), "device_id": dev})
			reply(http.StatusOK, map[string]any{"ok": true, "device_id": dev})
		case errors.As(err, &se):
			reply(http.StatusBadRequest, map[string]string{"name": se.Name, "message": se.Message, "error": se.Message})
		case errors.As(err, &sce):
			reply(http.StatusBadRequest, map[string]any{"error": sce.Error(), "results": checksOf(sce.Results)})
		case errors.As(err, &sse):
			reply(http.StatusBadRequest, map[string]string{"error": "Not started: a setting is not allowed. Change it in settings below."})
		case errors.As(err, &ref):
			reply(refusalStatus(ref.Code), map[string]string{"code": ref.Code, "error": ref.Why})
		default:
			reply(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
	}
}

func refusalStatus(code string) int {
	return cmp.Or(map[string]int{"not_found": http.StatusNotFound, flow.RefuseClosed: http.StatusConflict, "proof_required": http.StatusForbidden, "proof_rejected": http.StatusForbidden}[code], http.StatusBadRequest)
}

func writeFactoryRefusal(w http.ResponseWriter, err error) {
	var ref *factory.Refusal
	if !errors.As(err, &ref) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, refusalStatus(ref.Code), map[string]string{"code": ref.Code, "error": ref.Error()})
}

// checksOf is the page's form of the start checks (the setup wizard's Check).
func checksOf(rs []onboard.Result) []onboard.Check {
	out := make([]onboard.Check, 0, len(rs))
	for _, r := range rs {
		out = append(out, onboard.CheckOf(r))
	}
	return out
}
