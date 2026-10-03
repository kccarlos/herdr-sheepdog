package speech

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
)

// fakeTTS answers every call with 0.5 s of a 24 kHz tone and records the
// prompt and voice.
type fakeTTS struct {
	calls  atomic.Int32
	prompt string
	voice  string
	fail   bool
}

func (f *fakeTTS) Generate(_ context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	f.calls.Add(1)
	if f.fail {
		return nil, errors.New("Error 429, RESOURCE_EXHAUSTED")
	}
	f.prompt = contents[0].Parts[0].Text
	f.voice = cfg.SpeechConfig.VoiceConfig.PrebuiltVoiceConfig.VoiceName
	s := make([]int16, 12000)
	for i := range s {
		s[i] = pcm.Clip16(8000 * math.Sin(2*math.Pi*440*float64(i)/24000))
	}
	return &genai.GenerateContentResponse{
		Candidates:    []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{InlineData: &genai.Blob{MIMEType: "audio/L16;codec=pcm;rate=24000", Data: pcm.ToBytes(s)}}}}}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 12, CandidatesTokenCount: 13},
	}, nil
}

func TestGeminiStyleVoiceAndResample(t *testing.T) {
	f := &fakeTTS{}
	g := &Gemini{Model: f, ModelID: DefaultModel}
	c, err := g.Synthesize(context.Background(), Request{Text: "what's the codex one doing", Lang: "en", Voice: "Charon", Style: StyleTired})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(f.prompt, "Say slowly and tiredly") || !strings.HasSuffix(f.prompt, ": what's the codex one doing") {
		t.Errorf("prompt = %q", f.prompt)
	}
	if f.voice != "Charon" || c.Voice != "Charon" || c.Source != "gemini" || c.OutTokens != 13 {
		t.Errorf("clip = %+v voice %s", c, f.voice)
	}
	if len(c.Samples) != 8000 || c.DurationMS() != 500 {
		t.Errorf("resampled to %d samples (%d ms), want 8000 (500 ms)", len(c.Samples), c.DurationMS())
	}
	if p := Prompt(Request{Text: "先不要", Lang: "zh"}); !strings.Contains(p, "Mandarin") || !strings.HasSuffix(p, ": 先不要") {
		t.Errorf("zh prompt = %q", p)
	}
	if rateOf("audio/l16; rate=16000; channels=1") != 16000 || rateOf("audio/pcm") != 0 {
		t.Error("rateOf")
	}
}

func TestCacheRendersOnce(t *testing.T) {
	f := &fakeTTS{}
	c := &Cache{Dir: t.TempDir(), Inner: &Gemini{Model: f, ModelID: DefaultModel}}
	r := Request{Text: "approve it", Lang: "en", Voice: "Kore"}
	a, err := c.Synthesize(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Synthesize(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 || !b.Cached || a.Cached || a.SHA != b.SHA || len(b.Samples) != len(a.Samples) || b.Source != "gemini" {
		t.Fatalf("calls=%d a=%+v b=%+v", f.calls.Load(), a, b)
	}
	r.Style = StyleHurried
	if _, err := c.Synthesize(context.Background(), r); err != nil || f.calls.Load() != 2 {
		t.Fatalf("a different style must render again (calls %d, %v)", f.calls.Load(), err)
	}
	if Key("gemini/x", r) == Key("gemini/y", r) {
		t.Fatal("the model is not in the key")
	}
}

type stubSynth struct {
	id  string
	err error
}

func (s stubSynth) ID() string { return s.id }
func (s stubSynth) Synthesize(context.Context, Request) (Clip, error) {
	if s.err != nil {
		return Clip{}, s.err
	}
	return Clip{Samples: []int16{1}, Source: s.id}, nil
}

func TestFallback(t *testing.T) {
	f := Fallback{stubSynth{id: "gemini", err: errors.New("quota")}, stubSynth{id: "say"}}
	c, err := f.Synthesize(context.Background(), Request{Text: "x"})
	if err != nil || c.Source != "say" {
		t.Fatalf("fallback = %+v %v", c, err)
	}
	if _, err := (Fallback{stubSynth{id: "a", err: errors.New("one")}}).Synthesize(context.Background(), Request{}); err == nil || !strings.Contains(err.Error(), "one") {
		t.Fatalf("all failing = %v", err)
	}
	if f.ID() != "gemini|say" {
		t.Fatal(f.ID())
	}
}

func TestDisfluent(t *testing.T) {
	if got := Disfluent("approve the tests", "en", 0, rand.New(rand.NewSource(1))); got != "approve the tests" {
		t.Fatalf("p=0 changed the text: %q", got)
	}
	en := Disfluent("approve the README one please", "en", 1, rand.New(rand.NewSource(3)))
	if !strings.Contains(en, "…") || !strings.HasSuffix(en, "please") {
		t.Fatalf("en = %q, want a restart and a filler", en)
	}
	zh := Disfluent("让测试那个工人先不要动", "zh", 1, rand.New(rand.NewSource(2)))
	if !strings.ContainsAny(zh, "…，") || !strings.Contains(zh, "先不要动") {
		t.Fatalf("zh = %q", zh)
	}
	a := Disfluent("check on the codex worker", "en", 0.5, rand.New(rand.NewSource(9)))
	b := Disfluent("check on the codex worker", "en", 0.5, rand.New(rand.NewSource(9)))
	if a != b {
		t.Fatal("not deterministic for a seed")
	}
}

func TestTrim(t *testing.T) {
	s := make([]int16, 16000)
	for i := 8000; i < 8160; i++ {
		s[i] = 3000
	}
	got := Trim(s, 100, 50)
	if len(got) != 160+2*800 {
		t.Fatalf("trimmed to %d samples, want %d", len(got), 160+1600)
	}
	if Trim(make([]int16, 10), 100, 50) != nil {
		t.Fatal("silence should trim to nothing")
	}
}
