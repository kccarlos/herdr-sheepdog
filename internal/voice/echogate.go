package voice

import (
	"math"
	"sort"
	"time"
)

// EchoGate keeps the voice's own speech, leaking from the phone's speaker
// back into its microphone, from reaching the voice model
// (docs/research/echo-2026-10-03.md). Gemini Live's activity detection
// takes that echo for the shepherd: it cuts the reply off, transcribes
// it and answers itself (docs/bench/tier2-2026-10-03.md finding 3).
//
// While the client reports that it plays the model's audio (playback
// started until drained, plus an echo tail), microphone frames are held
// back and compared with what was played: the energy envelope of each
// 10 ms of microphone audio against the envelope of the reply's audio,
// over a range of delays. A window the reply explains (the envelopes
// move together and nothing is left over above the echo's level) is
// echo and is dropped; anything else — the shepherd talking over the
// reply, with or without echo under it — is released at once, held
// frames first so the model hears the start of the words, and the gate
// stays open until the client's silence gate closes. The decision is
// scale free: no level threshold, so it works the same on a phone whose
// echo canceller leaves −45 dB and on one that leaves −15 dB.
//
// When unsure it lets audio through: a missed echo costs one self
// interruption (what happens without the gate), a missed barge-in a
// shepherd who cannot stop the voice. Clients that never send playback
// reports are not gated. Not safe for concurrent use: the session calls
// it under its own lock.
type EchoGate struct {
	// reference: the reply's audio as 10 ms block powers, queued until
	// the client plays them, then placed on the play timeline.
	refCarry []byte
	pending  []float64
	played   []echoBlock
	playing  bool
	playhead time.Time
	reports  bool

	// microphone: frames held back while the reply plays, with their
	// block powers on the microphone timeline.
	held      []heldFrame
	segStart  time.Time
	segFrames int
	lastFrame time.Time
	pass      bool
	echoRun   int
	unsure    int
	// candLag / candN: the delay a streak of echo matches began at and
	// how many successive windows matched near it.
	candLag int
	candN   int
	// learnedHere: the echo's level was first learned from the frames
	// held now and not yet confirmed by echoConfirmations more echo
	// windows, so the echo verdicts are not trusted to keep any out
	// (speech that happened to match the reply's start).
	learnedHere bool
	confirmed   int
	// echoAt is the last held frame a window judged echo ended on.
	echoAt time.Time
	// recent are the last microphone block powers (the floor).
	recent []float64
	// coupling is the echo's microphone/reply power ratio, learned from
	// the windows judged echo (0: no echo seen yet).
	coupling float64
	lagMs    int

	stats EchoGateStats
}

// EchoGateStats counts what the gate did (voice stats, bench).
type EchoGateStats struct {
	Withheld int // frames dropped as echo
	Released int // held frames released on a shepherd verdict
	Opens    int // shepherd verdicts while the reply played
}

type echoBlock struct {
	at  time.Time
	pow float64
}

type heldFrame struct {
	data   []byte
	at     time.Time
	blocks [echoBlocksPerFrame]float64
}

