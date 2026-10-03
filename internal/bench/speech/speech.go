// Package speech renders the simulated CEO's words as 16 kHz PCM16 mono,
// the /ws input format: Gemini TTS on Vertex AI (several voices, English
// and Chinese, a speaking style per utterance), macOS `say` as the
// offline fallback, and a cache keyed by everything that shapes the
// clip, so a campaign renders each line once. Disfluent rewrites the
// text the way people actually talk ("um, the… the README one").
package speech

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/audioe2e"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wav"
)

// Rate is the output sample rate (the /ws input rate).
const Rate = 16000

// DefaultModel is the Gemini TTS model on Vertex AI. Checked live
// 2026-10-03 with the project's API key at location global through the
// genai SDK: gemini-2.5-flash-tts, gemini-2.5-pro-tts,
// gemini-2.5-flash-preview-tts, gemini-2.5-flash-lite-preview-tts and
// gemini-3.1-flash-tts-preview answer with 24 kHz L16 audio;
// gemini-3.1-flash-tts and gemini-3.5-flash-tts are 404.
const DefaultModel = "gemini-2.5-flash-tts"

// AltModel is the newer preview TTS model, selectable per run.
const AltModel = "gemini-3.1-flash-tts-preview"

// Voices are the prebuilt Gemini voices the bench rotates through (two
// female, two male); every one speaks English and Chinese.
var Voices = []string{"Kore", "Charon", "Aoede", "Puck"}

// Styles a line can be spoken in ("" is neutral).
const (
	StyleNeutral  = ""
	StyleTired    = "tired"
	StyleHurried  = "hurried"
	StyleCasual   = "casual"
	StyleHesitant = "hesitant"
)

// Styles lists the known styles, neutral first.
var Styles = []string{StyleNeutral, StyleTired, StyleHurried, StyleCasual, StyleHesitant}

// stylePrompt is the instruction in front of the text (the Gemini TTS
// way to steer delivery: "Say tiredly: …").
var stylePrompt = map[string]string{
	StyleNeutral:  "Say naturally, like someone talking on the phone",
	StyleTired:    "Say slowly and tiredly, in a low voice, like someone exhausted late at night",
	StyleHurried:  "Say quickly and a little impatiently, like someone in a hurry between meetings",
	StyleCasual:   "Say casually and relaxed, like talking to a colleague",
	StyleHesitant: "Say hesitantly, with small pauses, like someone unsure how to put it",
}

// Request is one line to render.
type Request struct {
	Text string `json:"text"`
	// Lang is en or zh (the say fallback picks its voice by it).
	Lang  string `json:"lang,omitempty"`
	Voice string `json:"voice,omitempty"`
	Style string `json:"style,omitempty"`
}

// Clip is a rendered line.
type Clip struct {
	Samples []int16 `json:"-"`
	// Source is gemini or say; Model and Voice what rendered it.
	Source string `json:"source"`
	Model  string `json:"model,omitempty"`
	Voice  string `json:"voice"`
	// Key is the cache key, SHA is the hash of the PCM bytes (what a
	// manifest pins).
	Key    string `json:"key"`
	SHA    string `json:"sha256"`
	Cached bool   `json:"-"`
	// InTokens and OutTokens are the TTS call's usage (0 when cached
	// or rendered by say).
	InTokens  int `json:"-"`
	OutTokens int `json:"-"`
}

// DurationMS is the clip's length.
func (c Clip) DurationMS() int { return pcm.DurationMS(len(c.Samples), Rate) }

// Synth renders a line.
type Synth interface {
	Synthesize(ctx context.Context, r Request) (Clip, error)
	// ID names the synthesizer and its model for the cache key.
	ID() string
}

// ValidStyle reports whether s is a known style.
func ValidStyle(s string) bool {
	_, ok := stylePrompt[s]
	return ok
}

// Gemini is Gemini TTS over a genai GenerateContent model (the brain's
// Vertex client: brain.NewVertexModel).
type Gemini struct {
	Model   brain.Model
	ModelID string
}

