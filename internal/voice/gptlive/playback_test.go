package gptlive

import (
	"fmt"
	"strings"
	"sync"
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

func TestGPTLiveCutRecapJoinAroundAndNote(t *testing.T) {
	cfg := testConfig()
	cfg.IdleClose = 120 * time.Millisecond
	s, d, _ := startSession(t, cfg)
	s.ClientAttached(true)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "open 1", s.Live)
	c1 := d.last()

	// User speaks first half -> settles.
	c1.server(map[string]any{"type": evInputTranscript, "delta": "check auth", "start_ms": 0, "end_ms": 400})
	time.Sleep(cfg.SettleWait + 30*time.Millisecond)

	// Assistant starts replying, immediately cut at 0 ms (0-heard).
	for _, w := range []string{"Let", "me", "check", "auth", "now"} {
		c1.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 120*24))})
		c1.server(map[string]any{"type": evOutputTranscript, "delta": w + " "})
	}
	waitFor(t, "first reply", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return strings.Contains(s.outText, "now")
	})
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.ClientPlayback(voice.PlaybackFlushed, 0)

	// User finishes sentence -> settles and joins around dropped 0-heard reply.
	c1.server(map[string]any{"type": evInputTranscript, "delta": "and docs", "start_ms": 800, "end_ms": 1200})
	time.Sleep(cfg.SettleWait + 30*time.Millisecond)

	// Assistant replies, cut 40% in ("Checking auth").
	for _, w := range []string{"Checking", "auth", "and", "docs", "now"} {
		c1.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 200*24))})
		c1.server(map[string]any{"type": evOutputTranscript, "delta": w + " "})
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
		c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 100*24))})
		c.server(map[string]any{"type": evOutputTranscript, "delta": w + " "})
	}
	waitFor(t, "run 1 text", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return strings.Contains(s.outText, "now")
	})
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.interrupt("user")

	// Before Run 1's PlaybackFlushed arrives, Run 2 (an urgent push answering
	// utt-2) interrupts, retires the speaking connection, starts playing on the
	// fresh session, and finishes a short reply ("Checking failure") whose text
	// is a substring of Run 1.
	_ = s.PushTagged("Checking failure", voice.UrgencyInterrupt, map[string]any{"utterance_id": "utt-2", "needs_user": true})
	waitFor(t, "push 2 sent", func() bool {
		return d.count() == 2 && len(d.last().events(evInstructionsAppend)) == 1
	})
	c2 := d.last()
	for _, w := range []string{"Checking", "failure"} {
		c2.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 100*24))})
		c2.server(map[string]any{"type": evOutputTranscript, "delta": w + " "})
	}
	waitFor(t, "run 2 finished", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.speakingNow && s.outText == "" && s.recap.Len() >= 3
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

func TestGPTLiveRealWireInterruptDropsStaleSpeech(t *testing.T) {
	t.Run("urgent_push_does_not_leak_post_instructions_appended_speech", func(t *testing.T) {
		d := &fakeDialer{}
		l := &recLog{}
		s, err := newSession(testConfig(), l, d)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })

		var chunks []voice.AudioChunk
		go func() {
			for ch := range s.AudioChunks() {
				s.mu.Lock()
				chunks = append(chunks, ch)
				s.mu.Unlock()
			}
		}()

		s.ClientAttached(true)
		_ = s.SendAudio(tone(3000, 640))
		waitFor(t, "open", s.Live)
		c1 := d.last()
		c1.mu.Lock()
		c1.holdAppendAck = true
		c1.mu.Unlock()

		// Real GPT-Live wire shapes carry delta/start_ms/end_ms and no item_id.
		c1.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 500*24))})
		c1.server(map[string]any{"type": evOutputTranscript, "delta": "old reply ", "start_ms": 0, "end_ms": 500})
		waitFor(t, "old reply", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.outText == "old reply "
		})
		s.ClientPlayback(voice.PlaybackStarted, 0)

		// Urgent push interrupts active speech.
		if err := s.PushTagged("urgent alert", voice.UrgencyInterrupt, map[string]any{"needs_user": true}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "urgent push dispatched", func() bool {
			return len(d.last().events(evInstructionsAppend, evCommentaryAppend)) >= 1
		})

		// Even if c1 acknowledges session.instructions.appended and THEN keeps
		// streaming pre-cancellation speech (because session.instructions.appended
		// only acknowledges timeline insertion), none of c1's continuing speech
		// may pass into the new epoch.
		eventID := "push_1"
		if ins := c1.events(evInstructionsAppend); len(ins) > 0 {
			eventID, _ = ins[0]["event_id"].(string)
		}
		c1.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 200*24))})
		c1.server(map[string]any{"type": evOutputTranscript, "delta": "stale before ack ", "start_ms": 500, "end_ms": 700})
		c1.server(map[string]any{"type": evInstructionsApplied, "client_event_id": eventID, "start_ms": 700, "end_ms": 700})
		c1.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 200*24))})
		c1.server(map[string]any{"type": evOutputTranscript, "delta": "stale after ack ", "start_ms": 700, "end_ms": 900})

		active := d.last()
		active.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 300*24))})
		active.server(map[string]any{"type": evOutputTranscript, "delta": "urgent alert", "start_ms": 900, "end_ms": 1200})

		waitFor(t, "new reply", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return strings.Contains(s.outText, "urgent alert")
		})

		s.ClientPlayback(voice.PlaybackFlushed, 200)

		s.mu.Lock()
		gotText := s.outText
		gotChunks := append([]voice.AudioChunk(nil), chunks...)
		s.mu.Unlock()
		if gotText != "urgent alert" {
			t.Fatalf("outText = %q, want %q (post-ack stale speech leaked into new epoch)", gotText, "urgent alert")
		}
		if got := c1.events("conversation.item.truncate"); len(got) != 0 {
			t.Fatalf("unexpected conversation.item.truncate on GPT-Live wire: %v", got)
		}
		if len(gotChunks) == 0 {
			t.Fatal("expected new reply audio chunk")
		}
		lastEpoch := gotChunks[len(gotChunks)-1].Epoch
		var newEpochBytes int
		for _, ch := range gotChunks {
			if ch.Epoch == lastEpoch {
				newEpochBytes += len(ch.Data)
			}
		}
		if newEpochBytes != 300*24*2 {
			t.Fatalf("new epoch audio bytes = %d, want %d (stale audio leaked into new epoch)", newEpochBytes, 300*24*2)
		}
	})

	t.Run("local_interrupt_gates_across_append_acks_and_overlapping_input", func(t *testing.T) {
		s, d, _ := startSession(t, testConfig())
		s.ClientAttached(true)
		_ = s.SendAudio(tone(3000, 640))
		waitFor(t, "open", s.Live)
		c := d.last()

		c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 24000))})
		c.server(map[string]any{"type": evOutputTranscript, "delta": "old reply", "start_ms": 0, "end_ms": 1000})
		waitFor(t, "reply", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.outText == "old reply" })
		s.ClientPlayback(voice.PlaybackStarted, 0)
		s.interrupt("user")
		s.ClientPlayback(voice.PlaybackFlushed, 100)

		// Continuing deltas from the interrupted utterance carry no item_id; even
		// overlapping user input transcripts or append acknowledgements while old
		// speech is still streaming must not open the gate early.
		c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 4800))})
		c.server(map[string]any{"type": evOutputTranscript, "delta": "zombie1 ", "start_ms": 1000, "end_ms": 1200})
		c.server(map[string]any{"type": evInputTranscript, "delta": "what about docs?", "start_ms": 1200, "end_ms": 1400})
		c.server(map[string]any{"type": evCommentaryApplied, "client_event_id": "push_1", "start_ms": 1400, "end_ms": 1400})
		c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 4800))})
		c.server(map[string]any{"type": evOutputTranscript, "delta": "zombie2", "start_ms": 1400, "end_ms": 1600})
		time.Sleep(20 * time.Millisecond)
		s.mu.Lock()
		got := s.outText
		s.mu.Unlock()
		if got != "" {
			t.Fatalf("continuing delta accepted during barge-in overlap: %q", got)
		}
		if d.count() != 1 {
			t.Fatalf("barge-in reconnected (%d sessions), want single open session", d.count())
		}

		// Once the interrupted speech run has gone quiet for SpeechHold, the
		// model's answer to the user's new turn flows on the same connection.
		time.Sleep(testConfig().SpeechHold + 20*time.Millisecond)
		c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 4800))})
		c.server(map[string]any{"type": evOutputTranscript, "delta": "Docs are green.", "start_ms": 1700, "end_ms": 2000})
		waitFor(t, "answer after quiet gap", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.outText == "Docs are green."
		})
	})
}

