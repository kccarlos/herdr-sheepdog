package onboard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

const vertexTestKey = "AQ.VertexTestKeyNeverEchoed0000000000"

// fakeVertex answers countTokens with status and body, and records the
// path and the key header it got.
func fakeVertex(t *testing.T, status int, body string) (*httptest.Server, *http.Request) {
	t.Helper()
	got := &http.Request{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = *r.Clone(context.Background())
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestCheckVertexErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   Status
		detail string
		fix    string
	}{
		{"ok", 200, `{"totalTokens":1}`, OK, "accepted the key for project p-1", ""},
		{"bad key", 400, `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT","details":[{"reason":"API_KEY_INVALID"}]}}`, Fail, "rejected the key", "bound to a service account"},
		{"unbound key", 401, `{"error":{"code":401,"message":"API keys are not supported by this API. Expected OAuth2 access token.","status":"UNAUTHENTICATED"}}`, Fail, "rejected the key", "bound to a service account"},
		{"api disabled", 403, `{"error":{"code":403,"message":"Vertex AI API has not been used in project p-1 before or it is disabled.","status":"PERMISSION_DENIED","details":[{"reason":"SERVICE_DISABLED"}]}}`, Fail, "not enabled in project p-1", "aiplatform.googleapis.com?project=p-1"},
		{"no project", 403, `{"error":{"code":403,"message":"Permission denied on resource project p-1.","status":"PERMISSION_DENIED","details":[{"reason":"CONSUMER_INVALID"}]}}`, Fail, "project p-1 was not found, or this key cannot use it", "project id"},
		{"not found", 404, `{"error":{"code":404,"message":"Publisher Model was not found or your project does not have access to it.","status":"NOT_FOUND"}}`, Fail, "was not found, or this key cannot use it", ""},
		{"blocked key", 403, `{"error":{"code":403,"message":"Requests to this API aiplatform.googleapis.com method countTokens are blocked.","status":"PERMISSION_DENIED","details":[{"reason":"API_KEY_SERVICE_BLOCKED"}]}}`, Fail, "not allowed to call Vertex AI", "aiplatform.googleapis.com"},
		{"trust and safety", 403, `{"error":{"code":403,"message":"Request rejected in accordance with our terms of use.","status":"PERMISSION_DENIED"}}`, Fail, "under its terms of use", "Google Cloud support"},
		{"iam", 403, `{"error":{"code":403,"message":"Permission 'aiplatform.endpoints.predict' denied","status":"PERMISSION_DENIED","details":[{"reason":"IAM_PERMISSION_DENIED"}]}}`, Fail, "may not use Vertex AI in project p-1", "Vertex AI User role"},
		{"rate limited", 429, `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`, Warn, "rate limiting", ""},
		{"echoing server", 500, `{"error":{"message":"internal error for key ` + vertexTestKey + `"}}`, Fail, "HTTP 500", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, got := fakeVertex(t, c.status, c.body)
			r := CheckVertex(context.Background(), Endpoints{VertexBase: srv.URL}, VertexCreds{APIKey: " " + vertexTestKey + " ", Project: "p-1"}, "")
			if r.Status != c.want || !strings.Contains(r.Detail, c.detail) || !strings.Contains(r.Fix, c.fix) {
				t.Errorf("got %v %q / %q", r.Status, r.Detail, r.Fix)
			}
			if strings.Contains(r.Detail+r.Fix, vertexTestKey) {
				t.Error("the key is in the result")
			}
			if got.Header.Get("x-goog-api-key") != vertexTestKey || strings.Contains(got.URL.String(), vertexTestKey) {
				t.Errorf("key header set %v, url %s", got.Header.Get("x-goog-api-key") != "", got.URL.Path)
			}
			if want := "/v1/projects/p-1/locations/global/publishers/google/models/gemini-3.5-flash:countTokens"; got.URL.Path != want || got.Method != http.MethodPost {
				t.Errorf("%s %s, want POST %s", got.Method, got.URL.Path, want)
			}
		})
	}
}

func TestCheckVertexMissingFields(t *testing.T) {
	ep := Endpoints{VertexBase: "http://127.0.0.1:9"}
	if r := CheckVertex(context.Background(), ep, VertexCreds{Project: "p"}, ""); r.Status != Fail || !strings.Contains(r.Detail, "no Vertex AI API key") {
		t.Errorf("no key: %+v", r)
	}
	if r := CheckVertex(context.Background(), ep, VertexCreds{APIKey: vertexTestKey}, ""); r.Status != Fail || !strings.Contains(r.Detail, "project id") {
		t.Errorf("no project: %+v", r)
	}
	if r := CheckVertex(context.Background(), ep, VertexCreds{APIKey: vertexTestKey, Project: "p"}, "gemini-3.8-live"); r.Status != Fail || !strings.Contains(r.Fix, "us-central1") {
		t.Errorf("no location: %+v", r)
	}
	r := CheckVertex(context.Background(), ep, VertexCreds{APIKey: vertexTestKey, Project: "p"}, "")
	if r.Status != Fail || !strings.Contains(r.Detail, "unreachable") || strings.Contains(r.Detail, vertexTestKey) {
		t.Errorf("unreachable: %+v", r)
	}
}

