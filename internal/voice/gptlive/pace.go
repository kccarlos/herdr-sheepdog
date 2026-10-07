package gptlive

import (
	"sync"
	"time"
)

// Input pacing (docs/research/voice-frameworks-2026-09-24.md P1, WP-A).
//
// GPT-Live wants "a continuous microphone stream paced at its recorded
// sample rate": the session timeline advances with input audio. The
// /ws client stops sending while its silence gate is closed — which is
// exactly while the model replies — so the daemon fills the gap with
// silence. It used to send one Tick of silence only when two Ticks had
// passed since the last send, so the input timeline advanced at ≤ 0.5×
// real time while the user listened. The pacer instead counts the
// audio sent on a connection against the wall clock since its first
// send, and keepAlive pays the difference (the debt) in silence.
const (
	// paceMaxBurst caps the silence one tick sends; debt beyond it (a
	// stalled daemon, a sleeping laptop) is forgiven and logged as
	// voice.input_debt_dropped rather than dumped on the model at once.
	paceMaxBurst = 200 * time.Millisecond
	// paceMaxLead forgives input sent ahead of the wall clock beyond
	// this (the pre-roll replayed on open, a client clock running fast),
	// so a lead cannot hold the keep-alive off for long.
	paceMaxLead = 2 * time.Second
	// paceClientTicks: the client counts as streaming while its last
	// frame is younger than this many Ticks (web frames are 20 ms, the
	// harness's 40 ms); no silence is mixed into its audio.
	paceClientTicks = 3
)

// audioSrc says where appended input audio came from.
type audioSrc int

const (
	srcClient  audioSrc = iota // the client's live frames
	srcReplay                  // client frames buffered while the session opened, sent late
	srcSilence                 // keepAlive's silence
)

// inputPacer is one connection's input-audio ledger, in 24 kHz samples.
// While the client streams, the ledger is left alone: a client that
// under-delivers for a long stretch leaves debt that is paid (up to
// paceMaxBurst, the rest forgiven) when its gate closes.
type inputPacer struct {
	mu         sync.Mutex
	anchor     time.Time // wall clock of the first send (zero: none yet)
	sent       int64     // samples sent since anchor, less forgiven lead, plus forgiven debt
	lastClient time.Time // last client (non-silence) audio
}

// add records n bytes of PCM16 sent at now. Replayed audio was
// captured in the past (the pre-roll and the frames buffered while the
// session opened): it moves the anchor back by its duration, so it is
// neither lead nor debt — counted as lead, it held the keep-alive off
// for 1–2 s right after the user's first utterance, when the model
// starts its reply.
func (p *inputPacer) add(now time.Time, n int, src audioSrc) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.anchor.IsZero() {
		p.anchor = now
	}
	samples := int64(n / 2)
	p.sent += samples
	switch src {
	case srcReplay:
		p.anchor = p.anchor.Add(-time.Duration(samples) * time.Second / SampleRate)
		p.lastClient = now
	case srcClient:
		p.lastClient = now
	}
}

// owe returns how many samples of silence to send at now (at most
// paceMaxBurst) and how much debt beyond that was forgiven. Nothing is
// owed before the first send or while the client streams (its last
// frame younger than quiet).
func (p *inputPacer) owe(now time.Time, quiet time.Duration) (samples int64, dropped time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.anchor.IsZero() || (!p.lastClient.IsZero() && now.Sub(p.lastClient) < quiet) {
		return 0, 0
	}
	wall := durSamples(now.Sub(p.anchor))
	debt := wall - p.sent
	if lead := durSamples(paceMaxLead); -debt > lead {
		p.sent = wall + lead
		return 0, 0
	}
	if debt <= 0 {
		return 0, 0
	}
	if burst := durSamples(paceMaxBurst); debt > burst {
		over := debt - burst
		p.sent += over
		dropped = time.Duration(over) * time.Second / SampleRate
		debt = burst
	}
	return debt, dropped
}

// durSamples converts a duration to 24 kHz samples (rounded down).
func durSamples(d time.Duration) int64 {
	return int64(d) * SampleRate / int64(time.Second)
}
