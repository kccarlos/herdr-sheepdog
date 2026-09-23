package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVoiceProviderConfig: [voice] provider and [voice.openai] from the
// file and env (OPENAI_API_KEY like the other provider keys), the gemini
// sensitivities, validation, and the key never printed.
func TestVoiceProviderConfig(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.toml"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Voice.Provider != "gemini" || cfg.Voice.OpenAI.Model != "gpt-live-1" || cfg.Voice.OpenAI.Voice != "marin" || cfg.Voice.OpenAI.IdleCloseS != 45 || cfg.Gemini.StartSensitivity != "low" || cfg.Gemini.EndSensitivity != "default" {
		t.Errorf("defaults = %+v / %s %s", cfg.Voice, cfg.Gemini.StartSensitivity, cfg.Gemini.EndSensitivity)
	}
	path := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(path, []byte("[voice]\nprovider = \"openai\"\n[voice.openai]\nvoice = \"cedar\"\nidle_close_s = 20\n[gemini]\nend_sensitivity = \"high\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = load(path, envFrom(map[string]string{"OPENAI_API_KEY": "sk-secret-1234", "SHEEPDOG_VOICE_OPENAI_IDLE_CLOSE_S": "30", "SHEEPDOG_GEMINI_START_SENSITIVITY": "HIGH"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Voice.Provider != "openai" || cfg.Voice.OpenAI.Voice != "cedar" || cfg.Voice.OpenAI.IdleCloseS != 30 || cfg.Voice.OpenAI.APIKey != "sk-secret-1234" || cfg.Gemini.StartSensitivity != "high" || cfg.Gemini.EndSensitivity != "high" {
		t.Errorf("file+env = %+v / %s %s", cfg.Voice, cfg.Gemini.StartSensitivity, cfg.Gemini.EndSensitivity)
	}
	s := cfg.String()
	if strings.Contains(s, "sk-secret") || !strings.Contains(s, "voice.openai.api_key=set") || !strings.Contains(s, "voice.provider=openai") {
		t.Errorf("String() = %s", s)
	}
	for env, want := range map[string]string{
		"SHEEPDOG_VOICE_PROVIDER":            "voice.provider",
		"SHEEPDOG_VOICE_OPENAI_IDLE_CLOSE_S": "SHEEPDOG_VOICE_OPENAI_IDLE_CLOSE_S",
		"SHEEPDOG_GEMINI_END_SENSITIVITY":    "gemini.end_sensitivity",
	} {
		if _, err := load(filepath.Join(t.TempDir(), "missing.toml"), envFrom(map[string]string{env: "bogus"})); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s=bogus: %v", env, err)
		}
	}
}
