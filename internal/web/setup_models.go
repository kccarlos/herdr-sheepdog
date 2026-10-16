package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// SetupModelsRequest is POST /api/setup/models: list the models an
// OpenAI-compatible endpoint offers. An empty key uses the key saved for
// that provider, when the base URL is the saved one.
type SetupModelsRequest struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Key     string `json:"key,omitempty"`
}

// SetupModel is one listed model. Tools is true (the endpoint says it
// supports tool calls), false (it says it does not) or null (no info).
type SetupModel struct {
	ID    string `json:"id"`
	Tools *bool  `json:"tools"`
}

// SetupModelsResponse answers POST /api/setup/models. Error and Fix are
// plain text for the page; the key is never part of it.
type SetupModelsResponse struct {
	Models   []SetupModel `json:"models"`
	ToolInfo bool         `json:"tool_info"`
	Error    string       `json:"error,omitempty"`
	Fix      string       `json:"fix,omitempty"`
}

// SetupProbeRequest is POST /api/setup/probe: one tool-calling turn.
type SetupProbeRequest struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Key     string `json:"key,omitempty"`
	Model   string `json:"model"`
}

// SetupProbeResponse answers POST /api/setup/probe.
type SetupProbeResponse struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// savedCompatKey is the key already saved for provider name, only when
// base is the base URL it was saved for: a saved key is never sent to
// another host the user typed.
func (s *Server) savedCompatKey(name, base string) string {
	sd := s.deps.Setup
	norm := func(u string) string { return strings.TrimSuffix(strings.TrimSpace(u), "/") }
	var key, saveBase, env string
	if name == brain.ProviderDeepSeek {
		key, saveBase, env = sd.Config.DeepSeek.APIKey, sd.Config.DeepSeek.BaseURL, "DEEPSEEK_API_KEY"
	} else if c, ok := sd.Config.Brain.CompatProvider(name); ok {
		key, saveBase, env = c.APIKey, c.BaseURL, c.APIKeyEnv
	}
	if env == "" {
		env = config.DefaultCompatKeyEnv(name)
	}
	if key == "" && sd.EnvPath != "" {
		if saved, err := onboard.ReadEnvFile(sd.EnvPath); err == nil {
			key = saved[env]
		}
	}
	if key == "" || norm(saveBase) != norm(base) {
		return ""
	}
	return key
}

func (s *Server) handleSetupModels(w http.ResponseWriter, r *http.Request) {
	var req SetupModelsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.listModels(r.Context(), req))
}

func (s *Server) listModels(ctx context.Context, req SetupModelsRequest) SetupModelsResponse {
	base := strings.TrimSpace(req.BaseURL)
	resp := SetupModelsResponse{Models: []SetupModel{}}
	if base == "" {
		resp.Error = "No Base URL yet."
		resp.Fix = "Type the Base URL first (it usually ends in /v1). You can still type the model name."
		return resp
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		key = s.savedCompatKey(compatName(req.Name), base)
	}
	list, err := onboard.ListModels(ctx, s.deps.Setup.Endpoints.HTTP, base, key)
	if err != nil {
		var me *onboard.ModelsError
		kind, reason := onboard.ModelsNoList, ""
		if errors.As(err, &me) {
			kind, reason = me.Kind, me.Reason
		}
		shown := strings.TrimSuffix(base, "/") + "/models"
		switch kind {
		case onboard.ModelsUnreachable:
			resp.Error = "Couldn't reach " + shown + ": " + reason + "."
			resp.Fix = "Check the Base URL (it usually ends in /v1). You can still type the model name."
		case onboard.ModelsAuth:
			resp.Error = "This endpoint needs a valid API key."
			resp.Fix = "Paste it above, then press Find models again."
		default:
			resp.Error = "This endpoint doesn't list its models."
			resp.Fix = "Type the model name from its docs."
		}
		return resp
	}
	for _, m := range list {
		resp.Models = append(resp.Models, SetupModel{ID: m.ID, Tools: m.Tools})
		if m.Tools != nil {
			resp.ToolInfo = true
		}
	}
	return resp
}

func (s *Server) handleSetupProbe(w http.ResponseWriter, r *http.Request) {
	var req SetupProbeRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	base, model := strings.TrimSpace(req.BaseURL), strings.TrimSpace(req.Model)
	if base == "" || model == "" {
		writeJSON(w, http.StatusOK, SetupProbeResponse{Detail: "A Base URL and a model name are needed.", Fix: "Fill both in, then press Test tool calling again."})
		return
	}
	name := compatName(req.Name)
	key := strings.TrimSpace(req.Key)
	if key == "" {
		key = s.savedCompatKey(name, base)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	res := onboard.ProbeTools(ctx, s.deps.Setup.Endpoints, onboard.Compat{Name: name, BaseURL: base, APIKey: key, KeyEnv: config.DefaultCompatKeyEnv(name), NoKey: onboard.KeyOptionalURL(base), Model: model})
	writeJSON(w, http.StatusOK, SetupProbeResponse{OK: res.Status == onboard.OK, Detail: res.Detail, Fix: res.Fix})
}
