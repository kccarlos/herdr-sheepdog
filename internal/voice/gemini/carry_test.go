package gemini

import (
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// TestSeedRecapFromGPTLiveIsSentSilently: a switch from GPT-Live hands
// Gemini the recent conversation; it goes out at once on the live
// connection as silent context that does not complete the turn, says
// the voice took the conversation over, and stays in the recap for a
// later re-seed. RecapEntries hands back what was said, the reply in
// progress included.
func TestSeedRecapFromGPTLiveIsSentSilently(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	h.s.SeedRecap([]voice.RecapEntry{
		{Role: voice.RecapUser, Text: "why did the auth tests fail?"},
		{Role: voice.RecapAssistant, Text: "Two tests time out on the token refresh."},
	})
	m := c.waitSent(t, "carried recap", isContent("taken over this voice conversation"))
	text := m.content.Turns[0].Parts[0].Text
	if !strings.Contains(text, "Shepherd: why did the auth tests fail?\nYou: Two tests time out on the token refresh.") {
		t.Errorf("recap = %q", text)
	}
	if *m.content.TurnComplete {
		t.Error("the carried recap completed the turn (the model would answer it)")
	}
	if p := h.log.payloads("voice.reseeded"); len(p) != 1 || p[0]["reason"] != "provider_switch" || p[0]["items"] != 2 {
		t.Errorf("voice.reseeded = %v", p)
	}
	h.s.mu.Lock()
	h.s.outText = "Docs is idle"
	h.s.mu.Unlock()
	got := voice.RecapLines(h.s.RecapEntries())
	if !strings.HasSuffix(got, "You: Two tests time out on the token refresh.\nYou: Docs is idle") {
		t.Errorf("entries = %q", got)
	}
}
