package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// `sheepdog init`'s orchestrator step (init_flow.go).

// orchestratorStep is step 3: the preset, its key, and its model. It
// returns the preset id as ResolveModels takes it.
func (f *initFlow) orchestratorStep(card onboard.StartCard, voice string) (string, error) {
	id := card.Orch
	if id == "" {
		flag := f.o.orchestrator
		if flag == "compat" {
			flag = onboard.PresetOther
		}
		ids := make([]string, len(f.presets))
		labels := make([]string, len(f.presets))
		for i, p := range f.presets {
			ids[i], labels[i] = p.ID, p.Label+": "+p.Note
		}
		def := f.savedOrch()
		if onboard.VoiceSetupNeeded(f.cur, f.existing) && f.hasPreset(brain.ProviderDeepSeek) {
			def = brain.ProviderDeepSeek
		}
		if f.in.nonInteractive {
			def = brain.ProviderGemini
		}
		var err error
		if id, err = f.in.chooseDef("Step 3 of 3. Orchestrator (the model that reads and acts on your agents)", ids, labels, def, flag); err != nil {
			return "", err
		}
	}
	p, ok := onboard.PresetByID(f.presets, id)
	if !ok {
		return "", fmt.Errorf("unknown orchestrator %q", id)
	}
	f.preset = p
	f.input.Orchestrator = id
	if id == onboard.PresetOther {
		f.input.Orchestrator = "compat"
	}
	var err error
	switch p.ID {
	case brain.ProviderGemini:
		err = f.geminiOrch()
	case brain.ProviderDeepSeek:
		_, err = f.key(initKey{env: "DEEPSEEK_API_KEY", label: "DeepSeek API key (" + p.DocsURL + ")",
			check: func(ctx context.Context, k string) onboard.Result {
				return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "deepseek", Key: k}))
			}})
	case brain.ProviderAnthropic:
		err = f.anthropicOrch(p)
	case brain.ProviderVertex:
		err = f.vertexOrch(voice)
	default:
		err = f.compatOrch(p)
	}
	return id, err
}

func (f *initFlow) geminiOrch() error {
	key, err := f.key(f.geminiKey())
	if err != nil {
		return err
	}
	model := f.o.model
	if !f.in.nonInteractive && model == "" {
		var ids []string
		if !f.o.skipValidate {
			var r onboard.Result
			ids, r = onboard.ListGeminiModels(f.ctx, f.ep, key)
			ids = f.listNote(ids, r, "models")
		}
		if model, err = f.in.pickName("Orchestrator model", onboard.WithFirst(ids, brain.DefaultModel), brain.DefaultModel, nil); err != nil {
			return err
		}
	}
	if model != brain.DefaultModel {
		f.input.GeminiModel = model
	}
	return nil
}

func (f *initFlow) vertexOrch(voice string) error {
	if err := f.vertex(voice == onboard.VoiceVertex); err != nil {
		return err
	}
	var names []string
	for _, m := range brain.VertexModels {
		names = append(names, m.Name)
	}
	model := f.o.model
	if !f.in.nonInteractive && model == "" {
		var err error
		if model, err = f.in.pickName("Orchestrator model", onboard.WithFirst(names, brain.DefaultModel), brain.DefaultModel, nil); err != nil {
			return err
		}
	}
	f.input.Vertex.Model = model
	return nil
}

