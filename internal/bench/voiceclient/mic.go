package voiceclient

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// InRate is the microphone rate (client -> server).
const InRate = wsproto.ClientSampleRate

// Frame is the streaming frame: 40 ms of 16 kHz PCM16, at real time.
const (
	FrameDur     = 40 * time.Millisecond
	FrameSamples = InRate / 25
)

// The apps' silence gate (docs/ws-contract.md "Silence gate").
const (
	gateFloor   = 8
	gateHold    = 300 * time.Millisecond
	gatePreroll = 100 * time.Millisecond
)

// The client speech detector (docs/ws-contract.md "Client speech
// detector"): it runs on the shepherd's own (clean) speech, the way a
// phone's voice processing does after its echo canceller, so the bot's
// echo or the room never opens it.
const (
	vadFloor = 300
	vadHang  = 500 * time.Millisecond
)

// micOut is what one frame tick sends: control messages first, then the
// binary frames, in that order.
type micOut struct {
	msgs   []wsproto.Message
	frames [][]int16
}

// mic is the capture pipeline in front of /ws: the silence gate (frames
// below 8 LSB for 300 ms close it; the next loud one reopens it after
// the last 100 ms of withheld audio) and the speech detector. Not safe
// for concurrent use.
type mic struct {
	gateOff  bool // send every frame (the apps' ?gate=off)
	closed   bool
	quietFor time.Duration
	preroll  [][]int16
	gated    int // frames withheld so far
	peak     int // last input peak

	sendSpeaking bool
	speaking     bool
	unvoiced     time.Duration
}

// push takes one frame: what goes to the daemon (frame, the mixed
// microphone signal) and whether the shepherd's own speech is in it
// (voice, the clean speech's peak), for the detector.
func (m *mic) push(frame []int16, voicePeak int) micOut {
	var out micOut
	m.peak = pcm.Peak(frame)
	if m.sendSpeaking {
		voiced := voicePeak >= vadFloor
		switch {
		case voiced && !m.speaking:
			m.speaking, m.unvoiced = true, 0
			out.msgs = append(out.msgs, wsproto.Speaking(true))
		case voiced:
			m.unvoiced = 0
		case m.speaking:
			m.unvoiced += FrameDur
			if m.unvoiced >= vadHang {
				m.speaking = false
				out.msgs = append(out.msgs, wsproto.Speaking(false))
			}
		}
	}
	if m.gateOff {
		out.frames = append(out.frames, frame)
		return out
	}
	loud := m.peak >= gateFloor
	if m.closed {
		if !loud {
			m.gated++
			m.preroll = append(m.preroll, frame)
			if len(m.preroll) > int(gatePreroll/FrameDur) {
				m.preroll = m.preroll[1:]
			}
			return out
		}
		// Reopen: the last 100 ms withheld, then this frame.
		m.closed, m.quietFor = false, 0
		out.msgs = append(out.msgs, wsproto.Silence(false))
		out.frames = append(append(out.frames, m.preroll...), frame)
		m.preroll = nil
		return out
	}
	if loud {
		m.quietFor = 0
	} else {
		m.quietFor += FrameDur
		if m.quietFor >= gateHold {
			m.closed = true
			m.gated++
			out.msgs = append(out.msgs, wsproto.Silence(true))
			return out
		}
	}
	out.frames = append(out.frames, frame)
	return out
}
