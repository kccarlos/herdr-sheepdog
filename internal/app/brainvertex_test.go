package app

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// The vertex brain provider needs the [gemini.vertex] key and project,
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
