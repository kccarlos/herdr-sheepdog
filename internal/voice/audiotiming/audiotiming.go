// Package audiotiming measures how bursty a voice provider's output audio
// arrives, shared by internal/voice/gptlive and internal/voice/gemini so
// the two providers log the same voice.audio_timing shape (see the
// "Measure where the gaps come from" investigation in the audio-gap fix:
// the browser's jitter buffer underruns when a model turn's chunks arrive
// in bursts with gaps the prebuffer cannot cover).
//
// A Tracker follows one model "turn" (one continuous run of output audio,
// from the first chunk to the provider going quiet again) and reports two
// different things about it:
//
//   - the **gap**: the wall-clock wait between two consecutive chunks.
//     max_gap_ms is the longest the provider went silent in the middle of
//     the turn, whatever audio was already banked.
//   - the **lateness**: how far behind a real-time schedule a chunk fell —
//     the schedule being "the turn's first chunk arrived at time T; if
//     playback started then, chunk i is due at T plus the audio duration
//     already delivered". A late chunk starves a player that started on
//     the first chunk with no buffer; 0 means the provider kept ahead.
//
// Until 2026-10-08 the lateness was reported as max_gap_ms. A provider
// that front-loads its audio (Gemini, often 2–4× real time) can then go
// silent for seconds with a lateness of 0: the turn of 00:48:47Z paused
// 5.1 s between chunks and logged max_gap_ms 0. Both are logged now, with
// the lead (audio delivered minus wall time) at the longest gap, which
// says whether that gap could be heard at all.
package audiotiming

import (
	"math"
	"sort"
	"time"
)

// bytesPerSample is fixed: every stream this package measures is PCM16
// mono (docs/dev/ws-contract.md).
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

	gaps     []float64 // inter-arrival wall gaps, ms
	maxGapMs float64
	maxGapAt float64 // ms since start when the longest gap ended
	// leadAtMaxGap is the audio delivered before the longest gap minus
	// the wall time elapsed when it ended: how much a real-time player
	// started on the first chunk still had buffered (negative: it ran dry).
	leadAtMaxGap float64
	lates        []float64 // lateness against the real-time schedule, ms
	maxLateMs    float64
	minLeadMs    float64
}

// New starts tracking turn id. sampleRate is the samples/sec of the PCM16
// mono audio that will be passed to Chunk (24 000 for both providers'
// output today; wsproto.ServerSampleRate is the canonical value).
func New(turn int, sampleRate int) *Tracker {
	return &Tracker{turn: turn, sampleRate: sampleRate}
}

// Chunk records one chunk of n bytes arriving at wall-clock time now.
// Returns the gap (ms) since the previous chunk (0 for the first).
func (t *Tracker) Chunk(now time.Time, n int) float64 {
	var gap float64
	if t.chunks == 0 {
		t.start = now
	} else {
		gap = math.Max(0, ms(now.Sub(t.last)))
		elapsed := ms(now.Sub(t.start))
		lead := t.audioMs - elapsed
		late := math.Max(0, -lead)
		t.gaps = append(t.gaps, gap)
		t.lates = append(t.lates, late)
		if gap > t.maxGapMs {
			t.maxGapMs = gap
			t.maxGapAt = elapsed
			t.leadAtMaxGap = lead
		}
		if late > t.maxLateMs {
			t.maxLateMs = late
		}
		if t.chunks == 1 || lead < t.minLeadMs {
			t.minLeadMs = lead
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

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Finish returns the voice.audio_timing payload for the turn, or nil if
// no chunk was ever recorded.
func (t *Tracker) Finish() map[string]any {
	if t.chunks == 0 {
		return nil
	}
	wallMs := ms(t.last.Sub(t.start))
	payload := map[string]any{
		"turn":        t.turn,
		"chunks":      t.chunks,
		"bytes":       t.bytes,
		"audio_ms":    round1(t.audioMs),
		"wall_ms":     round1(wallMs),
		"max_gap_ms":  round1(t.maxGapMs),
		"p95_gap_ms":  round1(percentile95(t.gaps)),
		"max_late_ms": round1(t.maxLateMs),
		"p95_late_ms": round1(percentile95(t.lates)),
	}
	if t.chunks > 1 {
		payload["max_gap_at_ms"] = round1(t.maxGapAt)
		payload["lead_at_max_gap_ms"] = round1(t.leadAtMaxGap)
		payload["min_lead_ms"] = round1(t.minLeadMs)
	}
	if wallMs > 0 {
		payload["realtime_ratio"] = round2(t.audioMs / wallMs)
	}
	return payload
}

// percentile95 uses the nearest-rank method; an empty slice (a turn of
// exactly one chunk) yields 0.
func percentile95(v []float64) float64 {
	n := len(v)
	if n == 0 {
		return 0
	}
	sorted := make([]float64, n)
	copy(sorted, v)
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
