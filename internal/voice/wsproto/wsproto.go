// Package wsproto defines the JSON control messages exchanged over the
// browser ↔ daemon WebSocket (docs/dev/ws-contract.md). Binary frames carry raw
// PCM16 and need no types; every text frame is one Message.
//
// Both the WS server (WP5/WP7) and the tests for the browser page (WP6)
// import this package so the two sides cannot drift apart.
package wsproto

import (
	"encoding/json"
	"fmt"
	"time"
)

// Audio formats of the binary frames.
const (
	// ClientSampleRate is the sample rate of client→server PCM16 frames.
	ClientSampleRate = 16000
	// ServerSampleRate is the sample rate of server→client PCM16 frames.
	ServerSampleRate = 24000
)

// Protocol versions of the /ws contract (docs/dev/ws-contract.md "Protocol
// version and handshake"). ProtocolVersion is what this build speaks;
// MinProtocolVersion is the oldest client protocol the daemon still
// serves. A client that never sends client-ready is refused.
const (
	ProtocolVersion    = 1
	MinProtocolVersion = 1
)

// CloseProtocolUnsupported is the WebSocket close code the daemon uses
// after rejecting a client whose protocol is below its minimum (an
// application code, 4000–4999).
const CloseProtocolUnsupported = 4001

// ReasonProtocolTooOld is the status reason (voice "closed") sent to a
// client whose protocol is below the daemon's minimum.
const ReasonProtocolTooOld = "protocol_too_old"

// Features a bot-ready message can list: optional parts of the contract
// (and the HTTP API next to it) this daemon serves, so a client can
// branch on a name instead of a version number.
const (
	// FeaturePlayback: playback reports drive "speaking" and cut-off
	// transcripts ("Playback reports").
	FeaturePlayback = "playback"
	// FeatureSpeaking: the client speech detector ("Client speech
	// detector").
	FeatureSpeaking = "speaking"
	// FeatureSilence: the silence gate ("Silence gate").
	FeatureSilence = "silence"
	// FeatureProvider: status.provider and the runtime voice provider
	// switch ("Voice provider"); absent with --voice=off.
	FeatureProvider = "provider"
	// FeatureHistory: /events resume, GET /api/conversation, POST
	// /api/conversation/reset and POST /api/pending/answer ("History
	// and resume").
	FeatureHistory = "history"
	// FeaturePush: APNs registration (docs/dev/push.md); absent when the
	// daemon has no push service.
	FeaturePush = "push"
	// FeatureModels: the orchestrator model picker (GET /api/brain/models, PUT
	// /api/brain/model); absent when the orchestrator is off.
	FeatureModels = "models"
	// FeatureScreen: the agent window over /ws (screen-subscribe,
	// screen-unsubscribe, screen, screen-error; "Agent window over /ws");
	// absent when the daemon has no supervisor.
	FeatureScreen = "screen"
	// FeatureFeedback: POST /api/feedback (the user's "that paused"
	// marks) and the client's drain messages ("Feedback and drains").
	FeatureFeedback = "feedback"
	// FeatureCards: the client's visibility reports and the daemon's
	// card messages (the orchestrator's show tool; "Cards").
	FeatureCards = "cards"
	// FeatureTerm: the terminal socket GET /term (docs/dev/term-contract.md);
	// listed only when herdr is new enough for its terminal bridge.
	FeatureTerm = "term"
	// FeatureVoiceCommand: the voice-command message and POST
	// /api/voice/stop and /ptt ("Voice commands over HTTP"); listed when a
	// voice is configured.
	FeatureVoiceCommand = "voice-command"
	// FeatureScreenANSI: screen frames and ?ansi=1 on the screen route
	// give the visible screen with SGR colours.
	FeatureScreenANSI = "screen-ansi"
	// FeatureAgentActions: the user's own agent and space actions — GET
	// /api/spaces and the /api/spaces/{id}/… routes, POST /api/agents (new agent) and POST
	// /api/agents/{pane_id}/rename|interrupt|close.
	FeatureAgentActions = "agent-actions"
)

