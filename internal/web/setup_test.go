package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/pair"
)

const goodKey = "AIzaGoodKeyForTheSetupTest0000000000"

// fakeGemini answers the key check: goodKey is valid, anything else is
// Google's 400 API_KEY_INVALID.
func fakeGemini(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") == goodKey {
			_, _ = io.WriteString(w, `{"models":[{"name":"models/a"},{"name":"models/b"}]}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type setupFixture struct {
	h        *harness
	dir, env string
	restarts chan string
	demo     *fakeDemo
}

func newSetupFixture(t *testing.T, cfg config.Config, token string) *setupFixture {
	t.Helper()
	t.Setenv("SHEEPDOG_WEB_TOKEN", "")
	_ = os.Unsetenv("SHEEPDOG_WEB_TOKEN")
	dir := t.TempDir()
	f := &setupFixture{dir: dir, env: filepath.Join(dir, ".env"), restarts: make(chan string, 8), demo: &fakeDemo{}}
	sd := &SetupDeps{
		ConfigPath: filepath.Join(dir, "config.toml"), EnvPath: f.env, Config: cfg,
		Endpoints: onboard.Endpoints{GeminiBase: fakeGemini(t), HTTP: &http.Client{Timeout: 5 * time.Second}},
		Service:   nil, Demo: f.demo, Boot: time.Unix(1, 0),
		SysAddrs: func() ([]pair.IfaceAddr, error) {
			return []pair.IfaceAddr{{Iface: "en0", IP: net.ParseIP("192.168.1.20")}}, nil
		},
		TailscaleCLI: func() bool { return false },
	}
	f.h = newHarness(t, harnessOpts{setup: sd, token: token, restart: func(r string) { f.restarts <- r }})
	return f
}

func (f *setupFixture) post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(f.h.http.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (f *setupFixture) get(t *testing.T, path string) string {
	t.Helper()
	resp, err := http.Get(f.h.http.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", path, resp.Status)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func (f *setupFixture) waitRestart(t *testing.T) string {
	t.Helper()
	select {
	case r := <-f.restarts:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("no restart requested")
		return ""
	}
}

// TestSetupIsLoopbackOnly: the page and its API answer only a loopback
// client that names a loopback host, and a change only from this
// origin as JSON; even with a token configured, the token is not needed.
func TestSetupIsLoopbackOnly(t *testing.T) {
	f := newSetupFixture(t, config.Default(), "a-token-of-thirty-two-characters!")
	mux := f.h.srv.Handler()
	do := func(method, path, remote, host string, hdr map[string]string) *httptest.ResponseRecorder {
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"action":"ping"}`)
		}
		r := httptest.NewRequest(method, path, body)
		r.RemoteAddr, r.Host = remote, host
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	json := map[string]string{"Content-Type": "application/json"}
	cases := []struct {
		name, method, path, remote, host string
		hdr                              map[string]string
		want                             int
	}{
		{"page from loopback", "GET", "/setup", "127.0.0.1:5000", "127.0.0.1:7777", nil, 200},
		{"page from ::1 as localhost", "GET", "/setup/setup.js", "[::1]:5000", "localhost:7777", nil, 200},
		{"state from loopback, no token", "GET", "/api/setup/state", "127.0.0.1:5000", "127.0.0.1:7777", nil, 200},
		{"page from the LAN", "GET", "/setup", "192.168.1.50:5000", "192.168.1.20:7777", nil, 403},
		{"api from the LAN", "GET", "/api/setup/state", "100.101.1.2:5000", "100.101.1.1:7777", nil, 403},
		{"rebinding host name", "GET", "/api/setup/state", "127.0.0.1:5000", "evil.example:7777", nil, 403},
		{"post from another origin", "POST", "/api/setup/demo", "127.0.0.1:5000", "127.0.0.1:7777", map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}, 403},
		{"form post", "POST", "/api/setup/demo", "127.0.0.1:5000", "127.0.0.1:7777", map[string]string{"Content-Type": "text/plain"}, 403},
		{"post from this origin", "POST", "/api/setup/demo", "127.0.0.1:5000", "127.0.0.1:7777", map[string]string{"Content-Type": "application/json", "Origin": "http://127.0.0.1:7777"}, 200},
		{"post without origin", "POST", "/api/setup/demo", "127.0.0.1:5000", "127.0.0.1:7777", json, 200},
	}
	for _, c := range cases {
		w := do(c.method, c.path, c.remote, c.host, c.hdr)
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
		if c.want == 403 && !strings.Contains(w.Body.String(), "The setup page only opens on the computer sheepdog runs on") {
			t.Errorf("%s: body %q", c.name, w.Body.String())
		}
	}
	// The voice page's API still needs the token from the LAN.
	if w := do("GET", "/api/state", "192.168.1.50:5000", "192.168.1.20:7777", nil); w.Code != 401 {
		t.Errorf("/api/state from the LAN without a token: %d", w.Code)
	}
}

