package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// agentActions returns the payloads of the web.agent_action events.
func agentActionEvents(t *testing.T, h *harness) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "web.agent_action" {
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

// The user's own actions: rename and stop a tracked agent, refusals in
// plain words with their status, metadata-only logging, a line for the
// orchestrator, and the feature in bot-ready.
func TestAgentActions(t *testing.T) {
	h := newHarness(t, harnessOpts{brain: true})
	trackWorking(t, h, "w1:p2", "auth", "• Working (3s • esc to interrupt)")

	var renamed struct {
		OK     bool   `json:"ok"`
		Spoken string `json:"spoken"`
	}
	if code, body := h.postJSON(windowPath("w1:p2", "rename"), map[string]any{"name": "login fix"}, &renamed); code != http.StatusOK || !renamed.OK || !strings.Contains(renamed.Spoken, "login fix") {
		t.Fatalf("rename = %d %s %+v", code, body, renamed)
	}
	if code, _ := h.postJSON(windowPath("w1:p2", "rename"), map[string]any{"name": " "}, nil); code != http.StatusBadRequest {
		t.Errorf("empty name = %d, want 400", code)
	}
	if code, _ := h.postJSON(windowPath("w9:p9", "close"), map[string]any{}, nil); code != http.StatusNotFound {
		t.Errorf("close of an untracked pane = %d, want 404", code)
	}
	// Mock herdr cannot start agents: a plain refusal, not a crash.
	secret := "a prompt that must not be logged"
	if code, body := h.postJSON("/api/agents", map[string]any{"kind": "claude", "dir": "bench", "text": secret}, nil); code < 400 || !strings.Contains(body, "cannot start agents") {
		t.Errorf("new agent on mock herdr = %d %s", code, body)
	}
	if code, _ := h.postJSON("/api/agents", map[string]any{"kind": "claude"}, nil); code != http.StatusBadRequest {
		t.Errorf("new agent without dir = %d, want 400", code)
	}

	ev := agentActionEvents(t, h)
	actions := []string{}
	for _, e := range ev {
		actions = append(actions, e["action"].(string))
	}
	if !slices.Equal(actions, []string{"rename", "rename", "close", "new"}) {
		t.Errorf("web.agent_action actions = %v", actions)
	}
	if ev[3]["text_len"] != float64(len(secret)) {
		t.Errorf("new agent event = %v", ev[3])
	}
	for _, e := range h.log.Tail(0) {
		if strings.Contains(string(e.Payload), secret) {
			t.Errorf("%s logged the prompt: %s", e.Type, e.Payload)
		}
	}
	if lines := brainLines(h); !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "renamed an agent from the phone") }) {
		t.Errorf("orchestrator lines = %q", lines)
	}

	if !slices.Contains(h.srv.features(), "agent-actions") {
		t.Errorf("features %v lack agent-actions", h.srv.features())
	}
}
