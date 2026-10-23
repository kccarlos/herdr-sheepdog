package onboard

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// Preset kinds: a native orchestrator provider sheepdog has its own
// adapter for, a known OpenAI-compatible endpoint (a generated
// [[brain.openai_compat]] entry named after the preset), or the generic
// "Other URL" fields.
const (
	PresetNative = "native"
	PresetCompat = "compat"
	PresetOther  = "other"
)

// OpenAIOrchKeyEnv holds the key of the OpenAI orchestrator preset.
// OPENAI_API_KEY is the GPT-Live voice key; the orchestrator uses it only
// when the user ticks "use the same OpenAI key" (Preset.ReuseEnv).
const OpenAIOrchKeyEnv = "OPENAI_ORCH_API_KEY"

// Preset is one orchestrator choice on the setup page: the page shows the
// grid in this order and asks only for what the preset needs.
type Preset struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Kind is PresetNative, PresetCompat or PresetOther.
	Kind string `json:"kind"`
	// BaseURL is the API root of a compat preset ("" otherwise).
	BaseURL string `json:"base_url,omitempty"`
	// KeyEnv names the env-file entry the key is saved under.
	KeyEnv string `json:"key_env,omitempty"`
	// ReuseEnv is another saved key the user may choose to reuse instead
	// (OpenAI: the voice's OPENAI_API_KEY), only after an explicit yes.
	ReuseEnv  string `json:"reuse_env,omitempty"`
	KeyNeeded bool   `json:"key_needed"`
	// Local: a server on this computer; the page checks it is running.
	Local bool `json:"local"`
	// DocsURL is where to get a key or the app.
	DocsURL string `json:"docs_url,omitempty"`
	// DefaultModel is filled in when the list has it ("": the user picks
	// from the discovered list).
	DefaultModel string `json:"default_model,omitempty"`
	// Note is the short line under the label. Quality claims quote only
	// our own measurements (docs/models.md).
	Note string `json:"note"`
}

// Presets are the orchestrator presets, in the order the page shows them.
func Presets() []Preset {
	return []Preset{
		{ID: brain.ProviderAnthropic, Label: "Anthropic", Kind: PresetNative, KeyEnv: "ANTHROPIC_API_KEY", KeyNeeded: true,
			DocsURL: "https://console.anthropic.com/settings/keys", DefaultModel: brain.DefaultAnthropicModel, Note: "92 % understood in our tests · fewer unsupported statements · slower, costs more"},
		{ID: brain.ProviderGemini, Label: "Gemini", Kind: PresetNative, KeyEnv: "GEMINI_API_KEY", KeyNeeded: true,
			DocsURL: "https://aistudio.google.com/apikey", DefaultModel: brain.DefaultModel, Note: "free, can be slow or busy"},
		{ID: brain.ProviderDeepSeek, Label: "DeepSeek", Kind: PresetNative, KeyEnv: "DEEPSEEK_API_KEY", KeyNeeded: true,
			DocsURL: "https://platform.deepseek.com/api_keys", DefaultModel: brain.DefaultDeepSeekModel, Note: "★ most recommended · 92 % understood in our tests · fast, low cost"},
		{ID: brain.ProviderVertex, Label: "Vertex AI", Kind: PresetNative, KeyEnv: "VERTEX_API_KEY", KeyNeeded: true,
			DocsURL: VertexKeyDocURL, DefaultModel: brain.DefaultModel, Note: "Gemini on Google Cloud"},
		{ID: "openrouter", Label: "OpenRouter", Kind: PresetCompat, BaseURL: "https://openrouter.ai/api/v1", KeyEnv: "OPENROUTER_API_KEY", KeyNeeded: true,
			DocsURL: "https://openrouter.ai/keys", Note: "one key, many models"},
		{ID: "openai", Label: "OpenAI", Kind: PresetCompat, BaseURL: "https://api.openai.com/v1", KeyEnv: OpenAIOrchKeyEnv, ReuseEnv: "OPENAI_API_KEY", KeyNeeded: true,
			DocsURL: "https://platform.openai.com/api-keys", Note: "paid per token"},
		{ID: "ollama", Label: "Ollama", Kind: PresetCompat, BaseURL: "http://localhost:11434/v1", Local: true,
			DocsURL: "https://ollama.com/download", Note: "on this computer, no key"},
		{ID: "lmstudio", Label: "LM Studio", Kind: PresetCompat, BaseURL: "http://localhost:1234/v1", Local: true,
			DocsURL: "https://lmstudio.ai", Note: "on this computer, no key"},
		{ID: PresetOther, Label: "Other URL", Kind: PresetOther, Note: "any OpenAI-compatible endpoint"},
	}
}

