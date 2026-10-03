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

// Gate modes.
const (
	// GateIOS is the iOS app's capture gate (SheepdogKit CaptureGate +
	// VoiceActivityDetector): a voice-activity detector opens it after
	// 120 ms of voiced audio within 280 ms and closes it after 400 ms
	// unvoiced; speaking opens and closes with it; the opening sends the
	// attack plus 100 ms of pre-roll. The default.
	GateIOS = "ios"
	// GateWeb is the /voice page's peak gate (docs/ws-contract.md
	// "Silence gate"): below 8 LSB for 300 ms closes it, the next louder
	// frame reopens it with 100 ms of pre-roll; speaking follows a
	// separate detector on the shepherd's speech.
	GateWeb = "web"
	// GateOff sends every frame.
	GateOff = "off"
)

const (
	webFloor   = 8
	webHold    = 300 * time.Millisecond
	preroll    = 100 * time.Millisecond
	iosAttack  = 3  // voiced frames (120 ms) ...
	iosWindow  = 7  // ... within the last 7 (280 ms)
	iosHang    = 10 // unvoiced frames (400 ms) close it
	voiceFloor = 300
	webVADHang = 500 * time.Millisecond
)

// micOut is what one frame tick sends: control messages first, then the
// binary frames, in that order.
type micOut struct {
	msgs   []wsproto.Message
	frames [][]int16
}

// mic is the capture pipeline in front of /ws. The detector runs on the
// shepherd's speech plus the echo that leaks past the phone's echo
// canceller, not on the room's steady noise (the app's detector adapts
// its floor to that). Not safe for concurrent use.
type mic struct {
	mode         string
	sendSpeaking bool

	open     bool // gate open (frames flow)
	held     [][]int16
	voiced   []bool // last iosWindow frames
	unvoiced int
	quietFor time.Duration
	gated    int // frames withheld so far
	peak     int // last input peak

	speaking bool
	silentFor time.Duration
}

func newMic(mode string, sendSpeaking bool) *mic {
	if mode == "" {
		mode = GateIOS
	}
	return &mic{mode: mode, sendSpeaking: sendSpeaking, open: mode != GateIOS}
}

// reset is the state of a fresh connection: the iOS gate starts closed
// (and says so), the web gate open.
func (m *mic) reset() []wsproto.Message {
	m.held, m.voiced, m.unvoiced, m.quietFor, m.speaking, m.silentFor = nil, nil, 0, 0, false, 0
	if m.mode == GateIOS {
		m.open = false
		return []wsproto.Message{wsproto.Silence(true)}
	}
	m.open = true
	return nil
}

// push takes one frame: frame is the mixed microphone signal, voicePeak
// the peak of what the detector hears.
func (m *mic) push(frame []int16, voicePeak int) micOut {
	m.peak = pcm.Peak(frame)
	switch m.mode {
	case GateOff:
		return micOut{msgs: m.webSpeaking(voicePeak), frames: [][]int16{frame}}
	case GateWeb:
		out := micOut{msgs: m.webSpeaking(voicePeak)}
		m.webGate(frame, &out)
		return out
	}
	return m.iosGate(frame, voicePeak >= voiceFloor)
}

func (m *mic) iosGate(frame []int16, voiced bool) micOut {
	var out micOut
	m.voiced = append(m.voiced, voiced)
	if len(m.voiced) > iosWindow {
		m.voiced = m.voiced[1:]
	}
	if !m.open {
		m.held = append(m.held, frame)
		if max := iosWindow + int(preroll/FrameDur); len(m.held) > max {
			m.held = m.held[len(m.held)-max:]
		}
		n := 0
		for _, v := range m.voiced {
			if v {
				n++
			}
		}
		if n < iosAttack {
			m.gated++
			return out
		}
		m.open, m.unvoiced = true, 0
		out.msgs = append(out.msgs, wsproto.Silence(false))
		if m.sendSpeaking {
			m.speaking = true
			out.msgs = append(out.msgs, wsproto.Speaking(true))
		}
		out.frames = append(out.frames, m.held...)
		m.held = nil
		return out
	}
	if voiced {
		m.unvoiced = 0
	} else {
		m.unvoiced++
	}
	out.frames = append(out.frames, frame)
	if m.unvoiced >= iosHang {
		m.open = false
		m.voiced = nil
		out.msgs = append(out.msgs, wsproto.Silence(true))
		if m.sendSpeaking {
			m.speaking = false
			out.msgs = append(out.msgs, wsproto.Speaking(false))
		}
	}
	return out
}

// webSpeaking is the web mode's separate detector.
func (m *mic) webSpeaking(voicePeak int) []wsproto.Message {
	if !m.sendSpeaking {
		return nil
	}
	voiced := voicePeak >= voiceFloor
	switch {
	case voiced && !m.speaking:
		m.speaking, m.silentFor = true, 0
		return []wsproto.Message{wsproto.Speaking(true)}
	case voiced:
		m.silentFor = 0
	case m.speaking:
		m.silentFor += FrameDur
		if m.silentFor >= webVADHang {
			m.speaking = false
			return []wsproto.Message{wsproto.Speaking(false)}
		}
	}
	return nil
}

func (m *mic) webGate(frame []int16, out *micOut) {
	loud := m.peak >= webFloor
	if !m.open {
		if !loud {
			m.gated++
			m.held = append(m.held, frame)
			if len(m.held) > int(preroll/FrameDur) {
				m.held = m.held[1:]
			}
			return
		}
		m.open, m.quietFor = true, 0
		out.msgs = append(out.msgs, wsproto.Silence(false))
		out.frames = append(append(out.frames, m.held...), frame)
		m.held = nil
		return
	}
	if loud {
		m.quietFor = 0
	} else {
		m.quietFor += FrameDur
		if m.quietFor >= webHold {
			m.open = false
			m.gated++
			out.msgs = append(out.msgs, wsproto.Silence(true))
			return
		}
	}
	out.frames = append(out.frames, frame)
}
