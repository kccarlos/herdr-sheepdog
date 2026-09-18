package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

func TestStaticPages(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for _, path := range []string{"/debug", "/debug/", "/debug/app.js", "/debug/style.css", "/debug/presets.js", "/voice", "/voice/"} {
		resp, err := http.Get(h.http.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
		}
	}
	resp, err := http.Get(h.http.URL + "/debug/nope.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing static file = %d", resp.StatusCode)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = client.Get(h.http.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/voice" {
		t.Errorf("/ = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestInjectBlockedChainAndConfirm(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	var res injectResponse
	code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, &res)
	if code != http.StatusOK || !res.OK || res.Agent.Alias != "auth" || res.Agent.State != herdr.StateBlocked || !res.Agent.Synthetic {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	want := []string{"herdr.state", "herdr.read", "llm.call", "pending.created", "voice.push"}
	if got := h.types(); !subsequence(got, want) {
		t.Fatalf("chain %v not in %v", want, got)
	}
	// The push is the skipped no-op.
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "voice.push" {
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			if p["skipped"] != true || !strings.Contains(p["text"].(string), "auth is asking") {
				t.Fatalf("voice.push payload = %s", ev.Payload)
			}
		}
	}

	var st StateResponse
	if code := h.getJSON("/api/state", &st); code != http.StatusOK {
		t.Fatalf("/api/state = %d", code)
	}
	if len(st.Agents) != 1 || st.PendingAction == nil || st.PendingAction.Kind != state.PendingAnswerPrompt || st.PendingAction.PaneID != "fake:1" {
		t.Fatalf("state = %+v", st)
	}
	if st.Voice.Mode != "off" || st.Voice.Status != "closed" || st.Herdr != "mock" || st.Version != "test" {
		t.Fatalf("state meta = %+v", st.Voice)
	}
	var agents []state.Agent
	if h.getJSON("/api/agents", &agents); len(agents) != 1 || agents[0].PaneID != "fake:1" {
		t.Fatalf("agents = %+v", agents)
	}

	// "yes" → confirm → send_keys → pending.confirmed.
	var tr ToolResponse
	code, body = h.postJSON("/api/inject/utterance", map[string]any{"text": "yes"}, &tr)
	if code != http.StatusOK || tr.Tool != "confirm" || tr.Via != "text_router" || !strings.Contains(tr.Spoken, "Told auth yes") {
		t.Fatalf("utterance yes = %d %s", code, body)
	}
	want = []string{"voice.tool_call", "herdr.send", "pending.confirmed", "voice.tool_result"}
	if got := h.types(); !subsequence(got, want) {
		t.Fatalf("confirm chain %v not in %v", want, got)
	}
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "herdr.send" {
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			if p["kind"] != "keys" || p["synthetic"] != true {
				t.Fatalf("herdr.send payload = %s", ev.Payload)
			}
		}
	}
	if h.getJSON("/api/state", &st); st.PendingAction != nil {
		t.Fatalf("pending not cleared: %+v", st.PendingAction)
	}

	// Unroutable text is rejected without touching the supervisor.
	code, body = h.postJSON("/api/inject/utterance", map[string]any{"text": "please reticulate splines"}, nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("unrouted = %d %s", code, body)
	}
	code, _ = h.postJSON("/api/inject/utterance", map[string]any{"text": ""}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("empty text = %d", code)
	}

	// done, working, remove.
	code, body = h.postJSON("/api/inject/done", map[string]any{"pane_id": "fake:1"}, &res)
	if code != http.StatusOK || res.Agent.State != herdr.StateDone {
		t.Fatalf("done = %d %s", code, body)
	}
	code, body = h.postJSON("/api/inject/state", map[string]any{"pane_id": "fake:1", "state": "working"}, &res)
	if code != http.StatusOK || res.Agent.State != herdr.StateWorking {
		t.Fatalf("working = %d %s", code, body)
	}
	code, body = h.postJSON("/api/inject/state", map[string]any{"pane_id": "fake:1", "state": "weird"}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("weird state = %d %s", code, body)
	}
	code, body = h.postJSON("/api/inject/done", map[string]any{"pane_id": "nope"}, nil)
	if code != http.StatusConflict {
		t.Fatalf("done unknown = %d %s", code, body)
	}
	code, body = h.postJSON("/api/inject/remove", map[string]any{"pane_id": "fake:1"}, nil)
	if code != http.StatusOK {
		t.Fatalf("remove = %d %s", code, body)
	}
	if h.getJSON("/api/agents", &agents); len(agents) != 0 {
		t.Fatalf("agents after remove = %+v", agents)
	}
	code, _ = h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "x"}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("missing screen = %d", code)
	}
}

