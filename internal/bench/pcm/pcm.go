// Package pcm holds the small sample-level helpers the voice bench
// shares (internal/bench/speech, internal/bench/voiceclient): PCM16
// conversion, a band-limited resampler (the bot speaks 24 kHz, the
// microphone sends 16 kHz) and the speaker-echo line that leaks the
// bot's own voice back into the microphone.
package pcm

import (
	"encoding/binary"
	"math"
)

// FromBytes reads PCM16 little-endian bytes as samples (a trailing odd
// byte is ignored).
func FromBytes(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return out
}

// ToBytes renders samples as PCM16 little-endian bytes.
func ToBytes(s []int16) []byte {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

// Peak is the largest absolute sample.
func Peak(s []int16) int {
	m := 0
	for _, v := range s {
		a := int(v)
		if a < 0 {
			a = -a
		}
		if a > m {
			m = a
		}
	}
	return m
}

// RMS is the root mean square of s (0 for none).
func RMS(s []int16) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(s)))
}

// Clip16 rounds v to the nearest int16, saturating.
func Clip16(v float64) int16 {
	v = math.Round(v)
	switch {
	case v > math.MaxInt16:
		return math.MaxInt16
	case v < math.MinInt16:
		return math.MinInt16
	}
	return int16(v)
}

// DurationMS is how long n samples last at rate.
func DurationMS(n, rate int) int {
	if rate <= 0 {
		return 0
	}
	return int(int64(n) * 1000 / int64(rate))
}

// Resample converts s from rate `from` to rate `to`: a windowed-sinc
// low-pass at 0.45 of the lower rate (so a downsample does not alias),
// then interpolation at the new sample positions. Good enough for speech
// into a speech recogniser; not a mastering resampler.
func Resample(s []int16, from, to int) []int16 {
	if from == to || len(s) == 0 || from <= 0 || to <= 0 {
		return append([]int16(nil), s...)
	}
	cutoff := 0.45 * float64(min(from, to)) / float64(from) // cycles per input sample
	const half = 16                                         // taps each side
	n := int(int64(len(s)) * int64(to) / int64(from))
	out := make([]int16, n)
	step := float64(from) / float64(to)
	for i := range out {
		pos := float64(i) * step
		c := int(math.Floor(pos))
		var acc, wsum float64
		for k := c - half + 1; k <= c+half; k++ {
			if k < 0 || k >= len(s) {
				continue
			}
			x := pos - float64(k)
			w := 2 * cutoff * sinc(2*cutoff*x) * blackman(x, half)
			acc += w * float64(s[k])
			wsum += w
		}
		if wsum != 0 {
			acc /= wsum
		}
		out[i] = Clip16(acc)
	}
	return out
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// blackman is a Blackman window over [-half, half].
func blackman(x float64, half int) float64 {
	t := (x + float64(half)) / float64(2*half)
	if t < 0 || t > 1 {
		return 0
	}
	return 0.42 - 0.5*math.Cos(2*math.Pi*t) + 0.08*math.Cos(4*math.Pi*t)
}

// EchoLine is the acoustic path from the phone's speaker back into its
// microphone: what the player plays (at the mic rate) comes out of Next
// DelayMS later, attenuated by GainDB (negative). A phone's echo
// canceller removes most of it; the residue that leaks through is what
// this simulates. The zero value is silent; not safe for concurrent use.
type EchoLine struct {
	gain  float64
	delay int
	buf   []int16 // samples waiting to come out, oldest first
	init  bool
}

// NewEchoLine returns an echo line at rate with the delay and gain.
func NewEchoLine(rate, delayMS int, gainDB float64) *EchoLine {
	e := &EchoLine{gain: math.Pow(10, gainDB/20), delay: rate * delayMS / 1000}
	return e
}

// Feed adds what the speaker just played (mic-rate samples). Silence
// while nothing plays must be fed too (or the delay collapses); Next
// does that for the caller when Feed falls behind.
func (e *EchoLine) Feed(played []int16) {
	if e == nil {
		return
	}
	e.prime()
	e.buf = append(e.buf, played...)
}

// Next returns the next n samples of echo at the microphone.
func (e *EchoLine) Next(n int) []int16 {
	out := make([]int16, n)
	if e == nil {
		return out
	}
	e.prime()
	if len(e.buf) < n {
		// The speaker was silent: pad so the delay stays the delay.
		e.buf = append(e.buf, make([]int16, n-len(e.buf))...)
	}
	for i := 0; i < n; i++ {
		out[i] = Clip16(float64(e.buf[i]) * e.gain)
	}
	e.buf = e.buf[n:]
	return out
}

func (e *EchoLine) prime() {
	if !e.init {
		e.buf = make([]int16, e.delay)
		e.init = true
	}
}

// Mix adds b onto a sample by sample (a's length), saturating.
func Mix(a, b []int16) []int16 {
	out := make([]int16, len(a))
	for i := range a {
		v := float64(a[i])
		if i < len(b) {
			v += float64(b[i])
		}
		out[i] = Clip16(v)
	}
	return out
}