// Message types. Client→server: client-ready, mute, silence, speaking,
// playback, drain, cancel, text, ping, screen-subscribe, screen-unsubscribe,
// visibility. Server→client: bot-ready, flush, status, transcript, error,
// pong, screen, screen-error, card, voice-command.
const (
	TypeClientReady = "client-ready"
	TypeBotReady    = "bot-ready"
	TypeMute        = "mute"
	TypeSilence     = "silence"
	TypeSpeaking    = "speaking"
	TypePlayback    = "playback"
	TypeDrain       = "drain"
	TypeCancel      = "cancel"
	TypeText        = "text"
	TypePing        = "ping"
	TypeFlush       = "flush"
	TypeStatus      = "status"
	TypeTranscript  = "transcript"
	TypeError       = "error"
	TypePong        = "pong"

	TypeScreenSubscribe   = "screen-subscribe"
	TypeScreenUnsubscribe = "screen-unsubscribe"
	TypeScreen            = "screen"
	TypeScreenError       = "screen-error"

	TypeVisibility = "visibility"
	TypeCard       = "card"

	TypeVoiceCommand = "voice-command"
)

// Actions of a voice-command message (docs/dev/ws-contract.md "Voice
// commands over HTTP").
const (
	// ActionStop: the speech was cut; the client drops what it plays.
	ActionStop = "stop"
	// ActionPTT: hold-to-talk; On says whether to open the microphone.
	ActionPTT = "ptt"
)

// Voice states carried by a status message.
const (
	VoiceConnecting   = "connecting"
	VoiceConnected    = "connected"
	VoiceReconnecting = "reconnecting"
	VoiceClosed       = "closed"
)

// Playback states carried by a playback message (docs/dev/ws-contract.md
// "Playback reports").
const (
	PlaybackStarted = "started"
	PlaybackDrained = "drained"
	PlaybackFlushed = "flushed"
)

// Transcript directions.
const (
	DirIn  = "in"  // what the model heard from the user
	DirOut = "out" // what the model is saying
)

// ClientInfo identifies the client in a client-ready message.
type ClientInfo struct {
	// Name is the client ("sheepdog-ios", "sheepdog-voice").
	Name string `json:"name"`
	// Version is the client's own version ("0.1.0 (10)").
	Version string `json:"version"`
	// Platform is where it runs ("ios", "web").
	Platform string `json:"platform"`
}

// DaemonInfo identifies the daemon in a bot-ready message.
type DaemonInfo struct {
	// Version is the daemon's binary version (as /api/hello "version").
	Version string `json:"version"`
}

// Screen is one frame of an agent's window on a screen message: the body
// of GET /api/agents/{pane_id}/screen without "unchanged" (a frame is only
// sent when it changed).
type Screen struct {
	PaneID     string `json:"pane_id"`
	Alias      string `json:"alias"`
	Spoken     string `json:"spoken,omitempty"`
	Kind       string `json:"kind,omitempty"`
	State      string `json:"state"`
	StateSince string `json:"state_since,omitempty"`
	Text       string `json:"text"`
	Lines      int    `json:"lines"`
	Truncated  bool   `json:"truncated"`
	Hash       string `json:"hash"`
	// ANSI marks Text as SGR-coloured lines (feature screen-ansi).
	ANSI bool `json:"ansi,omitempty"`
}

// Card is a card on a card message: something the orchestrator put on
// the user's screen ("Cards"). Kind card carries Title and Markdown;
// agent names the agent whose window it opens (PaneID); excerpt adds
// Text, the agent's screen lines verbatim; approval adds the open
// question (PendingID, Question, Command, Options), read-only.
type Card struct {
	ID        string       `json:"card_id"`
	Kind      string       `json:"kind"`
	At        string       `json:"at"`
	Title     string       `json:"title"`
	Markdown  string       `json:"markdown,omitempty"`
	Agent     string       `json:"agent,omitempty"`
	Spoken    string       `json:"spoken,omitempty"`
	PaneID    string       `json:"pane_id,omitempty"`
	Text      string       `json:"text,omitempty"`
	PendingID string       `json:"pending_id,omitempty"`
	Question  string       `json:"question,omitempty"`
	Command   string       `json:"command,omitempty"`
	Options   []CardOption `json:"options,omitempty"`
}

