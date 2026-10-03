package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
)

// Opening is a scenario's pre-rendered first line, pinned by the hash
// of its audio. The audio lives in the speech cache (outside the
// repository); the manifest (testdata/bench/openings.json) is in it.
type Opening struct {
	Scenario string `json:"scenario"`
	Text     string `json:"text"`
	Lang     string `json:"lang"`
	Voice    string `json:"voice"`
	Style    string `json:"style,omitempty"`
	Source   string `json:"source"`
	Model    string `json:"model,omitempty"`
	Key      string `json:"cache_key"`
	SHA      string `json:"sha256"`
	DurMS    int    `json:"dur_ms"`
}

// Manifest is the openings manifest.
type Manifest struct {
	Synth    string    `json:"synth"`
	Openings []Opening `json:"openings"`
}

// VoiceFor is the scenario's voice: its own, or one picked by its id
// (the same for every repeat, so the opening clip is fixed).
func VoiceFor(sc Scenario) string {
	if sc.Audio.Voice != "" {
		return sc.Audio.Voice
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(sc.ID))
	return speech.Voices[int(h.Sum32()%uint32(len(speech.Voices)))]
}

// OpeningRequest is the TTS request of the scenario's opening.
func OpeningRequest(sc Scenario) speech.Request {
	lang := sc.Language
	if lang == "" {
		lang = "en"
	}
	if hasHanText(sc.Opening) {
		lang = "zh"
	}
	style := sc.Audio.Style
	if style == "mixed" {
		style = speech.StyleNeutral
	}
	return speech.Request{Text: sc.Opening, Lang: lang, Voice: VoiceFor(sc), Style: style}
}

// RenderOpenings renders every scenario's opening (through the cache)
// and returns the manifest.
func RenderOpenings(ctx context.Context, syn speech.Synth, scs []Scenario) (Manifest, error) {
	m := Manifest{Synth: syn.ID()}
	for _, sc := range scs {
		if sc.Opening == "" {
			continue
		}
		req := OpeningRequest(sc)
		clip, err := syn.Synthesize(ctx, req)
		if err != nil {
			return m, fmt.Errorf("%s: %w", sc.ID, err)
		}
		m.Openings = append(m.Openings, Opening{Scenario: sc.ID, Text: req.Text, Lang: req.Lang, Voice: req.Voice, Style: req.Style, Source: clip.Source, Model: clip.Model, Key: clip.Key, SHA: clip.SHA, DurMS: clip.DurationMS()})
	}
	return m, nil
}

// LoadManifest reads a manifest.
func LoadManifest(path string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// Save writes the manifest.
func (m Manifest) Save(path string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Check compares the manifest with the scenarios: every opening listed
// with the same text and voice. It returns the problems.
func (m Manifest) Check(scs []Scenario) []string {
	by := map[string]Opening{}
	for _, o := range m.Openings {
		by[o.Scenario] = o
	}
	var out []string
	for _, sc := range scs {
		if sc.Opening == "" {
			continue
		}
		o, ok := by[sc.ID]
		req := OpeningRequest(sc)
		switch {
		case !ok:
			out = append(out, sc.ID+": no opening in the manifest (run sheepdog bench render)")
		case o.Text != req.Text || o.Voice != req.Voice || o.Style != req.Style || o.Lang != req.Lang:
			out = append(out, sc.ID+": the manifest's opening is stale (text, voice, style or language changed; run sheepdog bench render)")
		}
	}
	return out
}
