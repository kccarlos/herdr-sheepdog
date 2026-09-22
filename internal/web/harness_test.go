package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// fakeSession is an in-memory voice.Session.
type fakeSession struct {
	mu     sync.Mutex
	audio  chan []byte
	events chan voice.Event
	sent   [][]byte
	texts  []string
	pushes []string
	muted  bool
	status voice.Status
	ends   int // AudioStreamEnd calls
}

func newFakeSession() *fakeSession {
	return &fakeSession{audio: make(chan []byte, 64), events: make(chan voice.Event, 64), status: voice.StatusConnected}
}

func (f *fakeSession) Start(context.Context) error { return nil }
func (f *fakeSession) Close() error                { close(f.audio); close(f.events); return nil }
func (f *fakeSession) SendAudio(pcm []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, append([]byte(nil), pcm...))
	return nil
}
func (f *fakeSession) SendText(text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, text)
	return nil
}
func (f *fakeSession) Push(text string, _ voice.Urgency) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes = append(f.pushes, text)
	return nil
}
func (f *fakeSession) Audio() <-chan []byte       { return f.audio }
func (f *fakeSession) Events() <-chan voice.Event { return f.events }
func (f *fakeSession) SetMuted(m bool)            { f.mu.Lock(); f.muted = m; f.mu.Unlock() }
func (f *fakeSession) Status() voice.Status       { return f.status }
func (f *fakeSession) Speaking() bool             { return false }
func (f *fakeSession) sentFrames() int            { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }
func (f *fakeSession) sentTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}
func (f *fakeSession) pushed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pushes...)
}
func (f *fakeSession) isMuted() bool       { f.mu.Lock(); defer f.mu.Unlock(); return f.muted }
func (f *fakeSession) AudioStreamEnd()     { f.mu.Lock(); f.ends++; f.mu.Unlock() }
func (f *fakeSession) streamEnds() int     { f.mu.Lock(); defer f.mu.Unlock(); return f.ends }
func (f *fakeSession) emit(ev voice.Event) { f.events <- ev }

// fakeWatcher records injected status events.
type fakeWatcher struct {
	mu     sync.Mutex
	events []herdr.StatusEvent
}

func (w *fakeWatcher) Inject(ev herdr.StatusEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, ev)
}

// harness is a fully wired server over in-memory components.
type harness struct {
	t       *testing.T
	log     *eventlog.Log
	store   *state.Store
	herdr   *inject.Herdr
	sup     *supervisor.Supervisor
	srv     *Server
	http    *httptest.Server
	session *fakeSession
	watcher *fakeWatcher
	// model is the brain's scripted model when harnessOpts.brain is set.
	model  *braintest.Model
	cancel context.CancelFunc
}

type harnessOpts struct {
	voice   bool
	watcher bool
	// devTestdata serves /testdata/ from this directory (dev mode);
	// devBrain /testdata/brain/.
	devTestdata, devBrain string
	// statsInterval shortens the web.client.stats period.
	statsInterval time.Duration
	// reloadPrompts backs POST /api/prompts/reload; nil leaves it unset
	// (503).
	reloadPrompts func() []prompts.File
	// brain attaches a brain over the reflex fake model (--brain=fake)
	// to the supervisor and the server; providers, when set, registers
	// a second reflex provider "deepseek" (Catalog2) and lists gemini,
	// deepseek and an unavailable "openai" through Deps.BrainProviders.
	brain     bool
	providers bool
	// token, when set, becomes Deps.Token (config web.token): every
	// protected route then needs it.
	token string
}

func newHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	log, err := eventlog.Open("", eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open("", state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	h := inject.NewHerdr(nil, log)
	var pusher supervisor.Pusher = inject.SkippedPusher{Log: log}
	var session *fakeSession
	if o.voice {
		session = newFakeSession()
		pusher = session
	}
	var b *brain.Brain
	var model *braintest.Model
	if o.brain {
		model = braintest.Reflex()
		b, err = brain.New(brain.Config{PruneAfterTurns: 1}, model, log)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = b.Close() })
	}
	var providers []ProviderInfo
	if o.providers {
		second := braintest.Reflex()
		second.Models = braintest.Catalog2()
		if err := b.AddProvider(brain.ProviderDeepSeek, second); err != nil {
			t.Fatal(err)
		}
		providers = []ProviderInfo{{ID: "gemini", Label: "Gemini", Available: true}, {ID: "deepseek", Label: "DeepSeek", Available: true}, {ID: "openai", Label: "OpenAI", Available: false, Reason: "no OPENAI_API_KEY"}}
	}
	sup, err := supervisor.New(store, h, nil, pusher, log, supervisor.Config{Brain: b})
	if err != nil {
		t.Fatal(err)
	}
	sup.SetVoiceConnected(true)
	injector := inject.NewInjector(store, h, sup, log, nil)
	deps := Deps{Log: log, Store: store, Supervisor: sup, Injector: injector, Router: &inject.Router{}, Version: "test", Heartbeat: 200 * time.Millisecond, OutQueue: 8, DevTestdataDir: o.devTestdata, DevBrainDir: o.devBrain, StatsInterval: o.statsInterval,
		SystemPrompt: func() string { return "You are the voice. Aliases: auth." }, Aliases: []string{"auth"}, ReloadPrompts: o.reloadPrompts, Brain: b, BrainOffReason: "--brain=off", BrainProviders: providers, Token: o.token}
	if session != nil {
		deps.Voice = session
	}
	var watcher *fakeWatcher
	if o.watcher {
		watcher = &fakeWatcher{}
		deps.Watcher = watcher
	}
	srv, err := New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Run(ctx)
	if session != nil {
		go func() {
			for ev := range session.events {
				sup.OnVoiceEvent(ev)
				srv.OnVoiceEvent(ev)
			}
		}()
	}
	hs := httptest.NewServer(srv.Handler())
	hh := &harness{t: t, log: log, store: store, herdr: h, sup: sup, srv: srv, http: hs, session: session, watcher: watcher, model: model, cancel: cancel}
	t.Cleanup(func() {
		hs.Close()
		cancel()
		if session != nil {
			_ = session.Close()
		}
		_ = log.Close()
	})
	return hh
}

func codexScreen(t *testing.T) string {
	t.Helper()
	return screenFixture(t, "codex-approval-prompt.screen.txt")
}

func screenFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// postJSON posts v and decodes the reply into out (may be nil).
func (h *harness) postJSON(path string, v any, out any) (int, string) {
	h.t.Helper()
	return h.sendJSON(http.MethodPost, path, v, out)
}

// putModel PUTs v to /api/brain/model and decodes the reply into out.
func (h *harness) putModel(v any, out any) (int, string) {
	h.t.Helper()
	return h.sendJSON(http.MethodPut, "/api/brain/model", v, out)
}

func (h *harness) sendJSON(method, path string, v any, out any) (int, string) {
	h.t.Helper()
	body, _ := json.Marshal(v)
	req, err := http.NewRequest(method, h.http.URL+path, strings.NewReader(string(body)))
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf strings.Builder
	dec := json.NewDecoder(resp.Body)
	var raw json.RawMessage
	_ = dec.Decode(&raw)
	buf.Write(raw)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, buf.String()
}

func (h *harness) getJSON(path string, out any) int {
	h.t.Helper()
	resp, err := http.Get(h.http.URL + path)
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			h.t.Fatalf("GET %s: decode: %v", path, err)
		}
	}
	return resp.StatusCode
}

// authedRequest builds a GET or POST request against path, optionally
// carrying a bearer token, a cookie, or a request body (POST only) — the
// web.token auth tests exercise every combination.
func (h *harness) authedRequest(method, path, bearer string, cookies []*http.Cookie, body any) *http.Request {
	h.t.Helper()
	var r *http.Request
	var err error
	if body != nil {
		b, _ := json.Marshal(body)
		r, err = http.NewRequest(method, h.http.URL+path, strings.NewReader(string(b)))
		if err == nil {
			r.Header.Set("Content-Type", "application/json")
		}
	} else {
		r, err = http.NewRequest(method, h.http.URL+path, nil)
	}
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

// do sends req and returns the status, the decoded body (when out is
// not nil) and the response's own Set-Cookie headers.
func (h *harness) do(req *http.Request, out any) (int, []*http.Cookie) {
	h.t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, resp.Cookies()
}

// types returns the logged event types in order.
func (h *harness) types() []string {
	var out []string
	for _, ev := range h.log.Tail(0) {
		out = append(out, ev.Type)
	}
	return out
}

// subsequence checks that want appears in order (not necessarily
// adjacent) in got.
func subsequence(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", msg)
}
