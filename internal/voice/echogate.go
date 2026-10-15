package voice

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

// EchoGate keeps the voice's own speech, leaking from the phone's speaker
// back into its microphone, from reaching the voice model
// (docs/dev/research/echo-2026-10-03.md). Gemini Live's activity detection
// takes that echo for the user: it cuts the reply off, transcribes
// it and answers itself (docs/dev/bench/tier2-2026-10-03.md finding 3).
//
// While the client reports that it plays the model's audio (playback
// started until drained, plus an echo tail), microphone frames are held
// back and compared with what was played: the energy envelope of each
// 10 ms of microphone audio against the envelope of the reply's audio,
// over a range of delays. A window the reply explains (the envelopes
// move together and nothing is left over above the echo's level) is
// echo and is dropped; anything else — the user talking over the
// reply, with or without echo under it — is released at once, held
// frames first so the model hears the start of the words, and the gate
// stays open until the client's silence gate closes. The decision is
// scale free: no level threshold, so it works the same on a phone whose
// echo canceller leaves −45 dB and on one that leaves −15 dB.
//
// When unsure it lets audio through: a missed echo costs one self
// interruption (what happens without the gate), a missed barge-in a
// user who cannot stop the voice. Clients that never send playback
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
	// runStart is when the current reply began playing (after a pause
	// longer than the echo tail).
	runStart time.Time

	// microphone: frames held back while the reply plays, with their
	// block powers on the microphone timeline.
	held      []heldFrame
	segStart  time.Time
	segFrames int
	lastFrame time.Time
	pass      bool
	echoRun   int
	unsure    int
	// burst: this frame came with the previous one (pre-roll released at
	// once); the frames' places on the timeline settle once it is over,
	// so nothing is judged until then.
	burst bool
	// firstLag / firstN: before the gate is armed, the delay a streak of
	// echo matches began at and its length.
	firstLag int
	firstN   int
	// recent are the last microphone block powers (the floor).
	recent []float64
	// armed: this client's echo has shown itself; until then nothing is
	// held back.
	armed bool
	// said is the reply's recent words (Heard).
	said string
	// replyOnset: the microphone opened right as the current reply
	// began; selfCuts counts interruptions that followed one (Flush).
	replyOnset time.Time
	selfCuts   int
	// coupling is the echo's microphone/reply power ratio, learned from
	// the windows judged echo (0: none yet).
	coupling float64
	lagMs    int

	stats EchoGateStats
}

