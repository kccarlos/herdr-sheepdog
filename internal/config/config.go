// Package config loads the sheepdog daemon configuration.
//
// Configuration is read from ~/.config/sheepdog/config.toml when the file
// exists, then overridden by SHEEPDOG_* environment variables (and, for the
// Gemini key pool, GEMINI_API_KEY*). Only the keys needed by the herdr
// client, state table, voice session, brain, CLI query tool and
// supervisor are defined so far, plus the web server port; later work
// packages add their own sections here.
//
// String() must never print secrets: Gemini keys are rendered as a count,
// the DeepSeek and OpenAI keys as set or unset.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the fully resolved daemon configuration.
type Config struct {
	// DataDir is where the daemon keeps its files (state, event log).
	DataDir string `toml:"data_dir"`
	// DebounceMS is how long a `blocked` state must persist before it becomes
	// a transition (plan §3.1).
	DebounceMS int `toml:"debounce_ms"`
	// SnapshotIntervalS is the period of the session.snapshot diff loop.
	SnapshotIntervalS int `toml:"snapshot_interval_s"`
	// Aliases maps pane_id -> spoken alias, e.g. "w9:p1" = "auth".
	Aliases map[string]string `toml:"aliases"`

	// PendingTTLS is how long a pending action (an unanswered agent
	// question or an in-flight instruction) stays confirmable/cancellable.
	PendingTTLS int `toml:"pending_ttl_s"`

	Herdr      Herdr      `toml:"herdr"`
	State      State      `toml:"state"`
	Gemini     Gemini     `toml:"gemini"`
	Voice      Voice      `toml:"voice"`
	DeepSeek   DeepSeek   `toml:"deepseek"`
	Web        Web        `toml:"web"`
	CLI        CLI        `toml:"cli"`
	Supervisor Supervisor `toml:"supervisor"`
	Brain      Brain      `toml:"brain"`
	Prompts    Prompts    `toml:"prompts"`

	// deprecations are the notes about deprecated keys Load read
	// (Deprecations, deprecated.go).
	deprecations []string
}

// Prompts configures the prompt workspace (docs/roles.md "Prompt files"):
// user overrides of the embedded assistant/SVP/USER prompt files.
type Prompts struct {
	// Dir is the override directory: <dir>/<same relative path as in the
	// prompts/ package> overrides the matching embedded file; a missing
	// file or directory is not an error, the embedded default is used.
	// Re-read on SIGHUP and POST /api/prompts/reload, not just at start.
	Dir string `toml:"dir"`
}

// Brain configures the supervisor brain (plan D7-revised, docs/roles.md):
// the stateful agent that answers the shepherd's relayed words and the
// workers' events with tools over a function-calling model (Gemini or DeepSeek, switchable at runtime),
// keeping one transcript per daemon run under <data_dir>/brain/. When
// it errors or runs out of budget the supervisor falls back to the
// daemon's own code sentence.
type Brain struct {
	// Enabled turns the brain on (default true). `serve --brain=off`
	// overrides it; it is also silently off when the initial provider
	// has no key.
	Enabled bool `toml:"enabled"`
	// Provider is the provider the brain starts on: gemini (the Gemini
	// key pool) or deepseek ([deepseek]); the initial value only, like
	// Model (default gemini).
	Provider string `toml:"provider"`
	// Model is the function-calling model (default gemini-3.5-flash; a
	// model of Provider).
	Model string `toml:"model"`
	// MaxSteps bounds the model calls in one turn (default 6); beyond it
	// the turn falls back to the code sentence.
	MaxSteps int `toml:"max_steps"`
	// UtteranceTimeoutS bounds one utterance turn (the shepherd's relayed
	// words) and an investigation delivery turn, including tool calls
	// (default 12; formerly propose_timeout_s, still accepted).
	UtteranceTimeoutS int `toml:"utterance_timeout_s"`
	// WindowTokens is the context window target (estimated tokens); above
	// it the oldest half is compacted into a summary (default 24000).
	WindowTokens int `toml:"window_tokens"`
	// PruneAfterTurns is how many turns a tool result (screen, CLI output)
	// stays verbatim in the window before it is replaced by a one-line
	// stub; the disk transcript keeps it (default 3).
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
	// A finish waits this long (plus final_grace_s) for the brain's line
	// before anything is said (default 6; formerly enrich_timeout_s, still
	// accepted).
	AnnounceTurnTimeoutS int `toml:"announce_turn_timeout_s"`
	// AnnounceTimeoutS bounds an announce turn the shepherd is owed: one
	// that keeps a promise or reports the finish of an instruction they
	// gave; such a finish waits this long for the brain's line
	// (default 15).
	AnnounceTimeoutS int `toml:"announce_timeout_s"`
	// AnnounceWaitS is how long a block waits for the brain's announce
	// turn before the parsed prompt is spoken; a late line follows only
	// when it says the shepherd must act (default 2). A finish does not
	// use it: it waits for the turn's own budget (docs/roles.md "Pushes").
	AnnounceWaitS int `toml:"announce_wait_s"`
	// ReplyWaitS is how long a to_supervisor call waits for the brain's
	// reply before it returns a silent pending result and the reply is
	// pushed when it lands (0: default 2; negative: wait for the whole
	// turn, the fully synchronous behaviour).
	ReplyWaitS int `toml:"reply_wait_s"`
	// InvestigateMaxS is the sanity ceiling of one background
	// investigation — the only time limit on its answer; a job still
	// running after it is killed and reported as an error (default 600).
	InvestigateMaxS int `toml:"investigate_max_s"`
	// InvestigateSessionTTLS is how long an investigation's sub-agent
	// session stays resumable: a follow-up past it starts a fresh
	// session with the previous answer prepended (default 3600).
	InvestigateSessionTTLS int `toml:"investigate_session_ttl_s"`
}

// BrainProviders are the accepted values of brain.provider.
var BrainProviders = []string{"gemini", "deepseek"}

