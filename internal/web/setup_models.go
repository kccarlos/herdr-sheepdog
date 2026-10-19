package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// localTimeout bounds the check of a local server (Ollama, LM Studio): it
// answers at once or is not running.
const localTimeout = 1500 * time.Millisecond

// SetupModelsRequest is POST /api/setup/models: list the models an
// OpenAI-compatible endpoint offers. A compat Preset id sets the name and
// base URL (the typed ones are ignored). An empty key uses the key saved
// for that provider, only for its saved or preset base URL; Reuse (the
// OpenAI preset) uses the voice's OpenAI key.
type SetupModelsRequest struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Key     string `json:"key,omitempty"`
	Preset  string `json:"preset,omitempty"`
	Reuse   bool   `json:"reuse,omitempty"`
}

// SetupModel is one listed model. Tools is true (the endpoint says it
// supports tool calls), false (it says it does not) or null (no info).
type SetupModel struct {
	ID    string `json:"id"`
	Tools *bool  `json:"tools"`
}

// SetupModelsResponse answers POST /api/setup/models. Error and Fix are
// plain text for the page; the key is never part of it. Running is set
// for a local preset (whether its server answered); Suggested is the
// model to fill in ("": let the user pick).
type SetupModelsResponse struct {
	Models    []SetupModel `json:"models"`
	ToolInfo  bool         `json:"tool_info"`
	Running   *bool        `json:"running,omitempty"`
	Suggested string       `json:"suggested,omitempty"`
	Error     string       `json:"error,omitempty"`
	Fix       string       `json:"fix,omitempty"`
}

// SetupProbeRequest is POST /api/setup/probe: one tool-calling turn.
type SetupProbeRequest struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Key     string `json:"key,omitempty"`
	Model   string `json:"model"`
	Preset  string `json:"preset,omitempty"`
	Reuse   bool   `json:"reuse,omitempty"`
}

// SetupProbeResponse answers POST /api/setup/probe.
type SetupProbeResponse struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// savedCompatKey is the key already saved for provider name, only when
// base is the base URL it was saved for or its preset's: a saved key is
// never sent to another host the user typed. reuse (a preset with a
// reusable key, OpenAI) means the voice's key, for the preset's base only.
func (s *Server) savedCompatKey(name, base string, reuse bool) string {
	sd := s.deps.Setup
	if name == brain.ProviderDeepSeek {
		if normURL(sd.Config.DeepSeek.BaseURL) != normURL(base) {
			return ""
		}
		return s.savedKey("DEEPSEEK_API_KEY")
	}
	var allowed []string
	if c, ok := sd.Config.Brain.CompatProvider(name); ok && !reuse {
		allowed = append(allowed, c.BaseURL)
	}
	if p, ok := s.compatPreset(name); ok {
		allowed = append(allowed, p.BaseURL)
	}
	for _, a := range allowed {
		if normURL(a) == normURL(base) {
			return s.savedKey(s.compatKeyEnv(name, reuse))
		}
	}
	return ""
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
	name, base := s.compatTarget(req.Preset, req.Name, req.BaseURL)
	preset, isPreset := s.compatPreset(req.Preset)
	if !isPreset {
		preset, _ = s.compatPreset(name)
	}
	resp := SetupModelsResponse{Models: []SetupModel{}}
	if base == "" {
		resp.Error = "No Base URL yet."
		resp.Fix = "Type the Base URL first (it usually ends in /v1). You can still type the model name."
		return resp
	}
	key := strings.TrimSpace(req.Key)
	if key == "" || req.Reuse {
		key = s.savedCompatKey(name, base, req.Reuse && isPreset && preset.ReuseEnv != "")
	}
	client := s.deps.Setup.Endpoints.HTTP
	local := isPreset && preset.Local
	if local {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, localTimeout)
		defer cancel()
	}
	list, err := onboard.ListModels(ctx, client, base, key)
	if err != nil {
		var me *onboard.ModelsError
		kind, reason := onboard.ModelsNoList, ""
		if errors.As(err, &me) {
			kind, reason = me.Kind, me.Reason
		}
		shown := strings.TrimSuffix(base, "/") + "/models"
		switch {
		case local && kind == onboard.ModelsUnreachable:
			off := false
			resp.Running = &off
			resp.Error = preset.Label + " is not running."
			resp.Fix = "Start " + preset.Label + ", then press Check again."
		case local && kind == onboard.ModelsNoList && reason == "the list is empty":
			on := true
			resp.Running = &on
			resp.Error = preset.Label + " is running but has no models yet."
			resp.Fix = localNoModelsFix(preset.ID)
		case kind == onboard.ModelsUnreachable:
			resp.Error = "Couldn't reach " + shown + ": " + reason + "."
			resp.Fix = "Check the Base URL (it usually ends in /v1). You can still type the model name."
		case kind == onboard.ModelsAuth:
			resp.Error = "This endpoint needs a valid API key."
			resp.Fix = "Paste it above, then press Find models again."
		default:
			resp.Error = "This endpoint doesn't list its models."
			resp.Fix = "Type the model name from its docs."
		}
		return resp
	}
	if local {
		on := true
		resp.Running = &on
	}
	list = onboard.ChatModels(preset, list)
	for _, m := range list {
		resp.Models = append(resp.Models, SetupModel{ID: m.ID, Tools: m.Tools})
		if m.Tools != nil {
			resp.ToolInfo = true
		}
	}
	resp.Suggested = onboard.SuggestModel(preset, list)
	return resp
}

