package onboard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// TestMain clears every key and token the developer's shell exports, so
// no test can read, send or print a real one.
func TestMain(m *testing.M) {
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		u := strings.ToUpper(k)
		secret := strings.Contains(u, "KEY") || strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET")
		for _, p := range []string{"GEMINI_", "VERTEX_", "OPENAI_", "DEEPSEEK_", "OPENROUTER_", "ANTHROPIC_", "SHEEPDOG_"} {
			secret = secret || strings.HasPrefix(u, p)
		}
		if secret {
			_ = os.Unsetenv(k)
		}
	}
	os.Exit(m.Run())
}

func TestPresetTable(t *testing.T) {
	claims := regexp.MustCompile(`(?i)recommend|best|strong|fast|reliable|good|did well|measured`)
	seen := map[string]bool{}
	for _, p := range Presets() {
		if seen[p.ID] {
			t.Errorf("duplicate preset %s", p.ID)
		}
		seen[p.ID] = true
		if p.Label == "" || p.Note == "" {
			t.Errorf("%s: no label or note", p.ID)
		}
		if p.ID != "deepseek" && claims.MatchString(p.Note) {
			t.Errorf("%s: quality claim in %q (only our measured ones may claim)", p.ID, p.Note)
		}
		if p.Kind == PresetCompat {
			c := config.OpenAICompat{Name: p.ID, BaseURL: p.BaseURL}
			if p.Local != c.KeyOptional() || p.KeyNeeded == p.Local {
				t.Errorf("%s: local %v, key needed %v, loopback %v", p.ID, p.Local, p.KeyNeeded, c.KeyOptional())
			}
		}
	}
	if seen["anthropic"] {
		t.Error("the Anthropic preset is listed before its adapter exists")
	}
}

func TestPresetKeyEnv(t *testing.T) {
	cases := []struct {
		name  string
		reuse bool
		want  string
	}{
		{"openai", false, "OPENAI_ORCH_API_KEY"},
		{"openai", true, "OPENAI_API_KEY"},
		{"openrouter", false, "OPENROUTER_API_KEY"},
		{"openrouter", true, "OPENROUTER_API_KEY"},
		{"ollama", false, "OLLAMA_API_KEY"},
		{"my-groq", false, "MY_GROQ_API_KEY"},
		{"my-groq", true, "MY_GROQ_API_KEY"},
	}
	for _, c := range cases {
		if got := PresetKeyEnv(c.name, c.reuse); got != c.want {
			t.Errorf("PresetKeyEnv(%s, %v) = %s, want %s", c.name, c.reuse, got, c.want)
		}
	}
}

func entries(ids ...string) []ModelEntry {
	var out []ModelEntry
	for _, id := range ids {
		out = append(out, ModelEntry{ID: id})
	}
	return out
}

func TestSuggestModel(t *testing.T) {
	ps := Presets()
	get := func(id string) Preset { p, _ := PresetByID(ps, id); return p }
	yes := true
	cases := []struct {
		preset string
		list   []ModelEntry
		want   string
	}{
		{"openai", entries("gpt-4.1-mini", "gpt-9-mini-2031-01-01", "gpt-9.5-mini", "gpt-9-mini", "gpt-10"), "gpt-9.5-mini"},
		{"openai", entries("gpt-9", "o9"), ""},
		{"openrouter", []ModelEntry{{ID: "a/b", Tools: &yes}}, ""},
		{"ollama", entries("nomic-embed-text", "qwen3:8b"), "qwen3:8b"},
		{"lmstudio", append(entries("plain"), ModelEntry{ID: "tooly", Tools: &yes}), "tooly"},
		{"ollama", nil, ""},
	}
	for _, c := range cases {
		if got := SuggestModel(get(c.preset), c.list); got != c.want {
			t.Errorf("%s: SuggestModel = %q, want %q", c.preset, got, c.want)
		}
	}
	p := get("openrouter")
	p.DefaultModel = "x/y"
	if got := SuggestModel(p, entries("a", "x/y")); got != "x/y" {
		t.Errorf("listed default: %q", got)
	}
}

func TestChatModelsDropsOpenAINonChat(t *testing.T) {
	ps := Presets()
	oa, _ := PresetByID(ps, "openai")
	list := entries("gpt-9-mini", "gpt-9-mini-tts", "gpt-realtime", "text-embedding-3-large", "whisper-1", "dall-e-3", "omni-moderation-latest", "gpt-image-2", "gpt-live-1", "gpt-9")
	var got []string
	for _, m := range ChatModels(oa, list) {
		got = append(got, m.ID)
	}
	if strings.Join(got, ",") != "gpt-9-mini,gpt-9" {
		t.Errorf("openai chat models = %v", got)
	}
	or, _ := PresetByID(ps, "openrouter")
	if len(ChatModels(or, list)) != len(list) {
		t.Error("another preset's list was filtered")
	}
}

func TestUpsertCompat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c := config.OpenAICompat{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKeyEnv: "OPENAI_ORCH_API_KEY", Model: "gpt-9-mini"}
	if err := UpsertCompat(path, c); err != nil {
		t.Fatal(err)
	}
	// A hand-made entry with a label and comments around it.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("\n# my local server\n[[brain.openai_compat]]\nname = 'ollama'   # local\nlabel = \"My Ollama\"\nbase_url = \"http://localhost:11434/v1\"\nmodel = \"qwen3:8b\"\n")
	_ = f.Close()

	for i, env := range []string{"OPENAI_API_KEY", "OPENAI_ORCH_API_KEY", "OPENAI_API_KEY"} {
		c.APIKeyEnv, c.Model = env, "gpt-9-mini"
		if err := UpsertCompat(path, c); err != nil {
			t.Fatal(i, err)
		}
		cfg, err := config.Load(path)
		if err != nil {
			t.Fatal(i, err)
		}
		got, _ := cfg.Brain.CompatProvider("openai")
		if got.APIKeyEnv != env || len(cfg.Brain.OpenAICompat) != 2 {
			t.Errorf("%d: api_key_env = %s (%d entries)", i, got.APIKeyEnv, len(cfg.Brain.OpenAICompat))
		}
	}
	if err := UpsertCompat(path, config.OpenAICompat{Name: "ollama", BaseURL: "http://localhost:11434/v1", APIKeyEnv: "OLLAMA_API_KEY", Model: "llama3.2:3b"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := cfg.Brain.CompatProvider("ollama")
	raw, _ := os.ReadFile(path)
	if o.Model != "llama3.2:3b" || o.Label != "My Ollama" || !strings.Contains(string(raw), "# my local server") {
		t.Errorf("updated ollama = %+v\n%s", o, raw)
	}
	// A result that would not load is refused and the file restored.
	before, _ := os.ReadFile(path)
	if err := UpsertCompat(path, config.OpenAICompat{Name: "ollama", BaseURL: "not a url", Model: "m"}); err == nil {
		t.Error("a broken base_url was written")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("the config was not restored")
	}
}
