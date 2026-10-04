package gemini

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The CEO's Gemini Live experiment (2026-10-03): the server sends a new
// handle after every completed turn, a resume continues from the context
// at that handle, and silent context sent after it is lost. So: every
// handle update is logged with the prompt size at issue, and on a resume
// the silent pushes sent after the last handle go again; those sent
// before it do not.
func TestResumeResendsSilentContextAfterTheLastHandle(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)

	if err := h.s.Push("The agents are now called: Claude.", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	c1.waitSent(t, "names line", isContent("The agents are now called: Claude."))
	c1.serve(&genai.LiveServerMessage{UsageMetadata: &genai.UsageMetadata{PromptTokenCount: 420}})
	c1.serve(handleMsg("handle-A-0123456789"))
	waitHandleUpdates(t, h, 1)
	// After the handle: a focus line and a briefing, then the drop.
	if err := h.s.PushTagged("Current focus: kvoice Claude.", voice.UrgencySilent, map[string]any{"focus": true}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Push("Briefing: kvoice Claude is idle.", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	c1.waitSent(t, "briefing", isContent("Briefing: kvoice Claude is idle."))
	c1.fail(errors.New("websocket: close 1006 (abnormal closure)"))
	ev := h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if ev.(voice.EventResumed).ContextLost {
		t.Fatal("not resumed")
	}
	c2.waitSent(t, "focus again", isContent("Current focus: kvoice Claude."))
	c2.waitSent(t, "briefing again", isContent("Briefing: kvoice Claude is idle."))
	time.Sleep(20 * time.Millisecond)
	for _, m := range c2.sentMsgs() {
		if isContent("The agents are now called: Claude.")(m) {
			t.Errorf("a line sent before the handle was re-sent: %s", describe(c2.sentMsgs()))
		}
	}
	up := h.log.payloads("voice.handle_update")
	if len(up) != 1 || up[0]["handle"] != "handle-A" || up[0]["resumable"] != true || up[0]["accepted"] != true || up[0]["prompt_tokens"] != int32(420) || up[0]["silent_since"] != 1 {
		t.Errorf("voice.handle_update = %v", up)
	}
	res := h.log.payloads("voice.resume")
	if len(res) != 1 || res[0]["silent_resent"] != 2 {
		t.Errorf("voice.resume = %v", res)
	}
	// The re-sent lines are kept again until the next handle.
	c2.serve(handleMsg("handle-B"))
	waitHandleUpdates(t, h, 2)
	if up := h.log.payloads("voice.handle_update"); up[1]["silent_since"] != 2 {
		t.Errorf("second voice.handle_update = %v", up[1])
	}
}

// A non-resumable update is logged but changes nothing: the silent lines
// after the last accepted handle are still owed.
func TestNonResumableHandleUpdateIsLoggedOnly(t *testing.T) {
	c1 := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c1)}})
	c1.serve(handleMsg("handle-A"))
	waitHandleUpdates(t, h, 1)
	if err := h.s.Push("Briefing.", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	c1.waitSent(t, "briefing", isContent("Briefing."))
	c1.serve(&genai.LiveServerMessage{SessionResumptionUpdate: &genai.LiveServerSessionResumptionUpdate{Resumable: false}})
	waitHandleUpdates(t, h, 2)
	up := h.log.payloads("voice.handle_update")[1]
	if up["accepted"] != false || up["resumable"] != false || up["silent_since"] != 1 {
		t.Errorf("voice.handle_update = %v", up)
	}
	h.s.mu.Lock()
	n, handle := len(h.s.postHandle), h.s.handle
	h.s.mu.Unlock()
	if n != 1 || handle != "handle-A" {
		t.Errorf("after a non-resumable update: %d kept, handle %q", n, handle)
	}
}

// waitHandleUpdates waits for n voice.handle_update events.
func waitHandleUpdates(t *testing.T, h *harness, n int) {
	t.Helper()
	const typ = "voice.handle_update"
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.log.payloads(typ)) >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s: want %d, got %d", typ, n, len(h.log.payloads(typ)))
}
