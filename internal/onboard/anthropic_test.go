package onboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

const (
	antKey    = "sk-ant-test-good-0123456789"
	antBadKey = "sk-ant-test-bad-0123456789"
)

// anthropicModelsBody is GET /v1/models as the API answers it.
const anthropicModelsBody = `{"data":[{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5","type":"model","max_input_tokens":1000000,"max_tokens":128000,"created_at":"2026-09-01T00:00:00Z"}],"has_more":false,"first_id":"claude-opus-5-5","last_id":"claude-opus-5-5"}`

// fakeAnthropic answers GET /v1/models and POST /v1/messages (as SSE),
// and records the credentials and the request bodies it saw.
type fakeAnthropic struct {
	srv *httptest.Server
	mu  sync.Mutex
	// keys and auths are the x-api-key and Authorization headers seen.
	keys, auths []string
	bodies      []map[string]any
	// answer is the messages answer: tool (a tool call), text or refusal.
	answer string
}

func newFakeAnthropic(t *testing.T) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{answer: "tool"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.keys = append(f.keys, r.Header.Get("x-api-key"))
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		answer := f.answer
		f.mu.Unlock()
		if r.Header.Get("x-api-key") != antKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, anthropicModelsBody)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.bodies = append(f.bodies, body)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, anthropicSSE(answer))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAnthropic) endpoints() Endpoints { return Endpoints{AnthropicBase: f.srv.URL} }

// checkHeaders asserts every request carried x-api-key = want and no
// Authorization header.
func (f *fakeAnthropic) checkHeaders(t *testing.T, want string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.keys) == 0 {
		t.Fatal("the fake saw no request")
	}
	for i := range f.keys {
		if f.keys[i] != want || f.auths[i] != "" {
			t.Errorf("request %d: x-api-key match %v, Authorization set %v", i, f.keys[i] == want, f.auths[i] != "")
		}
	}
}

