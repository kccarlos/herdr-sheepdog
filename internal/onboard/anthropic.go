package onboard

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// anthropicService names the Anthropic API, its key variable and its
// console in every Result.
var anthropicService = brain.ServiceOf(brain.ProviderAnthropic)

// anthropicModel builds the brain's Anthropic model (the official SDK)
// over ep.AnthropicBase and ep's client; the key goes in x-api-key only.
func anthropicModel(ep Endpoints, key string) (*brain.AnthropicModel, error) {
	return brain.NewAnthropicModel(brain.AnthropicConfig{APIKey: key, BaseURL: ep.AnthropicBase, HTTPClient: ep.client()})
}

// anthropicNoKey is the Result for a missing key.
func anthropicNoKey(name string) Result {
	return Result{Name: name, Status: Fail, Detail: anthropicService.KeyVar + " is not set", Fix: "create a key at " + anthropicService.Console + " and paste it (sheepdog setup writes it)"}
}

// CheckAnthropicKey validates an Anthropic key with GET /v1/models
// (free). Anthropic does not expose the balance, so nothing is said
// about it.
func CheckAnthropicKey(ctx context.Context, ep Endpoints, key string) Result {
	const name = "anthropic key"
	ids, r := ListAnthropicModels(ctx, ep, key)
	if r.Status != OK {
		r.Name = name
		return r
	}
	return Result{Name: name, Status: OK, Detail: fmt.Sprintf("valid (%d models)", len(ids))}
}

// ListAnthropicModels lists the models an Anthropic key can use (GET
// /v1/models, free), brain.DefaultAnthropicModel first. Every Claude
// model calls tools, so each entry says so.
func ListAnthropicModels(ctx context.Context, ep Endpoints, key string) ([]ModelEntry, Result) {
	const name = "anthropic models"
	if key == "" {
		return nil, anthropicNoKey(name)
	}
	m, err := anthropicModel(ep, key)
	if err != nil {
		return nil, Result{Name: name, Status: Fail, Detail: err.Error(), Fix: "check anthropic.base_url (CONFIG.md)"}
	}
	list, err := m.ListModels(ctx)
	if err != nil {
		return nil, keyFailure(name, anthropicService.Name, anthropicService.KeyVar, anthropicService.Console, err)
	}
	yes := true
	out := make([]ModelEntry, 0, len(list))
	for _, mi := range list {
		if mi.Recommended {
			out = append(out, ModelEntry{ID: mi.Name, Tools: &yes})
		}
	}
	return out, Result{Name: name, Status: OK, Detail: fmt.Sprintf("%d models", len(out))}
}

// ProbeAnthropic runs the tool-calling probe once on model: one tiny
// turn with one tool declared and tool_choice auto (a few tokens, which
// Anthropic bills). OK when a tool call comes back.
func ProbeAnthropic(ctx context.Context, ep Endpoints, key, model string) Result {
	const name = "anthropic tools"
	switch {
	case model == "":
		return Result{Name: name, Status: Fail, Detail: "no model name given", Fix: "pick a model first"}
	case !ValidModelName(model):
		return Result{Name: name, Status: Fail, Detail: "the model name may only hold letters, digits and . _ / -", Fix: "pick a model from the list"}
	case key == "":
		return anthropicNoKey(name)
	}
	if ep.HTTP == nil {
		ep.HTTP = &http.Client{Timeout: 45 * time.Second}
	}
	m, err := anthropicModel(ep, key)
	if err != nil {
		return Result{Name: name, Status: Fail, Detail: err.Error(), Fix: "check anthropic.base_url (CONFIG.md)"}
	}
	return probeToolCall(ctx, m, Compat{Name: brain.ProviderAnthropic, KeyEnv: anthropicService.KeyVar, Model: model}, anthropicService.Console)
}
