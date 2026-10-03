package gemini

import (
	"errors"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// Config for a Session. Zero values are replaced by the defaults below;
// only APIKeys is required.
type Config struct {
	// APIKeys is the key pool (config gemini.api_keys). A connection is
	// bound to one key; rotation happens only at (re)connect. Never logged.
	// On Vertex it holds the one Vertex key: a rate limit then backs off
	// and retries the same key.
	APIKeys []string
	// Vertex, when set, sends the Live connection to Vertex AI instead of
	// Google AI Studio (config gemini.backend = "vertex"). Only the
	// connection differs; everything above it is backend-agnostic.
	Vertex *Vertex
	// Model is the Live model (default "gemini-3.8-live").
	Model string
	// Voice is the prebuilt voice name (default "Puck").
	Voice string
	// Aliases are the spoken agent names, put in the system prompt and in
	// the input transcription CustomVocabulary.
	Aliases []string
	// Names, when set, returns the agents' current spoken names
	// (docs/roles.md "Agent names") and replaces Aliases: read at every
	// (re)connect, so the system prompt and the vocabulary follow the
	// roster; the supervisor's silent names line covers a running
	// connection. An empty result falls back to Aliases.
	Names func() []string
	// Name is what the dog calls itself (config voice.name, default
	// "Sheepdog"), substituted into the system prompt as {name}.
	Name string
	// SystemPrompt overrides prompts/assistant.md (and any override of
	// it) entirely when non-empty. {aliases} and {name} are substituted
	// in both.
	SystemPrompt string
	// Prompts resolves prompts/assistant.md against its override
	// (docs/roles.md "Prompt files"); nil uses the embedded default.
	// Re-rendered on every (re)connect, so a Reload takes effect on the
	// next reconnect, not mid-session.
	Prompts *prompts.Loader
	// Handler executes tool calls. nil means every call fails.
	Handler voice.ToolHandler
	// Context, when set, receives this session's voice context (the
	// /debug Context tab): what the daemon sent the model and what it
	// answered, reconstructed per connection (capture.go).
	Context *ctxcap.VoiceLog
	// StartSensitivity / EndSensitivity are Live's automatic activity
	// detection sensitivities: "low", "high", or ""/"default" for the
	// server's own (config gemini.start_sensitivity / end_sensitivity).
	StartSensitivity string
	EndSensitivity   string

	// PushGap is the minimum gap between two spoken pushes (default 2 s).
	PushGap time.Duration
	// SpeechHold is how long after the last input transcription fragment
	// the user still counts as speaking (default 1.5 s).
	SpeechHold time.Duration
	// TurnTimeout bounds how long an idle push waits for a turn that never
	// completes (default 30 s).
	TurnTimeout time.Duration
	// ResumeAttempts is how many consecutive reconnects may fail while
	// presenting a resumption handle before the handle is dropped and the
	// next connect starts a fresh session (default 1: in both observed
	// 1011 closes the second try failed exactly like the first).
	ResumeAttempts int
	// StallAfter is how long an utterance may go without any model
	// reaction (tool call or audio) before voice.stalled is logged and
	// EventStatus{Stalled:true} is emitted (default 10 s).
	StallAfter time.Duration
	// StallsBeforeRecycle is how many stalls, or idle pushes that outwait
	// TurnTimeout, on one connection trigger a proactive reconnect with
	// the current resumption handle (default 2).
	StallsBeforeRecycle int
	// ReconnectBuffer is the maximum number of audio-in bytes kept while
	// reconnecting; oldest are dropped (default 5 s = 160 000).
	ReconnectBuffer int
	// RateLimitBackoff is the wait before reconnecting after a 429 whose
	// body carries no retry delay (default 30 s).
	RateLimitBackoff time.Duration
	// ReconnectBackoff is the initial wait after a non-rate-limit connect
	// failure; it doubles up to 30 s (default 1 s).
	ReconnectBackoff time.Duration
	// CompressionTrigger / CompressionTarget configure sliding-window
	// context compression in tokens (defaults 16 000 / 4 000, plan §6).
	CompressionTrigger int64
	CompressionTarget  int64
	// EventBuffer is the capacity of the Events channel (default 1024).
	EventBuffer int
	// GateTail is the comfort noise sent before audioStreamEnd when the
	// client's silence gate closes (default 1 s; negative disables), so
	// Live's activity detection hears the speech end (session.go
	// AudioStreamEnd).
	GateTail time.Duration
	// ToolResponseWait bounds how long a tool response is held back for
	// the model to close the turn that issued the call (default 1.5 s).
	// Responding before that turnComplete made the model treat the result
	// as unavailable in WP2's first live run.
	ToolResponseWait time.Duration
}

// Vertex selects the Vertex AI backend (config [gemini.vertex]).
type Vertex struct {
	// Project is the Google Cloud project id.
	Project string
	// Location serves the Live model, e.g. "us-central1" (the global
	// endpoint does not serve gemini-3.8-live).
	Location string
}

// Backend names the backend for logs and labels: "aistudio" or "vertex".
func (c Config) Backend() string {
	if c.Vertex != nil {
		return "vertex"
	}
	return "aistudio"
}

// DefaultModel is the Live model used when Config.Model is empty.
const DefaultModel = "gemini-3.8-live"

// ErrNoAPIKeys is returned by New when the key pool is empty.
var ErrNoAPIKeys = errors.New("gemini: no API keys configured")

// ErrVertexConfig is returned by New when Vertex is set without a project
// or a location.
var ErrVertexConfig = errors.New("gemini: vertex needs a project and a location")

func (c Config) withDefaults() Config {
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Voice == "" {
		c.Voice = "Puck"
	}
	if c.PushGap == 0 {
		c.PushGap = 2 * time.Second
	}
	if c.SpeechHold == 0 {
		c.SpeechHold = 1500 * time.Millisecond
	}
	if c.TurnTimeout == 0 {
		c.TurnTimeout = 30 * time.Second
	}
	if c.ResumeAttempts == 0 {
		c.ResumeAttempts = 1
	}
	if c.StallAfter == 0 {
		c.StallAfter = 10 * time.Second
	}
	if c.StallsBeforeRecycle == 0 {
		c.StallsBeforeRecycle = 2
	}
	if c.ReconnectBuffer == 0 {
		c.ReconnectBuffer = 5 * 16000 * 2
	}
	if c.RateLimitBackoff == 0 {
		c.RateLimitBackoff = 30 * time.Second
	}
	if c.ReconnectBackoff == 0 {
		c.ReconnectBackoff = time.Second
	}
	if c.CompressionTrigger == 0 {
		c.CompressionTrigger = 16000
	}
	if c.CompressionTarget == 0 {
		c.CompressionTarget = 4000
	}
	if c.EventBuffer == 0 {
		c.EventBuffer = 1024
	}
	if c.GateTail == 0 {
		c.GateTail = time.Second
	}
	if c.ToolResponseWait == 0 {
		c.ToolResponseWait = 1500 * time.Millisecond
	}
	return c
}

// names is the agent name list of one connection: Names when it has
// any, else Aliases.
func (c Config) names() []string {
	if c.Names != nil {
		if n := c.Names(); len(n) > 0 {
			return n
		}
	}
	return c.Aliases
}