// NewGemini returns Gemini TTS on Vertex AI with the [gemini.vertex]
// key and project (location global).
func NewGemini(apiKey, project, model string) (*Gemini, error) {
	m, err := brain.NewVertexModel(apiKey, project, nil)
	if err != nil {
		return nil, err
	}
	if model == "" {
		model = DefaultModel
	}
	return &Gemini{Model: m, ModelID: model}, nil
}

// ID implements Synth.
func (g *Gemini) ID() string { return "gemini/" + g.ModelID }

// Prompt is the text sent to the TTS model for r.
func Prompt(r Request) string {
	p, ok := stylePrompt[r.Style]
	if !ok {
		p = stylePrompt[StyleNeutral]
	}
	if r.Lang == "zh" {
		p += ", in Mandarin Chinese"
	}
	return p + ": " + r.Text
}

// Synthesize implements Synth.
func (g *Gemini) Synthesize(ctx context.Context, r Request) (Clip, error) {
	voice := r.Voice
	if voice == "" {
		voice = Voices[0]
	}
	cfg := &genai.GenerateContentConfig{
		ResponseModalities: []string{"AUDIO"},
		SpeechConfig:       &genai.SpeechConfig{VoiceConfig: &genai.VoiceConfig{PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{VoiceName: voice}}},
	}
	resp, err := g.Model.Generate(ctx, g.ModelID, genai.Text(Prompt(r)), cfg)
	if err != nil {
		return Clip{}, fmt.Errorf("gemini tts: %w", err)
	}
	var raw []byte
	rate := 24000
	if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, p := range resp.Candidates[0].Content.Parts {
			if p.InlineData != nil && len(p.InlineData.Data) > 0 {
				raw = append(raw, p.InlineData.Data...)
				if r := rateOf(p.InlineData.MIMEType); r > 0 {
					rate = r
				}
			}
		}
	}
	if len(raw) == 0 {
		return Clip{}, errors.New("gemini tts: no audio in the response")
	}
	c := Clip{Samples: pcm.Resample(pcm.FromBytes(raw), rate, Rate), Source: "gemini", Model: g.ModelID, Voice: voice}
	if resp.UsageMetadata != nil {
		c.InTokens, c.OutTokens = int(resp.UsageMetadata.PromptTokenCount), int(resp.UsageMetadata.CandidatesTokenCount)
	}
	return c, nil
}

// rateOf reads rate=N from an audio MIME type ("audio/L16;codec=pcm;rate=24000").
func rateOf(mime string) int {
	for _, part := range strings.Split(mime, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(k, "rate") {
			n, err := strconv.Atoi(v)
			if err == nil {
				return n
			}
		}
	}
	return 0
}

// Say is macOS `say` (the offline fallback): no styles, one voice per
// language.
type Say struct {
	// CacheDir is where `say` writes its own renders.
	CacheDir string
	// Voices maps a language to a `say` voice (default en Samantha, zh
	// Tingting).
	Voices map[string]string
}

// ID implements Synth.
func (s *Say) ID() string { return "say" }

// Synthesize implements Synth.
func (s *Say) Synthesize(ctx context.Context, r Request) (Clip, error) {
	voice := s.Voices[r.Lang]
	if voice == "" {
		voice = map[string]string{"zh": "Tingting"}[r.Lang]
	}
	if voice == "" {
		voice = "Samantha"
	}
	samples, err := audioe2e.Say(ctx, r.Text, voice, s.CacheDir)
	if err != nil {
		return Clip{}, err
	}
	return Clip{Samples: samples, Source: "say", Voice: voice}, nil
}

// Fallback tries each synthesizer in order and returns the first clip.
type Fallback []Synth

// ID implements Synth.
func (f Fallback) ID() string {
	ids := make([]string, len(f))
	for i, s := range f {
		ids[i] = s.ID()
	}
	return strings.Join(ids, "|")
}

