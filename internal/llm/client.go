package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// Defaults for Config zero values.
const (
	DefaultTimeout      = 15 * time.Second
	DefaultRetryBackoff = 500 * time.Millisecond
	// DefaultMaxRetryWait caps how long a rate-limited call sleeps before its
	// single retry when no other key is available; a longer Retry-After is
	// reported as ErrRateLimited instead.
	DefaultMaxRetryWait = 5 * time.Second
	// maxResponseBytes bounds the response body read.
	maxResponseBytes = 1 << 20
	// logOutputBytes is how much of the model output an llm.call event keeps.
	logOutputBytes = 400
)

// Errors callers can branch on with errors.Is.
var (
	ErrNoAPIKeys   = errors.New("llm: no API keys configured")
	ErrRateLimited = errors.New("llm: rate limited")
	ErrEmptyReply  = errors.New("llm: empty reply")
)

// Config for a Client. BaseURL, Model and at least one key are required.
type Config struct {
	// BaseURL is the API root; "/chat/completions" is appended.
	BaseURL string
	// Model is sent in every request.
	Model string
	// APIKeys is the key pool. The first key is used until it is rate
	// limited, then the next, round-robin. Never logged.
	APIKeys []string
	// Timeout bounds one attempt (default 15 s).
	Timeout time.Duration
	// RetryBackoff is the wait before the single retry of a transient
	// failure (default 500 ms).
	RetryBackoff time.Duration
	// MaxRetryWait caps the wait a rate-limited call is willing to sleep
	// when no other key exists (default 5 s).
	MaxRetryWait time.Duration
	// Temperature is passed through when non-nil.
	Temperature *float64
	// HTTPClient overrides http.DefaultClient (tests).
	HTTPClient *http.Client
	// Log receives llm.call and llm.rate_limited events; nil discards.
	Log logging.Logger
}

// Client calls one OpenAI-compatible chat endpoint. It is safe for
// concurrent use.
type Client struct {
	cfg  Config
	http *http.Client
	log  logging.Logger

	mu        sync.Mutex
	keyIdx    int
	noJSONFmt bool // backend rejected response_format; stop sending it
	sleep     func(context.Context, time.Duration) error
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("llm: base URL required")
	}
	if cfg.Model == "" {
		return nil, errors.New("llm: model required")
	}
	if len(cfg.APIKeys) == 0 {
		return nil, ErrNoAPIKeys
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.RetryBackoff == 0 {
		cfg.RetryBackoff = DefaultRetryBackoff
	}
	if cfg.MaxRetryWait == 0 {
		cfg.MaxRetryWait = DefaultMaxRetryWait
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	lg := cfg.Log
	if lg == nil {
		lg = logging.NopLogger{}
	}
	return &Client{cfg: cfg, http: hc, log: lg, sleep: sleepCtx}, nil
}

// Model returns the configured model name.
func (c *Client) Model() string { return c.cfg.Model }

// KeyIndex returns the index of the key currently in use (for reports).
func (c *Client) KeyIndex() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keyIdx
}

// Reply is one completed chat call.
type Reply struct {
	// Text is the assistant message content.
	Text string
	// PromptTokens / CompletionTokens are taken from `usage` when present.
	PromptTokens, CompletionTokens int
	// Attempts is how many HTTP requests were made (1 or 2).
	Attempts int
	// KeyIndex is the pool index of the key that succeeded.
	KeyIndex int
}

