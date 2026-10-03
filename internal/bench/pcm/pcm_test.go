package pcm

import (
	"math"
	"testing"
)

func tone(freq float64, rate, n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = Clip16(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

func TestBytesRoundTrip(t *testing.T) {
	s := []int16{0, 1, -1, 32767, -32768, 1234}
	if got := FromBytes(ToBytes(s)); len(got) != len(s) || got[3] != 32767 || got[4] != -32768 || got[5] != 1234 {
		t.Fatalf("round trip = %v", got)
	}
}

// A 1 kHz tone survives 24k -> 16k at the right length and level; a tone
// above the new Nyquist (10 kHz) is filtered rather than aliased.
func TestResample(t *testing.T) {
	in := tone(1000, 24000, 24000, 10000)
	out := Resample(in, 24000, 16000)
	if len(out) != 16000 {
		t.Fatalf("len = %d, want 16000", len(out))
	}
	if r := RMS(out[100 : len(out)-100]); math.Abs(r-10000/math.Sqrt2) > 300 {
		t.Fatalf("1 kHz RMS after resample = %.0f, want ≈ %.0f", r, 10000/math.Sqrt2)
	}
	hi := Resample(tone(10000, 24000, 24000, 10000), 24000, 16000)
	if r := RMS(hi[100 : len(hi)-100]); r > 1500 {
		t.Fatalf("10 kHz tone leaked through at RMS %.0f (aliasing)", r)
	}
	if got := Resample(in[:10], 16000, 16000); len(got) != 10 {
		t.Fatal("same-rate resample changed the length")
	}
}

func TestEchoLine(t *testing.T) {
	e := NewEchoLine(16000, 100, -20) // 1600 samples delay, x0.1
	e.Feed([]int16{10000, 10000})
	out := e.Next(1600)
	if Peak(out) != 0 {
		t.Fatal("echo came out before the delay")
	}
	out = e.Next(4)
	if out[0] != 1000 || out[1] != 1000 || out[2] != 0 {
		t.Fatalf("echo = %v, want [1000 1000 0 0]", out)
	}
	var nilLine *EchoLine
	if Peak(nilLine.Next(10)) != 0 {
		t.Fatal("a nil echo line is not silent")
	}
	if m := Mix([]int16{32000, -5}, []int16{32000}); m[0] != 32767 || m[1] != -5 {
		t.Fatalf("mix = %v", m)
	}
}
