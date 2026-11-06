package voice

import (
	"strings"
	"testing"
	"time"
)

// sendRun feeds a run of words: one fragment per word, each followed by
// its share of audio, total ms of audio at 48 bytes/ms.
func sendRun(p *PlaybackTracker, now time.Time, words []string, totalMs int) {
	per := totalMs * outBytesPerMs / len(words)
	for _, w := range words {
		p.Sent(now, per)
		p.Said(w + " ")
	}
}

func tenWords() []string {
	return strings.Fields("one two three four five six seven eight nine ten")
}

func TestPlaybackCutAfterFortyPercent(t *testing.T) {
	var p PlaybackTracker
	t0 := time.Unix(1000, 0)
	sendRun(&p, t0, tenWords(), 10000)
	p.Report(t0.Add(150*time.Millisecond), PlaybackStarted, 0)
	p.Flush(t0.Add(4 * time.Second)) // the daemon interrupted (barge-in)
	rep := p.Report(t0.Add(4100*time.Millisecond), PlaybackFlushed, 4000)
	if rep.Cut == nil {
		t.Fatalf("no cut: %+v", rep)
	}
	if rep.Heard != "one two three four" {
		t.Errorf("heard %q, want the first 40%% of the words", rep.Heard)
	}
	if rep.RunMs != 10000 || rep.Cut.HeardMs != 4000 || rep.Cut.Full != strings.Join(tenWords(), " ") {
		t.Errorf("report %+v cut %+v", rep, rep.Cut)
	}
	if got := rep.Payload(); got["cut"] != true || got["run_ms"] != 10000 || got["played_ms"] != 4000 {
		t.Errorf("payload %v", got)
	}
}

func TestPlaybackFullRunIsNotCut(t *testing.T) {
	var p PlaybackTracker
	t0 := time.Unix(1000, 0)
	sendRun(&p, t0, tenWords(), 10000)
	p.Report(t0, PlaybackStarted, 0)
	rep := p.Report(t0.Add(10*time.Second), PlaybackDrained, 10000)
	if rep.Cut != nil || rep.RunMs != 10000 {
		t.Fatalf("drained run: %+v", rep)
	}
	// A flush after the drain has nothing left to cut.
	p.Flush(t0.Add(11 * time.Second))
	if rep := p.Report(t0.Add(11*time.Second), PlaybackFlushed, 0); rep.Cut != nil {
		t.Fatalf("flush after drain cut: %+v", rep)
	}
}

func TestPlaybackClientFlushWithoutDaemonFlush(t *testing.T) {
	// The stop button flushes locally before the daemon interrupts.
	var p PlaybackTracker
	t0 := time.Unix(1000, 0)
	sendRun(&p, t0, tenWords(), 10000)
	rep := p.Report(t0.Add(2*time.Second), PlaybackFlushed, 2000)
	if rep.Cut == nil || rep.Heard != "one two" {
		t.Fatalf("client flush: %+v", rep)
	}
	// The daemon's own flush that follows finds an empty run.
	p.Flush(t0.Add(2100 * time.Millisecond))
	if rep := p.Report(t0.Add(2200*time.Millisecond), PlaybackFlushed, 0); rep.Cut != nil {
		t.Fatalf("second flush cut again: %+v", rep)
	}
}

func TestPlaybackDrainedKeepsTheNextRun(t *testing.T) {
	// The next reply's first audio is in flight when the client drains.
	var p PlaybackTracker
	t0 := time.Unix(1000, 0)
	sendRun(&p, t0, []string{"first", "reply"}, 1000)
	p.TurnComplete()
	sendRun(&p, t0, []string{"second", "reply"}, 1000)
	p.Report(t0, PlaybackStarted, 0)
	p.Report(t0.Add(time.Second), PlaybackDrained, 1000)
	p.Flush(t0.Add(1500 * time.Millisecond))
	rep := p.Report(t0.Add(1500*time.Millisecond), PlaybackFlushed, 500)
	if rep.Cut == nil || rep.Cut.Full != "second reply" || rep.Heard != "second" {
		t.Fatalf("second run: %+v %+v", rep, rep.Cut)
	}
}

