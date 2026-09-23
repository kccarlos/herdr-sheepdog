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

func TestNoGapWhenChunksKeepAheadOfRealTime(t *testing.T) {
	tr := New(1, 24000)
	t0 := time.Now()
	// 100 ms of audio (4800 bytes @ 24 kHz) delivered every 50 ms: the
	// provider is running ahead of real time, so no gap should register.
	tr.Chunk(t0, 4800)
	for i := 1; i <= 5; i++ {
		now := t0.Add(time.Duration(i) * 50 * time.Millisecond)
		if got := tr.Chunk(now, 4800); got != 0 {
			t.Fatalf("chunk %d gap = %v, want 0", i, got)
		}
	}
	p := tr.Finish()
	if p["max_gap_ms"] != 0.0 {
		t.Errorf("max_gap_ms = %v, want 0", p["max_gap_ms"])
	}
	if p["chunks"] != 6 {
		t.Errorf("chunks = %v, want 6", p["chunks"])
	}
}

func TestGapWhenAChunkArrivesLateRelativeToDeliveredAudio(t *testing.T) {
	tr := New(2, 24000)
	t0 := time.Now()
	// Chunk 1: 100 ms of audio, arrives at t0 (defines the schedule).
	tr.Chunk(t0, 4800)
	// Chunk 2 is due at t0+100ms but arrives at t0+300ms: 200 ms late.
	got := tr.Chunk(t0.Add(300*time.Millisecond), 4800)
	if got < 199 || got > 201 {
		t.Fatalf("gap = %v, want ~200", got)
	}
	p := tr.Finish()
	if mg := p["max_gap_ms"].(float64); mg < 199 || mg > 201 {
		t.Errorf("max_gap_ms = %v, want ~200", mg)
	}
	if p["p95_gap_ms"].(float64) < 199 {
		t.Errorf("p95_gap_ms = %v, want ~200", p["p95_gap_ms"])
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
