package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// VoiceSwitcher is the runtime voice provider switch (voice.Switcher).
type VoiceSwitcher interface {
	Provider() string
	Model() string
	Providers() []voice.ProviderInfo
	SetProvider(id, via string) error
}

// VoiceProvidersResponse is the body of GET /api/voice/providers and PUT
// /api/voice/provider: the current provider and model, and every
// provider, unavailable ones with the reason.
type VoiceProvidersResponse struct {
	Provider  string               `json:"provider"`
	Model     string               `json:"model"`
	Providers []voice.ProviderInfo `json:"providers"`
}

// SetVoiceProviderRequest is the body of PUT /api/voice/provider.
type SetVoiceProviderRequest struct {
	Provider string `json:"provider"`
}

// voiceProvider is the current provider id, "off" without a voice.
func (s *Server) voiceProvider() string {
	if s.deps.VoiceSwitch == nil {
		return "off"
	}
	return s.deps.VoiceSwitch.Provider()
}

// voiceModel is the current provider's model, "" without a voice.
func (s *Server) voiceModel() string {
	if s.deps.VoiceSwitch == nil {
		return ""
	}
	return s.deps.VoiceSwitch.Model()
}

func (s *Server) voiceProvidersResponse() VoiceProvidersResponse {
	resp := VoiceProvidersResponse{Provider: s.voiceProvider(), Model: s.voiceModel(), Providers: []voice.ProviderInfo{}}
	if s.deps.VoiceSwitch != nil {
		resp.Providers = s.deps.VoiceSwitch.Providers()
	}
	return resp
}

// handleVoiceProviders answers the voice providers and the current one.
func (s *Server) handleVoiceProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.voiceProvidersResponse())
}

// handleSetVoiceProvider switches the voice provider: the current voice
// session closes and the new provider's opens (GPT-Live: on the next
// speech or push). The choice is kept in the state file
// (session.voice_provider) and wins over voice.provider on restart. An
// unknown or unavailable provider is a 400 with the reason.
func (s *Server) handleSetVoiceProvider(w http.ResponseWriter, r *http.Request) {
	if s.deps.VoiceSwitch == nil {
		writeError(w, http.StatusServiceUnavailable, "the voice is off (--voice=off)")
		return
	}
	var req SetVoiceProviderRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body: "+err.Error())
		return
	}
	id := strings.ToLower(strings.TrimSpace(req.Provider))
	if err := s.deps.VoiceSwitch.SetProvider(id, "api"); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, voice.ErrUnknownProvider) || errors.Is(err, voice.ErrUnavailableProvider) {
			status = http.StatusBadRequest
		}
		writeError(w, status, "provider "+strconv.Quote(id)+": "+err.Error())
		return
	}
	provider := s.deps.VoiceSwitch.Provider()
	if err := s.deps.Store.UpdateSession(func(ss *state.Session) { ss.VoiceProvider = provider }); err != nil {
		writeError(w, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.voiceProvidersResponse())
}
