package gemini

import (
	"strings"
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
// (docs/research/voice-frameworks-2026-09-24.md): Gemini generates ~4×
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
	p := h.log.payloads("voice.push")
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
	p := h.log.payloads("voice.push")
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

// TestBargeInCutsTheTranscript is P5: the shepherd barges in 4 s into a
// 10 s reply; the client flushes having played 4 s, and the session
// emits a cut transcript with the words heard (for the brain's
// "Assistant said (cut off…)" line) and logs voice.playback with cut.
func TestBargeInCutsTheTranscript(t *testing.T) {
	c := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c)}})

	words := strings.Fields("one two three four five six seven eight nine ten")
	speakReply(h, c, words, 1000)
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
