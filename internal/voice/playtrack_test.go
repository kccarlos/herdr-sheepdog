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

// Run is the utterance whose reply the latest started run plays: set by
// the start a hand-over tags, cleared by an untagged start.
func TestPlaybackUtteranceRun(t *testing.T) {
	var p PlaybackUtterance
	p.Handed("utt-1")
	if p.Run() != "" {
		t.Errorf("run before a start = %q", p.Run())
	}
	if got := p.Started(PlaybackStarted); got != "utt-1" || p.Run() != "utt-1" {
		t.Errorf("started %q, run %q", got, p.Run())
	}
	p.Started(PlaybackFlushed)
	if p.Run() != "utt-1" {
		t.Errorf("a flush moved the run: %q", p.Run())
	}
	p.Started(PlaybackStarted)
	if p.Run() != "" {
		t.Errorf("an untagged start kept %q", p.Run())
	}
}
