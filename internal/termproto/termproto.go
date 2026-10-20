// Package termproto is the wire format of the daemon's terminal socket,
// GET /term (docs/dev/term-contract.md): JSON text frames for control and
// two binary frame kinds, a terminal frame (daemon → client) and raw
// input (client → daemon). It has no dependencies, so the web layer, its
// tests and tools share one definition.
package termproto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
)

// ProtocolVersion is the /term protocol this daemon speaks.
const ProtocolVersion = 1

// Limits.
const (
	// MaxChannels is how many windows one connection may hold open.
	MaxChannels = 4
	// MaxBridges is how many windows the daemon holds open across every
	// connection.
	MaxBridges = 8
	// MaxInput is the most raw input bytes one binary frame carries.
	MaxInput = 64 << 10
	// MaxSize bounds cols and rows.
	MaxSize = 1000
)

// Message types.
const (
	TypeHello  = "term-hello"
	TypeOpen   = "term-open"
	TypeResize = "term-resize"
	TypeScroll = "term-scroll"
	TypeMouse  = "term-mouse"
	TypeClose  = "term-close"

	TypeReady  = "term-ready"
	TypeOpened = "term-opened"
	TypeClosed = "term-closed"
	TypeError  = "term-error"
	// TypeFree tells an observe channel that the device controlling its
	// pane through this daemon let go: the client may open control now.
	TypeFree = "term-free"
)

// Modes.
const (
	ModeControl = "control"
	ModeObserve = "observe"
)

// Close reasons (term-closed). A refusal is "refused:" + why.
const (
	ReasonReleased  = "released"
	ReasonTakenOver = "taken_over"
	ReasonGone      = "gone"
	ReasonHerdr     = "herdr"

	RefusedBusy       = "refused:busy"
	RefusedTooMany    = "refused:too_many"
	RefusedShellOff   = "refused:shell_off"
	RefusedNotTracked = "refused:not_tracked"
	RefusedBad        = "refused:bad_request"
)

// ClientInfo identifies the client in term-hello (for the log).
type ClientInfo struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

// Limits is term-ready's limits.
type Limits struct {
	Channels int `json:"channels"`
}

// Shell is a target that asks for a new phone shell (phase 5; refused
// with shell_off until then).
type Shell struct {
	Cwd string `json:"cwd,omitempty"`
}

// Target is what a channel opens: a tracked agent's pane, or a shell.
type Target struct {
	PaneID string `json:"pane_id,omitempty"`
	Shell  *Shell `json:"shell,omitempty"`
}

// Message is every JSON control message, both directions; Type says which
// fields apply (docs/dev/term-contract.md).
type Message struct {
	Type        string      `json:"type"`
	Protocol    *int        `json:"protocol,omitempty"`
	Client      *ClientInfo `json:"client,omitempty"`
	Limits      *Limits     `json:"limits,omitempty"`
	Shell       *bool       `json:"shell,omitempty"`
	Ch          int         `json:"ch,omitempty"`
	Target      *Target     `json:"target,omitempty"`
	Mode        string      `json:"mode,omitempty"`
	Cols        int         `json:"cols,omitempty"`
	Rows        int         `json:"rows,omitempty"`
	Takeover    bool        `json:"takeover,omitempty"`
	PaneID      string      `json:"pane_id,omitempty"`
	Kind        string      `json:"kind,omitempty"`
	Reason      string      `json:"reason,omitempty"`
	CanTakeOver bool        `json:"can_take_over,omitempty"`
	Message     string      `json:"message,omitempty"`
	Dir         string      `json:"dir,omitempty"`
	Lines       int         `json:"lines,omitempty"`
	Action      string      `json:"action,omitempty"`
	Button      string      `json:"button,omitempty"`
	Col         *int        `json:"col,omitempty"`
	Row         *int        `json:"row,omitempty"`
}

