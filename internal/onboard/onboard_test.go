package onboard

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

const goodKey = "AIza-good-key-0123456789"

// fakeAPI answers the Gemini, OpenAI, DeepSeek and OpenRouter calls the
// checks make, keyed on the credentials, and records every URL.
type fakeAPI struct {
	srv  *httptest.Server
	mu   sync.Mutex
	urls []string
	// toolCall: the chat completion calls the probe tool.
	toolCall bool
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{toolCall: true}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.urls = append(f.urls, r.URL.String())
		toolCall := f.toolCall
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		auth := r.Header.Get("Authorization")
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1beta/models"):
			switch r.Header.Get("x-goog-api-key") {
			case goodKey:
				_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-3.5-flash"},{"name":"models/gemini-3.8-live"}]}`)
			case "quota-key":
				w.WriteHeader(429)
				_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Quota exceeded for metric generate_content_free_tier_requests","status":"RESOURCE_EXHAUSTED"}}`)
			default:
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`)
			}
		case auth == "Bearer broke-key":
			w.WriteHeader(402)
			_, _ = io.WriteString(w, `{"error":{"message":"Insufficient Balance"}}`)
		case auth != "Bearer good" && auth != "":
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":{"message":"Authentication Fails"}}`)
		case strings.HasSuffix(r.URL.Path, "/models"):
			_, _ = io.WriteString(w, `{"data":[{"id":"m1"},{"id":"m2"},{"id":"m3"}]}`)
		case strings.HasSuffix(r.URL.Path, "/user/balance"):
			_, _ = io.WriteString(w, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"42.50"}]}`)
		case strings.HasSuffix(r.URL.Path, "/key"):
			_, _ = io.WriteString(w, `{"data":{"usage":1.25,"limit":5}}`)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			msg := map[string]any{"role": "assistant", "content": "ready"}
			if toolCall {
				msg = map[string]any{"role": "assistant", "content": "", "tool_calls": []map[string]any{{"id": "c1", "type": "function", "function": map[string]any{"name": probeTool, "arguments": "{}"}}}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": msg}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) endpoints() Endpoints {
	return Endpoints{GeminiBase: f.srv.URL, OpenAIBase: f.srv.URL + "/oai/v1"}
}

func TestCheckGeminiKey(t *testing.T) {
	f := newFakeAPI(t)
	ctx := context.Background()
	if r := CheckGeminiKey(ctx, f.endpoints(), goodKey); r.Status != OK || !strings.Contains(r.Detail, "2 models") {
		t.Errorf("good: %+v", r)
	}
	r := CheckGeminiKey(ctx, f.endpoints(), "bad-key")
	if r.Status != Fail || !strings.Contains(r.Detail, "rejected the key") || !strings.Contains(r.Fix, "GEMINI_API_KEY") {
		t.Errorf("bad: %+v", r)
	}
	if r := CheckGeminiKey(ctx, f.endpoints(), "quota-key"); r.Status != Fail || !strings.Contains(r.Detail, "quota is used up") {
		t.Errorf("quota: %+v", r)
	}
	if r := CheckGeminiKey(ctx, f.endpoints(), ""); r.Status != Fail || !strings.Contains(r.Fix, "aistudio") {
		t.Errorf("empty: %+v", r)
	}
	for _, u := range f.urls {
		if strings.Contains(u, "key") && strings.Contains(u, "AIza") {
			t.Errorf("key in URL: %s", u)
		}
	}
}

func TestCheckCompat(t *testing.T) {
	f := newFakeAPI(t)
	ctx := context.Background()
	off := false
	rs := CheckCompat(ctx, f.endpoints(), Compat{Name: brain.ProviderDeepSeek, BaseURL: f.srv.URL, APIKey: "good", KeyEnv: "DEEPSEEK_API_KEY", Model: "deepseek-flash", Thinking: &off}, true)
	if len(rs) != 3 || rs[0].Status != OK || rs[1].Detail != "42.50 CNY left" || rs[2].Status != OK {
		t.Errorf("deepseek: %+v", rs)
	}
	rs = CheckCompat(ctx, f.endpoints(), Compat{Name: "openrouter", BaseURL: f.srv.URL + "/openrouter.ai/api/v1", APIKey: "good", KeyEnv: "OPENROUTER_API_KEY", Model: "x/y"}, false)
	if len(rs) != 2 || rs[1].Detail != "$1.25 of $5.00 used" {
		t.Errorf("openrouter: %+v", rs)
	}
	rs = CheckCompat(ctx, f.endpoints(), Compat{Name: "groq", BaseURL: f.srv.URL + "/v1", APIKey: "nope", KeyEnv: "GROQ_API_KEY", Model: "m"}, true)
	if len(rs) != 1 || rs[0].Status != Fail || !strings.Contains(rs[0].Fix, "GROQ_API_KEY") {
		t.Errorf("invalid: %+v", rs)
	}
	rs = CheckCompat(ctx, f.endpoints(), Compat{Name: brain.ProviderDeepSeek, BaseURL: f.srv.URL, APIKey: "broke-key", KeyEnv: "DEEPSEEK_API_KEY"}, false)
	if rs[0].Status != Fail || !strings.Contains(rs[0].Detail, "balance is used up") || !strings.Contains(rs[0].Fix, "top up") {
		t.Errorf("402: %+v", rs)
	}
	if rs := CheckCompat(ctx, f.endpoints(), Compat{Name: "x", BaseURL: f.srv.URL, KeyEnv: "X_API_KEY"}, false); rs[0].Status != Fail || !strings.Contains(rs[0].Detail, "X_API_KEY is not set") {
		t.Errorf("no key: %+v", rs)
	}
	f.mu.Lock()
	f.toolCall = false
	f.mu.Unlock()
	rs = CheckCompat(ctx, f.endpoints(), Compat{Name: "ollama", BaseURL: f.srv.URL + "/v1", NoKey: true, Model: "small"}, true)
	if len(rs) != 2 || !strings.Contains(rs[0].Detail, "local server") || rs[1].Status != Warn {
		t.Errorf("no tool call: %+v", rs)
	}
}

func TestCheckOpenAIVoiceKey(t *testing.T) {
	f := newFakeAPI(t)
	ctx := context.Background()
	if r := CheckOpenAIVoiceKey(ctx, f.endpoints(), ""); r.Status != Skip {
		t.Errorf("unset: %+v", r)
	}
	if r := CheckOpenAIVoiceKey(ctx, f.endpoints(), "good"); r.Status != OK {
		t.Errorf("good: %+v", r)
	}
	if r := CheckOpenAIVoiceKey(ctx, f.endpoints(), "bad"); r.Status != Fail || !strings.Contains(r.Fix, "OPENAI_API_KEY") {
		t.Errorf("bad: %+v", r)
	}
}

func TestCheckPort(t *testing.T) {
	ctx := context.Background()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	if r := CheckPort(ctx, nil, "127.0.0.1", port); r.Status != OK || !strings.Contains(r.Detail, "free") {
		t.Errorf("free: %+v", r)
	}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"name":"sheepdog","version":"v1.2.3"}`)
	}))
	defer daemon.Close()
	dport, _ := strconv.Atoi(daemon.URL[strings.LastIndex(daemon.URL, ":")+1:])
	if r := CheckPort(ctx, nil, "0.0.0.0", dport); r.Status != OK || !strings.Contains(r.Detail, "daemon is up") {
		t.Errorf("daemon: %+v", r)
	}
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	oport, _ := strconv.Atoi(other.URL[strings.LastIndex(other.URL, ":")+1:])
	if r := CheckPort(ctx, nil, "127.0.0.1", oport); r.Status != Fail {
		t.Errorf("other: %+v", r)
	}
}

