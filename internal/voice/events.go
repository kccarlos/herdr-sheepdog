package voice

// Status of the connection to the voice model, mirrored to the browser as
// wsproto status messages.
type Status int

const (
	StatusConnecting Status = iota
	StatusConnected
	StatusReconnecting
	StatusClosed
)

// String returns the wire name (docs/ws-contract.md).
func (s Status) String() string {
	switch s {
	case StatusConnecting:
		return "connecting"
	case StatusConnected:
		return "connected"
	case StatusReconnecting:
		return "reconnecting"
	case StatusClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// Direction of a transcript.
type Direction int

const (
	// DirIn is what the model heard from the user.
	DirIn Direction = iota
	// DirOut is what the model said.
	DirOut
)

// String returns the wire name.
func (d Direction) String() string {
	if d == DirIn {
		return "in"
	}
	return "out"
}

// Event is one item on Session.Events. The concrete types below are the
// only implementations.
type Event interface{ isEvent() }

// EventStatus reports a connection state change. Reason is set for
// StatusClosed and for StatusReconnecting (why the connection was lost).
type EventStatus struct {
	Status Status
	Reason string
}

// EventTranscript is an incremental transcript fragment.
type EventTranscript struct {
	Dir   Direction
	Text  string
	Final bool
}

// EventInterrupted means queued model audio was dropped; the browser must
// flush its playback buffer.
type EventInterrupted struct{}

// EventToolCall is emitted when the model requests a tool (for logging and
// the debug page; the ToolHandler is invoked separately).
type EventToolCall struct {
	Call ToolCall
}

// EventToolResult is emitted after the handler returned and the response
// was sent. Err is set when the handler failed or was cancelled; Cancelled
// distinguishes the latter.
type EventToolResult struct {
	Call      ToolCall
	Result    ToolResult
	Err       error
	Cancelled bool
}

// EventPush is emitted when a queued push is actually sent to the model.
type EventPush struct {
	Text    string
	Urgency Urgency
}

// EventTurnComplete is emitted when the model considers its turn finished
// (audio played out). Useful for "wait until quiet" logic and tests.
type EventTurnComplete struct{}

// EventResumed is emitted after a reconnect. Handle is the resumption
// handle used (empty when none was available); ContextLost is true when
// the new connection could not resume the previous conversation, so the
// Supervisor should re-push the briefing (plan §6).
type EventResumed struct {
	Handle      string
	ContextLost bool
}

// EventError reports a non-fatal problem (rate limit, send failure). The
// session keeps running unless an EventStatus{StatusClosed} follows.
type EventError struct {
	Err error
}

func (EventStatus) isEvent()       {}
func (EventTranscript) isEvent()   {}
func (EventInterrupted) isEvent()  {}
func (EventToolCall) isEvent()     {}
func (EventToolResult) isEvent()   {}
func (EventPush) isEvent()         {}
func (EventTurnComplete) isEvent() {}
func (EventResumed) isEvent()      {}
func (EventError) isEvent()        {}
