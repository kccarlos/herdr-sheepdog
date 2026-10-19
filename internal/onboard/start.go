package onboard

// The starting-point cards of the Models step and the summary line under
// them. The setup page gets this table from /api/setup/state (start) and
// the terminal setup prints the same text; neither keeps its own copy.

import (
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// Starting-point card ids.
const (
	CardRecommended = "recommended"
	CardCost        = "cost"
	CardFree        = "free"
	CardCustom      = "custom"
)

// StartCard is one starting point. Voice and Orch are the voice card and
// the orchestrator preset it selects ("" for Custom, which changes
// nothing).
type StartCard struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Voice string `json:"voice"`
	Orch  string `json:"orch"`
	Note  string `json:"note"`
	Need  string `json:"need"`
}

// StartCards are the four starting points, in the order shown.
func StartCards() []StartCard {
	return []StartCard{
		{ID: CardRecommended, Label: "Recommended", Voice: VoiceGemini, Orch: brain.ProviderAnthropic,
			Note: "Claude Opus 5.5: 92 % understood in our tests, like DeepSeek, with fewer unsupported statements. Slower replies (about 7 s).", Need: "Needs an Anthropic API key (paid)."},
		{ID: CardCost, Label: "Cost-effective", Voice: VoiceGemini, Orch: brain.ProviderDeepSeek,
			Note: "DeepSeek: 92 % understood in our tests, replies in about 3 s, and costs far less."},
		{ID: CardFree, Label: "Free quick start", Voice: VoiceGemini, Orch: brain.ProviderGemini,
			Note: "One free key for both. Good for trying; can be slow or busy. Upgrade any time."},
		{ID: CardCustom, Label: "Custom", Note: "Pick each part yourself."},
	}
}

// CardByID finds a card.
func CardByID(id string) (StartCard, bool) {
	for _, c := range StartCards() {
		if c.ID == id {
			return c, true
		}
	}
	return StartCard{}, false
}

// CardFor is the card matching a voice and orchestrator choice, or
// custom. The models inside a choice do not matter: they stay editable.
func CardFor(voice, orch string) string {
	for _, c := range StartCards() {
		if c.Voice != "" && c.Voice == voice && c.Orch == orch {
			return c.ID
		}
	}
	return CardCustom
}

// StartChoice is the card, voice and orchestrator selected at the start.
type StartChoice struct {
	Card  string `json:"card"`
	Voice string `json:"voice"`
	Orch  string `json:"orch"`
}

// InitialChoice is what the setup starts on: with nothing saved (the
// voice has no credentials) the Recommended card, else the saved voice
// and orchestrator and the card that matches them. hasAnthropic: the
// Anthropic preset exists (an older daemon keeps the saved choice).
func InitialChoice(setupNeeded, hasAnthropic bool, savedVoice, savedOrch string) StartChoice {
	if setupNeeded && hasAnthropic {
		return StartChoice{Card: CardRecommended, Voice: VoiceGemini, Orch: brain.ProviderAnthropic}
	}
	return StartChoice{Card: CardFor(savedVoice, savedOrch), Voice: savedVoice, Orch: savedOrch}
}

// VoiceSetupNeeded says whether the saved voice has no credentials yet
// (the daemon's setup_needed): saved is the env file, cfg the config.
func VoiceSetupNeeded(cfg config.Config, saved map[string]string) bool {
	has := func(env string) bool { return strings.TrimSpace(saved[env]) != "" }
	provider := OrDefault(saved["SHEEPDOG_VOICE_PROVIDER"], OrDefault(cfg.Voice.Provider, VoiceGemini))
	backend := OrDefault(saved["SHEEPDOG_GEMINI_BACKEND"], cfg.Gemini.Backend)
	switch {
	case provider == VoiceOpenAI:
		return !has("OPENAI_API_KEY") && cfg.Voice.OpenAI.APIKey == ""
	case backend == VoiceVertex:
		return !has("VERTEX_API_KEY") && cfg.Gemini.Vertex.APIKey == ""
	}
	return !has("GEMINI_API_KEY") && len(cfg.Gemini.APIKeys) == 0
}

// CostRule says a model id containing Contains costs Cost.
type CostRule struct {
	Contains string `json:"contains"`
	Cost     string `json:"cost"`
}

