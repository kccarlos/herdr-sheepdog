// Package wsproto defines the JSON control messages exchanged over the
// browser ↔ daemon WebSocket (docs/ws-contract.md). Binary frames carry raw
// PCM16 and need no types; every text frame is one Message.
//
// Both the WS server (WP5/WP7) and the tests for the browser page (WP6)
// import this package so the two sides cannot drift apart.
package wsproto

import (
	"encoding/json"
	"fmt"
)

// Audio formats of the binary frames.
const (
	// ClientSampleRate is the sample rate of client→server PCM16 frames.
	ClientSampleRate = 16000
	// ServerSampleRate is the sample rate of server→client PCM16 frames.
	ServerSampleRate = 24000
)

// Message types. Client→server: mute, silence, cancel, text, ping.
// Server→client: flush, status, transcript, error, pong.
const (
	TypeMute       = "mute"
	TypeSilence    = "silence"
	TypeCancel     = "cancel"
	TypeText       = "text"
	TypePing       = "ping"
	TypeFlush      = "flush"
	TypeStatus     = "status"
	TypeTranscript = "transcript"
	TypeError      = "error"
	TypePong       = "pong"
)

// Voice states carried by a status message.
const (
	VoiceConnecting   = "connecting"
	VoiceConnected    = "connected"
	VoiceReconnecting = "reconnecting"
	VoiceClosed       = "closed"
)

// Transcript directions.
const (
	DirIn  = "in"  // what the model heard from the user
	DirOut = "out" // what the model is saying
)

// Message is the union of all control messages. Type selects which of the
// optional fields are meaningful; unused fields are omitted on the wire.
type Message struct {
	Type string `json:"type"`

	// mute and silence
	On *bool `json:"on,omitempty"`

	// ping (optional client counters, mirrored into web.client.stats)
	GatedFrames *int `json:"gated_frames,omitempty"`
	Peak        *int `json:"peak,omitempty"`

	// text (client→server: simulated utterance) and transcript (server→client)
	Text string `json:"text,omitempty"`

	// status
	Voice   string `json:"voice,omitempty"`
	Muted   *bool  `json:"muted,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Stalled bool   `json:"stalled,omitempty"`
	// Provider is the voice provider behind the session ("gemini",
	// "openai"); set on status messages when a voice is configured.
	Provider string `json:"provider,omitempty"`

	// transcript
	Dir   string `json:"dir,omitempty"`
	Final *bool  `json:"final,omitempty"`

	// error
	Message string `json:"message,omitempty"`
}

// Mute builds a client→server mute message.
func Mute(on bool) Message { return Message{Type: TypeMute, On: &on} }

// Silence builds a client→server silence-gate message: on means the
// client's capture gate closed (near-zero input, no frames follow until
// off). See docs/ws-contract.md, "Silence gate".
func Silence(on bool) Message { return Message{Type: TypeSilence, On: &on} }

// Cancel builds the client→server "stop" message.
func Cancel() Message { return Message{Type: TypeCancel} }

// SimulateText builds a client→server simulated utterance.
func SimulateText(text string) Message { return Message{Type: TypeText, Text: text} }

// Ping builds a client→server liveness probe.
func Ping() Message { return Message{Type: TypePing} }

// Flush builds the server→client "drop your playback buffer" message.
func Flush() Message { return Message{Type: TypeFlush} }

// Status builds a server→client status message. reason may be empty.
func Status(voice string, muted bool, reason string) Message {
	return Message{Type: TypeStatus, Voice: voice, Muted: &muted, Reason: reason}
}

// Transcript builds a server→client transcript fragment.
func Transcript(dir, text string, final bool) Message {
	return Message{Type: TypeTranscript, Dir: dir, Text: text, Final: &final}
}

// Error builds a server→client non-fatal error message.
func Error(msg string) Message { return Message{Type: TypeError, Message: msg} }

// Pong builds the reply to Ping.
func Pong() Message { return Message{Type: TypePong} }

// Parse decodes one text frame and validates the fields the type requires.
func Parse(data []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return Message{}, fmt.Errorf("wsproto: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Message{}, err
	}
	return m, nil
}

// Validate checks that the fields required by m.Type are present.
func (m Message) Validate() error {
	switch m.Type {
	case TypeMute, TypeSilence:
		if m.On == nil {
			return fmt.Errorf("wsproto: %s: missing \"on\"", m.Type)
		}
	case TypeText:
		if m.Text == "" {
			return fmt.Errorf("wsproto: text: missing \"text\"")
		}
	case TypeStatus:
		switch m.Voice {
		case VoiceConnecting, VoiceConnected, VoiceReconnecting, VoiceClosed:
		default:
			return fmt.Errorf("wsproto: status: bad voice %q", m.Voice)
		}
	case TypeTranscript:
		if m.Dir != DirIn && m.Dir != DirOut {
			return fmt.Errorf("wsproto: transcript: bad dir %q", m.Dir)
		}
	case TypeError:
		if m.Message == "" {
			return fmt.Errorf("wsproto: error: missing \"message\"")
		}
	case TypeCancel, TypePing, TypeFlush, TypePong:
	case "":
		return fmt.Errorf("wsproto: missing \"type\"")
	default:
		return fmt.Errorf("wsproto: unknown type %q", m.Type)
	}
	return nil
}

// Encode renders m as one text frame.
func (m Message) Encode() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}
