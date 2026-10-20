package gemini

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Never resume a resumed session (handle.go): a handle issued by a
// connection that was itself opened by a resume restores only that
// connection's own content (docs/dev/research/live-resume-context-2026-10-04.md),
// so the reconnect after it opens a fresh session and re-seeds it. The
// rule is Vertex's: on AI Studio chained resumes keep the context
// (chainedResumesKeepContext), so these tests run on a Vertex config.

// onVertex sets a Vertex backend on cfg.
func onVertex(cfg Config) Config {
	cfg.Vertex = &Vertex{Project: "p", Location: "us-central1"}
	return cfg
}

// waitResumes waits until n EventResumed were emitted and returns the
// n-th.
func waitResumes(t *testing.T, h *harness, n int) voice.EventResumed {
	t.Helper()
	isResumed := func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok }
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.countEvents(isResumed) >= n {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.evMu.Lock()
	defer h.evMu.Unlock()
	k := 0
	for _, ev := range h.events {
		if r, ok := ev.(voice.EventResumed); ok {
			k++
			if k == n {
				return r
			}
		}
	}
	t.Fatalf("only %d resumes, want %d", k, n)
	return voice.EventResumed{}
}

// acceptHandle serves a handle on c and waits until the session took it.
func acceptHandle(t *testing.T, h *harness, c *fakeConn, handle string) {
	t.Helper()
	c.serve(handleMsg(handle))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.s.mu.Lock()
		got := h.s.handle
		h.s.mu.Unlock()
		if got == handle {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("handle %q not accepted", handle)
}

func dialHandles(d *fakeDialer) []string {
	var out []string
	for _, r := range d.records() {
		out = append(out, r.handle)
	}
	return out
}

func resumeGenerations(h *harness) []any {
	var out []any
	for _, p := range h.log.payloads("voice.resume") {
		out = append(out, p["generation"])
	}
	return out
}

// TestSecondGoAwayStartsFresh: the first GoAway of a fresh session
// resumes; the GoAway on the resumed connection opens a fresh session
// (no handle presented), re-seeded with the conversation from both
// connections, logged resumed_twice; and that fresh session's own first
// GoAway resumes again.
func TestSecondGoAwayStartsFresh(t *testing.T) {
	c1, c2, c3, c4 := newFakeConn("c1"), newFakeConn("c2"), newFakeConn("c3"), newFakeConn("c4")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2), ok(c3), ok(c4)}}
	h := newHarness(t, onVertex(Config{}), d)

	converse(t, h, c1, "what is codex doing?", "Codex is refactoring the parser.")
	acceptHandle(t, h, c1, "handle-1")
	c1.serve(goAwayMsg(30 * time.Second))
	if r := waitResumes(t, h, 1); r.ContextLost || r.Handle != "handle-1" {
		t.Fatalf("first GoAway: %+v, want a resume with handle-1", r)
	}

	c2.serve(inputTranscriptMsg("and claude?", false))
	c2.serve(outputTranscriptMsg("Claude is writing the release notes."))
	c2.serve(turnCompleteMsg())
	acceptHandle(t, h, c2, "handle-2")
	c2.serve(goAwayMsg(30 * time.Second))
	if r := waitResumes(t, h, 2); !r.ContextLost {
		t.Fatalf("second GoAway: %+v, want a fresh session (context lost)", r)
	}
	select {
	case <-c2.closed:
	case <-time.After(time.Second):
		t.Fatal("the resumed connection was not closed after the swap")
	}
	recap := c3.waitSent(t, "recap", isContent("Codex is refactoring the parser."))
	text := turnText(recap.content.Turns[0])
	if !strings.Contains(text, "Claude is writing the release notes.") {
		t.Errorf("recap lacks the resumed connection's turn:\n%s", text)
	}
	dropped := h.log.payloads("voice.handle_dropped")
	if len(dropped) != 1 || dropped[0]["reason"] != reasonResumedTwice || dropped[0]["handle_generation"] != 1 || dropped[0]["generation"] != 1 {
		t.Errorf("voice.handle_dropped = %v", dropped)
	}
	if p := h.log.payloads("voice.reseeded"); len(p) != 1 || p[0]["reason"] != reasonResumedTwice {
		t.Errorf("voice.reseeded = %v", p)
	}

	acceptHandle(t, h, c3, "handle-3")
	c3.serve(goAwayMsg(30 * time.Second))
	if r := waitResumes(t, h, 3); r.ContextLost || r.Handle != "handle-3" {
		t.Fatalf("the fresh session's first GoAway: %+v, want a resume with handle-3", r)
	}
	if got := dialHandles(d); strings.Join(got, ",") != ",handle-1,,handle-3" {
		t.Errorf("dials presented %q", got)
	}
	if got := resumeGenerations(h); len(got) != 3 || got[0] != 1 || got[1] != 0 || got[2] != 1 {
		t.Errorf("voice.resume generations = %v, want [1 0 1]", got)
	}
}