// StartInfo is the table the setup page reads (/api/setup/state, start):
// the cards, the summary template and the cost and provider lines.
type StartInfo struct {
	Cards          []StartCard       `json:"cards"`
	VoiceProviders map[string]string `json:"voice_providers"`
	VoiceCosts     map[string]string `json:"voice_costs"`
	// Anthropic prices per million input/output tokens (docs/models.md).
	AnthropicCosts []CostRule `json:"anthropic_costs"`
	PayPerUse      string     `json:"pay_per_use"`
	FreeLocal      string     `json:"free_local"`
	FreeDaily      string     `json:"free_daily"`
	Billed         string     `json:"billed"`
	// Template has {voice_model} {voice_provider} {orch_model}
	// {orch_provider} {voice_cost} {orch_cost}.
	Template string `json:"template"`
	// Fallbacks while a model or a name is not chosen yet.
	VoiceModelFallback string `json:"voice_model_fallback"`
	OrchModelFallback  string `json:"orch_model_fallback"`
	OtherFallback      string `json:"other_fallback"`
}

const (
	costFreeDaily = "free with daily limits"
	costBilled    = "billed to your Google Cloud project"
	costLocal     = "free on this computer"
	costPayPerUse = "pay per use"
)

// Start is the StartInfo.
func Start() StartInfo {
	return StartInfo{
		Cards:          StartCards(),
		VoiceProviders: map[string]string{VoiceGemini: "Google AI Studio", VoiceVertex: "Google Vertex AI", VoiceOpenAI: "OpenAI"},
		VoiceCosts:     map[string]string{VoiceGemini: costFreeDaily, VoiceVertex: costBilled, VoiceOpenAI: "$0.05/min of open session"},
		AnthropicCosts: []CostRule{
			{"opus-5-5", "$4/$20 per million tokens"},
			{"sonnet-5-5", "$2/$10 per million tokens"},
			{"haiku-5-5", "$0.10/$0.50 per million tokens"},
		},
		PayPerUse: costPayPerUse, FreeLocal: costLocal, FreeDaily: costFreeDaily, Billed: costBilled,
		Template:           "You talk to {voice_model} ({voice_provider}). Your agents are handled by {orch_model} ({orch_provider}). Cost: {voice_cost} · {orch_cost}",
		VoiceModelFallback: "a Live model", OrchModelFallback: "a model of your choice", OtherFallback: "your server",
	}
}

// VoiceCost is the voice's cost line.
func VoiceCost(voice string) string {
	if c, ok := Start().VoiceCosts[voice]; ok {
		return c
	}
	return costPayPerUse
}

// OrchCost is the orchestrator's cost line for a preset and model.
func OrchCost(p *Preset, model string) string {
	switch {
	case p == nil:
		return costPayPerUse
	case p.Local:
		return costLocal
	case p.ID == brain.ProviderGemini:
		return costFreeDaily
	case p.ID == brain.ProviderVertex:
		return costBilled
	case p.ID == brain.ProviderAnthropic:
		for _, r := range Start().AnthropicCosts {
			if strings.Contains(model, r.Contains) {
				return r.Cost
			}
		}
	}
	return costPayPerUse
}

// SummaryInput is what the summary line says: the voice card, its model,
// the orchestrator preset (nil: Gemini), its model and, for Other URL,
// the name typed.
type SummaryInput struct {
	Voice, VoiceModel string
	Preset            *Preset
	OrchModel         string
	OtherName         string
}

// Summary is the one-line summary of the choice, shown above Save on the
// page and before saving in the terminal.
func Summary(in SummaryInput) string {
	st := Start()
	voiceProv := st.VoiceProviders[in.Voice]
	if voiceProv == "" {
		voiceProv = st.VoiceProviders[VoiceGemini]
	}
	provider := "Gemini"
	if p := in.Preset; p != nil {
		provider = p.Label
		if p.Kind == PresetOther {
			provider = OrDefault(in.OtherName, st.OtherFallback)
		}
	}
	return strings.NewReplacer(
		"{voice_model}", OrDefault(in.VoiceModel, st.VoiceModelFallback),
		"{voice_provider}", voiceProv,
		"{orch_model}", OrDefault(in.OrchModel, st.OrchModelFallback),
		"{orch_provider}", provider,
		"{voice_cost}", VoiceCost(in.Voice),
		"{orch_cost}", OrchCost(in.Preset, in.OrchModel),
	).Replace(st.Template)
}
