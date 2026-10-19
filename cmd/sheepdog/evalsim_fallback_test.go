package main

import (
	"io"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

func TestFallbackModels(t *testing.T) {
	all := map[string]brain.Model{brain.ProviderVertex: braintest.NewModel(), brain.ProviderDeepSeek: braintest.NewModel(), brain.ProviderGemini: braintest.NewModel()}
	ids := func(m map[string]brain.Model) int { return len(m) }
	got, err := fallbackModels(evalSimOpts{provider: brain.ProviderVertex}, all)
	if err != nil || ids(got) != 1 || got[brain.ProviderDeepSeek] == nil {
		t.Errorf("default: %v %v", got, err)
	}
	got, _ = fallbackModels(evalSimOpts{provider: brain.ProviderVertex, allowAIStudio: true}, all)
	if ids(got) != 2 {
		t.Errorf("allow-ai-studio: %v", got)
	}
	if got, _ := fallbackModels(evalSimOpts{provider: brain.ProviderVertex, fallback: "none"}, all); ids(got) != 0 {
		t.Errorf("none: %v", got)
	}
	if got, _ := fallbackModels(evalSimOpts{provider: brain.ProviderVertex, fallback: brain.ProviderDeepSeek}, all); ids(got) != 1 {
		t.Errorf("deepseek: %v", got)
	}
	if _, err := fallbackModels(evalSimOpts{provider: brain.ProviderDeepSeek, fallback: brain.ProviderVertex}, map[string]brain.Model{brain.ProviderDeepSeek: braintest.NewModel()}); err == nil {
		t.Error("a fallback without credentials should fail")
	}
}

func TestParseFallbackFlag(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{[]string{"--fallback-provider", "deepseek"}, true},
		{[]string{"--fallback-provider", "none"}, true},
		{[]string{"--fallback-provider", "gemini"}, false},
		{[]string{"--fallback-provider", "gemini", "--allow-ai-studio"}, true},
		{[]string{"--fallback-provider", "bogus"}, false},
		{[]string{"--live", "--provider", "vertex", "--fallback-provider", "vertex"}, false},
		{[]string{"--live", "--provider", "anthropic", "--fallback-provider", "none"}, true},
		{[]string{"--live", "--provider", "deepseek", "--fallback-provider", "anthropic"}, true},
		{[]string{"--live", "--provider", "anthropic", "--fallback-provider", "anthropic"}, false},
		{[]string{"--live", "--provider", "openai"}, false},
	} {
		if _, _, run := parseEvalSimFlags(tc.args, io.Discard); run != tc.ok {
			t.Errorf("%v: run = %v, want %v", tc.args, run, tc.ok)
		}
	}
}

func TestSimSetupRecordsAnthropicEffort(t *testing.T) {
	cfg := config.Default()
	cfg.Anthropic.Effort, cfg.Anthropic.EventEffort = "low", "low"
	opts := eval.SimOptions{}
	opts.BrainConfig.Provider = brain.ProviderAnthropic
	got := simSetup(evalSimOpts{live: true, provider: brain.ProviderAnthropic}, opts, cfg)
	if got["anthropic effort"] != "low (user turns), low (event turns)" {
		t.Errorf("anthropic effort = %q", got["anthropic effort"])
	}
	opts.BrainConfig.Provider = brain.ProviderDeepSeek
	if _, ok := simSetup(evalSimOpts{live: true, provider: brain.ProviderDeepSeek}, opts, cfg)["anthropic effort"]; ok {
		t.Error("a deepseek run should not record an anthropic effort")
	}
}