// TestResumedConnectionLostBeforeItsOwnHandleStartsFresh: until the
// resumed connection issues a handle of its own, the session still holds
// the generation-0 handle it resumed with; presenting it again would be
// a second resume of the first session (baseline 2026-10-05,
// resume-recall-twice r3). The drop opens a fresh session instead,
// logged resumed_twice, and that session's own first GoAway resumes.
func TestResumedConnectionLostBeforeItsOwnHandleStartsFresh(t *testing.T) {
	c1, c2, c3, c4 := newFakeConn("c1"), newFakeConn("c2"), newFakeConn("c3"), newFakeConn("c4")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2), ok(c3), ok(c4)}}
	h := newHarness(t, onVertex(Config{ReconnectBackoff: 5 * time.Millisecond}), d)

	acceptHandle(t, h, c1, "handle-1")
	c1.serve(goAwayMsg(30 * time.Second))
	waitResumes(t, h, 1)
	c2.fail(errors.New("websocket: close 1006 (abnormal closure)"))
	if r := waitResumes(t, h, 2); !r.ContextLost {
		t.Fatalf("after the drop: %+v, want a fresh session", r)
	}
	dropped := h.log.payloads("voice.handle_dropped")
	if len(dropped) != 1 || dropped[0]["reason"] != reasonResumedTwice || dropped[0]["handle_generation"] != 0 || dropped[0]["generation"] != 1 {
		t.Errorf("voice.handle_dropped = %v", dropped)
	}
	acceptHandle(t, h, c3, "handle-3")
	c3.serve(goAwayMsg(30 * time.Second))
	if r := waitResumes(t, h, 3); r.ContextLost || r.Handle != "handle-3" {
		t.Fatalf("the fresh session's first GoAway: %+v, want a resume with handle-3", r)
	}
	if got := dialHandles(d); strings.Join(got, ",") != ",handle-1,,handle-3" {
		t.Errorf("dials presented %q", got)
	}
}

// TestTwoQuickRecyclesDoNotReuseTheHandle is the #139 gap (baseline
// 2026-10-05, resume-recall-twice r3, 09:06:44Z and 09:06:58Z): the
// first recycle resumes; a turn happens on the resumed connection but
// no handle arrives before the second recycle 14 s later. The second
// recycle must not present the first session's handle again (that turn
// would be lost with no re-seed): it opens a fresh session, re-seeded
// with the turn heard on the resumed connection.
func TestTwoQuickRecyclesDoNotReuseTheHandle(t *testing.T) {
	c1, c2, c3 := newFakeConn("c1"), newFakeConn("c2"), newFakeConn("c3")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2), ok(c3)}}
	h := newHarness(t, onVertex(Config{}), d)

	converse(t, h, c1, "what is codex doing?", "Codex is refactoring the parser.")
	acceptHandle(t, h, c1, "handle-1")
	h.s.Recycle("bench_resume")
	if r := waitResumes(t, h, 1); r.ContextLost || r.Handle != "handle-1" {
		t.Fatalf("first recycle: %+v, want a resume with handle-1", r)
	}
	c2.serve(inputTranscriptMsg("and what is claude writing?", false))
	c2.serve(outputTranscriptMsg("Claude is writing the README."))
	c2.serve(turnCompleteMsg())
	isTurn := func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok }
	for deadline := time.Now().Add(3 * time.Second); h.countEvents(isTurn) < 2 && time.Now().Before(deadline); {
		time.Sleep(2 * time.Millisecond)
	}
	h.s.Recycle("bench_resume")
	if r := waitResumes(t, h, 2); !r.ContextLost {
		t.Fatalf("second recycle: %+v, want a fresh session", r)
	}
	if got := dialHandles(d); strings.Join(got, ",") != ",handle-1," {
		t.Errorf("dials presented %q, want the third with no handle", got)
	}
	recap := c3.waitSent(t, "recap", isContent("Claude is writing the README."))
	if text := turnText(recap.content.Turns[0]); !strings.Contains(text, "Codex is refactoring the parser.") {
		t.Errorf("recap lacks the first connection's turn:\n%s", text)
	}
	if p := h.log.payloads("voice.handle_dropped"); len(p) != 1 || p[0]["reason"] != reasonResumedTwice {
		t.Errorf("voice.handle_dropped = %v", p)
	}
	if p := h.log.payloads("voice.reseeded"); len(p) != 1 || p[0]["reason"] != reasonResumedTwice {
		t.Errorf("voice.reseeded = %v", p)
	}
}

