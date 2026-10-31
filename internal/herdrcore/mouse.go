package herdrcore

import "fmt"

// hc never encodes the mouse for a viewer: a viewer that wants the program
// to see a wheel turn or a click writes the bytes itself, in the encoding
// the program asked for. termModes follows the DEC private modes in the
// terminal's output stream that say which (not a terminal emulator: it
// reads the `CSI ? Pm h|l` sequences and nothing else).

// Mouse tracking modes, as the number of their DECSET.
const (
	mouseOff   = 0
	mousePress = 1000 // button presses and releases
	mouseDrag  = 1002 // plus motion while a button is held
	mouseAny   = 1003 // plus all motion
)

// modeSGR is DECSET 1006, the SGR encoding of a mouse event; without it
// the legacy X10 one (one byte per number, so nothing past column 223).
const modeSGR = 1006

// scanner states of termModes.feed.
const (
	scanGround = iota
	scanEsc    // saw ESC
	scanCSI    // saw ESC [
	scanPriv   // saw ESC [ ?, collecting numbers
)

// termModes is the mouse state of one attach stream. Not safe for
// concurrent use: the bridge guards it.
type termModes struct {
	mouse int
	sgr   bool

	state  int
	params []int
	cur    int
	have   bool
}

// feed scans stream bytes, which may split a sequence anywhere.
func (m *termModes) feed(p []byte) {
	for _, c := range p {
		switch m.state {
		case scanGround:
			if c == 0x1b {
				m.state = scanEsc
			}
		case scanEsc:
			switch c {
			case '[':
				m.state = scanCSI
			case 0x1b:
			default:
				m.state = scanGround
			}
		case scanCSI:
			switch c {
			case '?':
				m.state, m.params, m.cur, m.have = scanPriv, m.params[:0], 0, false
			case 0x1b:
				m.state = scanEsc
			default:
				m.state = scanGround
			}
		case scanPriv:
			m.private(c)
		}
	}
}

// private consumes one byte of `CSI ? Pm ...`.
func (m *termModes) private(c byte) {
	switch {
	case c >= '0' && c <= '9':
		if m.cur < 1<<20 {
			m.cur = m.cur*10 + int(c-'0')
		}
		m.have = true
	case c == ';':
		m.params = append(m.params, m.cur)
		m.cur, m.have = 0, false
	case c == 'h' || c == 'l':
		if m.have {
			m.params = append(m.params, m.cur)
		}
		for _, n := range m.params {
			m.set(n, c == 'h')
		}
		m.state = scanGround
	case c == 0x1b:
		m.state = scanEsc
	default:
		m.state = scanGround
	}
}

func (m *termModes) set(mode int, on bool) {
	switch mode {
	case mousePress, mouseDrag, mouseAny:
		switch {
		case on:
			m.mouse = mode
		case m.mouse == mode:
			m.mouse = mouseOff
		}
	case modeSGR:
		m.sgr = on
	}
}

// Mouse button codes of an event (the low bits), plus the motion and
// wheel flags.
const (
	buttonLeft   = 0
	buttonMiddle = 1
	buttonRight  = 2
	buttonNone   = 3 // a legacy release
	flagMotion   = 32
	flagWheel    = 64
)

// event encodes one mouse event at the zero-based cell col,row, or nil
// when the program asked for no mouse (or the legacy encoding cannot
// reach the cell). A release in SGR keeps its button and ends in m; the
// legacy one has no button to say.
func (m *termModes) event(button, col, row int, release bool) []byte {
	if m.mouse == mouseOff {
		return nil
	}
	if m.sgr {
		end := 'M'
		if release {
			end = 'm'
		}
		return fmt.Appendf(nil, "\x1b[<%d;%d;%d%c", button, col+1, row+1, end)
	}
	if col+1 > 223 || row+1 > 223 {
		return nil
	}
	if release {
		button = buttonNone
	}
	return []byte{0x1b, '[', 'M', byte(32 + button), byte(32 + col + 1), byte(32 + row + 1)}
}

// canDrag reports whether the program asked for motion while a button is
// held.
func (m *termModes) canDrag() bool { return m.mouse == mouseDrag || m.mouse == mouseAny }