func TestPendingCancelAndProposeStop(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, nil)
	var tr ToolResponse
	code, body := h.postJSON("/api/pending/cancel", map[string]any{}, &tr)
	if code != http.StatusOK || tr.Tool != "cancel" || tr.Spoken != "Cancelled." {
		t.Fatalf("cancel = %d %s", code, body)
	}
	if !subsequence(h.types(), []string{"pending.created", "voice.tool_call", "pending.cancelled", "voice.tool_result"}) {
		t.Fatalf("cancel chain missing in %v", h.types())
	}
	var st StateResponse
	if h.getJSON("/api/state", &st); st.PendingAction != nil {
		t.Fatal("pending not cleared")
	}

	// propose through the router → send_instruction pending → stop → ctrl+c.
	h.postJSON("/api/inject/state", map[string]any{"pane_id": "fake:1", "state": "working"}, nil)
	code, body = h.postJSON("/api/inject/utterance", map[string]any{"text": "tell auth to skip the flaky test and rerun"}, &tr)
	if code != http.StatusOK || tr.Tool != "propose" || !strings.HasSuffix(tr.Spoken, "Sent.") {
		t.Fatalf("propose = %d %s", code, body)
	}
	if h.getJSON("/api/state", &st); st.PendingAction == nil || st.PendingAction.Kind != state.PendingSendInstruction {
		t.Fatalf("no send_instruction pending: %+v", st.PendingAction)
	}
	code, body = h.postJSON("/api/inject/utterance", map[string]any{"text": "stop"}, &tr)
	if code != http.StatusOK || tr.Tool != "cancel" {
		t.Fatalf("stop = %d %s", code, body)
	}
	sawCtrlC := false
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "herdr.send" && strings.Contains(string(ev.Payload), `"ctrl+c"`) {
			sawCtrlC = true
		}
	}
	if !sawCtrlC {
		t.Fatalf("no ctrl+c send in %v", h.types())
	}
}

// sseClient reads one SSE stream and delivers parsed `log` events.
type sseClient struct {
	events chan eventlog.Event
	lines  chan string
	cancel context.CancelFunc
}

func openSSE(t *testing.T, url string) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		cancel()
		t.Fatalf("SSE = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	c := &sseClient{events: make(chan eventlog.Event, 256), lines: make(chan string, 256), cancel: cancel}
	t.Cleanup(cancel)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		event := ""
		var id uint64
		for sc.Scan() {
			line := sc.Text()
			select {
			case c.lines <- line:
			default:
			}
			switch {
			case strings.HasPrefix(line, "id: "):
				id, _ = strconv.ParseUint(strings.TrimPrefix(line, "id: "), 10, 64)
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && event == "log":
				var ev eventlog.Event
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err == nil {
					ev.Seq = id
					c.events <- ev
				}
			case line == "":
				event, id = "", 0
			}
		}
	}()
	return c
}

func (c *sseClient) next(t *testing.T, d time.Duration) (eventlog.Event, bool) {
	t.Helper()
	select {
	case ev := <-c.events:
		return ev, true
	case <-time.After(d):
		return eventlog.Event{}, false
	}
}

func TestSSEStreamsFilteredEvents(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	// Replay: two events already logged.
	h.log.Log("herdr", "herdr.state", "w1:p1", map[string]any{"state": "working"})
	h.log.Log("supervisor", "supervisor.note", "", nil)

	all := openSSE(t, h.http.URL+"/events?since=10")
	first, ok := all.next(t, time.Second)
	if !ok || first.Type != "herdr.state" {
		t.Fatalf("replay first = %+v ok=%v", first, ok)
	}
	if second, ok := all.next(t, time.Second); !ok || second.Type != "supervisor.note" || second.Seq != first.Seq+1 {
		t.Fatalf("replay second = %+v", second)
	}

	sup := openSSE(t, h.http.URL+"/events?source=supervisor")
	pane := openSSE(t, h.http.URL+"/events?pane=fake:1&type=pending.")

	h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, nil)

	// Supervisor-only stream sees pending.created and nothing from herdr.
	var seen []string
	deadline := time.After(3 * time.Second)
