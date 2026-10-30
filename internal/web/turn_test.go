package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// typedTurnHarness is a harness with the fake orchestrator and a voice
// session, and the agent "auth" blocked on a prompt for the reflex model
// to read.
func typedTurnHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, harnessOpts{brain: true, voice: true})
	var res injectResponse
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, &res); code != http.StatusOK {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	h.brain.Quiesce()
	return h
}

func TestTurnReturnsTheReplyAndShowsInTheConversation(t *testing.T) {
	h := typedTurnHarness(t)
	spoken := len(h.session.pushed())

	var got TurnResponse
	if code, body := h.postJSON("/api/turn", map[string]any{"text": "what did auth say"}, &got); code != http.StatusOK {
		t.Fatalf("turn = %d %s", code, body)
	}
	if !strings.HasPrefix(got.Reply, "The screen ends with:") || got.TurnID == "" {
		t.Fatalf("turn = %+v, want the reflex model's reply and a turn id", got)
	}

	var conv ConversationResponse
	if code := h.getJSON("/api/conversation", &conv); code != http.StatusOK {
		t.Fatalf("conversation = %d", code)
	}
	var user, dog *ConversationItem
	for i, it := range conv.Items {
		switch {
		case it.Kind == "shepherd" && it.Text == "what did auth say":
			user = &conv.Items[i]
		case it.Kind == "dog" && it.Text == got.Reply:
			dog = &conv.Items[i]
		}
	}
	if user == nil || user.Via != "typed" {
		t.Errorf("typed user item = %+v in %+v", user, conv.Items)
	}
	if dog == nil || dog.Via != "typed" {
		t.Fatalf("dog item = %+v in %+v", dog, conv.Items)
	}
	if user != nil && user.ID >= dog.ID {
		t.Errorf("the reply (id %d) is not after the typed line (id %d)", dog.ID, user.ID)
	}

	// The voice got one silent context line; nothing was spoken.
	pushes := h.session.pushed()[spoken:]
	urgencies := h.session.pushUrgencies()
	if len(pushes) != 1 || urgencies[len(urgencies)-1] != voice.UrgencySilent {
		t.Fatalf("pushes = %q urgencies = %v, want one silent line", pushes, urgencies)
	}
	if !strings.Contains(pushes[0], "what did auth say") || !strings.Contains(pushes[0], got.Reply) {
		t.Errorf("silent line = %q, want the typed text and the reply", pushes[0])
	}
	if texts := h.session.sentTexts(); len(texts) != 0 {
		t.Errorf("the voice was sent %q, want nothing", texts)
	}
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "voice.push" && !strings.Contains(string(ev.Payload), `"urgency":"silent"`) {
			t.Errorf("a spoken push: %s", ev.Payload)
		}
	}
}

func TestTurnActedWithTheRealSupervisor(t *testing.T) {
	// An idle agent, so the instruction is not refused for an open prompt.
	h := newHarness(t, harnessOpts{brain: true, voice: true})
	if code, body := h.postJSON("/api/inject/pane", map[string]any{"pane_id": "fake:1", "alias": "auth", "kind": "codex", "state": "idle", "screen": "› Ask Codex to do anything"}, nil); code != http.StatusOK {
		t.Fatalf("inject pane = %d %s", code, body)
	}
	h.brain.Quiesce()
	var got TurnResponse
	if code, body := h.postJSON("/api/turn", map[string]any{"text": "tell auth to run the tests"}, &got); code != http.StatusOK {
		t.Fatalf("turn = %d %s", code, body)
	}
	if got.Reply == "" || !got.Acted {
		t.Fatalf("turn = %+v, want a reply and acted (the instruction went to the worker)", got)
	}
	h.brain.Quiesce()
	var sawTurn bool
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "brain.reply_pending" {
			t.Errorf("the typed turn ran detached: %s", ev.Payload)
		}
		sawTurn = sawTurn || (ev.Type == "supervisor.relay" && strings.Contains(string(ev.Payload), got.TurnID))
	}
	if !sawTurn {
		t.Errorf("no supervisor.relay for turn %s", got.TurnID)
	}
}

func TestTurnOrchestratorOff(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true})
	var got map[string]string
	code, body := h.postJSON("/api/turn", map[string]any{"text": "status"}, &got)
	if code != http.StatusServiceUnavailable || !strings.Contains(got["error"], "--brain=off") {
		t.Fatalf("turn = %d %s, want 503 with the reason", code, body)
	}
	if n := len(h.session.pushed()); n != 0 {
		t.Errorf("%d pushes with the orchestrator off", n)
	}
}

func TestTurnRejectsBadBodies(t *testing.T) {
	h := newHarness(t, harnessOpts{brain: true, voice: true})
	for _, body := range []string{"", "{}", `{"text":""}`, `{"text":"  "}`, `{"text":1}`, `{"text":`, `[]`} {
		req, err := http.NewRequest(http.MethodPost, h.http.URL+"/api/turn", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if code, _ := h.do(req, nil); code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want 400", body, code)
		}
	}
}

func TestTurnSameIdempotencyKeyRunsOnce(t *testing.T) {
	h := typedTurnHarness(t)
	send := func(key string) (int, string) {
		body, _ := json.Marshal(map[string]string{"text": "what did auth say"})
		req, _ := http.NewRequest(http.MethodPost, h.http.URL+"/api/turn", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(idemHeader, key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	turns := func() int {
		n := 0
		for _, ev := range h.log.Tail(0) {
			if ev.Type == "web.utterance" && strings.Contains(string(ev.Payload), `"via":"typed"`) {
				n++
			}
		}
		return n
	}
	c1, b1 := send("turn-1")
	c2, b2 := send("turn-1")
	if c1 != http.StatusOK || c2 != c1 || b1 != b2 {
		t.Fatalf("turn: %d %q then %d %q", c1, b1, c2, b2)
	}
	if n := turns(); n != 1 {
		t.Errorf("same key ran %d turns, want 1", n)
	}
	if c, _ := send("turn-2"); c != http.StatusOK || turns() != 2 {
		t.Errorf("a new key: %d, %d turns, want a second turn", c, turns())
	}
}