func (f *initFlow) anthropicOrch(p onboard.Preset) error {
	key, err := f.key(initKey{env: p.KeyEnv, label: "Anthropic API key (" + p.DocsURL + ")",
		check: func(ctx context.Context, k string) onboard.Result {
			return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "anthropic", Key: k}))
		}})
	if err != nil {
		return err
	}
	model := f.o.model
	if !f.in.nonInteractive && model == "" {
		var entries []onboard.ModelEntry
		if !f.o.skipValidate {
			var r onboard.Result
			entries, r = onboard.ListAnthropicModels(f.ctx, f.ep, key)
			if r.Status != onboard.OK {
				fprintf(f.out, "  could not list the models (%s), so only the default is offered\n", r.Detail)
				entries = nil
			}
		}
		if model, err = f.pickOrchModel(entries, brain.DefaultAnthropicModel, false); err != nil {
			return err
		}
	}
	if model != brain.DefaultAnthropicModel {
		f.input.AnthropicModel = model
	}
	shown := onboard.OrDefault(model, brain.DefaultAnthropicModel)
	if f.wantToolTest() && !f.o.skipValidate {
		r := onboard.ProbeAnthropic(f.ctx, f.ep, key, shown)
		fprintln(f.out, "  "+r.Line())
	}
	return nil
}

// wantToolTest: "test tool calling? [y/N]" (a few tokens, which a paid
// endpoint may bill); non-interactive runs it with --test-tools.
func (f *initFlow) wantToolTest() bool {
	if f.in.nonInteractive {
		return f.o.testTools
	}
	if f.o.skipValidate {
		return false
	}
	yes, err := f.in.yesNo("Test tool calling on this model now? (a few tokens; a paid endpoint may bill them)", false)
	return err == nil && yes
}

// compatOrch handles OpenRouter, OpenAI, Ollama, LM Studio and Other URL.
func (f *initFlow) compatOrch(p onboard.Preset) error {
	name, base := p.ID, p.BaseURL
	if p.Kind == onboard.PresetOther {
		var err error
		if name, err = f.in.ask("Provider name", f.o.compat.Name, "openrouter"); err != nil {
			return err
		}
		if base, err = f.in.ask("Base URL (the API root; /chat/completions is appended)", f.o.compat.BaseURL, "https://openrouter.ai/api/v1"); err != nil {
			return err
		}
		name = onboard.CompatName(name)
	}
	f.input.Compat.Name, f.input.Compat.BaseURL = name, base

	// The key: a keyed preset always needs one; a typed loopback URL may
	// run without.
	keyed := p.KeyNeeded || (p.Kind == onboard.PresetOther && !onboard.KeyOptionalURL(base))
	reuse := false
	var key string
	if keyed || p.Kind == onboard.PresetOther {
		reuse = p.ReuseEnv != "" && (f.o.reuseOpenAIKey || f.askReuse(p))
		f.input.Compat.ReuseKey = reuse
		env := onboard.CompatKeyEnv(f.cur, name, reuse)
		if p.Kind == onboard.PresetOther && f.o.compat.APIKeyEnv != "" {
			env = f.o.compat.APIKeyEnv
		}
		if reuse {
			key = f.have[p.ReuseEnv]
			if key == "" {
				key = f.existing[p.ReuseEnv]
			}
			if key == "" {
				return errors.New("no OpenAI voice key is saved to reuse: use an OpenAI voice, or give this orchestrator a key of its own")
			}
		} else {
			var err error
			label := name + " API key"
			if p.DocsURL != "" {
				label += " (" + p.DocsURL + ")"
			}
			probe := config.OpenAICompat{Name: name, BaseURL: base}
			if key, err = f.key(initKey{env: env, label: label, optional: !keyed, check: func(ctx context.Context, k string) onboard.Result {
				return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "compat", Key: k, Name: probe.Name, BaseURL: probe.BaseURL, Preset: pp(p)}))
			}}); err != nil {
				return err
			}
		}
	}

	model := f.o.model
	if model == "" || (!f.in.nonInteractive && p.Local) {
		list := f.findModels(p, name, base, key)
		if model == "" {
			var err error
			if f.in.nonInteractive {
				if list.Suggested == "" {
					return fmt.Errorf("--model: required for %s (it must support tool calling)", p.Label)
				}
				model = list.Suggested
			} else if model, err = f.pickCompatModel(list); err != nil {
				return err
			}
		}
	}
	f.input.Compat.Model = model
	if f.wantToolTest() && !f.o.skipValidate {
		r := onboard.ProbeTools(f.ctx, f.ep, onboard.Compat{Name: name, BaseURL: base, APIKey: key, KeyEnv: onboard.CompatKeyEnv(f.cur, name, reuse), NoKey: onboard.KeyOptionalURL(base), Model: model})
		fprintln(f.out, "  "+r.Line())
	}
	return nil
}