func TestCheckBindTokenPushVoice(t *testing.T) {
	if r := CheckBindToken(config.Web{Bind: "127.0.0.1"}); r.Status != OK {
		t.Errorf("loopback: %+v", r)
	}
	if r := CheckBindToken(config.Web{Bind: "0.0.0.0"}); r.Status != Fail {
		t.Errorf("no token: %+v", r)
	}
	if r := CheckBindToken(config.Web{Bind: "0.0.0.0", Token: "short"}); r.Status != Warn {
		t.Errorf("short: %+v", r)
	}
	if r := CheckBindToken(config.Web{Bind: "100.64.0.2", Token: strings.Repeat("x", 43)}); r.Status != OK || strings.Contains(r.Detail, "xxx") {
		t.Errorf("long: %+v", r)
	}

	ctx := context.Background()
	if r := CheckPush(ctx, Endpoints{}, config.Push{Mode: config.PushModeOff}); r.Status != Skip {
		t.Errorf("off: %+v", r)
	}
	if r := CheckPush(ctx, Endpoints{}, config.Push{APNs: config.APNs{KeyID: "K"}, Relay: config.PushRelay{URL: "https://relay.example"}}); r.Status != Fail {
		t.Errorf("partial: %+v", r)
	}
	if r := CheckPush(ctx, Endpoints{}, config.Push{Mode: config.PushModeAPNs}); r.Status != Fail {
		t.Errorf("apns, empty: %+v", r)
	}
	// The shared relay's URL before its first deploy is a placeholder.
	if r := CheckPush(ctx, Endpoints{}, config.Push{Relay: config.PushRelay{URL: "https://sheepdog-relay.REPLACE.workers.dev"}}); r.Status != Warn || !strings.Contains(r.Detail, "not deployed") {
		t.Errorf("placeholder relay: %+v", r)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"version":"1.0.0"}`))
	}))
	defer relay.Close()
	if r := CheckPush(ctx, Endpoints{HTTP: relay.Client()}, config.Push{Relay: config.PushRelay{URL: relay.URL}}); r.Status != OK || !strings.Contains(r.Detail, "end-to-end") {
		t.Errorf("relay up: %+v", r)
	}
	if r := CheckPush(ctx, Endpoints{HTTP: relay.Client()}, config.Push{Relay: config.PushRelay{URL: relay.URL + "/nope"}}); r.Status != Fail {
		t.Errorf("relay wrong: %+v", r)
	}
	p8 := filepath.Join(t.TempDir(), "AuthKey.p8")
	if err := os.WriteFile(p8, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A complete [push.apns] with the mode unset is the apns mode.
	a := config.Push{APNs: config.APNs{KeyPath: p8, KeyID: "K", TeamID: "T", Topic: "com.example.app"}}
	if r := CheckPush(ctx, Endpoints{}, a); r.Status != Warn {
		t.Errorf("readable: %+v", r)
	}
	_ = os.Chmod(p8, 0o600)
	if r := CheckPush(ctx, Endpoints{}, a); r.Status != OK || !strings.Contains(r.Detail, "apns") {
		t.Errorf("ok: %+v", r)
	}

	cfg := config.Default()
	if r := CheckVoice(cfg); r.Status != Fail {
		t.Errorf("no key: %+v", r)
	}
	cfg.Gemini.APIKeys = []string{"k"}
	if r := CheckVoice(cfg); r.Status != OK {
		t.Errorf("gemini: %+v", r)
	}
	cfg.Voice.Provider = "openai"
	if r := CheckVoice(cfg); r.Status != Fail || !strings.Contains(r.Fix, "OPENAI_API_KEY") {
		t.Errorf("openai: %+v", r)
	}
}

func TestEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", ".env")
	if err := UpsertEnvFile(path, map[string]string{"B": "2", "A": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# keep me\nA=1\nOTHER=\"x y\"\n\nB=2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UpsertEnvFile(path, map[string]string{"B": "3", "C": "4"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if got := string(data); got != "# keep me\nA=1\nOTHER=\"x y\"\n\nB=3\nC=4\n" {
		t.Errorf("file = %q", got)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o", st.Mode().Perm())
	}
	m, err := ReadEnvFile(path)
	if err != nil || m["OTHER"] != "x y" || m["B"] != "3" {
		t.Errorf("read = %v %v", m, err)
	}
	if err := UpsertEnvFile(path, map[string]string{"D": "a\nb"}); err == nil {
		t.Error("a value with a newline was written")
	}
	if m, err := ReadEnvFile(filepath.Join(t.TempDir(), "none")); err != nil || len(m) != 0 {
		t.Errorf("missing = %v %v", m, err)
	}
	tok, err := NewToken()
	if err != nil || len(tok) != 43 {
		t.Errorf("token len %d err %v", len(tok), err)
	}
}

// Doctor runs every check and never prints a key.
func TestDoctor(t *testing.T) {
	f := newFakeAPI(t)
	cfg := config.Default()
	cfg.Herdr.Socket = filepath.Join(t.TempDir(), "none.sock")
	cfg.Gemini.APIKeys = []string{goodKey}
	cfg.Brain.OpenAICompat = []config.OpenAICompat{{Name: "openrouter", BaseURL: f.srv.URL + "/v1", APIKeyEnv: "OPENROUTER_API_KEY", Model: "x/y", APIKey: "good"}, {Name: "groq", BaseURL: "https://api.groq.com/openai/v1", APIKeyEnv: "GROQ_API_KEY", Model: "m"}}
	cfg.Brain.Provider = "openrouter"
	cfg.Push.Mode = config.PushModeOff // the relay check has its own test; no network here
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	cfg.Web.Port = l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	rs := Doctor(context.Background(), cfg, DoctorOptions{Endpoints: f.endpoints(), Probe: true})
	byName := map[string]Result{}
	var all strings.Builder
	for _, r := range rs {
		byName[r.Name] = r
		all.WriteString(r.Line() + "\n")
	}
	for name, want := range map[string]Status{"herdr": Fail, "gemini key": OK, "openrouter key": OK, "openrouter tools": OK, "groq key": Skip, "orchestrator": OK, "voice": OK, "web port": OK, "bind/token": OK, "push": Skip, "openai voice key": Skip} {
		if byName[name].Status != want {
			t.Errorf("%s = %+v, want %v\n%s", name, byName[name], want, all.String())
		}
	}
	if strings.Contains(all.String(), goodKey) || strings.Contains(all.String(), "Bearer good") {
		t.Error("a key leaked into the output")
	}
	if !Failed(rs) {
		t.Error("Failed = false with herdr down")
	}
	cfg.Brain.Provider = "groq"
	if r := checkOrchestrator(cfg); r.Status != Fail || !strings.Contains(r.Detail, "GROQ_API_KEY") {
		t.Errorf("orchestrator without key: %+v", r)
	}
}

func TestAccountProblemOf(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
		want   brain.AccountProblem
	}{
		{401, "Authentication Fails", brain.ProblemInvalidKey},
		{403, "PERMISSION_DENIED", brain.ProblemInvalidKey},
		{400, "API key not valid. Please pass a valid API key.", brain.ProblemInvalidKey},
		{400, "bad tool schema", brain.ProblemNone},
		{402, "Insufficient Balance", brain.ProblemBalance},
		{429, "Quota exceeded for metric", brain.ProblemQuota},
		{429, "Rate limit reached, slow down", brain.ProblemNone},
		{503, "overloaded", brain.ProblemNone},
	} {
		if got := brain.AccountProblemOf(tc.status, tc.msg); got != tc.want {
			t.Errorf("%d %q = %q, want %q", tc.status, tc.msg, got, tc.want)
		}
	}
}

// doctor checks the Gemini key only when a configured provider uses it: not
// for an OpenAI voice with a non-Gemini (or disabled) orchestrator, even
// with no key or a stale one in the env file.
func TestGeminiKeyOnlyWhenNeeded(t *testing.T) {
	f := newFakeAPI(t)
	cfg := config.Default()
	cfg.Voice.Provider = "openai"
	cfg.Brain.Provider = "deepseek"
	for _, keys := range [][]string{nil, {"stale-key"}} {
		cfg.Gemini.APIKeys = keys
		rs := checkGeminiKeys(context.Background(), f.endpoints(), cfg)
		if len(rs) != 1 || rs[0].Status != Skip {
			t.Errorf("openai voice + deepseek, keys %v: %+v, want one Skip", keys, rs)
		}
	}
	cfg.Gemini.APIKeys = nil
	cfg.Brain.Provider = "gemini"
	cfg.Brain.Enabled = false
	if rs := checkGeminiKeys(context.Background(), f.endpoints(), cfg); rs[0].Status != Skip {
		t.Errorf("openai voice + disabled gemini orchestrator: %+v, want Skip", rs)
	}
	cfg.Brain.Enabled = true
	if rs := checkGeminiKeys(context.Background(), f.endpoints(), cfg); rs[0].Status != Fail {
		t.Errorf("openai voice + enabled gemini orchestrator, no key: %+v, want Fail", rs)
	}
	cfg.Voice.Provider = "gemini"
	cfg.Brain.Provider = "deepseek"
	if rs := checkGeminiKeys(context.Background(), f.endpoints(), cfg); rs[0].Status != Fail {
		t.Errorf("gemini voice, no key: %+v, want Fail", rs)
	}
}