func TestPlaybackPlayingWithReports(t *testing.T) {
	var p PlaybackTracker
	t0 := time.Unix(1000, 0)
	p.Sent(t0, 2000*outBytesPerMs) // 2 s of audio, generated at once
	p.Report(t0.Add(100*time.Millisecond), PlaybackStarted, 0)
	// Past the timing guess but before drained: still playing.
	if ok, _ := p.Playing(t0.Add(3 * time.Second)); !ok {
		t.Fatal("not playing before drained")
	}
	d := t0.Add(3500 * time.Millisecond)
	p.Report(d, PlaybackDrained, 2000)
	if ok, wait := p.Playing(d.Add(100 * time.Millisecond)); !ok || wait != 200*time.Millisecond {
		t.Fatalf("gap hold: %v %v", ok, wait)
	}
	if ok, _ := p.Playing(d.Add(PlaybackGapHold)); ok {
		t.Fatal("still playing after the gap hold")
	}
	if got := p.EndedAt(d.Add(time.Second)); !got.Equal(d) {
		t.Fatalf("EndedAt %v, want the drain", got)
	}
}

func TestPlaybackStartedNeverEndedIsBounded(t *testing.T) {
	var p PlaybackTracker
	t0 := time.Unix(1000, 0)
	p.Sent(t0, 1000*outBytesPerMs)
	p.Report(t0, PlaybackStarted, 0)
	if ok, _ := p.Playing(t0.Add(time.Second + PlaybackReportSlack - time.Millisecond)); !ok {
		t.Fatal("not playing within the slack")
	}
	if ok, _ := p.Playing(t0.Add(time.Second + PlaybackReportSlack)); ok {
		t.Fatal("a started run with no drained plays forever")
	}
	// Detach forgets it at once.
	p.Report(t0.Add(20*time.Second), PlaybackStarted, 0)
	p.Detach()
	if ok, _ := p.Playing(t0.Add(20 * time.Second)); ok {
		t.Fatal("playing after detach")
	}
}

func TestPlaybackFallbackTimingGuess(t *testing.T) {
	var p PlaybackTracker
	t0 := time.Unix(1000, 0)
	p.Sent(t0, 1000*outBytesPerMs)
	p.Sent(t0.Add(100*time.Millisecond), 1000*outBytesPerMs)
	if ok, wait := p.Playing(t0.Add(500 * time.Millisecond)); !ok || wait != 1500*time.Millisecond {
		t.Fatalf("guess: %v %v", ok, wait)
	}
	if ok, _ := p.Playing(t0.Add(2 * time.Second)); ok {
		t.Fatal("playing after the guessed end")
	}
	if got := p.EndedAt(t0.Add(3 * time.Second)); !got.Equal(t0.Add(2 * time.Second)) {
		t.Fatalf("EndedAt %v", got)
	}
}

func TestClientSpeechHold(t *testing.T) {
	var c ClientSpeech
	t0 := time.Unix(1000, 0)
	if c.Hold(t0) != 0 {
		t.Fatal("unknown detector holds")
	}
	c.Set(t0, true)
	if c.Hold(t0.Add(time.Second)) == 0 {
		t.Fatal("open detector does not hold")
	}
	if c.Hold(t0.Add(ClientSpeechTrust)) != 0 {
		t.Fatal("open detector holds past the trust bound")
	}
	c.Set(t0.Add(10*time.Second), false)
	if got := c.Hold(t0.Add(10*time.Second + 100*time.Millisecond)); got != 200*time.Millisecond {
		t.Fatalf("release hold %v", got)
	}
	if c.Hold(t0.Add(10*time.Second+ClientSpeechRelease)) != 0 {
		t.Fatal("closed detector still holds")
	}
}

func TestWordPrefix(t *testing.T) {
	for _, tc := range []struct {
		in    string
		share float64
		want  string
	}{
		{" a b c d", 0.5, " a b"},
		{"a b", 0.1, ""},
		{"a b", 1, "a b"},
	} {
		if got := wordPrefix(tc.in, tc.share); got != tc.want {
			t.Errorf("wordPrefix(%q, %v) = %q, want %q", tc.in, tc.share, got, tc.want)
		}
	}
}