const (
	echoBlock10ms      = 10 * time.Millisecond
	echoFrame          = 40 * time.Millisecond
	echoBlocksPerFrame = 4
	// echoMicBlockBytes / echoRefBlockBytes: 10 ms of 16 kHz input and
	// of 24 kHz output, PCM16.
	echoMicBlockBytes = 320
	echoRefBlockBytes = 480
	// EchoTail is how long after the last played audio the gate still
	// compares: the acoustic delay plus the report and network skew.
	EchoTail = 800 * time.Millisecond
	// echoMaxLag bounds the delay searched between the play timeline and
	// the microphone timeline (acoustic delay, output latency, a burst
	// of pre-roll the client released at once).
	echoMaxLag = 1000 * time.Millisecond
	// echoMinBlocks / echoWindowBlocks: the window a verdict needs and
	// looks at (240 / 320 ms).
	echoMinBlocks    = 24
	echoWindowBlocks = 32
	// echoHoldFrames bounds the frames held while undecided or echo
	// (960 ms): what a shepherd verdict releases ahead of the words.
	echoHoldFrames = 24
	// echoExitFrames: consecutive echo verdicts that close an open gate
	// again while the reply still plays (960 ms).
	echoExitFrames = 24
	// echoUnsureFrames: consecutive unsure verdicts after which held
	// audio is let through (640 ms, all of it still held).
	echoUnsureFrames = 16
	// echoReleaseOverlap: of the frames judged echo, how much is
	// released ahead of the shepherd's words anyway.
	echoReleaseOverlap = 80 * time.Millisecond
	// echoUsualLag: the echo delays to rule out before the first verdict
	// that a window is the shepherd's.
	echoUsualLag = 400 * time.Millisecond
	// echoSteadyFrames / echoLagSlackMs: an echo match must hold its
	// delay (±30 ms) over three successive windows.
	echoSteadyFrames = 3
	// echoMaxSpreadDB: the most the microphone/reply ratio of an echo
	// spreads (interquartile, dB) over a window.
	echoMaxSpreadDB = 4.0
	// echoConfirmations: echo windows after the first that confirm the
	// echo's level before its verdicts may keep held audio out.
	echoConfirmations = 8
	echoLagSlackMs    = 30
	// echoCorr is the envelope correlation at or above which the reply
	// may explain the window; echoResidual the share of the window's
	// energy above the echo's level (+6 dB) below which it does.
	echoCorr     = 0.7
	echoResidual = 0.35
	echoMargin   = 4.0 // +6 dB, as a power ratio
	// echoFirstCorr: the stricter correlation the first echo verdict
	// needs, before the echo's coupling is known.
	echoFirstCorr = 0.8
	// echoCouplingSpread: a window more than 10 dB louder, relative to
	// the reply, than the echo has been is not echo.
	echoCouplingSpread = 10.0
	// echoFloorPow: −70 dBFS, the floor under every block's power.
	echoFloorPow = 1e-7
	// echoRefActive: a reference block counts as sound above −55 dBFS.
	echoRefActive  = 3.2e-6
	echoPlayedKeep = 4 * time.Second
	// echoSoundOver: a block is sound 10 dB above the microphone floor.
	echoSoundOver  = 10.0
	echoRecentKeep = 400 // blocks (4 s)
)

// Out records output audio (24 kHz PCM16) queued for the client.
func (g *EchoGate) Out(b []byte) {
	if len(b) == 0 {
		return
	}
	buf := append(g.refCarry, b...)
	for len(buf) >= echoRefBlockBytes {
		g.pending = append(g.pending, blockPower(buf[:echoRefBlockBytes]))
		buf = buf[echoRefBlockBytes:]
	}
	g.refCarry = append([]byte(nil), buf...)
}

// Playback applies a client playback report.
func (g *EchoGate) Playback(now time.Time, state string) {
	g.reports = true
	g.advance(now)
	switch state {
	case PlaybackStarted:
		if !g.playing && g.playhead.Before(now) {
			g.playhead = now
		}
		g.playing = true
	case PlaybackDrained:
		g.playing = false
	case PlaybackFlushed:
		g.playing = false
		g.pending, g.refCarry = nil, nil
	}
}

// Flush records that the daemon dropped the output (an interruption):
// nothing queued will play.
func (g *EchoGate) Flush(now time.Time) {
	g.advance(now)
	g.pending, g.refCarry = nil, nil
}

// Reset forgets the client (it went away or was replaced).
func (g *EchoGate) Reset() {
	stats := g.stats
	*g = EchoGate{stats: stats}
}

// Stats reports the gate's counters.
func (g *EchoGate) Stats() EchoGateStats { return g.stats }

// EchoVerdict is one decision the gate made, for the session's log.
type EchoVerdict struct {
	State    EchoState
	Open     bool    // the shepherd: frames released
	Corr     float64 // best envelope correlation
	Residual float64 // share of energy the reply leaves unexplained
	LagMs    int
	Released int

	coupling float64
	full     bool
}

// EchoState is what a window of microphone audio was judged to be.
type EchoState int

