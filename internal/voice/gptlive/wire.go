package gptlive

// Wire types of the primary Live WebSocket, as documented in the API
// reference (api/reference/resources/live/primary-websocket) and checked
// against a live session on 2026-09-23 (docs/dev/gpt-live-notes.md).

// Client event types.
const (
	evSessionStart       = "session.start"
	evInputAudioAppend   = "session.input_audio.append"
	evInstructionsAppend = "session.instructions.append"
	evThinkingAppend     = "session.thinking.append"
	evCommentaryAppend   = "session.commentary.append"
	evSessionClose       = "session.close"
)

// Server event types.
const (
	evSessionStarted      = "session.started"
	evOutputAudioDelta    = "session.output_audio.delta"
	evInputTranscript     = "session.input_transcript.delta"
	evOutputTranscript    = "session.output_transcript.delta"
	evDelegationCreated   = "session.delegation.created"
	evUsageUpdated        = "session.usage.updated"
	evSessionClosed       = "session.closed"
	evError               = "error"
	evInstructionsApplied = "session.instructions.appended"
	evThinkingApplied     = "session.thinking.appended"
	evCommentaryApplied   = "session.commentary.appended"
)

type startEvent struct {
	Type    string        `json:"type"`
	EventID string        `json:"event_id,omitempty"`
	Session sessionConfig `json:"session"`
}

type sessionConfig struct {
	Model        string        `json:"model"`
	Instructions string        `json:"instructions,omitempty"`
	Audio        audioConfig   `json:"audio"`
	Delegation   delegationCfg `json:"delegation"`
	Input        []initialItem `json:"input,omitempty"`
}

type audioConfig struct {
	Format audioFormat `json:"format"`
	Output audioOutput `json:"output"`
}

type audioFormat struct {
	Type string `json:"type"`
	Rate int    `json:"rate"`
}

type audioOutput struct {
	Voice string `json:"voice,omitempty"`
}

type delegationCfg struct {
	Type string `json:"type"`
}

// initialItem is one text message of session.input (at most 128, 8,192
// tokens; one text part each).
type initialItem struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type audioAppend struct {
	Type  string `json:"type"`
	Audio string `json:"audio"`
}

// appendEvent is session.instructions/thinking/commentary.append:
// delegation_id is required and nullable.
type appendEvent struct {
	Type         string  `json:"type"`
	EventID      string  `json:"event_id,omitempty"`
	DelegationID *string `json:"delegation_id"`
	Content      string  `json:"content"`
}

type closeEvent struct {
	Type    string `json:"type"`
	EventID string `json:"event_id,omitempty"`
}

// serverEvent is the union of the server events the session reads.
type serverEvent struct {
	Type          string `json:"type"`
	EventID       string `json:"event_id"`
	ClientEventID string `json:"client_event_id"`

	// session.started, session.closed
	Session *struct {
		ID        string `json:"id"`
		ExpiresAt int64  `json:"expires_at"`
		Model     string `json:"model"`
	} `json:"session"`
	// session.closed
	Reason string `json:"reason"`

	// output audio and transcripts
	Delta   string `json:"delta"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`

	// session.delegation.created
	OffsetMS   int64 `json:"offset_ms"`
	Delegation *struct {
		ID     string `json:"id"`
		Target string `json:"target"`
	} `json:"delegation"`

	// session.usage.updated, session.closed
	Usage *struct {
		Seconds float64 `json:"seconds"`
	} `json:"usage"`
	ContextWindow *struct {
		UsageRatio float64 `json:"usage_ratio"`
	} `json:"context_window"`

	// error
	Error *struct {
		Type          string `json:"type"`
		Code          string `json:"code"`
		Message       string `json:"message"`
		Param         string `json:"param"`
		ClientEventID string `json:"client_event_id"`
	} `json:"error"`
}