func TestPlaybackStaleAwaitingRunExpires(t *testing.T) {
	var p PlaybackTracker
	t0 := time.Unix(1000, 0)
	sendRun(&p, t0, []string{"never", "answered"}, 1000)
	p.Flush(t0) // the client never reports this flush
	sendRun(&p, t0.Add(20*time.Second), []string{"later", "reply"}, 1000)
	rep := p.Report(t0.Add(20*time.Second+500*time.Millisecond), PlaybackFlushed, 500)
	if rep.Cut == nil || rep.Cut.Full != "later reply" {
		t.Fatalf("flushed matched a stale run: %+v %+v", rep, rep.Cut)
	}
}

// PlaybackUtterance tags only the first start after a hand-over and reports
// the active started run on flush until an untagged start clears it.
func TestPlaybackUtterance(t *testing.T) {
	var p PlaybackUtterance
	p.Handed("utt-1")
	if got := p.Flushed(false); got != "" {
		t.Errorf("flush before start = %q", got)
	}
	if got := p.Started(PlaybackStarted); got != "utt-1" {
		t.Errorf("started = %q, want utt-1", got)
	}
	if got := p.Started(PlaybackFlushed); got != "" {
		t.Errorf("non-start tagged %q", got)
	}
	if got := p.Flushed(false); got != "utt-1" {
		t.Errorf("flush of started run = %q, want utt-1", got)
	}
	p.Handed("utt-2")
	p.Started(PlaybackStarted)
	p.Started(PlaybackStarted)
	if got := p.Flushed(false); got != "" {
		t.Errorf("flush after untagged start = %q", got)
	}
}

func voicedPCM(ms int) []byte {
	n := ms * outBytesPerMs
	b := make([]byte, n)
	for i := 0; i+1 < n; i += 2 {
		// Little-endian int16 = 3000 (~0x0bb8), well above silence floor.
		b[i] = 0xb8
		b[i+1] = 0x0b
	}
	return b
}

func silentPCM(ms int) []byte {
	return make([]byte, ms*outBytesPerMs)
}

func feedVoicedRunWithPauses(p *PlaybackTracker, t0 time.Time) {
	// 500 ms leading silence -> 1000 ms voiced ("one two three four ") ->
	// 1000 ms mid-turn pause -> 1000 ms voiced ("five six seven eight") ->
	// 600 ms trailing silence. Total sent = 4100 ms, voiced = 2000 ms.
	p.SentPCM(t0, silentPCM(500))
	p.SentPCM(t0, voicedPCM(1000))
	p.Said("one two three four ")
	p.SentPCM(t0, silentPCM(1000))
	p.SentPCM(t0, voicedPCM(1000))
	p.Said("five six seven eight")
	p.SentPCM(t0, silentPCM(600))
}

func TestPlaybackSentPCMVoicedCheckpoints(t *testing.T) {
	t0 := time.Unix(1000, 0)

	t.Run("leading_silence", func(t *testing.T) {
		var p PlaybackTracker
		feedVoicedRunWithPauses(&p, t0)
		p.Flush(t0.Add(time.Second))
		rep := p.Report(t0.Add(time.Second), PlaybackFlushed, 450)
		if rep.Cut == nil || rep.Heard != "" {
			t.Fatalf("cut during leading silence = %+v (heard=%q), want empty heard", rep.Cut, rep.Heard)
		}
	})

	t.Run("first_phrase_half", func(t *testing.T) {
		var p PlaybackTracker
		feedVoicedRunWithPauses(&p, t0)
		p.Flush(t0.Add(time.Second))
		// 500 ms leading silence + 500 ms voiced = 50% of first 4-word phrase.
		rep := p.Report(t0.Add(time.Second), PlaybackFlushed, 1000)
		if rep.Cut == nil || rep.Heard != "one two" {
			t.Fatalf("cut mid first phrase = %+v (heard=%q), want %q", rep.Cut, rep.Heard, "one two")
		}
	})

	t.Run("mid_turn_pause", func(t *testing.T) {
		var p PlaybackTracker
		feedVoicedRunWithPauses(&p, t0)
		p.Flush(t0.Add(3 * time.Second))
		// 2300 ms is 800 ms into the 1000 ms silent pause before "five" begins.
		rep := p.Report(t0.Add(3*time.Second), PlaybackFlushed, 2300)
		if rep.Cut == nil || rep.Heard != "one two three four" {
			t.Fatalf("cut in mid-turn pause = %+v (heard=%q), want %q", rep.Cut, rep.Heard, "one two three four")
		}
	})

	t.Run("trailing_silence", func(t *testing.T) {
		var p PlaybackTracker
		feedVoicedRunWithPauses(&p, t0)
		p.Flush(t0.Add(4 * time.Second))
		// 3600 ms is 100 ms into the 600 ms trailing silence after all voiced words finished.
		rep := p.Report(t0.Add(4*time.Second), PlaybackFlushed, 3600)
		if rep.Cut != nil {
			t.Fatalf("cut in trailing silence after all voiced audio played: %+v (heard=%q)", rep.Cut, rep.Heard)
		}
	})
}

