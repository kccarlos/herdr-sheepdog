package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// applyCompatEnv fills each [[brain.openai_compat]] key from its
// api_key_env (defaulted from the name).
func applyCompatEnv(cfg *Config, lookup func(string) (string, bool)) {
	for i := range cfg.Brain.OpenAICompat {
		o := &cfg.Brain.OpenAICompat[i]
		o.Name = strings.ToLower(strings.TrimSpace(o.Name))
		o.APIKeyEnv = strings.TrimSpace(o.APIKeyEnv)
		if o.APIKeyEnv == "" {
			o.APIKeyEnv = DefaultCompatKeyEnv(o.Name)
		}
		if v, ok := lookup(o.APIKeyEnv); ok {
			o.APIKey = strings.TrimSpace(v)
		}
	}
}

// applyEnv overrides cfg from SHEEPDOG_* variables. Recognised names:
//
//	SHEEPDOG_DATA_DIR, SHEEPDOG_DEBOUNCE_MS, SHEEPDOG_SNAPSHOT_INTERVAL_S,
//	SHEEPDOG_HERDR_SOCKET, SHEEPDOG_HERDR_BIN, SHEEPDOG_STATE_PATH, SHEEPDOG_STATE_FULL_FSYNC,
//	SHEEPDOG_COMPUTER_NAME,
//	SHEEPDOG_GEMINI_MODEL,
//	SHEEPDOG_GEMINI_VOICE, SHEEPDOG_GEMINI_STALL_AFTER_S,
//	DEEPSEEK_API_KEY, SHEEPDOG_DEEPSEEK_BASE_URL, SHEEPDOG_DEEPSEEK_THINKING,
//	SHEEPDOG_DEEPSEEK_REASONING_EFFORT, ANTHROPIC_API_KEY, SHEEPDOG_ANTHROPIC_BASE_URL,
//	SHEEPDOG_ANTHROPIC_EFFORT, SHEEPDOG_ANTHROPIC_EVENT_EFFORT, SHEEPDOG_ANTHROPIC_FALLBACKS,
//	SHEEPDOG_PENDING_TTL_S, SHEEPDOG_WEB_BIND, SHEEPDOG_WEB_PORT, SHEEPDOG_WEB_SHELL, SHEEPDOG_WEB_ADVERTISE,
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
//	SHEEPDOG_GEMINI_BACKEND, VERTEX_API_KEY, VERTEX_PROJECT, VERTEX_LOCATION,
//	SHEEPDOG_BRAIN_ENABLED, SHEEPDOG_BRAIN_PROVIDER, SHEEPDOG_BRAIN_MODEL,
//	SHEEPDOG_BRAIN_MAX_STEPS,
//	SHEEPDOG_BRAIN_UTTERANCE_TIMEOUT_S,
//	SHEEPDOG_BRAIN_WINDOW_TOKENS, SHEEPDOG_BRAIN_PRUNE_AFTER_TURNS,
//	SHEEPDOG_BRAIN_STEP_RESERVE_S, SHEEPDOG_BRAIN_FINAL_GRACE_S,
//	SHEEPDOG_BRAIN_REPLY_WAIT_S,
//	SHEEPDOG_BRAIN_ANNOUNCE_TURN_TIMEOUT_S, SHEEPDOG_BRAIN_ANNOUNCE_TIMEOUT_S,
//	SHEEPDOG_PROMPTS_DIR,
//	SHEEPDOG_PUSH_MODE, SHEEPDOG_PUSH_RELAY_URL,
//	SHEEPDOG_APNS_KEY_PATH, SHEEPDOG_APNS_KEY_ID, SHEEPDOG_APNS_TEAM_ID,
//	SHEEPDOG_APNS_TOPIC.
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
	if err := applyAnthropicEnv(cfg, lookup); err != nil {
		return err
	}
	if v, ok := lookup("SHEEPDOG_DATA_DIR"); ok {
		cfg.DataDir = v
	}
	if v, ok := lookup("SHEEPDOG_LOG_LEVEL"); ok {
		cfg.Log.Level = v
	}
	if v, ok := lookup("SHEEPDOG_EVENTS_RETENTION_DAYS"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("config: SHEEPDOG_EVENTS_RETENTION_DAYS=%q: want 0 (keep forever) or a number of days", v)
		}
		cfg.Events.RetentionDays = n
	}
	if v, ok := lookup("SHEEPDOG_UPDATE_CHECK"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: SHEEPDOG_UPDATE_CHECK=%q: want true or false", v)
		}
		cfg.Update.Check = b
	}
	if v, ok := lookup("SHEEPDOG_UPDATE_AUTO_RESTART"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: SHEEPDOG_UPDATE_AUTO_RESTART=%q: want true or false", v)
		}
		cfg.Update.AutoRestart = b
	}
	if v, ok := lookup("SHEEPDOG_COMPUTER_NAME"); ok {
		cfg.Computer.Name = v
	}
	if v, ok := lookup("SHEEPDOG_HERDR_SOCKET"); ok {
		cfg.Herdr.Socket = v
	}
	if v, ok := lookup("SHEEPDOG_HERDR_BIN"); ok {
		cfg.Herdr.Bin = v
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
	if v, ok := lookup("SHEEPDOG_WEB_SHELL"); ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: SHEEPDOG_WEB_SHELL=%q: want true or false", v)
		}
		cfg.Web.Shell = b
	}
	if v, ok := lookup("SHEEPDOG_WEB_ADVERTISE"); ok && strings.TrimSpace(v) != "" {
		cfg.Web.Advertise = nil
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a != "" {
				cfg.Web.Advertise = append(cfg.Web.Advertise, a)
			}
		}
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
	if v, ok := lookup("SHEEPDOG_SUPERVISOR_PROJECT_ROOTS"); ok {
		cfg.Supervisor.ProjectRoots = filepath.SplitList(v)
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
// OPENAI_API_KEY, like the other provider keys), the [gemini]
// sensitivities and backend, and [gemini.vertex] (VERTEX_*).
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
	if v, ok := lookup("SHEEPDOG_GEMINI_BRIEFING_CHANNEL"); ok {
		cfg.Gemini.BriefingChannel = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := lookup("SHEEPDOG_GEMINI_BACKEND"); ok {
		cfg.Gemini.Backend = strings.ToLower(strings.TrimSpace(v))
	}
	// The Vertex names match the ones gcloud users already have in .env;
	// like the other keys, an empty value does not clear a file value.
	for name, dst := range map[string]*string{"VERTEX_API_KEY": &cfg.Gemini.Vertex.APIKey, "VERTEX_PROJECT": &cfg.Gemini.Vertex.Project, "VERTEX_LOCATION": &cfg.Gemini.Vertex.Location} {
		if v, ok := lookup(name); ok && strings.TrimSpace(v) != "" {
			*dst = strings.TrimSpace(v)
		}
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

// applyAnthropicEnv overrides the [anthropic] keys: the key from
// ANTHROPIC_API_KEY (no SHEEPDOG_ prefix, like DEEPSEEK_API_KEY), the
// rest from SHEEPDOG_ANTHROPIC_*. ANTHROPIC_BASE_URL and
// ANTHROPIC_AUTH_TOKEN are not read: Claude Code sets them for itself,
// and the daemon must not follow them.
func applyAnthropicEnv(cfg *Config, lookup func(string) (string, bool)) error {
	if v, ok := lookup("ANTHROPIC_API_KEY"); ok && v != "" {
		cfg.Anthropic.APIKey = v
	}
	if v, ok := lookup("SHEEPDOG_ANTHROPIC_BASE_URL"); ok {
		cfg.Anthropic.BaseURL = v
	}
	if v, ok := lookup("SHEEPDOG_ANTHROPIC_EFFORT"); ok {
		cfg.Anthropic.Effort = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := lookup("SHEEPDOG_ANTHROPIC_EVENT_EFFORT"); ok {
		cfg.Anthropic.EventEffort = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := lookup("SHEEPDOG_ANTHROPIC_FALLBACKS"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("config: SHEEPDOG_ANTHROPIC_FALLBACKS=%q: want true or false", v)
		}
		cfg.Anthropic.Fallbacks = b
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
	applyPushEnv(cfg, lookup)
	return nil
}

// applyPushEnv overrides [push] from SHEEPDOG_PUSH_* and [push.apns]
// from SHEEPDOG_APNS_*.
func applyPushEnv(cfg *Config, lookup func(string) (string, bool)) {
	for _, e := range []struct {
		name string
		dst  *string
	}{
		{"SHEEPDOG_PUSH_MODE", &cfg.Push.Mode},
		{"SHEEPDOG_PUSH_RELAY_URL", &cfg.Push.Relay.URL},
		{"SHEEPDOG_APNS_KEY_PATH", &cfg.Push.APNs.KeyPath},
		{"SHEEPDOG_APNS_KEY_ID", &cfg.Push.APNs.KeyID},
		{"SHEEPDOG_APNS_TEAM_ID", &cfg.Push.APNs.TeamID},
		{"SHEEPDOG_APNS_TOPIC", &cfg.Push.APNs.Topic},
	} {
		if v, ok := lookup(e.name); ok {
			*e.dst = v
		}
	}
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
