package app

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

func TestGeminiLive(t *testing.T) {
	keys, v, missing := GeminiLive(config.Gemini{Backend: "aistudio", APIKeys: []string{"a", "b"}})
	if len(keys) != 2 || v != nil || missing != "" || geminiLabel(v) != "Gemini Live" {
		t.Errorf("aistudio = %v %v %q", keys, v, missing)
	}
	if _, _, missing := GeminiLive(config.Gemini{Backend: "aistudio"}); missing != "GEMINI_API_KEY" {
		t.Errorf("aistudio without keys: missing = %q", missing)
	}
	// Vertex ignores the AI Studio pool: one key, project and location.
	keys, v, missing = GeminiLive(config.Gemini{Backend: "vertex", APIKeys: []string{"a"}, Vertex: config.GeminiVertex{APIKey: "vk", Project: "p", Location: "us-central1"}})
	if len(keys) != 1 || keys[0] != "vk" || v == nil || v.Project != "p" || v.Location != "us-central1" || missing != "" || geminiLabel(v) != "Gemini Live (Vertex)" {
		t.Errorf("vertex = %v %+v %q", keys, v, missing)
	}
	if _, _, missing := GeminiLive(config.Gemini{Backend: "vertex", APIKeys: []string{"a"}, Vertex: config.GeminiVertex{APIKey: "vk", Location: "us-central1"}}); missing != "VERTEX_PROJECT" {
		t.Errorf("vertex without project: missing = %q", missing)
	}
	if _, _, missing := GeminiLive(config.Gemini{Backend: "vertex", Vertex: config.GeminiVertex{Project: "p", Location: "us-central1"}}); missing != "VERTEX_API_KEY" {
		t.Errorf("vertex without key: missing = %q", missing)
	}
}