// CardOption is one option of an approval card's question (as
// /api/state's pending_action lists it).
type CardOption struct {
	Index   int      `json:"index"`
	Label   string   `json:"label"`
	Keys    []string `json:"keys"`
	Default bool     `json:"default"`
}

// Card kinds.
const (
	CardKindCard     = "card"
	CardKindAgent    = "agent"
	CardKindExcerpt  = "excerpt"
	CardKindApproval = "approval"
)

// Message is the union of all control messages. Type selects which of the
// optional fields are meaningful; unused fields are omitted on the wire.
type Message struct {
	Type string `json:"type"`

	// client-ready and bot-ready (docs/dev/ws-contract.md "Protocol version
	// and handshake"): the protocol the client speaks / the daemon will
	// speak on this connection, who is talking, and the daemon's
	// optional features. MinProtocol (bot-ready) is the oldest client
	// protocol the daemon serves.
	Protocol    *int        `json:"protocol,omitempty"`
	MinProtocol *int        `json:"min_protocol,omitempty"`
	Client      *ClientInfo `json:"client,omitempty"`
	Daemon      *DaemonInfo `json:"daemon,omitempty"`
	Features    []string    `json:"features,omitempty"`

	// mute, silence and speaking; voice-command with action ptt
	On *bool `json:"on,omitempty"`

	// playback: the client's player started, drained or flushed, and
	// how much of the current spoken run it played (ms of the 24 kHz
	// stream the daemon sent).
	State    string `json:"state,omitempty"`
	PlayedMs *int   `json:"played_ms,omitempty"`

	// drain: the client's player ran dry (docs/dev/ws-contract.md "Feedback
	// and drains"). At is when, on the client's clock (RFC 3339 with
	// milliseconds); PlayedMs and ReceivedMs are the run's stream ms
	// played and received by then, BufferMs what the client believed was
	// still buffered (0 for an ordinary dry ring: more than 0 means
	// audio the player dropped). Reason says how the client noticed it.
	At         string `json:"at,omitempty"`
	ReceivedMs *int   `json:"received_ms,omitempty"`

	// ping (optional client counters, mirrored into web.client.stats)
	GatedFrames *int `json:"gated_frames,omitempty"`
	Peak        *int `json:"peak,omitempty"`
	// Underruns, Drains and BufferMs are the playback worklet's jitter
	// buffer stats (audio-util.js Player), reported so the daemon's
	// web.client.stats carries the browser's underrun count and current
	// buffer target/level without a separate channel.
	Underruns *int `json:"underruns,omitempty"`
	Drains    *int `json:"drains,omitempty"`
	BufferMs  *int `json:"buffer_ms,omitempty"`
	// Rate and StretchedMs are the adaptive time-stretch's current
	// playback rate and the total wall-clock ms played at a rate other
	// than 1.0, docs/dev/ws-contract.md "Playback jitter buffer".
	Rate        *float64 `json:"rate,omitempty"`
	StretchedMs *int     `json:"stretched_ms,omitempty"`
	// AudioState is the page's AudioContext state ("running",
	// "suspended", "interrupted", "closed"): a context that is not
	// running plays nothing, and its worklet stops reporting, so the
	// playback counters above stay frozen at their last values.
	AudioState string `json:"audio_state,omitempty"`
	// AEC is whether the microphone's echo cancellation is on, as the
	// browser reports it (track.getSettings().echoCancellation):
	// AECOn, AECOff or AECUnknown. Other values are ignored.
	AEC string `json:"aec,omitempty"`

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

	// error and screen-error
	Message string `json:"message,omitempty"`
	// error about an account problem (docs/dev/ws-contract.md "Account
	// problems"): Problem is invalid_key, quota or balance, Role what it
	// blocks (orchestrator, voice), Provider (above) the service, and Fix
	// the one-line fix. Absent on any other error.
	Problem string `json:"problem,omitempty"`
	Role    string `json:"role,omitempty"`
	Fix     string `json:"fix,omitempty"`

	// screen-subscribe (PaneID), screen-error (PaneID, Ended) and
	// screen (Screen): the agent window over /ws. Ended is set on a
	// screen-error that ended the subscription.
	PaneID string  `json:"pane_id,omitempty"`
	Screen *Screen `json:"screen,omitempty"`
	Ended  bool    `json:"ended,omitempty"`

	// visibility (client→server): whether the client's screen is
	// visible (the app in the foreground, the page not hidden) and its
	// voice screen is the one in front. card (server→client): Card.
	Visible     *bool `json:"visible,omitempty"`
	VoiceScreen *bool `json:"voice_screen,omitempty"`
	Card        *Card `json:"card,omitempty"`

	// voice-command (server→client): Action is ActionStop or ActionPTT
	// (On says whether to talk).
	Action string `json:"action,omitempty"`
}

