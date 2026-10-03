package bench

import (
	"math"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/audioe2e"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
)

// Bed is a looping background-noise bed (the audioe2e generators: white,
// pink, brown, cafe) at an adjustable level, for voiceclient.Options.Bed.
// Safe for concurrent use.
type Bed struct {
	mu    sync.Mutex
	noise []float64 // unit RMS
	pos   int
	rms   float64
	floor []float64 // a quiet microphone self-noise floor
	fpos  int
	frms  float64
}

// bedLen is how much noise is generated up front (then looped): 30 s.
const bedLen = 30 * 16000

// NewBed returns a bed of kind ("" or "none": silent) with seed.
func NewBed(kind string, seed int64) (*Bed, error) {
	b := &Bed{}
	if kind != "" && kind != "none" {
		n, err := audioe2e.NoiseSamples(kind, bedLen, seed)
		if err != nil {
			return nil, err
		}
		b.noise = n
	}
	return b, nil
}

// SetSNR sets the level SNR dB below speech's voiced RMS.
func (b *Bed) SetSNR(speech []int16, snrDB float64) {
	b.SetRMS(audioe2e.VoicedRMS(speech) / math.Pow(10, snrDB/20))
}

// SetDBFS sets an absolute level.
func (b *Bed) SetDBFS(db float64) { b.SetRMS(32767 * math.Pow(10, db/20)) }

// SetRMS sets the level as an RMS in Int16 LSB (0: silent).
func (b *Bed) SetRMS(rms float64) {
	b.mu.Lock()
	b.rms = rms
	b.mu.Unlock()
}

// SetFloor adds a white self-noise floor at db dBFS (0: none): a real
// microphone is never digitally silent.
func (b *Bed) SetFloor(db float64, seed int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if db >= 0 {
		b.floor, b.frms = nil, 0
		return
	}
	b.floor, _ = audioe2e.NoiseSamples("white", 16000*7, seed+77)
	b.frms = 32767 * math.Pow(10, db/20)
}

// Next returns n samples of bed.
func (b *Bed) Next(n int) []int16 {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]int16, n)
	if len(b.noise) == 0 && len(b.floor) == 0 {
		return out
	}
	for i := range out {
		var v float64
		if len(b.noise) > 0 && b.rms > 0 {
			v += b.noise[b.pos] * b.rms
			b.pos = (b.pos + 1) % len(b.noise)
		}
		if len(b.floor) > 0 {
			v += b.floor[b.fpos] * b.frms
			b.fpos = (b.fpos + 1) % len(b.floor)
		}
		out[i] = pcm.Clip16(v)
	}
	return out
}
