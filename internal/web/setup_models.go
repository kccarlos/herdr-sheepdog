package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// SetupModelsRequest is POST /api/setup/models: list the models an
// OpenAI-compatible endpoint offers (see onboard.ModelsRequest).
type SetupModelsRequest = onboard.ModelsRequest

// SetupModel is one listed model.
type SetupModel = onboard.ModelInfo

// SetupModelsResponse answers POST /api/setup/models.
type SetupModelsResponse = onboard.ModelsResponse

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
	key := strings.TrimSpace(req.Key)
	if base != "" && (key == "" || req.Reuse) {
		key = s.savedCompatKey(name, base, req.Reuse && isPreset && preset.ReuseEnv != "")
	}
	return onboard.FindModels(ctx, s.deps.Setup.Endpoints.HTTP, s.presets(), req, key)
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
		resp.Models = append(resp.Models, SetupModel(m))
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
