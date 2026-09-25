package web

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// TestReplyWithNoListenerIsHeld: a to_supervisor reply that lands inside
// reply_wait_s is the tool result while a page is attached; when the
// page has just left, the voice would speak that result to nobody, so
// the call gets the pending result and the reply goes the push way,
// which the voice session holds until a client attaches (PR #64).
func TestReplyWithNoListenerIsHeld(t *testing.T) {
	h := newHarness(t, harnessOpts{voice: true, brain: true})
	if code, body := h.postJSON("/api/inject/blocked", map[string]any{"pane_id": "fake:1", "alias": "auth", "screen": codexScreen(t)}, nil); code != 200 {
		t.Fatalf("inject blocked = %d %s", code, body)
	}
	ask := func(text string) voice.ToolResult {
		t.Helper()
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: text, Final: true})
		res, err := h.srv.runTool(t.Context(), toolCall(text), "voice")
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	c := dialWS(t, h)
	waitFor(t, 2*time.Second, h.session.isAttached, "the client to attach")
	res := ask("what did auth say")
	if res.Pending || !strings.HasPrefix(res.Spoken, "The screen ends with:") {
		t.Fatalf("attached: result = %+v (want the reply inline)", res)
	}
	before := len(h.session.pushed())

	_ = c.conn.CloseNow()
	waitFor(t, 2*time.Second, func() bool { return !h.session.isAttached() }, "the client to leave")
	h.srv.deps.Brain.Quiesce()
	res = ask("and what did auth say now")
	if !res.Pending || res.Spoken != "" {
		t.Fatalf("detached: result = %+v (want the pending result)", res)
	}
	waitFor(t, 2*time.Second, func() bool { return len(h.session.pushed()) > before }, "the reply to be pushed")
	if p := h.session.pushed()[before]; !strings.Contains(p, "The screen ends with:") {
		t.Errorf("pushed %q, want the reply", p)
	}
	found := false
	for _, ev := range h.log.Tail(0) {
		if ev.Type == "brain.reply_pending" && strings.Contains(string(ev.Payload), `"no_listener":true`) {
			found = true
		}
	}
	if !found {
		t.Error("no brain.reply_pending with no_listener")
	}
}
