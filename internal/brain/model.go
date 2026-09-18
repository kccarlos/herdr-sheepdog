package brain

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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

// ErrAllKeysRateLimited is returned when every key of the pool was rate
// limited on one call.
var ErrAllKeysRateLimited = errors.New("brain: all API keys rate limited")

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
	var last error
	for attempt := 0; attempt < len(m.keys); attempt++ {
		idx, client, err := m.client(ctx)
		if err != nil {
			return nil, err
		}
		resp, err := client.Models.GenerateContent(ctx, model, contents, cfg)
		if err == nil {
			return resp, nil
		}
		last = err
		if !isRateLimited(err) || ctx.Err() != nil {
			return nil, err
		}
		wait, _ := retryDelay(err)
		m.log.Log(logSource, "brain.rate_limited", "", map[string]any{
			"model": model, "key_index": idx, "keys": len(m.keys), "retry_after_ms": wait.Milliseconds(),
		})
		m.rotate(idx)
	}
	return nil, fmt.Errorf("%w: %v", ErrAllKeysRateLimited, last)
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