// DeepSeek configures the second supervisor-brain provider: DeepSeek's
// OpenAI-compatible chat completions API, paid per token. Registered
// with the brain when APIKey is set; selectable at runtime from the
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

// DeepSeekReasoningEfforts are the accepted values of
// deepseek.reasoning_effort.
var DeepSeekReasoningEfforts = []string{"low", "high", "max"}

// Supervisor tunes how proactive the voice side is (README "How chatty
// is it?").
type Supervisor struct {
	// Chattiness is the initial push policy: "quiet" (agent questions and
	// exits only), "normal" (plus finishes of agents you dealt with
	// recently) or "chatty" (every finish and every pick-up of an
	// instruction). It can be changed at runtime from /debug or
	// PUT /api/settings; that choice is kept in the state file and wins
	// over this value on restart.
	Chattiness string `toml:"chattiness"`
	// RecentMinutes is the "normal" level's window: a finish is announced
	// when the agent was blocked, was sent something, was asked about or
	// was named in a spoken briefing within this many minutes.
	RecentMinutes int `toml:"recent_minutes"`
	// PromiseTTLS is how long a promise ("tell me when it's done") waits
	// for its event before it expires silently (default 7200, 2 h).
	PromiseTTLS int `toml:"promise_ttl_s"`
	// CancelWindowS is how soon after an instruction went to a working
	// agent a "stop" still interrupts it with Ctrl-C (docs/roles.md
	// "Stop", phase 3; default 30).
	CancelWindowS int `toml:"cancel_window_s"`
	// RepromptPollS is how long the post-answer poll watches a pane after
	// keys go to it, in case it is already showing a different prompt
	// within herdr's own poll window with no state change in between
	// (docs/roles.md "blocked -> blocked is a block"; default 10).
	RepromptPollS int `toml:"reprompt_poll_s"`
	// RepeatWindowS is how long an unchanged roster sentence counts as
	// "already reported": a status question inside it gets a "no change
	// since I last checked" note instead of the roster repeated in full
	// (docs/history/issues-2026-09-18.md "Smaller items"; default 60).
	RepeatWindowS int `toml:"repeat_window_s"`
	// AuthoriseWindowS is how recent the shepherd's own words (a voice
	// transcript, typed text or a tap) must be to authorise answering a
	// worker's approval prompt (docs/roles.md "The one rail"; default 30).
	AuthoriseWindowS int `toml:"authorise_window_s"`
}

// Voice is the dog's spoken half, as the operator meets it.
type Voice struct {
	// Name is what the dog calls itself, substituted into
	// prompts/assistant.md as {name} (default "Sheepdog").
	Name string `toml:"name"`
	// Provider is the voice model the daemon starts on: "gemini" (Gemini
	// Live, [gemini]) or "openai" (GPT-Live, [voice.openai]). The initial
	// value only: the provider picked at runtime (PUT
	// /api/voice/provider, /debug) is kept in the state file's
	// session.voice_provider and wins over this on restart.
	Provider string `toml:"provider"`
	// OpenAI configures the GPT-Live provider.
	OpenAI VoiceOpenAI `toml:"openai"`
}

// VoiceOpenAI configures the GPT-Live voice provider (billed per second
// of open session, so the daemon closes it when idle).
type VoiceOpenAI struct {
	// APIKey authenticates GPT-Live (env OPENAI_API_KEY). Never logged.
	// The voice only: the brain never uses it.
	APIKey string `toml:"api_key"`
	// Model is the Live model (default gpt-live-1).
	Model string `toml:"model"`
	// Voice is the output voice (default marin).
	Voice string `toml:"voice"`
	// IdleCloseS closes a live GPT-Live session after this many seconds
	// without speech, a reply, a delegation or a push (default 45).
	IdleCloseS int `toml:"idle_close_s"`
}

// VoiceProviders are the accepted values of voice.provider.
var VoiceProviders = []string{"gemini", "openai"}

// GeminiSensitivities are the accepted values of
// gemini.start_sensitivity / gemini.end_sensitivity.
var GeminiSensitivities = []string{"low", "high", "default"}

// ChattinessLevels are the accepted values of supervisor.chattiness.
var ChattinessLevels = []string{"quiet", "normal", "chatty"}

// DefaultChattiness is the built-in supervisor.chattiness.
const DefaultChattiness = "normal"

// DefaultVoiceName is the built-in voice.name: what the dog calls
// itself (docs/roles.md "Names").
const DefaultVoiceName = "Sheepdog"

// validChattiness reports whether v is one of ChattinessLevels.
func validChattiness(v string) bool {
	for _, l := range ChattinessLevels {
		if v == l {
			return true
		}
	}
	return false
}

// CLI configures the read-only CLI query tool (plan §3.5, WP4): codex
// and/or claude, invoked non-interactively and read-only to answer
// questions the brain cannot answer from a screen alone, and to run a
// background investigation in a worker's repository. CodexCmd and
// ClaudeCmd are argv templates ({question}, {cwd}, {model} and
// {reasoning} are substituted), the *ResumeCmd ones take {session} too;
// no shell is invoked. See docs/cli-query-tools.md for why each flag is
// there.
type CLI struct {
	// Prefer picks the backend: "codex", "claude" or "auto" (codex if
	// detected on PATH, else claude, else the deep-ask path is skipped).
	Prefer string `toml:"prefer"`
	// CodexBin / ClaudeBin are looked up on PATH, or may be an absolute
	// path; they also become argv[0] of the matching *Cmd template.
	CodexBin  string `toml:"codex_bin"`
	ClaudeBin string `toml:"claude_bin"`
	// CodexCmd / ClaudeCmd are the argv templates; TOML-only (like
	// [aliases], not a single scalar an env var could sanely hold).
	CodexCmd  []string `toml:"codex_cmd"`
	ClaudeCmd []string `toml:"claude_cmd"`
	// CodexResumeCmd / ClaudeResumeCmd are the argv templates of an
	// investigation's follow-up: the same read-only invocation with
	// {session} — the session id the first run reported — substituted as
	// well (docs/cli-query-tools.md §3).
	CodexResumeCmd  []string `toml:"codex_resume_cmd"`
	ClaudeResumeCmd []string `toml:"claude_resume_cmd"`
	// ClaudeModel fills {model} in ClaudeCmd (default a cheap Haiku id).
	ClaudeModel string `toml:"claude_model"`
	// CodexReasoning fills {reasoning} in CodexCmd (default "low").
	CodexReasoning string `toml:"codex_reasoning"`
	// TimeoutS bounds one invocation; the process group is killed on
	// expiry (default 60).
	TimeoutS int `toml:"timeout_s"`
	// MaxOutputKB caps captured stdout; the tail is kept (default 16).
	MaxOutputKB int `toml:"max_output_kb"`
}

