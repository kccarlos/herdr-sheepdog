package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// TestMain clears every key and token the developer's shell exports, so
// no test can read, send or print a real one.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		u := strings.ToUpper(k)
		secret := strings.Contains(u, "KEY") || strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET")
		for _, p := range []string{"GEMINI_", "VERTEX_", "OPENAI_", "DEEPSEEK_", "OPENROUTER_", "ANTHROPIC_", "SHEEPDOG_"} {
			secret = secret || strings.HasPrefix(u, p)
		}
		if secret {
			_ = os.Unsetenv(k)
		}
	}
	os.Exit(m.Run())
}

const voiceOpenAIKey = "sk-voice-openai-key-0000000000000000"

// newFakeLocal is a local server without auth (Ollama, LM Studio): GET
// /v1/models lists ids, a chat answers with a tool call.
func newFakeLocal(t *testing.T, models ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			var ids []string
			for _, m := range models {
				ids = append(ids, `{"id":"`+m+`"}`)
			}
			_, _ = io.WriteString(w, `{"data":[`+strings.Join(ids, ",")+`]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// routed makes the fixture's HTTP client reach srv for host, so a
// fake can stand in for a remote API under a name that is not loopback
// (a loopback base URL needs no key). It returns http://<host>/v1.
func routed(t *testing.T, f *setupFixture, srv *httptest.Server, host string) string {
	t.Helper()
	addr := srv.Listener.Addr().String()
	f.sd.Endpoints.HTTP = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, to string) (net.Conn, error) {
			if h, _, _ := net.SplitHostPort(to); h == host {
				to = addr
			}
			return (&net.Dialer{}).DialContext(ctx, network, to)
		},
	}}
	return "http://" + host + "/v1"
}

// closedURL is a loopback URL nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := "http://" + l.Addr().String() + "/v1"
	_ = l.Close()
	return u
}

// testPresets are the real presets with their base URLs pointed at the
// given servers (preset id → base URL).
func testPresets(bases map[string]string) []onboard.Preset {
	out := onboard.Presets()
	for i := range out {
		if b, ok := bases[out[i].ID]; ok {
			out[i].BaseURL = b
		}
	}
	return out
}

// reload is the config the daemon would start with after a save: the
// config file, with the keys from the env file.
func reload(t *testing.T, f *setupFixture) config.Config {
	t.Helper()
	cfg, err := config.Load(f.sd.ConfigPath)
	if err != nil {
		t.Fatalf("the saved config does not load: %v", err)
	}
	env, _ := onboard.ReadEnvFile(f.env)
	for i := range cfg.Brain.OpenAICompat {
		cfg.Brain.OpenAICompat[i].APIKey = env[cfg.Brain.OpenAICompat[i].APIKeyEnv]
	}
	cfg.Voice.OpenAI.APIKey = env["OPENAI_API_KEY"]
	cfg.Push.Mode = "off"
	cfg.Gemini.APIKeys = f.sd.Config.Gemini.APIKeys
	return cfg
}

func setupState(t *testing.T, f *setupFixture) SetupState {
	t.Helper()
	var st SetupState
	if err := json.Unmarshal([]byte(f.get(t, "/api/setup/state")), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSetupStateListsThePresets(t *testing.T) {
	f := newSetupFixture(t, config.Default(), "")
	st := setupState(t, f)
	var ids []string
	for _, p := range st.Presets {
		ids = append(ids, p.ID)
	}
	if got := strings.Join(ids, ","); got != "anthropic,gemini,deepseek,vertex,openrouter,openai,ollama,lmstudio,other" {
		t.Fatalf("presets = %s", got)
	}
	want := map[string]struct{ base, env, note string }{
		"openrouter": {"https://openrouter.ai/api/v1", "OPENROUTER_API_KEY", "one key, many models"},
		"openai":     {"https://api.openai.com/v1", "OPENAI_ORCH_API_KEY", ""},
		"ollama":     {"http://localhost:11434/v1", "", ""},
		"lmstudio":   {"http://localhost:1234/v1", "", ""},
		"deepseek":   {"", "DEEPSEEK_API_KEY", "★ most recommended · 92 % understood in our tests · fast, low cost"},
		"gemini":     {"", "GEMINI_API_KEY", "free, can be slow or busy"},
		"anthropic":  {"", "ANTHROPIC_API_KEY", "92 % understood in our tests · fewer unsupported statements · slower, costs more"},
	}
	for _, p := range st.Presets {
		w, ok := want[p.ID]
		if !ok {
			continue
		}
		if p.BaseURL != w.base || p.KeyEnv != w.env || (w.note != "" && p.Note != w.note) {
			t.Errorf("%s = %+v", p.ID, p.Preset)
		}
		if p.Local != (p.ID == "ollama" || p.ID == "lmstudio") || p.KeyNeeded == p.Local {
			t.Errorf("%s: local %v, key needed %v", p.ID, p.Local, p.KeyNeeded)
		}
		if p.KeySaved || p.ReuseOffered || p.ReuseActive {
			t.Errorf("%s: nothing is saved yet: %+v", p.ID, p)
		}
	}
	if st.Orchestrator.Preset != "gemini" {
		t.Errorf("default preset = %q", st.Orchestrator.Preset)
	}
}

func TestSetupStatePresetsShowWhatIsSaved(t *testing.T) {
	cfg := config.Default()
	cfg.Voice.OpenAI.APIKey = voiceOpenAIKey
	cfg.Brain.Provider = "openai"
	cfg.Brain.OpenAICompat = []config.OpenAICompat{
		{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_API_KEY", Model: "m", APIKey: voiceOpenAIKey},
		{Name: "ollama", BaseURL: "http://192.168.1.9:11434/v1", Model: "q"},
	}
	f := newSetupFixture(t, cfg, "")
	if err := os.WriteFile(f.env, []byte("OPENROUTER_API_KEY="+secretCompatKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := setupState(t, f)
	by := map[string]SetupPreset{}
	for _, p := range st.Presets {
		by[p.ID] = p
	}
	if !by["openrouter"].KeySaved || by["openai"].KeySaved || !by["openai"].ReuseOffered || !by["openai"].ReuseActive {
		t.Errorf("openrouter %+v, openai %+v", by["openrouter"], by["openai"])
	}
	if st.Orchestrator.Preset != "openai" {
		t.Errorf("preset = %q", st.Orchestrator.Preset)
	}
	// An entry named after a preset with another base URL is Other URL.
	if p, base := f.h.srv.presetOf("ollama"); p != "other" || base != "http://192.168.1.9:11434/v1" {
		t.Errorf("moved ollama = %q %q", p, base)
	}
	raw := f.get(t, "/api/setup/state")
	if strings.Contains(raw, voiceOpenAIKey) || strings.Contains(raw, secretCompatKey) {
		t.Error("a key reached the state")
	}
}

// saveReq is a models-step body that keeps the Gemini voice.
func saveReq(orch string, compat map[string]any) string {
	b, _ := json.Marshal(map[string]any{"gemini_key": goodKey, "voice": "gemini", "orchestrator": orch, "compat": compat})
	return string(b)
}

func TestSetupSaveKeyedPresets(t *testing.T) {
	or := newFakeCompat(t, `{"data":[{"id":"x/tools","supported_parameters":["tools"]}]}`)
	oa := newFakeCompat(t, `{"data":[{"id":"gpt-9-mini"}]}`)
	f := newSetupFixture(t, config.Default(), "")
	f.sd.Presets = testPresets(map[string]string{"openrouter": or.URL + "/v1", "openai": oa.URL + "/v1"})

	code, body := f.post(t, "/api/setup/keys", saveReq("openrouter", map[string]any{"model": "x/tools", "key": secretCompatKey, "name": "ignored", "base_url": "https://ignored.example/v1"}))
	if code != 200 {
		t.Fatalf("openrouter: %d %s", code, body)
	}
	cfg := reload(t, f)
	c, ok := cfg.Brain.CompatProvider("openrouter")
	env, _ := onboard.ReadEnvFile(f.env)
	if !ok || c.BaseURL != or.URL+"/v1" || c.APIKeyEnv != "OPENROUTER_API_KEY" || c.Model != "x/tools" {
		t.Errorf("openrouter entry = %+v", c)
	}
	if env["OPENROUTER_API_KEY"] != secretCompatKey || env["SHEEPDOG_BRAIN_PROVIDER"] != "openrouter" || env["SHEEPDOG_BRAIN_MODEL"] != "x/tools" {
		t.Errorf("env = %v", keysOf(env))
	}

	// OpenAI without reuse: its own key, under its own name.
	f.sd.Config = reload(t, f)
	code, body = f.post(t, "/api/setup/keys", saveReq("openai", map[string]any{"model": "gpt-9-mini", "key": secretCompatKey}))
	if code != 200 {
		t.Fatalf("openai: %d %s", code, body)
	}
	cfg = reload(t, f)
	c, _ = cfg.Brain.CompatProvider("openai")
	env, _ = onboard.ReadEnvFile(f.env)
	if c.APIKeyEnv != onboard.OpenAIOrchKeyEnv || env[onboard.OpenAIOrchKeyEnv] != secretCompatKey || env["OPENAI_API_KEY"] != "" {
		t.Errorf("openai entry %+v, env %v", c, keysOf(env))
	}
	if _, still := cfg.Brain.CompatProvider("openrouter"); !still {
		t.Error("the openrouter entry was dropped")
	}
	if strings.Contains(body, secretCompatKey) {
		t.Error("the key came back")
	}
}

func TestSetupSaveOpenAIReuseOnAndOff(t *testing.T) {
	oa := newFakeCompat(t, `{"data":[{"id":"gpt-9-mini"}]}`)
	cfg := config.Default()
	cfg.Gemini.APIKeys = []string{goodKey}
	f := newSetupFixture(t, cfg, "")
	f.sd.Presets = testPresets(map[string]string{"openai": routed(t, f, oa.Server, "api.openai.test")})

	// No voice key saved: reuse has nothing to point at.
	code, body := f.post(t, "/api/setup/keys", saveReq("openai", map[string]any{"model": "gpt-9-mini", "reuse_key": true}))
	if code != 422 || !strings.Contains(body, "no OpenAI voice key is saved to reuse") {
		t.Fatalf("reuse without a voice key: %d %s", code, body)
	}

	if err := os.WriteFile(f.env, []byte("OPENAI_API_KEY="+voiceOpenAIKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.sd.Config.Voice.OpenAI.APIKey = voiceOpenAIKey
	// Reuse on: the entry points at the voice key; a key sent along is
	// ignored, so the voice key is never overwritten from here.
	code, body = f.post(t, "/api/setup/keys", saveReq("openai", map[string]any{"model": "gpt-9-mini", "reuse_key": true, "key": "sk-should-not-be-written-000000"}))
	if code != 200 || !strings.Contains(body, "using the same OpenAI key as the voice") {
		t.Fatalf("reuse on: %d %s", code, body)
	}
	got := reload(t, f)
	c, _ := got.Brain.CompatProvider("openai")
	env, _ := onboard.ReadEnvFile(f.env)
	if c.APIKeyEnv != "OPENAI_API_KEY" || env["OPENAI_API_KEY"] != voiceOpenAIKey || env[onboard.OpenAIOrchKeyEnv] != "" {
		t.Errorf("reuse on: entry %+v, env %v", c, keysOf(env))
	}
	raw, _ := os.ReadFile(f.env)
	if strings.Contains(string(raw), "sk-should-not-be-written") {
		t.Error("the ignored key was written")
	}
	f.sd.Config = got
	for _, p := range setupState(t, f).Presets {
		if p.ID == "openai" && !p.ReuseActive {
			t.Errorf("reuse not shown as active: %+v", p)
		}
	}

	// Reuse off without a key of its own: the voice key does not count.
	code, body = f.post(t, "/api/setup/keys", saveReq("openai", map[string]any{"model": "gpt-9-mini"}))
	if code != 422 || !strings.Contains(body, onboard.OpenAIOrchKeyEnv) {
		t.Fatalf("reuse off, no key: %d %s", code, body)
	}
	// Reuse off with its own key: the same entry flips to it.
	code, body = f.post(t, "/api/setup/keys", saveReq("openai", map[string]any{"model": "gpt-9-mini", "key": secretCompatKey}))
	if code != 200 {
		t.Fatalf("reuse off: %d %s", code, body)
	}
	got = reload(t, f)
	c, _ = got.Brain.CompatProvider("openai")
	env, _ = onboard.ReadEnvFile(f.env)
	if c.APIKeyEnv != onboard.OpenAIOrchKeyEnv || env[onboard.OpenAIOrchKeyEnv] != secretCompatKey || env["OPENAI_API_KEY"] != voiceOpenAIKey || len(got.Brain.OpenAICompat) != 1 {
		t.Errorf("reuse off: entry %+v (%d), env %v", c, len(got.Brain.OpenAICompat), keysOf(env))
	}
}

func TestSetupSaveOpenAIUnderOtherURLNeverUsesTheVoiceKey(t *testing.T) {
	oa := newFakeCompat(t, `{"data":[{"id":"gpt-9-mini"}]}`)
	cfg := config.Default()
	cfg.Voice.OpenAI.APIKey = voiceOpenAIKey
	f := newSetupFixture(t, cfg, "")
	base := routed(t, f, oa.Server, "openai.example.test")
	code, body := f.post(t, "/api/setup/keys", saveReq("compat", map[string]any{"name": "openai", "base_url": base, "model": "gpt-9-mini", "reuse_key": true}))
	if code != 422 || !strings.Contains(body, onboard.OpenAIOrchKeyEnv) {
		t.Fatalf("typed openai without a key: %d %s", code, body)
	}
}

func TestSetupSaveLocalPresets(t *testing.T) {
	for _, id := range []string{"ollama", "lmstudio"} {
		t.Run(id, func(t *testing.T) {
			srv := newFakeLocal(t, "qwen3:8b")
			f := newSetupFixture(t, config.Default(), "")
			f.sd.Presets = testPresets(map[string]string{id: srv.URL + "/v1"})
			code, body := f.post(t, "/api/setup/keys", saveReq(id, map[string]any{"model": "qwen3:8b"}))
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			cfg := reload(t, f)
			c, ok := cfg.Brain.CompatProvider(id)
			env, _ := onboard.ReadEnvFile(f.env)
			if !ok || c.BaseURL != srv.URL+"/v1" || c.Model != "qwen3:8b" || !c.KeyOptional() {
				t.Errorf("entry = %+v", c)
			}
			if env["SHEEPDOG_BRAIN_PROVIDER"] != id || env[c.APIKeyEnv] != "" {
				t.Errorf("env = %v", keysOf(env))
			}
			// A model change updates the same entry.
			if code, body := f.post(t, "/api/setup/keys", saveReq(id, map[string]any{"model": "other:1b"})); code != 200 {
				t.Fatalf("update: %d %s", code, body)
			}
			cfg = reload(t, f)
			if c, _ := cfg.Brain.CompatProvider(id); c.Model != "other:1b" || len(cfg.Brain.OpenAICompat) != 1 {
				t.Errorf("updated = %+v (%d entries)", c, len(cfg.Brain.OpenAICompat))
			}
		})
	}
}

func TestSetupSavePresetNeedsAModel(t *testing.T) {
	f := newSetupFixture(t, config.Default(), "")
	code, body := f.post(t, "/api/setup/keys", saveReq("openrouter", map[string]any{"key": secretCompatKey}))
	if code != 400 || !strings.Contains(body, "model") {
		t.Errorf("%d %s", code, body)
	}
	code, body = f.post(t, "/api/setup/keys", saveReq("mistral", nil))
	if code != 400 || !strings.Contains(body, "anthropic") {
		t.Errorf("unknown preset: %d %s", code, body)
	}
}

func TestSetupLocalDetection(t *testing.T) {
	srv := newFakeLocal(t, "nomic-embed-text", "llama3.2:3b", "qwen3:8b")
	empty := newFakeLocal(t)
	f := newSetupFixture(t, config.Default(), "")
	f.sd.Presets = testPresets(map[string]string{"ollama": srv.URL + "/v1", "lmstudio": closedURL(t)})

	_, body := f.post(t, "/api/setup/models", `{"preset":"ollama","base_url":"http://ignored.example/v1"}`)
	r := decodeModels(t, body)
	if r.Running == nil || !*r.Running || len(r.Models) != 3 || r.Suggested != "llama3.2:3b" || r.Error != "" {
		t.Errorf("running: %s", body)
	}
	_, body = f.post(t, "/api/setup/models", `{"preset":"lmstudio"}`)
	r = decodeModels(t, body)
	if r.Running == nil || *r.Running || r.Error != "LM Studio is not running." || !strings.Contains(r.Fix, "Start LM Studio") {
		t.Errorf("not running: %s", body)
	}
	f.sd.Presets = testPresets(map[string]string{"ollama": empty.URL + "/v1"})
	_, body = f.post(t, "/api/setup/models", `{"preset":"ollama"}`)
	r = decodeModels(t, body)
	if r.Running == nil || !*r.Running || !strings.Contains(r.Fix, "ollama pull") {
		t.Errorf("no models: %s", body)
	}
}

func TestSetupModelsSuggestsAnOpenAIModelAndHidesNonChat(t *testing.T) {
	oa := newFakeCompat(t, `{"data":[{"id":"gpt-4.1-mini"},{"id":"gpt-9-mini"},{"id":"gpt-9-mini-2031-01-01"},{"id":"gpt-9-mini-tts"},{"id":"text-embedding-3-small"},{"id":"gpt-9"}]}`)
	f := newSetupFixture(t, config.Default(), "")
	f.sd.Presets = testPresets(map[string]string{"openai": oa.URL + "/v1"})
	_, body := f.post(t, "/api/setup/models", `{"preset":"openai","key":"`+secretCompatKey+`"}`)
	r := decodeModels(t, body)
	var ids []string
	for _, m := range r.Models {
		ids = append(ids, m.ID)
	}
	if r.Suggested != "gpt-9-mini" || strings.Contains(strings.Join(ids, ","), "tts") || strings.Contains(strings.Join(ids, ","), "embedding") {
		t.Errorf("suggested %q from %v", r.Suggested, ids)
	}
	if r.Running != nil {
		t.Error("a remote preset has no running state")
	}
}

func TestSetupReuseSendsTheVoiceKeyOnlyToOpenAI(t *testing.T) {
	oa := newFakeCompat(t, `{"data":[{"id":"gpt-9-mini"}]}`)
	elsewhere := newFakeCompat(t, `{"data":[{"id":"m"}]}`)
	cfg := config.Default()
	cfg.Voice.OpenAI.APIKey = secretCompatKey // the fakes accept this one
	f := newSetupFixture(t, cfg, "")
	f.sd.Presets = testPresets(map[string]string{"openai": oa.URL + "/v1"})

	_, body := f.post(t, "/api/setup/models", `{"preset":"openai"}`)
	if r := decodeModels(t, body); r.Error == "" {
		t.Errorf("without reuse the voice key was used: %s", body)
	}
	_, body = f.post(t, "/api/setup/models", `{"preset":"openai","reuse":true}`)
	if r := decodeModels(t, body); r.Error != "" || len(r.Models) != 1 {
		t.Errorf("with reuse: %s", body)
	}
	// Reuse asked for another host (Other URL): no key goes there.
	_, _ = f.post(t, "/api/setup/models", `{"name":"openai","base_url":"`+elsewhere.URL+`/v1","reuse":true}`)
	_, _ = f.post(t, "/api/setup/probe", `{"name":"openai","base_url":"`+elsewhere.URL+`/v1","reuse":true,"model":"m"}`)
	for _, a := range elsewhere.seenAuth {
		if strings.Contains(a, secretCompatKey) {
			t.Error("the voice key was sent to another host")
		}
	}
}

func TestSetupGeminiOrchestratorModel(t *testing.T) {
	gem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != goodKey || strings.Contains(r.URL.RawQuery, goodKey) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"API key not valid.","status":"INVALID_ARGUMENT"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"models":[
			{"name":"models/gemini-3.5-flash","supportedGenerationMethods":["generateContent","countTokens"]},
			{"name":"models/gemini-3.8-flash","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-3.8-live","supportedGenerationMethods":["bidiGenerateContent"]},
			{"name":"models/gemini-3.5-flash-tts","supportedGenerationMethods":["generateContent"]},
			{"name":"models/text-embedding-005","supportedGenerationMethods":["embedContent"]}]}`)
	}))
	t.Cleanup(gem.Close)
	cfg := config.Default()
	cfg.Gemini.APIKeys = []string{goodKey}
	f := newSetupFixture(t, cfg, "")
	f.sd.Endpoints.GeminiBase = gem.URL

	_, body := f.post(t, "/api/setup/gemini-models", `{}`)
	var r SetupGeminiModelsResponse
	_ = json.Unmarshal([]byte(body), &r)
	if strings.Join(r.Models, ",") != "gemini-3.8-flash,gemini-3.5-flash" || r.Default != "gemini-3.5-flash" || r.Error != "" {
		t.Errorf("saved key: %s", body)
	}
	_, body = f.post(t, "/api/setup/gemini-models", `{"key":"AIzaWrongKeyForTheSetupTest000000000"}`)
	r = SetupGeminiModelsResponse{}
	_ = json.Unmarshal([]byte(body), &r)
	if r.Error == "" || len(r.Models) != 0 || strings.Contains(body, "AIzaWrong") {
		t.Errorf("wrong key: %s", body)
	}

	code, body := f.post(t, "/api/setup/keys", `{"voice":"gemini","orchestrator":"gemini","gemini_model":"gemini-3.8-flash"}`)
	env, _ := onboard.ReadEnvFile(f.env)
	if code != 200 || env["SHEEPDOG_BRAIN_PROVIDER"] != "gemini" || env["SHEEPDOG_BRAIN_MODEL"] != "gemini-3.8-flash" {
		t.Errorf("save: %d %s %v", code, body, keysOf(env))
	}
}

