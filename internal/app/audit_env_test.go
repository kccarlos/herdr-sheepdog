package app

import (
	"slices"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

func TestAuditWithheldEnvProvenance(t *testing.T) {
	cfg := config.Config{}
	cfg.Brain.OpenAICompat = []config.OpenAICompat{{APIKeyEnv: "CUSTOM_PROVIDER"}}
	got := cliWithheldEnv(cfg, []string{"HTTPS_PROXY", "PATH", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"})
	for _, n := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CUSTOM_PROVIDER"} {
		if !slices.Contains(got, n) {
			t.Errorf("missing %s", n)
		}
	}
	for _, n := range []string{"HTTPS_PROXY", "PATH"} {
		if slices.Contains(got, n) {
			t.Errorf("withheld %s", n)
		}
	}
}
