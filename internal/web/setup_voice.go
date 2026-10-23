package web

// The voice part of the setup page's Models step: which model and which
// voice each voice card will use, and the model list behind its picker.

import (
	"net/http"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// SetupVoicePick is one voice family's model and voice as configured, its
// defaults and the voices the page offers (never a key).
type SetupVoicePick = onboard.VoicePick

// setupVoicePicks are the Gemini Live and GPT-Live picks.
func (s *Server) setupVoicePicks() (SetupVoicePick, SetupVoicePick) {
	return onboard.VoicePicks(s.deps.Setup.Config)
}

// SetupVoiceModelsRequest is POST /api/setup/voice-models: the models a
// voice card's picker offers. Provider is gemini, vertex or openai; an
// empty Key uses the saved one (vertex needs none: nothing is called).
type SetupVoiceModelsRequest struct {
	Provider string `json:"provider"`
	Key      string `json:"key,omitempty"`
}

// SetupVoiceModelsResponse lists them, the configured model first when
// the list lacks it. Listed: the list came from the provider; otherwise
// it is the configured and known-good models, and Note (or Error and
// Fix) says why.
type SetupVoiceModelsResponse struct {
	Models  []string `json:"models"`
	Current string   `json:"current"`
	Default string   `json:"default"`
	Listed  bool     `json:"listed"`
	Note    string   `json:"note,omitempty"`
	Error   string   `json:"error,omitempty"`
	Fix     string   `json:"fix,omitempty"`
}

func (s *Server) handleSetupVoiceModels(w http.ResponseWriter, r *http.Request) {
	var req SetupVoiceModelsRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	g, o := s.setupVoicePicks()
	ep := s.deps.Setup.Endpoints
	key := strings.TrimSpace(req.Key)
	var out SetupVoiceModelsResponse
	var ids []string
	var res onboard.Result
	switch req.Provider {
	case onboard.VoiceGemini:
		out = SetupVoiceModelsResponse{Current: g.Model, Default: g.DefaultModel}
		if key == "" {
			key = s.savedKey("GEMINI_API_KEY")
		}
		ids, res = onboard.ListLiveModels(r.Context(), ep, key)
	case onboard.VoiceVertex:
		out = SetupVoiceModelsResponse{Current: g.Model, Default: g.DefaultModel,
			Note: "Vertex AI does not list its models for an API key, so these are the Live models we checked on Vertex."}
		out.Models = setupVertex(s.deps.Setup.Config).LiveModels
		writeJSON(w, http.StatusOK, out)
		return
	case onboard.VoiceOpenAI:
		out = SetupVoiceModelsResponse{Current: o.Model, Default: o.DefaultModel}
		if key == "" {
			key = s.savedKey("OPENAI_API_KEY")
		}
		ids, res = onboard.ListGPTLiveModels(r.Context(), ep, key)
	default:
		writeError(w, http.StatusBadRequest, "provider must be gemini, vertex or openai")
		return
	}
	switch {
	case res.Status != onboard.OK:
		out.Error, out.Fix = res.Detail, res.Fix
		out.Models = withFirst([]string{out.Default}, out.Current)
	case len(ids) == 0:
		out.Note = "This key's model list names no Live model, so the default is offered."
		out.Models = withFirst([]string{out.Default}, out.Current)
	default:
		out.Listed = true
		out.Models = withFirst(ids, out.Current)
	}
	writeJSON(w, http.StatusOK, out)
}

func withFirst(list []string, first string) []string { return onboard.WithFirst(list, first) }