// Chat sends system + user as one chat completion and returns the reply
// text. template names the caller for the llm.call event. It makes at most
// two attempts.
func (c *Client) Chat(ctx context.Context, template, system, user string) (Reply, error) {
	start := time.Now()
	reply, err := c.chat(ctx, system, user)
	payload := map[string]any{
		"template":    template,
		"model":       c.cfg.Model,
		"prompt_hash": promptHash(system, user),
		"dur_ms":      time.Since(start).Milliseconds(),
		"attempts":    reply.Attempts,
		"key_index":   reply.KeyIndex,
		"output":      truncateRunes(reply.Text, logOutputBytes),
	}
	if reply.PromptTokens > 0 || reply.CompletionTokens > 0 {
		payload["prompt_tokens"] = reply.PromptTokens
		payload["completion_tokens"] = reply.CompletionTokens
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	c.log.Log("llm", "llm.call", "", payload)
	return reply, err
}

// chat is the retry loop around one attempt.
func (c *Client) chat(ctx context.Context, system, user string) (Reply, error) {
	var reply Reply
	for attempt := 1; attempt <= 2; attempt++ {
		reply.Attempts = attempt
		r, res, err := c.attempt(ctx, system, user)
		r.Attempts = attempt
		if err == nil {
			return r, nil
		}
		reply.KeyIndex = r.KeyIndex
		if attempt == 2 || ctx.Err() != nil {
			return reply, err
		}
		wait, retry := c.retryPolicy(res, err)
		if !retry {
			return reply, err
		}
		if wait > 0 {
			if err := c.sleep(ctx, wait); err != nil {
				return reply, err
			}
		}
	}
	return reply, errors.New("llm: unreachable")
}

// attemptResult is what retryPolicy needs to know about a failed attempt.
type attemptResult struct {
	status     int
	retryAfter time.Duration
	// responseFormatRejected is set when a 400 complained about
	// response_format (the backend does not support JSON mode).
	responseFormatRejected bool
	rateLimited            bool
}

// retryPolicy decides whether the failed attempt is retried and after how
// long. A rate limit is retried immediately on the next key when the pool
// has one (never a hot retry on the same key); otherwise only when the
// server's delay fits under MaxRetryWait.
func (c *Client) retryPolicy(res attemptResult, err error) (time.Duration, bool) {
	switch {
	case res.responseFormatRejected:
		return 0, true
	case res.rateLimited:
		if c.rotateKey() {
			return 0, true
		}
		wait := res.retryAfter
		if wait <= 0 {
			wait = c.cfg.RetryBackoff * 4
		}
		if wait > c.cfg.MaxRetryWait {
			return 0, false
		}
		return wait, true
	case res.status >= 500, res.status == http.StatusRequestTimeout:
		return c.cfg.RetryBackoff, true
	case res.status == 0 && !errors.Is(err, ErrEmptyReply):
		// Network / timeout error.
		return c.cfg.RetryBackoff, true
	}
	return 0, false
}

// rotateKey moves to the next key of the pool; it reports false when there
// is only one key.
func (c *Client) rotateKey() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cfg.APIKeys) < 2 {
		return false
	}
	c.keyIdx = (c.keyIdx + 1) % len(c.cfg.APIKeys)
	return true
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	Temperature    *float64       `json:"temperature,omitempty"`
	Stream         bool           `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Details []struct {
			RetryDelay string `json:"retryDelay"`
		} `json:"details"`
	} `json:"error"`
}

// attempt performs one HTTP request.
func (c *Client) attempt(ctx context.Context, system, user string) (Reply, attemptResult, error) {
	c.mu.Lock()
	keyIdx, key, noJSON := c.keyIdx, c.cfg.APIKeys[c.keyIdx], c.noJSONFmt
	c.mu.Unlock()
	reply := Reply{KeyIndex: keyIdx}

	req := chatRequest{Model: c.cfg.Model, Temperature: c.cfg.Temperature}
	if system != "" {
		req.Messages = append(req.Messages, chatMessage{Role: "system", Content: system})
	}
	req.Messages = append(req.Messages, chatMessage{Role: "user", Content: user})
	if !noJSON {
		req.ResponseFormat = map[string]any{"type": "json_object"}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return reply, attemptResult{}, fmt.Errorf("llm: encode request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/chat/completions"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return reply, attemptResult{}, fmt.Errorf("llm: build request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+key)

	resp, err := c.http.Do(hreq)
	if err != nil {
		return reply, attemptResult{}, fmt.Errorf("llm: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return reply, attemptResult{status: resp.StatusCode}, fmt.Errorf("llm: read response: %w", err)
	}

	var parsed chatResponse
	_ = json.Unmarshal(raw, &parsed)
	res := attemptResult{status: resp.StatusCode}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			res.rateLimited = true
			res.retryAfter = retryAfter(resp.Header.Get("Retry-After"), parsed)
			c.log.Log("llm", "llm.rate_limited", "", map[string]any{
				"model": c.cfg.Model, "key_index": keyIdx, "keys": len(c.cfg.APIKeys),
				"retry_after_ms": res.retryAfter.Milliseconds(),
			})
			return reply, res, fmt.Errorf("%w (key %d): %s", ErrRateLimited, keyIdx, truncateRunes(msg, 200))
		}
		if resp.StatusCode == http.StatusBadRequest && !noJSON && strings.Contains(strings.ToLower(msg), "response_format") {
			c.mu.Lock()
			c.noJSONFmt = true
			c.mu.Unlock()
			res.responseFormatRejected = true
		}
		return reply, res, fmt.Errorf("llm: http %d: %s", resp.StatusCode, truncateRunes(msg, 200))
	}
	reply.PromptTokens, reply.CompletionTokens = parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return reply, res, ErrEmptyReply
	}
	reply.Text = parsed.Choices[0].Message.Content
	return reply, res, nil
}

// retryAfter parses the Retry-After header (seconds or HTTP date) or
// Gemini's error.details[].retryDelay ("12s"); zero when neither is usable.
func retryAfter(header string, parsed chatResponse) time.Duration {
	if header != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(header); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}
	if parsed.Error != nil {
		for _, d := range parsed.Error.Details {
			if d.RetryDelay == "" {
				continue
			}
			if dur, err := time.ParseDuration(d.RetryDelay); err == nil && dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func promptHash(system, user string) string {
	h := sha256.Sum256([]byte(system + "\x00" + user))
	return hex.EncodeToString(h[:6])
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// truncateRunes cuts s to at most n runes, appending "…" when it did.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
