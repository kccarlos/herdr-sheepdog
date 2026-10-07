// Package voice defines the model-agnostic VoiceSession contract (plan
// §3.7). internal/voice/gemini implements it for Gemini Live; a GPT-Live
// adapter (plan P2) must be able to implement it without changes here, so
// the interface only talks about PCM audio, text, tool calls, pushes and a
// small event stream.
//
// Audio: SendAudio takes 16 kHz PCM16 LE mono; Audio() yields 24 kHz PCM16
// LE mono chunks in playback order. Both match docs/ws-contract.md.
package voice

import (
	"context"
	"time"
)

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

// SchedulingName renders u as the Gemini function-response scheduling
// name (docs/gemini-live-notes.md §5): interrupt, when_idle or silent.
// voice.push and voice.tool_result log this beside the requested
// urgency as scheduling_sent, the urgency actually used once a
// speaking model has downgraded an INTERRUPT (docs/roles.md "Pushes").
func (u Urgency) SchedulingName() string {
	switch u {
	case UrgencyInterrupt:
		return "interrupt"
	case UrgencySilent:
		return "silent"
	default:
		return "when_idle"
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
	// UtteranceID names the user utterance a spoken result answers. It is
	// never sent to the model: the session puts it on the voice.playback
	// {state: started} that follows (turn.latency's first audio).
	UtteranceID string
	// NotVerified lists the orchestrator's checks that did not finish
	// (brain.Reply.NotVerified); the voice is told nothing they would
	// have shown is verified.
	NotVerified []string
	// Agents are the aliases the answer's tools read or acted on
	// (brain.Reply.Agents): the voice gets their state at delivery.
	Agents []string
}

// ToolHandler executes one tool call. ctx is cancelled when the model
// cancels the call (user barged in) or the session closes; a cancelled
// handler's result is discarded.
type ToolHandler func(ctx context.Context, call ToolCall) (ToolResult, error)

// OpenCheck is the supervisor's answer to "is it still open?", which
// the voice layer cannot see for itself. Asked with nil tags: is a
// question waiting for the user (a pending approval or decision)?
// Asked with a push's tags: is what that line is about — the prompt it
// announced, the promise it keeps — still owed to the user?
type OpenCheck func(tags map[string]any) bool

// PushBacklogger is implemented by sessions with a push queue: how many
// pushes wait in it and when the oldest was queued (zero when none). The
// daemon watchdog reads it (internal/observe); it only reads.
type PushBacklogger interface {
	PushBacklog() (n int, oldest time.Time)
}

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

	// Speaking reports whether the model's speech is still reaching the
	// user: true from the first output fragment (audio or an output
	// transcript) of a turn until the /ws client has finished playing
	// it — its playback report drained (plus PlaybackGapHold) or
	// flushed, or without playback reports a timing guess (audio sent
	// minus wall time) — or the model is interrupted. The model's own
	// turn end is not enough: Gemini generates ~4× real time, so its
	// turnComplete arrives while the reply is still playing. Nothing
	// interrupts the model mid-sentence
	// except the user (docs/roles.md "Pushes"): an implementation
	// downgrades INTERRUPT scheduling to WHEN_IDLE while this is true,
	// with a due promise or a decision on a brand new block the only
	// exceptions. A session with no live model (--voice=off) reports
	// false.
	Speaking() bool

	// Audio yields 24 kHz PCM16 LE mono chunks in order. Closed on Close.
	Audio() <-chan []byte
	// Events yields transcripts, interruptions, status changes, tool
	// activity and errors. Closed on Close.
	Events() <-chan Event

	// SetMuted stops (true) or resumes (false) forwarding SendAudio frames.
	SetMuted(muted bool)
}
