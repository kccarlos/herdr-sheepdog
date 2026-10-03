package voice

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

// speechLike is n samples of syllable-shaped noise: bursts of 80–250 ms
// with an uneven envelope (random attack and decay, a few dB of
// flutter every 10 ms, like the energy of real syllables), pauses of
// 30–200 ms, peak amplitude amp.
func speechLike(seed int64, n int, amp float64) []int16 {
	const rate = 16000
	r := rand.New(rand.NewSource(seed))
	out := make([]int16, n)
	i := 0
	block := rate / 100
	for i < n {
		syl := rate * (80 + r.Intn(170)) / 1000
		level := amp * (0.4 + 0.6*r.Float64())
		attack := 0.15 + 0.5*r.Float64()
		flutter := 1.0
		for j := 0; j < syl && i < n; j, i = j+1, i+1 {
			if j%block == 0 {
				flutter = math.Pow(10, (r.Float64()*6-3)/20)
			}
			ph := float64(j) / float64(syl)
			env := ph / attack
			if ph > attack {
				env = (1 - ph) / (1 - attack)
			}
			out[i] = int16(math.Max(-32768, math.Min(32767, level*env*flutter*(r.Float64()*2-1))))
		}
		i += rate * (30 + r.Intn(170)) / 1000
	}
	return out
}

func noise(seed int64, n int, amp float64) []int16 {
	r := rand.New(rand.NewSource(seed))
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * (r.Float64()*2 - 1))
	}
	return out
}

// up24 resamples 16 kHz to 24 kHz (sample and hold: only the block
// powers matter here).
func up24(in []int16) []int16 {
	out := make([]int16, len(in)*3/2)
	for i := range out {
		out[i] = in[i*2/3]
	}
	return out
}

func pcmBytes(s []int16) []byte {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		b[2*i], b[2*i+1] = byte(v), byte(uint16(v)>>8)
	}
	return b
}

func gain(s []int16, db float64) []int16 {
	g := math.Pow(10, db/20)
	out := make([]int16, len(s))
	for i, v := range s {
		out[i] = int16(math.Max(-32768, math.Min(32767, float64(v)*g)))
	}
	return out
}

// echoScene is one reply played by a reporting client with an open mic
// gate: the bot's speech, its echo, optionally the shepherd's speech.
type echoScene struct {
	replyMs  int
	echoDB   float64 // 0: no echo
	delayMs  int
	ceoAtMs  int // shepherd starts this long after playback starts (-1: never)
	ceoMs    int
	ceoAmp   float64
	floorAmp float64
}

type sceneResult struct {
	sentDuringReply int // frames forwarded while the reply played
	firstSentMs     int // stream time of the first forwarded frame (-1)
	firstFrameMs    int // stream time of the earliest frame among those forwarded
	gate            *EchoGate
}

func (sc echoScene) run(t *testing.T) sceneResult {
	t.Helper()
	const rate = 16000
	t0 := time.Unix(5000, 0)
	bot := speechLike(1, rate*sc.replyMs/1000, 16000)
	totalMs := sc.replyMs + 1500
	n := rate * totalMs / 1000
	mic := noise(7, n, math.Max(sc.floorAmp, 6))
	if sc.echoDB != 0 {
		e := gain(bot, sc.echoDB)
		d := rate * sc.delayMs / 1000
		for i := range e {
			if i+d < n {
				mic[i+d] += e[i]
			}
		}
	}
	if sc.ceoAtMs >= 0 {
		ceo := speechLike(99, rate*sc.ceoMs/1000, sc.ceoAmp)
		at := rate * sc.ceoAtMs / 1000
		for i, v := range ceo {
			if at+i < n {
				mic[at+i] = int16(math.Max(-32768, math.Min(32767, float64(mic[at+i])+float64(v))))
			}
		}
	}
	g := &EchoGate{}
	g.Out(pcmBytes(up24(bot))) // the model sends faster than real time
	g.Playback(t0, PlaybackStarted)
	res := sceneResult{firstSentMs: -1, firstFrameMs: -1, gate: g}
	frameN := rate / 25
	// map of forwarded frame bytes back to stream time
	idx := map[string]int{}
	drained := false
	for k := 0; k*frameN+frameN <= n; k++ {
		ms := k * 40
		now := t0.Add(time.Duration(ms) * time.Millisecond)
		if !drained && ms >= sc.replyMs {
			g.Playback(now, PlaybackDrained)
			drained = true
		}
		f := pcmBytes(mic[k*frameN : (k+1)*frameN])
		idx[string(f)] = ms
		out, _ := g.Mic(now, f)
		for _, o := range out {
			if res.firstSentMs < 0 {
				res.firstSentMs = ms
			}
			if at, ok := idx[string(o)]; ok && (res.firstFrameMs < 0 || at < res.firstFrameMs) {
				res.firstFrameMs = at
			}
			if ms < sc.replyMs {
				res.sentDuringReply++
			}
		}
	}
	return res
}

