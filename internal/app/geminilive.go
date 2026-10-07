package app

import (
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
)

// GeminiLive resolves the [gemini] backend into the Live connection
// fields of gemini.Config: the AI Studio key pool, or on backend "vertex"
// the one Vertex key with its project and location. missing names what is
// unset when the Live voice cannot run ("" when it can). The orchestrator keeps
// using the AI Studio pool either way.
func GeminiLive(g config.Gemini) (keys []string, vertex *gemini.Vertex, missing string) {
	if g.Backend != "vertex" {
		if len(g.APIKeys) == 0 {
			return nil, nil, "GEMINI_API_KEY"
		}
		return g.APIKeys, nil, ""
	}
	v := g.Vertex
	switch {
	case v.APIKey == "":
		missing = "VERTEX_API_KEY"
	case v.Project == "":
		missing = "VERTEX_PROJECT"
	}
	return []string{v.APIKey}, &gemini.Vertex{Project: v.Project, Location: v.Location}, missing
}

// geminiLabel is the voice picker label: which backend Gemini Live runs on.
func geminiLabel(vertex *gemini.Vertex) string {
	if vertex != nil {
		return "Gemini Live (Vertex)"
	}
	return "Gemini Live"
}