func TestPlaybackFlushMetaPreservedAcrossNewTurn(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	var p PlaybackTracker

	// Run 1: 2000 ms sent, settled recap seq 2, flushed on barge-in.
	p.Sent(t0, 2000*outBytesPerMs)
	p.Said("first reply in full")
	p.SetRecapSeq(2)
	if !p.FlushMeta(t0.Add(500*time.Millisecond), FlushMeta{UtteranceID: "utt-1", ByPush: false}) {
		t.Fatal("FlushMeta = false, want true")
	}

	// Run 2 starts immediately before client's flushed report for Run 1 arrives.
	t2 := t0.Add(600 * time.Millisecond)
	p.Sent(t2, 2000*outBytesPerMs)
	p.Said("second reply stays intact")
	p.SetRecapSeq(4)
	p.Report(t2, PlaybackStarted, 0)

	// Late flushed report cuts Run 1 with Run 1's own metadata without ending Run 2.
	rep1 := p.Report(t2.Add(50*time.Millisecond), PlaybackFlushed, 500)
	if !rep1.Awaited || rep1.RecapSeq != 2 || rep1.Cut == nil || rep1.Cut.Full != "first reply in full" || rep1.Heard != "first" {
		t.Fatalf("flushed(1) = %+v (cut=%+v), want cut of run 1 with RecapSeq=2", rep1, rep1.Cut)
	}
	if rep1.Cut.UtteranceID != "utt-1" || rep1.Cut.ByPush {
		t.Fatalf("flushed(1) cut meta = %+v, want UtteranceID=utt-1 ByPush=false", rep1.Cut)
	}
	if playing, _ := p.Playing(t2.Add(100 * time.Millisecond)); !playing {
		t.Fatal("late flushed(1) falsely ended active run 2")
	}

	// Unsolicited client flush of Run 2 carries Run 2's RecapSeq.
	rep2 := p.Report(t2.Add(200*time.Millisecond), PlaybackFlushed, 500)
	if rep2.Awaited || rep2.RecapSeq != 4 || rep2.Cut == nil || rep2.Cut.Full != "second reply stays intact" || rep2.Heard != "second" {
		t.Fatalf("flushed(2) = %+v (cut=%+v), want cut of run 2 with RecapSeq=4", rep2, rep2.Cut)
	}
}

