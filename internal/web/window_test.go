package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// windowPath is /api/agents/{pane}/{what} with the pane id path-escaped.
func windowPath(pane, what string) string {
	return "/api/agents/" + url.PathEscape(pane) + "/" + what
}

// trackWorking registers a synthetic working pane the daemon tracks.
func trackWorking(t *testing.T, h *harness, pane, alias, screen string) {
	t.Helper()
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": pane, "alias": alias, "kind": "codex", "screen": screen}, nil); code != http.StatusOK {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	if code, body := h.postJSON("/api/inject/state", map[string]any{"pane_id": pane, "state": "working"}, nil); code != http.StatusOK {
		t.Fatalf("inject state = %d %s", code, body)
	}
}

// agentInputs returns the payloads of the web.agent_input events.
func agentInputs(t *testing.T, h *harness) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "web.agent_input" {
			continue
		}
		var pl map[string]any
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		pl["pane"] = ev.PaneID
		out = append(out, pl)
	}
	return out
}

// brainLines returns the brain transcript's contents.
func brainLines(h *harness) []string {
	var out []string
	for _, m := range h.brain.Transcript().Messages() {
		if m.Kind == brain.KindSystem {
			out = append(out, m.Content)
		}
	}
	return out
}

// The window reads a tracked agent's screen with its state and spoken
// name, ANSI stripped, and a hash that lets a poll skip an unchanged
// frame; an untracked pane is 404.
func TestAgentScreen(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	trackWorking(t, h, "fake:1", "auth", "line one\n\x1b[1;32mgreen\x1b[0m line two")

	var scr ScreenResponse
	if code := h.getJSON(windowPath("fake:1", "screen"), &scr); code != http.StatusOK {
		t.Fatalf("screen = %d", code)
	}
	if scr.PaneID != "fake:1" || scr.Alias != "auth" || scr.Spoken == "" || scr.State != "working" || scr.Unchanged || scr.Hash == "" || scr.Lines != 120 {
		t.Fatalf("screen = %+v", scr)
	}
	if !strings.Contains(scr.Text, "green line two") || strings.Contains(scr.Text, "\x1b") {
		t.Fatalf("text = %q", scr.Text)
	}

	var same ScreenResponse
	h.getJSON(windowPath("fake:1", "screen")+"?hash="+scr.Hash, &same)
	if !same.Unchanged || same.Text != "" || same.Hash != scr.Hash || same.State != "working" {
		t.Fatalf("unchanged screen = %+v", same)
	}

	if code, body := h.postJSON("/api/inject/screen", map[string]any{"pane_id": "fake:1", "screen": "line three"}, nil); code != http.StatusOK {
		t.Fatalf("inject screen = %d %s", code, body)
	}
	var changed ScreenResponse
	h.getJSON(windowPath("fake:1", "screen")+"?hash="+scr.Hash+"&lines=999", &changed)
	if changed.Unchanged || changed.Hash == scr.Hash || changed.Text != "line three" || changed.Lines != 500 {
		t.Fatalf("changed screen = %+v", changed)
	}

	var e map[string]string
	if code := h.getJSON(windowPath("other:9", "screen"), &e); code != http.StatusNotFound || e["error"] != "no such agent" {
		t.Errorf("untracked pane = %d %v", code, e)
	}
	if code := h.getJSON(windowPath("fake:1", "screen")+"?lines=x", &e); code != http.StatusBadRequest {
		t.Errorf("lines=x = %d", code)
	}
	// Looking is not talking: no read made it the subject or a send.
	for _, typ := range h.types() {
		if typ == "herdr.send" || typ == "web.agent_input" {
			t.Errorf("a screen read logged %s", typ)
		}
	}
}

// A pane id with % round-trips through the path.
func TestAgentScreenEscapedPane(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	trackWorking(t, h, "%12", "tmux", "hello")
	var scr ScreenResponse
	if code := h.getJSON(windowPath("%12", "screen"), &scr); code != http.StatusOK || scr.PaneID != "%12" || scr.Text != "hello" {
		t.Fatalf("screen = %d %+v", code, scr)
	}
}

// Both window routes need the token like every other /api route.
func TestAgentWindowNeedsToken(t *testing.T) {
	h := newHarness(t, harnessOpts{token: "s3cret"})
	for _, r := range []*http.Request{
		h.authedRequest(http.MethodGet, windowPath("fake:1", "screen"), "", nil, nil),
		h.authedRequest(http.MethodPost, windowPath("fake:1", "input"), "", nil, map[string]any{"keys": []string{"enter"}}),
		h.authedRequest(http.MethodGet, windowPath("fake:1", "screen"), "wrong", nil, nil),
	} {
		if code, _ := h.do(r, nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", r.Method, r.URL.Path, code)
		}
	}
	if code, _ := h.do(h.authedRequest(http.MethodGet, windowPath("fake:1", "screen"), "s3cret", nil, nil), nil); code != http.StatusNotFound {
		t.Errorf("authed untracked = %d, want 404", code)
	}
}