// localNoModelsFix says how to get a first model into a local server.
func localNoModelsFix(id string) string {
	if id == "ollama" {
		return "Pull one in a terminal (ollama pull <model>), then press Check again."
	}
	return "Download and load a model in the app, then press Check again."
}

func (s *Server) handleSetupProbe(w http.ResponseWriter, r *http.Request) {
	var req SetupProbeRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	name, base := s.compatTarget(req.Preset, req.Name, req.BaseURL)
	model := strings.TrimSpace(req.Model)
	if base == "" || model == "" {
		writeJSON(w, http.StatusOK, SetupProbeResponse{Detail: "A Base URL and a model name are needed.", Fix: "Fill both in, then press Test tool calling again."})
		return
	}
	preset, isPreset := s.compatPreset(req.Preset)
	reuse := req.Reuse && isPreset && preset.ReuseEnv != ""
	key := strings.TrimSpace(req.Key)
	if key == "" || reuse {
		key = s.savedCompatKey(name, base, reuse)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	res := onboard.ProbeTools(ctx, s.deps.Setup.Endpoints, onboard.Compat{Name: name, BaseURL: base, APIKey: key, KeyEnv: s.compatKeyEnv(name, reuse), NoKey: onboard.KeyOptionalURL(base), Model: model})
	writeJSON(w, http.StatusOK, SetupProbeResponse{OK: res.Status == onboard.OK, Detail: res.Detail, Fix: res.Fix})
}

// SetupGeminiModelsRequest is POST /api/setup/gemini-models: the models
// the Gemini key can run as the orchestrator (empty Key: the saved one).
type SetupGeminiModelsRequest struct {
	Key string `json:"key,omitempty"`
}

// SetupGeminiModelsResponse lists them, newest first; Default is the
// orchestrator's default model.
type SetupGeminiModelsResponse struct {
	Models  []string `json:"models"`
	Default string   `json:"default"`
	Error   string   `json:"error,omitempty"`
	Fix     string   `json:"fix,omitempty"`
}

func (s *Server) handleSetupGeminiModels(w http.ResponseWriter, r *http.Request) {
	var req SetupGeminiModelsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		key = s.savedKey("GEMINI_API_KEY")
	}
	ids, res := onboard.ListGeminiModels(r.Context(), s.deps.Setup.Endpoints, key)
	out := SetupGeminiModelsResponse{Models: []string{}, Default: brain.DefaultModel}
	if res.Status != onboard.OK {
		out.Error, out.Fix = res.Detail, res.Fix
	} else if ids != nil {
		out.Models = ids
	}
	writeJSON(w, http.StatusOK, out)
}

// anthropicEndpoints are the setup endpoints with the Anthropic API root:
// the test's, else the configured anthropic.base_url. The page never
// sends a host for Anthropic, so a saved key only goes where the daemon
// itself would send it.
func (s *Server) anthropicEndpoints() onboard.Endpoints {
	ep := s.deps.Setup.Endpoints
	if ep.AnthropicBase == "" {
		ep.AnthropicBase = s.deps.Setup.Config.Anthropic.BaseURL
	}
	return ep
}

// anthropicKey is the typed key, else the saved one.
func (s *Server) anthropicKey(typed string) string {
	if k := strings.TrimSpace(typed); k != "" {
		return k
	}
	return s.savedKey("ANTHROPIC_API_KEY")
}

// SetupAnthropicModelsRequest is POST /api/setup/anthropic-models: the
// models the Anthropic key can use (empty Key: the saved one).
type SetupAnthropicModelsRequest struct {
	Key string `json:"key,omitempty"`
}

func (s *Server) handleSetupAnthropicModels(w http.ResponseWriter, r *http.Request) {
	var req SetupAnthropicModelsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	resp := SetupModelsResponse{Models: []SetupModel{}}
	key := s.anthropicKey(req.Key)
	if key == "" {
		resp.Error = "No Anthropic key yet."
		resp.Fix = "Paste the key above first. You can still type the model name."
		writeJSON(w, http.StatusOK, resp)
		return
	}
	list, res := onboard.ListAnthropicModels(r.Context(), s.anthropicEndpoints(), key)
	if res.Status != onboard.OK {
		resp.Error, resp.Fix = res.Detail, res.Fix
		writeJSON(w, http.StatusOK, resp)
		return
	}
	for _, m := range list {
		resp.Models = append(resp.Models, SetupModel{ID: m.ID, Tools: m.Tools})
		resp.ToolInfo = resp.ToolInfo || m.Tools != nil
	}
	if p, ok := onboard.PresetByID(s.presets(), brain.ProviderAnthropic); ok {
		resp.Suggested = onboard.SuggestModel(p, list)
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetupAnthropicProbeRequest is POST /api/setup/anthropic-probe: one
// tool-calling turn on Model (empty Key: the saved one).
type SetupAnthropicProbeRequest struct {
	Key   string `json:"key,omitempty"`
	Model string `json:"model"`
}

func (s *Server) handleSetupAnthropicProbe(w http.ResponseWriter, r *http.Request) {
	var req SetupAnthropicProbeRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	res := onboard.ProbeAnthropic(ctx, s.anthropicEndpoints(), s.anthropicKey(req.Key), strings.TrimSpace(req.Model))
	switch {
	case res.Status != onboard.OK && strings.Contains(res.Fix, "doctor again"):
		res.Fix = "check the model name and the key, then press Test tool calling again"
	case res.Status != onboard.OK && strings.Contains(res.Fix, "sheepdog setup"):
		res.Fix = "paste the Anthropic key above, then press Test tool calling again"
	}
	writeJSON(w, http.StatusOK, SetupProbeResponse{OK: res.Status == onboard.OK, Detail: res.Detail, Fix: res.Fix})
}