func TestPlaybackEarlyTranscriptDoesNotCountAllWords(t *testing.T) {
	now := time.Unix(1000, 0)

	t.Run("single_early_fragment", func(t *testing.T) {
		var p PlaybackTracker
		p.Said("one two three four ")
		p.SentPCM(now, voicedPCM(1000))
		p.Said("five six seven eight")
		p.SentPCM(now, voicedPCM(1000))
		rep := p.Report(now, PlaybackFlushed, 100)
		if rep.Cut == nil || rep.Heard != "" {
			t.Fatalf("early transcript overcounted: %+v", rep)
		}
	})

	t.Run("multiple_early_fragments_before_audio", func(t *testing.T) {
		var p PlaybackTracker
		p.Said("one two ")
		p.Said("three four ")
		p.SentPCM(now, voicedPCM(1000))
		rep := p.Report(now, PlaybackFlushed, 500)
		if rep.Cut == nil || rep.Heard != "one two" {
			t.Fatalf("50%% of 4 pre-voiced words = %+v (heard=%q), want %q", rep.Cut, rep.Heard, "one two")
		}

		var p2 PlaybackTracker
		p2.Said("one two ")
		p2.Said("three four ")
		p2.SentPCM(now, voicedPCM(1000))
		rep2 := p2.Report(now, PlaybackFlushed, 750)
		if rep2.Cut == nil || rep2.Heard != "one two three" {
			t.Fatalf("75%% of 4 pre-voiced words = %+v (heard=%q), want %q", rep2.Cut, rep2.Heard, "one two three")
		}
	})

	t.Run("multiple_fragments_during_mid_turn_pause", func(t *testing.T) {
		build := func() PlaybackTracker {
			var p PlaybackTracker
			p.SentPCM(now, voicedPCM(500))
			p.Said("one two ")
			p.SentPCM(now, silentPCM(400))
			p.Said("three ")
			p.Said("four ")
			p.SentPCM(now, voicedPCM(500))
			return p
		}
		pPause := build()
		if rep := pPause.Report(now, PlaybackFlushed, 700); rep.Cut == nil || rep.Heard != "one two" {
			t.Fatalf("cut in silent pause = %+v (heard=%q), want %q", rep.Cut, rep.Heard, "one two")
		}
		pHalf := build()
		if rep := pHalf.Report(now, PlaybackFlushed, 1150); rep.Cut == nil || rep.Heard != "one two three" {
			t.Fatalf("cut mid second span = %+v (heard=%q), want %q", rep.Cut, rep.Heard, "one two three")
		}
	})

	t.Run("unvoiced_trailing_transcript_not_counted_whole", func(t *testing.T) {
		var p PlaybackTracker
		p.Said("one two ")
		p.SentPCM(now, voicedPCM(200))
		p.Said("three four five six")
		rep := p.Report(now, PlaybackFlushed, 200)
		if rep.Cut == nil || rep.Heard != "one two" {
			t.Fatalf("unvoiced trailing transcript treated as heard: %+v (heard=%q)", rep.Cut, rep.Heard)
		}
	})
}

func TestPlaybackShortVoicedRunIsCut(t *testing.T) {
	now := time.Unix(1000, 0)

	t.Run("with_trailing_silence", func(t *testing.T) {
		var p PlaybackTracker
		p.SentPCM(now, voicedPCM(100))
		p.Said("one two three four")
		p.SentPCM(now, silentPCM(1900))
		rep := p.Report(now, PlaybackFlushed, 10)
		if rep.Cut == nil {
			t.Fatalf("short voiced run considered complete: %+v", rep)
		}
	})

	t.Run("without_trailing_silence", func(t *testing.T) {
		var p PlaybackTracker
		p.SentPCM(now, voicedPCM(100))
		p.Said("one two three four")
		rep := p.Report(now, PlaybackFlushed, 10)
		if rep.Cut == nil || rep.Heard != "" {
			t.Fatalf("short 100ms run played 10ms = %+v (heard=%q), want cut", rep.Cut, rep.Heard)
		}
	})
}

func TestPlaybackShortVoicedRunHeardBeforeTrailingSilence(t *testing.T) {
	var p PlaybackTracker
	now := time.Unix(1000, 0)
	p.SentPCM(now, voicedPCM(100))
	p.Said("hello there")
	p.SentPCM(now, silentPCM(1900))
	if rep := p.Report(now, PlaybackFlushed, 200); rep.Cut != nil {
		t.Fatalf("all voiced audio was heard: %+v", rep)
	}
}

func TestPlaybackEmptyFlushAckDoesNotCutNextRun(t *testing.T) {
	var p PlaybackTracker
	t0 := time.Unix(1000, 0)

	// Client locally flushes Run 1 (iOS/web Stop button), then daemon flushes
	// while the run is empty (afterCancel / interrupt("push")).
	sendRun(&p, t0, tenWords(), 10000)
	if rep := p.Report(t0.Add(2*time.Second), PlaybackFlushed, 2000); rep.Cut == nil || rep.Heard != "one two" {
		t.Fatalf("initial local flush = %+v, want cut at 'one two'", rep)
	}
	p.Flush(t0.Add(2050 * time.Millisecond))
	p.Flush(t0.Add(2060 * time.Millisecond))

	// Before the two empty flush acks return from the client, the next reply begins.
	sendRun(&p, t0.Add(2100*time.Millisecond), []string{"Stopped", "auth", "agent"}, 900)

	for i := 1; i <= 2; i++ {
		rep := p.Report(t0.Add(2150*time.Millisecond), PlaybackFlushed, 0)
		if !rep.Awaited || rep.Cut != nil {
			t.Fatalf("empty flush ack %d = %+v (cut=%+v), want awaited with nil Cut", i, rep, rep.Cut)
		}
	}
	if p.Empty() {
		t.Fatal("next reply's run was consumed by empty flush acknowledgements")
	}
	if rep := p.Report(t0.Add(2500*time.Millisecond), PlaybackFlushed, 300); rep.Cut == nil || rep.Heard != "Stopped" {
		t.Fatalf("subsequent cut on next reply = %+v (cut=%+v), want heard='Stopped'", rep, rep.Cut)
	}
}