// A typed prompt goes to the agent as one submission, becomes its task,
// is logged by length only, and the brain hears it.
func TestAgentInputPrompt(t *testing.T) {
	h := newHarness(t, harnessOpts{brain: true})
	trackWorking(t, h, "fake:1", "auth", "ready")

	var res map[string]any
	code, body := h.postJSON(windowPath("fake:1", "input"), map[string]any{"text": "  run the tests  ", "submit": true}, &res)
	if code != http.StatusOK || res["ok"] != true || res["kind"] != "prompt" {
		t.Fatalf("prompt = %d %s", code, body)
	}
	if row, _ := h.store.Get("fake:1"); row.CurrentTask != "run the tests" {
		t.Errorf("current task = %q", row.CurrentTask)
	}
	var scr ScreenResponse
	h.getJSON(windowPath("fake:1", "screen"), &scr)
	if !strings.Contains(scr.Text, "› run the tests") {
		t.Errorf("screen after prompt = %q", scr.Text)
	}
	in := agentInputs(t, h)
	if len(in) != 1 || in[0]["kind"] != "prompt" || in[0]["len"] != float64(len("run the tests")) || in[0]["pane"] != "fake:1" || in[0]["text"] != nil {
		t.Errorf("web.agent_input = %v", in)
	}
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "web.agent_input" && strings.Contains(string(ev.Payload), "run the tests") {
			t.Errorf("the prompt text was logged: %s", ev.Payload)
		}
	}
	if lines := brainLines(h); !containsSub(lines, "the CEO typed a prompt in") || !containsSub(lines, "run the tests") {
		t.Errorf("brain lines = %q", lines)
	}

	for name, req := range map[string]map[string]any{
		"empty":        {"text": "   "},
		"submit false": {"text": "x", "submit": false},
		"both":         {"text": "x", "keys": []string{"enter"}},
		"neither":      {},
		"too long":     {"text": strings.Repeat("x", 4001)},
	} {
		if code, body := h.postJSON(windowPath("fake:1", "input"), req, nil); code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, code, body)
		}
	}
	if code, _ := h.postJSON(windowPath("nope:1", "input"), map[string]any{"text": "hi"}, nil); code != http.StatusNotFound {
		t.Errorf("untracked prompt = %d, want 404", code)
	}
}

// Allow-listed keys go to the pane and the brain hears them; anything
// off the list is refused before a byte is sent.
func TestAgentInputKeys(t *testing.T) {
	h := newHarness(t, harnessOpts{brain: true})
	trackWorking(t, h, "fake:1", "auth", "working")

	var res map[string]any
	code, body := h.postJSON(windowPath("fake:1", "input"), map[string]any{"keys": []string{"esc"}}, &res)
	if code != http.StatusOK || res["kind"] != "keys" {
		t.Fatalf("keys = %d %s", code, body)
	}
	sends := 0
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "herdr.send" && strings.Contains(string(ev.Payload), `"esc"`) {
			sends++
		}
	}
	if sends != 1 {
		t.Errorf("%d herdr.send esc events, want 1", sends)
	}
	if in := agentInputs(t, h); len(in) != 1 || in[0]["kind"] != "keys" {
		t.Errorf("web.agent_input = %v", in)
	}
	if lines := brainLines(h); !containsSub(lines, "the CEO pressed esc in") {
		t.Errorf("brain lines = %q", lines)
	}
	for _, keys := range [][]string{{"q"}, {"ctrl+d"}, {"enter", "rm -rf"}, {}, {"1", "1", "1", "1", "1", "1", "1", "1", "1"}} {
		if code, body := h.postJSON(windowPath("fake:1", "input"), map[string]any{"keys": keys}, nil); code != http.StatusBadRequest {
			t.Errorf("keys %v = %d %s, want 400", keys, code, body)
		}
	}
}

// Keys that are exactly an option of the agent's open question answer
// it through the tap path: confirmed, cleared, and heard like a tap.
func TestAgentInputKeysAnswerThePendingQuestion(t *testing.T) {
	h := newHarness(t, harnessOpts{brain: true})
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, nil); code != http.StatusOK {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	var st StateResponse
	h.getJSON("/api/state", &st)
	if st.PendingAction == nil || len(st.PendingAction.Options) < 1 {
		t.Fatalf("no pending action: %+v", st.PendingAction)
	}

	var res map[string]any
	code, body := h.postJSON(windowPath("fake:1", "input"), map[string]any{"keys": []string{"1"}}, &res)
	if code != http.StatusOK || res["kind"] != "answer" || !strings.HasPrefix(res["spoken"].(string), "Told auth ") {
		t.Fatalf("answer by keys = %d %s", code, body)
	}
	if _, has := h.store.Pending(); has {
		t.Error("the pending action is still open")
	}
	confirmed := false
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "pending.confirmed" {
			var pl map[string]any
			_ = json.Unmarshal(ev.Payload, &pl)
			confirmed = pl["via"] == "tap"
		}
	}
	if !confirmed {
		t.Errorf("no pending.confirmed via tap in %v", h.types())
	}
	if lines := brainLines(h); !containsSub(lines, "the shepherd tapped") {
		t.Errorf("brain lines = %q", lines)
	}
}

func containsSub(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// Enter is a plain key even while the agent's question is open (the
// CEO may have moved the cursor with up/down, so the recorded default is
// not what it selects); keys in another agent's window leave the
// question alone too.
func TestAgentInputEnterIsAPlainKey(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	trackWorking(t, h, "fake:2", "web", "working")
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, nil); code != http.StatusOK {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	var res map[string]any
	if code, body := h.postJSON(windowPath("fake:2", "input"), map[string]any{"keys": []string{"1"}}, &res); code != http.StatusOK || res["kind"] != "keys" {
		t.Fatalf("1 in another window = %d %s", code, body)
	}
	for _, keys := range [][]string{{"down"}, {"enter"}} {
		if code, body := h.postJSON(windowPath("fake:1", "input"), map[string]any{"keys": keys}, &res); code != http.StatusOK || res["kind"] != "keys" {
			t.Fatalf("%v = %d %s", keys, code, body)
		}
	}
	if _, has := h.store.Pending(); !has {
		t.Fatal("plain keys closed the question")
	}
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "pending.confirmed" {
			t.Errorf("plain keys confirmed the question: %s", ev.Payload)
		}
	}
}