const (
	// EchoUnsure: not enough of the reply under the window yet, or a
	// match too loose to trust before the echo's level is known.
	EchoUnsure EchoState = iota
	// EchoQuiet: no sound above the microphone's floor.
	EchoQuiet
	// EchoIsEcho: the reply explains the window.
	EchoIsEcho
	// EchoShepherd: sound the reply does not explain.
	EchoShepherd
)

// Mic takes one microphone frame (16 kHz PCM16) and returns the frames
// to send to the model now (none while it holds echo back), and a
// verdict when the gate opened on this frame.
func (g *EchoGate) Mic(now time.Time, frame []byte) ([][]byte, *EchoVerdict) {
	at := g.stamp(now)
	g.advance(now)
	f := heldFrame{at: at}
	for i := 0; i < echoBlocksPerFrame; i++ {
		lo := i * echoMicBlockBytes
		if lo+echoMicBlockBytes > len(frame) {
			f.blocks[i] = echoFloorPow
			continue
		}
		f.blocks[i] = blockPower(frame[lo : lo+echoMicBlockBytes])
	}
	g.recent = append(g.recent, f.blocks[:]...)
	if len(g.recent) > echoRecentKeep {
		g.recent = g.recent[len(g.recent)-echoRecentKeep:]
	}
	if !g.active(now) {
		out, v := g.settle()
		g.pass, g.echoRun, g.unsure = false, 0, 0
		return append(out, frame), v
	}
	f.data = append([]byte(nil), frame...)
	g.held = append(g.held, f)
	if g.pass {
		// Open: keep a window of history to notice echo alone again.
		if len(g.held) > echoWindowBlocks/echoBlocksPerFrame {
			g.held = g.held[len(g.held)-echoWindowBlocks/echoBlocksPerFrame:]
		}
		switch g.judge().State {
		case EchoIsEcho:
			g.echoRun++
			if g.echoRun >= echoExitFrames {
				g.pass, g.echoRun = false, 0
				g.held = nil
				g.stats.Withheld++
				return nil, nil
			}
		case EchoShepherd:
			g.echoRun = 0
		}
		return [][]byte{frame}, nil
	}
	if len(g.held)*echoBlocksPerFrame < echoMinBlocks {
		return nil, nil
	}
	v := g.judge()
	switch v.State {
	case EchoShepherd:
		out := g.open()
		v.Open, v.Released = true, len(out)
		return out, &v
	case EchoUnsure:
		// Unsure for long: let it through rather than eat the words.
		if g.unsure++; g.unsure >= echoUnsureFrames {
			out := g.open()
			v.Open, v.Released = true, len(out)
			return out, &v
		}
	case EchoIsEcho:
		g.unsure = 0
		g.echoAt = f.at
	default:
		g.unsure = 0
	}
	if len(g.held) > echoHoldFrames {
		g.stats.Withheld += len(g.held) - echoHoldFrames
		g.held = g.held[len(g.held)-echoHoldFrames:]
	}
	return nil, nil
}

// StreamEnd is the client's silence gate closing: frames still held are
// judged on what there is (a short word over the reply) and returned to
// send when they are not echo.
func (g *EchoGate) StreamEnd(now time.Time) ([][]byte, *EchoVerdict) {
	g.advance(now)
	out, v := g.settle()
	g.pass, g.echoRun, g.unsure, g.lastFrame, g.echoAt = false, 0, 0, time.Time{}, time.Time{}
	return out, v
}

// settle decides the held frames once the comparison is over: echo and
// silence are dropped, anything else is let through.
func (g *EchoGate) settle() ([][]byte, *EchoVerdict) {
	if g.pass || len(g.held) == 0 {
		g.held = nil
		return nil, nil
	}
	if len(g.held)*echoBlocksPerFrame < echoMinBlocks/2 {
		// Too short to be words, too short to judge: a click.
		g.stats.Withheld += len(g.held)
		g.held = nil
		return nil, nil
	}
	v := g.judge()
	if v.State == EchoIsEcho || v.State == EchoQuiet {
		g.stats.Withheld += len(g.held)
		g.held = nil
		return nil, nil
	}
	out := g.open()
	v.Open, v.Released = true, len(out)
	g.pass = false
	return out, &v
}