loop:
	for {
		select {
		case ev := <-sup.events:
			if ev.Source != "supervisor" {
				t.Fatalf("source filter leaked %s", ev.Source)
			}
			seen = append(seen, ev.Type)
			if ev.Type == "pending.created" {
				break loop
			}
		case <-deadline:
			t.Fatalf("no pending.created on supervisor stream; saw %v", seen)
		}
	}
	ev, ok := pane.next(t, 2*time.Second)
	if !ok || ev.Type != "pending.created" || ev.PaneID != "fake:1" {
		t.Fatalf("pane+type filter = %+v ok=%v", ev, ok)
	}
	// The unfiltered live stream got the whole chain without duplicates.
	var live []eventlog.Event
	for {
		ev, ok := all.next(t, 500*time.Millisecond)
		if !ok {
			break
		}
		live = append(live, ev)
	}
	types := make([]string, 0, len(live))
	for i, ev := range live {
		types = append(types, ev.Type)
		if i > 0 && ev.Seq <= live[i-1].Seq {
			t.Fatalf("out of order / duplicate seq at %d: %v", i, live)
		}
	}
	if !subsequence(types, []string{"herdr.state", "llm.call", "pending.created", "voice.push"}) {
		t.Fatalf("live chain = %v", types)
	}

	// Heartbeat comments arrive.
	waitFor(t, 2*time.Second, func() bool {
		for {
			select {
			case l := <-all.lines:
				if strings.HasPrefix(l, ": keepalive") {
					return true
				}
			default:
				return false
			}
		}
	}, "keepalive")

	// JSON tail endpoint shares the filters.
	var tail []eventlog.Event
	if code := h.getJSON("/api/events?source=supervisor&n=2", &tail); code != http.StatusOK || len(tail) != 2 || tail[1].Type != "pending.created" {
		t.Fatalf("tail = %d %+v", code, tail)
	}
}

func TestHerdrEventEndpoint(t *testing.T) {
	h := newHarness(t, harnessOpts{watcher: true})
	body := `{"event":"pane_agent_status_changed","data":{"type":"pane_agent_status_changed","pane_id":"w7:p1","workspace_id":"w7","agent_status":"blocked","agent":"codex"}}`
	resp, err := http.Post(h.http.URL+"/api/herdr/event", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status event = %d", resp.StatusCode)
	}
	h.watcher.mu.Lock()
	n := len(h.watcher.events)
	var ev herdr.StatusEvent
	if n > 0 {
		ev = h.watcher.events[0]
	}
	h.watcher.mu.Unlock()
	if n != 1 || ev.PaneID != "w7:p1" || ev.State != herdr.StateBlocked || ev.Source != "http" || ev.At.IsZero() {
		t.Fatalf("injected = %d %+v", n, ev)
	}
	// Lifecycle events are acknowledged and logged, not injected.
	resp, err = http.Post(h.http.URL+"/api/herdr/event", "application/json", strings.NewReader(`{"event":"pane_agent_detected","data":{"pane_id":"w7:p1","workspace_id":"w7","agent":"codex"}}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("fleet event = %d", resp.StatusCode)
	}
	if !subsequence(h.types(), []string{"web.herdr_event"}) {
		t.Fatalf("fleet event not logged: %v", h.types())
	}
	resp, _ = http.Post(h.http.URL+"/api/herdr/event", "application/json", strings.NewReader(`nope`))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body = %d", resp.StatusCode)
	}

	// Mock mode: 503.
	m := newHarness(t, harnessOpts{})
	resp, _ = http.Post(m.http.URL+"/api/herdr/event", "application/json", strings.NewReader(body))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("mock mode = %d", resp.StatusCode)
	}
}