// TestFreshAfter1011OnResumedConnectionResumesNext: a 1011 on the
// resumed connection drops its handle (server_internal_error) and the
// fresh connection is generation 0 again: its first GoAway resumes.
func TestFreshAfter1011OnResumedConnectionResumesNext(t *testing.T) {
	c1, c2, c3, c4 := newFakeConn("c1"), newFakeConn("c2"), newFakeConn("c3"), newFakeConn("c4")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2), ok(c3), ok(c4)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)

	acceptHandle(t, h, c1, "handle-1")
	c1.serve(goAwayMsg(30 * time.Second))
	waitResumes(t, h, 1)
	acceptHandle(t, h, c2, "handle-2")
	c2.fail(errors.New("websocket: close 1011 (internal server error): Internal error encountered."))
	if r := waitResumes(t, h, 2); !r.ContextLost {
		t.Fatalf("after the 1011: %+v, want fresh", r)
	}
	acceptHandle(t, h, c3, "handle-3")
	c3.serve(goAwayMsg(30 * time.Second))
	if r := waitResumes(t, h, 3); r.ContextLost || r.Handle != "handle-3" {
		t.Fatalf("after the fresh connection's GoAway: %+v, want a resume with handle-3", r)
	}
	if got := dialHandles(d); strings.Join(got, ",") != ",handle-1,,handle-3" {
		t.Errorf("dials presented %q", got)
	}
	dropped := h.log.payloads("voice.handle_dropped")
	if len(dropped) != 1 || dropped[0]["reason"] != "server_internal_error" {
		t.Errorf("voice.handle_dropped = %v", dropped)
	}
}

// TestFailedResumeThenFreshResumesNext: a resume the server rejects
// gives up the handle and connects fresh (generation 0); that session's
// first GoAway resumes.
func TestFailedResumeThenFreshResumesNext(t *testing.T) {
	c1, c2, c3 := newFakeConn("c1"), newFakeConn("c2"), newFakeConn("c3")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), fail(errors.New("setup: Internal error encountered.")), ok(c2), ok(c3)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)

	acceptHandle(t, h, c1, "handle-1")
	c1.fail(errors.New("websocket: close 1006 (abnormal closure)"))
	if r := waitResumes(t, h, 1); !r.ContextLost {
		t.Fatalf("after the failed resume: %+v, want fresh", r)
	}
	acceptHandle(t, h, c2, "handle-2")
	c2.serve(goAwayMsg(30 * time.Second))
	if r := waitResumes(t, h, 2); r.ContextLost || r.Handle != "handle-2" {
		t.Fatalf("after the fresh connection's GoAway: %+v, want a resume with handle-2", r)
	}
	if got := dialHandles(d); strings.Join(got, ",") != ",handle-1,,handle-2" {
		t.Errorf("dials presented %q", got)
	}
}