// Web configures the HTTP server (plan §3.8). It binds to Bind:Port,
// loopback only by default; a non-loopback Bind (a LAN or Tailscale
// address, or 0.0.0.0 for every interface) is how a phone reaches the
// daemon, and needs Token set (app.Build refuses to start without one —
// see IsLoopback and CONFIG.md "web.token").
type Web struct {
	// Bind is the listen address (default 127.0.0.1).
	Bind string `toml:"bind"`
	// Port is the TCP port (default 7777).
	Port int `toml:"port"`
	// Token, when non-empty, is the bearer token every request to /ws,
	// /events and /api/* must present: Authorization: Bearer <token>,
	// ?token=<token> for /ws and /events (a browser cannot set a header
	// on a WebSocket or EventSource request), or the cookie POST
	// /api/login sets so it is typed once. Required when Bind is not
	// loopback. Never logged: config show prints set/unset, like the LLM
	// keys.
	Token string `toml:"token"`
	// Dev enables developer-only routes: /testdata/<name>.wav serves the
	// audio fixtures so the /voice page can use one as a fake microphone
	// (WP6 file-source test mode). Off by default; `serve --dev` sets it.
	Dev bool `toml:"dev"`
	// DevTestdataDir is where /testdata/ reads from when Dev is on
	// (default testdata/audio relative to the working directory).
	DevTestdataDir string `toml:"dev_testdata_dir"`
}

// IsLoopback reports whether Bind reaches only this machine (127.0.0.1,
// ::1, or the hostname "localhost") — the one case that needs no Token,
// since nothing off-host can reach it.
func (w Web) IsLoopback() bool {
	if w.Bind == "localhost" {
		return true
	}
	ip := net.ParseIP(w.Bind)
	return ip != nil && ip.IsLoopback()
}

// DefaultDevTestdataDir is the directory /testdata/ serves in dev mode
// when web.dev_testdata_dir is empty: the repo's audio fixtures.
const DefaultDevTestdataDir = "testdata/audio"

// DefaultWebPort is the port the web server listens on by default.
const DefaultWebPort = 7777

// DefaultWebBind is the web.bind built-in: loopback only.
const DefaultWebBind = "127.0.0.1"

// Gemini configures the voice session (plan §6).
type Gemini struct {
	// APIKeys is the key pool. The voice session binds to one key per
	// connection and rotates to the next on a rate-limit error at connect.
	// Never logged.
	APIKeys []string `toml:"api_keys"`
	// Model is the Live model name.
	Model string `toml:"model"`
	// Voice is the prebuilt voice name.
	Voice string `toml:"voice"`
	// StallAfterS is how many seconds an utterance may go without any
	// model response (tool call or audio) before the session logs
	// voice.stalled; two stalls on one connection recycle it.
	StallAfterS int `toml:"stall_after_s"`
	// StartSensitivity / EndSensitivity tune Live's automatic activity
	// detection: "low", "high" or "default" (the server's own). Start
	// defaults to low: room noise was transcribed as speech ("fishing",
	// "Discord") at the default.
	StartSensitivity string `toml:"start_sensitivity"`
	EndSensitivity   string `toml:"end_sensitivity"`
}

// Herdr configures the herdr socket client.
type Herdr struct {
	// Socket is the path of the herdr unix socket.
	Socket string `toml:"socket"`
}

// State configures the persisted state table.
type State struct {
	// Path is the JSON file the state table is persisted to.
	Path string `toml:"path"`
	// FullFsync turns on the hardware-durable fsync pass after every
	// persist (debounced; see internal/state). Off by default: on macOS
	// the durable path is F_FULLFSYNC, which is dramatically slower than
	// the plain fsync used otherwise. Turn it on only if you need the
	// state file to survive a power loss, not just a process crash.
	FullFsync bool `toml:"full_fsync"`
}

// Default returns the built-in defaults, resolved against the current user's
// home directory. If the home directory cannot be determined, relative
// paths are used so the daemon still starts.
func Default() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dataDir := filepath.Join(home, ".local", "share", "sheepdog")
	return Config{
		DataDir:           dataDir,
		DebounceMS:        3000,
		SnapshotIntervalS: 30,
		Aliases:           map[string]string{},
		Herdr:             Herdr{Socket: filepath.Join(home, ".config", "herdr", "herdr.sock")},
		State:             State{Path: filepath.Join(dataDir, "state.json")},
		PendingTTLS:       120,
		Gemini:            Gemini{Model: "gemini-3.8-live", Voice: "Puck", StallAfterS: 10, StartSensitivity: "low", EndSensitivity: "default"},
		DeepSeek:          DeepSeek{BaseURL: DefaultDeepSeekBaseURL, Thinking: true, ReasoningEffort: DefaultDeepSeekReasoningEffort},
		Web:               Web{Bind: DefaultWebBind, Port: DefaultWebPort},
		CLI:               defaultCLI(),
		Voice:             Voice{Name: DefaultVoiceName, Provider: "gemini", OpenAI: VoiceOpenAI{Model: "gpt-live-1", Voice: "marin", IdleCloseS: 45}},
		Supervisor:        Supervisor{Chattiness: DefaultChattiness, RecentMinutes: 30, PromiseTTLS: 7200, CancelWindowS: 30, RepromptPollS: 10, RepeatWindowS: 60, AuthoriseWindowS: 30},
		Brain:             defaultBrain(),
		Prompts:           Prompts{Dir: filepath.Join(home, ".config", "sheepdog", "prompts")},
	}
}

