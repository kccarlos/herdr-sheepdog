package web

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// POST /api/conversation/reset resets the brain and the voice session,
// answers {"ok":true,"reset_id":"…","at":"…"}, logs conversation.reset
// (so the SSE stream carries it) and GET /api/conversation shows the
// "New conversation" divider afterwards.
func TestConversationReset(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, brain: true})

	var resp map[string]any
	code, _ := h.postJSON("/api/conversation/reset", map[string]string{"via": "debug"}, &resp)
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	if resp["ok"] != true {
		t.Fatalf("resp = %v", resp)
	}
	id, _ := resp["reset_id"].(string)
	if id == "" {
		t.Fatal("expected a reset_id")
	}
	at, _ := resp["at"].(string)
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Fatalf("at = %q: %v", at, err)
	}

	// The voice session's memory was reset too.
	if got := h.session.resetReasons(); len(got) != 1 || got[0] != "debug" {
		t.Fatalf("voice resets = %v", got)
	}

	// conversation.reset was logged (the SSE stream is just the log).
	found := false
	for _, ev := range h.log.Tail(0) {
		if ev.Type != "conversation.reset" {
			continue
		}
		found = true
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p["via"] != "debug" || p["reset_id"] != id {
			t.Errorf("conversation.reset payload = %v", p)
		}
	}
	if !found {
		t.Fatal("conversation.reset not logged")
	}

	// The conversation history shows a "New conversation" divider.
	var conv ConversationResponse
	if code := h.getJSON("/api/conversation", &conv); code != http.StatusOK {
		t.Fatalf("GET /api/conversation: %d", code)
	}
	last := conv.Items[len(conv.Items)-1]
	if last.Kind != "notice" || last.Subkind != "reset" || last.Text != "New conversation" {
		t.Fatalf("last item = %+v", last)
	}

	// GET /api/state reports conversation_started_at close to now.
	var st StateResponse
	if code := h.getJSON("/api/state", &st); code != http.StatusOK {
		t.Fatalf("GET /api/state: %d", code)
	}
	if st.ConversationStartedAt.IsZero() {
		t.Fatal("conversation_started_at not set")
	}
}

// The via field must be one of the accepted values.
func TestConversationResetBadVia(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	code, _ := h.postJSON("/api/conversation/reset", map[string]string{"via": "bogus"}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
}

// The endpoint is token-protected.
func TestConversationResetAuth(t *testing.T) {
	h := newHarness(t, harnessOpts{token: "s3cr3t"})
	req := h.authedRequest(http.MethodPost, "/api/conversation/reset", "", nil, map[string]string{"via": "app"})
	if code, _ := h.do(req, nil); code != http.StatusUnauthorized {
		t.Fatalf("no token: code = %d", code)
	}
	req = h.authedRequest(http.MethodPost, "/api/conversation/reset", "s3cr3t", nil, map[string]string{"via": "app"})
	if code, _ := h.do(req, nil); code != http.StatusOK {
		t.Fatalf("with token: code = %d", code)
	}
}
