// Package voice defines the model-agnostic VoiceSession contract (plan
// §3.7). internal/voice/gemini implements it for Gemini Live; a GPT-Live
// adapter (plan P2) must be able to implement it without changes here, so
// the interface only talks about PCM audio, text, tool calls, pushes and a
// small event stream.
//
// Audio: SendAudio takes 16 kHz PCM16 LE mono; Audio() yields 24 kHz PCM16
// LE mono chunks in playback order. Both match docs/ws-contract.md.
package voice

import "context"

// Urgency says how a push or a tool result should reach the user (plan §5,
// §6). The zero value is UrgencyIdle: speak when the model is not already
// talking.
type Urgency int

const (
	// UrgencyIdle speaks after the current model turn finishes and the user
	// is not speaking (Gemini: WHEN_IDLE for tool results).
	UrgencyIdle Urgency = iota
	// UrgencyInterrupt speaks now, cutting the model off if needed (Gemini:
	// INTERRUPT).
	UrgencyInterrupt
	// UrgencySilent adds the text to the model's context without making it
	// speak (Gemini: SILENT).
	UrgencySilent
)

// String returns the lower-case name used in logs.
func (u Urgency) String() string {
	switch u {
	case UrgencyIdle:
		return "idle"
	case UrgencyInterrupt:
		return "interrupt"
	case UrgencySilent:
		return "silent"
	default:
		return "unknown"
	}
}

// The two tools the voice model is declared (docs/roles.md "One
// decision-maker"). They are named here, in the model-agnostic package,
// because the adapters declare them and the Supervisor handles them.
const (
	// ToolToSupervisor carries the user's words verbatim; its one
	// argument is "text".
	ToolToSupervisor = "to_supervisor"
	// ToolStop runs the daemon's stop procedure; it takes no arguments.
	ToolStop = "stop"
)

// ToolCall is one function call requested by the voice model.
type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

// ToolResult is what the Supervisor returns for a ToolCall. Spoken is read
// aloud verbatim (decision D9); Detail is for the voice model's follow-up
// answers, not read aloud (also D9-scrubbed); Options lists the choices
// when there are some; Clarify marks a clarifying question; Pending marks
// an answer whose real result follows on its own; Scheduling is the
// urgency of the spoken result.
type ToolResult struct {
	Spoken     string
	Detail     string
	Options    []string
	Clarify    bool
	Pending    bool
	Scheduling Urgency
}

// ToolHandler executes one tool call. ctx is cancelled when the model
// cancels the call (user barged in) or the session closes; a cancelled
// handler's result is discarded.
type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)

// Session is one long-lived voice session owned by the daemon.
//
// Start connects; Close ends the session for good. All other methods are
// safe for concurrent use once Start has returned. SendAudio never blocks
// on the network for long and drops frames while muted or when the
// reconnect buffer is full. Push enqueues; the implementation owns the
// queue policy (one at a time, minimum gap between spoken pushes, never
// while the user is speaking).
type Session interface {
	Start(ctx context.Context) error
	Close() error

	// SendAudio streams 16 kHz PCM16 LE mono from the user's microphone.
	SendAudio(pcm16 []byte) error
	// SendText simulates a user utterance (debug page, tests).
	SendText(text string) error
	// Push makes the model speak (UrgencyInterrupt, UrgencyIdle) or adds
	// context silently (UrgencySilent).
	Push(text string, u Urgency) error

	// Audio yields 24 kHz PCM16 LE mono chunks in order. Closed on Close.
	Audio() <-chan []byte
	// Events yields transcripts, interruptions, status changes, tool
	// activity and errors. Closed on Close.
	Events() <-chan Event

	// SetMuted stops (true) or resumes (false) forwarding SendAudio frames.
	SetMuted(muted bool)
}