// DefaultPromptsDir returns the default prompts.dir, resolved against the
// current user's home directory (mirrors Default()'s use for other
// per-user paths).
func DefaultPromptsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "sheepdog", "prompts")
}

// DefaultBrainModel is the built-in brain.model; DefaultBrainProvider the
// built-in brain.provider.
const (
	DefaultBrainModel    = "gemini-3.5-flash"
	DefaultBrainProvider = "gemini"
)

// The built-in [deepseek] defaults.
const (
	DefaultDeepSeekBaseURL         = "https://api.deepseek.com"
	DefaultDeepSeekReasoningEffort = "low"
)

func defaultBrain() Brain {
	return Brain{Enabled: true, Provider: DefaultBrainProvider, Model: DefaultBrainModel, MaxSteps: 6, UtteranceTimeoutS: 12, WindowTokens: 24000, PruneAfterTurns: 3, AnnounceTurnTimeoutS: 6, AnnounceTimeoutS: 15, AnnounceWaitS: 2, ReplyWaitS: 2, InvestigateMaxS: 600, InvestigateSessionTTLS: 3600}
}

// defaultCLI mirrors the argv templates verified in
// docs/cli-query-tools.md §3. {question}, {cwd}, {model} and {reasoning}
// are substituted by internal/cli; no shell is invoked.
func defaultCLI() CLI {
	return CLI{
		Prefer:    "auto",
		CodexBin:  "codex",
		ClaudeBin: "claude",
		CodexCmd: []string{
			"codex", "exec",
			"--sandbox", "read-only",
			"--skip-git-repo-check",
			"--color", "never",
			"-c", "model_reasoning_effort={reasoning}",
			"-C", "{cwd}",
			"--json",
			"{question}",
		},
		ClaudeCmd: []string{
			"claude", "-p", "{question}",
			"--model", "{model}",
			"--tools", "Read,Grep,Glob,WebSearch",
			"--allowedTools", "Read,Grep,Glob,WebSearch",
			"--strict-mcp-config",
			"--permission-mode", "dontAsk",
			"--permission-prompts", "none",
			"--output-format", "json",
			"--max-budget-usd", "0.50",
		},
		CodexResumeCmd: []string{
			"codex", "exec", "resume", "{session}", "{question}",
			"-c", "sandbox_mode=read-only",
			"-c", "model_reasoning_effort={reasoning}",
			"--skip-git-repo-check",
			"--json",
		},
		ClaudeResumeCmd: []string{
			"claude", "-p", "{question}",
			"--resume", "{session}",
			"--model", "{model}",
			"--tools", "Read,Grep,Glob,WebSearch",
			"--allowedTools", "Read,Grep,Glob,WebSearch",
			"--strict-mcp-config",
			"--permission-mode", "dontAsk",
			"--permission-prompts", "none",
			"--output-format", "json",
			"--max-budget-usd", "0.50",
		},
		ClaudeModel:    "claude-haiku-4-5-20251001",
		CodexReasoning: "low",
		TimeoutS:       60,
		MaxOutputKB:    16,
	}
}

// DefaultPath returns the default location of the config file.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "sheepdog", "config.toml")
}

// Load reads the TOML file at path (DefaultPath() when path is empty) on top
// of Default(), then applies SHEEPDOG_* environment overrides. A missing file
// is not an error; a malformed one is.
func Load(path string) (Config, error) {
	return load(path, os.LookupEnv)
}