func TestGPTLiveStopSequenceEmptyFlushAcksDoNotCutNewReply(t *testing.T) {
	s, d, _ := startSession(t, testConfig())
	openWithReply(t, s, d, strings.Fields("Auth is running the long test suite"), 150)
	s.ClientPlayback(voice.PlaybackStarted, 0)

	// iOS/web Stop: client locally flushes first, then daemon afterCancel calls
	// FlushAudio and pushes the stop confirmation with UrgencyInterrupt.
	s.ClientPlayback(voice.PlaybackFlushed, 300)
	_ = s.FlushAudio()
	if err := s.PushTagged("Stopped auth.", voice.UrgencyInterrupt, map[string]any{"stop": true, "needs_user": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop push sent on fresh session", func() bool {
		return d.count() == 2 && len(d.last().events(evInstructionsAppend)) == 1
	})
	c2 := d.last()
	startInput := fmt.Sprint(c2.events(evSessionStart)[0]["session"].(map[string]any)["input"])
	if !strings.Contains(startInput, "Auth is…") || strings.Contains(startInput, "long test suite") {
		t.Fatalf("reopened session input = %s, want trimmed 'Auth is…' recap", startInput)
	}

	// Stop confirmation streams and settles into recap before the two empty
	// flush acknowledgements arrive from the client.
	for _, w := range []string{"Stopped", "auth."} {
		c2.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 200*24))})
		c2.server(map[string]any{"type": evOutputTranscript, "delta": w + " "})
	}
	waitFor(t, "stop reply settled", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.speakingNow && s.outText == "" && s.recap.Len() >= 2
	})

	// Drain earlier events, then deliver both empty flush acks.
	for {
		select {
		case <-s.Events():
		default:
			goto drainedEvents
		}
	}
