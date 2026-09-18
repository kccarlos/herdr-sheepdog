package brain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

// ListModels pages through the model list on the current key, keeps the
// generateContent models without their "models/" prefix, recommends the
// text models of the gemini / gemma families only, and orders
// recommended first then newest name first; a 429 rotates the key as
// Generate does.
func TestGenaiModelListModels(t *testing.T) {
	var hits atomic.Int32
	limited := map[string]bool{"k1": true}
	pages := map[string]string{
		"": `{"models":[
			{"name":"models/gemini-3.5-flash","displayName":"Gemini 3.5 Flash","description":"fast","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent","countTokens"]},
			{"name":"models/gemini-3.5-flash-tts","displayName":"TTS","supportedGenerationMethods":["generateContent"]},
			{"name":"models/embedding-001","supportedGenerationMethods":["embedContent"]},
			{"name":"models/gemini-3.8-live","supportedGenerationMethods":["bidiGenerateContent"]}],
			"nextPageToken":"p2"}`,
		"p2": `{"models":[
			{"name":"models/gemini-3.8-flash","displayName":"Gemini 3.8 Flash","inputTokenLimit":2097152,"supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemma-4-27b-it","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-3.5-flash-image","supportedGenerationMethods":["generateContent"]},
			{"name":"models/imagen-5","supportedGenerationMethods":["predict"]},
			{"name":"models/aqa","supportedGenerationMethods":["generateContent"]}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.Error(w, "bad path "+r.URL.Path, 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if limited[r.Header.Get("x-goog-api-key")] {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`))
			return
		}
		if r.URL.Query().Get("pageSize") != "1000" {
			http.Error(w, "pageSize "+r.URL.Query().Get("pageSize"), 400)
			return
		}
		body, ok := pages[r.URL.Query().Get("pageToken")]
		if !ok {
			http.Error(w, "bad page token", 400)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	log := &recLog{}
	m, err := NewGenaiModel([]string{"k1", "k2"}, log)
	if err != nil {
		t.Fatal(err)
	}
	m.newClient = func(ctx context.Context, key string) (*genai.Client, error) {
		return genai.NewClient(ctx, &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI, HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL}})
	}
	list, err := m.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var rec []bool
	for _, mi := range list {
		names = append(names, mi.Name)
		rec = append(rec, mi.Recommended)
	}
	if got := strings.Join(names, " "); got != "gemma-4-27b-it gemini-3.8-flash gemini-3.5-flash gemini-3.5-flash-tts gemini-3.5-flash-image aqa" {
		t.Errorf("names = %s", got)
	}
	if fmt.Sprint(rec) != "[true true true false false false]" {
		t.Errorf("recommended = %v", rec)
	}
	if f := list[2]; f.DisplayName != "Gemini 3.5 Flash" || f.Description != "fast" || f.InputTokenLimit != 1048576 || f.OutputTokenLimit != 65536 || strings.Join(f.Methods, ",") != "generateContent,countTokens" {
		t.Errorf("gemini-3.5-flash = %+v", f)
	}
	// One 429 on k1, then both pages on k2.
	if hits.Load() != 3 || m.KeyIndex() != 1 || len(log.ofType("brain.rate_limited")) != 1 {
		t.Errorf("hits = %d key = %d rate_limited = %d", hits.Load(), m.KeyIndex(), len(log.ofType("brain.rate_limited")))
	}
	limited["k2"] = true
	if _, err := m.ListModels(context.Background()); !errors.Is(err, ErrAllKeysRateLimited) {
		t.Errorf("all limited: err = %v", err)
	}
}

// TestGenaiModelListModelsLive fetches the real catalog once when
// SHEEPDOG_LIVE_CATALOG=1 and a key is set (the model list is a free
// call; it is still one request, so it is opt-in).
func TestGenaiModelListModelsLive(t *testing.T) {
	if os.Getenv("SHEEPDOG_LIVE_CATALOG") != "1" {
		t.Skip("set SHEEPDOG_LIVE_CATALOG=1 to hit the real model list")
	}
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		t.Skip("GEMINI_API_KEY not set")
	}
	m, err := NewGenaiModel([]string{key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	list, err := m.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ModelInfo{}
	for _, mi := range list {
		byName[mi.Name] = mi
		t.Logf("%-45s recommended=%-5v in=%d display=%q", mi.Name, mi.Recommended, mi.InputTokenLimit, mi.DisplayName)
	}
	for _, want := range []string{"gemini-3.8-flash", "gemini-3.5-flash"} {
		if mi, ok := byName[want]; !ok || !mi.Recommended {
			t.Errorf("%s: listed=%v recommended=%v", want, ok, mi.Recommended)
		}
	}
	for name, mi := range byName {
		if mi.Recommended && (strings.Contains(name, "tts") || strings.Contains(name, "image")) {
			t.Errorf("%s recommended", name)
		}
	}
	if len(list) > 0 && !list[0].Recommended {
		t.Errorf("first entry %q not recommended", list[0].Name)
	}
}
