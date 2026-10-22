package app

import (
	"fmt"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// buildBrain builds the orchestrator with the [brain] config
// (BrainConfig) and a transcript under the data dir, over every provider
// that has a key: gemini (the key pool), deepseek ([deepseek]),
// anthropic ([anthropic]),
// vertex ([gemini.vertex]) and each [[brain.openai_compat]] entry,
// starting on brain.provider. The caller has checked that the initial
// provider has a key.
func buildBrain(cfg config.Config, pl *prompts.Loader, log logging.Logger) (*brain.Brain, error) {
	models, err := BrainModels(cfg, log)
	if err != nil {
		return nil, fmt.Errorf("app: brain: %w", err)
	}
	bcfg := BrainConfig(cfg)
	bcfg.Prompts = pl
	initial, ok := models[cfg.Brain.Provider]
	if !ok {
		return nil, fmt.Errorf("app: brain: provider %q has no key", cfg.Brain.Provider)
	}
	b, err := brain.New(bcfg, initial, log)
	if err != nil {
		return nil, fmt.Errorf("app: brain: %w", err)
	}
	for id, m := range models {
		if id == cfg.Brain.Provider {
			continue
		}
		if err := b.AddProvider(id, m); err != nil {
			return nil, fmt.Errorf("app: brain: %w", err)
		}
	}
	return b, nil
}

// BrainModels builds the orchestrator's Model of every provider the config
// has a key for (shared with `sheepdog brain-test`): gemini over the
// key pool, deepseek over the OpenAI-compatible adapter, anthropic over
// the native Messages API adapter, vertex over
// the Gemini adapter on Vertex AI (VertexBrainMissing), and each
// [[brain.openai_compat]] entry over the same adapter without DeepSeek's
// extensions (a loopback one even without a key). An empty map
// when there is no key at all.
func BrainModels(cfg config.Config, log logging.Logger) (map[string]brain.Model, error) {
	models := map[string]brain.Model{}
	if len(cfg.Gemini.APIKeys) > 0 {
		m, err := brain.NewGenaiModel(cfg.Gemini.APIKeys, log)
		if err != nil {
			return nil, err
		}
		models[brain.ProviderGemini] = m
	}
	if cfg.DeepSeek.APIKey != "" {
		thinking := cfg.DeepSeek.Thinking
		m, err := brain.NewOpenAICompatModel(brain.OpenAICompatConfig{Provider: brain.ProviderDeepSeek, BaseURL: cfg.DeepSeek.BaseURL, APIKey: cfg.DeepSeek.APIKey, Thinking: &thinking, ReasoningEffort: cfg.DeepSeek.ReasoningEffort, Log: log})
		if err != nil {
			return nil, err
		}
		models[brain.ProviderDeepSeek] = m
	}
	if cfg.Anthropic.APIKey != "" {
		m, err := brain.NewAnthropicModel(AnthropicConfig(cfg, log))
		if err != nil {
			return nil, err
		}
		models[brain.ProviderAnthropic] = m
	}
	if VertexBrainMissing(cfg.Gemini.Vertex) == "" {
		m, err := brain.NewVertexModel(cfg.Gemini.Vertex.APIKey, cfg.Gemini.Vertex.Project, log)
		if err != nil {
			return nil, err
		}
		models[brain.ProviderVertex] = m
	}
	for _, o := range cfg.Brain.OpenAICompat {
		if !o.Usable() {
			continue
		}
		m, err := brain.NewOpenAICompatModel(brain.OpenAICompatConfig{Provider: o.Name, BaseURL: o.BaseURL, APIKey: o.APIKey, NoKey: o.KeyOptional(), DefaultModel: o.Model, Log: log})
		if err != nil {
			return nil, err
		}
		models[o.Name] = m
	}
	return models, nil
}

// AnthropicConfig maps the [anthropic] config onto the adapter's.
func AnthropicConfig(cfg config.Config, log logging.Logger) brain.AnthropicConfig {
	return brain.AnthropicConfig{
		APIKey: cfg.Anthropic.APIKey, BaseURL: cfg.Anthropic.BaseURL,
		Effort: cfg.Anthropic.Effort, EventEffort: cfg.Anthropic.EventEffort,
		NoFallbacks: !cfg.Anthropic.Fallbacks, PruneAfterTurns: cfg.Brain.PruneAfterTurns, Log: log,
	}
}

// compatMissing names the key variable the [[brain.openai_compat]]
// provider named provider lacks ("" when it has one, needs none, or
// provider is not such an entry).
func compatMissing(cfg config.Config, provider string) string {
	o, ok := cfg.Brain.CompatProvider(provider)
	if !ok || o.Usable() {
		return ""
	}
	return o.APIKeyEnv
}

// VertexBrainMissing names the variable the vertex orchestrator provider lacks
// ("" when it has both). It reuses the voice's [gemini.vertex] key and
// project but not gemini.backend (the orchestrator may run on Vertex while the
// voice stays on AI Studio, or the other way round) nor gemini.vertex.
// location (the orchestrator is always at global: brain.VertexLocation).
func VertexBrainMissing(v config.GeminiVertex) string {
	switch {
	case v.APIKey == "":
		return "VERTEX_API_KEY"
	case v.Project == "":
		return "VERTEX_PROJECT"
	}
	return ""
}

// BrainProviders lists every provider for the /debug picker, with why
// the ones without a key are unavailable.
func BrainProviders(cfg config.Config) []web.ProviderInfo {
	gemini := web.ProviderInfo{ID: brain.ProviderGemini, Label: web.ProviderLabel(brain.ProviderGemini), Available: len(cfg.Gemini.APIKeys) > 0}
	if !gemini.Available {
		gemini.Reason = "no GEMINI_API_KEY"
	}
	deepseek := web.ProviderInfo{ID: brain.ProviderDeepSeek, Label: web.ProviderLabel(brain.ProviderDeepSeek), Available: cfg.DeepSeek.APIKey != ""}
	if !deepseek.Available {
		deepseek.Reason = "no DEEPSEEK_API_KEY"
	}
	anthropic := web.ProviderInfo{ID: brain.ProviderAnthropic, Label: web.ProviderLabel(brain.ProviderAnthropic), Available: cfg.Anthropic.APIKey != ""}
	if !anthropic.Available {
		anthropic.Reason = "no ANTHROPIC_API_KEY"
	}
	vertex := web.ProviderInfo{ID: brain.ProviderVertex, Label: web.ProviderLabel(brain.ProviderVertex), Available: VertexBrainMissing(cfg.Gemini.Vertex) == ""}
	if !vertex.Available {
		vertex.Reason = "no " + VertexBrainMissing(cfg.Gemini.Vertex)
	}
	out := []web.ProviderInfo{gemini, deepseek, anthropic, vertex}
	for _, o := range cfg.Brain.OpenAICompat {
		label := o.Label
		if label == "" {
			label = web.ProviderLabel(o.Name)
		}
		p := web.ProviderInfo{ID: o.Name, Label: label, Available: o.Usable()}
		if !p.Available {
			p.Reason = "no " + o.APIKeyEnv
		}
		out = append(out, p)
	}
	return out
}

// BrainConfig maps the [brain] config onto brain.Config (shared with
// `sheepdog brain-test`).
func BrainConfig(cfg config.Config) brain.Config {
	return brain.Config{
		Provider:            cfg.Brain.Provider,
		Model:               cfg.Brain.Model,
		MaxSteps:            cfg.Brain.MaxSteps,
		UtteranceTimeout:    time.Duration(cfg.Brain.UtteranceTimeoutS) * time.Second,
		WindowTokens:        cfg.Brain.WindowTokens,
		PruneAfterTurns:     cfg.Brain.PruneAfterTurns,
		StepReserve:         time.Duration(cfg.Brain.StepReserveS) * time.Second,
		FinalGrace:          time.Duration(cfg.Brain.FinalGraceS) * time.Second,
		AnnounceTurnTimeout: time.Duration(cfg.Brain.AnnounceTurnTimeoutS) * time.Second,
		AnnounceTimeout:     time.Duration(cfg.Brain.AnnounceTimeoutS) * time.Second,
		DataDir:             cfg.DataDir,
		KeyVars:             compatKeyVars(cfg),
	}
}

// compatKeyVars maps each [[brain.openai_compat]] entry to the variable
// its key is read from, for the orchestrator's account-problem fix lines.
func compatKeyVars(cfg config.Config) map[string]string {
	m := map[string]string{}
	for _, o := range cfg.Brain.OpenAICompat {
		if o.APIKeyEnv != "" {
			m[o.Name] = o.APIKeyEnv
		}
	}
	return m
}

// BrainMode reports the effective orchestrator mode: BrainOn, BrainFake, or
// BrainOff (with a.brainOff saying why).
func (a *App) BrainMode() string {
	switch {
	case a.brain == nil:
		return BrainOff
	case a.opts.Brain == BrainFake:
		return BrainFake
	default:
		return BrainOn
	}
}

// BrainModel reports the orchestrator's current model ("" when it is off): the
// configured brain.model until a runtime switch. BrainProvider likewise
// for the provider.
func (a *App) BrainModel() string {
	if a.brain == nil {
		return ""
	}
	return a.brain.Model()
}

// BrainProvider reports the orchestrator's current provider ("" when it is
// off).
func (a *App) BrainProvider() string {
	if a.brain == nil {
		return ""
	}
	return a.brain.Provider()
}

// restoreBrainModel applies the provider and model chosen at runtime in
// an earlier run (the state file's session.brain_provider /
// brain_model, kept by PUT /api/brain/model) over the configured
// initial values; a provider that is not registered in this run (its
// key is gone) is logged as daemon.brain_model_error and the configured
// pair kept. A model without a provider was kept by a run from before
// providers existed, when Gemini was the only one: it is Gemini's, never
// the configured provider's. Called from Run right after daemon.start,
// before any turn: the brain.model_changed {via: state} it logs belongs
// to this run, not before it.
func (a *App) restoreBrainModel() {
	if a.brain == nil {
		return
	}
	ss := a.store.Snapshot().Session
	if ss.BrainModel == "" {
		return
	}
	provider := ss.BrainProvider
	if provider == "" {
		provider = brain.ProviderGemini
	}
	if err := a.brain.SetModel(provider, ss.BrainModel, "state"); err != nil {
		a.log.Log("daemon", "daemon.brain_model_error", "", map[string]any{"provider": provider, "model": ss.BrainModel, "error": err.Error()})
	}
}

// ReloadPrompts rescans the prompt override directory (SIGHUP, POST
// /api/prompts/reload). The orchestrator and the voice session share this
// Loader, so both pick up the change: the orchestrator on its next turn, the
// voice session on its next (re)connect. Callers log prompts.reloaded
// themselves (the "via" differs: sighup vs api).
func (a *App) ReloadPrompts() []prompts.File { return a.prompts.Reload() }