// pp is a preset id for a compat key check ("" for Other URL).
func pp(p onboard.Preset) string {
	if p.Kind == onboard.PresetCompat {
		return p.ID
	}
	return ""
}

// askReuse offers the voice's OpenAI key to the OpenAI orchestrator, only
// on an explicit yes.
func (f *initFlow) askReuse(p onboard.Preset) bool {
	if f.in.nonInteractive || (f.have[p.ReuseEnv] == "" && f.existing[p.ReuseEnv] == "") {
		return false
	}
	yes, err := f.in.yesNo("Use the same OpenAI key as the voice for the orchestrator?", false)
	return err == nil && yes
}

// findModels is the page's "Find models": the models the endpoint lists,
// tool-capable first. A local server that is not running is waited for
// (interactive) or reported.
func (f *initFlow) findModels(p onboard.Preset, name, base, key string) onboard.ModelsResponse {
	if f.o.skipValidate {
		return onboard.ModelsResponse{}
	}
	req := onboard.ModelsRequest{Name: name, BaseURL: base, Preset: pp(p)}
	for attempt := 0; ; attempt++ {
		resp := onboard.FindModels(f.ctx, f.ep.HTTP, f.presets, req, key)
		if resp.Error == "" {
			if p.Local {
				fprintf(f.out, "  %s is running (%d models)\n", p.Label, len(resp.Models))
			}
			sort.SliceStable(resp.Models, func(i, j int) bool { return isTool(resp.Models[i]) && !isTool(resp.Models[j]) })
			return resp
		}
		fprintf(f.out, "  %s %s\n", resp.Error, resp.Fix)
		if !p.Local || f.in.nonInteractive || attempt >= 2 {
			return onboard.ModelsResponse{}
		}
		// The page's "press Check again".
		if v, err := f.in.ask("Press Enter to check again, or type s to type the model name yourself", "", "again"); err != nil || strings.EqualFold(v, "s") {
			return onboard.ModelsResponse{}
		}
	}
}

func isTool(m onboard.ModelInfo) bool { return m.Tools != nil && *m.Tools }

// pickCompatModel offers the listed models (tool-capable first) and the
// suggested one on Enter; with no list the name is typed.
func (f *initFlow) pickCompatModel(list onboard.ModelsResponse) (string, error) {
	entries := make([]onboard.ModelEntry, len(list.Models))
	for i, m := range list.Models {
		entries[i] = onboard.ModelEntry(m)
	}
	return f.pickOrchModel(entries, list.Suggested, true)
}

// pickOrchModel is the numbered model list: tool-capable first, marked
// when the endpoint says. def is what Enter takes ("" : the name must be
// typed, as for OpenRouter's long list).
func (f *initFlow) pickOrchModel(entries []onboard.ModelEntry, def string, required bool) (string, error) {
	names := make([]string, len(entries))
	notes := map[string]string{}
	tools := false
	for i, e := range entries {
		names[i] = e.ID
		if e.Tools != nil {
			tools = true
			if *e.Tools {
				notes[e.ID] = "tool calling"
			} else {
				notes[e.ID] = "no tool calling"
			}
		}
	}
	if tools {
		sort.SliceStable(names, func(i, j int) bool { return notes[names[i]] == "tool calling" && notes[names[j]] != "tool calling" })
	}
	if def == "" && required && tools {
		for _, n := range names {
			if notes[n] == "tool calling" {
				def = n
				break
			}
		}
	}
	if !required || len(names) > 0 {
		names = onboard.WithFirst(names, def)
	}
	return f.in.pickName("Orchestrator model", names, def, notes)
}