func TestSetupPresetsNeverLeakAKey(t *testing.T) {
	const sentinel = "sk-SENTINEL-presets-0000000000000000"
	oa := newFakeCompat(t, `{"data":[{"id":"gpt-9-mini"}]}`)
	f := newSetupFixture(t, config.Default(), "")
	f.sd.Presets = testPresets(map[string]string{"openai": oa.URL + "/v1", "ollama": closedURL(t)})
	var out bytes.Buffer
	for _, c := range []struct{ path, body string }{
		{"/api/setup/models", `{"preset":"openai","key":"` + sentinel + `"}`},
		{"/api/setup/probe", `{"preset":"openai","key":"` + sentinel + `","model":"gpt-9-mini"}`},
		{"/api/setup/validate", `{"kind":"compat","preset":"openai","key":"` + sentinel + `"}`},
		{"/api/setup/keys", saveReq("openai", map[string]any{"model": "gpt-9-mini", "key": sentinel})},
		{"/api/setup/models", `{"preset":"ollama","key":"` + sentinel + `"}`},
	} {
		_, body := f.post(t, c.path, c.body)
		out.WriteString(body)
	}
	out.WriteString(f.get(t, "/api/setup/state"))
	evs, _, _ := f.h.log.Since(0, 100000)
	for _, ev := range evs {
		b, _ := json.Marshal(ev)
		out.Write(b)
	}
	if !strings.Contains(out.String(), "rejected the key") && !strings.Contains(out.String(), "valid API key") {
		t.Fatalf("the fake never refused the sentinel (so the check below proves nothing)")
	}
	if strings.Contains(out.String(), sentinel) {
		t.Error("the sentinel key reached a response or the event log")
	}
}