// Validate checks a message's required fields for its type.
func (m Message) Validate() error {
	chOK := m.Ch >= 1 && m.Ch <= 65535
	sizeOK := m.Cols >= 1 && m.Rows >= 1 && m.Cols <= MaxSize && m.Rows <= MaxSize
	switch m.Type {
	case TypeHello:
		if m.Protocol == nil {
			return errors.New("term-hello needs protocol")
		}
	case TypeReady:
		if m.Protocol == nil || m.Limits == nil || m.Shell == nil {
			return errors.New("term-ready needs protocol, limits and shell")
		}
	case TypeOpen:
		if !chOK || !sizeOK || m.Target == nil {
			return errors.New("term-open needs ch, target, cols and rows")
		}
		if (m.Target.PaneID == "") == (m.Target.Shell == nil) {
			return errors.New("term-open target is pane_id or shell")
		}
		if m.Mode != ModeControl && m.Mode != ModeObserve {
			return fmt.Errorf("term-open mode %q", m.Mode)
		}
	case TypeResize:
		if !chOK || !sizeOK {
			return errors.New("term-resize needs ch, cols and rows")
		}
	case TypeScroll:
		if !chOK || (m.Dir != "up" && m.Dir != "down") || m.Lines < 1 || (m.Col == nil) != (m.Row == nil) {
			return errors.New("term-scroll needs ch, dir up|down and lines (col and row together)")
		}
	case TypeMouse:
		switch m.Action {
		case "down", "up", "drag":
		default:
			return fmt.Errorf("term-mouse action %q", m.Action)
		}
		switch m.Button {
		case "", "left", "right", "middle":
		default:
			return fmt.Errorf("term-mouse button %q", m.Button)
		}
		if !chOK || m.Col == nil || m.Row == nil || *m.Col < 0 || *m.Row < 0 {
			return errors.New("term-mouse needs ch, col and row")
		}
	case TypeClose:
		if !chOK {
			return errors.New("term-close needs ch")
		}
	case TypeOpened:
		if !chOK || m.PaneID == "" || m.Mode == "" || !sizeOK || m.Kind == "" {
			return errors.New("term-opened needs ch, pane_id, mode, cols, rows and kind")
		}
	case TypeClosed:
		if !chOK || m.Reason == "" {
			return errors.New("term-closed needs ch and reason")
		}
	case TypeFree:
		if !chOK || m.PaneID == "" {
			return errors.New("term-free needs ch and pane_id")
		}
	case TypeError:
		if m.Message == "" {
			return errors.New("term-error needs message")
		}
	default:
		return fmt.Errorf("unknown type %q", m.Type)
	}
	return nil
}

// Parse decodes and validates one control message.
func Parse(data []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return Message{}, err
	}
	return m, m.Validate()
}

// Ready is the daemon's term-ready.
func Ready(shell bool) Message {
	p := ProtocolVersion
	return Message{Type: TypeReady, Protocol: &p, Limits: &Limits{Channels: MaxChannels}, Shell: &shell}
}

// Free is term-free.
func Free(ch int, paneID string) Message {
	return Message{Type: TypeFree, Ch: ch, PaneID: paneID}
}

// Opened is term-opened.
func Opened(ch int, paneID, mode string, cols, rows int, kind string) Message {
	return Message{Type: TypeOpened, Ch: ch, PaneID: paneID, Mode: mode, Cols: cols, Rows: rows, Kind: kind}
}

// Closed is term-closed.
func Closed(ch int, reason string) Message {
	return Message{Type: TypeClosed, Ch: ch, Reason: reason, CanTakeOver: reason == RefusedBusy}
}

// Error is term-error (ch 0: about the connection, not a channel).
func Error(ch int, msg string) Message {
	return Message{Type: TypeError, Ch: ch, Message: msg}
}

// Binary frame kinds (the first byte).
const (
	KindFrame byte = 0x01
	KindInput byte = 0x02
)

// frameHeader is kind, ch, seq, cols, rows, flags.
const frameHeader = 1 + 2 + 4 + 2 + 2 + 1

// FlagFull marks a full repaint.
const FlagFull byte = 1

// Frame is one terminal frame for a channel.
type Frame struct {
	Ch   uint16
	Seq  uint32
	Cols uint16
	Rows uint16
	Full bool
	Data []byte
}

// EncodeFrame is `0x01 | ch u16 | seq u32 | cols u16 | rows u16 | flags u8
// | ANSI bytes`, big-endian.
func EncodeFrame(f Frame) []byte {
	b := make([]byte, frameHeader+len(f.Data))
	b[0] = KindFrame
	binary.BigEndian.PutUint16(b[1:], f.Ch)
	binary.BigEndian.PutUint32(b[3:], f.Seq)
	binary.BigEndian.PutUint16(b[7:], f.Cols)
	binary.BigEndian.PutUint16(b[9:], f.Rows)
	if f.Full {
		b[11] = FlagFull
	}
	copy(b[frameHeader:], f.Data)
	return b
}

// DecodeFrame is EncodeFrame's inverse (clients and tests).
func DecodeFrame(b []byte) (Frame, error) {
	if len(b) < frameHeader || b[0] != KindFrame {
		return Frame{}, errors.New("not a terminal frame")
	}
	return Frame{
		Ch: binary.BigEndian.Uint16(b[1:]), Seq: binary.BigEndian.Uint32(b[3:]),
		Cols: binary.BigEndian.Uint16(b[7:]), Rows: binary.BigEndian.Uint16(b[9:]),
		Full: b[11]&FlagFull != 0, Data: b[frameHeader:],
	}, nil
}

// EncodeInput is `0x02 | ch u16 | raw input bytes`.
func EncodeInput(ch uint16, data []byte) []byte {
	b := make([]byte, 3+len(data))
	b[0] = KindInput
	binary.BigEndian.PutUint16(b[1:], ch)
	copy(b[3:], data)
	return b
}

// DecodeInput parses a client input frame.
func DecodeInput(b []byte) (ch uint16, data []byte, err error) {
	if len(b) < 3 || b[0] != KindInput {
		return 0, nil, errors.New("not an input frame")
	}
	if len(b)-3 > MaxInput {
		return 0, nil, fmt.Errorf("input over %d bytes", MaxInput)
	}
	return binary.BigEndian.Uint16(b[1:]), b[3:], nil
}
