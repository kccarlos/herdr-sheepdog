package onboard

import (
	"context"
	"fmt"
	"net/http"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// DoctorOptions configures Doctor.
type DoctorOptions struct {
	// Endpoints are the key checks' API roots (DefaultEndpoints).
	Endpoints Endpoints
	// Probe runs one tiny tool-calling turn on each OpenAI-compatible
	// orchestrator provider (a few tokens each).
	Probe bool
	// ConfigPath and EnvPath are the files checked for permissions ("":
	// not checked).
	ConfigPath, EnvPath string
	// PortClient is the client of the port check's /api/hello call.
	PortClient *http.Client
}

// Doctor runs every check on the effective config, in a fixed order.
func Doctor(ctx context.Context, cfg config.Config, o DoctorOptions) []Result {
	var out []Result
	out = append(out, CheckHerdr(cfg.Herdr.Socket))
	out = append(out, checkGeminiKeys(ctx, o.Endpoints, cfg)...)
	if cfg.Gemini.Vertex.APIKey != "" || cfg.Gemini.Vertex.Project != "" {
		r := Result{Name: "vertex", Status: OK, Detail: "VERTEX_API_KEY and VERTEX_PROJECT set (not checked live)"}
		if cfg.Gemini.Vertex.APIKey == "" || cfg.Gemini.Vertex.Project == "" {
			r = Result{Name: "vertex", Status: Fail, Detail: "only one of VERTEX_API_KEY and VERTEX_PROJECT is set", Fix: "set both, or neither"}
		}
		out = append(out, r)
	}
	if cfg.DeepSeek.APIKey != "" {
		model := brain.DefaultDeepSeekModel
		if cfg.Brain.Provider == brain.ProviderDeepSeek {
			model = cfg.Brain.Model
		}
		off := false
		out = append(out, CheckCompat(ctx, o.Endpoints, Compat{Name: brain.ProviderDeepSeek, BaseURL: cfg.DeepSeek.BaseURL, APIKey: cfg.DeepSeek.APIKey, KeyEnv: "DEEPSEEK_API_KEY", Model: model, Thinking: &off}, o.Probe)...)
	}
	for _, c := range cfg.Brain.OpenAICompat {
		if !c.Usable() {
			st := Skip
			if cfg.Brain.Provider == c.Name {
				st = Fail
			}
			out = append(out, Result{Name: c.Name + " key", Status: st, Detail: c.APIKeyEnv + " is not set", Fix: "put the key in " + c.APIKeyEnv + " (sheepdog setup writes it)"})
			continue
		}
		out = append(out, CheckCompat(ctx, o.Endpoints, Compat{Name: c.Name, BaseURL: c.BaseURL, APIKey: c.APIKey, KeyEnv: c.APIKeyEnv, NoKey: c.KeyOptional(), Model: c.Model}, o.Probe)...)
	}
	out = append(out, checkOrchestrator(cfg))
	out = append(out, CheckOpenAIVoiceKey(ctx, o.Endpoints, cfg.Voice.OpenAI.APIKey))
	out = append(out, CheckVoice(cfg))
	out = append(out, CheckPort(ctx, o.PortClient, cfg.Web.Bind, cfg.Web.Port))
	out = append(out, CheckBindToken(cfg.Web))
	out = append(out, CheckPush(cfg.Push.APNs))
	if o.EnvPath != "" {
		out = append(out, CheckSecretFile("env file", o.EnvPath))
	}
	if o.ConfigPath != "" && cfg.Web.Token != "" {
		out = append(out, CheckSecretFile("config file", o.ConfigPath))
	}
	return out
}

// checkGeminiKeys checks every key of the Gemini pool; with no key it
// fails only when something needs one.
func checkGeminiKeys(ctx context.Context, ep Endpoints, cfg config.Config) []Result {
	keys := cfg.Gemini.APIKeys
	if len(keys) == 0 {
		needed := cfg.Brain.Provider == brain.ProviderGemini || (cfg.Voice.Provider == "gemini" && cfg.Gemini.Backend != "vertex")
		if !needed {
			return []Result{{Name: "gemini key", Status: Skip, Detail: "GEMINI_API_KEY not set (not needed by this config)"}}
		}
		return []Result{CheckGeminiKey(ctx, ep, "")}
	}
	var out []Result
	for i, k := range keys {
		r := CheckGeminiKey(ctx, ep, k)
		if len(keys) > 1 {
			r.Name = fmt.Sprintf("gemini key %d/%d", i+1, len(keys))
		}
		out = append(out, r)
	}
	return out
}

// checkOrchestrator reports whether brain.provider has what it needs (the
// daemon turns the orchestrator off silently otherwise).
func checkOrchestrator(cfg config.Config) Result {
	const name = "orchestrator"
	if !cfg.Brain.Enabled {
		return Result{Name: name, Status: Warn, Detail: "brain.enabled = false: the daemon answers with its own sentences only", Fix: "set brain.enabled = true"}
	}
	p := cfg.Brain.Provider
	missing := ""
	switch p {
	case brain.ProviderGemini:
		if len(cfg.Gemini.APIKeys) == 0 {
			missing = "GEMINI_API_KEY"
		}
	case brain.ProviderDeepSeek:
		if cfg.DeepSeek.APIKey == "" {
			missing = "DEEPSEEK_API_KEY"
		}
	case brain.ProviderVertex:
		switch {
		case cfg.Gemini.Vertex.APIKey == "":
			missing = "VERTEX_API_KEY"
		case cfg.Gemini.Vertex.Project == "":
			missing = "VERTEX_PROJECT"
		}
	default:
		if c, ok := cfg.Brain.CompatProvider(p); ok && !c.Usable() {
			missing = c.APIKeyEnv
		}
	}
	if missing != "" {
		return Result{Name: name, Status: Fail, Detail: fmt.Sprintf("brain.provider %s has no %s: the orchestrator would be off", p, missing), Fix: "set " + missing + " (sheepdog setup), or pick another brain.provider"}
	}
	return Result{Name: name, Status: OK, Detail: fmt.Sprintf("starts on %s/%s", p, cfg.Brain.Model)}
}

// Failed reports whether any result is a failure.
func Failed(rs []Result) bool {
	for _, r := range rs {
		if r.Status == Fail {
			return true
		}
	}
	return false
}
