package gemini

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

func isContextLost(ev voice.Event) bool {
	r, ok := ev.(voice.EventResumed)
	return ok && r.ContextLost
}

// backdate moves the session's fresh-start time back by d, as if the
// model session had been running (resumed throughout) that long.
func (h *harness) backdate(d time.Duration) {
	h.s.mu.Lock()
	h.s.freshAt = h.s.freshAt.Add(-d)
	h.s.mu.Unlock()
}

// refreshUntil calls maybeRefresh until the dialer saw a second dial
// (the fresh connection) or the deadline passes, and reports whether it
// did.
func refreshUntil(h *harness, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		h.s.maybeRefresh()
		if len(h.dialer.records()) >= 2 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// TestSessionRefreshedAfterTwoHours is docs/bench-2026-10-03.md #9: run
// 1's 18-hour, ~120-resume session voiced 5 % of push bodies and relayed
// 17/17 Chinese push questions back, where run 2's fresh session voiced
// them whole. Past two hours the session no longer resumes: at a quiet
// moment it dials without the handle, is re-seeded with the recent
// conversation (the bare "On it." left out) and emits
// EventResumed{ContextLost} so the supervisor re-pushes the briefing.
// The refresh loop finds the moment by itself.
func TestSessionRefreshedAfterTwoHours(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{}, d)

	c1.serve(handleMsg("handle-A"))
	converse(t, h, c1, "what is auth doing?", "On it.")
	converse(t, h, c1, "and docs?", "Docs finished the readme.")
	h.backdate(refreshMaxAge + time.Minute)

	h.waitEvent("fresh connection", isContextLost)
	recs := d.records()
	if len(recs) != 2 || recs[0].handle != "" || recs[1].handle != "" {
		t.Fatalf("dials = %+v, want a second dial without the handle", recs)
	}
	p := h.log.payloads("voice.session_refreshed")
	if len(p) != 1 || p[0]["reason"] != refreshAge {
		t.Fatalf("voice.session_refreshed = %v", p)
	}
	if age, _ := p[0]["age_s"].(int); age < int(refreshMaxAge.Seconds()) {
		t.Errorf("age_s = %v, want >= %v", p[0]["age_s"], refreshMaxAge.Seconds())
	}
	seed := c2.waitSent(t, "reseed", isContent("Shepherd: and docs?"))
	text := seed.content.Turns[0].Parts[0].Text
	if strings.Contains(text, "On it") {
		t.Errorf("the reseed kept a bare acknowledgement:\n%s", text)
	}
	if !strings.Contains(text, "You: Docs finished the readme.") {
		t.Errorf("the reseed lost the voice's answer:\n%s", text)
	}
	if got := h.log.payloads("voice.handle_dropped"); len(got) != 1 || got[0]["reason"] != "session_refresh" {
		t.Errorf("voice.handle_dropped = %v", got)
	}
}

// TestRefreshWaitsForAQuietMoment: a due refresh never cuts the model
// mid-reply, nor an open question the supervisor reports; it goes once
// the turn completes and nothing is open.
func TestRefreshWaitsForAQuietMoment(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{}, d)
	open := make(chan bool, 1)
	open <- true
	h.s.SetOpenCheck(func(tags map[string]any) bool {
		v := <-open
		open <- v
		return tags == nil && v
	})
	h.backdate(refreshMaxAge + time.Minute)

	c1.serve(outputTranscriptMsg("Auth is running "))
	h.waitEvent("speech", func(ev voice.Event) bool { tr, ok := ev.(voice.EventTranscript); return ok && tr.Dir == voice.DirOut })
	if refreshUntil(h, 200*time.Millisecond) {
		t.Fatal("refreshed while the model was speaking")
	}
	c1.serve(turnCompleteMsg())
	h.waitEvent("turn complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
	if refreshUntil(h, 200*time.Millisecond) {
		t.Fatal("refreshed while a question was open")
	}
	<-open
	open <- false
	if !refreshUntil(h, 2*time.Second) {
		t.Fatal("no refresh once quiet")
	}
	h.waitEvent("fresh connection", isContextLost)
}

// TestRefreshWhenTheClientComesBackAfterHalfAnHour: the phone back after
// more than 30 minutes away gets a fresh session; back after five
// minutes it resumes as before.
func TestRefreshWhenTheClientComesBackAfterHalfAnHour(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{}, d)
	away := func(dur time.Duration) {
		h.s.ClientAttached(false)
		h.s.mu.Lock()
		h.s.clientGoneAt = h.s.clientGoneAt.Add(-dur)
		h.s.freshAt = h.s.freshAt.Add(-dur - time.Minute)
		h.s.mu.Unlock()
		h.s.ClientAttached(true)
	}
	h.s.ClientAttached(false) // as the Switcher tells a new session
	h.s.ClientAttached(true)

	away(5 * time.Minute)
	if refreshUntil(h, 150*time.Millisecond) {
		t.Fatal("refreshed after a five-minute absence")
	}
	away(refreshAway + time.Minute)
	if !refreshUntil(h, 2*time.Second) {
		t.Fatal("no refresh after a 31-minute absence")
	}
	h.waitEvent("fresh connection", isContextLost)
	if p := h.log.payloads("voice.session_refreshed"); len(p) != 1 || p[0]["reason"] != refreshClientAway {
		t.Errorf("voice.session_refreshed = %v", p)
	}
}

// TestRefreshWaitsForAnOwedReply: a to_supervisor call answered
// "pending" holds a refresh until the push carrying its reply went out.
func TestRefreshWaitsForAnOwedReply(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{Handler: func(_ context.Context, _ voice.ToolCall) (voice.ToolResult, error) {
		return voice.ToolResult{Pending: true, Scheduling: voice.UrgencySilent}, nil
	}}, d)
	c1.serve(toolCallMsg("call-1", voice.ToolToSupervisor, map[string]any{"text": "what is auth doing?"}))
	c1.serve(turnCompleteMsg())
	c1.waitSent(t, "pending tool result", func(m sent) bool { return m.kind == "tool" })
	h.backdate(refreshMaxAge + time.Minute)
	if refreshUntil(h, 200*time.Millisecond) {
		t.Fatal("refreshed while a reply was owed")
	}
	if err := h.s.PushTagged("Auth is running the tests.", voice.UrgencyIdle, map[string]any{"call_id": "call-1", voice.TagAnswersUtterance: "utt-1"}); err != nil {
		t.Fatal(err)
	}
	c1.waitSent(t, "reply push", isContent("Auth is running the tests."))
	c1.serve(outputTranscriptMsg("Auth is running the tests."))
	c1.serve(turnCompleteMsg())
	if !refreshUntil(h, 2*time.Second) {
		t.Fatal("no refresh after the reply went out")
	}
}