// open releases the held frames on a shepherd verdict.
func (g *EchoGate) open() [][]byte {
	out := make([][]byte, 0, len(g.held))
	for _, h := range g.held {
		// Frames a window already judged echo stay out, but for the
		// last 80 ms of it: the words may have begun at its tail.
		if !g.echoAt.IsZero() && !g.learnedHere && h.at.Before(g.echoAt.Add(-echoReleaseOverlap)) {
			g.stats.Withheld++
			continue
		}
		out = append(out, h.data)
	}
	g.echoAt = time.Time{}
	g.stats.Released += len(out)
	g.stats.Opens++
	g.held = g.held[:0]
	g.pass, g.echoRun = true, 0
	return out
}

// stamp places a frame on the microphone timeline. Frames come at real
// time; a frame after a pause starts a segment and the next ones follow
// at 40 ms steps, but never ahead of their arrival: a burst (the
// client's pre-roll, released at once when its gate opens) is audio
// from before it arrived, so the segment shifts back to end at now.
func (g *EchoGate) stamp(now time.Time) time.Time {
	if g.lastFrame.IsZero() || now.Sub(g.lastFrame) > 250*time.Millisecond {
		g.segStart, g.segFrames = now, 0
	}
	g.lastFrame = now
	at := g.segStart.Add(time.Duration(g.segFrames) * echoFrame)
	g.segFrames++
	if d := at.Sub(now); d > 0 {
		g.segStart = g.segStart.Add(-d)
		at = now
		for i := range g.held {
			g.held[i].at = g.held[i].at.Add(-d)
		}
	}
	return at
}

// advance moves the reply's queued blocks onto the play timeline up to
// now while the client plays.
func (g *EchoGate) advance(now time.Time) {
	if g.playing {
		for len(g.pending) > 0 && !g.playhead.After(now) {
			g.played = append(g.played, echoBlock{at: g.playhead, pow: g.pending[0]})
			g.pending = g.pending[1:]
			g.playhead = g.playhead.Add(echoBlock10ms)
		}
		if len(g.pending) == 0 && g.playhead.Before(now.Add(-echoBlock10ms)) {
			g.playhead = now // an underrun: the next audio plays when it comes
		}
	}
	cut := 0
	for cut < len(g.played) && now.Sub(g.played[cut].at) > echoPlayedKeep {
		cut++
	}
	if cut > 0 {
		g.played = append(g.played[:0], g.played[cut:]...)
	}
}

// active: the client reports playback and the reply plays, or played
// within the echo tail.
func (g *EchoGate) active(now time.Time) bool {
	if !g.reports {
		return false
	}
	if g.playing && (len(g.pending) > 0 || len(g.played) > 0) {
		return true
	}
	return len(g.played) > 0 && now.Sub(g.played[len(g.played)-1].at) < EchoTail
}

