package brain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"
)

// A 429 on one key moves to the next key without a retry on the same key;
// when every key is rate limited the call fails with ErrAllKeysRateLimited
// and brain.rate_limited is logged per key.
func TestGenaiModelRotatesOn429(t *testing.T) {
	var hits atomic.Int32
	limited := map[string]bool{"k1": true, "k2": true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		key := r.Header.Get("x-goog-api-key")
		if !strings.Contains(r.URL.Path, "generateContent") {
			http.Error(w, "bad path "+r.URL.Path, 404)
			return
		}
		if limited[key] {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"7s"}]}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"spoken\":\"hi\"}"}],"role":"model"}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}`))
	}))
	defer srv.Close()

	log := &recLog{}
	m, err := NewGenaiModel([]string{"k1", "k2", "k3"}, log)
	if err != nil {
		t.Fatal(err)
	}
	m.newClient = func(ctx context.Context, key string) (*genai.Client, error) {
		return genai.NewClient(ctx, &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI, HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL}})
	}
	resp, err := m.Generate(context.Background(), "test-model", []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "x"}}}}, &genai.GenerateContentConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Text(); got != `{"spoken":"hi"}` {
		t.Errorf("text = %q", got)
	}
	if hits.Load() != 3 || m.KeyIndex() != 2 {
		t.Errorf("hits = %d key = %d", hits.Load(), m.KeyIndex())
	}
	rl := log.ofType("brain.rate_limited")
	if len(rl) != 2 || payload(rl[0])["key_index"] != 0 || payload(rl[1])["key_index"] != 1 || payload(rl[0])["retry_after_ms"] != int64(7000) {
		t.Errorf("brain.rate_limited = %v", rl)
	}
	// The key that worked stays in use; the next call is one hit.
	hits.Store(0)
	if _, err := m.Generate(context.Background(), "test-model", nil, &genai.GenerateContentConfig{}); err != nil || hits.Load() != 1 {
		t.Errorf("second call: err = %v hits = %d", err, hits.Load())
	}
	// All limited: no hot loop, one attempt per key, ErrAllKeysRateLimited.
	limited["k3"] = true
	hits.Store(0)
	_, err = m.Generate(context.Background(), "test-model", nil, &genai.GenerateContentConfig{})
	if !errors.Is(err, ErrAllKeysRateLimited) || hits.Load() != 3 {
		t.Errorf("all limited: err = %v hits = %d", err, hits.Load())
	}
	if _, err := NewGenaiModel(nil, nil); err == nil {
		t.Error("empty pool accepted")
	}
}
