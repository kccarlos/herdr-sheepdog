package gemini

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// ms of 24 kHz PCM16 mono output audio.
func outAudio(ms int) []byte { return make([]byte, ms*48) }

func isFlushEvent(ev voice.Event) bool { _, ok := ev.(voice.EventInterrupted); return ok }

// speakReply serves a reply of words, each word's audio (perWordMs)
// followed by its transcript fragment, and waits until all arrived.
func speakReply(h *harness, c *fakeConn, words []string, perWordMs int) {
	h.t.Helper()
	for _, w := range words {
		c.serve(audioMsg(outAudio(perWordMs)))
		c.serve(outputTranscriptMsg(w + " "))
	}
	isOut := func(ev voice.Event) bool {
		tr, ok := ev.(voice.EventTranscript)
		return ok && tr.Dir == voice.DirOut && tr.Cut == nil
	}
	deadline := time.Now().Add(3 * time.Second)
	for h.countEvents(isOut) < len(words) {
		if time.Now().After(deadline) {
			h.t.Fatalf("reply transcripts not all seen")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestInterruptPushAfterTurnCompleteWaitsForDrained is P4
// (docs/dev/research/voice-frameworks-2026-09-24.md): Gemini generates ~4×
// real time, so its turnComplete arrives while the client is still
// playing. An INTERRUPT push then must not cut the sentence being heard:
// it is deferred until the client reports drained.
func TestInterruptPushAfterTurnCompleteWaitsForDrained(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{PushGap: 50 * time.Millisecond}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	speakReply(h, c, []string{"Auth", "is", "running", "the", "tests"}, 400)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c.serve(turnCompleteMsg())
	h.waitEvent("turn complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
	if !h.s.Speaking() {
		t.Fatal("Speaking() false while the client is still playing")
	}

	if err := h.s.Push("auth crashed", voice.UrgencyInterrupt); err != nil {
		t.Fatal(err)
	}
	h.waitEvent("push deferred", func(voice.Event) bool { return h.log.has("voice.push_deferred") })
	time.Sleep(100 * time.Millisecond)
	if c.count("content") != 0 {
		t.Fatalf("push sent while the client was playing: %s", describe(c.sentMsgs()))
	}
	if h.countEvents(isFlushEvent) != 0 {
		t.Fatal("the push flushed a reply the shepherd was still hearing")
	}

	drained := time.Now()
	h.s.ClientPlayback(voice.PlaybackDrained, 2000)
	c.waitSent(t, "deferred push after drained", isContent("auth crashed"))
	if el := time.Since(drained); el < voice.PlaybackGapHold {
		t.Errorf("push %v after drained, want >= the %v gap hold", el, voice.PlaybackGapHold)
	}
	p := h.pushesLogged(1)
	if len(p) != 1 || p[0]["scheduling_sent"] != "when_idle" {
		t.Errorf("voice.push = %v, want scheduling_sent when_idle", p)
	}
	if h.countEvents(isFlushEvent) != 0 {
		t.Error("a deferred push must not flush")
	}
	pb := h.log.payloads("voice.playback")
	if len(pb) != 2 || pb[1]["state"] != "drained" || pb[1]["played_ms"] != 2000 || pb[1]["run_ms"] != 2000 {
		t.Errorf("voice.playback = %v", pb)
	}
	if got := h.s.LastPlayback(); got.State != voice.PlaybackDrained || got.PlayedMs != 2000 {
		t.Errorf("LastPlayback = %+v", got)
	}
}

// TestInterruptPushAfterDrainedInterrupts: once the client has drained,
// nothing is playing and an INTERRUPT push goes at once, as INTERRUPT.
func TestInterruptPushAfterDrainedInterrupts(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	speakReply(h, c, []string{"Done."}, 500)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c.serve(turnCompleteMsg())
	h.waitEvent("turn complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
	h.s.ClientPlayback(voice.PlaybackDrained, 500)
	time.Sleep(voice.PlaybackGapHold + 20*time.Millisecond)
	if h.s.Speaking() {
		t.Fatal("Speaking() after drained and the gap hold")
	}

	if err := h.s.Push("auth crashed", voice.UrgencyInterrupt); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "interrupt push", isContent("auth crashed"))
	if h.log.has("voice.push_deferred") {
		t.Error("push deferred with nothing playing")
	}
	p := h.pushesLogged(1)
	if len(p) != 1 || p[0]["scheduling_sent"] != "interrupt" {
		t.Errorf("voice.push = %v, want scheduling_sent interrupt", p)
	}
}

// TestNoPlaybackReportsFallsBackToTimingGuess: an old client that never
// reports playback still gets a reply's audio length before an
// interrupt push cuts in (audio delivered minus wall time).
func TestNoPlaybackReportsFallsBackToTimingGuess(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	speakReply(h, c, []string{"Checking."}, 400)
	c.serve(turnCompleteMsg())
	h.waitEvent("turn complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
	if !h.s.Speaking() {
		t.Fatal("Speaking() false within the guessed playback")
	}
	if err := h.s.Push("auth crashed", voice.UrgencyInterrupt); err != nil {
		t.Fatal(err)
	}
	h.waitEvent("push deferred", func(voice.Event) bool { return h.log.has("voice.push_deferred") })
	c.waitSent(t, "push after the guessed end", isContent("auth crashed"))
	if h.s.Speaking() {
		t.Error("Speaking() after the guessed end")
	}
}

// TestBargeInCutsTheTranscript is P5: the user barges in 4 s into a
// 10 s reply; the client flushes having played 4 s, and the session
// emits a cut transcript with the words heard (for the orchestrator's
// "Assistant said (cut off…)" line) and logs voice.playback with cut.
func TestBargeInCutsTheTranscript(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	words := strings.Fields("one two three four five six seven eight nine ten")
	speakReply(h, c, words, 1000)
	h.s.mu.Lock()
	h.s.playUtt.Handed("utt-9") // the reply to the user's utterance utt-9
	h.s.mu.Unlock()
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c.serve(interruptedMsg())
	h.waitEvent("flush", isFlushEvent)
	h.s.ClientPlayback(voice.PlaybackFlushed, 4000)

	ev := h.waitEvent("cut transcript", func(ev voice.Event) bool { tr, ok := ev.(voice.EventTranscript); return ok && tr.Cut != nil })
	tr := ev.(voice.EventTranscript)
	if tr.Text != "one two three four" || tr.Dir != voice.DirOut || !tr.Final {
		t.Errorf("cut transcript %+v, want the first four words", tr)
	}
	if tr.Cut.HeardMs != 4000 || tr.Cut.RunMs != 10000 || tr.Cut.Full != strings.Join(words, " ") {
		t.Errorf("cut %+v", tr.Cut)
	}
	// The supervisor records the answer to utt-9 as unanswered: the run
	// is tagged with it, and the user (not a push) cut it.
	if tr.Cut.UtteranceID != "utt-9" || tr.Cut.ByPush {
		t.Errorf("cut %+v, want utt-9 cut by the user", tr.Cut)
	}
	pb := h.log.payloads("voice.playback")
	if len(pb) != 2 || pb[1]["cut"] != true || pb[1]["heard"] != "one two three four" || pb[1]["run_ms"] != 10000 {
		t.Errorf("voice.playback = %v", pb)
	}
}

// TestFullRunIsNotCut: a reply played to its end yields no cut.
func TestFullRunIsNotCut(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	speakReply(h, c, []string{"all", "of", "it"}, 1000)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	c.serve(turnCompleteMsg())
	h.s.ClientPlayback(voice.PlaybackDrained, 3000)
	h.s.ClientPlayback(voice.PlaybackFlushed, 0)
	time.Sleep(50 * time.Millisecond)
	if n := h.countEvents(func(ev voice.Event) bool { tr, ok := ev.(voice.EventTranscript); return ok && tr.Cut != nil }); n != 0 {
		t.Fatalf("%d cut transcripts for a run heard whole", n)
	}
}

// TestPushWaitsWhileClientSpeaking is P6: the client's speech detector
// opening holds a push at once (no transcript yet); closing releases it
// after ClientSpeechRelease.
func TestPushWaitsWhileClientSpeaking(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	h.s.ClientSpeaking(true)
	if err := h.s.Push("auth crashed", voice.UrgencyInterrupt); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if c.count("content") != 0 {
		t.Fatalf("push sent while the client hears the shepherd: %s", describe(c.sentMsgs()))
	}
	closed := time.Now()
	h.s.ClientSpeaking(false)
	c.waitSent(t, "push after the detector closed", isContent("auth crashed"))
	if el := time.Since(closed); el < voice.ClientSpeechRelease-10*time.Millisecond {
		t.Errorf("push %v after close, want >= %v", el, voice.ClientSpeechRelease)
	}
}

// TestPlaybackForgottenWhenClientChanges: a started run from a client
// that went away does not keep the session speaking.
func TestPlaybackForgottenWhenClientChanges(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	speakReply(h, c, []string{"long"}, 3000)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	h.s.ClientSpeaking(true)
	c.serve(turnCompleteMsg())
	h.waitEvent("turn complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
	if !h.s.Speaking() {
		t.Fatal("not speaking while the client plays")
	}
	h.s.ClientAttached(true) // a new client replaced it
	if h.s.Speaking() {
		t.Fatal("Speaking() still true after the client changed")
	}
	if err := h.s.Push("hello", voice.UrgencyInterrupt); err != nil {
		t.Fatal(err)
	}
	c.waitSent(t, "push with the old client's state forgotten", isContent("hello"))
}

// The playback start after a reply push carries the push's utterance_id,
// once; a later start does not (turn.latency's first audio).
func TestPlaybackStartTaggedWithReplyUtterance(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{PushGap: time.Millisecond}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	if err := h.s.PushTagged("The tests are green.", voice.UrgencyInterrupt, map[string]any{"utterance_id": "utt-7"}); err != nil {
		t.Fatal(err)
	}
	h.waitEvent("push sent", func(voice.Event) bool { return h.log.has("voice.push") })
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	h.s.ClientPlayback(voice.PlaybackDrained, 100)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	ps := h.log.payloads("voice.playback")
	if len(ps) != 3 || ps[0]["utterance_id"] != "utt-7" || ps[1]["utterance_id"] != nil || ps[2]["utterance_id"] != nil {
		t.Fatalf("voice.playback = %v", ps)
	}
}

func TestGeminiLateFlushedAndUnsolicitedFlushRaces(t *testing.T) {
	t.Run("late_flushed_preserves_new_turn_and_utterance", func(t *testing.T) {
		c := newFakeConn("c1")
		h := newHarness(t, Config{PushGap: time.Millisecond}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

		// Run 1 answers utt-1 and is interrupted by the user (model barge-in).
		h.s.mu.Lock()
		h.s.playUtt.Handed("utt-1")
		h.s.mu.Unlock()
		speakReply(h, c, strings.Fields("Checking the auth failure right now"), 200)
		h.s.ClientPlayback(voice.PlaybackStarted, 0)
		c.serve(interruptedMsg())
		h.waitEvent("flush 1", isFlushEvent)

		// Before Run 1's PlaybackFlushed arrives, Run 2 (an urgent push answering
		// utt-2) goes out, speaks a substring reply ("Checking failure"), and starts playing.
		if err := h.s.PushTagged("Checking failure", voice.UrgencyInterrupt, map[string]any{"utterance_id": "utt-2", "needs_user": true}); err != nil {
			t.Fatal(err)
		}
		h.waitEvent("push 2 sent", func(voice.Event) bool { return h.log.has("voice.push") })
		speakReply(h, c, []string{"Checking", "failure"}, 200)
		c.serve(turnCompleteMsg())
		h.waitEvent("turn 2 complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
		h.s.ClientPlayback(voice.PlaybackStarted, 0)

		// Now Run 1's late PlaybackFlushed arrives.
		h.s.ClientPlayback(voice.PlaybackFlushed, 400)
		ev := h.waitEvent("cut transcript for run 1", func(ev voice.Event) bool {
			tr, ok := ev.(voice.EventTranscript)
			return ok && tr.Cut != nil
		})
		tr := ev.(voice.EventTranscript)
		if tr.Cut.UtteranceID != "utt-1" {
			t.Fatalf("cut UtteranceID = %q, want %q", tr.Cut.UtteranceID, "utt-1")
		}
		if tr.Cut.ByPush {
			t.Fatalf("cut ByPush = true, want false (run 1 was cut by user)")
		}
		entries := h.s.RecapEntries()
		last := entries[len(entries)-1]
		if last.Text != "Checking failure" || last.Cut {
			t.Fatalf("newer reply corrupted by late cut: entries=%+v", entries)
		}
	})

	t.Run("unsolicited_flushed_does_not_split_user_input_in_progress", func(t *testing.T) {
		c := newFakeConn("c1")
		h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

		speakReply(h, c, strings.Fields("Auth is running the migration suite"), 300)
		h.s.ClientPlayback(voice.PlaybackStarted, 0)

		// User starts speaking (partial input transcript) while client flushes playback
		// ahead of the server's interrupted signal.
		c.serve(inputTranscriptMsg("check the ", false))
		h.waitEvent("partial in", func(ev voice.Event) bool {
			tr, ok := ev.(voice.EventTranscript)
			return ok && tr.Dir == voice.DirIn && strings.Contains(tr.Text, "check")
		})
		h.s.ClientPlayback(voice.PlaybackFlushed, 600)
		c.serve(inputTranscriptMsg("build logs", true))
		h.waitEvent("final in", func(ev voice.Event) bool {
			tr, ok := ev.(voice.EventTranscript)
			return ok && tr.Dir == voice.DirIn && tr.Final
		})

		var userTexts []string
		for _, e := range h.s.RecapEntries() {
			if e.Role == voice.RecapUser {
				userTexts = append(userTexts, e.Text)
			}
		}
		if len(userTexts) != 1 || userTexts[0] != "check the build logs" {
			t.Fatalf("user recap entries = %q, want single [\"check the build logs\"]", userTexts)
		}
	})
}

func TestGeminiStopSequenceEmptyFlushAcksDoNotCutNewReply(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{PushGap: time.Millisecond}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	h.s.ClientAttached(true)

	// Run 1 is playing when the user taps Stop on iOS/web: client locally
	// flushes first, then sends cancel (afterCancel -> FlushAudio + stop push).
	speakReply(h, c, strings.Fields("Auth is running the long test suite"), 200)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	h.s.ClientPlayback(voice.PlaybackFlushed, 400)
	h.waitEvent("cut 1", func(ev voice.Event) bool {
		tr, ok := ev.(voice.EventTranscript)
		return ok && tr.Cut != nil
	})

	_ = h.s.FlushAudio()
	if err := h.s.PushTagged("Stopped auth.", voice.UrgencyInterrupt, map[string]any{"stop": true}); err != nil {
		t.Fatal(err)
	}
	h.waitEvent("stop push flush", isFlushEvent)
	c.waitSent(t, "stop push", isContent("Stopped auth."))

	// Model begins speaking the stop confirmation before the client's two
	// empty flush acknowledgements (for FlushAudio and interrupt("push")) arrive.
	speakReply(h, c, []string{"Stopped", "auth."}, 300)
	c.serve(turnCompleteMsg())
	h.waitEvent("stop reply complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })

	isCut := func(ev voice.Event) bool {
		tr, ok := ev.(voice.EventTranscript)
		return ok && tr.Cut != nil
	}
	cutsBefore := h.countEvents(isCut)
	h.s.ClientPlayback(voice.PlaybackFlushed, 0)
	h.s.ClientPlayback(voice.PlaybackFlushed, 0)
	time.Sleep(30 * time.Millisecond)

	if n := h.countEvents(isCut) - cutsBefore; n != 0 {
		t.Fatalf("empty flush acks emitted %d spurious cut transcripts on new reply", n)
	}
	entries := h.s.RecapEntries()
	if len(entries) == 0 {
		t.Fatal("recap is empty; new reply was deleted by empty flush ack")
	}
	last := entries[len(entries)-1]
	if last.Text != "Stopped auth." || last.Cut {
		t.Fatalf("last recap entry = %+v, want intact 'Stopped auth.'", last)
	}
}

func TestGeminiCutAfterUnderrunKeepsHeardFirstSentence(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	h.s.ClientAttached(true)

	// First sentence plays and drains mid-turn (underrun before TurnComplete).
	speakReply(h, c, []string{"Auth", "passed", "tests."}, 300)
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	h.s.ClientPlayback(voice.PlaybackDrained, 900)

	// Same turn resumes with a second sentence, then is cut at 0 ms of the second run.
	speakReply(h, c, []string{"Now", "deploying", "staging."}, 300)
	c.serve(turnCompleteMsg())
	h.waitEvent("turn complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	h.s.ClientPlayback(voice.PlaybackFlushed, 0)

	ev := h.waitEvent("cut transcript", func(ev voice.Event) bool {
		tr, ok := ev.(voice.EventTranscript)
		return ok && tr.Cut != nil
	})
	tr := ev.(voice.EventTranscript)
	if tr.Text != "Auth passed tests." || tr.Cut.HeardMs != 900 {
		t.Fatalf("cut transcript = %+v (cut=%+v), want Text='Auth passed tests.' HeardMs=900", tr, tr.Cut)
	}
	wantFull := "Auth passed tests. Now deploying staging."
	if tr.Cut.Full != wantFull {
		t.Fatalf("cut Full = %q, want %q", tr.Cut.Full, wantFull)
	}
	entries := h.s.RecapEntries()
	if len(entries) != 1 || entries[0].Text != "Auth passed tests." || !entries[0].Cut || entries[0].CutAfterMs != 900 {
		t.Fatalf("recap entries = %+v, want single cut entry 'Auth passed tests.' at 900ms", entries)
	}
}

func TestGeminiClientFlushDoesNotCreateUntrackedDaemonFlush(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{PushGap: time.Millisecond}, &fakeDialer{results: []func() (Conn, error){ok(c)}})
	h.s.ClientAttached(true)

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
		for ch := range h.s.AudioChunks() {
			for _, ep := range ch.FlushedEpochs {
				noteBarrier(ep)
			}
		}
	}()

	speakReply(h, c, strings.Fields("Auth is running the long test suite"), 200)

	// Client flushes locally on Stop, and Gemini streams one more chunk while
	// the stop tool waits for herdr before afterCancel runs.
	h.s.ClientPlayback(voice.PlaybackStarted, 0)
	h.s.ClientPlayback(voice.PlaybackFlushed, 400)
	h.waitEvent("cut 1", func(ev voice.Event) bool {
		tr, ok := ev.(voice.EventTranscript)
		return ok && tr.Cut != nil
	})
	c.serve(audioMsg(outAudio(200)))
	c.serve(outputTranscriptMsg("still "))
	time.Sleep(40 * time.Millisecond)

	noteBarrier(h.s.FlushAudio())
	if err := h.s.PushTagged("Stopped auth.", voice.UrgencyInterrupt, map[string]any{"stop": true}); err != nil {
		t.Fatal(err)
	}
	h.waitEvent("stop push flush", isFlushEvent)
	c.waitSent(t, "stop push", isContent("Stopped auth."))

	speakReply(h, c, []string{"Stopped", "auth."}, 300)
	c.serve(turnCompleteMsg())
	h.waitEvent("stop reply complete", func(ev voice.Event) bool { _, ok := ev.(voice.EventTurnComplete); return ok })
	time.Sleep(20 * time.Millisecond)

	h.evMu.Lock()
	evs := append([]voice.Event(nil), h.events...)
	h.evMu.Unlock()
	for _, ev := range evs {
		if intr, ok := ev.(voice.EventInterrupted); ok {
			noteBarrier(intr.Epoch)
		}
	}

	mu.Lock()
	acks := daemonFlushes
	mu.Unlock()
	if acks != 2 {
		t.Fatalf("daemon flush barriers emitted = %d, want 2 (client flush created an untracked barrier)", acks)
	}
	for i := 0; i < acks; i++ {
		h.s.ClientPlayback(voice.PlaybackFlushed, 0)
	}
	entries := h.s.RecapEntries()
	last := entries[len(entries)-1]
	if last.Text != "Stopped auth." || last.Cut {
		t.Fatalf("last recap entry = %+v, want intact 'Stopped auth.'", last)
	}
}
