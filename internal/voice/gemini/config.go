package gemini

import (
	"errors"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Config for a Session. Zero values are replaced by the defaults below;
// only APIKeys is required.
type Config struct {
	// APIKeys is the key pool (config gemini.api_keys). A connection is
	// bound to one key; rotation happens only at (re)connect. Never logged.
	APIKeys []string
	// Model is the Live model (default "gemini-3.8-live").
	Model string
	// Voice is the prebuilt voice name (default "Puck").
	Voice string
	// Aliases are the spoken agent names, put in the system prompt and in
	// the input transcription CustomVocabulary.
	Aliases []string
	// SystemPrompt overrides the embedded prompts/voice_system.txt when
	// non-empty. {aliases} is substituted in both.
	SystemPrompt string
	// Handler executes tool calls. nil means every call fails.
	Handler voice.ToolHandler

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
	// next connect starts a fresh session (default 2).
	ResumeAttempts int
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
	// ToolResponseWait bounds how long a tool response is held back for
	// the model to close the turn that issued the call (default 1.5 s).
	// Responding before that turnComplete made the model treat the result
	// as unavailable in WP2's first live run.
	ToolResponseWait time.Duration
}

// DefaultModel is the Live model used when Config.Model is empty.
const DefaultModel = "gemini-3.8-live"

// ErrNoAPIKeys is returned by New when the key pool is empty.
var ErrNoAPIKeys = errors.New("gemini: no API keys configured")

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
		c.ResumeAttempts = 2
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
	if c.ToolResponseWait == 0 {
		c.ToolResponseWait = 1500 * time.Millisecond
	}
	return c
}