// ClientReady builds the client→server handshake message, the first text
// frame a protocol-aware client sends.
func ClientReady(protocol int, client ClientInfo) Message {
	return Message{Type: TypeClientReady, Protocol: &protocol, Client: &client}
}

// BotReady builds the server→client reply to ClientReady: the protocol
// the daemon speaks on this connection, the oldest it serves, its
// version and its features.
func BotReady(protocol, minProtocol int, daemonVersion string, features []string) Message {
	return Message{Type: TypeBotReady, Protocol: &protocol, MinProtocol: &minProtocol, Daemon: &DaemonInfo{Version: daemonVersion}, Features: features}
}

// Mute builds a client→server mute message.
func Mute(on bool) Message { return Message{Type: TypeMute, On: &on} }

// Silence builds a client→server silence-gate message: on means the
// client's capture gate closed (near-zero input, no frames follow until
// off). See docs/dev/ws-contract.md, "Silence gate".
func Silence(on bool) Message { return Message{Type: TypeSilence, On: &on} }

// Speaking builds a client→server speech-detector message: on means the
// client hears the user speaking. See docs/dev/ws-contract.md, "Client
// speech detector".
func Speaking(on bool) Message { return Message{Type: TypeSpeaking, On: &on} }

// Playback builds a client→server playback report.
func Playback(state string, playedMs int) Message {
	return Message{Type: TypePlayback, State: state, PlayedMs: &playedMs}
}

// Drain builds a client→server drain message; at is RFC 3339 and reason
// may be empty.
func Drain(at string, bufferMs, playedMs, receivedMs int, reason string) Message {
	return Message{Type: TypeDrain, At: at, BufferMs: &bufferMs, PlayedMs: &playedMs, ReceivedMs: &receivedMs, Reason: reason}
}

// Drain reasons (Message.Reason on a drain message).
const (
	// DrainDry: the predicted playhead ran out of audio.
	DrainDry = "dry"
	// DrainEarly: the player said it had finished before the predicted
	// playhead got there (iOS: an engine restart discards what was
	// scheduled); BufferMs is the audio that never played.
	DrainEarly = "early"
)

// Cancel builds the client→server "stop" message.
func Cancel() Message { return Message{Type: TypeCancel} }

// SimulateText builds a client→server simulated utterance.
func SimulateText(text string) Message { return Message{Type: TypeText, Text: text} }

// Values of Message.AEC (ping).
const (
	AECOn      = "true"
	AECOff     = "false"
	AECUnknown = "unknown"
)

// ValidAEC reports whether v is one of the ping's aec values.
func ValidAEC(v string) bool { return v == AECOn || v == AECOff || v == AECUnknown }

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

// AccountError builds the error message for an account problem: the
// plain sentence naming the service, and the problem, the role it
// blocks, the provider and the fix as fields.
func AccountError(msg, problem, role, provider, fix string) Message {
	return Message{Type: TypeError, Message: msg, Problem: problem, Role: role, Provider: provider, Fix: fix}
}

// Pong builds the reply to Ping.
func Pong() Message { return Message{Type: TypePong} }

// VoiceStop builds the server→client voice-command message that tells
// the client its speech was cut.
func VoiceStop() Message { return Message{Type: TypeVoiceCommand, Action: ActionStop} }

// VoicePTT builds the server→client voice-command message for
// hold-to-talk: on opens the client's microphone, off closes it.
func VoicePTT(on bool) Message {
	return Message{Type: TypeVoiceCommand, Action: ActionPTT, On: &on}
}

