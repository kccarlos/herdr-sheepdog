package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// Model is the one model call the brain makes, in Gemini's terms:
// *GenaiModel implements it over the Gemini API, *OpenAICompatModel
// translates it for an OpenAI-compatible endpoint, and tests inject a
// scripted fake (braintest). The compaction summary and the eval harness
// call it directly with plain contents.
type Model interface {
	Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// MessageModel is a Model that builds the provider's request from the
// window's own messages instead of genai contents (the loop prefers it
// when the Model implements it). It exists for provider-native data the
// genai types cannot carry: DeepSeek's reasoning_content, which must be
// passed back with every earlier assistant message while tools are
// declared. The adapter re-attaches it from Message.Raw for the messages
// whose Provider and Model are its own, and returns the assistant
// message it got (raw) so the loop can store it on the reply's
// transcript message for the same purpose; raw is nil when there is
// nothing to replay. cfg carries the system instruction, the tools and
// the answer-only tool config exactly as for Generate.
type MessageModel interface {
	GenerateMessages(ctx context.Context, model string, msgs []Message, cfg *genai.GenerateContentConfig) (resp *genai.GenerateContentResponse, raw json.RawMessage, err error)
}

// Provider ids: the key of the brain's provider map, the first half of
// the /debug picker's choice, and Message.Provider on what a model
// produced.
const (
	ProviderGemini   = "gemini"
	ProviderDeepSeek = "deepseek"
	// ProviderVertex is Gemini on Vertex AI: the same GenaiModel and model
	// ids as ProviderGemini, over the [gemini.vertex] key and project.
	ProviderVertex = "vertex"
)

// ModelInfo is one entry of a provider's model catalog, as the /debug
// model picker shows it.
type ModelInfo struct {
	// Name is the id passed to Model.Generate ("gemini-3.5-flash", no
	// "models/" prefix).
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	// InputTokenLimit / OutputTokenLimit are the provider's limits (0
	// when unknown).
	InputTokenLimit  int `json:"input_token_limit,omitempty"`
	OutputTokenLimit int `json:"output_token_limit,omitempty"`
	// Methods are the generation methods the provider lists for the
	// model; only models supporting generateContent are listed at all.
	Methods []string `json:"methods,omitempty"`
	// Recommended marks a plausible brain model: a text model of the
	// gemini / gemma families that is not a speech, image, video, music,
	// embedding, robotics or live variant. Whether it supports function
	// calling is not discoverable from the list: a bad pick surfaces as a
	// brain.step error and the daemon answers for itself.
	Recommended bool `json:"recommended"`
}

// ModelCatalog lists the models a Model implementation can run; the
// /debug model picker discovers its choices through it. *GenaiModel
// implements it over the Gemini model list.
type ModelCatalog interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ErrAllKeysRateLimited is returned when every key of the pool was rate
// limited on one call.
var ErrAllKeysRateLimited = errors.New("brain: all API keys rate limited")

// ErrNoCatalog is returned by Brain.Models when the model does not
// implement ModelCatalog.
var ErrNoCatalog = errors.New("brain: the model has no catalog")

// catalogPageSize asks for the whole Gemini model list in one page (the
// API's maximum), so a catalog fetch is one request.
const catalogPageSize = 1000

// notRecommended marks the model families the brain cannot use: speech,
// image, video and music generation, transcription, embeddings, robotics,
// computer use and the Live API.
var notRecommended = regexp.MustCompile(`tts|image|transcribe|lyria|robotics|computer-use|omni|antigravity|deep-research|live|embedding|banana`)

// GenaiModel calls Gemini's GenerateContent with a key pool: a 429 on one
// key moves to the next, once per key, never a retry on the same key
// (the free tier needs a cool-down, not a hot loop). One client per key is
// built lazily. Safe for concurrent use.
type GenaiModel struct {
	keys []string
	log  logging.Logger
	// provider is the id brain.rate_limited names (ProviderGemini or
	// ProviderVertex).
	provider string
	// catalog, when set, is what ListModels returns without a request
	// (Vertex: its model list does not take an API key).
	catalog []ModelInfo
	// noMinimal holds the models that refused thinking level minimal
	// (Generate).
	noMinimal sync.Map

	mu      sync.Mutex
	idx     int
	clients map[int]*genai.Client
	// newClient overrides the genai constructor (tests).
	newClient func(ctx context.Context, key string) (*genai.Client, error)
}

// NewGenaiModel returns a GenaiModel over the key pool. log may be nil.
func NewGenaiModel(keys []string, log logging.Logger) (*GenaiModel, error) {
	if len(keys) == 0 {
		return nil, errors.New("brain: no API keys configured")
	}
	if log == nil {
		log = logging.NopLogger{}
	}
	return &GenaiModel{keys: append([]string(nil), keys...), log: log, provider: ProviderGemini, clients: map[int]*genai.Client{}, newClient: func(ctx context.Context, key string) (*genai.Client, error) {
		return genai.NewClient(ctx, &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI})
	}}, nil
}