func load(path string, lookup func(string) (string, bool)) (Config, error) {
	cfg := Default()
	if path == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		md, err := toml.Decode(string(data), &cfg)
		if err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
		if err := applyDeprecatedKeys(&cfg, data, md); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// No file: defaults + env only.
	default:
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if cfg.Aliases == nil {
		cfg.Aliases = map[string]string{}
	}
	if err := applyDeprecatedEnv(&cfg, lookup); err != nil {
		return Config{}, err
	}
	if err := applyEnv(&cfg, lookup); err != nil {
		return Config{}, err
	}
	if !validChattiness(cfg.Supervisor.Chattiness) {
		return Config{}, fmt.Errorf("config: supervisor.chattiness=%q: want one of %s", cfg.Supervisor.Chattiness, strings.Join(ChattinessLevels, ", "))
	}
	if cfg.Supervisor.RecentMinutes <= 0 {
		return Config{}, fmt.Errorf("config: supervisor.recent_minutes=%d: want a positive integer", cfg.Supervisor.RecentMinutes)
	}
	if cfg.Supervisor.PromiseTTLS <= 0 {
		return Config{}, fmt.Errorf("config: supervisor.promise_ttl_s=%d: want a positive integer", cfg.Supervisor.PromiseTTLS)
	}
	if cfg.Supervisor.RepromptPollS <= 0 {
		return Config{}, fmt.Errorf("config: supervisor.reprompt_poll_s=%d: want a positive integer", cfg.Supervisor.RepromptPollS)
	}
	if cfg.Supervisor.RepeatWindowS <= 0 {
		return Config{}, fmt.Errorf("config: supervisor.repeat_window_s=%d: want a positive integer", cfg.Supervisor.RepeatWindowS)
	}
	if cfg.Supervisor.AuthoriseWindowS <= 0 {
		return Config{}, fmt.Errorf("config: supervisor.authorise_window_s=%d: want a positive integer", cfg.Supervisor.AuthoriseWindowS)
	}
	if err := validateBrain(cfg.Brain); err != nil {
		return Config{}, err
	}
	if err := validateDeepSeek(cfg.DeepSeek); err != nil {
		return Config{}, err
	}
	if err := validateVoice(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// validateVoice checks [voice], [voice.openai] and the [gemini]
// sensitivities.
func validateVoice(cfg Config) error {
	if !oneOf(cfg.Voice.Provider, VoiceProviders) {
		return fmt.Errorf("config: voice.provider=%q: want one of %s", cfg.Voice.Provider, strings.Join(VoiceProviders, ", "))
	}
	if cfg.Voice.OpenAI.IdleCloseS <= 0 {
		return fmt.Errorf("config: voice.openai.idle_close_s=%d: want a positive integer", cfg.Voice.OpenAI.IdleCloseS)
	}
	if strings.TrimSpace(cfg.Voice.OpenAI.Model) == "" {
		return errors.New("config: voice.openai.model must not be empty")
	}
	for _, f := range []struct{ name, v string }{{"start_sensitivity", cfg.Gemini.StartSensitivity}, {"end_sensitivity", cfg.Gemini.EndSensitivity}} {
		if !oneOf(f.v, GeminiSensitivities) {
			return fmt.Errorf("config: gemini.%s=%q: want one of %s", f.name, f.v, strings.Join(GeminiSensitivities, ", "))
		}
	}
	return nil
}

// validateDeepSeek checks the [deepseek] values.
func validateDeepSeek(d DeepSeek) error {
	if strings.TrimSpace(d.BaseURL) == "" {
		return errors.New("config: deepseek.base_url must not be empty")
	}
	if !oneOf(d.ReasoningEffort, DeepSeekReasoningEfforts) {
		return fmt.Errorf("config: deepseek.reasoning_effort=%q: want one of %s", d.ReasoningEffort, strings.Join(DeepSeekReasoningEfforts, ", "))
	}
	return nil
}

// oneOf reports whether v is in list.
func oneOf(v string, list []string) bool {
	for _, l := range list {
		if v == l {
			return true
		}
	}
	return false
}

// validateBrain checks the [brain] integers.
func validateBrain(b Brain) error {
	for _, f := range []struct {
		name string
		v    int
	}{{"max_steps", b.MaxSteps}, {"utterance_timeout_s", b.UtteranceTimeoutS}, {"window_tokens", b.WindowTokens}, {"prune_after_turns", b.PruneAfterTurns}, {"announce_turn_timeout_s", b.AnnounceTurnTimeoutS}, {"announce_timeout_s", b.AnnounceTimeoutS}, {"announce_wait_s", b.AnnounceWaitS}, {"investigate_max_s", b.InvestigateMaxS}, {"investigate_session_ttl_s", b.InvestigateSessionTTLS}} {
		if f.v <= 0 {
			return fmt.Errorf("config: brain.%s=%d: want a positive integer", f.name, f.v)
		}
	}
	if b.Model == "" {
		return errors.New("config: brain.model must not be empty")
	}
	if !oneOf(b.Provider, BrainProviders) {
		return fmt.Errorf("config: brain.provider=%q: want one of %s", b.Provider, strings.Join(BrainProviders, ", "))
	}
	if b.StepReserveS < 0 {
		return fmt.Errorf("config: brain.step_reserve_s=%d: want 0 (default) or a positive integer", b.StepReserveS)
	}
	return nil
}

// applyEnv overrides cfg from SHEEPDOG_* variables. Recognised names:
//
//	SHEEPDOG_DATA_DIR, SHEEPDOG_DEBOUNCE_MS, SHEEPDOG_SNAPSHOT_INTERVAL_S,
//	SHEEPDOG_HERDR_SOCKET, SHEEPDOG_STATE_PATH, SHEEPDOG_STATE_FULL_FSYNC,
//	SHEEPDOG_GEMINI_MODEL,
//	SHEEPDOG_GEMINI_VOICE, SHEEPDOG_GEMINI_STALL_AFTER_S,
//	DEEPSEEK_API_KEY, SHEEPDOG_DEEPSEEK_BASE_URL, SHEEPDOG_DEEPSEEK_THINKING,
//	SHEEPDOG_DEEPSEEK_REASONING_EFFORT,
//	SHEEPDOG_PENDING_TTL_S, SHEEPDOG_WEB_BIND, SHEEPDOG_WEB_PORT, SHEEPDOG_WEB_TOKEN,
//	SHEEPDOG_CLI_PREFER, SHEEPDOG_CLI_CODEX_BIN, SHEEPDOG_CLI_CLAUDE_BIN,
//	SHEEPDOG_CLI_CLAUDE_MODEL, SHEEPDOG_CLI_CODEX_REASONING,
//	SHEEPDOG_CLI_TIMEOUT_S, SHEEPDOG_CLI_MAX_OUTPUT_KB,
//	SHEEPDOG_SUPERVISOR_CHATTINESS, SHEEPDOG_SUPERVISOR_RECENT_MINUTES,
//	SHEEPDOG_SUPERVISOR_PROMISE_TTL_S, SHEEPDOG_SUPERVISOR_CANCEL_WINDOW_S,
//	SHEEPDOG_SUPERVISOR_REPROMPT_POLL_S, SHEEPDOG_SUPERVISOR_REPEAT_WINDOW_S,
//	SHEEPDOG_SUPERVISOR_AUTHORISE_WINDOW_S,
//	SHEEPDOG_VOICE_NAME, SHEEPDOG_VOICE_PROVIDER, OPENAI_API_KEY,
//	SHEEPDOG_VOICE_OPENAI_MODEL, SHEEPDOG_VOICE_OPENAI_VOICE,
//	SHEEPDOG_VOICE_OPENAI_IDLE_CLOSE_S,
//	SHEEPDOG_GEMINI_START_SENSITIVITY, SHEEPDOG_GEMINI_END_SENSITIVITY,
//	SHEEPDOG_BRAIN_ENABLED, SHEEPDOG_BRAIN_PROVIDER, SHEEPDOG_BRAIN_MODEL,
//	SHEEPDOG_BRAIN_MAX_STEPS,
//	SHEEPDOG_BRAIN_ASK_TIMEOUT_S, SHEEPDOG_BRAIN_PROPOSE_TIMEOUT_S,
//	SHEEPDOG_BRAIN_WINDOW_TOKENS, SHEEPDOG_BRAIN_PRUNE_AFTER_TURNS,
//	SHEEPDOG_BRAIN_STEP_RESERVE_S, SHEEPDOG_BRAIN_FINAL_GRACE_S,
//	SHEEPDOG_BRAIN_REPLY_WAIT_S,
//	SHEEPDOG_BRAIN_ENRICH_TIMEOUT_S, SHEEPDOG_BRAIN_ANNOUNCE_TIMEOUT_S,
//	SHEEPDOG_PROMPTS_DIR.
//	The [cli] argv templates (codex_cmd, claude_cmd) are TOML-only, like
//	[aliases].
//
// The Gemini key pool is collected from GEMINI_API_KEY, then
// GEMINI_API_KEY_1 … GEMINI_API_KEY_9, in that order; if any is set the
// pool from the file is replaced. Aliases are only configurable through
// the TOML file.
func applyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	if v, ok := lookup("SHEEPDOG_GEMINI_MODEL"); ok {
		cfg.Gemini.Model = v
	}
	if v, ok := lookup("SHEEPDOG_GEMINI_VOICE"); ok {
		cfg.Gemini.Voice = v
	}
	if v, ok := lookup("SHEEPDOG_GEMINI_STALL_AFTER_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_GEMINI_STALL_AFTER_S=%q: want a positive integer", v)
		}
		cfg.Gemini.StallAfterS = n
	}
	if keys := geminiKeysFromEnv(lookup); len(keys) > 0 {
		cfg.Gemini.APIKeys = keys
	}
	if err := applyDeepSeekEnv(cfg, lookup); err != nil {
		return err
	}
	if v, ok := lookup("SHEEPDOG_DATA_DIR"); ok {
		cfg.DataDir = v
	}
	if v, ok := lookup("SHEEPDOG_HERDR_SOCKET"); ok {
		cfg.Herdr.Socket = v
	}
	if v, ok := lookup("SHEEPDOG_STATE_PATH"); ok {
		cfg.State.Path = v
	}
	if v, ok := lookup("SHEEPDOG_STATE_FULL_FSYNC"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: SHEEPDOG_STATE_FULL_FSYNC=%q: want true or false", v)
		}
		cfg.State.FullFsync = b
	}
	if v, ok := lookup("SHEEPDOG_DEBOUNCE_MS"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("config: SHEEPDOG_DEBOUNCE_MS=%q: want a non-negative integer", v)
		}
		cfg.DebounceMS = n
	}
	if v, ok := lookup("SHEEPDOG_SNAPSHOT_INTERVAL_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_SNAPSHOT_INTERVAL_S=%q: want a positive integer", v)
		}
		cfg.SnapshotIntervalS = n
	}
	if v, ok := lookup("SHEEPDOG_PENDING_TTL_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_PENDING_TTL_S=%q: want a positive integer", v)
		}
		cfg.PendingTTLS = n
	}
	if v, ok := lookup("SHEEPDOG_WEB_BIND"); ok {
		cfg.Web.Bind = v
	}
	if v, ok := lookup("SHEEPDOG_WEB_PORT"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("config: SHEEPDOG_WEB_PORT=%q: want a port number", v)
		}
		cfg.Web.Port = n
	}
	// Like DEEPSEEK_API_KEY: an empty env value does not clear a file
	// value (never logged).
	if v, ok := lookup("SHEEPDOG_WEB_TOKEN"); ok && v != "" {
		cfg.Web.Token = v
	}
	if err := applyCLIEnv(cfg, lookup); err != nil {
		return err
	}
	if v, ok := lookup("SHEEPDOG_SUPERVISOR_CHATTINESS"); ok {
		cfg.Supervisor.Chattiness = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := lookup("SHEEPDOG_SUPERVISOR_RECENT_MINUTES"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_SUPERVISOR_RECENT_MINUTES=%q: want a positive integer", v)
		}
		cfg.Supervisor.RecentMinutes = n
	}
	if v, ok := lookup("SHEEPDOG_SUPERVISOR_PROMISE_TTL_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_SUPERVISOR_PROMISE_TTL_S=%q: want a positive integer", v)
		}
		cfg.Supervisor.PromiseTTLS = n
	}
	if v, ok := lookup("SHEEPDOG_SUPERVISOR_CANCEL_WINDOW_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_SUPERVISOR_CANCEL_WINDOW_S=%q: want a positive integer", v)
		}
		cfg.Supervisor.CancelWindowS = n
	}
	if v, ok := lookup("SHEEPDOG_SUPERVISOR_REPROMPT_POLL_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_SUPERVISOR_REPROMPT_POLL_S=%q: want a positive integer", v)
		}
		cfg.Supervisor.RepromptPollS = n
	}
	if v, ok := lookup("SHEEPDOG_SUPERVISOR_REPEAT_WINDOW_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_SUPERVISOR_REPEAT_WINDOW_S=%q: want a positive integer", v)
		}
		cfg.Supervisor.RepeatWindowS = n
	}
	if v, ok := lookup("SHEEPDOG_SUPERVISOR_AUTHORISE_WINDOW_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_SUPERVISOR_AUTHORISE_WINDOW_S=%q: want a positive integer", v)
		}
		cfg.Supervisor.AuthoriseWindowS = n
	}
	if v, ok := lookup("SHEEPDOG_VOICE_NAME"); ok {
		cfg.Voice.Name = strings.TrimSpace(v)
	}
	if err := applyVoiceEnv(cfg, lookup); err != nil {
		return err
	}
	if v, ok := lookup("SHEEPDOG_PROMPTS_DIR"); ok {
		cfg.Prompts.Dir = v
	}
	return applyBrainEnv(cfg, lookup)
}

