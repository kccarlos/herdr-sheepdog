package audioe2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wav"
)

// Rate is the /ws input sample rate.
const Rate = 16000

// Say renders text with macOS `say` and converts it to 16 kHz PCM16 mono
// with afconvert, cached by (voice, text) in cacheDir.
func Say(ctx context.Context, text, voiceName, cacheDir string) ([]int16, error) {
	sum := sha256.Sum256([]byte(voiceName + "\x00" + text))
	base := filepath.Join(cacheDir, "say-"+hex.EncodeToString(sum[:])[:16])
	wavPath := base + ".wav"
	if _, err := os.Stat(wavPath); err != nil {
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return nil, err
		}
		aiff := base + ".aiff"
		if out, err := exec.CommandContext(ctx, "say", "-v", voiceName, "-o", aiff, text).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("say: %v: %s", err, out)
		}
		if out, err := exec.CommandContext(ctx, "afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", aiff, wavPath).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("afconvert: %v: %s", err, out)
		}
		_ = os.Remove(aiff)
	}
	b, err := os.ReadFile(wavPath)
	if err != nil {
		return nil, err
	}
	rate, pcm, err := wav.Read(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if rate != Rate {
		return nil, fmt.Errorf("%s: %d Hz, want %d", wavPath, rate, Rate)
	}
	return toSamples(pcm), nil
}

func toSamples(pcm []byte) []int16 {
	out := make([]int16, len(pcm)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(pcm[i*2:]))
	}
	return out
}

// ToPCM renders samples as PCM16 LE bytes.
func ToPCM(s []int16) []byte {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

// RMS of samples; VoicedRMS only over 20 ms windows above -50 dBFS, so
// the silence around speech does not dilute it.
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

// VoicedRMS is the RMS of the 20 ms windows louder than -50 dBFS.
func VoicedRMS(s []int16) float64 {
	const win = Rate / 50
	floor := dbfsToRMS(-50)
	var sum float64
	n := 0
	for i := 0; i+win <= len(s); i += win {
		if r := RMS(s[i : i+win]); r >= floor {
			sum += r * r
			n++
		}
	}
	if n == 0 {
		return RMS(s)
	}
	return math.Sqrt(sum / float64(n))
}

func dbfsToRMS(db float64) float64 { return 32767 * math.Pow(10, db/20) }

// Noise generates n samples of kind (white, pink, brown, cafe) at RMS 1
// (unit scale, float), deterministic for a seed.
func NoiseSamples(kind string, n int, seed int64) ([]float64, error) {
	r := rand.New(rand.NewSource(seed + 1))
	out := make([]float64, n)
	switch kind {
	case "", "white":
		for i := range out {
			out[i] = r.NormFloat64()
		}
	case "pink":
		// Paul Kellet's economy pink filter over white noise.
		var b0, b1, b2 float64
		for i := range out {
			w := r.NormFloat64()
			b0 = 0.99765*b0 + w*0.0990460
			b1 = 0.96300*b1 + w*0.2965164
			b2 = 0.57000*b2 + w*1.0526913
			out[i] = b0 + b1 + b2 + w*0.1848
		}
	case "brown":
		var last float64
		for i := range out {
			last = 0.998*last + r.NormFloat64()*0.05
			out[i] = last
		}
	case "cafe":
		// A café bed: pink noise under a slow swell (voices coming and
		// going), band-limited "babble" bursts and the odd cup clink.
		pink, _ := NoiseSamples("pink", n, seed)
		var lp float64
		clinkLeft, clinkPhase := 0, 0.0
		for i := range out {
			t := float64(i) / Rate
			swell := 0.7 + 0.3*math.Sin(2*math.Pi*0.23*t+1.3) + 0.15*math.Sin(2*math.Pi*1.7*t)
			// babble: white noise low-passed to the speech band, gated by
			// a syllable-rate envelope
			lp = 0.85*lp + 0.15*r.NormFloat64()
			syll := math.Max(0, math.Sin(2*math.Pi*4.1*t+0.5*math.Sin(2*math.Pi*0.9*t)))
			v := pink[i]*swell + lp*syll*1.8
			if clinkLeft == 0 && r.Float64() < 1.0/(Rate*3) {
				clinkLeft, clinkPhase = Rate/25, 0
			}
			if clinkLeft > 0 {
				env := float64(clinkLeft) / float64(Rate/25)
				v += 4 * env * math.Sin(clinkPhase)
				clinkPhase += 2 * math.Pi * 3100 / Rate
				clinkLeft--
			}
			out[i] = v
		}
	default:
		return nil, fmt.Errorf("unknown noise kind %q (white, pink, brown, cafe)", kind)
	}
	// Normalise to RMS 1.
	var sum float64
	for _, v := range out {
		sum += v * v
	}
	if rms := math.Sqrt(sum / float64(max(n, 1))); rms > 0 {
		for i := range out {
			out[i] /= rms
		}
	}
	return out, nil
}

// Mix adds noise (unit RMS) scaled to rms onto s, clipping.
func Mix(s []int16, noise []float64, rms float64) []int16 {
	out := make([]int16, len(s))
	for i := range s {
		v := float64(s[i])
		if i < len(noise) {
			v += noise[i] * rms
		}
		out[i] = clip16(v)
	}
	return out
}

func clip16(v float64) int16 {
	v = math.Round(v)
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
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
