// Package audiotiming measures how bursty a voice provider's output audio
// arrives, shared by internal/voice/gptlive and internal/voice/gemini so
// the two providers log the same voice.audio_timing shape (see the
// "Measure where the gaps come from" investigation in the audio-gap fix:
// the browser's jitter buffer underruns when a model turn's chunks arrive
// in bursts with gaps the prebuffer cannot cover).
//
// A Tracker follows one model "turn" (one continuous run of output audio,
// from the first chunk to the provider going quiet again). For each chunk
// after the first it computes how far behind a real-time schedule the
// chunk's arrival fell — the schedule being "the turn's first chunk
// arrived at time T; if playback started then, chunk i is due at T plus
// the audio duration already delivered". A chunk that arrives after its
// due time is starving whatever is consuming the audio in real time; the
// gap is that lateness, in ms. Gaps of 0 mean the provider is keeping
// ahead of real time (typical: TTS renders faster than it plays).
package audiotiming

import (
	"math"
	"sort"
	"time"
)

// bytesPerSample is fixed: every stream this package measures is PCM16
// mono (docs/ws-contract.md).
const bytesPerSample = 2

// Tracker accumulates one turn's chunk arrivals. Not safe for concurrent
// use; callers already serialise chunk delivery under their own lock.
type Tracker struct {
	turn       int
	sampleRate int
	start      time.Time
	last       time.Time
	chunks     int
	bytes      int
	audioMs    float64
	maxGapMs   float64
	gaps       []float64
}

// New starts tracking turn id. sampleRate is the samples/sec of the PCM16
// mono audio that will be passed to Chunk (24 000 for both providers'
// output today; wsproto.ServerSampleRate is the canonical value).
func New(turn int, sampleRate int) *Tracker {
	return &Tracker{turn: turn, sampleRate: sampleRate}
}

// Chunk records one chunk of n bytes arriving at wall-clock time now.
// Returns the gap (ms) this chunk fell behind the real-time schedule (0
// for the first chunk, which defines the schedule's origin).
func (t *Tracker) Chunk(now time.Time, n int) float64 {
	var gap float64
	if t.chunks == 0 {
		t.start = now
	} else {
		schedule := t.start.Add(msToDuration(t.audioMs))
		if now.After(schedule) {
			gap = float64(now.Sub(schedule)) / float64(time.Millisecond)
		}
		t.gaps = append(t.gaps, gap)
		if gap > t.maxGapMs {
			t.maxGapMs = gap
		}
	}
	t.chunks++
	t.bytes += n
	if t.sampleRate > 0 {
		t.audioMs += float64(n) / bytesPerSample / float64(t.sampleRate) * 1000
	}
	t.last = now
	return gap
}

func msToDuration(ms float64) time.Duration {
	return time.Duration(ms * float64(time.Millisecond))
}

// Finish returns the voice.audio_timing payload for the turn, or nil if
// no chunk was ever recorded.
func (t *Tracker) Finish() map[string]any {
	if t.chunks == 0 {
		return nil
	}
	wallMs := float64(t.last.Sub(t.start)) / float64(time.Millisecond)
	payload := map[string]any{
		"turn":       t.turn,
		"chunks":     t.chunks,
		"bytes":      t.bytes,
		"audio_ms":   round1(t.audioMs),
		"wall_ms":    round1(wallMs),
		"max_gap_ms": round1(t.maxGapMs),
		"p95_gap_ms": round1(percentile95(t.gaps)),
	}
	if wallMs > 0 {
		payload["realtime_ratio"] = round2(t.audioMs / wallMs)
	}
	return payload
}

// percentile95 uses the nearest-rank method; an empty slice (a turn of
// exactly one chunk) yields 0.
func percentile95(gaps []float64) float64 {
	n := len(gaps)
	if n == 0 {
		return 0
	}
	sorted := make([]float64, n)
	copy(sorted, gaps)
	sort.Float64s(sorted)
	idx := int(math.Ceil(0.95*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
