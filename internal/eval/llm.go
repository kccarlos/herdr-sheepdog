package eval

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// The judge and the simulated user are plain text models: a
// GenerateContent call, the brain.Model surface. Three backends serve
// them: Gemini Flash-Lite on Vertex AI (the default for live runs: cheap,
// and the same client the orchestrator's vertex provider uses), a local Ollama
// server (small runs; slow with many parallel sessions), and a scripted
// fake (offline, tests, CI).

// Backend kinds for --judge and --simuser.
const (
	BackendVertex = "vertex"
	BackendOllama = "ollama"
	// BackendDeepSeek is DeepSeek over the OpenAI-compatible adapter
	// (DEEPSEEK_API_KEY), thinking off: the simulated user's fallback
	// while Vertex is unavailable.
	BackendDeepSeek = "deepseek"
	BackendFake     = "fake"
	BackendNone     = "none"
)

// DefaultJudgeKind is the judge backend when neither --judge nor
// SHEEPDOG_JUDGE says otherwise: Claude Sonnet through the Claude Code
// CLI (claudecli.go), the winner of the 2026-10-06 bake-off
// (docs/dev/eval/calibration/README.md). Flash-Lite stays selectable with
// --judge vertex.
const DefaultJudgeKind = BackendClaude

// DefaultVertexModel is the Vertex model the simulated user (and a
// --judge vertex judge) calls. Checked live 2026-10-02 at location global with the project's API
// key: gemini-3.1-flash-lite and gemini-3.5-flash-lite both answer
// generateContent; gemini-3.8-flash-lite and the -preview ids are 404.
const DefaultVertexModel = "gemini-3.1-flash-lite"

// AltJudgeModel is the Flash-Lite id the judge's and the simulated
// user's retries alternate with on Vertex (a separate rate-limit bucket).
const AltJudgeModel = "gemini-3.5-flash-lite"

// DefaultOllamaModel and DefaultOllamaURL are the local fallback.
const (
	DefaultOllamaModel = "gemma4:latest"
	DefaultOllamaURL   = "http://localhost:11434/v1"
	// DefaultDeepSeekModel is the deepseek backend's model.
	DefaultDeepSeekModel = "deepseek-flash"
)

// BackendOptions selects and configures one text model backend.
type BackendOptions struct {
	Kind  string // vertex | deepseek | ollama | claude | fake
	Model string // model id; empty takes the backend's default
	// VertexAPIKey and VertexProject are the [gemini.vertex] credentials
	// (VERTEX_API_KEY / VERTEX_PROJECT); never logged.
	VertexAPIKey, VertexProject string
	// OllamaURL is the OpenAI-compatible root of the Ollama server.
	OllamaURL string
	// DeepSeekAPIKey is DEEPSEEK_API_KEY for the deepseek backend; never
	// logged.
	DeepSeekAPIKey string
}

// NewBackend returns the model for o and the model id to pass to it.
// fake returns (nil, "fake"): the caller supplies its scripted stand-in.
func NewBackend(o BackendOptions, log logging.Logger) (brain.Model, string, error) {
	switch o.Kind {
	case BackendVertex:
		m, err := brain.NewVertexModel(o.VertexAPIKey, o.VertexProject, log)
		if err != nil {
			return nil, "", err
		}
		return m, orDefault(o.Model, DefaultVertexModel), nil
	case BackendOllama:
		m, err := brain.NewOpenAICompatModel(brain.OpenAICompatConfig{Provider: "ollama", BaseURL: orDefault(o.OllamaURL, DefaultOllamaURL), APIKey: "ollama", Log: log})
		if err != nil {
			return nil, "", err
		}
		return m, orDefault(o.Model, DefaultOllamaModel), nil
	case BackendDeepSeek:
		off := false
		m, err := brain.NewOpenAICompatModel(brain.OpenAICompatConfig{Provider: "deepseek", BaseURL: brain.DeepSeekBaseURL, APIKey: o.DeepSeekAPIKey, Thinking: &off, Log: log})
		if err != nil {
			return nil, "", err
		}
		return m, orDefault(o.Model, DefaultDeepSeekModel), nil
	case BackendClaude:
		return &ClaudeCLI{}, orDefault(o.Model, DefaultClaudeModel), nil
	case BackendFake:
		return nil, BackendFake, nil
	}
	return nil, "", fmt.Errorf("unknown backend %q (claude, vertex, deepseek, ollama or fake)", o.Kind)
}

