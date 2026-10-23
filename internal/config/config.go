// Package config loads the sheepdog daemon configuration.
//
// Configuration is read from ~/.config/sheepdog/config.toml when the file
// exists, then overridden by SHEEPDOG_* environment variables (and, for the
// Gemini key pool, GEMINI_API_KEY*). Only the keys needed by the herdr
// client, state table, voice session, orchestrator, CLI query tool and
// supervisor are defined so far, plus the web server port; later work
// packages add their own sections here.
//
// String() must never print secrets: Gemini keys are rendered as a count,
// the DeepSeek and OpenAI keys as set or unset.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	// PendingTTLS is how long an in-flight instruction's pending action
	// stays cancellable. An unanswered agent question has no TTL: it
	// lives while the agent is blocked at that prompt.
	PendingTTLS int `toml:"pending_ttl_s"`

	Herdr      Herdr      `toml:"herdr"`
	State      State      `toml:"state"`
	Gemini     Gemini     `toml:"gemini"`
	Voice      Voice      `toml:"voice"`
	DeepSeek   DeepSeek   `toml:"deepseek"`
	Anthropic  Anthropic  `toml:"anthropic"`
	Web        Web        `toml:"web"`
	CLI        CLI        `toml:"cli"`
	Supervisor Supervisor `toml:"supervisor"`
	Brain      Brain      `toml:"brain"`
	Prompts    Prompts    `toml:"prompts"`
	Push       Push       `toml:"push"`
	Log        Log        `toml:"log"`
	Events     Events     `toml:"events"`
	Update     Update     `toml:"update"`
	Computer   Computer   `toml:"computer"`
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
		Gemini:            Gemini{Model: "gemini-3.8-live", Voice: "Puck", StallAfterS: 10, StartSensitivity: "low", EndSensitivity: "default", BriefingChannel: "function_response", Backend: "aistudio", Vertex: GeminiVertex{Location: "us-central1"}},
		DeepSeek:          DeepSeek{BaseURL: DefaultDeepSeekBaseURL, Thinking: true, ReasoningEffort: DefaultDeepSeekReasoningEffort},
		Anthropic:         Anthropic{BaseURL: DefaultAnthropicBaseURL, Effort: "medium", EventEffort: "low", Fallbacks: true},
		Web:               Web{Bind: DefaultWebBind, Port: DefaultWebPort, Shell: true},
		CLI:               defaultCLI(),
		Voice:             Voice{Name: DefaultVoiceName, Provider: "gemini", OpenAI: VoiceOpenAI{Model: "gpt-live-1", Voice: "marin", IdleCloseS: 45}},
		Supervisor:        Supervisor{Chattiness: DefaultChattiness, RecentMinutes: 30, PromiseTTLS: 7200, CancelWindowS: 30, RepromptPollS: 10, RepeatWindowS: 60, AuthoriseWindowS: 30, ProjectRoots: []string{"~/Documents"}},
		Brain:             defaultBrain(),
		Prompts:           Prompts{Dir: filepath.Join(home, ".config", "sheepdog", "prompts")},
		Log:               Log{Level: "info", MaxSizeMB: 10, MaxFiles: 3},
		Events:            Events{RetentionDays: 14},
		Update:            Update{Check: true, AutoRestart: true},
		Push:              Push{Relay: PushRelay{URL: DefaultRelayURL}},
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

// DefaultPath returns the default location of the config file.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "sheepdog", "config.toml")
}

