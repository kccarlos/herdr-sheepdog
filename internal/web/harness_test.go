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

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// fakeLLMServer is an OpenAI-compatible endpoint answering the three
// templates from the prompt text, so the real llm.Client (and its
// llm.call events) is exercised.
func fakeLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := ""
		for _, m := range req.Messages {
			prompt += m.Content + "\n"
		}
		var content string
		switch {
		case strings.Contains(prompt, "Would you like to run the following command?"):
			content = `{"spoken":"auth is asking whether it can run a command to create hello.txt.","question":"whether it can create hello.txt","is_question":true,"needs_decision":true,"options":[{"label":"Yes, proceed","key":"enter"},{"label":"No","key":"esc"}],"answer_keys":{"yes":["enter"],"no":["esc"]}}`
		case strings.Contains(prompt, "Ask Codex to do anything"):
			content = `{"spoken":"auth is idle at its prompt.","question":"","is_question":false,"needs_decision":false,"options":[],"answer_keys":{"yes":[],"no":[]}}`
		case strings.Contains(prompt, "can_answer"):
			content = `{"spoken":"auth is waiting at an approval prompt.","can_answer":true}`
		default: // resolve_instruction
			content = `{"pane_id":"fake:1","instruction_text":"skip the flaky test and rerun","is_answer":false,"answer":"","echo":"Telling auth to skip the flaky test and rerun.","ambiguous":false,"clarify_question":""}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": content}}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

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
	cancel  context.CancelFunc
}

type harnessOpts struct {
	voice   bool
	watcher bool
	// devTestdata serves /testdata/ from this directory (dev mode).
	devTestdata string
	// statsInterval shortens the web.client.stats period.
	statsInterval time.Duration
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
	llmSrv := fakeLLMServer(t)
	client, err := llm.New(llm.Config{BaseURL: llmSrv.URL + "/v1", Model: "fake", APIKeys: []string{"k"}, Log: log})
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
	sup, err := supervisor.New(store, h, client, nil, pusher, log, supervisor.Config{BriefingInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	sup.SetVoiceConnected(true)
	injector := inject.NewInjector(store, h, sup, log, nil)
	deps := Deps{Log: log, Store: store, Supervisor: sup, Injector: injector, Router: &inject.Router{}, Version: "test", Heartbeat: 200 * time.Millisecond, OutQueue: 8, DevTestdataDir: o.devTestdata, StatsInterval: o.statsInterval,
		SystemPrompt: "You are the voice. Aliases: auth.", Aliases: []string{"auth"}}
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
	hh := &harness{t: t, log: log, store: store, herdr: h, sup: sup, srv: srv, http: hs, session: session, watcher: watcher, cancel: cancel}
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
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", "codex-approval-prompt.screen.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// postJSON posts v and decodes the reply into out (may be nil).
func (h *harness) postJSON(path string, v any, out any) (int, string) {
	h.t.Helper()
	body, _ := json.Marshal(v)
	resp, err := http.Post(h.http.URL+path, "application/json", strings.NewReader(string(body)))
	if err != nil {
		h.t.Fatalf("POST %s: %v", path, err)
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
