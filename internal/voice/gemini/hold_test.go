package gemini

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// TestPushHeldWhileNoClientAndReleasedOnAttach replays 2026-09-24
// 18:22:04: the brain's late reply was pushed (when_idle) 10 s after the
// page closed, and Gemini spoke 12 s of audio to nobody. With no /ws
// client attached a spoken push is held — not sent to the model — and
// goes out, at its urgency, when a client attaches; silent context is
// never held.
func TestPushHeldWhileNoClientAndReleasedOnAttach(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	h.s.ClientAttached(false)

	tags := map[string]any{"push_id": "push-tlvscs-20", "answers_utterance": "utt-tlvscl-19"}
	if err := h.s.PushTagged("Nothing has changed in the last minute.", voice.UrgencyIdle, tags); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Push("docs is idle", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if n := c.count("content"); n != 0 {
		t.Fatalf("pushes sent with no client attached: %s", describe(c.sentMsgs()))
	}
	held := h.log.payloads("voice.push_held")
	if len(held) != 1 || held[0]["push_id"] != "push-tlvscs-20" || held[0]["reason"] != "no_listener" {
		t.Fatalf("voice.push_held = %v, want one for push-tlvscs-20 (no_listener)", held)
	}
	if cl := h.log.payloads("voice.client"); len(cl) != 1 || cl[0]["attached"] != false {
		t.Errorf("voice.client = %v, want one attached:false", cl)
	}

	h.s.ClientAttached(true)
	m := c.waitSent(t, "released push", isContent("Nothing has changed in the last minute."))
	if !*m.content.TurnComplete {
		t.Error("a released spoken push must complete the turn")
	}
	c.waitSent(t, "silent push after it (FIFO)", isContent("docs is idle"))
	rel := h.log.payloads("voice.push_released")
	if len(rel) != 1 || rel[0]["push_id"] != "push-tlvscs-20" {
		t.Fatalf("voice.push_released = %v, want one for push-tlvscs-20", rel)
	}
	if _, ok := rel[0]["held_ms"]; !ok {
		t.Errorf("voice.push_released lacks held_ms: %v", rel[0])
	}
	pushes := h.log.payloads("voice.push")
	if len(pushes) != 2 || pushes[0]["scheduling_sent"] != "when_idle" || pushes[1]["scheduling_sent"] != "silent" {
		t.Errorf("voice.push = %v, want the when_idle answer then the silent line", pushes)
	}
	if len(h.log.payloads("voice.push_held")) != 1 {
		t.Error("an item is logged as held once, not on every wake")
	}
}

// TestSilentPushNotHeldWithoutClient: context the model never speaks
// needs no listener.
func TestSilentPushNotHeldWithoutClient(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	h.s.ClientAttached(false)
	if err := h.s.Push("docs is idle", voice.UrgencySilent); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "silent push", isContent("docs is idle"))
	if h.log.has("voice.push_held") {
		t.Error("a silent push was held")
	}
}

// TestHeldPushDroppedAfterTTL: a spoken push nobody attaches for within
// heldPushTTL is dropped and logged, like GPT-Live's standby queue.
func TestHeldPushDroppedAfterTTL(t *testing.T) {
	log := &recLogger{}
	s, err := newSession(Config{APIKeys: []string{"k"}}, log, &fakeDialer{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 18, 22, 4, 0, time.UTC)
	s.now = func() time.Time { return now }
	s.noClient = true
	s.pushQ = []pushItem{
		{text: "old", urgency: voice.UrgencyIdle, requested: voice.UrgencyIdle, at: now},
		{text: "new", urgency: voice.UrgencyIdle, requested: voice.UrgencyIdle, at: now.Add(4 * time.Minute)},
	}
	p := s.pickPush()
	if p.ok || p.wait != heldPushTTL || len(p.held) != 1 {
		t.Fatalf("pickPush = ok %v, wait %v, held %d; want the head held, wait %v", p.ok, p.wait, len(p.held), heldPushTTL)
	}
	now = now.Add(heldPushTTL)
	p = s.pickPush()
	s.logPick(p)
	if p.ok || len(p.dropped) != 1 {
		t.Fatalf("expired head: pickPush = ok %v, dropped %d; want the old one dropped", p.ok, len(p.dropped))
	}
	if len(s.pushQ) != 1 || s.pushQ[0].text != "new" {
		t.Fatalf("queue after drop = %v, want only \"new\"", s.pushQ)
	}
	if d := log.payloads("voice.push_dropped"); len(d) != 1 || d[0]["text"] != "old" || d[0]["reason"] != "no_listener" {
		t.Errorf("voice.push_dropped = %v", d)
	}
	if p.wait != 4*time.Minute || len(p.held) != 1 || p.held[0].text != "new" {
		t.Errorf("new head: wait %v, newly held %v; want 4m left, \"new\" held", p.wait, p.held)
	}
}
