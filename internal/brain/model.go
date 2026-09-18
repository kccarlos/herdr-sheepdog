package brain

import (
	"context"
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

// Model is the one model call the brain makes; *GenaiModel implements it
// over the Gemini API and tests inject a scripted fake (braintest).
type Model interface {
	Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// ProviderGemini is the one model provider today; the catalog and the
// model switch carry a provider id so another one can be added later.
const ProviderGemini = "gemini"

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
	// brain.step error and the template path answers.
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
	return &GenaiModel{keys: append([]string(nil), keys...), log: log, clients: map[int]*genai.Client{}, newClient: func(ctx context.Context, key string) (*genai.Client, error) {
		return genai.NewClient(ctx, &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI})
	}}, nil
}

// KeyIndex returns the index of the key in use (reports; never the key).
func (m *GenaiModel) KeyIndex() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idx
}

// Generate implements Model.
func (m *GenaiModel) Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	var resp *genai.GenerateContentResponse
	err := m.withKeys(ctx, model, func(client *genai.Client) error {
		var err error
		resp, err = client.Models.GenerateContent(ctx, model, contents, cfg)
		return err
	})
	return resp, err
}

// ListModels implements ModelCatalog: the Gemini model list (a free
// call, not billed against the per-model quota), filtered to the models
// that support generateContent, recommended ones first then newest name
// first. The key pool applies as for Generate.
func (m *GenaiModel) ListModels(ctx context.Context) ([]ModelInfo, error) {
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
			"model": model, "key_index": idx, "keys": len(m.keys), "retry_after_ms": wait.Milliseconds(),
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