func TestEchoGatePassesEverythingWhenNothingPlays(t *testing.T) {
	g := &EchoGate{}
	now := time.Unix(1, 0)
	g.Playback(now, PlaybackDrained) // a reporting client, nothing queued
	f := pcmBytes(speechLike(3, 640, 12000))
	for i := 0; i < 20; i++ {
		out, _ := g.Mic(now.Add(time.Duration(i)*40*time.Millisecond), f)
		if len(out) != 1 {
			t.Fatalf("frame %d: %d frames out, want 1 (no reply plays)", i, len(out))
		}
	}
}

func TestEchoGateIgnoresClientsWithoutReports(t *testing.T) {
	g := &EchoGate{}
	g.Out(pcmBytes(up24(speechLike(1, 16000*3, 16000))))
	f := pcmBytes(speechLike(3, 640, 12000))
	out, _ := g.Mic(time.Unix(1, 0), f)
	if len(out) != 1 {
		t.Fatalf("%d frames out, want 1: no playback reports, no gate", len(out))
	}
}

func TestEchoGateDropsSpeakerEcho(t *testing.T) {
	for _, db := range []float64{-10, -16, -18, -30, -40} {
		res := echoScene{replyMs: 6000, echoDB: db, delayMs: 120, ceoAtMs: -1, floorAmp: 6}.run(t)
		if res.sentDuringReply > 2 {
			t.Errorf("echo %v dB: %d frames of echo forwarded during the reply, want ≤ 2 (stats %+v)", db, res.sentDuringReply, res.gate.Stats())
		}
	}
}

func TestEchoGateDropsEchoWithLongerDelays(t *testing.T) {
	for _, d := range []int{40, 200, 350} {
		res := echoScene{replyMs: 5000, echoDB: -16, delayMs: d, ceoAtMs: -1, floorAmp: 6}.run(t)
		if res.sentDuringReply > 2 {
			t.Errorf("delay %d ms: %d frames forwarded, want ≤ 2", d, res.sentDuringReply)
		}
	}
}

func TestEchoGateLetsBargeInThrough(t *testing.T) {
	cases := []struct {
		name   string
		echoDB float64
		ceoAmp float64
	}{
		{"no echo", 0, 12000},
		{"no echo, quiet shepherd", 0, 1500},
		{"echo -40", -40, 12000},
		{"echo -16", -16, 12000},
		{"echo -16, quieter shepherd", -16, 6000},
		{"echo -10", -10, 12000},
	}
	for _, c := range cases {
		res := echoScene{replyMs: 6000, echoDB: c.echoDB, delayMs: 120, ceoAtMs: 2000, ceoMs: 1500, ceoAmp: c.ceoAmp, floorAmp: 6}.run(t)
		if res.firstSentMs < 0 {
			t.Errorf("%s: barge-in never forwarded (stats %+v)", c.name, res.gate.Stats())
			continue
		}
		if lat := res.firstSentMs - 2000; lat < 0 || lat > 500 {
			t.Errorf("%s: first frame forwarded %d ms after the shepherd began, want 0–500", c.name, lat)
		}
		if res.firstFrameMs > 2040 {
			t.Errorf("%s: the start of the words was not released (earliest frame at %d ms)", c.name, res.firstFrameMs)
		}
		if res.sentDuringReply < 1500/40-5 {
			t.Errorf("%s: only %d frames forwarded during the reply, want most of the 1.5 s", c.name, res.sentDuringReply)
		}
	}
}

