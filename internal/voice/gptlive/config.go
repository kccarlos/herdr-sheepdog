package gptlive

import (
	"errors"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Defaults.
const (
	// DefaultModel is the Live model (docs: "Model ID: gpt-live-1").
	DefaultModel = "gpt-live-1"
	// DefaultVoice is the documented default voice.
	DefaultVoice = "marin"
	// DefaultURL is the primary WebSocket endpoint.
	DefaultURL = "wss://api.openai.com/v1/live/sessions"
	// SampleRate is the session's audio format (PCM16 mono, both ways).
	SampleRate = 24000
)

// ErrNoAPIKey is returned by New without a key.
var ErrNoAPIKey = errors.New("gptlive: no OPENAI_API_KEY configured")

// Config for a Session. Zero values take the defaults noted.
type Config struct {
	// APIKey authenticates the WebSocket (Authorization: Bearer). Never
	// logged. Required.
	APIKey string
	// Model is the Live model (default gpt-live-1).
	Model string
	// Voice is the output voice (default marin).
	Voice string
	// URL overrides the endpoint (tests).
	URL string
	// Instructions renders the session instructions; it is called on
	// every open so a prompt reload takes effect on the next session.
	// nil means no instructions.
	Instructions func() (string, error)
	// Handler executes the delegated tool calls (to_supervisor, stop).
	Handler voice.ToolHandler
	// Context, when set, receives this session's voice context (the
	// /debug Context tab), reconstructed from what the daemon sent
	// (capture.go).
	Context *ctxcap.VoiceLog
	// Classify maps an utterance to the tool call it makes (stop for a
	// bare stop word, to_supervisor otherwise). nil sends everything to
	// to_supervisor, which runs the same stop check itself. A settled
	// utterance no delegation claimed is also run through it: a bare
	// stop word runs stop even when the model did not delegate.
	Classify func(text string) (voice.ToolCall, bool)
	// Names, when set, returns the agents' current spoken names: the
	// heard check counts the ones a push mentions among its key terms
	// (heard.go).
	Names func() []string

	// IdleClose is how long a live session may go without activity
	// before it is closed (config voice.openai.idle_close_s, default 45 s).
	IdleClose time.Duration
	// SettleWait is how long after the last input transcript fragment
	// the utterance counts as finished (default 700 ms).
	SettleWait time.Duration
	// SettleMax bounds how long a delegation waits for the input
	// transcript to settle before it takes what it has (default 2 s).
	SettleMax time.Duration
	// SpeechHold is how long after the last voiced output audio or
	// output transcript fragment the model still counts as speaking
	// (default 800 ms).
	SpeechHold time.Duration
	// PauseHold is how long silent output audio keeps being forwarded
	// after voiced audio, so pauses inside a reply survive (default 1.2 s).
	PauseHold time.Duration
	// PushGap is the minimum gap between two spoken pushes (default 2 s).
	PushGap time.Duration
	// PushTTL drops a spoken push that could not be delivered (no client
	// listening) for this long (default 5 min).
	PushTTL time.Duration
	// OpenRMS is the RMS level (PCM16) a microphone frame must reach to
	// open a session (default 300); quieter frames are only buffered.
	OpenRMS float64
	// VoicedPeak is the peak level above which an output chunk is speech
	// (default 200).
	VoicedPeak int
	// NoiseRun is how long a run of loud microphone frames may hold off
	// the idle close (the shepherd may be mid-sentence before a
	// transcript arrives); a longer run is room noise (default 8 s).
	NoiseRun time.Duration
	// StartTimeout bounds dial + session.started (default 15 s).
	StartTimeout time.Duration
	// CloseTimeout bounds the wait for session.closed (default 5 s).
	CloseTimeout time.Duration
	// OpenBackoff is the wait after a failed open (default 5 s; a rate
	// limit waits RateLimitBackoff).
	OpenBackoff      time.Duration
	RateLimitBackoff time.Duration
	// Tick is the housekeeping period (default 40 ms, the keep-alive
	// frame length).
	Tick time.Duration
	// RecapTurns / RecapChars bound the text history seeded into a
	// reopened session (defaults 12 / 2000).
	RecapTurns int
	RecapChars int
	// ReconnectBuffer bounds the microphone audio (24 kHz bytes) kept
	// while a session opens (default 5 s).
	ReconnectBuffer int
	// EventBuffer is the capacity of the Events channel (default 1024).
	EventBuffer int
}

func (c Config) withDefaults() Config {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Voice == "" {
		c.Voice = DefaultVoice
	}
	if c.URL == "" {
		c.URL = DefaultURL
	}
	def(&c.IdleClose, 45*time.Second)
	def(&c.SettleWait, 700*time.Millisecond)
	def(&c.SettleMax, 2*time.Second)
	def(&c.SpeechHold, 800*time.Millisecond)
	def(&c.PauseHold, 1200*time.Millisecond)
	def(&c.PushGap, 2*time.Second)
	def(&c.PushTTL, 5*time.Minute)
	def(&c.StartTimeout, 15*time.Second)
	def(&c.CloseTimeout, 5*time.Second)
	def(&c.OpenBackoff, 5*time.Second)
	def(&c.RateLimitBackoff, 30*time.Second)
	def(&c.Tick, 40*time.Millisecond)
	def(&c.NoiseRun, 8*time.Second)
	if c.OpenRMS <= 0 {
		c.OpenRMS = 300
	}
	if c.VoicedPeak <= 0 {
		c.VoicedPeak = 200
	}
	if c.RecapTurns <= 0 {
		c.RecapTurns = 12
	}
	if c.RecapChars <= 0 {
		c.RecapChars = 2000
	}
	if c.ReconnectBuffer <= 0 {
		c.ReconnectBuffer = 5 * SampleRate * 2
	}
	if c.EventBuffer <= 0 {
		c.EventBuffer = 1024
	}
	return c
}