// ScreenSubscribe builds the client→server request for an agent's window.
func ScreenSubscribe(paneID string) Message {
	return Message{Type: TypeScreenSubscribe, PaneID: paneID}
}

// ScreenUnsubscribe builds the client→server end of the subscription.
func ScreenUnsubscribe() Message { return Message{Type: TypeScreenUnsubscribe} }

// ScreenFrame builds a server→client screen frame.
func ScreenFrame(s Screen) Message { return Message{Type: TypeScreen, Screen: &s} }

// ScreenError builds a server→client screen-error; ended means the
// subscription is over.
func ScreenError(paneID, msg string, ended bool) Message {
	return Message{Type: TypeScreenError, PaneID: paneID, Message: msg, Ended: ended}
}

// Visibility builds the client→server visibility report.
func Visibility(visible, voiceScreen bool) Message {
	return Message{Type: TypeVisibility, Visible: &visible, VoiceScreen: &voiceScreen}
}

// CardFrame builds a server→client card message.
func CardFrame(c Card) Message { return Message{Type: TypeCard, Card: &c} }

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
	case TypeClientReady:
		if m.Protocol == nil {
			return fmt.Errorf("wsproto: client-ready: missing \"protocol\"")
		}
	case TypeBotReady:
		if m.Protocol == nil {
			return fmt.Errorf("wsproto: bot-ready: missing \"protocol\"")
		}
		if m.Daemon == nil {
			return fmt.Errorf("wsproto: bot-ready: missing \"daemon\"")
		}
	case TypeMute, TypeSilence, TypeSpeaking:
		if m.On == nil {
			return fmt.Errorf("wsproto: %s: missing \"on\"", m.Type)
		}
	case TypePlayback:
		switch m.State {
		case PlaybackStarted:
		case PlaybackDrained, PlaybackFlushed:
			if m.PlayedMs == nil {
				return fmt.Errorf("wsproto: playback: %s: missing \"played_ms\"", m.State)
			}
		default:
			return fmt.Errorf("wsproto: playback: bad state %q", m.State)
		}
		if m.PlayedMs != nil && *m.PlayedMs < 0 {
			return fmt.Errorf("wsproto: playback: negative \"played_ms\"")
		}
	case TypeDrain:
		if m.At == "" {
			return fmt.Errorf("wsproto: drain: missing \"at\"")
		}
		if _, err := time.Parse(time.RFC3339Nano, m.At); err != nil {
			return fmt.Errorf("wsproto: drain: bad \"at\": %v", err)
		}
		if m.PlayedMs == nil || m.BufferMs == nil {
			return fmt.Errorf("wsproto: drain: missing \"played_ms\" or \"buffer_ms\"")
		}
		if *m.PlayedMs < 0 || *m.BufferMs < 0 || (m.ReceivedMs != nil && *m.ReceivedMs < 0) {
			return fmt.Errorf("wsproto: drain: negative duration")
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
	case TypeScreenSubscribe:
		if m.PaneID == "" {
			return fmt.Errorf("wsproto: screen-subscribe: missing \"pane_id\"")
		}
	case TypeScreen:
		if m.Screen == nil || m.Screen.PaneID == "" {
			return fmt.Errorf("wsproto: screen: missing \"screen\"")
		}
	case TypeScreenError:
		if m.PaneID == "" || m.Message == "" {
			return fmt.Errorf("wsproto: screen-error: missing \"pane_id\" or \"message\"")
		}
	case TypeVisibility:
		if m.Visible == nil {
			return fmt.Errorf("wsproto: visibility: missing \"visible\"")
		}
	case TypeCard:
		if m.Card == nil || m.Card.ID == "" || m.Card.Kind == "" {
			return fmt.Errorf("wsproto: card: missing \"card\" with \"card_id\" and \"kind\"")
		}
	case TypeVoiceCommand:
		switch m.Action {
		case ActionStop:
		case ActionPTT:
			if m.On == nil {
				return fmt.Errorf("wsproto: voice-command: ptt: missing \"on\"")
			}
		default:
			return fmt.Errorf("wsproto: voice-command: bad action %q", m.Action)
		}
	case TypeCancel, TypePing, TypeFlush, TypePong, TypeScreenUnsubscribe:
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