// Usage is the token count of one role's calls to one model.
type Usage struct {
	Calls     int `json:"calls"`
	In        int `json:"in"`
	Out       int `json:"out"`
	Reasoning int `json:"reasoning,omitempty"`
	Cached    int `json:"cached,omitempty"`
}

func (u *Usage) add(o Usage) {
	u.Calls += o.Calls
	u.In += o.In
	u.Out += o.Out
	u.Reasoning += o.Reasoning
	u.Cached += o.Cached
}

// Price is a model's list price in US dollars per million tokens.
// Reasoning tokens bill as output.
type Price struct{ In, Out float64 }

// Prices are ESTIMATES for spend logging, not quotes: the published list
// prices as last known for the Gemini 3.x / DeepSeek families, which
// could not be verified from here for every id. Unknown models cost
// UnknownPrice (deliberately high, so an unpriced model never reads as
// free). Ollama is local.
var Prices = map[string]Price{
	"gemini-3.1-flash-lite":  {In: 0.25, Out: 1.50},
	"gemini-3.5-flash-lite":  {In: 0.25, Out: 1.50},
	"gemini-3.5-flash":       {In: 0.30, Out: 2.50},
	"gemini-3.8-flash":       {In: 0.50, Out: 3.00},
	"gemini-3.1-pro-preview": {In: 2.00, Out: 12.00},
	// The Claude judge runs through the Claude Code CLI on the user's
	// subscription: no API spend (its list-price equivalent is in
	// docs/dev/eval/calibration/README.md).
	DefaultClaudeModel: {},
	"deepseek-flash":   {In: 0.30, Out: 0.60},
	"deepseek-chat":    {In: 0.30, Out: 0.60},
	// The voice bench (internal/bench): Gemini TTS on Vertex, and the
	// Live model's session tokens (voice.usage), priced at the native
	// audio rates on the high side (audio in, audio out) since the log
	// does not split text from audio.
	"gemini-2.5-flash-tts":         {In: 0.50, Out: 10.00},
	"gemini-3.1-flash-tts-preview": {In: 1.00, Out: 20.00},
	"gemini-3.8-live":              {In: 3.00, Out: 12.00},
	"gemma4:latest":                {},
	BackendFake:                    {},
	braintestFakeModel:             {},
}

// UnknownPrice is what a model missing from Prices is charged at.
var UnknownPrice = Price{In: 2.00, Out: 10.00}

// braintestFakeModel is the scripted orchestrator's model id offline.
const braintestFakeModel = "fake-reflex"

// Cost is u's estimated price at model's list price.
func Cost(model string, u Usage) float64 {
	p, ok := Prices[model]
	if !ok {
		p = UnknownPrice
	}
	return (float64(u.In)*p.In + float64(u.Out+u.Reasoning)*p.Out) / 1e6
}

// Meter counts tokens per role ("orchestrator", "judge", "simuser") and model.
// Safe for concurrent use.
type Meter struct {
	mu sync.Mutex
	m  map[[2]string]*Usage
}

// NewMeter returns an empty Meter.
func NewMeter() *Meter { return &Meter{m: map[[2]string]*Usage{}} }

// Add records u for role and model.
func (m *Meter) Add(role, model string, u Usage) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := [2]string{role, model}
	if m.m[k] == nil {
		m.m[k] = &Usage{}
	}
	m.m[k].add(u)
}

// MeterLine is one row of a Meter's report.
type MeterLine struct {
	Role    string  `json:"role"`
	Model   string  `json:"model"`
	Usage   Usage   `json:"usage"`
	CostUSD float64 `json:"cost_usd_estimate"`
}

