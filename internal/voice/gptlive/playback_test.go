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

func TestGPTLiveTruncateOnBargeInAndClamp(t *testing.T) {
	t.Run("truncate_at_played_ms", func(t *testing.T) {
		s, d, _ := startSession(t, testConfig())
		s.ClientAttached(true)
		_ = s.SendAudio(tone(3000, 640))
		waitFor(t, "open", s.Live)
		c := d.last()
		words := strings.Fields("one two three four five six seven eight nine ten")
		for _, w := range words {
			c.server(map[string]any{
				"type":          evOutputAudioDelta,
				"item_id":       "item_abc",
				"content_index": 0,
				"delta":         b64(tone(4000, 100*24)),
			})
			c.server(map[string]any{
				"type":          evOutputTranscript,
				"item_id":       "item_abc",
				"content_index": 0,
				"delta":         w + " ",
			})
		}
		waitFor(t, "reply received", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return strings.Contains(s.outText, "ten")
		})
		s.ClientPlayback(voice.PlaybackStarted, 0)
		s.ClientPlayback(voice.PlaybackFlushed, 400)
		waitFor(t, "truncate sent", func() bool {
			return len(c.events("conversation.item.truncate")) == 1
		})
		tr := c.events("conversation.item.truncate")[0]
		if tr["item_id"] != "item_abc" || tr["content_index"] != float64(0) || tr["audio_end_ms"] != float64(400) {
			t.Fatalf("truncate event = %v", tr)
		}
	})

	t.Run("clamp_to_server_audio_ms", func(t *testing.T) {
		s, d, _ := startSession(t, testConfig())
		s.ClientAttached(true)
		_ = s.SendAudio(tone(3000, 640))
		waitFor(t, "open", s.Live)
		c := d.last()
		// Server sends 600 ms for item_short; client reports 950 ms played.
		for _, w := range []string{"alpha", "beta", "gamma"} {
			c.server(map[string]any{
				"type":          evOutputAudioDelta,
				"item_id":       "item_short",
				"content_index": 1,
				"delta":         b64(tone(4000, 200*24)),
			})
			c.server(map[string]any{
				"type":          evOutputTranscript,
				"item_id":       "item_short",
				"content_index": 1,
				"delta":         w + " ",
			})
		}
		waitFor(t, "reply received", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return strings.Contains(s.outText, "gamma")
		})
		s.ClientPlayback(voice.PlaybackStarted, 0)
		s.ClientPlayback(voice.PlaybackFlushed, 950)
		waitFor(t, "truncate sent", func() bool {
			return len(c.events("conversation.item.truncate")) == 1
		})
		tr := c.events("conversation.item.truncate")[0]
		if tr["item_id"] != "item_short" || tr["content_index"] != float64(1) || tr["audio_end_ms"] != float64(600) {
			t.Fatalf("clamped truncate event = %v, want audio_end_ms=600", tr)
		}
	})
}

func TestGPTLiveCutRecapJoinAroundAndNote(t *testing.T) {
	cfg := testConfig()
	cfg.IdleClose = 120 * time.Millisecond
	s, d, _ := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open 1", s.Live)
	c1 := d.last()

	// User speaks first half -> settles.
	c1.server(map[string]any{"type": evInputTranscript, "delta": "check auth"})
	time.Sleep(cfg.SettleWait + 30*time.Millisecond)

	// Assistant starts replying, immediately cut at 0 ms (0-heard).
	for _, w := range []string{"Let", "me", "check", "auth", "now"} {
		c1.server(map[string]any{"type": evOutputAudioDelta, "item_id": "it_1", "delta": b64(tone(4000, 120*24))})
		c1.server(map[string]any{"type": evOutputTranscript, "item_id": "it_1", "delta": w + " "})
	}
	waitFor(t, "first reply", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return strings.Contains(s.outText, "now")
	})
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.ClientPlayback(voice.PlaybackFlushed, 0)

	// User finishes sentence -> settles and joins around dropped 0-heard reply.
	c1.server(map[string]any{"type": evInputTranscript, "delta": "and docs"})
	time.Sleep(cfg.SettleWait + 30*time.Millisecond)

	// Assistant replies, cut 40% in ("Checking auth").
	for _, w := range []string{"Checking", "auth", "and", "docs", "now"} {
		c1.server(map[string]any{"type": evOutputAudioDelta, "item_id": "it_2", "delta": b64(tone(4000, 200*24))})
		c1.server(map[string]any{"type": evOutputTranscript, "item_id": "it_2", "delta": w + " "})
	}
	waitFor(t, "second reply", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return strings.Contains(s.outText, "now")
	})
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.ClientPlayback(voice.PlaybackFlushed, 400)

	// Wait for idle close, then reopen and inspect session.start input items.
	waitFor(t, "idle close", func() bool { return !s.Live() })
	s.SetMuted(true)
	s.SetMuted(false)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open 2", func() bool { return d.count() == 2 && s.Live() })
	c2 := d.last()
	starts := c2.events(evSessionStart)
	if len(starts) != 1 {
		t.Fatalf("session.start events = %d", len(starts))
	}
	sess, _ := starts[0]["session"].(map[string]any)
	rawInput, _ := sess["input"].([]any)
	var roles, texts []string
	for _, it := range rawInput {
		m, _ := it.(map[string]any)
		role, _ := m["role"].(string)
		content, _ := m["content"].([]any)
		var txt string
		if len(content) > 0 {
			p, _ := content[0].(map[string]any)
			txt, _ = p["text"].(string)
		}
		roles = append(roles, role)
		texts = append(texts, txt)
	}
	if len(texts) != 3 {
		t.Fatalf("recap items = %d (%v / %v), want [user, assistant, developer]", len(texts), roles, texts)
	}
	if roles[0] != "user" || texts[0] != "check auth and docs" {
		t.Errorf("item[0] = %s:%q, want user:%q", roles[0], texts[0], "check auth and docs")
	}
	if roles[1] != "assistant" || texts[1] != "Checking auth…" {
		t.Errorf("item[1] = %s:%q, want assistant:%q", roles[1], texts[1], "Checking auth…")
	}
}
