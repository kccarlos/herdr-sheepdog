package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const compatTOML = `
[brain]
provider = "openrouter"

[[brain.openai_compat]]
name = "openrouter"
base_url = "https://openrouter.ai/api/v1"
model = "vendor/model-a"

[[brain.openai_compat]]
name = "my-groq"
label = "Groq"
base_url = "https://api.groq.com/openai/v1"
api_key_env = "GROQ_KEY"
model = "openai/gpt-oss-120b"

[[brain.openai_compat]]
name = "ollama"
base_url = "http://localhost:11434/v1"
model = "qwen3:8b"
`

// Each entry takes its key from api_key_env (defaulted from the name),
// is a valid brain.provider, and supplies brain.model when that is not
// set; config show prints the key as set/unset only.
func TestOpenAICompatProviders(t *testing.T) {
	path := writeConfig(t, compatTOML)
	cfg, err := load(path, envFrom(map[string]string{"OPENROUTER_API_KEY": "sk-or-secret-value", "GROQ_KEY": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Brain.OpenAICompat) != 3 {
		t.Fatalf("entries = %+v", cfg.Brain.OpenAICompat)
	}
	or, groq, ollama := cfg.Brain.OpenAICompat[0], cfg.Brain.OpenAICompat[1], cfg.Brain.OpenAICompat[2]
	if or.APIKeyEnv != "OPENROUTER_API_KEY" || or.APIKey != "sk-or-secret-value" || !or.Usable() || or.KeyOptional() {
		t.Errorf("openrouter = %+v", or)
	}
	if groq.APIKeyEnv != "GROQ_KEY" || groq.APIKey != "" || groq.Usable() {
		t.Errorf("groq = %+v", groq)
	}
	if ollama.APIKeyEnv != "OLLAMA_API_KEY" || !ollama.KeyOptional() || !ollama.Usable() {
		t.Errorf("ollama = %+v", ollama)
	}
	if cfg.Brain.Provider != "openrouter" || cfg.Brain.Model != "vendor/model-a" {
		t.Errorf("brain = %s/%s", cfg.Brain.Provider, cfg.Brain.Model)
	}
	s := cfg.String()
	if strings.Contains(s, "sk-or-secret-value") {
		t.Error("String leaks the key")
	}
	for _, want := range []string{"brain.openai_compat.openrouter.api_key=set", "brain.openai_compat.my-groq.api_key=unset", "brain.openai_compat.ollama.base_url=http://localhost:11434/v1", "brain.openai_compat.my-groq.api_key_env=GROQ_KEY"} {
		if !strings.Contains(s, want) {
			t.Errorf("String lacks %q", want)
		}
	}
	if got := DefaultCompatKeyEnv("my-groq"); got != "MY_GROQ_API_KEY" {
		t.Errorf("DefaultCompatKeyEnv = %q", got)
	}
}

// An explicit brain.model (file or env) wins over the entry's model.
func TestOpenAICompatExplicitModel(t *testing.T) {
	path := writeConfig(t, strings.Replace(compatTOML, `provider = "openrouter"`, "provider = \"openrouter\"\nmodel = \"vendor/model-b\"", 1))
	cfg, err := load(path, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Brain.Model != "vendor/model-b" {
		t.Errorf("file: brain.model = %q", cfg.Brain.Model)
	}
	cfg, err = load(writeConfig(t, compatTOML), envFrom(map[string]string{"SHEEPDOG_BRAIN_MODEL": "vendor/model-c"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Brain.Model != "vendor/model-c" {
		t.Errorf("env: brain.model = %q", cfg.Brain.Model)
	}
	// Another provider keeps the built-in default model.
	cfg, err = load(writeConfig(t, strings.Replace(compatTOML, `provider = "openrouter"`, `provider = "gemini"`, 1)), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Brain.Model != DefaultBrainModel {
		t.Errorf("gemini: brain.model = %q", cfg.Brain.Model)
	}
}

func TestOpenAICompatValidation(t *testing.T) {
	entry := func(name, url, model string) string {
		return "[[brain.openai_compat]]\nname = \"" + name + "\"\nbase_url = \"" + url + "\"\nmodel = \"" + model + "\"\n"
	}
	for _, tc := range []struct{ name, body, want string }{
		{"reserved", entry("deepseek", "https://x.example/v1", "m"), "reserved"},
		{"fake", entry("fake2", "https://x.example/v1", "m"), "reserved"},
		{"bad name", entry("Open Router", "https://x.example/v1", "m"), "lowercase"},
		{"duplicate", entry("or", "https://x.example/v1", "m") + entry("or", "https://y.example/v1", "m"), "duplicate"},
		{"no scheme", entry("or", "openrouter.ai/api/v1", "m"), "http(s) URL"},
		{"no model", entry("or", "https://x.example/v1", ""), "model must not be empty"},
		{"unknown provider", "[brain]\nprovider = \"groq\"\n" + entry("or", "https://x.example/v1", "m"), "want one of gemini, deepseek, vertex, anthropic, or"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(writeConfig(t, tc.body), noEnv)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
