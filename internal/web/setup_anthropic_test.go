package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

const (
	antGoodKey  = "sk-ant-setup-good-000000000000000000"
	antBadKey   = "sk-ant-setup-bad-0000000000000000000"
	antSavedKey = "sk-ant-setup-saved-00000000000000000"
)

// fakeAnthropicAPI answers GET /v1/models and POST /v1/messages (SSE)
// for the good and saved keys, 401 otherwise, and records the
// credentials it saw.
type fakeAnthropicAPI struct {
	srv         *httptest.Server
	mu          sync.Mutex
	keys, auths []string
}

func newFakeAnthropicAPI(t *testing.T) *fakeAnthropicAPI {
	t.Helper()
	f := &fakeAnthropicAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-api-key")
		f.mu.Lock()
		f.keys = append(f.keys, key)
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if key != antGoodKey && key != antSavedKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-5-5","display_name":"Claude Sonnet 5.5","type":"model","max_input_tokens":1000000,"max_tokens":128000,"created_at":"2026-08-01T00:00:00Z"},{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5","type":"model","max_input_tokens":1000000,"max_tokens":128000,"created_at":"2026-09-01T00:00:00Z"}],"has_more":false,"first_id":"claude-sonnet-5-5","last_id":"claude-opus-5-5"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			ev := func(typ, data string) { _, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data) }
			ev("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":1}}}`)
			ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"report_ready","input":{}}}`)
			ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`)
			ev("content_block_stop", `{"type":"content_block_stop","index":0}`)
			ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`)
			ev("message_stop", `{"type":"message_stop"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// seen is the x-api-key values seen, and whether any request carried an
// Authorization header.
func (f *fakeAnthropicAPI) seen() ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	auth := false
	for _, a := range f.auths {
		auth = auth || a != ""
	}
	return append([]string(nil), f.keys...), auth
}

// anthropicFixture is a setup fixture whose Anthropic API is the fake.
func anthropicFixture(t *testing.T, cfg config.Config) (*setupFixture, *fakeAnthropicAPI) {
	t.Helper()
	api := newFakeAnthropicAPI(t)
	f := newSetupFixture(t, cfg)
	f.sd.Endpoints.AnthropicBase = api.srv.URL
	return f, api
}

// noKeyIn fails when any of the test keys is in body.
func noKeyIn(t *testing.T, where, body string) {
	t.Helper()
	for _, k := range []string{antGoodKey, antBadKey, antSavedKey} {
		if strings.Contains(body, k) {
			t.Errorf("%s: a key reached the response", where)
		}
	}
}

func anthropicSave(key, model string) string {
	b, _ := json.Marshal(map[string]any{"gemini_key": goodKey, "voice": "gemini", "orchestrator": "anthropic", "anthropic_key": key, "anthropic_model": model})
	return string(b)
}

func TestSetupStateAnthropicPreset(t *testing.T) {
	f, _ := anthropicFixture(t, config.Default())
	st := setupState(t, f)
	a := st.Presets[0]
	if a.ID != "anthropic" || a.Label != "Anthropic" || a.Kind != onboard.PresetNative || a.DefaultModel != "claude-opus-5-5" || strings.Contains(a.Note, "★") || a.KeySaved {
		t.Errorf("first preset = %+v", a)
	}
	if st.Keys.Anthropic || st.Orchestrator.Preset != "gemini" {
		t.Errorf("nothing saved: keys %+v, preset %q", st.Keys, st.Orchestrator.Preset)
	}

	cfg := config.Default()
	cfg.Anthropic.APIKey = antSavedKey
	cfg.Brain.Provider = "anthropic"
	f, _ = anthropicFixture(t, cfg)
	st = setupState(t, f)
	if !st.Keys.Anthropic || !st.Presets[0].KeySaved || st.Orchestrator.Preset != "anthropic" {
		t.Errorf("saved: keys %+v, preset %+v, orchestrator %q", st.Keys, st.Presets[0], st.Orchestrator.Preset)
	}
	noKeyIn(t, "state", f.get(t, "/api/setup/state"))
}

func TestSetupValidateAnthropic(t *testing.T) {
	f, api := anthropicFixture(t, config.Default())
	_, body := f.post(t, "/api/setup/validate", `{"kind":"anthropic","key":"`+antGoodKey+`"}`)
	var c SetupCheck
	_ = json.Unmarshal([]byte(body), &c)
	if c.Status != "ok" || !strings.Contains(c.Detail, "valid") {
		t.Errorf("good key: %s", body)
	}
	_, body = f.post(t, "/api/setup/validate", `{"kind":"anthropic","key":"`+antBadKey+`"}`)
	c = SetupCheck{}
	_ = json.Unmarshal([]byte(body), &c)
	if c.Status != "fail" || !strings.Contains(c.Detail, "rejected the key") || !strings.Contains(c.Fix, "console.anthropic.com") || strings.Contains(strings.ToLower(body), "balance") {
		t.Errorf("bad key: %s", body)
	}
	noKeyIn(t, "validate", body)
	keys, auth := api.seen()
	if len(keys) != 2 || keys[0] != antGoodKey || keys[1] != antBadKey || auth {
		t.Errorf("the fake saw %d requests (Authorization %v)", len(keys), auth)
	}
}

func TestSetupSaveAnthropic(t *testing.T) {
	f, _ := anthropicFixture(t, config.Default())
	code, body := f.post(t, "/api/setup/keys", anthropicSave(antGoodKey, ""))
	if code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	noKeyIn(t, "save", body)
	f.waitRestart(t)
	env, _ := onboard.ReadEnvFile(f.env)
	if env["ANTHROPIC_API_KEY"] != antGoodKey || env["SHEEPDOG_BRAIN_PROVIDER"] != "anthropic" || env["SHEEPDOG_BRAIN_MODEL"] != "claude-opus-5-5" {
		t.Errorf("env file: key saved %v, provider %q, model %q", env["ANTHROPIC_API_KEY"] == antGoodKey, env["SHEEPDOG_BRAIN_PROVIDER"], env["SHEEPDOG_BRAIN_MODEL"])
	}
	// The daemon starts with that orchestrator.
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg, err := config.Load(f.sd.ConfigPath)
	if err != nil {
		t.Fatalf("the saved config does not load: %v", err)
	}
	if cfg.Brain.Provider != "anthropic" || cfg.Brain.Model != "claude-opus-5-5" || cfg.Anthropic.APIKey != antGoodKey {
		t.Errorf("config: provider %q, model %q, key set %v", cfg.Brain.Provider, cfg.Brain.Model, cfg.Anthropic.APIKey != "")
	}

	// Another model, the saved key kept.
	f.sd.Config.Anthropic.APIKey = antGoodKey
	code, body = f.post(t, "/api/setup/keys", anthropicSave("", "claude-sonnet-5-5"))
	if code != 200 || !strings.Contains(body, "keeping the saved key") {
		t.Fatalf("keep the key: %d %s", code, body)
	}
	if env, _ = onboard.ReadEnvFile(f.env); env["SHEEPDOG_BRAIN_MODEL"] != "claude-sonnet-5-5" {
		t.Errorf("model = %q", env["SHEEPDOG_BRAIN_MODEL"])
	}
}

func TestSetupSaveAnthropicRefusals(t *testing.T) {
	f, _ := anthropicFixture(t, config.Default())
	code, body := f.post(t, "/api/setup/keys", anthropicSave(antBadKey, ""))
	if code != 422 || !strings.Contains(body, "rejected the key") {
		t.Errorf("bad key: %d %s", code, body)
	}
	noKeyIn(t, "bad key", body)
	code, body = f.post(t, "/api/setup/keys", anthropicSave("", ""))
	if code != 422 || !strings.Contains(body, "ANTHROPIC_API_KEY is needed") || !strings.Contains(body, "console.anthropic.com") {
		t.Errorf("no key: %d %s", code, body)
	}
	code, body = f.post(t, "/api/setup/keys", anthropicSave(antGoodKey, "claude opus; rm"))
	if code != 400 {
		t.Errorf("bad model: %d %s", code, body)
	}
	if _, err := os.Stat(f.env); err == nil {
		if env, _ := onboard.ReadEnvFile(f.env); env["ANTHROPIC_API_KEY"] != "" || env["SHEEPDOG_BRAIN_PROVIDER"] != "" {
			t.Error("a refused save wrote the env file")
		}
	}
	// Other URL may not take the built-in name.
	code, body = f.post(t, "/api/setup/keys", saveReq("compat", map[string]any{"name": "anthropic", "base_url": "https://proxy.example/v1", "model": "m", "key": "k"}))
	if code != 400 || !strings.Contains(body, "built-in") {
		t.Errorf("compat named anthropic: %d %s", code, body)
	}
}

func TestSetupAnthropicModelsAndProbe(t *testing.T) {
	f, api := anthropicFixture(t, config.Default())
	_, body := f.post(t, "/api/setup/anthropic-models", `{}`)
	var r SetupModelsResponse
	_ = json.Unmarshal([]byte(body), &r)
	if r.Error == "" || r.Fix == "" || len(r.Models) != 0 {
		t.Errorf("no key: %s", body)
	}
	_, body = f.post(t, "/api/setup/anthropic-models", `{"key":"`+antGoodKey+`"}`)
	r = SetupModelsResponse{}
	_ = json.Unmarshal([]byte(body), &r)
	if r.Error != "" || len(r.Models) != 2 || r.Models[0].ID != "claude-opus-5-5" || r.Suggested != "claude-opus-5-5" || !r.ToolInfo {
		t.Errorf("models: %s", body)
	}
	_, body = f.post(t, "/api/setup/anthropic-models", `{"key":"`+antBadKey+`"}`)
	r = SetupModelsResponse{}
	_ = json.Unmarshal([]byte(body), &r)
	if !strings.Contains(r.Error, "rejected the key") {
		t.Errorf("bad key: %s", body)
	}
	noKeyIn(t, "models", body)

	_, body = f.post(t, "/api/setup/anthropic-probe", `{"key":"`+antGoodKey+`","model":"claude-opus-5-5"}`)
	var p SetupProbeResponse
	_ = json.Unmarshal([]byte(body), &p)
	if !p.OK || !strings.Contains(p.Detail, "calls tools") {
		t.Errorf("probe: %s", body)
	}
	_, body = f.post(t, "/api/setup/anthropic-probe", `{"key":"`+antBadKey+`","model":"claude-opus-5-5"}`)
	p = SetupProbeResponse{}
	_ = json.Unmarshal([]byte(body), &p)
	if p.OK || !strings.Contains(p.Detail, "rejected the key") || strings.Contains(p.Fix, "doctor") {
		t.Errorf("probe, bad key: %s", body)
	}
	noKeyIn(t, "probe", body)
	_, body = f.post(t, "/api/setup/anthropic-probe", `{"model":"claude-opus-5-5"}`)
	p = SetupProbeResponse{}
	_ = json.Unmarshal([]byte(body), &p)
	if p.OK || p.Fix == "" {
		t.Errorf("probe, no key: %s", body)
	}
	if _, auth := api.seen(); auth {
		t.Error("an Authorization header reached the Anthropic API")
	}
}

// TestSetupAnthropicSavedKey: an empty key uses the saved one, only
// toward the Anthropic API, never toward a host typed under Other URL.
func TestSetupAnthropicSavedKey(t *testing.T) {
	cfg := config.Default()
	cfg.Anthropic.APIKey = antSavedKey
	f, api := anthropicFixture(t, cfg)
	if err := os.WriteFile(f.env, []byte("ANTHROPIC_API_KEY="+antSavedKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, body := f.post(t, "/api/setup/anthropic-models", `{}`)
	if !strings.Contains(body, "claude-opus-5-5") {
		t.Errorf("saved key: %s", body)
	}
	_, body = f.post(t, "/api/setup/anthropic-probe", `{"model":"claude-opus-5-5"}`)
	if !strings.Contains(body, `"ok": true`) {
		t.Errorf("probe with the saved key: %s", body)
	}
	if keys, auth := api.seen(); len(keys) != 2 || keys[0] != antSavedKey || keys[1] != antSavedKey || auth {
		t.Errorf("the Anthropic API saw %d requests (Authorization %v)", len(keys), auth)
	}

	// Other URL named anthropic at a typed host gets no saved key.
	other := newFakeAnthropicAPI(t)
	for _, path := range []string{"/api/setup/models", "/api/setup/probe"} {
		f.post(t, path, `{"name":"anthropic","base_url":"`+other.srv.URL+`/v1","model":"m"}`)
		f.post(t, path, `{"preset":"anthropic","name":"anthropic","base_url":"`+other.srv.URL+`/v1","model":"m"}`)
	}
	keys, auth := other.seen()
	if len(keys) == 0 {
		t.Fatal("the typed host saw no request")
	}
	for _, k := range keys {
		if k != "" {
			t.Error("a saved key went to a typed host (x-api-key)")
		}
	}
	if auth {
		t.Error("a saved key went to a typed host (Authorization)")
	}
}
