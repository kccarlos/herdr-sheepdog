package web

// The voice part of the setup page's Models step: which model and which
// voice each voice card will use, and the model list behind its picker.

import (
	"net/http"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gptlive"
)

// SetupVoicePick is one voice family's model and voice as configured,
// its defaults and the voices the page offers (never a key).
type SetupVoicePick struct {
	Model        string   `json:"model"`
	Voice        string   `json:"voice"`
	DefaultModel string   `json:"default_model"`
	DefaultVoice string   `json:"default_voice"`
	Voices       []string `json:"voices"`
}

// setupVoicePicks are the Gemini Live (AI Studio and Vertex share
// gemini.model and gemini.voice) and GPT-Live picks.
func (s *Server) setupVoicePicks() (SetupVoicePick, SetupVoicePick) {
	cfg := s.deps.Setup.Config
	g := SetupVoicePick{
		Model: orDefault(cfg.Gemini.Model, gemini.DefaultModel), Voice: orDefault(cfg.Gemini.Voice, gemini.DefaultVoice),
		DefaultModel: gemini.DefaultModel, DefaultVoice: gemini.DefaultVoice,
	}
	g.Voices = onboard.VoiceNames(onboard.VoiceGemini, g.Voice)
	o := SetupVoicePick{
		Model: orDefault(cfg.Voice.OpenAI.Model, gptlive.DefaultModel), Voice: orDefault(cfg.Voice.OpenAI.Voice, gptlive.DefaultVoice),
		DefaultModel: gptlive.DefaultModel, DefaultVoice: gptlive.DefaultVoice,
	}
	o.Voices = onboard.VoiceNames(onboard.VoiceOpenAI, o.Voice)
	return g, o
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

// withFirst is list with first in front when list lacks it (no
// duplicates, a new slice).
func withFirst(list []string, first string) []string {
	out := []string{}
	if first != "" {
		out = append(out, first)
	}
	for _, m := range list {
		if m != first {
			out = append(out, m)
		}
	}
	return out
}

// voiceChoice is the model and voice a save writes for voice provider p,
// with the env entries they go to. An empty model or voice in the
// request keeps the configured one (nothing is written for it). msg is
// a plain reason when either is refused.
type voiceChoice struct {
	modelEnv, model string
	voiceEnv, voice string
}

func (s *Server) voiceChoice(p, model, voice string) (voiceChoice, string) {
	g, o := s.setupVoicePicks()
	pick, c := g, voiceChoice{modelEnv: "SHEEPDOG_GEMINI_MODEL", voiceEnv: "SHEEPDOG_GEMINI_VOICE"}
	if p == onboard.VoiceOpenAI {
		pick, c = o, voiceChoice{modelEnv: "SHEEPDOG_VOICE_OPENAI_MODEL", voiceEnv: "SHEEPDOG_VOICE_OPENAI_VOICE"}
	}
	if m := strings.TrimSpace(model); m != "" {
		if !onboard.ValidModelName(m) {
			return c, "the voice model name may only hold letters, digits and . _ / -"
		}
		c.model = m
	}
	if v := strings.TrimSpace(voice); v != "" {
		name, ok := onboard.CanonicalVoice(p, v, pick.Voice)
		if !ok {
			return c, "unknown voice " + quoteShort(v) + "; pick one of " + strings.Join(pick.Voices, ", ")
		}
		c.voice = name
	}
	return c, ""
}

// quoteShort quotes a typed value for an error line, cut to 40 bytes.
func quoteShort(v string) string {
	if len(v) > 40 {
		v = v[:40] + "…"
	}
	return `"` + strings.Map(func(r rune) rune {
		if r < ' ' || r == '"' {
			return -1
		}
		return r
	}, v) + `"`
}
