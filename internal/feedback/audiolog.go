// Package feedback backs the user's "that paused" marks (POST
// /api/feedback, docs/dev/ws-contract.md "Feedback and drains"): a short-term
// record of the voice's output audio as the daemon received and sent it,
// the snapshot of the last 30 s a user.feedback event carries, and the
// diagnosis `sheepdog feedback` reads off it (model gap, delivery gap or
// client playback).
package feedback

import (
	"sync"
	"time"
)

// keepFor is how long the AudioLog keeps a chunk: twice the snapshot
// window, so a run that began before the window is still whole.
const keepFor = 2 * Window

// maxChunks bounds the AudioLog however fast chunks come.
const maxChunks = 4000

// Chunk is one binary audio frame of the voice's output on its way to
// the /ws client.
type Chunk struct {
	Seq uint64
	// Run numbers the voice's runs of output audio: a new run begins
	// after the model's turn completes, an interruption or a flush.
	Run int
	// At is when the daemon got the chunk from the voice session.
	At time.Time
	// Bytes of PCM16 mono at 24 kHz.
	Bytes int
	// SentAt is when the /ws writer finished writing it to the socket
	// (zero: never sent — no client, dropped, or still queued).
	SentAt time.Time
}

// AudioMs is the chunk's audio length.
func (c Chunk) AudioMs() float64 { return float64(c.Bytes) / 48.0 }

// AudioLog records the voice's output chunks as they pass through the
// web layer. Safe for concurrent use; the zero value is not usable, use
// NewAudioLog.
type AudioLog struct {
	mu      sync.Mutex
	chunks  []Chunk
	seq     uint64
	run     int
	runOpen bool
}

// NewAudioLog returns an empty log.
func NewAudioLog() *AudioLog { return &AudioLog{} }

// Arrived records a chunk of n bytes the voice produced at at and
// returns its sequence number (for Sent).
func (a *AudioLog) Arrived(at time.Time, n int) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.runOpen {
		a.run++
		a.runOpen = true
	}
	a.seq++
	a.chunks = append(a.chunks, Chunk{Seq: a.seq, Run: a.run, At: at, Bytes: n})
	a.trimLocked(at)
	return a.seq
}

// Sent records that chunk seq was written to the client at at.
func (a *AudioLog) Sent(seq uint64, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Chunks are in seq order; the one just written is near the end.
	for i := len(a.chunks) - 1; i >= 0; i-- {
		if a.chunks[i].Seq == seq {
			a.chunks[i].SentAt = at
			return
		}
		if a.chunks[i].Seq < seq {
			return
		}
	}
}

// EndRun closes the current run: the next chunk starts a new one.
func (a *AudioLog) EndRun() {
	a.mu.Lock()
	a.runOpen = false
	a.mu.Unlock()
}

// Since returns the chunks of every run with a chunk at or after from,
// whole (a run that began before from is included from its start).
func (a *AudioLog) Since(from time.Time) []Chunk {
	a.mu.Lock()
	defer a.mu.Unlock()
	firstRun := -1
	for _, c := range a.chunks {
		if !c.At.Before(from) {
			firstRun = c.Run
			break
		}
	}
	if firstRun < 0 {
		return nil
	}
	var out []Chunk
	for _, c := range a.chunks {
		if c.Run >= firstRun {
			out = append(out, c)
		}
	}
	return out
}

func (a *AudioLog) trimLocked(now time.Time) {
	cut := 0
	for cut < len(a.chunks) && (now.Sub(a.chunks[cut].At) > keepFor || len(a.chunks)-cut > maxChunks) {
		cut++
	}
	if cut > 0 {
		a.chunks = append(a.chunks[:0], a.chunks[cut:]...)
	}
}
