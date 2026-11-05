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

func TestInterruptedItemRejectsLateDeltas(t *testing.T) {
	s, d, _ := startSession(t, testConfig())
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open", s.Live)
	c := d.last()
	c.server(map[string]any{"type": evOutputItemAdded, "item": map[string]any{"id": "item_old", "role": "assistant"}})
	c.server(map[string]any{"type": evOutputAudioDelta, "item_id": "item_old", "content_index": 2, "delta": b64(tone(4000, 24000))})
	c.server(map[string]any{"type": evOutputTranscript, "item_id": "item_old", "content_index": 2, "delta": "old reply"})
	waitFor(t, "reply", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.outText == "old reply" })
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.interrupt("user")
	c.server(map[string]any{"type": evOutputTranscript, "item_id": "item_old", "content_index": 2, "delta": "zombie"})
	time.Sleep(50 * time.Millisecond)
	s.mu.Lock()
	got := s.outText
	s.mu.Unlock()
	if got != "" {
		t.Fatalf("late delta accepted: %q", got)
	}
	s.ClientPlayback(voice.PlaybackFlushed, 100)
	waitFor(t, "truncate", func() bool { return len(c.events(evItemTruncate)) > 0 })
	if got := c.events(evItemTruncate)[0]["content_index"]; got != float64(2) {
		t.Fatalf("content index = %v", got)
	}
}

func TestGPTLiveLateFlushedPreservesNewTurnAndUtterance(t *testing.T) {
	s, d, _ := startSession(t, testConfig())
	c := openWithReply(t, s, d, []string{"hi"}, 50)
	s.ClientPlayback(voice.PlaybackDrained, 50)
	waitFor(t, "initial turn over", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.speakingNow
	})

	// Run 1 answers utt-1 and is interrupted by the user (not a push).
	_ = s.PushTagged("Checking the auth failure right now", voice.UrgencyIdle, map[string]any{"utterance_id": "utt-1"})
	waitFor(t, "push 1 sent", func() bool { return len(c.events(evCommentaryAppend)) == 1 })
	for _, w := range strings.Fields("Checking the auth failure right now") {
		c.server(map[string]any{"type": evOutputAudioDelta, "item_id": "it_run1", "delta": b64(tone(4000, 100*24))})
		c.server(map[string]any{"type": evOutputTranscript, "item_id": "it_run1", "delta": w + " "})
	}
	waitFor(t, "run 1 text", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return strings.Contains(s.outText, "now")
	})
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.interrupt("user")

	// Before Run 1's PlaybackFlushed arrives, Run 2 (an urgent push answering
	// utt-2) interrupts, starts playing, and finishes a short reply ("Checking
	// failure") whose text is a substring of Run 1.
	_ = s.PushTagged("Checking failure", voice.UrgencyInterrupt, map[string]any{"utterance_id": "utt-2", "needs_user": true})
	waitFor(t, "push 2 sent", func() bool { return len(c.events(evInstructionsAppend)) == 1 })
	for _, w := range []string{"Checking", "failure"} {
		c.server(map[string]any{"type": evOutputAudioDelta, "item_id": "it_run2", "delta": b64(tone(4000, 100*24))})
		c.server(map[string]any{"type": evOutputTranscript, "item_id": "it_run2", "delta": w + " "})
	}
	waitFor(t, "run 2 finished", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.speakingNow && s.outText == "" && s.recap.Len() >= 2
	})
	s.ClientPlayback(voice.PlaybackStarted, 0)

	// Drain earlier events, then deliver Run 1's late PlaybackFlushed.
	for {
		select {
		case <-s.Events():
		default:
			goto drained
		}
	}
