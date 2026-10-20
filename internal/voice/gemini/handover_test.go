package gemini

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// errAlreadyConnected is AI Studio's refusal of a resume while the
// session's old connection is still open (live log 2026-10-19).
var errAlreadyConnected = errors.New("failed to receive setup complete: websocket: close 1008 (policy violation): BidiGenerateContent session is already connected to an existing client")

// errDeadline is the close at the end of a GoAway's time left.
var errDeadline = errors.New("websocket: close 1011 (internal server error): Deadline expired before operation could complete")

func handoverSteps(h *harness) []string {
	var out []string
	for _, p := range h.log.payloads("voice.handover") {
		out = append(out, p["step"].(string))
	}
	return out
}

// TestGoAwayRefusedOverlapHandsOver: AI Studio refuses the make-before-
// break dial after a GoAway; at the next quiet moment the session closes
// the old connection and resumes with the same handle (context kept),
// logging voice.handover instead of voice.connect_error.
func TestGoAwayRefusedOverlapHandsOver(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), fail(errAlreadyConnected), ok(c2)}}
	h := newHarness(t, Config{}, d)

	converse(t, h, c1, "what is codex doing?", "Codex is refactoring the parser.")
	acceptHandle(t, h, c1, "handle-1")
	c1.serve(goAwayMsg(50 * time.Second))
	if r := waitResumes(t, h, 1); r.ContextLost || r.Handle != "handle-1" {
		t.Fatalf("after the handover: %+v, want a resume with handle-1", r)
	}
	select {
	case <-c1.closed:
	case <-time.After(time.Second):
		t.Fatal("the old connection was not closed")
	}
	if got := dialHandles(d); strings.Join(got, ",") != ",handle-1,handle-1" {
		t.Errorf("dials presented %q", got)
	}
	if got := strings.Join(handoverSteps(h), ","); got != "refused,scheduled,closing" {
		t.Errorf("voice.handover steps = %q", got)
	}
	if h.log.has("voice.connect_error") || h.log.has("voice.handle_dropped") {
		t.Errorf("connect_error or handle_dropped logged: %v %v", h.log.payloads("voice.connect_error"), h.log.payloads("voice.handle_dropped"))
	}
	if h.log.has("voice.recap") || h.log.has("voice.reseeded") {
		t.Error("a resumed session was re-seeded")
	}
}

// TestGoAwayDeadlineKeepsHandle: if the GoAway deadline closes the old
// connection (1011 "Deadline expired") before a quiet moment came, the
// handle is kept and the reconnect resumes with it; a 1011 without a
// GoAway still drops it (TestFreshAfter1011OnResumedConnectionResumesNext).
func TestGoAwayDeadlineKeepsHandle(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), fail(errAlreadyConnected), ok(c2)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)

	acceptHandle(t, h, c1, "handle-1")
	// Not quiet: a reply is under way, so the handover waits.
	h.s.mu.Lock()
	h.s.turnOpen = true
	h.s.mu.Unlock()
	c1.serve(goAwayMsg(50 * time.Second))
	deadline := time.Now().Add(3 * time.Second)
	for len(d.records()) < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	c1.fail(errDeadline)
	if r := waitResumes(t, h, 1); r.ContextLost || r.Handle != "handle-1" {
		t.Fatalf("after the deadline: %+v, want a resume with handle-1", r)
	}
	if h.log.has("voice.handle_dropped") {
		t.Errorf("handle dropped: %v", h.log.payloads("voice.handle_dropped"))
	}
	disc := h.log.payloads("voice.disconnected")
	if len(disc) != 1 || disc[0]["after_goaway"] != true {
		t.Errorf("voice.disconnected = %v, want after_goaway", disc)
	}
}

// TestHandoverResumeRefusedOnceRetries: the server may still count the
// closed connection as open for a moment; one more refusal is retried
// with the same handle instead of dropping it.
func TestHandoverResumeRefusedOnceRetries(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), fail(errAlreadyConnected), fail(errAlreadyConnected), ok(c2)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)

	acceptHandle(t, h, c1, "handle-1")
	c1.serve(goAwayMsg(50 * time.Second))
	if r := waitResumes(t, h, 1); r.ContextLost || r.Handle != "handle-1" {
		t.Fatalf("after the handover: %+v, want a resume with handle-1", r)
	}
	if got := dialHandles(d); strings.Join(got, ",") != ",handle-1,handle-1,handle-1" {
		t.Errorf("dials presented %q", got)
	}
	if h.log.has("voice.handle_dropped") {
		t.Errorf("handle dropped: %v", h.log.payloads("voice.handle_dropped"))
	}
}

func TestIsAlreadyConnected(t *testing.T) {
	if !isAlreadyConnected(errAlreadyConnected) {
		t.Error("the AI Studio refusal not recognised")
	}
	for _, err := range []error{nil, errDeadline, errors.New("websocket: close 1008 (policy violation): Publisher model not found")} {
		if isAlreadyConnected(err) {
			t.Errorf("%v recognised as already connected", err)
		}
	}
}