drainedEvents:
	s.ClientPlayback(voice.PlaybackFlushed, 0)
	s.ClientPlayback(voice.PlaybackFlushed, 0)
	time.Sleep(30 * time.Millisecond)

	for {
		select {
		case ev := <-s.Events():
			if tr, ok := ev.(voice.EventTranscript); ok && tr.Cut != nil {
				t.Fatalf("empty flush ack emitted spurious cut transcript on new reply: %+v (%+v)", tr, tr.Cut)
			}
			if u, ok := ev.(voice.EventPushUnheard); ok {
				t.Fatalf("empty flush ack marked new push unheard: %+v", u)
			}
		default:
			goto checkedEvents
		}
	}
checkedEvents:
	entries := s.recap.Entries()
	if len(entries) == 0 {
		t.Fatal("recap is empty; new reply was deleted by empty flush ack")
	}
	last := entries[len(entries)-1]
	if last.Text != "Stopped auth." || last.Cut {
		t.Fatalf("last recap entry = %+v, want intact 'Stopped auth.' (entries=%+v)", last, entries)
	}
}

func TestGPTLiveCutAfterUnderrunKeepsHeardFirstSentence(t *testing.T) {
	cfg := testConfig()
	cfg.SpeechHold = 500 * time.Millisecond
	s, d, _ := startSession(t, cfg)
	c := openWithReply(t, s, d, []string{"Auth", "passed."}, 200)
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.ClientPlayback(voice.PlaybackDrained, 400)

	// Within SpeechHold (same assistant reply), the model continues speaking,
	// and the user cuts at 0 ms of the resumed audio.
	for _, w := range []string{"Staging", "is", "deploying", "now."} {
		c.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 200*24))})
		c.server(map[string]any{"type": evOutputTranscript, "delta": w + " "})
	}
	waitFor(t, "resumed reply received", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return strings.Contains(s.outText, "now.")
	})
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.ClientPlayback(voice.PlaybackFlushed, 0)

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
	if cut.Text != "Auth passed." || cut.Cut.HeardMs != 400 {
		t.Fatalf("cut = %+v (%+v), want Text='Auth passed.' HeardMs=400", cut, cut.Cut)
	}
	if cut.Cut.Full != "Auth passed. Staging is deploying now." {
		t.Fatalf("cut.Full = %q, want full reply", cut.Cut.Full)
	}
	entries := s.recap.Entries()
	if len(entries) != 1 || entries[0].Text != "Auth passed." || !entries[0].Cut || entries[0].CutAfterMs != 400 {
		t.Fatalf("recap entries = %+v, want single cut entry 'Auth passed.' at 400ms", entries)
	}
}

