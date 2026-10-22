package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gptlive"
	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// VoiceIssue says why the voice is not connected ("" when nothing is
// wrong).
func (a *App) VoiceIssue() string {
	a.issueMu.Lock()
	defer a.issueMu.Unlock()
	return a.voiceIssue
}

func (a *App) setVoiceIssue(s string) {
	a.issueMu.Lock()
	a.voiceIssue = s
	a.issueMu.Unlock()
}

// initialVoiceProvider is the provider the voice starts on before the
// state file is consulted: the --voice flag when it names one, else
// voice.provider.
func initialVoiceProvider(cfg config.Config, opts Options) string {
	if opts.Voice == VoiceGemini || opts.Voice == VoiceOpenAI {
		return opts.Voice
	}
	return cfg.Voice.Provider
}

// voiceProviders lists both voice providers for the switcher, the one
// without a key unavailable with the reason.
func (a *App) voiceProviders(gcfg gemini.Config, handler voice.ToolHandler) []voice.Provider {
	cfg := a.cfg
	_, _, missing := GeminiLive(cfg.Gemini)
	g := voice.Provider{ProviderInfo: voice.ProviderInfo{ID: VoiceGemini, Label: geminiLabel(gcfg.Vertex), Model: gcfg.Model, Available: missing == ""}}
	if gcfg.Model == "" {
		g.Model = gemini.DefaultModel
	}
	if g.Available {
		g.New = func() (voice.Session, error) { return gemini.New(gcfg, a.log) }
	} else {
		g.Reason = "no " + missing
	}
	oc := cfg.Voice.OpenAI
	o := voice.Provider{ProviderInfo: voice.ProviderInfo{ID: VoiceOpenAI, Label: "GPT-Live", Model: oc.Model, Available: oc.APIKey != "", Billing: voice.BillingPerMinute}}
	if o.Available {
		router := &inject.Router{}
		name, aliases, names, pl := cfg.Voice.Name, gcfg.Aliases, gcfg.Names, a.prompts
		language := gcfg.Language
		if language == nil {
			language = func() string { return "" }
		}
		ocfg := gptlive.Config{
			APIKey: oc.APIKey, Model: oc.Model, Voice: oc.Voice,
			IdleClose: time.Duration(oc.IdleCloseS) * time.Second,
			Handler:   handler,
			Classify:  router.Route,
			Context:   a.voiceCtx,
			// The spoken names are key terms of the heard check.
			Names: names,
			Instructions: func() (string, error) {
				list := aliases
				if names != nil {
					if n := names(); len(n) > 0 {
						list = n
					}
				}
				text, _ := pl.Render(prompts.AssistantGPTLive, prompts.AssistantVars(name, list))
				return prompts.WithLanguage(text, language()), nil
			},
		}
		o.New = func() (voice.Session, error) { return gptlive.New(ocfg, a.log) }
	} else {
		o.Reason = "no OPENAI_API_KEY"
	}
	return []voice.Provider{g, o}
}

// restoreVoiceProvider applies the provider picked at runtime in an
// earlier run (the state file's session.voice_provider, kept by PUT
// /api/voice/provider) over the configured one, unless --voice named a
// provider for this run. One that is unavailable now (its key is gone)
// is logged as daemon.voice_provider_error and the configured one kept.
// Called from Run before the voice starts.
func (a *App) restoreVoiceProvider() {
	if a.voiceSwitch == nil || a.opts.Voice != VoiceOn {
		return
	}
	id := a.store.Snapshot().Session.VoiceProvider
	if id == "" {
		return
	}
	if err := a.voiceSwitch.SetProvider(id, "state"); err != nil {
		a.log.Log("daemon", "daemon.voice_provider_error", "", map[string]any{"provider": id, "error": err.Error()})
	}
}

// RecycleVoice forces the voice session to reconnect with its resumption
// handle (a Gemini resume), reporting whether the provider supports it.
// For the bench and debugging; the daemon never calls it on its own.
func (a *App) RecycleVoice(reason string) bool {
	if a.voiceSwitch == nil {
		return false
	}
	return a.voiceSwitch.Recycle(reason)
}

// VoiceProvider reports the voice provider ("off" without a voice).
func (a *App) VoiceProvider() string {
	if a.voiceSwitch == nil {
		return VoiceOff
	}
	return a.voiceSwitch.Provider()
}

// Voice start backoff: the first retry after voiceRetryMin, doubling up
// to voiceRetryMax. Variables so tests can shorten them.
var (
	voiceRetryMin = 5 * time.Second
	voiceRetryMax = 5 * time.Minute
)

// startVoice starts the voice session, retrying a failed connect with a
// backoff until it succeeds or ctx ends. With no credentials for the
// provider it does not retry: the daemon is in setup mode until
// `sheepdog setup` saves a key (and restarts it). Every failure is
// logged (daemon.voice_error) and reported to the pages as a closed
// status with the reason, and an account problem (a rejected key, a
// used-up quota) as the voice's account alert.
func (a *App) startVoice(ctx context.Context) {
	wait := voiceRetryMin
	for attempt := 1; ; attempt++ {
		err := a.voice.Start(ctx)
		if err == nil {
			a.setVoiceIssue("")
			if attempt > 1 {
				a.log.Log("daemon", "daemon.voice_recovered", "", map[string]any{"attempts": attempt})
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		provider := a.VoiceProvider()
		if errors.Is(err, voice.ErrUnavailableProvider) {
			issue := SetupIssue(provider)
			a.setVoiceIssue(issue)
			a.log.Log("daemon", "daemon.setup_mode", "", map[string]any{"voice_provider": provider, "reason": issue})
			a.voiceSwitch.Report(voice.EventStatus{Status: voice.StatusClosed, Reason: "setup_needed"})
			return
		}
		issue := voiceIssueText(provider, err, wait)
		a.setVoiceIssue(issue)
		a.log.Log("daemon", "daemon.voice_error", "", map[string]any{"error": err.Error(), "attempt": attempt, "retry_in_s": int(wait.Seconds())})
		a.voiceSwitch.Report(voice.EventError{Err: err})
		a.voiceSwitch.Report(voice.EventStatus{Status: voice.StatusClosed, Reason: issue})
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait *= 2
		if wait > voiceRetryMax {
			wait = voiceRetryMax
		}
	}
}

// SetupIssue is the voice issue of setup mode: the provider has no key.
func SetupIssue(provider string) string {
	if provider == VoiceOpenAI {
		return "Setup needed: the GPT-Live voice has no OpenAI key yet. Run `sheepdog setup`."
	}
	return "Setup needed: no Gemini API key yet. Run `sheepdog setup`."
}

// voiceIssueText words a failed voice connect for a screen: the account
// problem when it is one, else the error, and when the next try is.
func voiceIssueText(provider string, err error, wait time.Duration) string {
	retry := fmt.Sprintf(" Retrying in %s.", wait.Round(time.Second))
	svc := provider
	if svc == VoiceOpenAI {
		svc = "openai"
	}
	if p := brain.AccountProblemOfErr(err); p != brain.ProblemNone {
		al := brain.NewAccountAlert(brain.RoleVoice, svc, p, false)
		return al.Message + " Fix: " + al.Fix + "." + retry
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return "The voice cannot connect (" + msg + ")." + retry
}