// TestLateHandleFromOldConnectionIsNotKept: a handle the old connection
// issues while the fresh one is being dialled belongs to the old
// session; the fresh connection must not carry it.
func TestLateHandleFromOldConnectionIsNotKept(t *testing.T) {
	c1, c2, c3 := newFakeConn("c1"), newFakeConn("c2"), newFakeConn("c3")
	gate := make(chan struct{})
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2), ok(c3)}, gate: gate}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)

	acceptHandle(t, h, c1, "handle-1")
	if err := h.s.Reset("test"); err != nil {
		t.Fatal(err)
	}
	for len(d.records()) < 2 {
		time.Sleep(2 * time.Millisecond)
	}
	acceptHandle(t, h, c1, "late") // the old connection is still live
	close(gate)
	if r := waitResumes(t, h, 1); !r.ContextLost {
		t.Fatalf("reset: %+v, want fresh", r)
	}
	h.s.mu.Lock()
	got := h.s.handle
	h.s.mu.Unlock()
	if got != "" {
		t.Errorf("handle after the fresh install = %q, want none", got)
	}
	c2.fail(errors.New("websocket: close 1006 (abnormal closure)"))
	waitResumes(t, h, 2)
	if hs := dialHandles(d); hs[2] != "" {
		t.Errorf("dials presented %q, want the third without the late handle", hs)
	}
}

// TestFreshSessionIsGivenRecentBriefings: on the briefing channel a
// fresh connection also gets the recent briefings again, silently and
// marked re-sent (the recap holds only the spoken ones); a focus line is
// left to the recap; after a "New conversation" none are re-sent.
func TestFreshSessionIsGivenRecentBriefings(t *testing.T) {
	c1, c2, c3, c4 := newFakeConn("c1"), newFakeConn("c2"), newFakeConn("c3"), newFakeConn("c4")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2), ok(c3), ok(c4)}}
	h := newHarness(t, onVertex(briefingConfig()), d)

	if err := h.s.PushTagged("bench-codex is refactoring the parser, 2 minutes in.", voice.UrgencySilent, map[string]any{"agent": "bench-codex"}); err != nil {
		t.Fatal(err)
	}
	c1.waitSent(t, "briefing", isBriefing("refactoring the parser"))
	if err := h.s.PushTagged("Focus: bench-codex.", voice.UrgencySilent, map[string]any{voice.TagFocus: true}); err != nil {
		t.Fatal(err)
	}
	c1.waitSent(t, "focus", isBriefing("Focus: bench-codex."))
	acceptHandle(t, h, c1, "handle-1")
	c1.serve(goAwayMsg(30 * time.Second))
	waitResumes(t, h, 1)
	acceptHandle(t, h, c2, "handle-2")
	c2.serve(goAwayMsg(30 * time.Second))
	if r := waitResumes(t, h, 2); !r.ContextLost {
		t.Fatalf("second GoAway: %+v, want fresh", r)
	}
	m := c3.waitSent(t, "re-sent briefing", isBriefing("refactoring the parser"))
	if note, _ := m.tool.FunctionResponses[0].Response["resent"].(string); !strings.Contains(note, "without its history") {
		t.Errorf("re-sent briefing note = %q", note)
	}
	for _, s := range c3.sentMsgs() {
		if isBriefing("Focus: bench-codex.")(s) {
			t.Errorf("the focus briefing was re-sent (the recap carries it)")
		}
	}
	var reasons []any
	for _, p := range h.log.payloads("voice.briefing_resent") {
		if p["reason"] == reasonResumedTwice {
			reasons = append(reasons, p["briefing"])
		}
	}
	if len(reasons) != 1 {
		t.Errorf("voice.briefing_resent resumed_twice = %v, want one", reasons)
	}

	if err := h.s.Reset("new conversation"); err != nil {
		t.Fatal(err)
	}
	waitResumes(t, h, 3)
	time.Sleep(30 * time.Millisecond)
	for _, s := range c4.sentMsgs() {
		if isBriefing("refactoring the parser")(s) {
			t.Errorf("a briefing from before the reset was re-sent")
		}
	}
}
