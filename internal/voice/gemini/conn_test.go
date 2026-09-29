package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// liveHandshake is what a fake Live endpoint saw from one dial.
type liveHandshake struct {
	path, apiKey, auth, query string
	setup                     map[string]any
}

// fakeLiveServer accepts one Live socket, records the handshake and the
// setup message, and answers setupComplete. No network beyond loopback.
func fakeLiveServer(t *testing.T) (*httptest.Server, <-chan liveHandshake) {
	t.Helper()
	got := make(chan liveHandshake, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_, msg, err := c.Read(ctx)
		if err != nil {
			t.Errorf("read setup: %v", err)
			return
		}
		var body struct {
			Setup map[string]any `json:"setup"`
		}
		if err := json.Unmarshal(msg, &body); err != nil {
			t.Errorf("setup json: %v", err)
		}
		got <- liveHandshake{path: r.URL.Path, apiKey: r.Header.Get("X-Goog-Api-Key"), auth: r.Header.Get("Authorization"), query: r.URL.RawQuery, setup: body.Setup}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"setupComplete":{"sessionId":"s1"}}`))
		_, _, _ = c.Read(ctx) // until the client closes
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func dialFake(t *testing.T, d *sdkDialer, srv *httptest.Server) {
	t.Helper()
	d.baseURL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := d.Dial(ctx, "test-key-1234", "gemini-3.8-live", liveConfig(Config{Voice: "Puck"}.withDefaults(), "prompt", ""))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
}

// TestSDKDialerVertex: backend vertex dials the aiplatform LlmBidiService
// socket with the key in x-goog-api-key (what the spike found works) and
// names the model by its full Vertex resource path in the setup.
func TestSDKDialerVertex(t *testing.T) {
	srv, got := fakeLiveServer(t)
	dialFake(t, newSDKDialer(&Vertex{Project: "proj-1", Location: "us-central1"}), srv)
	h := <-got
	if h.path != "/ws/google.cloud.aiplatform.v1beta1.LlmBidiService/BidiGenerateContent" {
		t.Errorf("path = %q", h.path)
	}
	if h.apiKey != "test-key-1234" || h.auth != "" || h.query != "" {
		t.Errorf("auth: x-goog-api-key=%q authorization=%q query=%q", h.apiKey, h.auth, h.query)
	}
	if m := h.setup["model"]; m != "projects/proj-1/locations/us-central1/publishers/google/models/gemini-3.8-live" {
		t.Errorf("setup.model = %v", m)
	}
	// The rest of the setup is the same session config as on AI Studio.
	for _, k := range []string{"sessionResumption", "contextWindowCompression", "tools", "outputAudioTranscription"} {
		if _, ok := h.setup[k]; !ok {
			t.Errorf("setup lacks %s: %v", k, h.setup)
		}
	}
}

// TestSDKDialerAIStudio: the default backend still dials the
// generativelanguage socket with a bare models/ name.
func TestSDKDialerAIStudio(t *testing.T) {
	srv, got := fakeLiveServer(t)
	dialFake(t, newSDKDialer(nil), srv)
	h := <-got
	if !strings.Contains(h.path, "google.ai.generativelanguage.") || h.apiKey != "test-key-1234" {
		t.Errorf("path = %q key=%q", h.path, h.apiKey)
	}
	if m := h.setup["model"]; m != "models/gemini-3.8-live" {
		t.Errorf("setup.model = %v", m)
	}
}

func TestVertexBaseURL(t *testing.T) {
	for loc, want := range map[string]string{
		"us-central1": "https://us-central1-aiplatform.googleapis.com/",
		"global":      "https://aiplatform.googleapis.com/",
		"eu":          "https://aiplatform.eu.rep.googleapis.com/",
	} {
		if got := vertexBaseURL(loc); got != want {
			t.Errorf("vertexBaseURL(%q) = %q, want %q", loc, got, want)
		}
	}
}

// TestNewVertexNeedsProject: a Vertex session without a project fails at
// New instead of at the first dial.
func TestNewVertexNeedsProject(t *testing.T) {
	if _, err := New(Config{APIKeys: []string{"k"}, Vertex: &Vertex{Location: "us-central1"}}, nil); err != ErrVertexConfig {
		t.Errorf("New = %v, want ErrVertexConfig", err)
	}
}

// TestVertexRateLimitBacksOffOnOneKey: Vertex has one key, so a 429
// mid-session logs voice.rate_limited, waits, and reconnects on the same
// key with its resumption handle: no rotation, no hot retry.
func TestVertexRateLimitBacksOffOnOneKey(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{APIKeys: []string{"vertex-key"}, Vertex: &Vertex{Project: "p", Location: "us-central1"}}, d)
	if p := h.log.payloads("voice.connect"); len(p) != 1 || p[0]["backend"] != "vertex" {
		t.Fatalf("voice.connect = %v", p)
	}

	c1.serve(handleMsg("handle-V"))
	time.Sleep(10 * time.Millisecond)
	t0 := time.Now()
	c1.fail(errors.New(`websocket: close 1008 (policy violation): RESOURCE_EXHAUSTED retryDelay: "0.2s"`))

	h.waitEvent("reconnected", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if el := time.Since(t0); el < 190*time.Millisecond {
		t.Errorf("reconnected after %v, want >= retryDelay 200ms", el)
	}
	if rl := h.log.payloads("voice.rate_limited"); len(rl) != 1 {
		t.Fatalf("rate_limited = %v", rl)
	}
	if h.log.has("voice.key_rotated") {
		t.Error("voice.key_rotated logged with a single Vertex key")
	}
	recs := d.records()
	if len(recs) != 2 || recs[1].key != "vertex-key" || recs[1].handle != "handle-V" {
		t.Fatalf("dials = %+v, want the same key resuming handle-V", recs)
	}
}