// applyVoiceEnv overrides [voice] and [voice.openai] (the key from
// OPENAI_API_KEY, like the other provider keys) and the [gemini]
// sensitivities.
func applyVoiceEnv(cfg *Config, lookup func(string) (string, bool)) error {
	if v, ok := lookup("SHEEPDOG_VOICE_PROVIDER"); ok {
		cfg.Voice.Provider = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := lookup("OPENAI_API_KEY"); ok && v != "" {
		cfg.Voice.OpenAI.APIKey = v
	}
	if v, ok := lookup("SHEEPDOG_VOICE_OPENAI_MODEL"); ok {
		cfg.Voice.OpenAI.Model = strings.TrimSpace(v)
	}
	if v, ok := lookup("SHEEPDOG_VOICE_OPENAI_VOICE"); ok {
		cfg.Voice.OpenAI.Voice = strings.TrimSpace(v)
	}
	if v, ok := lookup("SHEEPDOG_VOICE_OPENAI_IDLE_CLOSE_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_VOICE_OPENAI_IDLE_CLOSE_S=%q: want a positive integer", v)
		}
		cfg.Voice.OpenAI.IdleCloseS = n
	}
	if v, ok := lookup("SHEEPDOG_GEMINI_START_SENSITIVITY"); ok {
		cfg.Gemini.StartSensitivity = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := lookup("SHEEPDOG_GEMINI_END_SENSITIVITY"); ok {
		cfg.Gemini.EndSensitivity = strings.ToLower(strings.TrimSpace(v))
	}
	return nil
}

