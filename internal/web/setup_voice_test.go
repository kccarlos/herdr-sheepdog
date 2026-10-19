package web

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeLiveModels is Google's model list for goodKey: two Live models
// (bidiGenerateContent), text and embedding models. Any other key is
// refused with a message that repeats it (so a leak would show).
func fakeLiveModels(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-goog-api-key")
		if key != goodKey || strings.Contains(r.URL.RawQuery, "key") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"API key not valid: `+key+`","status":"INVALID_ARGUMENT"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"models":[
			{"name":"models/gemini-3.5-flash","supportedGenerationMethods":["generateContent","countTokens"]},
			{"name":"models/gemini-3.8-live","supportedGenerationMethods":["bidiGenerateContent","countTokens"]},
			{"name":"models/gemini-3.5-flash-live","supportedGenerationMethods":["bidiGenerateContent"]},
			{"name":"models/gemini-3.8-live","supportedGenerationMethods":["bidiGenerateContent"]},
			{"name":"models/text-embedding-005","supportedGenerationMethods":["embedContent"]}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// fakeOpenAIModels lists GPT-Live and chat models for voiceOpenAIKey.
func fakeOpenAIModels(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key != voiceOpenAIKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided: `+key+`","type":"invalid_request_error","code":"invalid_api_key"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-9-mini"},{"id":"gpt-live-1"},{"id":"gpt-live-2"},{"id":"gpt-realtime"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

func voiceModels(t *testing.T, f *setupFixture, body string) (SetupVoiceModelsResponse, string) {
	t.Helper()
	code, raw := f.post(t, "/api/setup/voice-models", body)
	if code != 200 {
		t.Fatalf("voice-models %s: %d %s", body, code, raw)
	}
	var r SetupVoiceModelsResponse
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	return r, raw
}

// TestSetupVoiceModelsKeepsLiveModels: the Gemini picker lists the models
// that support bidiGenerateContent, the configured one first; a refused
// key falls back to the configured model with the reason.
func TestSetupVoiceModelsKeepsLiveModels(t *testing.T) {
	cfg := config.Default()
	cfg.Gemini.APIKeys = []string{goodKey}
	f := newSetupFixture(t, cfg, "")
	f.sd.Endpoints.GeminiBase = fakeLiveModels(t)

	r, raw := voiceModels(t, f, `{"provider":"gemini"}`)
	if strings.Join(r.Models, ",") != "gemini-3.8-live,gemini-3.5-flash-live" || !r.Listed || r.Current != "gemini-3.8-live" || r.Error != "" {
		t.Errorf("saved key: %s", raw)
	}

	f.sd.Config.Gemini.Model = "gemini-old-live"
	r, raw = voiceModels(t, f, `{"provider":"gemini","key":"`+goodKey+`"}`)
	if strings.Join(r.Models, ",") != "gemini-old-live,gemini-3.8-live,gemini-3.5-flash-live" {
		t.Errorf("a hand-set model is kept first: %s", raw)
	}

	const wrong = "AIzaWrongKeyForTheVoiceTest000000000"
	r, raw = voiceModels(t, f, `{"provider":"gemini","key":"`+wrong+`"}`)
	if r.Listed || r.Error == "" || strings.Join(r.Models, ",") != "gemini-old-live,gemini-3.8-live" || strings.Contains(raw, wrong) {
		t.Errorf("wrong key: %s", raw)
	}

	f.sd.Config.Gemini.Model = ""
	r, raw = voiceModels(t, f, `{"provider":"vertex"}`)
	if strings.Join(r.Models, ",") != "gemini-3.8-live" || r.Listed || r.Note == "" {
		t.Errorf("vertex: %s", raw)
	}
	// An AI Studio pick is not offered on Vertex; a model the Vertex voice
	// already runs on is.
	f.sd.Config.Gemini.Model = "gemini-3.5-flash-live"
	if r, raw = voiceModels(t, f, `{"provider":"vertex"}`); strings.Join(r.Models, ",") != "gemini-3.8-live" {
		t.Errorf("vertex after an AI Studio pick: %s", raw)
	}
	f.sd.Config.Gemini.Backend = "vertex"
	if r, raw = voiceModels(t, f, `{"provider":"vertex"}`); strings.Join(r.Models, ",") != "gemini-3.5-flash-live,gemini-3.8-live" {
		t.Errorf("vertex with its own model: %s", raw)
	}
	if code, _ := f.post(t, "/api/setup/voice-models", `{"provider":"elevenlabs"}`); code != 400 {
		t.Errorf("unknown provider: %d", code)
	}
}

func TestSetupVoiceModelsGPTLive(t *testing.T) {
	f := newSetupFixture(t, config.Default(), "")
	f.sd.Endpoints.OpenAIBase = fakeOpenAIModels(t)
	r, raw := voiceModels(t, f, `{"provider":"openai","key":"`+voiceOpenAIKey+`"}`)
	if strings.Join(r.Models, ",") != "gpt-live-1,gpt-live-2" || r.Default != "gpt-live-1" || !r.Listed || strings.Contains(raw, voiceOpenAIKey) {
		t.Errorf("listed: %s", raw)
	}
	// No key saved and none typed: the default, and why.
	r, raw = voiceModels(t, f, `{"provider":"openai"}`)
	if strings.Join(r.Models, ",") != "gpt-live-1" || r.Error == "" {
		t.Errorf("no key: %s", raw)
	}
}

func TestSetupStateHasTheVoicePicks(t *testing.T) {
	cfg := config.Default()
	cfg.Gemini.Voice = "Fenrir" // set by hand: offered too
	f := newSetupFixture(t, cfg, "")
	st := setupState(t, f)
	g, o := st.Voice.Gemini, st.Voice.OpenAI
	if g.Model != "gemini-3.8-live" || g.Voice != "Fenrir" || g.DefaultVoice != "Puck" || strings.Join(g.Voices, ",") != "Puck,Kore,Charon,Aoede,Fenrir" {
		t.Errorf("gemini pick: %+v", g)
	}
	if o.Model != "gpt-live-1" || o.Voice != "marin" || o.Voices[0] != "marin" || len(o.Voices) < 10 {
		t.Errorf("openai pick: %+v", o)
	}
	if st.Voice.OpenAIPrice != "$0.05 per minute of open session" {
		t.Errorf("price: %q", st.Voice.OpenAIPrice)
	}
}

// TestSetupSaveVoiceChoice: each voice card writes its model and voice to
// its own env entries; an unknown voice or a bad model name saves nothing.
func TestSetupSaveVoiceChoice(t *testing.T) {
	cfg := config.Default()
	cfg.Gemini.APIKeys = []string{goodKey}
	f := newSetupFixture(t, cfg, "")
	f.sd.Endpoints.OpenAIBase = fakeOpenAIModels(t)

	for _, bad := range []string{
		`{"voice":"gemini","voice_name":"Brian"}`,
		`{"voice":"openai","openai_key":"` + voiceOpenAIKey + `","voice_name":"Puck"}`,
		`{"voice":"gemini","voice_model":"gemini-3.8-live\nSHEEPDOG_WEB_BIND=0.0.0.0"}`,
		`{"voice":"gemini","voice_model":"a model"}`,
	} {
		code, body := f.post(t, "/api/setup/keys", bad)
		if code != 400 || strings.Contains(body, voiceOpenAIKey) {
			t.Errorf("%s: %d %s", bad, code, body)
		}
		if _, err := os.Stat(f.env); !os.IsNotExist(err) {
			t.Fatalf("%s: wrote the env file", bad)
		}
	}

	code, body := f.post(t, "/api/setup/keys", `{"voice":"gemini","orchestrator":"gemini","voice_model":"gemini-3.5-flash-live","voice_name":"kore"}`)
	if code != 200 {
		t.Fatalf("gemini: %d %s", code, body)
	}
	env, _ := onboard.ReadEnvFile(f.env)
	if env["SHEEPDOG_GEMINI_MODEL"] != "gemini-3.5-flash-live" || env["SHEEPDOG_GEMINI_VOICE"] != "Kore" || env["SHEEPDOG_GEMINI_BACKEND"] != "aistudio" || env["SHEEPDOG_VOICE_PROVIDER"] != "gemini" {
		t.Errorf("gemini: env %v", keysOf(env))
	}
	if _, ok := env["SHEEPDOG_VOICE_OPENAI_VOICE"]; ok {
		t.Error("gemini wrote GPT-Live's voice")
	}

	code, body = f.post(t, "/api/setup/keys", `{"voice":"openai","orchestrator":"gemini","openai_key":"`+voiceOpenAIKey+`","voice_model":"gpt-live-2","voice_name":"Cedar"}`)
	if code != 200 || strings.Contains(body, voiceOpenAIKey) {
		t.Fatalf("openai: %d %s", code, body)
	}
	env, _ = onboard.ReadEnvFile(f.env)
	if env["SHEEPDOG_VOICE_PROVIDER"] != "openai" || env["SHEEPDOG_VOICE_OPENAI_MODEL"] != "gpt-live-2" || env["SHEEPDOG_VOICE_OPENAI_VOICE"] != "cedar" || env["OPENAI_API_KEY"] != voiceOpenAIKey {
		t.Errorf("openai: env %v", keysOf(env))
	}
	if env["SHEEPDOG_GEMINI_VOICE"] != "Kore" {
		t.Error("openai changed Gemini's voice")
	}

	// Empty choices keep what is saved.
	code, _ = f.post(t, "/api/setup/keys", `{"voice":"gemini","orchestrator":"gemini"}`)
	env, _ = onboard.ReadEnvFile(f.env)
	if code != 200 || env["SHEEPDOG_GEMINI_VOICE"] != "Kore" || env["SHEEPDOG_VOICE_OPENAI_MODEL"] != "gpt-live-2" {
		t.Errorf("keep: %d env %v", code, keysOf(env))
	}
	logged := f.logged(t)
	if !strings.Contains(logged, "SHEEPDOG_VOICE_OPENAI_VOICE") || strings.Contains(logged, voiceOpenAIKey) {
		t.Error("the save is not logged, or the key is")
	}
}

// TestSetupSaveVertexVoiceModel: the Vertex voice's model is checked live
// at the location (with the saved key when none is typed) before it is
// saved.
func TestSetupSaveVertexVoiceModel(t *testing.T) {
	cfg := config.Default()
	cfg.Gemini.Vertex = config.GeminiVertex{APIKey: vertexKey, Project: "good-proj", Location: "us-central1"}
	f := newVertexFixture(t, cfg)
	var mu sync.Mutex
	var opened []string
	f.sd.Endpoints.VertexLive = func(ctx context.Context, key, project, location, model string) error {
		mu.Lock()
		opened = append(opened, model)
		mu.Unlock()
		if model != "gemini-3.8-live" {
			return errors.New("websocket: close 1008 (policy violation): Publisher model `projects/p/locations/" + location + "/publishers/google/models/" + model + "` was not found")
		}
		return fakeLive(ctx, key, project, location, model)
	}
	code, body := f.post(t, "/api/setup/keys", `{"voice":"vertex","orchestrator":"vertex","voice_model":"gemini-nope-live","vertex":{"project":"good-proj","location":"us-central1"}}`)
	if code != 422 || strings.Contains(body, vertexKey) {
		t.Fatalf("unserved model: %d %s", code, body)
	}
	code, body = f.post(t, "/api/setup/keys", `{"voice":"vertex","orchestrator":"vertex","voice_model":"gemini-3.8-live","voice_name":"Charon","vertex":{"project":"good-proj","location":"us-central1"}}`)
	if code != 200 {
		t.Fatalf("save: %d %s", code, body)
	}
	env, _ := onboard.ReadEnvFile(f.env)
	if env["SHEEPDOG_GEMINI_BACKEND"] != "vertex" || env["SHEEPDOG_GEMINI_MODEL"] != "gemini-3.8-live" || env["SHEEPDOG_GEMINI_VOICE"] != "Charon" {
		t.Errorf("env %v", keysOf(env))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(opened) == 0 || opened[0] != "gemini-nope-live" {
		t.Errorf("the Live check opened %v", opened)
	}
	// The validate route takes the model too.
	_, raw := f.post(t, "/api/setup/validate", `{"kind":"vertex","project":"good-proj","voice":true,"location":"us-central1","live_model":"bad model"}`)
	if !strings.Contains(raw, `"fail"`) {
		t.Errorf("bad model name: %s", raw)
	}
}

func TestSetupServesTheVoiceModule(t *testing.T) {
	f := newSetupFixture(t, config.Default(), "")
	if !strings.Contains(f.get(t, "/setup/voice.js"), "export function talkLine") {
		t.Error("the page's voice module is not served")
	}
	page := f.get(t, "/setup/")
	for _, id := range []string{`id="voice-grid"`, `id="voice-pick"`, `id="voice-model"`, `id="voice-name"`} {
		if !strings.Contains(page, id) {
			t.Errorf("the page lacks %s", id)
		}
	}
	if strings.Contains(page, `id="advanced"`) {
		t.Error("the voice is still under Advanced")
	}
}
