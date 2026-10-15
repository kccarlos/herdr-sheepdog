package gemini

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Reset ("New conversation", docs/dev/roles.md) drops the resumption handle
// and reconnects fresh: the next dial carries no handle and the
// supervisor sees EventResumed{ContextLost:true} so it re-learns the
// roster, exactly like any other context-losing reconnect.
func TestResetReconnectsWithoutHandle(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{}, d)

	c1.serve(handleMsg("handle-R"))
	time.Sleep(10 * time.Millisecond)
	if !h.s.hasHandle() {
		t.Fatal("expected a resumption handle before Reset")
	}

	if err := h.s.Reset("new_conversation"); err != nil {
		t.Fatal(err)
	}
	ev := h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	r := ev.(voice.EventResumed)
	if !r.ContextLost {
		t.Fatalf("resumed = %+v, want ContextLost", r)
	}
	recs := d.records()
	if len(recs) != 2 || recs[1].handle != "" {
		t.Fatalf("dials = %+v, want the second dial to carry no handle", recs)
	}
	if h.s.hasHandle() {
		t.Error("handle still set after Reset")
	}
	if !h.log.has("voice.reset") {
		t.Error("voice.reset not logged")
	}
	if !h.log.has("voice.handle_dropped") {
		t.Error("voice.handle_dropped not logged")
	}
	select {
	case <-c1.closed:
	case <-time.After(time.Second):
		t.Fatal("old connection not closed after Reset")
	}
	h.waitEvent("connected again", func(ev voice.Event) bool {
		st, ok := ev.(voice.EventStatus)
		return ok && st.Status == voice.StatusConnected
	})
}

// Reset before Start (or after Close) is a harmless no-op.
func TestResetBeforeStartIsNoop(t *testing.T) {
	s, err := newSession(Config{APIKeys: []string{"k"}}, nil, &fakeDialer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reset("x"); err != nil {
		t.Fatalf("Reset before Start: %v", err)
	}
}
