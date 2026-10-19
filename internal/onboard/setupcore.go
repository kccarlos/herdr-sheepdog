package onboard

// The rules the setup page and the terminal setup (sheepdog init) share:
// which voice and orchestrator a choice means, which env entries and
// config keys a save writes, and the key and model checks around it. The
// page (internal/web) and cmd/sheepdog both call these; neither repeats
// them.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gptlive"
)

// GeminiKeyURL is where a free Gemini (AI Studio) key is made.
const GeminiKeyURL = "https://aistudio.google.com/apikey"

// DefaultVertexLocation is the location offered for the Vertex voice:
// gemini-3.8-live is served from us-central1 and the us multi-region, not
// from global (CONFIG.md, [gemini.vertex]).
const DefaultVertexLocation = "us-central1"

// LocalTimeout bounds the check of a local server (Ollama, LM Studio): it
// answers at once or is not running.
const LocalTimeout = 1500 * time.Millisecond

// OrDefault is v trimmed, or def when that is empty.
func OrDefault(v, def string) string {
	if v = strings.TrimSpace(v); v == "" {
		return def
	}
	return v
}

// QuoteShort quotes a typed value for an error line, cut to 40 bytes.
func QuoteShort(v string) string {
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

// WithFirst is list with first in front when list lacks it (no
// duplicates, a new slice).
func WithFirst(list []string, first string) []string {
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

// NormURL is u without spaces and a trailing slash.
func NormURL(u string) string { return strings.TrimSuffix(strings.TrimSpace(u), "/") }

// VoicePick is one voice family's model and voice as configured, its
// defaults and the voices offered (never a key).
type VoicePick struct {
	Model        string   `json:"model"`
	Voice        string   `json:"voice"`
	DefaultModel string   `json:"default_model"`
	DefaultVoice string   `json:"default_voice"`
	Voices       []string `json:"voices"`
}

// VoicePicks are the Gemini Live (AI Studio and Vertex share gemini.model
// and gemini.voice) and GPT-Live picks.
func VoicePicks(cfg config.Config) (VoicePick, VoicePick) {
	g := VoicePick{
		Model: OrDefault(cfg.Gemini.Model, gemini.DefaultModel), Voice: OrDefault(cfg.Gemini.Voice, gemini.DefaultVoice),
		DefaultModel: gemini.DefaultModel, DefaultVoice: gemini.DefaultVoice,
	}
	g.Voices = VoiceNames(VoiceGemini, g.Voice)
	o := VoicePick{
		Model: OrDefault(cfg.Voice.OpenAI.Model, gptlive.DefaultModel), Voice: OrDefault(cfg.Voice.OpenAI.Voice, gptlive.DefaultVoice),
		DefaultModel: gptlive.DefaultModel, DefaultVoice: gptlive.DefaultVoice,
	}
	o.Voices = VoiceNames(VoiceOpenAI, o.Voice)
	return g, o
}

// VoiceSel is the model and voice a save writes for a voice provider,
// with the env entries they go to. An empty Model or Voice keeps the
// configured one (nothing is written for it).
type VoiceSel struct {
	ModelEnv, Model string
	VoiceEnv, Voice string
}

// ChooseVoice checks a typed voice model and voice name for provider p;
// msg is a plain reason when either is refused.
func ChooseVoice(cfg config.Config, p, model, voice string) (VoiceSel, string) {
	g, o := VoicePicks(cfg)
	pick, c := g, VoiceSel{ModelEnv: "SHEEPDOG_GEMINI_MODEL", VoiceEnv: "SHEEPDOG_GEMINI_VOICE"}
	if p == VoiceOpenAI {
		pick, c = o, VoiceSel{ModelEnv: "SHEEPDOG_VOICE_OPENAI_MODEL", VoiceEnv: "SHEEPDOG_VOICE_OPENAI_VOICE"}
	}
	if m := strings.TrimSpace(model); m != "" {
		if !ValidModelName(m) {
			return c, "the voice model name may only hold letters, digits and . _ / -"
		}
		c.Model = m
	}
	if v := strings.TrimSpace(voice); v != "" {
		name, ok := CanonicalVoice(p, v, pick.Voice)
		if !ok {
			return c, "unknown voice " + QuoteShort(v) + "; pick one of " + strings.Join(pick.Voices, ", ")
		}
		c.Voice = name
	}
	return c, ""
}

// PresetOf is the preset a saved orchestrator provider matches, and the
// base URL of its OpenAI-compatible entry.
func PresetOf(presets []Preset, cfg config.Config, provider string) (string, string) {
	if provider == "" {
		return brain.ProviderGemini, ""
	}
	if p, ok := PresetByID(presets, provider); ok && p.Kind == PresetNative {
		return p.ID, ""
	}
	c, ok := cfg.Brain.CompatProvider(provider)
	if !ok {
		return PresetOther, ""
	}
	if p, ok := CompatPreset(presets, provider); ok && NormURL(p.BaseURL) == NormURL(c.BaseURL) {
		return p.ID, c.BaseURL
	}
	return PresetOther, c.BaseURL
}

// CompatPreset is the compat preset named id.
func CompatPreset(presets []Preset, id string) (Preset, bool) {
	p, ok := PresetByID(presets, id)
	return p, ok && p.Kind == PresetCompat
}

// CompatKeyEnv is where the key of the OpenAI-compatible provider name
// lives: a preset with a reusable key (OpenAI) by the reuse choice, else
// the saved entry's api_key_env, else the preset's or the default name.
func CompatKeyEnv(cfg config.Config, name string, reuse bool) string {
	if p, ok := PresetByID(Presets(), name); ok && p.ReuseEnv != "" {
		return PresetKeyEnv(name, reuse)
	}
	if c, ok := cfg.Brain.CompatProvider(name); ok && c.APIKeyEnv != "" {
		return c.APIKeyEnv
	}
	return PresetKeyEnv(name, false)
}

// CompatName is the provider name typed ("openrouter" when empty).
func CompatName(n string) string {
	n = strings.ToLower(strings.TrimSpace(n))
	if n == "" {
		return "openrouter"
	}
	return n
}

// CompatTarget is the provider name and base URL a request means: a
// compat preset id fixes both, else the typed ones.
func CompatTarget(presets []Preset, preset, name, base string) (string, string) {
	if p, ok := CompatPreset(presets, preset); ok {
		return p.ID, p.BaseURL
	}
	return CompatName(name), strings.TrimSpace(base)
}

// SavedVertexKey is the saved Vertex key, only when project is the
// project it was saved for: a key bound to one project's service account
// is not tried on another.
func SavedVertexKey(cfg config.Config, project string) string {
	v := cfg.Gemini.Vertex
	if v.APIKey == "" || project == "" || v.Project != project {
		return ""
	}
	return v.APIKey
}

// AnthropicEndpoints is ep with the Anthropic API root: ep's own (a
// test's), else the configured anthropic.base_url, so a saved key only
// goes where the daemon itself would send it.
func AnthropicEndpoints(ep Endpoints, cfg config.Config) Endpoints {
	if ep.AnthropicBase == "" {
		ep.AnthropicBase = cfg.Anthropic.BaseURL
	}
	return ep
}

// ValidateRequest is one key to check live, before anything is saved.
type ValidateRequest struct {
	// Kind is gemini, openai, deepseek, anthropic, compat or vertex.
	Kind string `json:"kind"`
	Key  string `json:"key"`
	// BaseURL and Name are the compat provider's; a compat Preset id
	// sets both (the typed ones are ignored).
	BaseURL string `json:"base_url,omitempty"`
	Name    string `json:"name,omitempty"`
	Preset  string `json:"preset,omitempty"`
	// Project and Location are Vertex AI's; Voice also checks that the
	// location serves the Live model. An empty Key uses the saved Vertex
	// key, only for the project it was saved for.
	Project  string `json:"project,omitempty"`
	Location string `json:"location,omitempty"`
	Voice    bool   `json:"voice,omitempty"`
	// LiveModel is the Live model the voice check opens ("": the
	// configured gemini.model).
	LiveModel string `json:"live_model,omitempty"`
}

// Check is one check's outcome for a page or a terminal.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail | skip
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// CheckOf is r as a Check.
func CheckOf(r Result) Check {
	return Check{Name: r.Name, Status: strings.ToLower(r.Status.String()), Detail: r.Detail, Fix: r.Fix}
}

// ValidateKey checks one key live (no key in any result). cfg is what is
// saved today, presets the preset list.
func ValidateKey(ctx context.Context, ep Endpoints, cfg config.Config, presets []Preset, req ValidateRequest) Check {
	key := strings.TrimSpace(req.Key)
	switch req.Kind {
	case "gemini":
		c := CheckOf(CheckGeminiKey(ctx, ep, key))
		if c.Status == "fail" && strings.Contains(c.Detail, "rejected the key") {
			c.Fix = "check that you copied the whole key, or create a new one at " + GeminiKeyURL
		}
		return c
	case "openai":
		if key == "" {
			return Check{Name: "openai voice key", Status: "fail", Detail: "no key given", Fix: "paste an OpenAI API key (https://platform.openai.com/api-keys)"}
		}
		return CheckOf(CheckOpenAIVoiceKey(ctx, ep, key))
	case "deepseek":
		off := false
		return CheckOf(CheckCompat(ctx, ep, Compat{Name: brain.ProviderDeepSeek, BaseURL: config.DefaultDeepSeekBaseURL, APIKey: key, KeyEnv: "DEEPSEEK_API_KEY", Thinking: &off}, false)[0])
	case "anthropic":
		c := CheckOf(CheckAnthropicKey(ctx, AnthropicEndpoints(ep, cfg), key))
		if c.Status == "fail" && strings.Contains(c.Detail, "rejected the key") {
			c.Fix = "check that you copied the whole key, or create a new one at " + brain.ServiceOf(brain.ProviderAnthropic).Console
		}
		return c
	case "compat":
		name, base := CompatTarget(presets, req.Preset, req.Name, req.BaseURL)
		c := config.OpenAICompat{Name: name, BaseURL: base}
		if c.BaseURL == "" {
			return Check{Name: name + " key", Status: "fail", Detail: "no base URL", Fix: "give the API root, e.g. https://openrouter.ai/api/v1"}
		}
		return CheckOf(CheckCompat(ctx, ep, Compat{Name: name, BaseURL: c.BaseURL, APIKey: key, KeyEnv: CompatKeyEnv(cfg, name, false), NoKey: c.KeyOptional()}, false)[0])
	case "vertex":
		project := strings.TrimSpace(req.Project)
		if key == "" {
			key = SavedVertexKey(cfg, project)
		}
		live := ""
		if req.Voice {
			live = OrDefault(req.LiveModel, cfg.Gemini.Model)
			if !ValidModelName(live) {
				return Check{Name: "vertex", Status: "fail", Detail: "the voice model name may only hold letters, digits and . _ / -", Fix: "pick a model from the list"}
			}
		}
		return CheckOf(CheckVertex(ctx, ep, VertexCreds{APIKey: key, Project: project, Location: req.Location}, live))
	}
	return Check{Name: req.Kind, Status: "fail", Detail: "unknown key kind " + fmt.Sprintf("%q", req.Kind)}
}

// ModelsRequest asks for the models an OpenAI-compatible endpoint
// offers. A compat Preset id sets the name and base URL (the typed ones
// are ignored). Key and Reuse are resolved by the caller.
type ModelsRequest struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Key     string `json:"key,omitempty"`
	Preset  string `json:"preset,omitempty"`
	Reuse   bool   `json:"reuse,omitempty"`
}

// ModelInfo is one listed model. Tools is true (the endpoint says it
// supports tool calls), false (it says it does not) or null (no info).
type ModelInfo struct {
	ID    string `json:"id"`
	Tools *bool  `json:"tools"`
}

// ModelsResponse answers a model-list request. Error and Fix are plain
// text; the key is never part of it. Running is set for a local preset
// (whether its server answered); Suggested is the model to fill in ("":
// let the user pick).
type ModelsResponse struct {
	Models    []ModelInfo `json:"models"`
	ToolInfo  bool        `json:"tool_info"`
	Running   *bool       `json:"running,omitempty"`
	Suggested string      `json:"suggested,omitempty"`
	Error     string      `json:"error,omitempty"`
	Fix       string      `json:"fix,omitempty"`
}

// FindModels lists the models of the compat endpoint req names, with the
// plain-text errors and fixes the page shows. key is already resolved
// (typed, or saved for this host only).
func FindModels(ctx context.Context, client *http.Client, presets []Preset, req ModelsRequest, key string) ModelsResponse {
	name, base := CompatTarget(presets, req.Preset, req.Name, req.BaseURL)
	preset, isPreset := CompatPreset(presets, req.Preset)
	if !isPreset {
		preset, _ = CompatPreset(presets, name)
	}
	resp := ModelsResponse{Models: []ModelInfo{}}
	if base == "" {
		resp.Error = "No Base URL yet."
		resp.Fix = "Type the Base URL first (it usually ends in /v1). You can still type the model name."
		return resp
	}
	local := isPreset && preset.Local
	if local {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, LocalTimeout)
		defer cancel()
	}
	list, err := ListModels(ctx, client, base, key)
	if err != nil {
		var me *ModelsError
		kind, reason := ModelsNoList, ""
		if errors.As(err, &me) {
			kind, reason = me.Kind, me.Reason
		}
		shown := strings.TrimSuffix(base, "/") + "/models"
		switch {
		case local && kind == ModelsUnreachable:
			off := false
			resp.Running = &off
			resp.Error = preset.Label + " is not running."
			resp.Fix = "Start " + preset.Label + ", then press Check again."
		case local && kind == ModelsNoList && reason == "the list is empty":
			on := true
			resp.Running = &on
			resp.Error = preset.Label + " is running but has no models yet."
			resp.Fix = localNoModelsFix(preset.ID)
		case kind == ModelsUnreachable:
			resp.Error = "Couldn't reach " + shown + ": " + reason + "."
			resp.Fix = "Check the Base URL (it usually ends in /v1). You can still type the model name."
		case kind == ModelsAuth:
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
	list = ChatModels(preset, list)
	for _, m := range list {
		resp.Models = append(resp.Models, ModelInfo{ID: m.ID, Tools: m.Tools})
		if m.Tools != nil {
			resp.ToolInfo = true
		}
	}
	resp.Suggested = SuggestModel(preset, list)
	return resp
}

// localNoModelsFix says how to get a first model into a local server.
func localNoModelsFix(id string) string {
	if id == "ollama" {
		return "Pull one in a terminal (ollama pull <model>), then press Check again."
	}
	return "Download and load a model in the app, then press Check again."
}

// ModelsInput is the models step: the voice and the orchestrator chosen,
// and the keys typed for them (empty keeps the saved one). The setup
// page posts it as /api/setup/keys; the terminal builds it from its
// answers.
type ModelsInput struct {
	GeminiKey string `json:"gemini_key"`
	// Voice is gemini (default: Gemini Live on AI Studio), vertex (Gemini
	// Live on Vertex AI) or openai (GPT-Live).
	Voice     string `json:"voice"`
	OpenAIKey string `json:"openai_key"`
	// VoiceModel and VoiceName are the chosen voice's Live model and
	// voice ("": keep the configured one). Gemini Live (either backend)
	// writes gemini.model and gemini.voice, GPT-Live voice.openai.model
	// and voice.openai.voice. An unknown voice saves nothing.
	VoiceModel string `json:"voice_model"`
	VoiceName  string `json:"voice_name"`
	// Orchestrator is a preset id (gemini, the default; anthropic,
	// deepseek, vertex, openrouter, openai, ollama, lmstudio) or compat
	// (Other URL).
	Orchestrator string `json:"orchestrator"`
	// GeminiModel is the Gemini orchestrator's model ("": the default).
	GeminiModel string `json:"gemini_model"`
	// Vertex is the shared Vertex AI block (voice and orchestrator): the
	// key (empty keeps the saved one, for the same project only), the
	// project, the voice's location and the orchestrator's model.
	Vertex struct {
		Key      string `json:"key"`
		Project  string `json:"project"`
		Location string `json:"location"`
		Model    string `json:"model"`
	} `json:"vertex"`
	DeepSeekKey string `json:"deepseek_key"`
	// AnthropicKey and AnthropicModel are the Anthropic orchestrator's
	// ("" model: brain.DefaultAnthropicModel).
	AnthropicKey   string `json:"anthropic_key"`
	AnthropicModel string `json:"anthropic_model"`
	// Compat is the OpenAI-compatible orchestrator: a compat preset
	// fixes the name and base URL. ReuseKey (OpenAI preset only) points it
	// at the voice's OPENAI_API_KEY instead of a key of its own; Key is
	// then ignored.
	Compat struct {
		Name     string `json:"name"`
		BaseURL  string `json:"base_url"`
		Model    string `json:"model"`
		Key      string `json:"key"`
		ReuseKey bool   `json:"reuse_key"`
	} `json:"compat"`
}

// ModelsChoice is a ModelsInput resolved against what is saved: the
// voice, the brain provider and model, and the entries a save writes.
type ModelsChoice struct {
	Voice string
	// Orch is the orchestrator id as asked (a preset id, or compat).
	Orch     string
	VoiceSel VoiceSel
	Preset   Preset
	IsPreset bool
	Provider string // SHEEPDOG_BRAIN_PROVIDER
	Model    string // SHEEPDOG_BRAIN_MODEL ("": none)
	Compat   *config.OpenAICompat
	Reuse    bool
	// VertexProject is the project to save; VertexLocation the voice's
	// location ("" unless the voice is on Vertex).
	VertexProject, VertexLocation string
}

// ResolveModels turns in into a ModelsChoice; msg is a plain reason when
// the choice is refused (nothing is read or written).
func ResolveModels(cfg config.Config, presets []Preset, in ModelsInput) (c ModelsChoice, msg string) {
	c.Voice = OrDefault(in.Voice, VoiceGemini)
	c.Orch = OrDefault(in.Orchestrator, brain.ProviderGemini)
	if c.Voice != VoiceGemini && c.Voice != VoiceVertex && c.Voice != VoiceOpenAI {
		return c, "voice must be gemini, vertex or openai"
	}
	vs, msg := ChooseVoice(cfg, c.Voice, in.VoiceModel, in.VoiceName)
	if msg != "" {
		return c, msg
	}
	c.VoiceSel = vs
	if c.Voice == VoiceVertex || c.Orch == brain.ProviderVertex {
		c.VertexProject = strings.TrimSpace(in.Vertex.Project)
		if c.VertexProject == "" {
			return c, "Vertex AI needs the Google Cloud project id"
		}
		if c.Voice == VoiceVertex {
			c.VertexLocation = OrDefault(in.Vertex.Location, DefaultVertexLocation)
		}
	}
	c.Provider = c.Orch
	switch c.Orch {
	case brain.ProviderGemini:
		c.Model = strings.TrimSpace(in.GeminiModel)
	case brain.ProviderVertex:
		c.Model = OrDefault(in.Vertex.Model, brain.DefaultModel)
	case brain.ProviderDeepSeek:
		c.Model = brain.DefaultDeepSeekModel
	case brain.ProviderAnthropic:
		c.Model = OrDefault(in.AnthropicModel, brain.DefaultAnthropicModel)
		if !ValidModelName(c.Model) {
			return c, "the Anthropic model name may only hold letters, digits and . _ / -"
		}
	default:
		preset, isPreset := CompatPreset(presets, c.Orch)
		if !isPreset && c.Orch != "compat" && c.Orch != PresetOther {
			return c, "orchestrator must be anthropic, gemini, deepseek, vertex, openrouter, openai, ollama, lmstudio or compat"
		}
		cc := config.OpenAICompat{Name: CompatName(in.Compat.Name), BaseURL: strings.TrimSpace(in.Compat.BaseURL), Model: strings.TrimSpace(in.Compat.Model)}
		if isPreset {
			cc.Name, cc.BaseURL = preset.ID, preset.BaseURL
		}
		if cc.BaseURL == "" || cc.Model == "" {
			return c, "an OpenAI-compatible orchestrator needs its base URL and a model that supports tool calling"
		}
		if cc.Name == brain.ProviderGemini || cc.Name == brain.ProviderDeepSeek || cc.Name == brain.ProviderVertex || cc.Name == brain.ProviderAnthropic {
			return c, fmt.Sprintf("%q is a built-in provider name; pick another name", cc.Name)
		}
		c.Preset, c.IsPreset = preset, isPreset
		c.Reuse = isPreset && preset.ReuseEnv != "" && in.Compat.ReuseKey
		cc.APIKeyEnv = CompatKeyEnv(cfg, cc.Name, c.Reuse)
		c.Compat = &cc
		c.Provider, c.Model = cc.Name, cc.Model
	}
	return c, ""
}

// SaveModels writes a resolved choice: the OpenAI-compatible entry and
// the Vertex project to configPath, and the validated keys in set plus
// the voice and brain settings to envPath. saved is the env file as read
// before. It returns the names of the env entries written. Callers check
// the keys first; nothing is written here for a key that failed.
func SaveModels(configPath, envPath string, saved map[string]string, c ModelsChoice, set map[string]string) ([]string, error) {
	if set == nil {
		set = map[string]string{}
	}
	if c.Compat != nil {
		if err := UpsertCompat(configPath, *c.Compat); err != nil {
			return nil, fmt.Errorf("cannot add the provider to %s: %w", configPath, err)
		}
	}
	var del []string
	if c.VertexProject != "" {
		if err := SetVertexConfig(configPath, c.VertexProject, c.VertexLocation); err != nil {
			return nil, fmt.Errorf("cannot write the Vertex project to %s: %w", configPath, err)
		}
		// The env file overrides config.toml: values left there by hand
		// would hide the ones just written.
		del = append(del, "VERTEX_PROJECT")
		if c.VertexLocation != "" {
			del = append(del, "VERTEX_LOCATION")
		}
	}
	switch c.Voice {
	case VoiceVertex:
		set["SHEEPDOG_VOICE_PROVIDER"], set["SHEEPDOG_GEMINI_BACKEND"] = "gemini", "vertex"
	case VoiceGemini:
		set["SHEEPDOG_VOICE_PROVIDER"], set["SHEEPDOG_GEMINI_BACKEND"] = "gemini", "aistudio"
	default:
		set["SHEEPDOG_VOICE_PROVIDER"] = c.Voice
	}
	if c.VoiceSel.Model != "" {
		set[c.VoiceSel.ModelEnv] = c.VoiceSel.Model
	}
	if c.VoiceSel.Voice != "" {
		set[c.VoiceSel.VoiceEnv] = c.VoiceSel.Voice
	}
	set["SHEEPDOG_BRAIN_PROVIDER"] = c.Provider
	if c.Model != "" {
		set["SHEEPDOG_BRAIN_MODEL"] = c.Model
	} else if p := saved["SHEEPDOG_BRAIN_PROVIDER"]; p != "" && p != c.Provider {
		// Back to Gemini from another provider: that provider's model
		// must not stay behind.
		del = append(del, "SHEEPDOG_BRAIN_MODEL")
	}
	if err := EditEnvFile(envPath, set, del); err != nil {
		return nil, fmt.Errorf("cannot write %s: %w", envPath, err)
	}
	names := make([]string, 0, len(set))
	for k := range set {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}