// PresetByID finds a preset in list.
func PresetByID(list []Preset, id string) (Preset, bool) {
	for _, p := range list {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// PresetKeyEnv is the env-file entry the key of the OpenAI-compatible
// provider name lives in. A compat preset's name uses the preset's own
// entry (reuse: its ReuseEnv, when it has one), even when typed under
// "Other URL": the OpenAI orchestrator never falls on OPENAI_API_KEY, the
// voice key, by default. Any other name: config.DefaultCompatKeyEnv.
func PresetKeyEnv(name string, reuse bool) string {
	if p, ok := PresetByID(Presets(), name); ok && p.Kind == PresetCompat && p.KeyEnv != "" {
		if reuse && p.ReuseEnv != "" {
			return p.ReuseEnv
		}
		return p.KeyEnv
	}
	return config.DefaultCompatKeyEnv(name)
}

// notChat matches OpenAI model ids that are not chat models with tool
// calling (speech, audio, images, video, embeddings, moderation,
// realtime, search, legacy completions).
var notChat = regexp.MustCompile(`embedding|tts|whisper|transcribe|audio|realtime|live|image|dall-e|sora|moderation|search|babbage|davinci|codex|instruct|computer-use|deep-research`)

// smallChat matches a current small chat model id without a date:
// gpt-5-mini, gpt-5.4-mini (the version is captured).
var smallChat = regexp.MustCompile(`^gpt-(\d+(?:\.\d+)?)-mini$`)

// ChatModels drops what the preset knows cannot be an orchestrator: for
// OpenAI, its speech, image, embedding and other non-chat models (its
// list says nothing about tool calling). Other presets keep the list.
func ChatModels(p Preset, list []ModelEntry) []ModelEntry {
	if p.ID != "openai" {
		return list
	}
	out := make([]ModelEntry, 0, len(list))
	for _, m := range list {
		if !notChat.MatchString(strings.ToLower(m.ID)) {
			out = append(out, m)
		}
	}
	return out
}

// SuggestModel is the model the page fills in after a model list: the
// preset's default when listed; for OpenAI the newest small chat model
// (gpt-N-mini) in the live list; for a local server the first model that
// says it calls tools, else the first listed. "" (OpenRouter, or nothing
// fitting): the user picks from the list.
func SuggestModel(p Preset, list []ModelEntry) string {
	if p.DefaultModel != "" {
		for _, m := range list {
			if m.ID == p.DefaultModel {
				return m.ID
			}
		}
	}
	switch {
	case p.ID == "openai":
		best, bestV := "", -1.0
		for _, m := range list {
			sm := smallChat.FindStringSubmatch(m.ID)
			if sm == nil {
				continue
			}
			if v, err := strconv.ParseFloat(sm[1], 64); err == nil && v > bestV {
				best, bestV = m.ID, v
			}
		}
		return best
	case p.Local:
		sorted := append([]ModelEntry(nil), list...)
		tools := func(m ModelEntry) bool { return m.Tools != nil && *m.Tools }
		sort.SliceStable(sorted, func(i, j int) bool { return tools(sorted[i]) && !tools(sorted[j]) })
		for _, m := range sorted {
			if !strings.Contains(strings.ToLower(m.ID), "embed") {
				return m.ID
			}
		}
	}
	return ""
}

// ListGeminiModels lists the orchestrator models an AI Studio key can use:
// the models that support generateContent and look like text models
// (brain.RecommendedModel), newest name first. Google's list does not say
// which models call tools. The key goes in a header, never the URL.
func ListGeminiModels(ctx context.Context, ep Endpoints, key string) ([]string, Result) {
	const name, console = "gemini key", "https://aistudio.google.com/apikey"
	if key == "" {
		return nil, Result{Name: name, Status: Fail, Detail: "no Gemini key yet", Fix: "paste the Gemini key above first; you can still type the model name"}
	}
	var out struct {
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	u := strings.TrimSuffix(ep.GeminiBase, "/") + "/v1beta/models?pageSize=1000"
	if err := getJSON(ctx, ep.client(), u, map[string]string{"x-goog-api-key": key}, &out); err != nil {
		return nil, keyFailure(name, "Gemini (AI Studio)", "GEMINI_API_KEY", console, err)
	}
	var ids []string
	seen := map[string]bool{}
	for _, m := range out.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		ok := false
		for _, x := range m.Methods {
			ok = ok || x == "generateContent"
		}
		if ok && !seen[id] && brain.RecommendedModel(id) {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, Result{Name: name, Status: OK, Detail: fmt.Sprintf("%d models", len(ids))}
}
