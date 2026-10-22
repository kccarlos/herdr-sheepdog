package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

const vertexKey = "AQ.VertexSetupTestKey000000000000000"

// fakeVertexAPI answers countTokens: vertexKey on project good-proj is
// accepted; project off-proj has the API disabled; any other key is
// rejected with a message that repeats the key (so a leak would show).
func fakeVertexAPI(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("x-goog-api-key")
		switch {
		case key != vertexKey:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":400,"message":"API key not valid: `+key+`","status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}`)
		case strings.Contains(r.URL.Path, "/projects/off-proj/"):
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":403,"message":"Vertex AI API has not been used in project off-proj before or it is disabled.","status":"PERMISSION_DENIED","details":[{"reason":"SERVICE_DISABLED"}]}}`)
		case strings.Contains(r.URL.Path, "/projects/good-proj/"):
			_, _ = io.WriteString(w, `{"totalTokens":1}`)
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":403,"message":"Permission denied on resource project.","status":"PERMISSION_DENIED","details":[{"reason":"CONSUMER_INVALID"}]}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// fakeLive serves gemini-3.8-live at us-central1 and us only.
func fakeLive(_ context.Context, key, _, location, model string) error {
	if key != vertexKey {
		return errors.New("close 1008: API keys are not supported by this API")
	}
	if location != "us-central1" && location != "us" {
		return errors.New("websocket: close 1008 (policy violation): Publisher model `projects/p/locations/" + location + "/publishers/google/models/" + model + "` was not found")
	}
	return nil
}

func newVertexFixture(t *testing.T, cfg config.Config) *setupFixture {
	t.Helper()
	for _, k := range []string{"VERTEX_API_KEY", "VERTEX_PROJECT", "VERTEX_LOCATION", "SHEEPDOG_GEMINI_BACKEND", "SHEEPDOG_BRAIN_PROVIDER", "SHEEPDOG_BRAIN_MODEL", "SHEEPDOG_VOICE_PROVIDER", "GEMINI_API_KEY"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	f := newSetupFixture(t, cfg)
	f.sd.Endpoints.VertexBase = fakeVertexAPI(t)
	f.sd.Endpoints.VertexLive = fakeLive
	return f
}

func (f *setupFixture) logged(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	evs, _, _ := f.h.log.Since(0, 100000)
	for _, ev := range evs {
		j, _ := json.Marshal(ev)
		b.Write(j)
	}
	return b.String()
}

// TestSetupVertexValidate: the live check's plain errors, the location
// checked only for the voice, and no key in any answer.
func TestSetupVertexValidate(t *testing.T) {
	f := newVertexFixture(t, config.Default())
	bad := "AQ.RejectedVertexKey0000000000000000000"
	cases := []struct{ body, want string }{
		{`{"kind":"vertex","key":"` + vertexKey + `","project":"good-proj"}`, "Vertex AI accepted the key for project good-proj"},
		{`{"kind":"vertex","key":"` + vertexKey + `","project":"good-proj","location":"us-central1","voice":true}`, "us-central1 serves gemini-3.8-live"},
		{`{"kind":"vertex","key":"` + vertexKey + `","project":"good-proj","location":"global","voice":true}`, "location global does not serve gemini-3.8-live"},
		{`{"kind":"vertex","key":"` + bad + `","project":"good-proj"}`, "Vertex AI rejected the key"},
		{`{"kind":"vertex","key":"` + vertexKey + `","project":"off-proj"}`, "the Vertex AI API is not enabled in project off-proj"},
		{`{"kind":"vertex","key":"` + vertexKey + `","project":"nope"}`, "project nope was not found, or this key cannot use it"},
		{`{"kind":"vertex","key":"","project":"good-proj"}`, "no Vertex AI API key given"},
	}
	for _, c := range cases {
		code, body := f.post(t, "/api/setup/validate", c.body)
		if code != 200 || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s", c.body, code, body)
		}
		if strings.Contains(body, vertexKey) || strings.Contains(body, bad) {
			t.Errorf("a key is in the answer: %s", body)
		}
	}
	if !strings.Contains(f.get(t, "/api/setup/state"), `"default_location": "us-central1"`) {
		t.Error("state lacks the default location")
	}
	if !strings.Contains(f.get(t, "/setup/vertex.js"), "export function validateBody") {
		t.Error("the page's Vertex module is not served")
	}
}

