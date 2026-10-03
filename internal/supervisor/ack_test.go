package supervisor

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The logged sequences (docs/bench/tier2-2026-10-03.md #1): the voice
// acknowledged the shepherd's words without calling to_supervisor, and
// the relay watch took the acknowledgement for an answer. An
// acknowledgement is no answer: the daemon relays the words.
func TestAcknowledgementIsNotAnAnswer(t *testing.T) {
	cases := []struct {
		name  string
		heard string
		said  []string
	}{
		{"approve-claude-permission r3", "Yes, make the edit.", []string{"On ", "it."}},
		{"status-en r1", "What's going on?", []string{"I'll check on the agents ", "for you right now."}},
		{"a deflection", "What is bench-claude doing?", []string{"I don't have an update on bench-claude's ", "specific task at the moment."}},
		{"chinese", "现在情况怎么样？", []string{"好的，我查", "一下。"}},
		{"chinese wait", "让它跑测试。", []string{"稍等。"}},
		{"a bare okay", "Tell it to also update the docs.", []string{"Okay."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Bench-claude is editing the file.", "")})
			h := newBrainHarness(t, m, relayAfter(30*time.Millisecond))
			h.addAgent("w5:p1", "bench-claude", "claude", herdr.StateWorking)
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: tc.heard})
			for _, f := range tc.said {
				h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: f})
			}
			h.sup.OnVoiceEvent(voice.EventTurnComplete{})
			waitFor(t, func() bool { return h.log.count("supervisor.relay_by_daemon") == 1 }, "the daemon's relay")
			h.sup.replies.Wait()
			if ev := h.log.ofType("supervisor.ack_not_answer"); len(ev) != 1 || payload(ev[0])["text"] != tc.heard {
				t.Errorf("ack_not_answer = %v", ev)
			}
			if text := lastCall(t, m); !strings.Contains(text, `utterance="`+tc.heard+`"`) {
				t.Errorf("model saw:\n%s", text)
			}
		})
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

func TestAckOnly(t *testing.T) {
	for said, want := range map[string]bool{
		"On it.":    true,
		"Checking.": true,
		"I'll check on the agents for you right now.": true,
		"Let me look into that.":                      true,
		"One moment.":                                 true,
		"Okay.":                                       true,
		"好的。":                                         true,
		"我看一下。":                                       true,
		"收到，马上。":                                      true,
		"不太清楚。":                                       true,
		"<no speech detected>":                        false,
		"":                                            false,
		"Doing well, thanks for asking!":              false,
		"Okay, bench-claude finished the changelog and the tests pass.":                                          false,
		"Sure, here is what I can tell you about the weather today, it looks sunny and warm all afternoon long.": false,
		"你好，今天天气不错。": false,
	} {
		if got := ackOnly(said); got != want {
			t.Errorf("ackOnly(%q) = %v, want %v", said, got, want)
		}
	}
}
