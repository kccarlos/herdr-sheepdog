package voiceclient

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// OutRate is the daemon's audio rate (server -> client).
const OutRate = wsproto.ServerSampleRate

// PlayerConfig is the jitter buffer (docs/ws-contract.md "Playback jitter
// buffer"); zero values take the Gemini numbers.
type PlayerConfig struct {
	StartTarget time.Duration // 150 ms
	Cap         time.Duration // 500 ms
	// StartWait is how long a short utterance that never reaches the
	// target waits before it plays anyway, past the target (250 ms).
	StartWait time.Duration
	// UnderrunWindow is how long a dry buffer waits for more audio
	// before the run counts as drained (300 ms).
	UnderrunWindow time.Duration
}

func (c PlayerConfig) withDefaults() PlayerConfig {
	if c.StartTarget <= 0 {
		c.StartTarget = 150 * time.Millisecond
	}
	if c.Cap <= 0 {
		c.Cap = 500 * time.Millisecond
	}
	if c.StartWait <= 0 {
		c.StartWait = 250 * time.Millisecond
	}
	if c.UnderrunWindow <= 0 {
		c.UnderrunWindow = 300 * time.Millisecond
	}
	return c
}

// Report is a playback report the player wants sent.
type Report struct {
	State    string // started, drained, flushed
	PlayedMS int
}

type playerState int

const (
	pIdle    playerState = iota // nothing buffered, no run
	pWaiting                    // buffering up to the target before the run starts
	pPlaying                    // consuming
	pDry                        // ran dry mid-run; within the underrun window
	pRefill                     // more audio came after a dry spell: refill to the target, then resume (no report)
)

// Player consumes the daemon's audio at real time in stream time, like
// the apps' player: it buffers to a start target, plays 24 kHz samples
// as wall time passes, and says when a run started, drained or was
// flushed with played_ms (audio of the run actually played). Time is
// passed in, so it is deterministic under test. Not safe for concurrent
// use (the Client locks around it).
type Player struct {
	cfg       PlayerConfig
	target    time.Duration
	state     playerState
	buf       []int16
	since     time.Time // when the current waiting/dry phase began
	last      time.Time // the last Tick
	frac      float64   // fractional samples owed by the clock
	played    int       // samples of the current run consumed
	underran  bool      // the current run had a mid-run underrun
	underruns int
	drains    int
}

// NewPlayer returns an idle player.
func NewPlayer(cfg PlayerConfig) *Player {
	cfg = cfg.withDefaults()
	return &Player{cfg: cfg, target: cfg.StartTarget}
}

func samplesFor(d time.Duration) int { return int(d * OutRate / time.Second) }

// BufferedMS is the audio received and not yet played.
func (p *Player) BufferedMS() int { return len(p.buf) * 1000 / OutRate }

// Active reports whether a run is under way or audio is waiting to play.
func (p *Player) Active() bool { return p.state != pIdle || len(p.buf) > 0 }

// Playing reports whether the speaker is producing the bot's voice now.
func (p *Player) Playing() bool { return p.state == pPlaying }

// Stats are the counters a ping carries.
func (p *Player) Stats() (underruns, drains, bufferMS int) {
	return p.underruns, p.drains, p.BufferedMS()
}

// Push appends received audio.
func (p *Player) Push(now time.Time, s []int16) {
	if len(s) == 0 {
		return
	}
	p.buf = append(p.buf, s...)
	switch p.state {
	case pIdle:
		p.state, p.since, p.played, p.underran = pWaiting, now, 0, false
	case pDry:
		// Mid-run underrun: raise the target and refill before resuming.
		p.underruns++
		p.underran = true
		p.target = min(p.target+100*time.Millisecond, p.cfg.Cap)
		p.state = pRefill
	}
}

// Tick advances the clock to now and returns the samples played since
// the last tick (silence is not returned) and any reports to send.
func (p *Player) Tick(now time.Time) (played []int16, reports []Report) {
	if p.last.IsZero() {
		p.last = now
	}
	dt := now.Sub(p.last)
	p.last = now
	if dt < 0 {
		dt = 0
	}
	switch p.state {
	case pWaiting:
		if len(p.buf) >= samplesFor(p.target) || now.Sub(p.since) >= p.target+p.cfg.StartWait {
			p.state = pPlaying
			reports = append(reports, Report{State: wsproto.PlaybackStarted})
			dt = 0 // playing starts now
		}
	case pRefill:
		if len(p.buf) >= samplesFor(p.target) {
			p.state = pPlaying
			dt = 0
		}
	}
	switch p.state {
	case pPlaying:
		p.frac += float64(dt) * OutRate / float64(time.Second)
		n := int(p.frac)
		p.frac -= float64(n)
		if n > len(p.buf) {
			n = len(p.buf)
		}
		played = append(played, p.buf[:n]...)
		p.buf = p.buf[n:]
		p.played += n
		if len(p.buf) == 0 {
			p.state, p.since, p.frac = pDry, now, 0
		}
	case pDry:
		if now.Sub(p.since) >= p.cfg.UnderrunWindow {
			reports = append(reports, Report{State: wsproto.PlaybackDrained, PlayedMS: p.played * 1000 / OutRate})
			p.drains++
			if !p.underran {
				p.target = max(p.target-20*time.Millisecond, p.cfg.StartTarget)
			}
			p.state, p.played = pIdle, 0
		}
	}
	return played, reports
}

// Flush drops everything buffered and ends the run: the flushed report
// (sent on every flush, with played_ms 0 when nothing played).
func (p *Player) Flush() Report {
	r := Report{State: wsproto.PlaybackFlushed, PlayedMS: p.played * 1000 / OutRate}
	p.buf, p.state, p.played, p.frac = nil, pIdle, 0, 0
	return r
}

// PlayedMS is the audio of the current run played so far.
func (p *Player) PlayedMS() int { return p.played * 1000 / OutRate }
