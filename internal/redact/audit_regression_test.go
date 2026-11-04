package redact

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

func TestAuditRedaction(t *testing.T) {
	cfg := config.Config{}
	cfg.Anthropic.APIKey = "proxy-secret-example"
	r := New(append(ConfigSecrets(cfg), EnvSecrets([]string{"GOOGLE_APPLICATION_CREDENTIALS=/tmp/adc.json"})...)...)
	for _, text := range []string{"primary_key: user_id", "max_token: 128000", "/tmp/adc.json"} {
		if r.String(text) != text {
			t.Errorf("changed %q", text)
		}
	}
	if r.String("proxy-secret-example") != Mask {
		t.Error("anthropic secret leaked")
	}
}