// Synthesize implements Synth.
func (f Fallback) Synthesize(ctx context.Context, r Request) (Clip, error) {
	var errs []error
	for _, s := range f {
		c, err := s.Synthesize(ctx, r)
		if err == nil {
			return c, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", s.ID(), err))
		if ctx.Err() != nil {
			break
		}
	}
	return Clip{}, errors.Join(errs...)
}

// Cache keeps rendered clips under Dir as 16 kHz WAV plus a JSON note,
// keyed by the synthesizer, its model and the request. Safe for
// concurrent use.
type Cache struct {
	Dir   string
	Inner Synth

	mu       sync.Mutex
	inflight map[string]*sync.Mutex
}

// ID implements Synth.
func (c *Cache) ID() string { return c.Inner.ID() }

// Key is the cache key of r under synthesizer id.
func Key(id string, r Request) string {
	b, _ := json.Marshal([]string{id, r.Lang, r.Voice, r.Style, r.Text})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:24]
}

// Path is where the clip of key lives.
func (c *Cache) Path(key string) string { return filepath.Join(c.Dir, key+".wav") }

// Synthesize implements Synth: the cached clip, or a fresh render saved
// for next time.
func (c *Cache) Synthesize(ctx context.Context, r Request) (Clip, error) {
	key := Key(c.Inner.ID(), r)
	lock := c.lock(key)
	lock.Lock()
	defer lock.Unlock()
	if clip, err := c.load(key); err == nil {
		return clip, nil
	}
	clip, err := c.Inner.Synthesize(ctx, r)
	if err != nil {
		return Clip{}, err
	}
	clip.Key = key
	clip.SHA = SHA(clip.Samples)
	if err := c.save(clip); err != nil {
		return Clip{}, err
	}
	return clip, nil
}

func (c *Cache) lock(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight == nil {
		c.inflight = map[string]*sync.Mutex{}
	}
	if c.inflight[key] == nil {
		c.inflight[key] = &sync.Mutex{}
	}
	return c.inflight[key]
}

func (c *Cache) load(key string) (Clip, error) {
	b, err := os.ReadFile(c.Path(key))
	if err != nil {
		return Clip{}, err
	}
	rate, raw, err := wav.Read(bytes.NewReader(b))
	if err != nil {
		return Clip{}, err
	}
	if rate != Rate {
		return Clip{}, fmt.Errorf("%s: %d Hz", c.Path(key), rate)
	}
	var clip Clip
	if meta, err := os.ReadFile(filepath.Join(c.Dir, key+".json")); err == nil {
		_ = json.Unmarshal(meta, &clip)
	}
	clip.Samples, clip.Key, clip.Cached = pcm.FromBytes(raw), key, true
	clip.SHA = SHA(clip.Samples)
	return clip, nil
}

func (c *Cache) save(clip Clip) error {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	if err := WriteWAV(c.Path(clip.Key), clip.Samples, Rate); err != nil {
		return err
	}
	meta, _ := json.MarshalIndent(clip, "", "  ")
	return os.WriteFile(filepath.Join(c.Dir, clip.Key+".json"), meta, 0o644)
}

// SHA is the hex SHA-256 of the samples' PCM16 bytes.
func SHA(s []int16) string {
	sum := sha256.Sum256(pcm.ToBytes(s))
	return hex.EncodeToString(sum[:])
}

// WriteWAV saves samples as a mono PCM16 WAV at rate.
func WriteWAV(path string, s []int16, rate int) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w, err := wav.NewWriter(f, rate)
	if err == nil {
		_, err = w.Write(pcm.ToBytes(s))
	}
	if err == nil {
		err = w.Close()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Trim cuts leading and trailing near-silence (below floor, Int16 LSB)
// down to keep milliseconds on each side, so a TTS clip's own padding
// does not add to the user's pause.
func Trim(s []int16, floor, keepMS int) []int16 {
	first, last := -1, -1
	for i, v := range s {
		if abs(int(v)) >= floor {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil
	}
	keep := Rate * keepMS / 1000
	return s[max(0, first-keep):min(len(s), last+keep+1)]
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