// judge compares the last window of held microphone blocks with the
// play timeline at every lag. Echo: at some lag the envelopes move
// together and little energy rises above the echo's level. Only the
// blocks the reply had already reached at that lag count, so the start
// of a reply is not judged on the silence before it.
func (g *EchoGate) judge() EchoVerdict {
	type mb struct {
		at  time.Time
		pow float64
	}
	var mic []mb
	for _, h := range g.held {
		for i, p := range h.blocks {
			mic = append(mic, mb{at: h.at.Add(time.Duration(i) * echoBlock10ms), pow: p})
		}
	}
	if len(mic) > echoWindowBlocks {
		mic = mic[len(mic)-echoWindowBlocks:]
	}
	floor := g.micFloor() * echoSoundOver
	sound := 0
	for _, m := range mic {
		if m.pow > floor {
			sound++
		}
	}
	if sound < len(mic)/4 {
		return EchoVerdict{State: EchoQuiet, Corr: -1}
	}
	if len(g.played) == 0 {
		return EchoVerdict{State: EchoShepherd, Corr: -1, Residual: 1}
	}
	start := g.played[0].at.Add(-echoBlock10ms)
	need := echoMinBlocks
	if g.coupling == 0 {
		need = echoWindowBlocks - echoBlocksPerFrame // the first echo verdict needs (nearly) a full window
	}
	var x, lx, y, ly, ratios []float64
	best := EchoVerdict{State: EchoUnsure, Corr: -1, Residual: 1}
	usable, loose := time.Duration(-1), false
	for lag := time.Duration(0); lag <= echoMaxLag; lag += echoBlock10ms {
		x, lx, y, ly = x[:0], lx[:0], y[:0], ly[:0]
		activeRef := 0
		for _, m := range mic {
			t := m.at.Add(-lag)
			if t.Before(start) {
				continue // before the reply began
			}
			xv := math.Max(g.refAt(t), echoFloorPow)
			yv := math.Max(m.pow, echoFloorPow)
			x, lx = append(x, xv), append(lx, math.Log10(xv))
			y, ly = append(y, yv), append(ly, math.Log10(yv))
			if xv >= echoRefActive {
				activeRef++
			}
		}
		if len(x) < need {
			continue
		}
		usable = lag
		if activeRef < len(x)/4 {
			continue // the reply was nearly silent here: it explains nothing
		}
		r := pearson(lx, ly)
		if r < echoCorr {
			if best.State != EchoIsEcho && r > best.Corr {
				best.Corr, best.LagMs = r, int(lag/time.Millisecond)
			}
			continue
		}
		// The echo's level: the median microphone/reply ratio where the
		// reply sounds; what rises above it (+6 dB) is not the reply.
		ratios = ratios[:0]
		for i := range x {
			if x[i] >= echoRefActive {
				ratios = append(ratios, y[i]/x[i])
			}
		}
		sort.Float64s(ratios)
		coupling := ratios[len(ratios)/2]
		// An echo is the reply at one level: the ratio barely moves from
		// block to block (interquartile spread), where speech that only
		// shares the reply's rhythm swings by many dB.
		if spread := 10 * math.Log10(ratios[3*len(ratios)/4]/ratios[len(ratios)/4]); spread > echoMaxSpreadDB {
			if best.State != EchoIsEcho && r > best.Corr {
				best.Corr, best.LagMs = r, int(lag/time.Millisecond)
			}
			continue
		}
		if g.coupling == 0 && r < echoFirstCorr {
			loose = true // might be echo; not sure enough for a first verdict
			if best.State != EchoIsEcho && r > best.Corr {
				best.Corr, best.LagMs = r, int(lag/time.Millisecond)
			}
			continue
		}
		if g.coupling > 0 && coupling > g.coupling*echoCouplingSpread {
			continue // far louder than the echo has been: not the reply
		}
		k := coupling * echoMargin
		var excess, total float64
		for i := range x {
			total += y[i]
			if e := y[i] - k*x[i]; e > 0 {
				excess += e
			}
		}
		res := excess / total
		lagMs := int(lag / time.Millisecond)
		if res < echoResidual && (best.State != EchoIsEcho || g.better(lagMs, r, best)) {
			best = EchoVerdict{State: EchoIsEcho, Corr: r, Residual: res, LagMs: lagMs, coupling: coupling, full: len(x) >= echoWindowBlocks-echoBlocksPerFrame}
		} else if best.State != EchoIsEcho && r > best.Corr {
			best.Corr, best.Residual, best.LagMs = r, res, int(lag/time.Millisecond)
		}
	}
	if best.State == EchoIsEcho && !g.steady(best.LagMs) {
		// A match at a delay of its own: speech-like sound matches the
		// reply somewhere among a hundred delays now and then; an echo
		// keeps its delay.
		best.State = EchoUnsure
		return best
	}
	if best.State != EchoIsEcho {
		g.candN = 0
	}
	switch {
	case best.State == EchoIsEcho:
		if best.full {
			g.learn(best.coupling, best.LagMs)
		}
	case usable < g.lagNeeded() || loose:
		best.State = EchoUnsure
	default:
		best.State = EchoShepherd
	}
	return best
}

