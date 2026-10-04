package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// echoSaid is the line the iOS speaker played on 2026-09-24 18:23:50,
// as the voice's output transcript fragments came in.
var echoSaid = []string{"No change, all ", "four are idle", " and nothing is broken.", " The question I asked", " a minute ago still"}

// speak feeds the voice's output transcript fragments, then the model
// interrupting itself on the echo (voice.interrupted by:model).
func speak(h *harness, fragments []string) {
	for _, f := range fragments {
		h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: f})
	}
	h.sup.OnVoiceEvent(voice.EventInterrupted{})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
}

// The echo text guard (#48, #63, #108) is gone (dual-brain ablation G6):
// the words reach the brain with the fact that they began just after the
// voice's own line played, and what that line was; the brain judges
// whether they are the CEO.
func TestWordsAfterTheVoiceLineCarryTheEchoFact(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Still no change.", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	speak(h, echoSaid)
	h.clock.Advance(1700 * time.Millisecond)
	h.say("new change")
	if text := lastCall(t, m); !strings.Contains(text, "began while the phone was still playing the voice's own line") || !strings.Contains(text, "No change, all four are idle") {
		t.Errorf("the brain did not get the echo fact:\n%s", text)
	}
	if n := h.log.count("supervisor.echo_possible"); n != 1 {
		t.Errorf("echo_possible = %d", n)
	}
}

// Words said well after the line, or before the voice spoke, carry no
// such fact.
func TestWordsOutsideTheEchoWindowCarryNoFact(t *testing.T) {
	m := braintest.NewModel()
	m.Default = &braintest.Step{Text: braintest.Reply("ok.", "")}
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	speak(h, echoSaid)
	h.clock.Advance(10 * time.Second)
	h.say("no change then?")
	if text := lastCall(t, m); strings.Contains(text, "began while the phone was still playing") {
		t.Errorf("fact on words said after the window:\n%s", text)
	}
}