func TestCheckVertexLocation(t *testing.T) {
	srv, _ := fakeVertex(t, 200, `{"totalTokens":1}`)
	var gotLoc, gotModel, gotKey string
	live := func(err error) func(context.Context, string, string, string, string) error {
		return func(_ context.Context, key, project, location, model string) error {
			gotKey, gotLoc, gotModel = key, location, model
			return err
		}
	}
	v := VertexCreds{APIKey: vertexTestKey, Project: "p-1", Location: "us-central1"}
	r := CheckVertex(context.Background(), Endpoints{VertexBase: srv.URL, VertexLive: live(nil)}, v, "gemini-3.8-live")
	if r.Status != OK || !strings.Contains(r.Detail, "us-central1 serves gemini-3.8-live") || gotLoc != "us-central1" || gotModel != "gemini-3.8-live" || gotKey != vertexTestKey {
		t.Errorf("ok: %+v (%s %s)", r, gotLoc, gotModel)
	}
	v.Location = "global"
	notFound := errors.New("websocket: close 1008 (policy violation): Publisher model `projects/p-1/locations/global/publishers/google/models/gemini-3.8-live` was not found")
	r = CheckVertex(context.Background(), Endpoints{VertexBase: srv.URL, VertexLive: live(notFound)}, v, "gemini-3.8-live")
	if r.Status != Fail || r.Detail != "location global does not serve gemini-3.8-live" || !strings.Contains(r.Fix, "us-central1") {
		t.Errorf("not served: %+v", r)
	}
	// The close reason as Vertex sends it, cut at 123 bytes (seen live).
	cut := errors.New("failed to receive setup complete: websocket: close 1008 (policy violation): Publisher model `projects/p-1/locations/global/publishers/google/models/gemini-3.8-live` was not")
	if r = CheckVertex(context.Background(), Endpoints{VertexBase: srv.URL, VertexLive: live(cut)}, v, "gemini-3.8-live"); r.Detail != "location global does not serve gemini-3.8-live" {
		t.Errorf("cut reason: %+v", r)
	}
	r = CheckVertex(context.Background(), Endpoints{VertexBase: srv.URL, VertexLive: live(errors.New("close 1008: Request rejected in accordance with our terms of use"))}, v, "gemini-3.8-live")
	if r.Status != Fail || !strings.Contains(r.Fix, "Google Cloud support") {
		t.Errorf("t&s on live: %+v", r)
	}
	// The orchestrator alone: no Live check.
	gotLoc = ""
	r = CheckVertex(context.Background(), Endpoints{VertexBase: srv.URL, VertexLive: live(notFound)}, v, "")
	if r.Status != OK || gotLoc != "" {
		t.Errorf("orchestrator only: %+v, live called at %q", r, gotLoc)
	}
}

// clearVertexEnv keeps the developer's shell from reaching config.Load
// (an empty value does not override a file value).
func clearVertexEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"VERTEX_API_KEY", "VERTEX_PROJECT", "VERTEX_LOCATION", "SHEEPDOG_BRAIN_PROVIDER", "SHEEPDOG_GEMINI_BACKEND"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

func TestSetVertexConfig(t *testing.T) {
	clearVertexEnv(t)
	dir := t.TempDir()
	load := func(p string) config.GeminiVertex {
		t.Helper()
		c, err := config.Load(p)
		if err != nil {
			t.Fatal(err)
		}
		return c.Gemini.Vertex
	}
	// No config yet: the template, with the values set in [gemini.vertex].
	p := filepath.Join(dir, "new.toml")
	if err := SetVertexConfig(p, "p-1", "us"); err != nil {
		t.Fatal(err)
	}
	if v := load(p); v.Project != "p-1" || v.Location != "us" {
		t.Errorf("new: %s %s", v.Project, v.Location)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	// Again: replaced in place, no second table; no location keeps it.
	if err := SetVertexConfig(p, "p-2", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if v := load(p); v.Project != "p-2" || v.Location != "us" || strings.Count(string(b), "\n[gemini.vertex]\n") != 1 || strings.Count(string(b), "\nproject = ") != 1 {
		t.Errorf("replace: %s %s, %d tables", v.Project, v.Location, strings.Count(string(b), "\n[gemini.vertex]\n"))
	}
	// A config without the table: appended; other settings kept.
	p = filepath.Join(dir, "short.toml")
	if err := os.WriteFile(p, []byte("[brain]\nprovider = \"vertex\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetVertexConfig(p, "p-3", "us-central1"); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(p)
	if err != nil || c.Gemini.Vertex.Project != "p-3" || c.Brain.Provider != "vertex" {
		t.Errorf("append: %v %s %s", err, c.Gemini.Vertex.Project, c.Brain.Provider)
	}
	// A config that does not load afterwards is restored.
	p = filepath.Join(dir, "dotted.toml")
	orig := "[gemini]\nvertex.project = \"x\"\n"
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetVertexConfig(p, "p-4", ""); err == nil {
		t.Error("a duplicate table was accepted")
	}
	if b, _ := os.ReadFile(p); string(b) != orig {
		t.Errorf("not restored: %s", b)
	}
}
