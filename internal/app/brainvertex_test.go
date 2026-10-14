package app

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// The vertex orchestrator provider needs the [gemini.vertex] key and project,
// whatever gemini.backend says; the picker names the missing variable.
func TestBrainVertexProvider(t *testing.T) {
	find := func(cfg config.Config) (avail bool, reason string) {
		for _, p := range BrainProviders(cfg) {
			if p.ID == brain.ProviderVertex {
				if p.Label != "Gemini (Vertex)" {
					t.Errorf("label = %q", p.Label)
				}
				return p.Available, p.Reason
			}
		}
		t.Fatal("vertex not listed")
		return false, ""
	}
	var cfg config.Config
	cfg.Gemini.Backend = "aistudio"
	if ok, r := find(cfg); ok || r != "no VERTEX_API_KEY" {
		t.Errorf("no key: %t %q", ok, r)
	}
	cfg.Gemini.Vertex.APIKey = "vk"
	if ok, r := find(cfg); ok || r != "no VERTEX_PROJECT" {
		t.Errorf("no project: %t %q", ok, r)
	}
	cfg.Gemini.Vertex.Project = "p"
	if ok, r := find(cfg); !ok || r != "" {
		t.Errorf("key and project: %t %q", ok, r)
	}
	models, err := BrainModels(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := models[brain.ProviderVertex]; !ok || len(models) != 1 {
		t.Errorf("models = %v", models)
	}
}

// Each [[brain.openai_compat]] entry is listed in the picker (greyed
// with its key variable when it has none) and registered as a model
// when it has a key or a loopback base_url.
func TestBrainOpenAICompatProviders(t *testing.T) {
	var cfg config.Config
	cfg.Brain.OpenAICompat = []config.OpenAICompat{
		{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OPENROUTER_API_KEY", Model: "m", APIKey: "k"},
		{Name: "groq", Label: "Groq", BaseURL: "https://api.groq.com/openai/v1", APIKeyEnv: "GROQ_API_KEY", Model: "m"},
		{Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1", APIKeyEnv: "OLLAMA_API_KEY", Model: "qwen3:8b"},
	}
	got := map[string]string{}
	for _, p := range BrainProviders(cfg) {
		got[p.ID] = p.Label + "|" + p.Reason
		if (p.ID == "groq") == p.Available && (p.ID == "openrouter" || p.ID == "groq" || p.ID == "ollama") {
			t.Errorf("%s available = %v", p.ID, p.Available)
		}
	}
	if got["openrouter"] != "openrouter|" || got["groq"] != "Groq|no GROQ_API_KEY" || got["ollama"] != "ollama|" {
		t.Errorf("providers = %v", got)
	}
	models, err := BrainModels(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, or := models["openrouter"]
	_, groq := models["groq"]
	_, ollama := models["ollama"]
	if !or || groq || !ollama {
		t.Errorf("models: openrouter %v groq %v ollama %v", or, groq, ollama)
	}
	if compatMissing(cfg, "groq") != "GROQ_API_KEY" || compatMissing(cfg, "openrouter") != "" || compatMissing(cfg, "gemini") != "" {
		t.Error("compatMissing")
	}
}
