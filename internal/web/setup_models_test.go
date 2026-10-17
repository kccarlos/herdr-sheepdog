package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

const secretCompatKey = "sk-compat-secret-value-0123456789"

// fakeCompat serves /models (needs secretCompatKey as a Bearer token) and
// /chat/completions; toolCall decides whether the chat answer calls the
// probe tool. It counts the chat calls and remembers the key it saw.
type fakeCompat struct {
	*httptest.Server
	chats    atomic.Int32
	toolCall atomic.Bool
	mu       sync.Mutex
	seenAuth []string
}

func newFakeCompat(t *testing.T, modelsBody string) *fakeCompat {
	t.Helper()
	f := &fakeCompat{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seenAuth = append(f.seenAuth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+secretCompatKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, modelsBody)
		case "/v1/chat/completions":
			f.chats.Add(1)
			msg := `{"role":"assistant","content":"hello"}`
			if f.toolCall.Load() {
				msg = `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"report_ready","arguments":"{}"}}]}`
			}
			_, _ = io.WriteString(w, `{"choices":[{"message":`+msg+`,"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func decodeModels(t *testing.T, body string) SetupModelsResponse {
	t.Helper()
	var r SetupModelsResponse
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return r
}

func TestSetupModelsListsWithTheTypedKey(t *testing.T) {
	fc := newFakeCompat(t, `{"data":[{"id":"a/one","supported_parameters":["tools","temperature"]},{"id":"b/two","supported_parameters":["temperature"]},{"id":"c/three"}]}`)
	f := newSetupFixture(t, config.Default(), "")
	code, body := f.post(t, "/api/setup/models", `{"name":"x","base_url":"`+fc.URL+`/v1","key":"`+secretCompatKey+`"}`)
	r := decodeModels(t, body)
	if code != 200 || r.Error != "" || len(r.Models) != 3 || !r.ToolInfo {
		t.Fatalf("%d %s", code, body)
	}
	if r.Models[0].Tools == nil || !*r.Models[0].Tools || r.Models[2].Tools != nil {
		t.Errorf("tools = %+v", r.Models)
	}
	if strings.Contains(body, secretCompatKey) {
		t.Error("the key came back")
	}
	if fc.chats.Load() != 0 {
		t.Error("listing sent a chat request")
	}
}

func TestSetupModelsUsesTheSavedKeyOnlyForTheSavedBase(t *testing.T) {
	fc := newFakeCompat(t, `{"data":[{"id":"m1"}]}`)
	other := newFakeCompat(t, `{"data":[{"id":"m1"}]}`)
	cfg := config.Default()
	cfg.Brain.OpenAICompat = []config.OpenAICompat{{Name: "x", BaseURL: fc.URL + "/v1", APIKeyEnv: "X_API_KEY", APIKey: secretCompatKey}}
	f := newSetupFixture(t, cfg, "")
	code, body := f.post(t, "/api/setup/models", `{"name":"x","base_url":"`+fc.URL+`/v1/"}`)
	r := decodeModels(t, body)
	if code != 200 || r.Error != "" || len(r.Models) != 1 {
		t.Fatalf("saved key not used: %d %s", code, body)
	}
	if strings.Contains(body, secretCompatKey) {
		t.Error("the saved key came back")
	}
	// Same name, another host: the saved key must not travel there.
	_, body = f.post(t, "/api/setup/models", `{"name":"x","base_url":"`+other.URL+`/v1"}`)
	if r := decodeModels(t, body); !strings.Contains(r.Error, "valid API key") {
		t.Errorf("another host got an answer: %s", body)
	}
	for _, a := range other.seenAuth {
		if strings.Contains(a, secretCompatKey) {
			t.Error("the saved key was sent to another host")
		}
	}
}

func TestSetupModelsErrorTexts(t *testing.T) {
	fc := newFakeCompat(t, `{"data":[{"id":"m1"}]}`)
	f := newSetupFixture(t, config.Default(), "")
	cases := []struct {
		name, body, wantErr, wantFix string
	}{
		{"unreachable", `{"base_url":"http://127.0.0.1:1/v1"}`, "Couldn't reach http://127.0.0.1:1/v1/models: ", "Check the Base URL (it usually ends in /v1). You can still type the model name."},
		{"no key", `{"base_url":"` + fc.URL + `/v1"}`, "This endpoint needs a valid API key.", "Paste it above, then press Find models again."},
		{"wrong key", `{"base_url":"` + fc.URL + `/v1","key":"nope"}`, "This endpoint needs a valid API key.", "Paste it above, then press Find models again."},
		{"404", `{"base_url":"` + fc.URL + `/other","key":"` + secretCompatKey + `"}`, "This endpoint doesn't list its models.", "Type the model name from its docs."},
		{"no url", `{"base_url":""}`, "No Base URL yet.", "Type the Base URL first"},
	}
	for _, c := range cases {
		code, body := f.post(t, "/api/setup/models", c.body)
		r := decodeModels(t, body)
		if code != 200 || !strings.HasPrefix(r.Error, c.wantErr) || !strings.HasPrefix(r.Fix, c.wantFix) || len(r.Models) != 0 {
			t.Errorf("%s: %d %s", c.name, code, body)
		}
		if strings.Contains(body, secretCompatKey) {
			t.Errorf("%s: the key came back", c.name)
		}
	}
	empty := newFakeCompat(t, `{"data":[]}`)
	_, body := f.post(t, "/api/setup/models", `{"base_url":"`+empty.URL+`/v1","key":"`+secretCompatKey+`"}`)
	if r := decodeModels(t, body); r.Error != "This endpoint doesn't list its models." {
		t.Errorf("empty list: %s", body)
	}
}

func TestSetupProbeAgainstAFakeServer(t *testing.T) {
	fc := newFakeCompat(t, `{"data":[{"id":"m1"}]}`)
	f := newSetupFixture(t, config.Default(), "")
	req := `{"name":"x","base_url":"` + fc.URL + `/v1","model":"m1","key":"` + secretCompatKey + `"}`

	if fc.chats.Load() != 0 {
		t.Fatal("a request went out before any click")
	}
	code, body := f.post(t, "/api/setup/probe", req)
	var r SetupProbeResponse
	_ = json.Unmarshal([]byte(body), &r)
	if code != 200 || r.OK || !strings.Contains(r.Detail, "without calling") || r.Fix == "" {
		t.Errorf("no tool call: %d %s", code, body)
	}
	fc.toolCall.Store(true)
	_, body = f.post(t, "/api/setup/probe", req)
	r = SetupProbeResponse{}
	_ = json.Unmarshal([]byte(body), &r)
	if !r.OK || !strings.Contains(r.Detail, "calls tools") {
		t.Errorf("tool call: %s", body)
	}
	if got := fc.chats.Load(); got != 2 {
		t.Errorf("chat requests = %d, want one per click", got)
	}
	_, body = f.post(t, "/api/setup/probe", `{"name":"x","base_url":"`+fc.URL+`/v1","model":"m1","key":"wrong"}`)
	r = SetupProbeResponse{}
	_ = json.Unmarshal([]byte(body), &r)
	if r.OK || strings.Contains(body, secretCompatKey) || strings.Contains(r.Fix, "doctor") {
		t.Errorf("bad key: %s", body)
	}
	_, body = f.post(t, "/api/setup/probe", `{"base_url":"`+fc.URL+`/v1","model":""}`)
	if !strings.Contains(body, "model name are needed") {
		t.Errorf("no model: %s", body)
	}
}