// anthropicSSE is a streamed Messages API answer.
func anthropicSSE(answer string) string {
	ev := func(typ, data string) string { return "event: " + typ + "\ndata: " + data + "\n\n" }
	var b strings.Builder
	b.WriteString(ev("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"output_tokens":1}}}`))
	stop := "tool_use"
	switch answer {
	case "tool":
		b.WriteString(ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"`+probeTool+`","input":{}}}`))
		b.WriteString(ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`))
		b.WriteString(ev("content_block_stop", `{"type":"content_block_stop","index":0}`))
	case "text":
		stop = "end_turn"
		b.WriteString(ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
		b.WriteString(ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ready"}}`))
		b.WriteString(ev("content_block_stop", `{"type":"content_block_stop","index":0}`))
	case "refusal":
		stop = "refusal"
	}
	b.WriteString(ev("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":5}}`, stop)))
	b.WriteString(ev("message_stop", `{"type":"message_stop"}`))
	return b.String()
}

func TestCheckAnthropicKey(t *testing.T) {
	f := newFakeAnthropic(t)
	ctx := context.Background()
	if r := CheckAnthropicKey(ctx, f.endpoints(), antKey); r.Status != OK || !strings.Contains(r.Detail, "1 models") {
		t.Errorf("good key: %+v", r)
	}
	f.checkHeaders(t, antKey)
	r := CheckAnthropicKey(ctx, f.endpoints(), antBadKey)
	if r.Status != Fail || !strings.Contains(r.Detail, "Anthropic rejected the key") || !strings.Contains(r.Fix, "ANTHROPIC_API_KEY") || !strings.Contains(r.Fix, "console.anthropic.com") {
		t.Errorf("bad key: %+v", r)
	}
	if strings.Contains(r.Line(), antBadKey) || strings.Contains(strings.ToLower(r.Line()), "balance") {
		t.Errorf("the key or a balance claim in %q", r.Line())
	}
	if r := CheckAnthropicKey(ctx, f.endpoints(), ""); r.Status != Fail || !strings.Contains(r.Detail, "ANTHROPIC_API_KEY") {
		t.Errorf("no key: %+v", r)
	}
}

func TestListAnthropicModels(t *testing.T) {
	f := newFakeAnthropic(t)
	list, r := ListAnthropicModels(context.Background(), f.endpoints(), antKey)
	if r.Status != OK || len(list) != 1 || list[0].ID != brain.DefaultAnthropicModel || list[0].Tools == nil || !*list[0].Tools {
		t.Fatalf("list = %+v, %+v", list, r)
	}
	p, _ := PresetByID(Presets(), brain.ProviderAnthropic)
	if got := SuggestModel(p, list); got != "claude-opus-5-5" {
		t.Errorf("suggested %q", got)
	}
	f.checkHeaders(t, antKey)
}

func TestProbeAnthropic(t *testing.T) {
	f := newFakeAnthropic(t)
	ctx := context.Background()
	r := ProbeAnthropic(ctx, f.endpoints(), antKey, "claude-opus-5-5")
	if r.Status != OK || !strings.Contains(r.Detail, "calls tools") {
		t.Fatalf("tool call: %+v", r)
	}
	f.checkHeaders(t, antKey)
	f.mu.Lock()
	body := f.bodies[0]
	f.mu.Unlock()
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 || body["model"] != "claude-opus-5-5" || body["stream"] != true {
		t.Errorf("request: %d tools, model %v, stream %v", len(tools), body["model"], body["stream"])
	}
	if tc, ok := body["tool_choice"].(map[string]any); ok && tc["type"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", tc)
	}

	f.mu.Lock()
	f.answer = "text"
	f.mu.Unlock()
	if r := ProbeAnthropic(ctx, f.endpoints(), antKey, "claude-opus-5-5"); r.Status != Warn || !strings.Contains(r.Detail, "without calling") {
		t.Errorf("text answer: %+v", r)
	}
	f.mu.Lock()
	f.answer = "refusal"
	f.mu.Unlock()
	if r := ProbeAnthropic(ctx, f.endpoints(), antKey, "claude-opus-5-5"); r.Status != Warn || !strings.Contains(r.Detail, "declined") {
		t.Errorf("refusal: %+v", r)
	}
	r = ProbeAnthropic(ctx, f.endpoints(), antBadKey, "claude-opus-5-5")
	if r.Status != Fail || !strings.Contains(r.Detail, "rejected the key") || strings.Contains(r.Line(), antBadKey) {
		t.Errorf("bad key: %+v", r)
	}
	for _, c := range []struct{ key, model string }{{antKey, ""}, {antKey, "bad model;"}, {"", "claude-opus-5-5"}} {
		if r := ProbeAnthropic(ctx, f.endpoints(), c.key, c.model); r.Status != Fail {
			t.Errorf("key %v, model %q: %+v", c.key != "", c.model, r)
		}
	}
}

func TestDoctorAnthropic(t *testing.T) {
	f := newFakeAnthropic(t)
	cfg := config.Default()
	cfg.Anthropic.APIKey = antKey
	cfg.Anthropic.BaseURL = f.srv.URL
	cfg.Brain.Provider = brain.ProviderAnthropic
	cfg.Brain.Model = "claude-opus-5-5"
	rs := checkAnthropic(context.Background(), DoctorOptions{Probe: true}, cfg)
	if len(rs) != 2 || rs[0].Status != OK || !strings.Contains(rs[0].Detail, "the orchestrator") || rs[1].Status != OK {
		t.Fatalf("doctor anthropic = %+v", rs)
	}
	for _, r := range rs {
		if strings.Contains(r.Line(), antKey) {
			t.Error("the key leaked into the output")
		}
	}
	if r := checkOrchestrator(cfg); r.Status != OK {
		t.Errorf("orchestrator with key: %+v", r)
	}
	cfg.Brain.Provider = brain.ProviderGemini
	if rs := checkAnthropic(context.Background(), DoctorOptions{}, cfg); len(rs) != 1 || strings.Contains(rs[0].Detail, "orchestrator") {
		t.Errorf("not the orchestrator, no probe: %+v", rs)
	}
	cfg.Brain.Provider = brain.ProviderAnthropic
	cfg.Anthropic.APIKey = ""
	if r := checkOrchestrator(cfg); r.Status != Fail || !strings.Contains(r.Detail, "ANTHROPIC_API_KEY") {
		t.Errorf("orchestrator without key: %+v", r)
	}
	// A key in the config is checked by Doctor itself (no herdr, no
	// push relay, a free web port: nothing outside the test is touched).
	cfg.Anthropic.APIKey = antBadKey
	cfg.Herdr.Socket = filepath.Join(t.TempDir(), "none.sock")
	cfg.Push.Mode = config.PushModeOff
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	cfg.Web.Port = l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	found := false
	for _, r := range Doctor(context.Background(), cfg, DoctorOptions{}) {
		if r.Name == "anthropic key" {
			found = r.Status == Fail
		}
	}
	if !found {
		t.Error("Doctor did not fail the bad Anthropic key")
	}
}