func TestGPTLiveClientFlushDoesNotCreateUntrackedDaemonFlush(t *testing.T) {
	cfg := testConfig()
	cfg.IdleClose = 120 * time.Millisecond
	d := &fakeDialer{}
	l := &recLog{}
	s, err := newSession(cfg, l, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var (
		flushedEpoch  uint64
		daemonFlushes int
		mu            sync.Mutex
	)
	noteBarrier := func(ep uint64) {
		mu.Lock()
		if ep > flushedEpoch {
			flushedEpoch = ep
			daemonFlushes++
		}
		mu.Unlock()
	}
	go func() {
		for ch := range s.AudioChunks() {
			for _, ep := range ch.FlushedEpochs {
				noteBarrier(ep)
			}
		}
	}()

	c1 := openWithReply(t, s, d, []string{"Auth", "is", "running", "tests."}, 150)
	s.ClientPlayback(voice.PlaybackStarted, 0)
	s.ClientPlayback(voice.PlaybackFlushed, 300)

	// After the interrupted reply goes quiet, the user asks a follow-up on c1
	// and gets a new reply without any daemon-initiated flush.
	time.Sleep(cfg.SpeechHold + 20*time.Millisecond)
	c1.server(map[string]any{"type": evInputTranscript, "delta": "How is docs?"})
	c1.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 300*24))})
	c1.server(map[string]any{"type": evOutputTranscript, "delta": "Docs is idle."})
	waitFor(t, "follow-up reply", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.outText == "Docs is idle."
	})
	// Idle close (finishDetach) and reopen must not attach an untracked flush barrier either.
	waitFor(t, "idle close", func() bool { return !s.Live() })
	s.SetMuted(true)
	s.SetMuted(false)
	_ = s.SendAudio(tone(3000, 640))
	waitFor(t, "reopen", func() bool { return d.count() == 2 && s.Live() })
	c2 := d.last()
	c2.server(map[string]any{"type": evOutputAudioDelta, "delta": b64(tone(4000, 300*24))})
	c2.server(map[string]any{"type": evOutputTranscript, "delta": "All green."})
	waitFor(t, "reopened reply", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.outText == "All green."
	})
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	gotFlushes := daemonFlushes
	mu.Unlock()
	if gotFlushes != 0 {
		t.Fatalf("daemon flush barriers emitted = %d, want 0 (client flush or detach created an untracked barrier)", gotFlushes)
	}
}
