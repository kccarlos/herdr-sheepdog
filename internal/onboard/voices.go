package onboard

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gptlive"
)

// Voice providers as the setup page names its voice cards.
const (
	VoiceGemini = "gemini" // Gemini Live on Google AI Studio
	VoiceVertex = "vertex" // Gemini Live on Vertex AI
	VoiceOpenAI = "openai" // GPT-Live
)

// GPTLivePrice is the GPT-Live price line the setup page and the docs
// show (CONFIG.md voice.provider).
const GPTLivePrice = "$0.05 per minute of open session"

// modelName is what a model id may look like before it is written to the
// env file: no spaces, no newlines, no quotes.
var modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

// ValidModelName reports whether m may be saved as a model id.
func ValidModelName(m string) bool { return modelName.MatchString(m) }

// VoiceNames are the voices offered for voice provider p (gemini and
// vertex share Gemini's prebuilt voices), plus current when it was set
// by hand and is not among them.
func VoiceNames(p, current string) []string {
	list := gemini.Voices
	if p == VoiceOpenAI {
		list = gptlive.Voices
	}
	out := append([]string(nil), list...)
	if current != "" && !containsFold(out, current) {
		out = append(out, current)
	}
	return out
}

// CanonicalVoice is name as spelled in VoiceNames(p, current), matched
// without regard to case; ok is false when it is not offered.
func CanonicalVoice(p, name, current string) (string, bool) {
	name = strings.TrimSpace(name)
	for _, v := range VoiceNames(p, current) {
		if strings.EqualFold(v, name) {
			return v, true
		}
	}
	return "", false
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// ListLiveModels lists the Gemini Live models an AI Studio key can use:
// the models whose supportedGenerationMethods include
// bidiGenerateContent (the Live API), newest first. Free (a model-list
// call); the key goes in a header.
func ListLiveModels(ctx context.Context, ep Endpoints, key string) ([]string, Result) {
	const name, console = "gemini key", "https://aistudio.google.com/apikey"
	if key == "" {
		return nil, Result{Name: name, Status: Fail, Detail: "no Gemini key yet", Fix: "paste the Gemini key first"}
	}
	var out struct {
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	u := strings.TrimSuffix(ep.GeminiBase, "/") + "/v1beta/models?pageSize=1000"
	if err := getJSON(ctx, ep.client(), u, map[string]string{"x-goog-api-key": key}, &out); err != nil {
		return nil, scrubbed(keyFailure(name, "Gemini (AI Studio)", "GEMINI_API_KEY", console, err), key)
	}
	var ids []string
	seen := map[string]bool{}
	for _, m := range out.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		live := false
		for _, x := range m.Methods {
			live = live || x == "bidiGenerateContent"
		}
		if live && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, Result{Name: name, Status: OK, Detail: fmt.Sprintf("%d Live models", len(ids))}
}

// ListGPTLiveModels lists the GPT-Live models an OpenAI key can use: the
// ids of GET /models that start with gpt-live, newest first. Free.
func ListGPTLiveModels(ctx context.Context, ep Endpoints, key string) ([]string, Result) {
	const name, console = "openai voice key", "https://platform.openai.com/api-keys"
	if key == "" {
		return nil, Result{Name: name, Status: Fail, Detail: "no OpenAI key yet", Fix: "paste the OpenAI key first"}
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	u := strings.TrimSuffix(ep.OpenAIBase, "/") + "/models"
	if err := getJSON(ctx, ep.client(), u, map[string]string{"Authorization": "Bearer " + key}, &out); err != nil {
		return nil, scrubbed(keyFailure(name, "OpenAI", "OPENAI_API_KEY", console, err), key)
	}
	var ids []string
	seen := map[string]bool{}
	for _, m := range out.Data {
		if strings.HasPrefix(m.ID, "gpt-live") && !seen[m.ID] {
			seen[m.ID] = true
			ids = append(ids, m.ID)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, Result{Name: name, Status: OK, Detail: fmt.Sprintf("%d Live models", len(ids))}
}

// scrubbed is r with key taken out of its texts: some APIs repeat the
// key they refuse.
func scrubbed(r Result, key string) Result {
	r.Detail, r.Fix = scrub(r.Detail, key), scrub(r.Fix, key)
	return r
}