func TestPlaybackCutAfterUnderrunRetainsHeardPrefix(t *testing.T) {
	t0 := time.Unix(1000, 0)

	t.Run("zero_ms_after_underrun_keeps_first_sentence", func(t *testing.T) {
		var p PlaybackTracker
		sendRun(&p, t0, []string{"First", "sentence."}, 1000)
		p.Report(t0, PlaybackStarted, 0)
		p.Report(t0.Add(time.Second), PlaybackDrained, 1000)

		// Same reply resumes after underrun gap, then is cut at 0 ms of the continuation.
		sendRun(&p, t0.Add(1100*time.Millisecond), []string{"Second", "sentence", "long", "tail."}, 2000)
		p.Report(t0.Add(1150*time.Millisecond), PlaybackStarted, 0)
		rep := p.Report(t0.Add(1200*time.Millisecond), PlaybackFlushed, 0)
		if rep.Cut == nil {
			t.Fatalf("expected cut report, got %+v", rep)
		}
		if rep.Heard != "First sentence." {
			t.Fatalf("rep.Heard = %q, want %q (first sentence erased after drain)", rep.Heard, "First sentence.")
		}
		if rep.Cut.HeardMs != 1000 || rep.Cut.RunMs != 3000 {
			t.Fatalf("cut timing = %+v, want HeardMs=1000 RunMs=3000", rep.Cut)
		}
		wantFull := "First sentence. Second sentence long tail."
		if rep.Cut.Full != wantFull {
			t.Fatalf("rep.Cut.Full = %q, want %q", rep.Cut.Full, wantFull)
		}
	})

	t.Run("partial_ms_after_underrun_appends_continuation_prefix", func(t *testing.T) {
		var p PlaybackTracker
		sendRun(&p, t0, []string{"First", "sentence."}, 1000)
		p.Report(t0, PlaybackStarted, 0)
		p.Report(t0.Add(time.Second), PlaybackDrained, 1000)

		sendRun(&p, t0.Add(1100*time.Millisecond), []string{"Second", "sentence", "long", "tail."}, 2000)
		p.Report(t0.Add(1150*time.Millisecond), PlaybackStarted, 0)
		rep := p.Report(t0.Add(1650*time.Millisecond), PlaybackFlushed, 500)
		if rep.Cut == nil || rep.Heard != "First sentence. Second" || rep.Cut.HeardMs != 1500 || rep.Cut.RunMs != 3000 {
			t.Fatalf("cut = %+v (heard=%q), want Heard='First sentence. Second' HeardMs=1500 RunMs=3000", rep.Cut, rep.Heard)
		}
	})

	t.Run("cjk_no_space_preserves_exact_full_string", func(t *testing.T) {
		var p PlaybackTracker
		p.Sent(t0, 1000*outBytesPerMs)
		p.Said("第一句。")
		p.Report(t0.Add(time.Second), PlaybackDrained, 1000)

		p.Sent(t0.Add(1100*time.Millisecond), 1000*outBytesPerMs)
		p.Said("第二句很长。")
		rep := p.Report(t0.Add(1150*time.Millisecond), PlaybackFlushed, 0)
		if rep.Cut == nil || rep.Heard != "第一句。" || rep.Cut.Full != "第一句。第二句很长。" {
			t.Fatalf("CJK cut = %+v (heard=%q), want Heard='第一句。' Full='第一句。第二句很长。'", rep.Cut, rep.Heard)
		}
	})
}