func TestEchoGateShortWordAtGateClose(t *testing.T) {
	// "Stop." (300 ms) over the reply, then the client's silence gate
	// closes: what is held is judged and released.
	g := &EchoGate{}
	t0 := time.Unix(9000, 0)
	bot := speechLike(1, 16000*4, 16000)
	g.Out(pcmBytes(up24(bot)))
	g.Playback(t0, PlaybackStarted)
	word := speechLike(42, 16000*3/10, 12000)
	var sent int
	for k := 0; k < 5; k++ {
		f := pcmBytes(word[k*640 : min((k+1)*640, len(word))])
		if len(f) < 1280 {
			f = append(f, make([]byte, 1280-len(f))...)
		}
		out, _ := g.Mic(t0.Add(time.Duration(1000+40*k)*time.Millisecond), f)
		sent += len(out)
	}
	out, v := g.StreamEnd(t0.Add(1300 * time.Millisecond))
	sent += len(out)
	if sent < 5 || v == nil && sent == 0 {
		t.Fatalf("short word over the reply: %d frames sent, want all 5 (verdict %+v)", sent, v)
	}
}

func TestEchoGateFlushEndsTheReply(t *testing.T) {
	g := &EchoGate{}
	t0 := time.Unix(9000, 0)
	g.Out(pcmBytes(up24(speechLike(1, 16000*6, 16000))))
	g.Playback(t0, PlaybackStarted)
	g.Flush(t0.Add(time.Second))
	g.Playback(t0.Add(1100*time.Millisecond), PlaybackFlushed)
	f := pcmBytes(speechLike(3, 640, 12000))
	out, _ := g.Mic(t0.Add(3*time.Second), f)
	if len(out) != 1 {
		t.Fatalf("%d frames out after a flush, want 1", len(out))
	}
}

func TestEchoGateBurstOfPreroll(t *testing.T) {
	// The client's gate opens on the echo and releases its pre-roll at
	// once: ten frames arrive together, then real time.
	const rate = 16000
	t0 := time.Unix(7000, 0)
	bot := speechLike(1, rate*5, 16000)
	n := rate * 6
	mic := noise(7, n, 6)
	e := gain(bot, -16)
	for i := range e {
		if i+rate/10 < n {
			mic[i+rate/10] += e[i]
		}
	}
	g := &EchoGate{}
	g.Out(pcmBytes(up24(bot)))
	g.Playback(t0, PlaybackStarted)
	sent := 0
	for k := 0; k*640+640 <= rate*5; k++ {
		arrive := k * 40
		if k < 10 {
			arrive = 400 // the burst
		}
		if arrive < 400 {
			continue
		}
		out, _ := g.Mic(t0.Add(time.Duration(arrive)*time.Millisecond), pcmBytes(mic[k*640:(k+1)*640]))
		sent += len(out)
	}
	if sent > 2 {
		t.Fatalf("%d frames of echo forwarded after a pre-roll burst, want ≤ 2 (stats %+v)", sent, g.Stats())
	}
}

func TestEchoGateBargeInAtTheStartOfAReply(t *testing.T) {
	for _, db := range []float64{0, -16} {
		res := echoScene{replyMs: 5000, echoDB: db, delayMs: 120, ceoAtMs: 80, ceoMs: 1500, ceoAmp: 12000, floorAmp: 6}.run(t)
		if res.firstSentMs < 0 || res.firstSentMs > 80+900 {
			t.Errorf("echo %v dB: shepherd talking as the reply starts first forwarded at %d ms, want by %d", db, res.firstSentMs, 80+900)
		}
		if res.firstFrameMs > 120 {
			t.Errorf("echo %v dB: the start of the words was not released (earliest %d ms)", db, res.firstFrameMs)
		}
	}
}

