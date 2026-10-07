package supervisor

import (
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// 2026-10-02 23:55:05 (docs/history/bench-2026-10-03.md #11): the user said
// "结果。" and Gemini's only output was "<no speech detected>", in two
// fragments, with 0.2 ms of audio. That is no answer: the daemon relays
// the words. A real answer still keeps them from being relayed, and so
// does an answer the client played normally; one it played for under
// 50 ms does not.
func TestNoSpeechOutputIsNotAnAnswer(t *testing.T) {
	cases := []struct {
		name     string
		out      []string
		playback *voice.PlaybackReport // RunMs; At is set after the answer
		relay    bool
	}{
		{"no speech placeholder", []string{"<no speech ", "detected>"}, nil, true},
		{"placeholder with silence played", []string{"<no speech detected>"}, &voice.PlaybackReport{State: voice.PlaybackDrained, RunMs: 0}, true},
		{"real answer", []string{"auth 还在跑测试,快跑完了。"}, nil, false},
		{"real answer played", []string{"Auth is running the tests."}, &voice.PlaybackReport{State: voice.PlaybackDrained, RunMs: 700}, false},
		{"answer with under 50 ms of audio", []string{"Auth is running the tests."}, &voice.PlaybackReport{State: voice.PlaybackDrained, RunMs: 20}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, relayAfter(60*time.Millisecond))
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "结果。"})
			for _, f := range tc.out {
				h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: f})
			}
			h.sup.OnVoiceEvent(voice.EventTurnComplete{})
			if tc.playback != nil {
				r := *tc.playback
				r.At = h.clock.Now().Add(300 * time.Millisecond)
				h.pusher.setPlayback(r)
			}
			time.Sleep(200 * time.Millisecond)
			h.sup.replies.Wait()
			if got := h.log.count("supervisor.relay_by_daemon") == 1; got != tc.relay {
				t.Errorf("relay_by_daemon = %d, want relayed=%v", h.log.count("supervisor.relay_by_daemon"), tc.relay)
			}
		})
	}
}

func TestNoSpeech(t *testing.T) {
	for text, want := range map[string]bool{
		"<no speech detected>": true,
		"<no speech ":          true,
		"  ":                   true,
		"<noise> ...":          true,
		"on it":                false,
		"<pause> 好的":           false,
	} {
		if got := noSpeech(text); got != want {
			t.Errorf("noSpeech(%q) = %v, want %v", text, got, want)
		}
	}
}