// VertexLocation is where the brain calls Gemini on Vertex AI: always
// the global endpoint, whatever gemini.vertex.location says. Checked live
// 2026-10-01 with the API key: the text models (gemini-3.8-flash,
// gemini-3.5-flash, gemini-3.5-flash-lite) answer at global and are "not
// found" at us-central1, while the Live model the voice uses is served
// at us-central1 and not at global (docs/research/
// vertex-live-spike-2026-09-29.md), so the two cannot share one location.
const VertexLocation = "global"

// VertexBaseURL is the host of VertexLocation. Set explicitly: the SDK's
// own choice for an API key is the same today, but the brain must not
// follow gemini.vertex.location.
const VertexBaseURL = "https://aiplatform.googleapis.com/"

// VertexModels is the Vertex provider's catalog for the /debug picker: a
// static list, because Vertex's model listing (publishers/google/models,
// and the OpenAI-compatible endpoints/openapi/models) refuses an API key
// (401 / 404). Every entry answered generateContent at global on
// 2026-10-01; gemini-3.8-pro, gemini-3.5-pro and gemini-3.8-flash-lite
// did not (404).
var VertexModels = []ModelInfo{
	{Name: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", Methods: []string{"generateContent"}, Recommended: true},
	{Name: "gemini-3.5-flash", DisplayName: "Gemini 3.5 Flash", Methods: []string{"generateContent"}, Recommended: true},
	{Name: "gemini-3.5-flash-lite", DisplayName: "Gemini 3.5 Flash-Lite", Methods: []string{"generateContent"}, Recommended: true},
}

// NewVertexModel returns a GenaiModel calling Gemini on Vertex AI with
// one API key (bound to a service account; no OAuth) in project, at
// VertexLocation. It is the Gemini provider's model, so thought
// signatures round-trip exactly as there (Message.Raw holds the model
// content); a 429 logs brain.rate_limited once and fails the call with
// ErrAllKeysRateLimited, no retry. log may be nil.
func NewVertexModel(apiKey, project string, log logging.Logger) (*GenaiModel, error) {
	return newVertexModel(apiKey, project, VertexBaseURL, log)
}

func newVertexModel(apiKey, project, baseURL string, log logging.Logger) (*GenaiModel, error) {
	switch {
	case apiKey == "":
		return nil, errors.New("brain: vertex: no API key configured")
	case project == "":
		return nil, errors.New("brain: vertex: no project configured")
	}
	m, err := NewGenaiModel([]string{apiKey}, log)
	if err != nil {
		return nil, fmt.Errorf("brain: vertex: %w", err)
	}
	m.provider = ProviderVertex
	m.catalog = VertexModels
	m.newClient = func(ctx context.Context, key string) (*genai.Client, error) {
		return genai.NewClient(ctx, vertexClientConfig(key, project, baseURL))
	}
	return m, nil
}

// vertexClientConfig is the genai configuration for Vertex AI with an
// API key: the SDK sends it as x-goog-api-key, skips Application Default
// Credentials, and builds projects/{project}/locations/global/publishers/
// google/models/{model} from Project and Location.
func vertexClientConfig(apiKey, project, baseURL string) *genai.ClientConfig {
	return &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		APIKey:      apiKey,
		Project:     project,
		Location:    VertexLocation,
		HTTPOptions: genai.HTTPOptions{BaseURL: baseURL},
	}
}

// Generate implements Model. A model without a minimal thinking level
// (thinkingOff, an event turn's) gets low, its least, rather than a 400
// on every event turn: a Pro model up front, and any other model once it
// has refused minimal (gemini-3.8-flash does, on AI Studio and Vertex,
// checked 2026-10-01): that one 400 is retried at low right away and the
// model remembered, so it costs one extra request per run.
func (m *GenaiModel) Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	minimal := cfg != nil && cfg.ThinkingConfig != nil && cfg.ThinkingConfig.ThinkingLevel == genai.ThinkingLevelMinimal
	_, refused := m.noMinimal.Load(model)
	if minimal && (strings.Contains(model, "-pro") || refused) {
		cfg, minimal = thinkingLow(cfg), false
	}
	var resp *genai.GenerateContentResponse
	call := func(client *genai.Client) error {
		var err error
		resp, err = client.Models.GenerateContent(ctx, model, contents, cfg)
		return err
	}
	err := m.withKeys(ctx, model, call)
	if minimal && minimalRefused(err) {
		m.noMinimal.Store(model, true)
		cfg = thinkingLow(cfg)
		err = m.withKeys(ctx, model, call)
	}
	return resp, err
}

// thinkingLow is cfg at thinking level low.
func thinkingLow(cfg *genai.GenerateContentConfig) *genai.GenerateContentConfig {
	c := *cfg
	c.ThinkingConfig = &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelLow}
	return &c
}

