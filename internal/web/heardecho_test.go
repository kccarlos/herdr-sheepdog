package web

import (
	"net/http"
	"sync"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// With HeardEcho (serve --voice=off --heard-echo) a routed utterance
// reports what a voice session would around it: the to_supervisor call,
// then the answer spoken in full and the end of the turn.
func TestUtteranceHeardEcho(t *testing.T) {
	h := newHarness(t, harnessOpts{brain: true})
	var mu sync.Mutex
	var got []voice.Event
	h.srv.deps.HeardEcho = func(e voice.Event) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, e)
	}
	var tr ToolResponse
	if code, body := h.postJSON("/api/utterance", map[string]any{"text": "let me know when auth is done"}, &tr); code != http.StatusOK || tr.Spoken == "" {
		t.Fatalf("utterance = %d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 {
		t.Fatalf("events = %#v", got)
	}
	if c, ok := got[0].(voice.EventToolCall); !ok || c.Call.Name == "" {
		t.Errorf("first = %#v, want the tool call", got[0])
	}
	if tr2, ok := got[1].(voice.EventTranscript); !ok || tr2.Dir != voice.DirOut || tr2.Text != tr.Spoken || !tr2.Final {
		t.Errorf("second = %#v, want the spoken answer", got[1])
	}
	if _, ok := got[2].(voice.EventTurnComplete); !ok {
		t.Errorf("third = %#v, want turn complete", got[2])
	}
}