// applyDeepSeekEnv overrides the [deepseek] keys: the key from
// DEEPSEEK_API_KEY (like GEMINI_API_KEY, no SHEEPDOG_ prefix), the rest
// from SHEEPDOG_DEEPSEEK_*.
func applyDeepSeekEnv(cfg *Config, lookup func(string) (string, bool)) error {
	if v, ok := lookup("DEEPSEEK_API_KEY"); ok && v != "" {
		cfg.DeepSeek.APIKey = v
	}
	if v, ok := lookup("SHEEPDOG_DEEPSEEK_BASE_URL"); ok {
		cfg.DeepSeek.BaseURL = v
	}
	if v, ok := lookup("SHEEPDOG_DEEPSEEK_THINKING"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: SHEEPDOG_DEEPSEEK_THINKING=%q: want true or false", v)
		}
		cfg.DeepSeek.Thinking = b
	}
	if v, ok := lookup("SHEEPDOG_DEEPSEEK_REASONING_EFFORT"); ok {
		cfg.DeepSeek.ReasoningEffort = strings.ToLower(strings.TrimSpace(v))
	}
	return nil
}

// applyBrainEnv overrides the [brain] keys from SHEEPDOG_BRAIN_*.
func applyBrainEnv(cfg *Config, lookup func(string) (string, bool)) error {
	if v, ok := lookup("SHEEPDOG_BRAIN_ENABLED"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: SHEEPDOG_BRAIN_ENABLED=%q: want true or false", v)
		}
		cfg.Brain.Enabled = b
	}
	if v, ok := lookup("SHEEPDOG_BRAIN_PROVIDER"); ok {
		cfg.Brain.Provider = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := lookup("SHEEPDOG_BRAIN_MODEL"); ok {
		cfg.Brain.Model = v
	}
	for _, f := range []struct {
		env string
		dst *int
	}{
		{"SHEEPDOG_BRAIN_MAX_STEPS", &cfg.Brain.MaxSteps},
		{"SHEEPDOG_BRAIN_UTTERANCE_TIMEOUT_S", &cfg.Brain.UtteranceTimeoutS},
		{"SHEEPDOG_BRAIN_WINDOW_TOKENS", &cfg.Brain.WindowTokens},
		{"SHEEPDOG_BRAIN_PRUNE_AFTER_TURNS", &cfg.Brain.PruneAfterTurns},
		{"SHEEPDOG_BRAIN_ANNOUNCE_TURN_TIMEOUT_S", &cfg.Brain.AnnounceTurnTimeoutS},
		{"SHEEPDOG_BRAIN_ANNOUNCE_TIMEOUT_S", &cfg.Brain.AnnounceTimeoutS},
		{"SHEEPDOG_BRAIN_ANNOUNCE_WAIT_S", &cfg.Brain.AnnounceWaitS},
		{"SHEEPDOG_BRAIN_INVESTIGATE_MAX_S", &cfg.Brain.InvestigateMaxS},
		{"SHEEPDOG_BRAIN_INVESTIGATE_SESSION_TTL_S", &cfg.Brain.InvestigateSessionTTLS},
	} {
		v, ok := lookup(f.env)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: %s=%q: want a positive integer", f.env, v)
		}
		*f.dst = n
	}
	if v, ok := lookup("SHEEPDOG_BRAIN_STEP_RESERVE_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("config: SHEEPDOG_BRAIN_STEP_RESERVE_S=%q: want 0 (default) or a positive integer", v)
		}
		cfg.Brain.StepReserveS = n
	}
	if v, ok := lookup("SHEEPDOG_BRAIN_FINAL_GRACE_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("config: SHEEPDOG_BRAIN_FINAL_GRACE_S=%q: want an integer", v)
		}
		cfg.Brain.FinalGraceS = n
	}
	if v, ok := lookup("SHEEPDOG_BRAIN_REPLY_WAIT_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("config: SHEEPDOG_BRAIN_REPLY_WAIT_S=%q: want an integer", v)
		}
		cfg.Brain.ReplyWaitS = n
	}
	return nil
}