// minimalRefused reports the 400 a model without a minimal thinking
// level answers ("Thinking level is unsupported: THINKING_LEVEL_MINIMAL").
func minimalRefused(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "Thinking level is unsupported") && strings.Contains(s, "MINIMAL")
}

// ListModels implements ModelCatalog: the Gemini model list (a free
// call, not billed against the per-model quota), filtered to the models
// that support generateContent, recommended ones first then newest name
// first. The key pool applies as for Generate.
func (m *GenaiModel) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if m.catalog != nil {
		return append([]ModelInfo(nil), m.catalog...), nil
	}
	var raw []*genai.Model
	err := m.withKeys(ctx, "", func(client *genai.Client) error {
		raw = raw[:0]
		page, err := client.Models.List(ctx, &genai.ListModelsConfig{PageSize: catalogPageSize})
		for err == nil {
			raw = append(raw, page.Items...)
			page, err = page.Next(ctx)
		}
		if errors.Is(err, genai.ErrPageDone) {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return catalogFromModels(raw), nil
}

// catalogFromModels maps the provider's list onto ModelInfo (see
// ListModels for the filter and order).
func catalogFromModels(raw []*genai.Model) []ModelInfo {
	out := make([]ModelInfo, 0, len(raw))
	for _, gm := range raw {
		if gm == nil || !supports(gm.SupportedActions, "generateContent") {
			continue
		}
		name := strings.TrimPrefix(gm.Name, "models/")
		out = append(out, ModelInfo{
			Name: name, DisplayName: gm.DisplayName, Description: gm.Description,
			InputTokenLimit: int(gm.InputTokenLimit), OutputTokenLimit: int(gm.OutputTokenLimit),
			Methods: append([]string(nil), gm.SupportedActions...), Recommended: recommended(name),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Recommended != out[j].Recommended {
			return out[i].Recommended
		}
		return out[i].Name > out[j].Name
	})
	return out
}

// recommended reports whether name looks like a text model the brain can
// run (ModelInfo.Recommended).
func recommended(name string) bool {
	return (strings.HasPrefix(name, "gemini-") || strings.HasPrefix(name, "gemma-")) && !notRecommended.MatchString(name)
}

func supports(methods []string, want string) bool {
	for _, m := range methods {
		if m == want {
			return true
		}
	}
	return false
}

// withKeys runs call with the current key's client, moving to the next
// key on a 429 (once per key, never a retry on the same key). model is
// for the brain.rate_limited event only.
func (m *GenaiModel) withKeys(ctx context.Context, model string, call func(*genai.Client) error) error {
	var last error
	for attempt := 0; attempt < len(m.keys); attempt++ {
		idx, client, err := m.client(ctx)
		if err != nil {
			return err
		}
		err = call(client)
		if err == nil {
			return nil
		}
		last = err
		if !isRateLimited(err) || ctx.Err() != nil {
			return err
		}
		wait, _ := retryDelay(err)
		m.log.Log(logSource, "brain.rate_limited", "", map[string]any{
			"provider": m.provider, "model": model, "key_index": idx, "keys": len(m.keys), "retry_after_ms": wait.Milliseconds(),
		})
		m.rotate(idx)
	}
	return fmt.Errorf("%w: %v", ErrAllKeysRateLimited, last)
}

// client returns the current key's client, building it on first use.
func (m *GenaiModel) client(ctx context.Context) (int, *genai.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.idx
	if c, ok := m.clients[idx]; ok {
		return idx, c, nil
	}
	c, err := m.newClient(ctx, m.keys[idx])
	if err != nil {
		return idx, nil, fmt.Errorf("brain: client: %w", err)
	}
	m.clients[idx] = c
	return idx, c, nil
}

// rotate moves past key idx unless another call already did.
func (m *GenaiModel) rotate(idx int) {
	m.mu.Lock()
	if m.idx == idx {
		m.idx = (m.idx + 1) % len(m.keys)
	}
	m.mu.Unlock()
}

// isRateLimited reports whether err is an HTTP 429 / RESOURCE_EXHAUSTED.
func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	var api genai.APIError
	if errors.As(err, &api) && api.Code == 429 {
		return true
	}
	var compat *APIError
	if errors.As(err, &compat) {
		return compat.Status == 429
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{"resource_exhausted", "429", "quota", "rate limit", "too many requests"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

var retryDelayRE = regexp.MustCompile(`(?i)(?:retrydelay"?\s*:\s*"?|retry (?:in|after)\s+)(\d+(?:\.\d+)?)\s*s`)

// retryDelay extracts RetryInfo.retryDelay ("30s") from an error message.
func retryDelay(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	m := retryDelayRE.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, false
	}
	secs, perr := strconv.ParseFloat(m[1], 64)
	if perr != nil || secs <= 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}
