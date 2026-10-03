package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// docs/eval-campaign-2026-10-03.md H6, j4-finish-finding r2 (B-deepseek):
// the CEO said "Yes, let me know when it's done." to the brain's offer;
// a second later the finish line "Want it to fix those two links?" went
// to the voice, and the brain took the yes for that newer question. The
// utterance request now says when the words began and which lines the
// voice was given after that.
func TestUtteranceRequestSaysWhatTheCEOHadNotHeard(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("I'll tell you when it's done.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "bench-claude", "claude", herdr.StateWorking)
	const offer = "Bench-claude has written the README and is now checking the links in it. Shall I let you know when it finishes?"
	const finish = "The README is done. While checking it, bench-claude spotted two dead links in the contributing guide. Want it to fix those two links?"
	h.sup.OnVoiceEvent(voice.EventPush{Text: offer, Urgency: voice.UrgencyIdle})
	h.clock.Advance(4 * time.Second)
	h.hear("Yes, let me know when it's done.")
	h.clock.Advance(time.Second)
	h.sup.OnVoiceEvent(voice.EventPush{Text: finish, Urgency: voice.UrgencyIdle})
	h.clock.Advance(time.Second)
	h.relay("Yes, let me know when it's done.")
	calls := m.Calls()
	if len(calls) == 0 {
		t.Fatal("no brain turn")
	}
	text := calls[0].Text()
	for _, want := range []string{
		"The CEO began saying these words at",
		"The last line the voice was given before that",
		offer,
		"Given to the voice after they began, so not heard when they spoke: [",
		"1 s after] \"" + finish + "\"",
		"Their words answer what they had heard, not these",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("request lacks %q:\n%s", want, text)
		}
	}
}

// With nothing given to the voice after the CEO began, the request says
// nothing extra: the transcript's order is already right.
func TestUtteranceRequestQuietWhenNothingNewerWasPushed(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("It's still checking links.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "bench-claude", "claude", herdr.StateWorking)
	h.sup.OnVoiceEvent(voice.EventPush{Text: "Bench-claude is checking links.", Urgency: voice.UrgencyIdle})
	h.clock.Advance(3 * time.Second)
	h.say("How's the README coming along?")
	if calls := m.Calls(); len(calls) == 0 || strings.Contains(calls[0].Text(), "began saying these words") {
		t.Fatalf("calls = %d, request:\n%s", len(calls), calls[0].Text())
	}
}

// A push the voice layer judged unheard is marked so in the facts.
func TestHeardFactsMarkUnheardPushes(t *testing.T) {
	h := newHarness(t)
	h.sup.OnVoiceEvent(voice.EventPush{Text: "auth asks to run go test.", Urgency: voice.UrgencyIdle})
	h.sup.OnVoiceEvent(voice.EventPushUnheard{Push: voice.UnheardPush{Text: "auth asks to run go test.", Reason: voice.UnheardUserTurn}})
	start := h.clock.Now().Add(time.Second)
	h.clock.Advance(2 * time.Second)
	h.sup.OnVoiceEvent(voice.EventPush{Text: "docs finished.", Urgency: voice.UrgencyIdle})
	f := h.sup.heardFacts(start)
	if f == nil || f.Before == nil || !f.Before.Unheard || len(f.After) != 1 || f.After[0].Text != "docs finished." || f.After[0].Unheard {
		t.Fatalf("facts = %+v", f)
	}
}