// DefaultEnvPath returns the secret file next to the default config:
// ~/.config/sheepdog/.env (written by `sheepdog init`, mode 0600). serve
// loads it when there is no ./.env.
func DefaultEnvPath() string {
	return filepath.Join(filepath.Dir(DefaultPath()), ".env")
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
	modelSet := false
	switch {
	case err == nil:
		md, err := toml.Decode(string(data), &cfg)
		if err != nil {
			return Config{}, parseError(path, err)
		}
		modelSet = md.IsDefined("brain", "model")
	case errors.Is(err, os.ErrNotExist):
		// No file: defaults + env only.
	default:
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	if cfg.Aliases == nil {
		cfg.Aliases = map[string]string{}
	}
	if err := applyEnv(&cfg, lookup); err != nil {
		return Config{}, err
	}
	applyCompatEnv(&cfg, lookup)
	if _, ok := lookup("SHEEPDOG_BRAIN_MODEL"); ok {
		modelSet = true
	}
	if cfg.Brain.Provider == "anthropic" && !modelSet {
		cfg.Brain.Model = DefaultAnthropicModel
	}
	if o, ok := cfg.Brain.CompatProvider(cfg.Brain.Provider); ok && !modelSet {
		// The built-in default model is Gemini's; an OpenAI-compatible
		// provider starts on its own model unless brain.model says
		// otherwise.
		cfg.Brain.Model = o.Model
	}
	cfg.Supervisor.ProjectRoots = expandRoots(cfg.Supervisor.ProjectRoots)
	if err := validate(cfg); err != nil {
		return Config{}, locate(path, data, err)
	}
	return cfg, nil
}

// String renders the configuration for logs. Secrets are rendered as
// counts (or set/unset) only; keep it that way when secret-bearing
// fields are added.
func (c Config) String() string {
	return fmt.Sprintf("data_dir=%s herdr.socket=%s herdr.bin=%s state.path=%s state.full_fsync=%t debounce_ms=%d snapshot_interval_s=%d pending_ttl_s=%d aliases=%d gemini.model=%s gemini.voice=%s gemini.stall_after_s=%d gemini.start_sensitivity=%s gemini.end_sensitivity=%s gemini.api_keys=%d gemini.backend=%s gemini.vertex.api_key=%s gemini.vertex.project=%s gemini.vertex.location=%s voice.name=%s voice.provider=%s voice.openai.api_key=%s voice.openai.model=%s voice.openai.voice=%s voice.openai.idle_close_s=%d deepseek.api_key=%s deepseek.base_url=%s deepseek.thinking=%t deepseek.reasoning_effort=%s anthropic.api_key=%s anthropic.base_url=%s anthropic.effort=%s anthropic.event_effort=%s anthropic.fallbacks=%t web.bind=%s web.port=%d web.shell=%t web.advertise=%s web.tailscale_https=%t cli.prefer=%s cli.timeout_s=%d cli.max_output_kb=%d supervisor.chattiness=%s supervisor.recent_minutes=%d supervisor.promise_ttl_s=%d supervisor.cancel_window_s=%d supervisor.reprompt_poll_s=%d supervisor.repeat_window_s=%d supervisor.authorise_window_s=%d supervisor.project_roots=%s brain.enabled=%t brain.provider=%s brain.model=%s brain.max_steps=%d brain.utterance_timeout_s=%d brain.window_tokens=%d brain.prune_after_turns=%d brain.step_reserve_s=%d brain.final_grace_s=%d brain.announce_turn_timeout_s=%d brain.announce_timeout_s=%d brain.announce_wait_s=%d brain.reply_wait_s=%d brain.investigate_max_s=%d brain.investigate_session_ttl_s=%d prompts.dir=%s push.mode=%s push.relay.url=%s push.apns.key_path=%s push.apns.key_id=%s push.apns.team_id=%s push.apns.topic=%s",
		c.DataDir, c.Herdr.Socket, c.Herdr.Bin, c.State.Path, c.State.FullFsync, c.DebounceMS, c.SnapshotIntervalS, c.PendingTTLS, len(c.Aliases), c.Gemini.Model, c.Gemini.Voice, c.Gemini.StallAfterS, c.Gemini.StartSensitivity, c.Gemini.EndSensitivity, len(c.Gemini.APIKeys), c.Gemini.Backend, setOrUnset(c.Gemini.Vertex.APIKey), c.Gemini.Vertex.Project, c.Gemini.Vertex.Location, c.Voice.Name, c.Voice.Provider, setOrUnset(c.Voice.OpenAI.APIKey), c.Voice.OpenAI.Model, c.Voice.OpenAI.Voice, c.Voice.OpenAI.IdleCloseS, setOrUnset(c.DeepSeek.APIKey), c.DeepSeek.BaseURL, c.DeepSeek.Thinking, c.DeepSeek.ReasoningEffort, setOrUnset(c.Anthropic.APIKey), c.Anthropic.BaseURL, c.Anthropic.Effort, c.Anthropic.EventEffort, c.Anthropic.Fallbacks, c.Web.Bind, c.Web.Port, c.Web.Shell, strings.Join(c.Web.Advertise, ","), c.Web.TailscaleHTTPS, c.CLI.Prefer, c.CLI.TimeoutS, c.CLI.MaxOutputKB, c.Supervisor.Chattiness, c.Supervisor.RecentMinutes, c.Supervisor.PromiseTTLS, c.Supervisor.CancelWindowS, c.Supervisor.RepromptPollS, c.Supervisor.RepeatWindowS, c.Supervisor.AuthoriseWindowS, strings.Join(c.Supervisor.ProjectRoots, string(filepath.ListSeparator)), c.Brain.Enabled, c.Brain.Provider, c.Brain.Model, c.Brain.MaxSteps, c.Brain.UtteranceTimeoutS, c.Brain.WindowTokens, c.Brain.PruneAfterTurns, c.Brain.StepReserveS, c.Brain.FinalGraceS, c.Brain.AnnounceTurnTimeoutS, c.Brain.AnnounceTimeoutS, c.Brain.AnnounceWaitS, c.Brain.ReplyWaitS, c.Brain.InvestigateMaxS, c.Brain.InvestigateSessionTTLS, c.Prompts.Dir, c.Push.EffectiveMode(), c.Push.Relay.URL, setOrUnset(c.Push.APNs.KeyPath), c.Push.APNs.KeyID, c.Push.APNs.TeamID, c.Push.APNs.Topic) + c.compatString()
}

// compatString renders the [[brain.openai_compat]] entries for String,
// the key as set or unset.
func (c Config) compatString() string {
	var b strings.Builder
	for _, o := range c.Brain.OpenAICompat {
		fmt.Fprintf(&b, " brain.openai_compat.%s.base_url=%s brain.openai_compat.%s.model=%s brain.openai_compat.%s.api_key_env=%s brain.openai_compat.%s.api_key=%s", o.Name, o.BaseURL, o.Name, o.Model, o.Name, o.APIKeyEnv, o.Name, setOrUnset(o.APIKey))
	}
	return b.String()
}

// setOrUnset renders a secret's presence, never its value.
func setOrUnset(secret string) string {
	if secret == "" {
		return "unset"
	}
	return "set"
}

// expandRoots expands a leading "~/" (or a bare "~") of each project
// root to the home directory and drops empty entries.
func expandRoots(roots []string) []string {
	home, _ := os.UserHomeDir()
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		r = strings.TrimSpace(r)
		switch {
		case r == "":
			continue
		case home != "" && r == "~":
			r = home
		case home != "" && strings.HasPrefix(r, "~/"):
			r = filepath.Join(home, r[2:])
		}
		out = append(out, r)
	}
	return out
}