// sweep plays one 5 s reply (seed) with the echo at echoDB (0: none)
// and the shepherd from ceoAt ms (-1: never); it returns the barge-in
// latency, where the first released frame starts relative to the words,
// and the frames forwarded before the words began.
func sweep(echoDB float64, ceoAt int, seed int64) (lat, onsetMiss int, leaked int) {
	const rate = 16000
	t0 := time.Unix(5000, 0)
	bot := speechLike(seed, rate*5, 16000)
	n := rate * 6
	mic := noise(7, n, 6)
	if echoDB != 0 {
		e := gain(bot, echoDB)
		for i := range e {
			if i+rate*12/100 < n {
				mic[i+rate*12/100] += e[i]
			}
		}
	}
	idx := map[string]int{}
	if ceoAt >= 0 {
		ceo := speechLike(seed+1000, rate*3/2, 12000)
		at := rate * ceoAt / 1000
		for i, v := range ceo {
			mic[at+i] = int16(math.Max(-32768, math.Min(32767, float64(mic[at+i])+float64(v))))
		}
	}
	g := &EchoGate{}
	g.Out(pcmBytes(up24(bot)))
	g.Playback(t0, PlaybackStarted)
	first, earliest := -1, -1
	for k := 0; k < 125; k++ {
		ms := k * 40
		f := pcmBytes(mic[k*640 : (k+1)*640])
		idx[string(f)] = ms
		out, _ := g.Mic(t0.Add(time.Duration(ms)*time.Millisecond), f)
		for _, o := range out {
			if ceoAt < 0 || ms < ceoAt {
				leaked++
			}
			if first < 0 && ceoAt >= 0 && ms >= ceoAt {
				first = ms
			}
			if a := idx[string(o)]; ceoAt >= 0 && (earliest < 0 || a < earliest) && a >= ceoAt-200 {
				earliest = a
			}
		}
	}
	return first - ceoAt, earliest - ceoAt, leaked
}

// TestEchoGateSweep runs many replies and shepherds (seeds), the
// shepherd starting with the reply, early in it and late: every
// barge-in gets through with its first words, and echo alone stays out.
func TestEchoGateSweep(t *testing.T) {
	for _, db := range []float64{0, -16, -40} {
		bad, leaks, lats := 0, 0, 0
		maxLat := 0
		for seed := int64(1); seed <= 15; seed++ {
			for _, at := range []int{80, 700, 2000} {
				lat, on, lk := sweep(db, at, seed)
				lats += lat
				if lat > maxLat {
					maxLat = lat
				}
				if lat < 0 || lat > 1000 || on > 40 {
					bad++
					t.Logf("echo %v dB seed %d at %d ms: latency %d ms, first words from %+d ms", db, seed, at, lat, on)
				}
				leaks += lk
			}
		}
		echoOnlyLeak := 0
		for seed := int64(1); seed <= 15 && db != 0; seed++ {
			_, _, lk := sweep(db, -1, seed)
			echoOnlyLeak += lk
		}
		t.Logf("echo %v dB: barge-ins late or clipped %d/45, mean latency %d ms, max %d; echo-only frames forwarded %d over 15 replies", db, bad, lats/45, maxLat, echoOnlyLeak)
		if bad > 0 {
			t.Errorf("echo %v dB: %d of 45 barge-ins late (> 1 s) or without their first words", db, bad)
		}
		if echoOnlyLeak > 15*5 {
			t.Errorf("echo %v dB: %d frames of echo alone forwarded over 15 replies, want ≤ 5 a reply", db, echoOnlyLeak)
		}
	}
}
