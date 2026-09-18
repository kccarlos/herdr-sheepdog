package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// fakeOpenAI is an httptest OpenAI-compatible endpoint driven by a script
// of responses; it records every request (with the bearer key used).
type fakeOpenAI struct {
	t  *testing.T
	mu sync.Mutex
	// script is consumed one entry per request; the last entry repeats.
	script   []fakeReply
	requests []fakeRequest
	srv      *httptest.Server
}

type fakeReply struct {
	status  int
	body    string
	headers map[string]string
	delay   time.Duration
}

type fakeRequest struct {
	key            string
	responseFormat bool
	model          string
	user           string
}

func newFake(t *testing.T, script ...fakeReply) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{t: t, script: script}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenAI) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	var req struct {
		Model          string         `json:"model"`
		Messages       []chatMessage  `json:"messages"`
		ResponseFormat map[string]any `json:"response_format"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, fakeRequest{
		key:            strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		responseFormat: req.ResponseFormat != nil,
		model:          req.Model,
		user:           req.Messages[len(req.Messages)-1].Content,
	})
	rep := f.script[0]
	if len(f.script) > 1 {
		f.script = f.script[1:]
	}
	f.mu.Unlock()
	if rep.delay > 0 {
		time.Sleep(rep.delay)
	}
	for k, v := range rep.headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.status)
	_, _ = w.Write([]byte(rep.body))
}

func (f *fakeOpenAI) Requests() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeRequest(nil), f.requests...)
}

func ok(content string) fakeReply {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": content}}},
		"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 7},
	})
	return fakeReply{status: 200, body: string(b)}
}

// recLog records events for assertions.
type recLog struct {
	mu     sync.Mutex
	events []logging.Line
}

func (l *recLog) Log(source, typ, paneID string, payload any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, logging.Line{Source: source, Type: typ, PaneID: paneID, Payload: payload})
}

func (l *recLog) ofType(typ string) []logging.Line {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logging.Line
	for _, e := range l.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func newClient(t *testing.T, f *fakeOpenAI, keys []string, lg logging.Logger) *Client {
	t.Helper()
	c, err := New(Config{
		BaseURL:      f.srv.URL + "/v1",
		Model:        "fake-model",
		APIKeys:      keys,
		Timeout:      2 * time.Second,
		RetryBackoff: 5 * time.Millisecond,
		MaxRetryWait: 50 * time.Millisecond,
		Log:          lg,
	})
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = func(ctx context.Context, d time.Duration) error { return nil }
	return c
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{Model: "m", APIKeys: []string{"k"}}); err == nil {
		t.Error("missing base URL accepted")
	}
	if _, err := New(Config{BaseURL: "http://x", APIKeys: []string{"k"}}); err == nil {
		t.Error("missing model accepted")
	}
	if _, err := New(Config{BaseURL: "http://x", Model: "m"}); !errors.Is(err, ErrNoAPIKeys) {
		t.Errorf("missing keys: %v", err)
	}
}

func TestChatLogsCall(t *testing.T) {
	f := newFake(t, ok(`{"spoken":"hi"}`))
	lg := &recLog{}
	c := newClient(t, f, []string{"k0"}, lg)
	rep, err := c.Chat(context.Background(), "unit", "sys", "usr")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Text != `{"spoken":"hi"}` || rep.PromptTokens != 12 || rep.CompletionTokens != 7 || rep.Attempts != 1 {
		t.Errorf("reply = %+v", rep)
	}
	reqs := f.Requests()
	if len(reqs) != 1 || reqs[0].key != "k0" || !reqs[0].responseFormat || reqs[0].model != "fake-model" {
		t.Errorf("request = %+v", reqs)
	}
	calls := lg.ofType("llm.call")
	if len(calls) != 1 {
		t.Fatalf("llm.call events = %d", len(calls))
	}
	p := calls[0].Payload.(map[string]any)
	if p["template"] != "unit" || p["model"] != "fake-model" || p["prompt_hash"] == "" || p["prompt_tokens"] != 12 {
		t.Errorf("llm.call payload = %v", p)
	}
	if _, ok := p["dur_ms"]; !ok {
		t.Errorf("llm.call has no dur_ms: %v", p)
	}
}

func TestRetryOnceOnServerError(t *testing.T) {
	f := newFake(t, fakeReply{status: 503, body: "busy"}, ok("second"))
	c := newClient(t, f, []string{"k0"}, nil)
	rep, err := c.Chat(context.Background(), "unit", "", "u")
	if err != nil || rep.Text != "second" || rep.Attempts != 2 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	f = newFake(t, fakeReply{status: 503, body: "busy"})
	c = newClient(t, f, []string{"k0"}, nil)
	if _, err := c.Chat(context.Background(), "unit", "", "u"); err == nil {
		t.Fatal("two failures should surface")
	}
	if n := len(f.Requests()); n != 2 {
		t.Errorf("attempts = %d, want exactly 2", n)
	}
}

func TestBadRequestIsNotRetried(t *testing.T) {
	f := newFake(t, fakeReply{status: 400, body: `{"error":{"message":"bad model"}}`})
	c := newClient(t, f, []string{"k0"}, nil)
	if _, err := c.Chat(context.Background(), "unit", "", "u"); err == nil || !strings.Contains(err.Error(), "bad model") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.Requests()); n != 1 {
		t.Errorf("attempts = %d, want 1", n)
	}
}

func TestResponseFormatRejectedFallsBack(t *testing.T) {
	f := newFake(t, fakeReply{status: 400, body: `{"error":{"message":"response_format is not supported"}}`}, ok("plain"))
	c := newClient(t, f, []string{"k0"}, nil)
	if rep, err := c.Chat(context.Background(), "unit", "", "u"); err != nil || rep.Text != "plain" {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	reqs := f.Requests()
	if len(reqs) != 2 || !reqs[0].responseFormat || reqs[1].responseFormat {
		t.Errorf("response_format should be dropped on retry: %+v", reqs)
	}
	// And stays off for the next call.
	if _, err := c.Chat(context.Background(), "unit", "", "u"); err != nil {
		t.Fatal(err)
	}
	if reqs := f.Requests(); reqs[2].responseFormat {
		t.Error("response_format came back")
	}
}

func TestRateLimitRotatesKey(t *testing.T) {
	body := `{"error":{"message":"quota","details":[{"retryDelay":"12s"}]}}`
	f := newFake(t, fakeReply{status: 429, body: body}, ok("via k1"))
	lg := &recLog{}
	c := newClient(t, f, []string{"sk-zero-0", "sk-one-1", "sk-two-2"}, lg)
	rep, err := c.Chat(context.Background(), "unit", "", "u")
	if err != nil || rep.Text != "via k1" || rep.KeyIndex != 1 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	reqs := f.Requests()
	if len(reqs) != 2 || reqs[0].key != "sk-zero-0" || reqs[1].key != "sk-one-1" {
		t.Errorf("keys used = %+v", reqs)
	}
	rl := lg.ofType("llm.rate_limited")
	if len(rl) != 1 {
		t.Fatalf("rate_limited events = %d", len(rl))
	}
	p := rl[0].Payload.(map[string]any)
	if p["key_index"] != 0 || p["retry_after_ms"] != int64(12000) {
		t.Errorf("rate_limited payload = %v", p)
	}
	for _, e := range lg.events {
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), "sk-zero-0") || strings.Contains(string(b), "sk-one-1") {
			t.Errorf("log leaks a key: %s", b)
		}
	}
	// Next call starts from the rotated key.
	if _, err := c.Chat(context.Background(), "unit", "", "u"); err != nil {
		t.Fatal(err)
	}
	if reqs := f.Requests(); reqs[2].key != "sk-one-1" {
		t.Errorf("third request used %q, want k1", reqs[2].key)
	}
}

func TestRateLimitSingleKeyHonoursRetryAfter(t *testing.T) {
	f := newFake(t, fakeReply{status: 429, body: "slow down", headers: map[string]string{"Retry-After": "0"}}, ok("later"))
	lg := &recLog{}
	c := newClient(t, f, []string{"only"}, lg)
	var slept time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error { slept = d; return nil }
	if rep, err := c.Chat(context.Background(), "unit", "", "u"); err != nil || rep.Text != "later" {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	if slept <= 0 {
		t.Errorf("a 429 must never be hot-retried; slept %v", slept)
	}

	// A long Retry-After with no other key: give up with ErrRateLimited.
	f = newFake(t, fakeReply{status: 429, body: "slow down", headers: map[string]string{"Retry-After": "60"}})
	c = newClient(t, f, []string{"only"}, nil)
	_, err := c.Chat(context.Background(), "unit", "", "u")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if n := len(f.Requests()); n != 1 {
		t.Errorf("attempts = %d, want 1 (no hot retry)", n)
	}
}

func TestTimeout(t *testing.T) {
	f := newFake(t, fakeReply{status: 200, body: "{}", delay: 300 * time.Millisecond})
	c, err := New(Config{BaseURL: f.srv.URL + "/v1", Model: "m", APIKeys: []string{"k"}, Timeout: 50 * time.Millisecond, RetryBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	c.sleep = func(context.Context, time.Duration) error { return nil }
	start := time.Now()
	_, err = c.Chat(context.Background(), "unit", "", "u")
	if err == nil {
		t.Fatal("expected timeout")
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("took %v", el)
	}
	if n := len(f.Requests()); n != 2 {
		t.Errorf("attempts = %d, want 2 (one retry)", n)
	}
	// Parent context cancelled: no retry.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f2 := newFake(t, ok("x"))
	c2 := newClient(t, f2, []string{"k"}, nil)
	if _, err := c2.Chat(ctx, "unit", "", "u"); err == nil {
		t.Error("cancelled context should fail")
	}
}

func TestEmptyReply(t *testing.T) {
	f := newFake(t, fakeReply{status: 200, body: `{"choices":[]}`})
	c := newClient(t, f, []string{"k"}, nil)
	if _, err := c.Chat(context.Background(), "unit", "", "u"); !errors.Is(err, ErrEmptyReply) {
		t.Errorf("err = %v", err)
	}
	if n := len(f.Requests()); n != 1 {
		t.Errorf("empty reply retried %d times", n-1)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	if d := retryAfter("7", chatResponse{}); d != 7*time.Second {
		t.Errorf("seconds: %v", d)
	}
	if d := retryAfter(time.Now().Add(3*time.Second).UTC().Format(http.TimeFormat), chatResponse{}); d <= 0 || d > 3*time.Second {
		t.Errorf("http date: %v", d)
	}
	var parsed chatResponse
	_ = json.Unmarshal([]byte(`{"error":{"message":"x","details":[{"retryDelay":"2.5s"}]}}`), &parsed)
	if d := retryAfter("", parsed); d != 2500*time.Millisecond {
		t.Errorf("retryDelay: %v", d)
	}
	if d := retryAfter("garbage", chatResponse{}); d != 0 {
		t.Errorf("garbage: %v", d)
	}
}

// fixture reads a captured screen from testdata/herdr.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
