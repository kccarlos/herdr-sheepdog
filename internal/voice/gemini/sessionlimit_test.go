package gemini

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// AI Studio ends a Live session 60 minutes after it opened (refresh.go):
// the live connection closes with 1008 "Requested entity was not found"
// and a resume with its handle is refused "session not found or expired"
// (live log 2026-10-08..20).
var (
	errEntityNotFound = errors.New("websocket: close 1008 (policy violation): Requested entity was not found")
	errSessionExpired = errors.New("failed to receive setup complete: websocket: close 1008 (policy violation): BidiGenerateContent session not found or expired")
)

// ageSession moves the session's fresh-start time back by d.
func (h *harness) ageSession(d time.Duration) {
	h.s.mu.Lock()
	h.s.freshAt = h.s.freshAt.Add(-d)
	h.s.mu.Unlock()
}

func droppedReasons(h *harness) []string {
	var out []string
	for _, p := range h.log.payloads("voice.handle_dropped") {
		out = append(out, p["reason"].(string))
	}
	return out
}

// TestSessionGoneStartsFreshAtOnce: the server ending the session
// (1008 "Requested entity was not found") drops the handle as
// session_expired, not resumed_twice, and the reconnect opens a fresh,
// re-seeded session without first trying the dead handle.
func TestSessionGoneStartsFreshAtOnce(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{}, d)

	converse(t, h, c1, "what is codex doing?", "Codex is refactoring the parser.")
	acceptHandle(t, h, c1, "handle-1")
	c1.fail(errEntityNotFound)
	if r := waitResumes(t, h, 1); !r.ContextLost {
		t.Fatalf("after the session ended: %+v, want a fresh session", r)
	}
	if got := dialHandles(d); strings.Join(got, ",") != "," {
		t.Errorf("dials presented %q, want no handle on the reconnect", got)
	}
	if got := droppedReasons(h); strings.Join(got, ",") != reasonSessionExpired {
		t.Errorf("voice.handle_dropped reasons = %q, want %q", got, reasonSessionExpired)
	}
	c2.waitSent(t, "reseed", isContent("User: what is codex doing?"))
}

// TestResumeRefusedAsExpiredGoesFresh: a resume the server refuses with
// "session not found or expired" is not retried, however many attempts
// the config allows.
func TestResumeRefusedAsExpiredGoesFresh(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), fail(errSessionExpired), ok(c2)}}
	h := newHarness(t, Config{ResumeAttempts: 3, ReconnectBackoff: 5 * time.Millisecond}, d)

	acceptHandle(t, h, c1, "handle-1")
	c1.fail(errors.New("websocket: close 1006 (abnormal closure)"))
	if r := waitResumes(t, h, 1); !r.ContextLost {
		t.Fatalf("after the refused resume: %+v, want a fresh session", r)
	}
	if got := dialHandles(d); strings.Join(got, ",") != ",handle-1," {
		t.Errorf("dials presented %q", got)
	}
}

// TestAIStudioRefreshesBeforeTheSessionLimit: on AI Studio the max-age
// refresh is due before the 60-minute limit (and before the GoAway at
// about 57 minutes), at a quiet moment; Vertex keeps two hours.
func TestAIStudioRefreshesBeforeTheSessionLimit(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{}, d)
	acceptHandle(t, h, c1, "handle-1")
	h.ageSession(aistudioMaxAge + time.Second)
	if !refreshUntil(h, 3*time.Second) {
		t.Fatal("no refresh past the AI Studio max age")
	}
	h.waitEvent("fresh connection", isContextLost)
	if got := dialHandles(d); strings.Join(got, ",") != "," {
		t.Errorf("dials presented %q", got)
	}
	if p := h.log.payloads("voice.session_refreshed"); len(p) != 1 || p[0]["reason"] != refreshAge {
		t.Errorf("voice.session_refreshed = %v", p)
	}

	v1 := newFakeConn("v1")
	dv := &fakeDialer{results: []func() (Conn, error){ok(v1), ok(newFakeConn("v2"))}}
	hv := newHarness(t, onVertex(Config{}), dv)
	hv.ageSession(aistudioSessionLife)
	if refreshUntil(hv, 300*time.Millisecond) {
		t.Error("a Vertex session refreshed at an hour")
	}
}

// TestGoAwayPastMaxAgeStartsFresh: a GoAway when the session is past
// its max age (AI Studio at ~57 minutes, the refresh not yet done
// because the moment was never quiet) opens a fresh session at once
// instead of resuming into a connection the server cuts minutes later:
// no resume dial, no handover.
func TestGoAwayPastMaxAgeStartsFresh(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{}, d)

	converse(t, h, c1, "what is codex doing?", "Codex is refactoring the parser.")
	acceptHandle(t, h, c1, "handle-1")
	h.s.mu.Lock()
	h.s.turnOpen = true // not quiet: the max-age refresh waits
	h.s.mu.Unlock()
	h.ageSession(57 * time.Minute)
	c1.serve(goAwayMsg(50 * time.Second))
	if r := waitResumes(t, h, 1); !r.ContextLost {
		t.Fatalf("after the GoAway: %+v, want a fresh session", r)
	}
	if got := dialHandles(d); strings.Join(got, ",") != "," {
		t.Errorf("dials presented %q, want no resume dial", got)
	}
	if h.log.has("voice.handover") {
		t.Errorf("voice.handover logged: %v", h.log.payloads("voice.handover"))
	}
	if p := h.log.payloads("voice.session_refreshed"); len(p) != 1 || p[0]["reason"] != refreshAge {
		t.Errorf("voice.session_refreshed = %v", p)
	}
	c2.waitSent(t, "reseed", isContent("User: what is codex doing?"))
}

// TestAIStudioResumesAResumedSession: on AI Studio a handle issued by a
// resumed connection restores the whole session (canary chain arm
// 2026-10-20), so a drop of the resumed connection resumes again
// instead of opening a fresh session.
func TestAIStudioResumesAResumedSession(t *testing.T) {
	c1, c2, c3 := newFakeConn("c1"), newFakeConn("c2"), newFakeConn("c3")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2), ok(c3)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)

	acceptHandle(t, h, c1, "handle-1")
	c1.serve(goAwayMsg(50 * time.Second))
	if r := waitResumes(t, h, 1); r.ContextLost {
		t.Fatalf("first resume lost the context: %+v", r)
	}
	acceptHandle(t, h, c2, "handle-2")
	c2.fail(errors.New("websocket: close 1006 (abnormal closure)"))
	if r := waitResumes(t, h, 2); r.ContextLost || r.Handle != "handle-2" {
		t.Fatalf("second resume: %+v, want a resume with handle-2", r)
	}
	if got := dialHandles(d); strings.Join(got, ",") != ",handle-1,handle-2" {
		t.Errorf("dials presented %q", got)
	}
	if got := resumeGenerations(h); len(got) != 2 || got[1] != 2 {
		t.Errorf("voice.resume generations = %v, want [1 2]", got)
	}
	if h.log.has("voice.handle_dropped") {
		t.Errorf("handle dropped: %v", h.log.payloads("voice.handle_dropped"))
	}
}

func TestIsSessionGone(t *testing.T) {
	for _, err := range []error{errEntityNotFound, errSessionExpired} {
		if !isSessionGone(err) {
			t.Errorf("%v not recognised", err)
		}
	}
	for _, err := range []error{nil, errDeadline, errAlreadyConnected, errors.New("websocket: close 1008 (policy violation): Publisher model was not found")} {
		if isSessionGone(err) {
			t.Errorf("%v recognised as session gone", err)
		}
	}
}
