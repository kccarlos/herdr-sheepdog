package gptlive

import (
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// openWithReply opens a session and serves a reply: per word, ms of
// voiced 24 kHz audio followed by its transcript fragment.
func openWithReply(t *testing.T, s *Session, d *fakeDialer, words []string, perWordMs int) *fakeConn {
	t.Helper()
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	for _, w := range words {
		c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, perWordMs*24))})
		c.server(map[string]any{"type": evOutputTranscript, "delta": w + " "})
	}
	waitFor(t, "reply received", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return strings.Contains(s.outText, words[len(words)-1])
	})
	return c
}

// TestGPTLiveInterruptPushWaitsForClientDrained: the model stopped
// generating (its synthesised turn end), but the client is still
// playing: an INTERRUPT push is deferred until the client drains.
func TestGPTLiveInterruptPushWaitsForClientDrained(t *testing.T) {
	s, d, l := startSession(t, testConfig())
	c := openWithReply(t, s, d, []string{"auth", "is", "green"}, 200)
	s.ClientPlayback(voice.PlaybackStarted, 0)
	waitFor(t, "generation over", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.speakingNow
	})
	if !s.Speaking() {
		t.Fatal("Speaking() false while the client plays")
	}
	_ = s.Push("auth crashed", voice.UrgencyInterrupt)
	time.Sleep(100 * time.Millisecond)
	if n := len(c.events(evInstructionsAppend, evCommentaryAppend)); n != 0 {
		t.Fatal("push went out while the client was playing")
	}
	s.ClientPlayback(voice.PlaybackDrained, 600)
	waitFor(t, "deferred push as commentary", func() bool { return len(c.events(evCommentaryAppend)) == 1 })
	if len(c.events(evInstructionsAppend)) != 0 {
		t.Error("the deferred push interrupted")
	}
	if p := l.find("voice.playback"); len(p) != 2 || p[1]["state"] != "drained" || p[1]["run_ms"] != 600 {
		t.Errorf("voice.playback %v", p)
	}
}

// TestGPTLiveCutTranscript: the client flushed 40 % into the reply; the
// session emits the heard words as a cut transcript.
func TestGPTLiveCutTranscript(t *testing.T) {
	s, d, l := startSession(t, testConfig())
	words := strings.Fields("one two three four five six seven eight nine ten")
	openWithReply(t, s, d, words, 100)
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.ClientPlayback(voice.PlaybackFlushed, 400)
	var cut voice.EventTranscript
	waitFor(t, "cut transcript", func() bool {
		for {
			select {
			case ev := <-s.Events():
				if tr, ok := ev.(voice.EventTranscript); ok && tr.Cut != nil {
					cut = tr
					return true
				}
			default:
				return false
			}
		}
	})
	if cut.Text != "one two three four" || cut.Cut.RunMs != 1000 || cut.Cut.HeardMs != 400 {
		t.Fatalf("cut %+v %+v", cut, cut.Cut)
	}
	if p := l.find("voice.playback"); len(p) != 2 || p[1]["cut"] != true {
		t.Errorf("voice.playback %v", p)
	}
}

// TestGPTLivePushWaitsWhileClientSpeaking: the client's speech detector
// holds a push; closing it releases the push after ClientSpeechRelease.
func TestGPTLivePushWaitsWhileClientSpeaking(t *testing.T) {
	s, d, _ := startSession(t, testConfig())
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	s.ClientSpeaking(true)
	_ = s.Push("docs finished", voice.UrgencyIdle)
	time.Sleep(150 * time.Millisecond)
	if len(c.events(evCommentaryAppend)) != 0 {
		t.Fatal("push went out while the client hears the shepherd")
	}
	closed := time.Now()
	s.ClientSpeaking(false)
	waitFor(t, "push after close", func() bool { return len(c.events(evCommentaryAppend)) == 1 })
	if el := time.Since(closed); el < voice.ClientSpeechRelease-10*time.Millisecond {
		t.Errorf("push %v after close, want >= %v", el, voice.ClientSpeechRelease)
	}
}

// The playback start after a reply push carries its utterance_id, once.
func TestGPTLivePlaybackStartTaggedWithReplyUtterance(t *testing.T) {
	s, d, l := startSession(t, testConfig())
	c := openWithReply(t, s, d, []string{"hi"}, 100)
	s.ClientPlayback(voice.PlaybackDrained, 100)
	waitFor(t, "generation over", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.speakingNow
	})
	_ = s.PushTagged("the tests are green", voice.UrgencyIdle, map[string]any{"utterance_id": "utt-7"})
	waitFor(t, "push sent", func() bool { return len(c.events(evCommentaryAppend)) == 1 })
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.ClientPlayback(voice.PlaybackStarted, 0)
	p := l.find("voice.playback")
	if len(p) != 3 || p[1]["utterance_id"] != "utt-7" || p[2]["utterance_id"] != nil {
		t.Fatalf("voice.playback = %v", p)
	}
}
