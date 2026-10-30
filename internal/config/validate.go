package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// validate checks the resolved config's values.
func validate(cfg Config) error {
	if !validChattiness(cfg.Supervisor.Chattiness) {
		return fmt.Errorf("config: supervisor.chattiness=%q: want one of %s", cfg.Supervisor.Chattiness, strings.Join(ChattinessLevels, ", "))
	}
	if cfg.Supervisor.RecentMinutes <= 0 {
		return fmt.Errorf("config: supervisor.recent_minutes=%d: want a positive integer", cfg.Supervisor.RecentMinutes)
	}
	if cfg.Supervisor.PromiseTTLS <= 0 {
		return fmt.Errorf("config: supervisor.promise_ttl_s=%d: want a positive integer", cfg.Supervisor.PromiseTTLS)
	}
	if cfg.Supervisor.RepromptPollS <= 0 {
		return fmt.Errorf("config: supervisor.reprompt_poll_s=%d: want a positive integer", cfg.Supervisor.RepromptPollS)
	}
	if cfg.Supervisor.RepeatWindowS <= 0 {
		return fmt.Errorf("config: supervisor.repeat_window_s=%d: want a positive integer", cfg.Supervisor.RepeatWindowS)
	}
	if cfg.Supervisor.AuthoriseWindowS <= 0 {
		return fmt.Errorf("config: supervisor.authorise_window_s=%d: want a positive integer", cfg.Supervisor.AuthoriseWindowS)
	}
	if err := validateBrain(cfg.Brain); err != nil {
		return err
	}
	if err := validateDeepSeek(cfg.DeepSeek); err != nil {
		return err
	}
	if err := validateAnthropic(cfg.Anthropic); err != nil {
		return err
	}
	if err := validateVoice(cfg); err != nil {
		return err
	}
	if cfg.Push.Mode != "" && !oneOf(cfg.Push.Mode, PushModes) {
		return fmt.Errorf("config: push.mode=%q: want one of %s", cfg.Push.Mode, strings.Join(PushModes, ", "))
	}
	if cfg.Push.EffectiveMode() == PushModeRelay && !strings.HasPrefix(cfg.Push.Relay.URL, "https://") && !strings.HasPrefix(cfg.Push.Relay.URL, "http://") {
		return fmt.Errorf("config: push.relay.url=%q: want an http(s) URL", cfg.Push.Relay.URL)
	}
	if !oneOf(cfg.Herdr.Backend, HerdrBackends) {
		return fmt.Errorf("config: herdr.backend=%q: want one of %s", cfg.Herdr.Backend, strings.Join(HerdrBackends, ", "))
	}
	if !oneOf(cfg.Log.Level, LogLevels) {
		return fmt.Errorf("config: log.level=%q: want one of %s", cfg.Log.Level, strings.Join(LogLevels, ", "))
	}
	if cfg.Log.MaxSizeMB <= 0 {
		return fmt.Errorf("config: log.max_size_mb=%d: want a positive number of megabytes", cfg.Log.MaxSizeMB)
	}
	if cfg.Log.MaxFiles < 1 {
		return fmt.Errorf("config: log.max_files=%d: want 1 or more", cfg.Log.MaxFiles)
	}
	if cfg.Events.RetentionDays < 0 {
		return fmt.Errorf("config: events.retention_days=%d: want 0 (keep forever) or a number of days", cfg.Events.RetentionDays)
	}
	return nil
}

// validateVoice checks [voice], [voice.openai] and the [gemini]
// sensitivities and backend.
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
	if !oneOf(cfg.Gemini.BriefingChannel, GeminiBriefingChannels) {
		return fmt.Errorf("config: gemini.briefing_channel=%q: want one of %s", cfg.Gemini.BriefingChannel, strings.Join(GeminiBriefingChannels, ", "))
	}
	if !oneOf(cfg.Gemini.Backend, GeminiBackends) {
		return fmt.Errorf("config: gemini.backend=%q: want one of %s", cfg.Gemini.Backend, strings.Join(GeminiBackends, ", "))
	}
	if cfg.Gemini.Backend == "vertex" && strings.TrimSpace(cfg.Gemini.Vertex.Location) == "" {
		return errors.New("config: gemini.vertex.location must not be empty")
	}
	return nil
}

// validateAnthropic checks the [anthropic] values.
func validateAnthropic(a Anthropic) error {
	if strings.TrimSpace(a.BaseURL) == "" {
		return errors.New("config: anthropic.base_url must not be empty")
	}
	if !oneOf(a.Effort, AnthropicEfforts) {
		return fmt.Errorf("config: anthropic.effort=%q: want one of %s", a.Effort, strings.Join(AnthropicEfforts, ", "))
	}
	if !oneOf(a.EventEffort, AnthropicEfforts) {
		return fmt.Errorf("config: anthropic.event_effort=%q: want one of %s", a.EventEffort, strings.Join(AnthropicEfforts, ", "))
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
	if err := validateCompat(b.OpenAICompat); err != nil {
		return err
	}
	if _, ok := b.CompatProvider(b.Provider); !ok && !oneOf(b.Provider, BrainProviders) {
		names := append([]string(nil), BrainProviders...)
		for _, o := range b.OpenAICompat {
			names = append(names, o.Name)
		}
		return fmt.Errorf("config: brain.provider=%q: want one of %s", b.Provider, strings.Join(names, ", "))
	}
	if b.StepReserveS < 0 {
		return fmt.Errorf("config: brain.step_reserve_s=%d: want 0 (default) or a positive integer", b.StepReserveS)
	}
	return nil
}

// validateCompat checks the [[brain.openai_compat]] entries.
func validateCompat(list []OpenAICompat) error {
	seen := map[string]bool{}
	for i, o := range list {
		switch {
		case !compatNameRE.MatchString(o.Name):
			return fmt.Errorf("config: brain.openai_compat[%d].name=%q: want lowercase letters, digits, '-' or '_'", i, o.Name)
		case oneOf(o.Name, BrainProviders) || strings.HasPrefix(o.Name, "fake"):
			return fmt.Errorf("config: brain.openai_compat[%d].name=%q: reserved; pick another name", i, o.Name)
		case seen[o.Name]:
			return fmt.Errorf("config: brain.openai_compat[%d].name=%q: duplicate", i, o.Name)
		}
		seen[o.Name] = true
		u, err := url.Parse(strings.TrimSpace(o.BaseURL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("config: brain.openai_compat %q: base_url=%q: want an http(s) URL", o.Name, o.BaseURL)
		}
		if strings.TrimSpace(o.Model) == "" {
			return fmt.Errorf("config: brain.openai_compat %q: model must not be empty", o.Name)
		}
	}
	return nil
}
