package config

import (
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Brain configures the orchestrator (plan D7-revised, docs/dev/roles.md):
// the stateful agent that answers the user's relayed words and the
// workers' events with tools over a function-calling model (Gemini or DeepSeek, switchable at runtime),
// keeping one transcript per daemon run under <data_dir>/brain/. When
// it errors or runs out of budget the supervisor falls back to the
// daemon's own code sentence.
type Brain struct {
	// Enabled turns the orchestrator on (default true). `serve --brain=off`
	// overrides it; it is also silently off when the initial provider
	// has no key.
	Enabled bool `toml:"enabled"`
	// Provider is the provider the orchestrator starts on: gemini (the Gemini
	// key pool), deepseek ([deepseek]) or vertex (Gemini on Vertex AI
	// with the [gemini.vertex] key and project, always at location
	// global); the initial value only, like Model (default gemini).
	Provider string `toml:"provider"`
	// Model is the function-calling model (default gemini-3.5-flash; a
	// model of Provider).
	Model string `toml:"model"`
	// MaxSteps bounds the model calls in one turn (default 6); beyond it
	// the turn falls back to the code sentence.
	MaxSteps int `toml:"max_steps"`
	// UtteranceTimeoutS bounds one utterance turn (the user's relayed
	// words) and an investigation delivery turn, including tool calls
	// (default 25).
	UtteranceTimeoutS int `toml:"utterance_timeout_s"`
	// WindowTokens is the context window target (estimated tokens); above
	// it the oldest half is compacted into a summary (default 12000;
	// 24000 before docs/dev/iteration-2.md WP5).
	WindowTokens int `toml:"window_tokens"`
	// PruneAfterTurns is how many turns a tool result (screen, CLI output)
	// stays verbatim in the window before it is replaced by a one-line
	// stub; the disk transcript keeps it (default 2; 3 before
	// docs/dev/iteration-2.md WP5).
	PruneAfterTurns int `toml:"prune_after_turns"`
	// StepReserveS: when less of the turn's timeout remains after a tool
	// round than this, the next step is answer-only (no more tools), so
	// the turn ends with an answer rather than another read (0: default
	// 3s).
	StepReserveS int `toml:"step_reserve_s"`
	// FinalGraceS is how far past the timeout that one answer-only call
	// may run before the turn falls back (0: default 3s; negative: none).
	FinalGraceS int `toml:"final_grace_s"`
	// AnnounceTurnTimeoutS bounds an event turn (the announce turn on a
	// block or finish); on expiry the template sentence is spoken instead.
	// A finish waits this long (plus final_grace_s) for the orchestrator's line
	// before anything is said, counted from when the turn gets the turn
	// lock; queued behind a user turn, it may first wait up to
	// utterance_timeout_s (default 6).
	AnnounceTurnTimeoutS int `toml:"announce_turn_timeout_s"`
	// AnnounceTimeoutS bounds an announce turn the user is owed: one
	// that keeps a promise or reports the finish of an instruction they
	// gave; such a finish waits this long for the orchestrator's line
	// (default 15).
	AnnounceTimeoutS int `toml:"announce_timeout_s"`
	// AnnounceWaitS is how long a block waits for the orchestrator's announce
	// turn before the parsed prompt is spoken; a late line follows only
	// when it says the user must act (default 2). A finish does not
	// use it: it waits for the turn's own budget (docs/dev/roles.md "Pushes").
	AnnounceWaitS int `toml:"announce_wait_s"`
	// ReplyWaitS is how long a to_supervisor call waits for the orchestrator's
	// reply before it returns a silent pending result and the reply is
	// pushed when it lands (0: default 30, above the utterance budget, so
	// the reply is the call's own result; negative: wait for the whole
	// turn, the fully synchronous behaviour, where a barge-in cancels it).
	ReplyWaitS int `toml:"reply_wait_s"`
	// InvestigateMaxS is the sanity ceiling of one background
	// investigation — the only time limit on its answer; a job still
	// running after it is killed and reported as an error (default 600).
	InvestigateMaxS int `toml:"investigate_max_s"`
	// InvestigateSessionTTLS is how long an investigation's sub-agent
	// session stays resumable: a follow-up past it starts a fresh
	// session with the previous answer prepended (default 3600).
	InvestigateSessionTTLS int `toml:"investigate_session_ttl_s"`
	// OpenAICompat lists extra orchestrator providers over any
	// OpenAI-compatible chat completions endpoint (OpenRouter, a local
	// Ollama /v1, Groq, …), one [[brain.openai_compat]] table each.
	// Each is registered with the orchestrator when its key is set (or,
	// for a loopback base_url, without one), selectable from the /debug
	// picker and `brain-test --provider`, and joins failover; its name
	// is a valid brain.provider.
	OpenAICompat []OpenAICompat `toml:"openai_compat"`
}

// OpenAICompat is one [[brain.openai_compat]] provider.
type OpenAICompat struct {
	// Name is the provider id (lowercase letters, digits, '-' and '_';
	// not gemini, deepseek, vertex or fake*).
	Name string `toml:"name"`
	// Label is the /debug picker's display name (default Name).
	Label string `toml:"label"`
	// BaseURL is the API root; /chat/completions and /models are
	// appended (e.g. https://openrouter.ai/api/v1,
	// http://localhost:11434/v1, https://api.groq.com/openai/v1).
	BaseURL string `toml:"base_url"`
	// APIKeyEnv names the environment variable (or env-file entry)
	// holding the key (default the upper-cased name + "_API_KEY", '-'
	// as '_'). The key itself is never put in the config file.
	APIKeyEnv string `toml:"api_key_env"`
	// Model is the model id the provider runs by default: the initial
	// model when brain.provider names it and brain.model is not set,
	// and the model a failover to it uses.
	Model string `toml:"model"`
	// APIKey is read from APIKeyEnv at load. Never logged.
	APIKey string `toml:"-"`
}

// KeyOptional reports whether the provider may run without a key: a
// local server on a loopback address (Ollama needs none).
func (o OpenAICompat) KeyOptional() bool {
	u, err := url.Parse(strings.TrimSpace(o.BaseURL))
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Usable reports whether the provider has what it needs to be
// registered: a key, or a loopback base_url.
func (o OpenAICompat) Usable() bool { return o.APIKey != "" || o.KeyOptional() }

// CompatProvider returns the [[brain.openai_compat]] entry named name.
func (b Brain) CompatProvider(name string) (OpenAICompat, bool) {
	for _, o := range b.OpenAICompat {
		if o.Name == name {
			return o, true
		}
	}
	return OpenAICompat{}, false
}

// BrainProviders are the built-in values of brain.provider; the names
// of [[brain.openai_compat]] entries are accepted too.
var BrainProviders = []string{"gemini", "deepseek", "vertex", "anthropic"}

// compatNameRE is the shape of an [[brain.openai_compat]] name.
var compatNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// DefaultCompatKeyEnv is the default api_key_env of a provider named
// name: OPENROUTER_API_KEY for openrouter, MY_GROQ_API_KEY for my-groq.
func DefaultCompatKeyEnv(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_API_KEY"
}

// DeepSeek configures the second orchestrator provider: DeepSeek's
// OpenAI-compatible chat completions API, paid per token. Registered
// with the orchestrator when APIKey is set; selectable at runtime from the
// /debug model picker whatever brain.provider says.
type DeepSeek struct {
	// APIKey authenticates requests (env DEEPSEEK_API_KEY). Never logged.
	APIKey string `toml:"api_key"`
	// BaseURL is the API root (default https://api.deepseek.com).
	BaseURL string `toml:"base_url"`
	// Thinking turns DeepSeek's thinking mode on (default true).
	Thinking bool `toml:"thinking"`
	// ReasoningEffort is the thinking effort: low, high or max (default
	// low, measured against the 8 s ask budget; see CONFIG.md).
	ReasoningEffort string `toml:"reasoning_effort"`
}

// Anthropic configures the native Anthropic orchestrator provider
// (docs/dev/anthropic-adapter.md): the Messages API, paid per token.
// Registered with the orchestrator when APIKey is set; selectable at
// runtime from the /debug model picker whatever brain.provider says.
type Anthropic struct {
	// APIKey authenticates requests (env ANTHROPIC_API_KEY). Never
	// logged.
	APIKey string `toml:"api_key"`
	// BaseURL is the API root (default https://api.anthropic.com).
	BaseURL string `toml:"base_url"`
	// Effort is output_config.effort on utterance turns (default
	// medium); EventEffort on event turns and the compaction summary
	// (default low).
	Effort      string `toml:"effort"`
	EventEffort string `toml:"event_effort"`
	// Fallbacks turns on the server-side refusal fallbacks (default
	// true).
	Fallbacks bool `toml:"fallbacks"`
}

// AnthropicEfforts are the accepted values of anthropic.effort and
// anthropic.event_effort.
var AnthropicEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// DeepSeekReasoningEfforts are the accepted values of
// deepseek.reasoning_effort.
var DeepSeekReasoningEfforts = []string{"low", "high", "max"}

// DefaultBrainModel is the built-in brain.model; DefaultBrainProvider the
// built-in brain.provider.
const (
	DefaultBrainModel    = "gemini-3.5-flash"
	DefaultBrainProvider = "gemini"
)

// The built-in [anthropic] defaults: the API root, and the model
// brain.provider = "anthropic" runs when brain.model is not set.
const (
	DefaultAnthropicBaseURL = "https://api.anthropic.com"
	DefaultAnthropicModel   = "claude-opus-5-5"
)

// The built-in [deepseek] defaults.
const (
	DefaultDeepSeekBaseURL         = "https://api.deepseek.com"
	DefaultDeepSeekReasoningEffort = "low"
)

func defaultBrain() Brain {
	return Brain{Enabled: true, Provider: DefaultBrainProvider, Model: DefaultBrainModel, MaxSteps: 6, UtteranceTimeoutS: 25, WindowTokens: 12000, PruneAfterTurns: 2, AnnounceTurnTimeoutS: 6, AnnounceTimeoutS: 15, AnnounceWaitS: 2, ReplyWaitS: 30, InvestigateMaxS: 600, InvestigateSessionTTLS: 3600}
}