// applyCLIEnv overrides the [cli] scalars from SHEEPDOG_CLI_* (the argv
// templates codex_cmd/claude_cmd are TOML-only, like [aliases]).
func applyCLIEnv(cfg *Config, lookup func(string) (string, bool)) error {
	if v, ok := lookup("SHEEPDOG_CLI_PREFER"); ok {
		cfg.CLI.Prefer = v
	}
	if v, ok := lookup("SHEEPDOG_CLI_CODEX_BIN"); ok {
		cfg.CLI.CodexBin = v
	}
	if v, ok := lookup("SHEEPDOG_CLI_CLAUDE_BIN"); ok {
		cfg.CLI.ClaudeBin = v
	}
	if v, ok := lookup("SHEEPDOG_CLI_CLAUDE_MODEL"); ok {
		cfg.CLI.ClaudeModel = v
	}
	if v, ok := lookup("SHEEPDOG_CLI_CODEX_REASONING"); ok {
		cfg.CLI.CodexReasoning = v
	}
	if v, ok := lookup("SHEEPDOG_CLI_TIMEOUT_S"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_CLI_TIMEOUT_S=%q: want a positive integer", v)
		}
		cfg.CLI.TimeoutS = n
	}
	if v, ok := lookup("SHEEPDOG_CLI_MAX_OUTPUT_KB"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("config: SHEEPDOG_CLI_MAX_OUTPUT_KB=%q: want a positive integer", v)
		}
		cfg.CLI.MaxOutputKB = n
	}
	return nil
}

// geminiKeysFromEnv returns the non-empty values of GEMINI_API_KEY and
// GEMINI_API_KEY_1..9 in that order.
func geminiKeysFromEnv(lookup func(string) (string, bool)) []string {
	var keys []string
	if v, ok := lookup("GEMINI_API_KEY"); ok && v != "" {
		keys = append(keys, v)
	}
	for i := 1; i <= 9; i++ {
		if v, ok := lookup(fmt.Sprintf("GEMINI_API_KEY_%d", i)); ok && v != "" {
			keys = append(keys, v)
		}
	}
	return keys
}

// String renders the configuration for logs. Secrets are rendered as
// counts (or set/unset) only; keep it that way when secret-bearing
// fields are added.
func (c Config) String() string {
	return fmt.Sprintf("data_dir=%s herdr.socket=%s state.path=%s state.full_fsync=%t debounce_ms=%d snapshot_interval_s=%d pending_ttl_s=%d aliases=%d gemini.model=%s gemini.voice=%s gemini.stall_after_s=%d gemini.start_sensitivity=%s gemini.end_sensitivity=%s gemini.api_keys=%d voice.name=%s voice.provider=%s voice.openai.api_key=%s voice.openai.model=%s voice.openai.voice=%s voice.openai.idle_close_s=%d deepseek.api_key=%s deepseek.base_url=%s deepseek.thinking=%t deepseek.reasoning_effort=%s web.bind=%s web.port=%d web.token=%s cli.prefer=%s cli.timeout_s=%d cli.max_output_kb=%d supervisor.chattiness=%s supervisor.recent_minutes=%d supervisor.promise_ttl_s=%d supervisor.cancel_window_s=%d supervisor.reprompt_poll_s=%d supervisor.repeat_window_s=%d supervisor.authorise_window_s=%d brain.enabled=%t brain.provider=%s brain.model=%s brain.max_steps=%d brain.utterance_timeout_s=%d brain.window_tokens=%d brain.prune_after_turns=%d brain.step_reserve_s=%d brain.final_grace_s=%d brain.announce_turn_timeout_s=%d brain.announce_timeout_s=%d brain.announce_wait_s=%d brain.reply_wait_s=%d brain.investigate_max_s=%d brain.investigate_session_ttl_s=%d prompts.dir=%s",
		c.DataDir, c.Herdr.Socket, c.State.Path, c.State.FullFsync, c.DebounceMS, c.SnapshotIntervalS, c.PendingTTLS, len(c.Aliases), c.Gemini.Model, c.Gemini.Voice, c.Gemini.StallAfterS, c.Gemini.StartSensitivity, c.Gemini.EndSensitivity, len(c.Gemini.APIKeys), c.Voice.Name, c.Voice.Provider, setOrUnset(c.Voice.OpenAI.APIKey), c.Voice.OpenAI.Model, c.Voice.OpenAI.Voice, c.Voice.OpenAI.IdleCloseS, setOrUnset(c.DeepSeek.APIKey), c.DeepSeek.BaseURL, c.DeepSeek.Thinking, c.DeepSeek.ReasoningEffort, c.Web.Bind, c.Web.Port, setOrUnset(c.Web.Token), c.CLI.Prefer, c.CLI.TimeoutS, c.CLI.MaxOutputKB, c.Supervisor.Chattiness, c.Supervisor.RecentMinutes, c.Supervisor.PromiseTTLS, c.Supervisor.CancelWindowS, c.Supervisor.RepromptPollS, c.Supervisor.RepeatWindowS, c.Supervisor.AuthoriseWindowS, c.Brain.Enabled, c.Brain.Provider, c.Brain.Model, c.Brain.MaxSteps, c.Brain.UtteranceTimeoutS, c.Brain.WindowTokens, c.Brain.PruneAfterTurns, c.Brain.StepReserveS, c.Brain.FinalGraceS, c.Brain.AnnounceTurnTimeoutS, c.Brain.AnnounceTimeoutS, c.Brain.AnnounceWaitS, c.Brain.ReplyWaitS, c.Brain.InvestigateMaxS, c.Brain.InvestigateSessionTTLS, c.Prompts.Dir)
}

// setOrUnset renders a secret's presence, never its value.
func setOrUnset(secret string) string {
	if secret == "" {
		return "unset"
	}
	return "set"
}
