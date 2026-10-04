package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The fast brain owns the judgment an acknowledgement used to get in
// code (docs/design/dual-brain.md; ackCues removed): whatever the voice
// said is its answer, and the daemon does not second-guess it. Words met
// with neither speech nor a tool call are still relayed (a lost
// utterance, not a judgment of what was said).
func TestVoiceAnswerIsNotSecondGuessed(t *testing.T) {
	h := newHarness(t, relayAfter(30*time.Millisecond))
	h.addAgent("w5:p1", "bench-claude", "claude", herdr.StateWorking)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Yes, make the edit."})
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "On it."})
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	time.Sleep(120 * time.Millisecond)
	h.sup.replies.Wait()
	if n := h.log.count("supervisor.relay_by_daemon"); n != 0 {
		t.Errorf("relay_by_daemon = %d, want 0", n)
	}
}

func TestUnansweredWordsAreStillRelayed(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Bench-claude is editing the file.", "")})
	h := newBrainHarness(t, m, relayAfter(30*time.Millisecond))
	h.addAgent("w5:p1", "bench-claude", "claude", herdr.StateWorking)
	h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "What is bench-claude doing?"})
	waitFor(t, func() bool { return h.log.count("supervisor.relay_by_daemon") == 1 }, "the daemon's relay")
	h.sup.replies.Wait()
	if text := lastCall(t, m); !strings.Contains(text, `utterance="What is bench-claude doing?"`) {
		t.Errorf("model saw:\n%s", text)
	}
}

// A real answer the voice gave itself (small talk) still holds the
// relay, and so does an acknowledgement followed by the tool call.
func TestAnswerOrRelayAfterAcknowledgementHoldsTheWatch(t *testing.T) {
	cases := []struct {
		name  string
		heard string
		after func(h *harness)
	}{
		{"small talk", "Hi, how are you?", func(h *harness) {
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "Doing well, thanks for asking!"})
		}},
		{"ack then tool call", "What's going on?", func(h *harness) {
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: "Checking."})
			h.sup.OnVoiceEvent(voice.EventToolCall{Call: voice.ToolCall{ID: "g1", Name: ToolToSupervisor}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, relayAfter(30*time.Millisecond))
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: tc.heard})
			tc.after(h)
			time.Sleep(120 * time.Millisecond)
			h.sup.replies.Wait()
			if n := h.log.count("supervisor.relay_by_daemon"); n != 0 {
				t.Errorf("relay_by_daemon = %d, want 0", n)
			}
		})
	}
}
