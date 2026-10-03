package main

import (
	"io"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
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
	} {
		if _, _, run := parseEvalSimFlags(tc.args, io.Discard); run != tc.ok {
			t.Errorf("%v: run = %v, want %v", tc.args, run, tc.ok)
		}
	}
}