// Lines returns the usage rows, sorted by role then model.
func (m *Meter) Lines() []MeterLine {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MeterLine, 0, len(m.m))
	for k, u := range m.m {
		out = append(out, MeterLine{Role: k[0], Model: k[1], Usage: *u, CostUSD: Cost(k[1], *u)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Total is the estimated spend so far.
func (m *Meter) Total() float64 {
	t := 0.0
	for _, l := range m.Lines() {
		t += l.CostUSD
	}
	return t
}

// Merge adds every row of o into m.
func (m *Meter) Merge(o *Meter) {
	for _, l := range o.Lines() {
		m.Add(l.Role, l.Model, l.Usage)
	}
}

// metered is a brain.Model that records each call's usage on a Meter.
type metered struct {
	inner brain.Model
	meter *Meter
	role  string
}

// Metered wraps m so every call's tokens land on meter under role. Do
// not wrap the orchestrator's own model: the loop needs DeepSeek's
// MessageModel, which a wrapper hides (the orchestrator's tokens are counted
// from its brain.step events instead).
func Metered(m brain.Model, meter *Meter, role string) brain.Model {
	if m == nil || meter == nil {
		return m
	}
	return &metered{inner: m, meter: meter, role: role}
}

func (m *metered) Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	resp, err := m.inner.Generate(ctx, model, contents, cfg)
	u := Usage{Calls: 1}
	if resp != nil && resp.UsageMetadata != nil {
		md := resp.UsageMetadata
		u.In, u.Out, u.Reasoning, u.Cached = int(md.PromptTokenCount), int(md.CandidatesTokenCount), int(md.ThoughtsTokenCount), int(md.CachedContentTokenCount)
	}
	m.meter.Add(m.role, model, u)
	return resp, err
}

// textOf returns a response's text parts joined (thoughts excluded).
func textOf(resp *genai.GenerateContentResponse) string {
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range resp.Candidates[0].Content.Parts {
		if p.Text != "" && !p.Thought {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// errNoModel is returned when a live component has no model.
var errNoModel = errors.New("eval: no model configured")

// patient is a brain.Model for the judge and the simulated user (never
// the orchestrator, whose no-retry policy is part of what is evaluated): at most
// cap(sem) calls in flight, and a rate limit or a transient server error
// is retried with jittered exponential backoff (capped at 30 s). With an
// alternate model id, retries alternate between the two: Vertex rate
// limits each model separately, and Flash-Lite's shared quota ran out
// under a 5-way campaign (2026-10-03: 45 of 130 runs lost the simulated
// user to 429s).
type patient struct {
	inner brain.Model
	sem   chan struct{}
	tries int
	base  time.Duration
	alt   string
}

// Patient wraps m with a concurrency limit shared by every wrapper built
// on the same sem, and up to tries attempts per call (backoff from 2 s).
// alt, when set, is a second model id the retries alternate with.
func Patient(m brain.Model, sem chan struct{}, tries int, alt string) brain.Model {
	if m == nil {
		return nil
	}
	return &patient{inner: m, sem: sem, tries: max(tries, 1), base: 2 * time.Second, alt: alt}
}

func (p *patient) Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	var err error
	for i := 0; i < p.tries; i++ {
		id := model
		if i > 0 {
			d := min(p.base<<(i-1), 30*time.Second)
			d += time.Duration(rand.Int64N(int64(d)/2 + 1))
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if p.alt != "" && i%2 == 1 {
				id = p.alt
			}
		}
		if p.sem != nil {
			select {
			case p.sem <- struct{}{}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		var resp *genai.GenerateContentResponse
		resp, err = p.inner.Generate(ctx, id, contents, cfg)
		if p.sem != nil {
			<-p.sem
		}
		if err == nil || !transient(err) {
			return resp, err
		}
	}
	return nil, err
}

// transient reports whether err is worth retrying: a rate limit, an
// overloaded or unavailable server, a server-side deadline.
func transient(err error) bool {
	if errors.Is(err, ErrClaudeUsageLimit) {
		return false
	}
	if isRateLimited(err) {
		return true
	}
	s := strings.ToLower(err.Error())
	for _, n := range []string{"error 500", "error 502", "error 503", "error 504", "unavailable", "overloaded", "deadline_exceeded", "internal error"} {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}
