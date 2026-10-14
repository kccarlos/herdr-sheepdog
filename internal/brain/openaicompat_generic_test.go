package brain_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// genericServer is an OpenAI-compatible endpoint under /v1 (OpenRouter,
// Ollama, Groq) that records each request's path, auth header and body.
type genericServer struct {
	*compatServer
	mu    sync.Mutex
	auths []string
	raw   []map[string]json.RawMessage
}

func newGenericServer(t *testing.T, script ...compatReply) *genericServer {
	t.Helper()
	g := &genericServer{compatServer: &compatServer{script: script}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.auths = append(g.auths, r.Header.Get("Authorization"))
		g.mu.Unlock()
		g.compatServer.mu.Lock()
		defer g.compatServer.mu.Unlock()
		g.paths = append(g.paths, r.Method+" "+r.URL.Path)
		data, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost {
			var body map[string]any
			var raw map[string]json.RawMessage
			_ = json.Unmarshal(data, &body)
			_ = json.Unmarshal(data, &raw)
			g.requests = append(g.requests, body)
			g.mu.Lock()
			g.raw = append(g.raw, raw)
			g.mu.Unlock()
		}
		if len(g.script) == 0 {
			http.Error(w, `{"error":{"message":"unscripted call"}}`, http.StatusInternalServerError)
			return
		}
		rep := g.script[0]
		g.script = g.script[1:]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func newGeneric(t *testing.T, cfg OpenAICompatConfig) *OpenAICompatModel {
	t.Helper()
	m, err := NewOpenAICompatModel(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A generic provider speaks the plain OpenAI shape: the path under the
// configured /v1 root, the bearer key, the model id, tools — and none of
// DeepSeek's fields, on a user turn or an event turn, nor the
// reasoning_content its own answer carried.
func TestGenericProviderRequests(t *testing.T) {
	srv := newGenericServer(t,
		toolCalls("let me look", [3]string{"call_a", ToolReadScreen, `{"agent":"auth","lines":50}`}),
		ok(`{"spoken":"Auth is running the tests."}`),
		ok(`{"spoken":"Auth finished."}`),
	)
	m := newGeneric(t, OpenAICompatConfig{Provider: "openrouter", BaseURL: srv.srv.URL + "/v1/", APIKey: "or-key", DefaultModel: "vendor/model-a"})
	fake := braintest.NewModel()
	h := newHarness(t, fake, func(c *Config) { c.Provider = "openrouter"; c.Model = "vendor/model-a"; c.Now = time.Now })
	if err := h.brain.AddProvider("openrouter", m); err != nil {
		t.Fatal(err)
	}
	rep, err := h.ask("what is auth doing?", "auth")
	if err != nil || rep.Spoken != "Auth is running the tests." {
		t.Fatalf("reply = %+v err = %v", rep, err)
	}
	if srv.count() != 2 {
		t.Fatalf("requests = %d", srv.count())
	}
	for i := 0; i < 2; i++ {
		if srv.paths[i] != "POST /v1/chat/completions" {
			t.Errorf("path %d = %s", i, srv.paths[i])
		}
		if srv.auths[i] != "Bearer or-key" {
			t.Errorf("auth %d = %q", i, srv.auths[i])
		}
		raw := srv.raw[i]
		for _, k := range []string{"thinking", "reasoning_effort"} {
			if _, ok := raw[k]; ok {
				t.Errorf("request %d carries DeepSeek field %q", i, k)
			}
		}
		if string(raw["model"]) != `"vendor/model-a"` {
			t.Errorf("model %d = %s", i, raw["model"])
		}
		if _, ok := raw["tools"]; !ok {
			t.Errorf("request %d has no tools", i)
		}
	}
	for _, mm := range srv.request(1)["messages"].([]any) {
		if _, ok := mm.(map[string]any)["reasoning_content"]; ok {
			t.Errorf("reasoning_content replayed to a generic provider: %v", mm)
		}
	}
}

// A local server needs no key: NoKey allows an empty one and no
// Authorization header is sent; without NoKey a key is required.
func TestGenericProviderNoKey(t *testing.T) {
	if _, err := NewOpenAICompatModel(OpenAICompatConfig{Provider: "ollama", BaseURL: "http://localhost:11434/v1"}); err == nil {
		t.Error("an empty key without NoKey was accepted")
	}
	srv := newGenericServer(t, compatReply{status: 200, body: `{"data":[{"id":"qwen3:8b"}]}`})
	m := newGeneric(t, OpenAICompatConfig{Provider: "ollama", BaseURL: srv.srv.URL + "/v1", NoKey: true})
	models, err := m.ListModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "qwen3:8b" {
		t.Errorf("models = %+v", models)
	}
	if srv.paths[0] != "GET /v1/models" || srv.auths[0] != "" {
		t.Errorf("path = %s auth = %q", srv.paths[0], srv.auths[0])
	}
}

// A failover to a generic provider runs that provider's configured
// model, not the failing provider's id.
func TestFailoverToGenericProviderUsesItsModel(t *testing.T) {
	srv := newGenericServer(t, ok(`{"spoken":"Auth is running the tests."}`))
	m := newGeneric(t, OpenAICompatConfig{Provider: "groq", BaseURL: srv.srv.URL + "/openai/v1", APIKey: "gk", DefaultModel: "openai/gpt-oss-120b"})
	h := newHarness(t, failing(err504), func(c *Config) { c.Provider, c.Model = ProviderVertex, "gemini-3.8-flash" })
	if err := h.brain.AddProvider("groq", m); err != nil {
		t.Fatal(err)
	}
	rep, err := h.ask("what is auth doing?", "auth")
	if err != nil || rep.Spoken != "Auth is running the tests." {
		t.Fatalf("reply = %+v err = %v", rep, err)
	}
	if got := srv.request(0)["model"]; got != "openai/gpt-oss-120b" {
		t.Errorf("failover model = %v", got)
	}
	if p := payload(h.log.ofType("brain.failover")[0]); p["to"] != "groq" || p["to_model"] != "openai/gpt-oss-120b" {
		t.Errorf("brain.failover = %v", p)
	}
}
