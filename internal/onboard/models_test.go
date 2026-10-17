package onboard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serve(t *testing.T, status int, body string, check func(*http.Request)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func kindOf(err error) ModelsErrorKind {
	var me *ModelsError
	if errors.As(err, &me) {
		return me.Kind
	}
	return 0
}

func TestListModelsShapes(t *testing.T) {
	cases := []struct {
		name, body string
		ids        []string
		tools      []string // "yes", "no", "unknown" per model
	}{
		{"openai", `{"object":"list","data":[{"id":"gpt-a","object":"model"},{"id":"gpt-b"}]}`, []string{"gpt-a", "gpt-b"}, []string{"unknown", "unknown"}},
		{"openrouter", `{"data":[{"id":"x/a","supported_parameters":["tools","max_tokens"]},{"id":"x/b","supported_parameters":["max_tokens"]},{"id":"x/c"}]}`, []string{"x/a", "x/b", "x/c"}, []string{"yes", "no", "unknown"}},
		{"ollama", `{"object":"list","data":[{"id":"qwen3:8b","object":"model","owned_by":"library"}]}`, []string{"qwen3:8b"}, []string{"unknown"}},
		{"groq", `{"object":"list","data":[{"id":"openai/gpt-oss-120b","active":true,"context_window":131072}]}`, []string{"openai/gpt-oss-120b"}, []string{"unknown"}},
		{"duplicates and blanks", `{"data":[{"id":"a"},{"id":""},{"id":"a"}]}`, []string{"a"}, []string{"unknown"}},
	}
	for _, c := range cases {
		got, err := ListModels(context.Background(), nil, serve(t, 200, c.body, nil), "")
		if err != nil || len(got) != len(c.ids) {
			t.Fatalf("%s: %v %+v", c.name, err, got)
		}
		for i, m := range got {
			tools := "unknown"
			if m.Tools != nil && *m.Tools {
				tools = "yes"
			} else if m.Tools != nil {
				tools = "no"
			}
			if m.ID != c.ids[i] || tools != c.tools[i] {
				t.Errorf("%s[%d] = %+v (%s)", c.name, i, m, tools)
			}
		}
	}
}

func TestListModelsErrors(t *testing.T) {
	bg := context.Background()
	for _, c := range []struct {
		name   string
		status int
		body   string
		kind   ModelsErrorKind
	}{
		{"garbage", 200, "<html>nope</html>", ModelsNoList},
		{"wrong shape", 200, `{"models":["a"]}`, ModelsNoList},
		{"empty", 200, `{"data":[]}`, ModelsNoList},
		{"404", 404, "not found", ModelsNoList},
		{"401", 401, `{"error":{"message":"bad"}}`, ModelsAuth},
		{"403", 403, "", ModelsAuth},
	} {
		_, err := ListModels(bg, nil, serve(t, c.status, c.body, nil), "k")
		if kindOf(err) != c.kind {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, err := ListModels(bg, nil, "ftp://x", ""); kindOf(err) != ModelsUnreachable {
		t.Errorf("bad scheme: %v", err)
	}
	if _, err := ListModels(bg, nil, "http://127.0.0.1:1/v1", ""); kindOf(err) != ModelsUnreachable {
		t.Errorf("refused: %v", err)
	}
}

func TestListModelsTimeoutAndCaps(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	_, err := ListModels(context.Background(), &http.Client{Timeout: 100 * time.Millisecond}, slow.URL, "")
	if kindOf(err) != ModelsUnreachable || !strings.Contains(err.Error(), "no answer") {
		t.Errorf("timeout: %v", err)
	}
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := 0; i < 3000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"m` + strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676)) + `"}`)
	}
	b.WriteString(`]}`)
	got, err := ListModels(context.Background(), nil, serve(t, 200, b.String(), nil), "")
	if err != nil || len(got) != maxModelsCount {
		t.Errorf("count cap: %d %v", len(got), err)
	}
}

func TestListModelsKeyOnlyInTheHeader(t *testing.T) {
	var auth, rawURL string
	base := serve(t, 200, `{"data":[{"id":"a"}]}`, func(r *http.Request) { auth, rawURL = r.Header.Get("Authorization"), r.URL.String() })
	if _, err := ListModels(context.Background(), nil, base+"/v1", "sk-secret"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer sk-secret" || strings.Contains(rawURL, "sk-secret") || rawURL != "/v1/models" {
		t.Errorf("auth %q url %q", auth, rawURL)
	}
	_, err := ListModels(context.Background(), nil, "http://user:sk-secret@127.0.0.1:1/v1", "")
	if err == nil || strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("error leaks credentials: %v", err)
	}
}
