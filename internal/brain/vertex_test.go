package brain_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// fakeVertex is a Vertex AI generateContent endpoint: it records each
// request body and answers with the next scripted response.
type fakeVertex struct {
	t       *testing.T
	mu      sync.Mutex
	bodies  []string
	answers []func(w http.ResponseWriter)
}

func (f *fakeVertex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const want = "/projects/proj-1/locations/global/publishers/google/models/gemini-3.8-flash:generateContent"
	if !strings.HasSuffix(r.URL.Path, want) {
		f.t.Errorf("path = %s, want suffix %s", r.URL.Path, want)
	}
	if got := r.Header.Get("x-goog-api-key"); got != "vk-test" {
		f.t.Errorf("x-goog-api-key = %q", got)
	}
	if r.Header.Get("Authorization") != "" {
		f.t.Error("unexpected Authorization header")
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.bodies = append(f.bodies, string(body))
	n := len(f.bodies)
	f.mu.Unlock()
	if n > len(f.answers) {
		http.Error(w, "unscripted", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	f.answers[n-1](w)
}

func (f *fakeVertex) body(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[i]
}

func jsonAnswer(v string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { _, _ = io.WriteString(w, v) }
}

// A turn on the vertex provider: the request goes to the global
// location of the configured project with the key in x-goog-api-key,
// and the function call's thought signature comes back on the model
// content of the next step (Gemini 3 refuses a follow-up without it).
func TestVertexTurnReplaysThoughtSignature(t *testing.T) {
	reply, _ := json.Marshal(braintest.Reply("Auth is asking whether it can create hello.txt.", ""))
	fv := &fakeVertex{t: t, answers: []func(http.ResponseWriter){
		jsonAnswer(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_screen","args":{"agent":"auth"}},"thoughtSignature":"c2lnLXZlcnRleA=="}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3}}`),
		jsonAnswer(`{"candidates":[{"content":{"role":"model","parts":[{"text":` + string(reply) + `}]}}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5}}`),
	}}
	srv := httptest.NewServer(fv)
	defer srv.Close()

	log := &recLog{}
	m, err := NewVertexModelAt("vk-test", "proj-1", srv.URL, log)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(Config{DataDir: t.TempDir(), RunID: "run1", Now: time.Now, Provider: ProviderVertex, Model: "gemini-3.8-flash"}, m, log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	host := braintest.NewHost()
	host.SetAgent(AgentInfo{PaneID: "w5:p1", Alias: "auth", Kind: "codex", State: "working"})
	host.SetScreen("w5:p1", "Would you like to create hello.txt?")
	b.SetHost(host)

	rep, err := b.Turn(context.Background(), Request{Kind: KindUtterance, Question: "what is auth asking?"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rep.Spoken, "Auth is asking") {
		t.Errorf("reply = %+v", rep)
	}
	if len(fv.bodies) != 2 {
		t.Fatalf("requests = %d", len(fv.bodies))
	}
	var req struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				FunctionCall     *struct{ Name string } `json:"functionCall"`
				ThoughtSignature string                 `json:"thoughtSignature"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal([]byte(fv.body(1)), &req); err != nil {
		t.Fatal(err)
	}
	var replayed bool
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if c.Role == "model" && p.FunctionCall != nil && p.FunctionCall.Name == "read_screen" && p.ThoughtSignature == "c2lnLXZlcnRleA==" {
				replayed = true
			}
		}
	}
	if !replayed {
		t.Errorf("thought signature not replayed: %s", fv.body(1))
	}
	if b.Provider() != ProviderVertex {
		t.Errorf("provider = %s", b.Provider())
	}
}

// A 429 is one request, brain.rate_limited naming the provider, and
// ErrAllKeysRateLimited: no retry on the single key.
func TestVertexRateLimitedNoRetry(t *testing.T) {
	fv := &fakeVertex{t: t, answers: []func(http.ResponseWriter){func(w http.ResponseWriter) {
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`)
	}}}
	srv := httptest.NewServer(fv)
	defer srv.Close()
	log := &recLog{}
	m, err := NewVertexModelAt("vk-test", "proj-1", srv.URL, log)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Generate(context.Background(), "gemini-3.8-flash", []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "x"}}}}, &genai.GenerateContentConfig{})
	if !errors.Is(err, ErrAllKeysRateLimited) || len(fv.bodies) != 1 {
		t.Errorf("err = %v requests = %d", err, len(fv.bodies))
	}
	rl := log.ofType("brain.rate_limited")
	if len(rl) != 1 || payload(rl[0])["provider"] != ProviderVertex {
		t.Errorf("brain.rate_limited = %v", rl)
	}
}

// The catalog is the static list (Vertex's model listing refuses an API
// key): no request is made. A missing key or project is refused.
func TestVertexCatalogAndConfig(t *testing.T) {
	fv := &fakeVertex{t: t}
	srv := httptest.NewServer(fv)
	defer srv.Close()
	m, err := NewVertexModelAt("vk-test", "proj-1", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.ListModels(context.Background())
	if err != nil || len(got) != len(VertexModels) || got[0].Name != "gemini-3.8-flash" || len(fv.bodies) != 0 {
		t.Errorf("ListModels = %v, %v (requests %d)", got, err, len(fv.bodies))
	}
	if _, err := NewVertexModel("", "proj-1", nil); err == nil {
		t.Error("empty key accepted")
	}
	if _, err := NewVertexModel("vk-test", "", nil); err == nil {
		t.Error("empty project accepted")
	}
}

// A model that refuses thinking level minimal (gemini-3.8-flash) is
// retried once at low and remembered: the next minimal call goes out at
// low directly.
func TestGenaiModelMinimalRefusedFallsBackToLow(t *testing.T) {
	ok := `{"candidates":[{"content":{"role":"model","parts":[{"text":"{}"}]}}]}`
	refuse := func(w http.ResponseWriter) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"code":400,"message":"Thinking level is unsupported: THINKING_LEVEL_MINIMAL","status":"INVALID_ARGUMENT"}}`)
	}
	fv := &fakeVertex{t: t, answers: []func(http.ResponseWriter){refuse, jsonAnswer(ok), jsonAnswer(ok)}}
	srv := httptest.NewServer(fv)
	defer srv.Close()
	m, err := NewVertexModelAt("vk-test", "proj-1", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &genai.GenerateContentConfig{ThinkingConfig: &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelMinimal}}
	for i := 0; i < 2; i++ {
		if _, err := m.Generate(context.Background(), "gemini-3.8-flash", []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "x"}}}}, cfg); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(fv.bodies) != 3 || !strings.Contains(fv.body(0), "MINIMAL") || !strings.Contains(fv.body(1), `"LOW"`) || !strings.Contains(fv.body(2), `"LOW"`) {
		t.Errorf("requests = %d: %v", len(fv.bodies), fv.bodies)
	}
	if cfg.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelMinimal {
		t.Error("caller's config modified")
	}
}
