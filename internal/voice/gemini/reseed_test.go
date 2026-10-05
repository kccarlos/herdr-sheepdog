package gemini

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// converse plays one exchange on c: the shepherd's question (never
// marked final, as Gemini sends it), the voice's answer, turnComplete.
func converse(t *testing.T, h *harness, c *fakeConn, heard, said string) {
	t.Helper()
	c.serve(inputTranscriptMsg(heard, false))
	c.serve(outputTranscriptMsg(said))
	c.serve(turnCompleteMsg())
	h.waitEvent("turn complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
}

// TestReconnectAfter1011ReseedsTheConversation is docs/timelines.md C23:
// a 1011 drops the resumption handle (68 of 77 resumes came back
// resumed:false on 2026-09-23..25), and the fresh connection used to
// get back only the silent briefing. It is now seeded first, silently
// (no turn complete), with the recent conversation — the shepherd's
// words, the voice's answer and the spoken push — and the briefing the
// supervisor pushes on EventResumed follows it. voice.reseeded says so.
func TestReconnectAfter1011ReseedsTheConversation(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)

	c1.serve(handleMsg("handle-A"))
	converse(t, h, c1, "why did the auth tests fail?", "Two tests time out on the token refresh.")
	if err := h.s.Push("docs finished the readme.", voice.UrgencyIdle); err != nil {
		t.Fatal(err)
	}
	c1.waitSent(t, "push", isContent("docs finished the readme."))
	c1.fail(errors.New("websocket: close 1011 (internal server error): Internal error encountered."))
	h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	// The supervisor's briefing, as it pushes on EventResumed.
	if err := h.s.Push("auth is idle; docs is idle.", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	c2.waitSent(t, "briefing", isContent("auth is idle; docs is idle."))

	var contents []sent
	for _, m := range c2.sentMsgs() {
		if m.kind == "content" {
			contents = append(contents, m)
		}
	}
	if len(contents) != 2 {
		t.Fatalf("c2 content = %s, want the recap then the briefing", describe(c2.sentMsgs()))
	}
	recap := contents[0].content.Turns[0].Parts[0].Text
	for _, want := range []string{
		"User: why did the auth tests fail?",
		"You: Two tests time out on the token refresh.",
		"Supervisor update: docs finished the readme.",
		"Do not answer it",
	} {
		if !strings.Contains(recap, want) {
			t.Errorf("recap lacks %q:\n%s", want, recap)
		}
	}
	if *contents[0].content.TurnComplete {
		t.Error("the recap must not complete the turn (the model would answer it)")
	}
	if strings.Index(recap, "Shepherd:") > strings.Index(recap, "You:") {
		t.Errorf("recap not oldest first:\n%s", recap)
	}
	p := h.log.payloads("voice.reseeded")
	if len(p) != 1 || p[0]["items"] != 3 || p[0]["reason"] != "not_resumed" {
		t.Errorf("voice.reseeded = %v", p)
	}
}

// TestResumedReconnectDoesNotReseed: a connection that resumed with its
// handle still holds the conversation; nothing is re-sent.
func TestResumedReconnectDoesNotReseed(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)

	c1.serve(handleMsg("handle-A"))
	converse(t, h, c1, "why did the auth tests fail?", "Two tests time out.")
	c1.fail(errors.New("websocket: close 1006 (abnormal closure)"))
	ev := h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if r := ev.(voice.EventResumed); r.ContextLost {
		t.Fatalf("resumed = %+v, want resumed with the handle", r)
	}
	time.Sleep(30 * time.Millisecond)
	if n := c2.count("content"); n != 0 || h.log.has("voice.reseeded") {
		t.Errorf("a resumed connection was re-seeded: %s", describe(c2.sentMsgs()))
	}
}

// TestResetDoesNotReseed: "New conversation" drops the handle on
// purpose, and the recap with it — the fresh connection starts empty.
func TestResetDoesNotReseed(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{}, d)

	converse(t, h, c1, "why did the auth tests fail?", "Two tests time out.")
	if err := h.s.Reset("new_conversation"); err != nil {
		t.Fatal(err)
	}
	h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	time.Sleep(30 * time.Millisecond)
	if n := c2.count("content"); n != 0 || h.log.has("voice.reseeded") {
		t.Errorf("a reset connection was re-seeded: %s", describe(c2.sentMsgs()))
	}
}

// TestRecapKeepsOnlyWhatWasHeard: a reply the client cut off is recapped
// as the words that played.
func TestRecapKeepsOnlyWhatWasHeard(t *testing.T) {
	c1 := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c1)}})
	h.s.ClientAttached(true)
	c1.serve(inputTranscriptMsg("what is auth doing?", false))
	c1.serve(audioMsg(make([]byte, 48*2000))) // 2 s of speech
	c1.serve(outputTranscriptMsg("Auth is running the migration tests and"))
	c1.serve(audioMsg(make([]byte, 48*2000)))
	c1.serve(outputTranscriptMsg(" then it will deploy to staging."))
	h.waitEvent("speaking", func(ev voice.Event) bool { return h.s.Speaking() })
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c1.serve(interruptedMsg())
	h.waitEvent("interrupted", func(ev voice.Event) bool { _, ok := ev.(voice.EventInterrupted); return ok })
	h.s.ClientPlayback(voice.PlaybackFlushed, 2000)
	text, _, _ := h.s.reseedText()
	if !strings.Contains(text, "You (cut off after 2 s): Auth is running the migration tests and") || strings.Contains(text, "staging") {
		t.Errorf("recap:\n%s", text)
	}
}

// TestReseedEndsWithTheFocusLine: the supervisor's current-focus line
// (voice.TagFocus), sent silently on the old connection, is kept and
// ends the re-seed of a connection that came back without its context
// (a 1011, a session refresh), so a follow-up about "it" still works.
func TestReseedEndsWithTheFocusLine(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)

	c1.serve(handleMsg("handle-A"))
	converse(t, h, c1, "how are the tests?", "auth ran them: 3 of 5 pass.")
	focus := "Current focus, for follow-ups (context only, never read it out). Agent in focus: auth."
	if err := h.s.PushTagged(focus, voice.UrgencySilent, map[string]any{voice.TagFocus: true}); err != nil {
		t.Fatal(err)
	}
	c1.waitSent(t, "focus", isContent(focus))
	c1.fail(errors.New("websocket: close 1011 (internal server error): Internal error encountered."))
	h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	c2.waitSent(t, "recap", isContent("User: how are the tests?"))
	var recap string
	for _, m := range c2.sentMsgs() {
		if m.kind == "content" {
			recap = m.content.Turns[0].Parts[0].Text
			break
		}
	}
	if !strings.HasSuffix(recap, focus) || !strings.Contains(recap, "You: auth ran them: 3 of 5 pass.") {
		t.Errorf("re-seed does not end with the focus line:\n%s", recap)
	}
}