// EchoGateStats counts what the gate did (voice stats, bench).
type EchoGateStats struct {
	Withheld int // frames dropped as echo
	Released int // held frames released on a user verdict
	Opens    int // user verdicts while the reply played
	Armed    bool
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
	// echoMinLag: the echo may lead the play timeline a little: the
	// client reports "started" once its player has begun, a few frames
	// after the first sound left the speaker (on the bench the echo's
	// delay at the daemon measured 10–50 ms).
	echoMinLag = -200 * time.Millisecond
	// echoMinBlocks / echoWindowBlocks: the window a verdict needs and
	// looks at (240 / 320 ms).
	echoMinBlocks    = 24
	echoWindowBlocks = 32
	// echoHoldFrames bounds the frames held while undecided or echo
	// (960 ms): what a user verdict releases ahead of the words.
	echoHoldFrames = 24
	// echoExitFrames: consecutive echo verdicts that close an open gate
	// again while the reply still plays (960 ms).
	echoExitFrames = 24
	// echoUnsureFrames: consecutive unsure verdicts after which held
	// audio is let through (640 ms, all of it still held).
	echoUnsureFrames = 16
	// echoUsualLag: the echo delays to rule out before the first verdict
	// that a window is the user's.
	echoUsualLag = 400 * time.Millisecond
	// echoCorr is the envelope correlation at or above which the reply
	// may explain the window; echoResidual the share of the window's
	// energy above the echo's level (+6 dB) below which it does.
	echoCorr     = 0.7
	echoResidual = 0.35
	echoMargin   = 4.0 // +6 dB, as a power ratio
	// echoFirstCorr: the stricter correlation the first echo verdict
	// needs, before the echo's coupling is known.
	echoFirstCorr = 0.9
	// echoFirstSteady: successive full windows (600 ms of sliding, about
	// three windows that do not overlap) the arming match must hold its
	// delay over.
	echoFirstSteady = 15
	// echoSaidKeep: the reply's words kept for Heard (runes);
	// echoHeardWithin: how long after the reply a transcript of its
	// words still counts as its echo.
	echoSaidKeep    = 400
	echoHeardWithin = 3 * time.Second
	// echoOnsetWithin / echoSelfCutWithin / echoSelfCuts: a microphone
	// opening within 400 ms of a reply's start, an interruption within
	// 1.5 s of it, twice, arm the gate.
	echoOnsetWithin   = 400 * time.Millisecond
	echoSelfCutWithin = 1500 * time.Millisecond
	echoSelfCuts      = 2
	// echoCouplingSpread: a window more than 6 dB louder, relative to
	// the reply, than the echo has been is not echo.
	echoCouplingSpread = 4.0
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
// nothing queued will play. An interruption that follows a microphone
// opening right at the start of the reply is the model cutting itself
// off on its echo; the second one arms the gate (it reports whether it
// armed now). A client leaking echo shows itself this way first: the
// model cuts the reply within half a second, too soon for the audio
// comparison to be sure.
func (g *EchoGate) Flush(now time.Time) bool {
	g.advance(now)
	g.pending, g.refCarry = nil, nil
	onset := g.replyOnset
	g.replyOnset = time.Time{}
	if g.armed || onset.IsZero() || now.Sub(onset) > echoSelfCutWithin {
		return false
	}
	if g.selfCuts++; g.selfCuts < echoSelfCuts {
		return false
	}
	g.armed, g.stats.Armed = true, true
	return true
}

// Reset forgets the client (it went away or was replaced).
func (g *EchoGate) Reset() {
	stats := g.stats
	stats.Armed = false // the next client shows its own echo, or not
	*g = EchoGate{stats: stats}
}

// Stats reports the gate's counters.
func (g *EchoGate) Stats() EchoGateStats { return g.stats }

// EchoVerdict is one decision the gate made, for the session's log.
type EchoVerdict struct {
	State    EchoState
	Open     bool    // the user: frames released
	Armed    bool    // the client's echo showed itself: the gate holds from now on
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
// verdict when the gate armed or opened on this frame.
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
	if g.segFrames == 1 && g.playing && !g.runStart.IsZero() {
		if d := now.Sub(g.runStart); d >= -100*time.Millisecond && d <= echoOnsetWithin {
			g.replyOnset = now // the microphone opened as the reply began
		}
	}
	if !g.active(now) {
		out, v := g.settle()
		g.pass, g.echoRun, g.unsure = false, 0, 0
		return append(out, frame), v
	}
	f.data = append([]byte(nil), frame...)
	g.held = append(g.held, f)
	if !g.armed {
		// Not armed: until this client's echo has shown itself (a strong
		// match that holds its delay, or the model hearing the reply's
		// own words: Heard), nothing is held back — the microphone flows
		// as it would without the gate, and the frames are only watched.
		if len(g.held) > echoWindowBlocks/echoBlocksPerFrame {
			g.held = g.held[len(g.held)-echoWindowBlocks/echoBlocksPerFrame:]
		}
		if !g.burst && len(g.held)*echoBlocksPerFrame >= echoWindowBlocks {
			if v := g.judge(); v.State == EchoIsEcho && g.coupling > 0 {
				g.held = nil // armed from the next frame on
				g.armed, g.stats.Armed = true, true
				v.Armed = true
				return [][]byte{frame}, &v
			}
		}
		return [][]byte{frame}, nil
	}
	if g.pass {
		// Open: keep a window of history to notice echo alone again.
		if len(g.held) > echoWindowBlocks/echoBlocksPerFrame {
			g.held = g.held[len(g.held)-echoWindowBlocks/echoBlocksPerFrame:]
		}
		if g.burst {
			return [][]byte{frame}, nil
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
	if g.burst || len(g.held)*echoBlocksPerFrame < echoMinBlocks {
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
	g.pass, g.echoRun, g.unsure, g.lastFrame = false, 0, 0, time.Time{}
	return out, v
}

// settle decides the held frames once the comparison is over: echo and
// silence are dropped, anything else is let through.
func (g *EchoGate) settle() ([][]byte, *EchoVerdict) {
	if g.pass || !g.armed || len(g.held) == 0 {
		// Open, or unarmed (the held frames are copies already sent).
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

// open releases the held frames on a user verdict.
func (g *EchoGate) open() [][]byte {
	out := make([][]byte, 0, len(g.held))
	for _, h := range g.held {
		out = append(out, h.data)
	}
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
	g.burst = !g.lastFrame.IsZero() && now.Sub(g.lastFrame) < 20*time.Millisecond
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
			if n := len(g.played); n == 0 || g.playhead.Sub(g.played[n-1].at) > EchoTail {
				g.runStart = g.playhead // a reply after a pause
			}
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
	// Only what the current reply can explain: the microphone before it
	// began (the user, the room, an older reply's tail) is not
	// judged against the silence between replies.
	start := g.runStart.Add(-echoBlock10ms)
	need := echoMinBlocks
	if g.coupling == 0 {
		need = echoWindowBlocks - echoBlocksPerFrame // the first echo verdict needs (nearly) a full window
	}
	var x, lx, y, ly, ratios []float64
	best := EchoVerdict{State: EchoUnsure, Corr: -1, Residual: 1}
	usable, loose := echoMinLag-echoBlock10ms, false
	for lag := echoMinLag; lag <= echoMaxLag; lag += echoBlock10ms {
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
		if res < echoResidual && (best.State != EchoIsEcho || res < best.Residual) {
			best = EchoVerdict{State: EchoIsEcho, Corr: r, Residual: res, LagMs: lagMs, coupling: coupling, full: len(x) >= echoWindowBlocks-echoBlocksPerFrame}
		} else if best.State != EchoIsEcho && r > best.Corr {
			best.Corr, best.Residual, best.LagMs = r, res, int(lag/time.Millisecond)
		}
	}
	if best.State == EchoIsEcho && !g.armed && !g.firstSteady(best.LagMs, best.full) {
		best.State = EchoUnsure
		return best
	}
	if best.State != EchoIsEcho {
		g.firstN = 0
	}
	switch {
	case best.State == EchoIsEcho:
		if best.full {
			g.learn(best.coupling, best.LagMs)
		}
	case usable < g.lagNeeded() || loose || !g.covered(mic[0].at, start):
		best.State = EchoUnsure
	default:
		best.State = EchoShepherd
	}
	return best
}

// Said records the reply's words as the model speaks them (its output
// transcript).
func (g *EchoGate) Said(text string) {
	g.said += text
	if r := []rune(g.said); len(r) > echoSaidKeep {
		g.said = string(r[len(r)-echoSaidKeep:])
	}
}

// Heard takes what the model heard the user say so far this turn
// (its input transcript) and arms the gate when it is the reply's own
// words heard while the reply played or just after: the speaker's echo
// reached the model. That is how a leaking client usually shows itself
// first — the model cuts its reply off on the echo within half a
// second, too soon for the audio comparison to be sure. It reports
// whether the gate armed now.
func (g *EchoGate) Heard(now time.Time, heard string) bool {
	if g.armed || !g.reports {
		return false
	}
	g.advance(now)
	if !g.playing && (len(g.played) == 0 || now.Sub(g.played[len(g.played)-1].at) > echoHeardWithin) {
		return false
	}
	if !EchoText(heard, g.said) {
		return false
	}
	g.armed, g.stats.Armed = true, true
	return true
}

// EchoText reports whether heard is (mostly) the words of said, in
// their order: at least two of its word pairs (Han characters count as
// words), and half of them, appear in said. Pairs, not words: the
// user's own sentence often shares words with the reply that
// answers it, rarely their run.
func EchoText(heard, said string) bool {
	h, sd := echoTokens(heard), echoTokens(said)
	if len(h) < 3 || len(sd) < 2 {
		return false
	}
	in := map[string]bool{}
	for i := 1; i < len(sd); i++ {
		in[sd[i-1]+" "+sd[i]] = true
	}
	n := 0
	for i := 1; i < len(h); i++ {
		if in[h[i-1]+" "+h[i]] {
			n++
		}
	}
	return n >= 2 && n*2 >= len(h)-1
}

// echoTokens splits text into lower-case words of letters and digits;
// each Han character is a word of its own.
func echoTokens(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
		}
		cur = cur[:0]
	}
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.Is(unicode.Han, r):
			flush()
			out = append(out, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// firstSteady: before the gate is armed, an echo match must hold its
// delay (±30 ms of where the streak began) over echoFirstSteady full
// windows. Speech that shares the reply's rhythm matches it somewhere
// among a hundred delays now and then; an echo stays put. (Once armed,
// the delay may wander as the client's player underruns; the echo's
// level then keeps the user apart.)
func (g *EchoGate) firstSteady(lagMs int, full bool) bool {
	if d := lagMs - g.firstLag; full && g.firstN > 0 && d >= -30 && d <= 30 {
		g.firstN++
	} else if full {
		g.firstN, g.firstLag = 1, lagMs
	} else {
		g.firstN = 0
	}
	return g.firstN >= echoFirstSteady
}

// learn follows the echo's coupling (log average) and delay (average)
// from full echo windows.
func (g *EchoGate) learn(c float64, lagMs int) {
	if c <= 0 {
		return
	}
	if g.coupling == 0 {
		g.coupling, g.lagMs = c, lagMs
		return
	}
	g.coupling = math.Exp(0.8*math.Log(g.coupling) + 0.2*math.Log(c))
	// The delay follows slowly too: it wanders as the player underruns,
	// but one odd window must not move it.
	g.lagMs = (4*g.lagMs + lagMs) / 5
}

// covered: the whole window, shifted by the echo's delay, lies within
// the current reply. A window that still holds the moment before the
// reply's echo arrived is not judged the user's: the echo's attack
// against the quiet before it matches the reply poorly.
func (g *EchoGate) covered(first, start time.Time) bool {
	return !first.Add(-g.lagNeeded()).Before(start)
}

// lagNeeded is how far back the comparison must reach before a window
// may be called the user's: past the echo's known delay, or past
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
