package gptlive

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// TestPushHeldWhileClientGoneEvenWithSessionOpen: an open (billed)
// session whose client went away must not speak a push to nobody
// (2026-09-24 18:22:04, seen on Gemini; before this fix GPT-Live checked
// for a listener only in standby). The push is held, does not keep the
// billed session open, and is delivered in a fresh session when a
// client attaches again.
func TestPushHeldWhileClientGoneEvenWithSessionOpen(t *testing.T) {
	s, d, l := startSession(t, testConfig())
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	first := d.last()

	s.ClientAttached(false)
	tags := map[string]any{"push_id": "push-tlvscs-20"}
	if err := s.PushTagged("Nothing has changed in the last minute.", voice.UrgencyIdle, tags); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "voice.push_held", func() bool { return len(l.find("voice.push_held")) == 1 })
	if held := l.find("voice.push_held")[0]; held["push_id"] != "push-tlvscs-20" || held["reason"] != "no_listener" {
		t.Fatalf("voice.push_held %v", held)
	}
	waitFor(t, "idle close despite the held push", func() bool { return len(l.find("voice.session_close")) == 1 })
	if n := len(first.events(evCommentaryAppend, evInstructionsAppend)); n != 0 {
		t.Fatalf("push delivered to a session nobody hears: %v", first.events(evCommentaryAppend))
	}

	s.ClientAttached(true)
	waitFor(t, "delivered on attach", func() bool {
		c := d.last()
		return c != first && c != nil && len(c.events(evCommentaryAppend)) == 1
	})
	if got := l.find("voice.push_released"); len(got) != 1 || got[0]["push_id"] != "push-tlvscs-20" || got[0]["held_ms"] == nil {
		t.Fatalf("voice.push_released %v", got)
	}
	if len(l.find("voice.push_held")) != 1 {
		t.Error("held logged more than once for one push")
	}
}

// TestHeldPushDroppedAfterTTLWithSessionOpen: the PushTTL that bounds
// the standby wait bounds the no-listener wait too.
func TestHeldPushDroppedAfterTTLWithSessionOpen(t *testing.T) {
	cfg := testConfig()
	cfg.PushTTL = 60 * time.Millisecond
	cfg.IdleClose = 5 * time.Second
	s, d, l := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	s.ClientAttached(false)
	_ = s.Push("docs finished", voice.UrgencyIdle)
	waitFor(t, "voice.push_dropped", func() bool { return len(l.find("voice.push_dropped")) == 1 })
	if p := l.find("voice.push_dropped")[0]; p["reason"] != "no_listener" || p["text"] != "docs finished" {
		t.Fatalf("voice.push_dropped %v", p)
	}
	if n := len(d.last().events(evCommentaryAppend)); n != 0 {
		t.Fatal("dropped push was delivered")
	}
}