func TestSetupKeysValidateSaveAndNeverEcho(t *testing.T) {
	f := newSetupFixture(t, config.Default(), "")

	// A key that is needed and missing: nothing saved.
	code, body := f.post(t, "/api/setup/keys", `{}`)
	if code != 422 || !strings.Contains(body, "GEMINI_API_KEY is needed and not saved yet") || !strings.Contains(body, "aistudio.google.com/apikey") {
		t.Fatalf("missing key: %d %s", code, body)
	}
	// A rejected key: nothing saved, the plain reason.
	bad := "AIzaRejectedKeyForTheSetupTest000000"
	code, body = f.post(t, "/api/setup/keys", `{"gemini_key":"`+bad+`"}`)
	if code != 422 || !strings.Contains(body, "rejected the key") || strings.Contains(body, bad) {
		t.Fatalf("bad key: %d %s", code, body)
	}
	if _, err := os.Stat(f.env); !os.IsNotExist(err) {
		t.Fatalf("a rejected key wrote the env file: %v", err)
	}
	// Live validation alone.
	code, body = f.post(t, "/api/setup/validate", `{"kind":"gemini","key":"`+goodKey+`"}`)
	if code != 200 || !strings.Contains(body, `"status": "ok"`) || strings.Contains(body, goodKey) {
		t.Fatalf("validate: %d %s", code, body)
	}
	// A good key: saved 0600, the daemon asked to restart, never echoed.
	code, body = f.post(t, "/api/setup/keys", `{"gemini_key":"  `+goodKey+` "}`)
	if code != 200 || !strings.Contains(body, `"restarting": true`) || strings.Contains(body, goodKey) {
		t.Fatalf("good key: %d %s", code, body)
	}
	f.waitRestart(t)
	st, err := os.Stat(f.env)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("env file: %v %v", err, st)
	}
	env, _ := onboard.ReadEnvFile(f.env)
	if env["GEMINI_API_KEY"] != goodKey || env["SHEEPDOG_VOICE_PROVIDER"] != "gemini" || env["SHEEPDOG_BRAIN_PROVIDER"] != "gemini" {
		t.Errorf("env = %v", keysOf(env))
	}
	state := f.get(t, "/api/setup/state")
	var logged bytes.Buffer
	evs, _, _ := f.h.log.Since(0, 100000)
	for _, ev := range evs {
		b, _ := json.Marshal(ev)
		logged.Write(b)
	}
	if !strings.Contains(logged.String(), "web.setup_saved") || !strings.Contains(logged.String(), "GEMINI_API_KEY") {
		t.Fatalf("the event log does not have the save (so the check below proves nothing): %d bytes", logged.Len())
	}
	if strings.Contains(state, goodKey) || strings.Contains(logged.String(), goodKey) || strings.Contains(logged.String(), bad) {
		t.Error("a key reached the state or the event log")
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSetupKeysKeepsTheSavedKey: an empty field with a saved key keeps
// it (setup run twice), and going from DeepSeek back to Gemini drops
// DeepSeek's model.
func TestSetupKeysKeepsTheSavedKey(t *testing.T) {
	cfg := config.Default()
	cfg.Gemini.APIKeys = []string{goodKey}
	cfg.DeepSeek.APIKey = "sk-saved"
	f := newSetupFixture(t, cfg, "")
	if err := onboard.UpsertEnvFile(f.env, map[string]string{"GEMINI_API_KEY": goodKey, "SHEEPDOG_BRAIN_PROVIDER": "deepseek", "SHEEPDOG_BRAIN_MODEL": "deepseek-flash", "OTHER": "kept"}); err != nil {
		t.Fatal(err)
	}
	code, body := f.post(t, "/api/setup/keys", `{"gemini_key":"","orchestrator":"gemini"}`)
	if code != 200 || !strings.Contains(body, "keeping the saved key") {
		t.Fatalf("%d %s", code, body)
	}
	env, _ := onboard.ReadEnvFile(f.env)
	if env["GEMINI_API_KEY"] != goodKey || env["SHEEPDOG_BRAIN_PROVIDER"] != "gemini" || env["OTHER"] != "kept" {
		t.Errorf("env = %v", keysOf(env))
	}
	if _, ok := env["SHEEPDOG_BRAIN_MODEL"]; ok {
		t.Error("DeepSeek's model stayed behind")
	}
}

// TestSetupCompatAddsTheProvider: an OpenAI-compatible orchestrator
// (a loopback one needs no key) is added to a new config that loads.
func TestSetupCompatAddsTheProvider(t *testing.T) {
	cfg := config.Default()
	cfg.Gemini.APIKeys = []string{goodKey}
	f := newSetupFixture(t, cfg, "")
	compat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"local-model"}]}`)
	}))
	defer compat.Close()
	code, body := f.post(t, "/api/setup/keys", `{"orchestrator":"compat","compat":{"name":"Ollama","base_url":"`+compat.URL+`","model":"local-model"}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	loaded, err := config.Load(filepath.Join(f.dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := loaded.Brain.CompatProvider("ollama"); !ok || c.Model != "local-model" {
		t.Errorf("compat provider = %+v %v", c, ok)
	}
	env, _ := onboard.ReadEnvFile(f.env)
	if env["SHEEPDOG_BRAIN_PROVIDER"] != "ollama" || env["SHEEPDOG_BRAIN_MODEL"] != "local-model" {
		t.Errorf("env = %v", env)
	}
}

// TestSetupPhoneTokenRules: phone access binds every address and makes
// a token only when none exists; an existing token is never replaced
// unless "new token" is asked for; off goes back to loopback.
func TestSetupPhoneTokenRules(t *testing.T) {
	f := newSetupFixture(t, config.Default(), "")
	if code, body := f.post(t, "/api/setup/phone", `{"enable":true}`); code != 200 || !strings.Contains(body, `"token_created": true`) {
		t.Fatalf("enable: %d %s", code, body)
	}
	f.waitRestart(t)
	env, _ := onboard.ReadEnvFile(f.env)
	tok := env["SHEEPDOG_WEB_TOKEN"]
	if env["SHEEPDOG_WEB_BIND"] != "0.0.0.0" || len(tok) < onboard.MinTokenLen {
		t.Fatalf("bind %q token length %d", env["SHEEPDOG_WEB_BIND"], len(tok))
	}
	// Again (setup run twice): the token stays.
	if code, body := f.post(t, "/api/setup/phone", `{"enable":true}`); code != 200 || !strings.Contains(body, `"token_created": false`) {
		t.Fatalf("enable again: %d %s", code, body)
	}
	env, _ = onboard.ReadEnvFile(f.env)
	if env["SHEEPDOG_WEB_TOKEN"] != tok {
		t.Fatal("the token was replaced silently")
	}
	// Explicit new token.
	if code, body := f.post(t, "/api/setup/phone", `{"enable":true,"new_token":true}`); code != 200 || !strings.Contains(body, `"token_created": true`) {
		t.Fatalf("new token: %d %s", code, body)
	}
	env, _ = onboard.ReadEnvFile(f.env)
	if env["SHEEPDOG_WEB_TOKEN"] == tok {
		t.Fatal("new token did not replace it")
	}
	// Off: loopback, the token kept.
	if code, _ := f.post(t, "/api/setup/phone", `{"enable":false}`); code != 200 {
		t.Fatal("off")
	}
	env, _ = onboard.ReadEnvFile(f.env)
	if env["SHEEPDOG_WEB_BIND"] != "127.0.0.1" || env["SHEEPDOG_WEB_TOKEN"] == "" {
		t.Errorf("off: %v", keysOf(env))
	}

	// A token in the config counts: none is created beside it.
	cfg := config.Default()
	cfg.Web.Token = "a-config-token-of-thirty-two-chars"
	g := newSetupFixture(t, cfg, "")
	if code, body := g.post(t, "/api/setup/phone", `{"enable":true}`); code != 200 || !strings.Contains(body, `"token_created": false`) {
		t.Fatalf("config token: %d %s", code, body)
	}
}

// TestSetupPhoneConnected: an authorized request from another machine
// shows as the phone connecting.
func TestSetupPhoneConnected(t *testing.T) {
	cfg := config.Default()
	cfg.Web.Bind, cfg.Web.Token = "0.0.0.0", "a-token-of-thirty-two-characters!"
	f := newSetupFixture(t, cfg, cfg.Web.Token)
	mux := f.h.srv.Handler()
	r := httptest.NewRequest("GET", "/api/state", nil)
	r.RemoteAddr = "192.168.1.77:4000"
	r.Header.Set("Authorization", "Bearer "+cfg.Web.Token)
	mux.ServeHTTP(httptest.NewRecorder(), r)
	body := f.get(t, "/api/setup/state")
	var st SetupState
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Phone.Enabled || !st.Phone.Connected || st.Phone.Remote != "192.168.1.77" {
		t.Errorf("phone = %+v", st.Phone)
	}
	if strings.Contains(body, cfg.Web.Token) {
		t.Error("the token is in the state")
	}
}

// TestSetupDemoRoutes: start, ping and stop go to the demo control.
func TestSetupDemoRoutes(t *testing.T) {
	f := newSetupFixture(t, config.Default(), "")
	for _, a := range []string{"start", "ping", "stop"} {
		if code, body := f.post(t, "/api/setup/demo", `{"action":"`+a+`"}`); code != 200 {
			t.Fatalf("%s: %d %s", a, code, body)
		}
	}
	if got := strings.Join(f.demo.calls(), ","); got != "start,ping,stop" {
		t.Errorf("calls %s", got)
	}
	if code, _ := f.post(t, "/api/setup/demo", `{"action":"explode"}`); code != 400 {
		t.Errorf("bad action: %d", code)
	}
}

type fakeDemo struct {
	mu  sync.Mutex
	log []string
	on  bool
}

func (d *fakeDemo) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string{}, d.log...)
}
func (d *fakeDemo) Start(config.Config) (DemoState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log, d.on = append(d.log, "start"), true
	return DemoState{Running: true, URL: "http://127.0.0.1:7778"}, nil
}
func (d *fakeDemo) Stop(string) {
	d.mu.Lock()
	d.log, d.on = append(d.log, "stop"), false
	d.mu.Unlock()
}
func (d *fakeDemo) Ping() { d.mu.Lock(); d.log = append(d.log, "ping"); d.mu.Unlock() }
func (d *fakeDemo) State() DemoState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return DemoState{Running: d.on}
}
