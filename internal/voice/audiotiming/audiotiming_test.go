package audiotiming

import (
	"testing"
	"time"
)

func TestFirstChunkDefinesTheScheduleWithZeroGap(t *testing.T) {
	tr := New(1, 24000)
	if got := tr.Chunk(time.Now(), 4800); got != 0 {
		t.Fatalf("first chunk gap = %v, want 0", got)
	}
}

func TestChunksAheadOfRealTimeAreNeverLate(t *testing.T) {
	tr := New(1, 24000)
	t0 := time.Now()
	// 100 ms of audio (4800 bytes @ 24 kHz) delivered every 50 ms: the
	// provider is running ahead of real time, so nothing is late, and
	// the gap between chunks is 50 ms.
	tr.Chunk(t0, 4800)
	for i := 1; i <= 5; i++ {
		now := t0.Add(time.Duration(i) * 50 * time.Millisecond)
		if got := tr.Chunk(now, 4800); got != 50 {
			t.Fatalf("chunk %d gap = %v, want 50", i, got)
		}
	}
	p := tr.Finish()
	if p["max_late_ms"] != 0.0 {
		t.Errorf("max_late_ms = %v, want 0", p["max_late_ms"])
	}
	if p["max_gap_ms"] != 50.0 {
		t.Errorf("max_gap_ms = %v, want 50", p["max_gap_ms"])
	}
	if p["chunks"] != 6 {
		t.Errorf("chunks = %v, want 6", p["chunks"])
	}
}

func TestLateChunk(t *testing.T) {
	tr := New(2, 24000)
	t0 := time.Now()
	// Chunk 1: 100 ms of audio, arrives at t0 (defines the schedule).
	tr.Chunk(t0, 4800)
	// Chunk 2 is due at t0+100ms but arrives at t0+300ms: 200 ms late,
	// after a 300 ms gap.
	if got := tr.Chunk(t0.Add(300*time.Millisecond), 4800); got != 300 {
		t.Fatalf("gap = %v, want 300", got)
	}
	p := tr.Finish()
	if p["max_late_ms"] != 200.0 || p["p95_late_ms"] != 200.0 {
		t.Errorf("late max/p95 = %v/%v, want 200/200", p["max_late_ms"], p["p95_late_ms"])
	}
	if p["max_gap_ms"] != 300.0 || p["lead_at_max_gap_ms"] != -200.0 {
		t.Errorf("max_gap_ms = %v, lead_at_max_gap_ms = %v, want 300, -200", p["max_gap_ms"], p["lead_at_max_gap_ms"])
	}
}

// TestFrontLoadedTurnWithALongPause is the turn of 2026-10-08 00:48:47Z
// (Gemini, 11.48 s of audio over 7.14 s): about 7.8 s of audio arrived
// in the first 1.5 s, then nothing for 5.1 s, then the last 3.7 s. The
// old metric (lateness, then called max_gap_ms) logged 0; the gap is
// 5.1 s, and the lead at it says a real-time player still had 1.2 s.
func TestFrontLoadedTurnWithALongPause(t *testing.T) {
	tr := New(1, 24000)
	t0 := time.Now()
	const second = 48000      // bytes of 1 s at 24 kHz PCM16
	for i := 0; i < 26; i++ { // 26 × 300 ms = 7.8 s over 1.5 s
		tr.Chunk(t0.Add(time.Duration(i)*60*time.Millisecond), second*3/10)
	}
	resume := t0.Add(1500*time.Millisecond + 5100*time.Millisecond)
	if gap := tr.Chunk(resume, second*37/10); gap < 5099 || gap > 5101 {
		t.Fatalf("gap at resume = %v, want ~5100", gap)
	}
	p := tr.Finish()
	if p["max_late_ms"] != 0.0 {
		t.Errorf("max_late_ms = %v, want 0 (the banked audio covered the pause)", p["max_late_ms"])
	}
	if g := p["max_gap_ms"].(float64); g < 5099 || g > 5101 {
		t.Errorf("max_gap_ms = %v, want ~5100", g)
	}
	if lead := p["lead_at_max_gap_ms"].(float64); lead < 1100 || lead > 1300 {
		t.Errorf("lead_at_max_gap_ms = %v, want ~1200", lead)
	}
	if at := p["max_gap_at_ms"].(float64); at < 6599 || at > 6601 {
		t.Errorf("max_gap_at_ms = %v, want ~6600", at)
	}
}

func TestFinishOnEmptyTrackerReturnsNil(t *testing.T) {
	tr := New(1, 24000)
	if p := tr.Finish(); p != nil {
		t.Fatalf("Finish() = %v, want nil", p)
	}
}

func TestRealtimeRatio(t *testing.T) {
	tr := New(1, 24000)
	t0 := time.Now()
	tr.Chunk(t0, 4800)                          // 100 ms audio
	tr.Chunk(t0.Add(50*time.Millisecond), 4800) // another 100 ms, arrives fast
	p := tr.Finish()
	// 200 ms of audio delivered over 50 ms of wall time: ratio 4.
	ratio, ok := p["realtime_ratio"].(float64)
	if !ok {
		t.Fatalf("realtime_ratio missing: %v", p)
	}
	if ratio < 3.9 || ratio > 4.1 {
		t.Errorf("realtime_ratio = %v, want ~4", ratio)
	}
}