// better: of two echo matches, the one at the echo's known delay, else
// the closer one.
func (g *EchoGate) better(lagMs int, r float64, cur EchoVerdict) bool {
	if g.coupling > 0 {
		near, curNear := absInt(lagMs-g.lagMs) <= echoLagSlackMs, absInt(cur.LagMs-g.lagMs) <= echoLagSlackMs
		if near != curNear {
			return near
		}
	}
	return r > cur.Corr
}

// steady follows the delay of successive echo matches: a match counts
// once it held its delay over echoSteadyFrames windows, or at once at
// the echo's known delay.
func (g *EchoGate) steady(lagMs int) bool {
	// candLag is the delay the streak began at: an echo stays there, a
	// chance match drifts.
	if g.candN > 0 && absInt(lagMs-g.candLag) <= echoLagSlackMs {
		g.candN++
	} else {
		g.candN, g.candLag = 1, lagMs
	}
	if g.coupling > 0 && absInt(lagMs-g.lagMs) <= echoLagSlackMs {
		return true
	}
	return g.candN >= echoSteadyFrames
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// learn follows the echo's coupling (log average) and delay from full
// echo windows.
func (g *EchoGate) learn(c float64, lagMs int) {
	if c <= 0 {
		return
	}
	g.lagMs = lagMs
	if g.coupling == 0 {
		g.coupling, g.learnedHere, g.confirmed = c, true, 0
		return
	}
	if g.confirmed++; g.confirmed >= echoConfirmations {
		g.learnedHere = false
	}
	g.coupling = math.Exp(0.8*math.Log(g.coupling) + 0.2*math.Log(c))
}

// lagNeeded is how far back the comparison must reach before a window
// may be called the shepherd's: past the echo's known delay, or past
// the delays usual on a phone when none is known yet.
func (g *EchoGate) lagNeeded() time.Duration {
	if g.coupling > 0 {
		return time.Duration(g.lagMs)*time.Millisecond + 60*time.Millisecond
	}
	return echoUsualLag
}

// micFloor is the microphone's floor: a low percentile of the recent
// block powers (the pauses between words, the room).
func (g *EchoGate) micFloor() float64 {
	if len(g.recent) < 2*echoWindowBlocks {
		return echoFloorPow // too little history to tell the room from the words
	}
	s := append([]float64(nil), g.recent...)
	sort.Float64s(s)
	return math.Max(s[len(s)/10], echoFloorPow)
}

// refAt is the reply's block power playing at t (the nearest block
// within 10 ms; the floor when nothing played then).
func (g *EchoGate) refAt(t time.Time) float64 {
	i := sort.Search(len(g.played), func(i int) bool { return !g.played[i].at.Before(t) })
	best, bd := 0.0, echoBlock10ms
	for _, j := range []int{i - 1, i} {
		if j < 0 || j >= len(g.played) {
			continue
		}
		d := g.played[j].at.Sub(t)
		if d < 0 {
			d = -d
		}
		if d < bd || (d == bd && g.played[j].pow > best) {
			best, bd = g.played[j].pow, d
		}
	}
	return best
}

// blockPower is the mean square of PCM16 little-endian samples, full
// scale 1.
func blockPower(b []byte) float64 {
	n := len(b) / 2
	if n == 0 {
		return 0
	}
	var s float64
	for i := 0; i < n; i++ {
		v := float64(int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8))
		s += v * v
	}
	return s / float64(n) / (32768 * 32768)
}

// pearson is the correlation of a and b (0 when either is flat).
func pearson(a, b []float64) float64 {
	n := float64(len(a))
	if n == 0 {
		return 0
	}
	var ma, mb float64
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma /= n
	mb /= n
	var sab, saa, sbb float64
	for i := range a {
		da, db := a[i]-ma, b[i]-mb
		sab += da * db
		saa += da * da
		sbb += db * db
	}
	if saa < 1e-9 || sbb < 1e-9 {
		return 0
	}
	return sab / math.Sqrt(saa*sbb)
}