drained:
	s.ClientPlayback(voice.PlaybackFlushed, 200)

	var cut voice.EventTranscript
	waitFor(t, "cut transcript for run 1", func() bool {
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
	if cut.Cut.UtteranceID != "utt-1" {
		t.Fatalf("cut UtteranceID = %q, want %q", cut.Cut.UtteranceID, "utt-1")
	}
	if cut.Cut.ByPush {
		t.Fatalf("cut ByPush = true, want false (run 1 was cut by user)")
	}
	entries := s.recap.Entries()
	last := entries[len(entries)-1]
	if last.Text != "Checking failure" || last.Cut {
		t.Fatalf("newer reply was corrupted by late cut: entries=%+v", entries)
	}
}

func TestGPTLiveInterruptDeltaRacesAndFirstTurnTruncate(t *testing.T) {
	t.Run("zero_byte_item_rejects_late_deltas_after_flushed", func(t *testing.T) {
		s, d, _ := startSession(t, testConfig())
		s.ClientAttached(true)
		_ = s.SendAudio(tone(3000, 640))
		waitFor(t, "open", s.Live)
		c := d.last()
		c.server(map[string]any{"type": evOutputItemAdded, "item": map[string]any{"id": "item_zero", "role": "assistant"}})
		c.server(map[string]any{"type": evOutputTranscript, "item_id": "item_zero", "content_index": 0, "delta": "early words"})
		waitFor(t, "early transcript", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.outText == "early words"
		})
		s.ClientPlayback(voice.PlaybackStarted, 0)
		s.interrupt("user")
		s.ClientPlayback(voice.PlaybackFlushed, 0)

		// Late audio and transcript deltas for item_zero arrive after flushed popped awaitingItems.
		c.server(map[string]any{"type": evOutputAudioDelta, "item_id": "item_zero", "content_index": 0, "delta": b64(tone(4000, 4800))})
		c.server(map[string]any{"type": evOutputTranscript, "item_id": "item_zero", "content_index": 0, "delta": "zombie"})
		time.Sleep(40 * time.Millisecond)
		s.mu.Lock()
		gotText := s.outText
		s.mu.Unlock()
		if gotText != "" {
			t.Fatalf("late delta resurrected zero-byte interrupted item: %q", gotText)
		}
	})

	t.Run("two_interrupts_reject_older_item_deltas", func(t *testing.T) {
		s, d, _ := startSession(t, testConfig())
		s.ClientAttached(true)
		_ = s.SendAudio(tone(3000, 640))
		waitFor(t, "open", s.Live)
		c := d.last()
		for _, id := range []string{"item_1", "item_2"} {
			c.server(map[string]any{"type": evOutputAudioDelta, "item_id": id, "content_index": 0, "delta": b64(tone(4000, 4800))})
			c.server(map[string]any{"type": evOutputTranscript, "item_id": id, "content_index": 0, "delta": id})
			waitFor(t, id, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.outText == id })
			s.ClientPlayback(voice.PlaybackStarted, 0)
			s.interrupt("user")
			s.ClientPlayback(voice.PlaybackFlushed, 50)
		}
		waitFor(t, "both truncated", func() bool { return len(c.events(evItemTruncate)) == 2 })

		c.server(map[string]any{"type": evOutputTranscript, "item_id": "item_1", "content_index": 0, "delta": "zombie1"})
		time.Sleep(40 * time.Millisecond)
		s.mu.Lock()
		gotText := s.outText
		s.mu.Unlock()
		if gotText != "" {
			t.Fatalf("older interrupted item_1 accepted after item_2 truncated: %q", gotText)
		}
	})

	t.Run("first_turn_interrupt_before_started_uses_flushed_report", func(t *testing.T) {
		s, d, _ := startSession(t, testConfig())
		s.ClientAttached(true)
		_ = s.SendAudio(tone(3000, 640))
		waitFor(t, "open", s.Live)
		c := d.last()

		c.server(map[string]any{"type": evOutputAudioDelta, "item_id": "item_first", "content_index": 0, "delta": b64(tone(4000, 600*24))})
		c.server(map[string]any{"type": evOutputTranscript, "item_id": "item_first", "content_index": 0, "delta": "first reply words"})
		waitFor(t, "first reply", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.outText == "first reply words"
		})
		time.Sleep(80 * time.Millisecond)
		// Interrupt before any PlaybackStarted report arrived on Turn 1.
		s.interrupt("push")
		// Client answers flush with played_ms: 0.
		s.ClientPlayback(voice.PlaybackFlushed, 0)
		waitFor(t, "truncate", func() bool { return len(c.events(evItemTruncate)) >= 1 })
		evs := c.events(evItemTruncate)
		if len(evs) != 1 || evs[0]["audio_end_ms"] != float64(0) {
			t.Fatalf("truncate events = %v, want single truncate at audio_end_ms=0", evs)
		}
	})

	t.Run("reportless_client_falls_back_to_wall_clock_truncate", func(t *testing.T) {
		s, d, _ := startSession(t, testConfig())
		s.ClientAttached(true)
		_ = s.SendAudio(tone(3000, 640))
		waitFor(t, "open", s.Live)
		c := d.last()

		c.server(map[string]any{"type": evOutputAudioDelta, "item_id": "item_headless", "content_index": 0, "delta": b64(tone(4000, 600*24))})
		c.server(map[string]any{"type": evOutputTranscript, "item_id": "item_headless", "content_index": 0, "delta": "headless words"})
		waitFor(t, "headless reply", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.outText == "headless words"
		})
		time.Sleep(60 * time.Millisecond)
		s.interrupt("push")
		waitFor(t, "fallback truncate", func() bool { return len(c.events(evItemTruncate)) == 1 })
		endMs, _ := c.events(evItemTruncate)[0]["audio_end_ms"].(float64)
		if endMs <= 0 || endMs >= 600 {
			t.Fatalf("fallback audio_end_ms = %v, want in (0, 600)", endMs)
		}
	})
}
