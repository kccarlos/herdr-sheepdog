package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) (string, bool) { return "", false }

func envFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.toml"), noEnv)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	def := Default()
	if cfg.Herdr.Socket != def.Herdr.Socket || !strings.HasSuffix(cfg.Herdr.Socket, filepath.Join(".config", "herdr", "herdr.sock")) {
		t.Errorf("herdr.socket = %q", cfg.Herdr.Socket)
	}
	if !strings.HasSuffix(cfg.State.Path, filepath.Join("sheepdog", "state.json")) {
		t.Errorf("state.path = %q", cfg.State.Path)
	}
	if cfg.DebounceMS != 3000 || cfg.SnapshotIntervalS != 30 {
		t.Errorf("timing defaults = %d/%d", cfg.DebounceMS, cfg.SnapshotIntervalS)
	}
	if cfg.Aliases == nil || len(cfg.Aliases) != 0 {
		t.Errorf("aliases = %v, want empty map", cfg.Aliases)
	}
}

func TestLoadFileAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
data_dir = "/data"
debounce_ms = 1500

[herdr]
socket = "/tmp/file.sock"

[state]
path = "/data/s.json"

[aliases]
"w9:p1" = "auth"
"w9:p2" = "docs"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := load(path, noEnv)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != "/data" || cfg.DebounceMS != 1500 || cfg.Herdr.Socket != "/tmp/file.sock" || cfg.State.Path != "/data/s.json" {
		t.Errorf("file values not applied: %+v", cfg)
	}
	if cfg.SnapshotIntervalS != 30 {
		t.Errorf("unset key should keep default, got %d", cfg.SnapshotIntervalS)
	}
	if cfg.Aliases["w9:p1"] != "auth" || cfg.Aliases["w9:p2"] != "docs" {
		t.Errorf("aliases = %v", cfg.Aliases)
	}

	cfg, err = load(path, envFrom(map[string]string{
		"SHEEPDOG_HERDR_SOCKET":        "/tmp/env.sock",
		"SHEEPDOG_STATE_PATH":          "/env/state.json",
		"SHEEPDOG_DATA_DIR":            "/env",
		"SHEEPDOG_DEBOUNCE_MS":         "250",
		"SHEEPDOG_SNAPSHOT_INTERVAL_S": "5",
	}))
	if err != nil {
		t.Fatalf("load with env: %v", err)
	}
	if cfg.Herdr.Socket != "/tmp/env.sock" || cfg.State.Path != "/env/state.json" || cfg.DataDir != "/env" {
		t.Errorf("env paths not applied: %+v", cfg)
	}
	if cfg.DebounceMS != 250 || cfg.SnapshotIntervalS != 5 {
		t.Errorf("env timings not applied: %d/%d", cfg.DebounceMS, cfg.SnapshotIntervalS)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(bad, []byte("this is = not [toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(bad, noEnv); err == nil {
		t.Error("malformed file should fail")
	}
	if _, err := load(filepath.Join(dir, "none.toml"), envFrom(map[string]string{"SHEEPDOG_DEBOUNCE_MS": "soon"})); err == nil {
		t.Error("non-numeric SHEEPDOG_DEBOUNCE_MS should fail")
	}
	if _, err := load(filepath.Join(dir, "none.toml"), envFrom(map[string]string{"SHEEPDOG_SNAPSHOT_INTERVAL_S": "0"})); err == nil {
		t.Error("zero SHEEPDOG_SNAPSHOT_INTERVAL_S should fail")
	}
}

func TestLoadUsesRealEnv(t *testing.T) {
	t.Setenv("SHEEPDOG_HERDR_SOCKET", "/real/env.sock")
	cfg, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Herdr.Socket != "/real/env.sock" {
		t.Errorf("Load did not read process env: %q", cfg.Herdr.Socket)
	}
	if s := cfg.String(); !strings.Contains(s, "/real/env.sock") {
		t.Errorf("String() = %q", s)
	}
}

func TestGeminiKeyPool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
[gemini]
api_keys = ["file-key-a", "file-key-b"]
model = "gemini-file"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Gemini.APIKeys) != 2 || cfg.Gemini.APIKeys[0] != "file-key-a" || cfg.Gemini.Model != "gemini-file" || cfg.Gemini.Voice != "Puck" {
		t.Errorf("file gemini section = %+v", cfg.Gemini)
	}

	cfg, err = load(path, envFrom(map[string]string{
		"GEMINI_API_KEY":        "env-0",
		"GEMINI_API_KEY_1":      "env-1",
		"GEMINI_API_KEY_2":      "",
		"GEMINI_API_KEY_3":      "env-3",
		"SHEEPDOG_GEMINI_MODEL": "gemini-env",
		"SHEEPDOG_GEMINI_VOICE": "Kore",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"env-0", "env-1", "env-3"}
	if len(cfg.Gemini.APIKeys) != len(want) {
		t.Fatalf("keys = %d, want %d", len(cfg.Gemini.APIKeys), len(want))
	}
	for i := range want {
		if cfg.Gemini.APIKeys[i] != want[i] {
			t.Errorf("key[%d] = %q, want %q", i, cfg.Gemini.APIKeys[i], want[i])
		}
	}
	if cfg.Gemini.Model != "gemini-env" || cfg.Gemini.Voice != "Kore" {
		t.Errorf("env overrides not applied: %+v", cfg.Gemini)
	}
	if s := cfg.String(); strings.Contains(s, "env-0") || !strings.Contains(s, "gemini.api_keys=3") {
		t.Errorf("String leaks or omits key count: %s", s)
	}
}

func TestLLMConfig(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.toml"), envFrom(map[string]string{"GEMINI_API_KEY": "g0", "GEMINI_API_KEY_1": "g1"}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LLM.BaseURL != GeminiOpenAIBaseURL || cfg.LLM.Model != "gemini-3.5-flash-lite" || cfg.PendingTTLS != 120 {
		t.Errorf("llm defaults = %+v ttl=%d", cfg.LLM, cfg.PendingTTLS)
	}
	if keys := cfg.LLMKeys(); len(keys) != 2 || keys[0] != "g0" {
		t.Errorf("Gemini pool should back the default llm endpoint, got %d keys", len(keys))
	}

	cfg, err = load(filepath.Join(t.TempDir(), "missing.toml"), envFrom(map[string]string{
		"GEMINI_API_KEY":         "g0",
		"SHEEPDOG_LLM_BASE_URL":  "http://localhost:11434/v1",
		"SHEEPDOG_LLM_MODEL":     "gemma4:latest",
		"SHEEPDOG_PENDING_TTL_S": "30",
	}))
	if err != nil {
		t.Fatalf("load with env: %v", err)
	}
	if cfg.LLM.BaseURL != "http://localhost:11434/v1" || cfg.LLM.Model != "gemma4:latest" || cfg.PendingTTLS != 30 {
		t.Errorf("env llm not applied: %+v ttl=%d", cfg.LLM, cfg.PendingTTLS)
	}
	if keys := cfg.LLMKeys(); len(keys) != 0 {
		t.Errorf("non-Gemini endpoint must not borrow the Gemini pool, got %d keys", len(keys))
	}
	cfg.LLM.APIKey = "explicit"
	if keys := cfg.LLMKeys(); len(keys) != 1 || keys[0] != "explicit" {
		t.Errorf("explicit key should win, got %v", keys)
	}
	if strings.Contains(cfg.String(), "explicit") || strings.Contains(cfg.String(), "g0") {
		t.Errorf("String leaks a key: %s", cfg)
	}
	if _, err := load(filepath.Join(t.TempDir(), "none.toml"), envFrom(map[string]string{"SHEEPDOG_PENDING_TTL_S": "0"})); err == nil {
		t.Error("zero SHEEPDOG_PENDING_TTL_S should fail")
	}
}

func TestWebPort(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.toml"), envFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.Port != DefaultWebPort {
		t.Errorf("default web.port = %d", cfg.Web.Port)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[web]\nport = 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = load(path, envFrom(nil))
	if err != nil || cfg.Web.Port != 8080 {
		t.Errorf("file web.port = %d err=%v", cfg.Web.Port, err)
	}
	cfg, err = load(path, envFrom(map[string]string{"SHEEPDOG_WEB_PORT": "9090"}))
	if err != nil || cfg.Web.Port != 9090 {
		t.Errorf("env web.port = %d err=%v", cfg.Web.Port, err)
	}
	if !strings.Contains(cfg.String(), "web.port=9090") {
		t.Errorf("String lacks web.port: %s", cfg)
	}
	if _, err := load(path, envFrom(map[string]string{"SHEEPDOG_WEB_PORT": "70000"})); err == nil {
		t.Error("bad SHEEPDOG_WEB_PORT should fail")
	}
}