// TestSetupVertexSaves: Vertex for the voice and the orchestrator needs
// no Gemini key; the key goes to the env file (0600), the project and
// location to config.toml (the env file's copies, which would hide them,
// are removed), the backend and provider choices to the env file, and the
// daemon reloads. No key in the answer, the state or the event log.
func TestSetupVertexSaves(t *testing.T) {
	f := newVertexFixture(t, config.Default())
	if err := onboard.UpsertEnvFile(f.env, map[string]string{"VERTEX_PROJECT": "old-proj", "VERTEX_LOCATION": "europe-west4", "OTHER": "kept"}); err != nil {
		t.Fatal(err)
	}
	// A location without the Live model: nothing saved.
	code, body := f.post(t, "/api/setup/keys", `{"voice":"vertex","orchestrator":"vertex","vertex":{"key":"`+vertexKey+`","project":"good-proj","location":"global"}}`)
	if code != 422 || !strings.Contains(body, "location global does not serve") || !strings.Contains(body, `"name": "VERTEX_API_KEY"`) {
		t.Fatalf("bad location: %d %s", code, body)
	}
	if _, err := os.Stat(f.sd.ConfigPath); !os.IsNotExist(err) {
		t.Fatal("a failed check wrote the config")
	}
	code, body = f.post(t, "/api/setup/keys", `{"voice":"vertex","orchestrator":"vertex","vertex":{"key":" `+vertexKey+` ","project":"good-proj","location":"us-central1","model":"gemini-3.8-flash"}}`)
	if code != 200 || !strings.Contains(body, `"restarting": true`) || strings.Contains(body, vertexKey) {
		t.Fatalf("save: %d %s", code, body)
	}
	f.waitRestart(t)
	env, _ := onboard.ReadEnvFile(f.env)
	want := map[string]string{"VERTEX_API_KEY": vertexKey, "SHEEPDOG_VOICE_PROVIDER": "gemini", "SHEEPDOG_GEMINI_BACKEND": "vertex", "SHEEPDOG_BRAIN_PROVIDER": "vertex", "SHEEPDOG_BRAIN_MODEL": "gemini-3.8-flash", "OTHER": "kept"}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env %s wrong (has %v)", k, keysOf(env))
		}
	}
	for _, k := range []string{"VERTEX_PROJECT", "VERTEX_LOCATION", "GEMINI_API_KEY"} {
		if _, ok := env[k]; ok {
			t.Errorf("env still has %s", k)
		}
	}
	if st, err := os.Stat(f.env); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("env file mode: %v", err)
	}
	cfg, err := config.Load(f.sd.ConfigPath)
	if err != nil || cfg.Gemini.Vertex.Project != "good-proj" || cfg.Gemini.Vertex.Location != "us-central1" || cfg.Gemini.Vertex.APIKey != "" {
		t.Errorf("config: %v project=%q location=%q key in config=%v", err, cfg.Gemini.Vertex.Project, cfg.Gemini.Vertex.Location, cfg.Gemini.Vertex.APIKey != "")
	}
	raw, _ := os.ReadFile(f.sd.ConfigPath)
	logged := f.logged(t)
	if !strings.Contains(logged, "VERTEX_API_KEY") {
		t.Fatal("the save is not in the event log (so the check below proves nothing)")
	}
	if strings.Contains(string(raw), vertexKey) || strings.Contains(logged, vertexKey) || strings.Contains(f.get(t, "/api/setup/state"), vertexKey) {
		t.Error("the key reached the config, the event log or the state")
	}

	// Back to AI Studio for the voice: the backend follows.
	f.sd.Config.Gemini.APIKeys = []string{goodKey}
	code, body = f.post(t, "/api/setup/keys", `{"voice":"gemini","orchestrator":"gemini"}`)
	if code != 200 {
		t.Fatalf("back: %d %s", code, body)
	}
	env, _ = onboard.ReadEnvFile(f.env)
	if env["SHEEPDOG_GEMINI_BACKEND"] != "aistudio" || env["SHEEPDOG_BRAIN_PROVIDER"] != "gemini" {
		t.Errorf("back: env %v", keysOf(env))
	}
	if _, ok := env["SHEEPDOG_BRAIN_MODEL"]; ok {
		t.Error("Vertex's model stayed behind")
	}
}

// TestSetupVertexSavedKeyOnlyForItsProject: an empty key field reuses the
// saved key for the project it was saved for, never for another one.
func TestSetupVertexSavedKeyOnlyForItsProject(t *testing.T) {
	cfg := config.Default()
	cfg.Gemini.Vertex = config.GeminiVertex{APIKey: vertexKey, Project: "good-proj", Location: "us-central1"}
	f := newVertexFixture(t, cfg)

	code, body := f.post(t, "/api/setup/validate", `{"kind":"vertex","project":"good-proj"}`)
	if code != 200 || !strings.Contains(body, `"status": "ok"`) {
		t.Errorf("same project: %d %s", code, body)
	}
	code, body = f.post(t, "/api/setup/validate", `{"kind":"vertex","project":"other-proj"}`)
	if code != 200 || !strings.Contains(body, "no Vertex AI API key given") {
		t.Errorf("other project: %d %s", code, body)
	}
	code, body = f.post(t, "/api/setup/keys", `{"voice":"openai","openai_key":"","orchestrator":"vertex","vertex":{"project":"other-proj"}}`)
	if code != 422 || !strings.Contains(body, "the saved Vertex key is for project good-proj; paste the key for project other-proj") {
		t.Errorf("save for another project: %d %s", code, body)
	}
	// Same project, orchestrator only: kept without a new check.
	f.sd.Config.Voice.OpenAI.APIKey = "sk-saved"
	code, body = f.post(t, "/api/setup/keys", `{"voice":"openai","orchestrator":"vertex","vertex":{"project":"good-proj"}}`)
	if code != 200 || !strings.Contains(body, "keeping the saved key") {
		t.Fatalf("keep: %d %s", code, body)
	}
	env, _ := onboard.ReadEnvFile(f.env)
	if _, ok := env["VERTEX_API_KEY"]; ok {
		t.Error("the kept key was written again")
	}
	if env["SHEEPDOG_BRAIN_MODEL"] != "gemini-3.5-flash" || env["SHEEPDOG_VOICE_PROVIDER"] != "openai" {
		t.Errorf("env %v", keysOf(env))
	}
	if _, ok := env["SHEEPDOG_GEMINI_BACKEND"]; ok {
		t.Error("GPT-Live changed the Gemini backend")
	}
	// A new location for the voice is checked with the saved key.
	code, body = f.post(t, "/api/setup/keys", `{"voice":"vertex","orchestrator":"vertex","vertex":{"project":"good-proj","location":"europe-west4"}}`)
	if code != 422 || !strings.Contains(body, "location europe-west4 does not serve") {
		t.Errorf("new location: %d %s", code, body)
	}
}
