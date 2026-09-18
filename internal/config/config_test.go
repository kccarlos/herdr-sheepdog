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

func TestStateFullFsync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[state]\nfull_fsync = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path, noEnv)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.State.FullFsync {
		t.Error("state.full_fsync from file not applied")
	}
	if !strings.Contains(cfg.String(), "state.full_fsync=true") {
		t.Errorf("String() = %q", cfg.String())
	}

	// Default is false, and the env override can turn it off again.
	def, err := load(filepath.Join(dir, "missing.toml"), noEnv)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if def.State.FullFsync {
		t.Error("state.full_fsync default should be false")
	}
	cfg, err = load(path, envFrom(map[string]string{"SHEEPDOG_STATE_FULL_FSYNC": "false"}))
	if err != nil {
		t.Fatalf("load with env: %v", err)
	}
	if cfg.State.FullFsync {
		t.Error("SHEEPDOG_STATE_FULL_FSYNC=false should override the file")
	}

	if _, err := load(path, envFrom(map[string]string{"SHEEPDOG_STATE_FULL_FSYNC": "loud"})); err == nil {
		t.Error("non-bool SHEEPDOG_STATE_FULL_FSYNC should be an error")
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
	if cfg.Web.Dev || cfg.Web.DevTestdataDir != "" {
		t.Errorf("web.dev on by default: %+v", cfg.Web)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[web]\nport = 8080\ndev = true\ndev_testdata_dir = \"/tmp/fixtures\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = load(path, envFrom(nil))
	if err != nil || cfg.Web.Port != 8080 {
		t.Errorf("file web.port = %d err=%v", cfg.Web.Port, err)
	}
	if !cfg.Web.Dev || cfg.Web.DevTestdataDir != "/tmp/fixtures" {
		t.Errorf("file web.dev = %+v", cfg.Web)
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

func TestCLIConfig(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.toml"), envFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CLI.Prefer != "auto" || cfg.CLI.CodexBin != "codex" || cfg.CLI.ClaudeBin != "claude" {
		t.Errorf("cli defaults = %+v", cfg.CLI)
	}
	if cfg.CLI.TimeoutS != 60 || cfg.CLI.MaxOutputKB != 16 {
		t.Errorf("cli timeout/cap defaults = %d/%d", cfg.CLI.TimeoutS, cfg.CLI.MaxOutputKB)
	}
	if cfg.CLI.ClaudeModel == "" || cfg.CLI.CodexReasoning == "" {
		t.Errorf("cli model/reasoning defaults = %+v", cfg.CLI)
	}
	if len(cfg.CLI.CodexCmd) == 0 || len(cfg.CLI.ClaudeCmd) == 0 {
		t.Error("default argv templates should not be empty")
	}
	for _, want := range []string{"{question}", "{cwd}", "{reasoning}"} {
		if !strings.Contains(strings.Join(cfg.CLI.CodexCmd, " "), want) {
			t.Errorf("codex_cmd missing %s placeholder: %v", want, cfg.CLI.CodexCmd)
		}
	}
	for _, want := range []string{"{question}", "{model}"} {
		if !strings.Contains(strings.Join(cfg.CLI.ClaudeCmd, " "), want) {
			t.Errorf("claude_cmd missing %s placeholder: %v", want, cfg.CLI.ClaudeCmd)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
[cli]
prefer = "codex"
codex_bin = "/usr/local/bin/codex"
codex_cmd = ["codex", "exec", "{question}"]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = load(path, envFrom(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.CLI.Prefer != "codex" || cfg.CLI.CodexBin != "/usr/local/bin/codex" {
		t.Errorf("file cli values not applied: %+v", cfg.CLI)
	}
	if len(cfg.CLI.CodexCmd) != 3 {
		t.Errorf("file codex_cmd not applied: %v", cfg.CLI.CodexCmd)
	}
	if cfg.CLI.ClaudeBin != "claude" {
		t.Errorf("unset cli key should keep default, got %q", cfg.CLI.ClaudeBin)
	}

	cfg, err = load(path, envFrom(map[string]string{
		"SHEEPDOG_CLI_PREFER":          "claude",
		"SHEEPDOG_CLI_CLAUDE_BIN":      "/opt/claude",
		"SHEEPDOG_CLI_CLAUDE_MODEL":    "claude-haiku-test",
		"SHEEPDOG_CLI_CODEX_REASONING": "high",
		"SHEEPDOG_CLI_TIMEOUT_S":       "10",
		"SHEEPDOG_CLI_MAX_OUTPUT_KB":   "4",
	}))
	if err != nil {
		t.Fatalf("load with env: %v", err)
	}
	if cfg.CLI.Prefer != "claude" || cfg.CLI.ClaudeBin != "/opt/claude" || cfg.CLI.ClaudeModel != "claude-haiku-test" {
		t.Errorf("env cli values not applied: %+v", cfg.CLI)
	}
	if cfg.CLI.CodexReasoning != "high" || cfg.CLI.TimeoutS != 10 || cfg.CLI.MaxOutputKB != 4 {
		t.Errorf("env cli values not applied: %+v", cfg.CLI)
	}
	if !strings.Contains(cfg.String(), "cli.prefer=claude") {
		t.Errorf("String lacks cli.prefer: %s", cfg)
	}

	if _, err := load(path, envFrom(map[string]string{"SHEEPDOG_CLI_TIMEOUT_S": "0"})); err == nil {
		t.Error("zero SHEEPDOG_CLI_TIMEOUT_S should fail")
	}
	if _, err := load(path, envFrom(map[string]string{"SHEEPDOG_CLI_MAX_OUTPUT_KB": "nope"})); err == nil {
		t.Error("non-numeric SHEEPDOG_CLI_MAX_OUTPUT_KB should fail")
	}
}

func TestSupervisorConfig(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.toml"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Supervisor.Chattiness != "normal" || cfg.Supervisor.RecentMinutes != 30 {
		t.Errorf("supervisor defaults = %+v", cfg.Supervisor)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[supervisor]\nchattiness = \"quiet\"\nrecent_minutes = 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = load(path, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Supervisor.Chattiness != "quiet" || cfg.Supervisor.RecentMinutes != 5 {
		t.Errorf("from file = %+v", cfg.Supervisor)
	}
	cfg, err = load(path, envFrom(map[string]string{"SHEEPDOG_SUPERVISOR_CHATTINESS": " Chatty ", "SHEEPDOG_SUPERVISOR_RECENT_MINUTES": "45"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Supervisor.Chattiness != "chatty" || cfg.Supervisor.RecentMinutes != 45 {
		t.Errorf("env override = %+v", cfg.Supervisor)
	}
	if !strings.Contains(cfg.String(), "supervisor.chattiness=chatty supervisor.recent_minutes=45") {
		t.Errorf("String() = %q", cfg.String())
	}
	for _, env := range []map[string]string{
		{"SHEEPDOG_SUPERVISOR_CHATTINESS": "loud"},
		{"SHEEPDOG_SUPERVISOR_RECENT_MINUTES": "0"},
		{"SHEEPDOG_SUPERVISOR_RECENT_MINUTES": "soon"},
	} {
		if _, err := load(path, envFrom(env)); err == nil {
			t.Errorf("%v should fail", env)
		}
	}
	if err := os.WriteFile(path, []byte("[supervisor]\nchattiness = \"LOUD\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(path, noEnv); err == nil || !strings.Contains(err.Error(), "supervisor.chattiness") {
		t.Errorf("bad level in file: %v", err)
	}
}

func TestBrainConfig(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.toml"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Brain.Enabled || cfg.Brain.Model != DefaultBrainModel || cfg.Brain.MaxSteps != 6 || cfg.Brain.AskTimeoutS != 8 || cfg.Brain.ProposeTimeoutS != 12 || cfg.Brain.WindowTokens != 24000 || cfg.Brain.PruneAfterTurns != 3 {
		t.Errorf("brain defaults = %+v", cfg.Brain)
	}
	if cfg.LLM.TimeoutS != 15 {
		t.Errorf("llm.timeout_s default = %d", cfg.LLM.TimeoutS)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "[llm]\ntimeout_s = 40\n[brain]\nenabled = false\nmodel = \"file-model\"\nmax_steps = 3\nwindow_tokens = 1000\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = load(path, envFrom(map[string]string{
		"SHEEPDOG_BRAIN_ENABLED": "true", "SHEEPDOG_BRAIN_MODEL": "env-model", "SHEEPDOG_BRAIN_ASK_TIMEOUT_S": "4",
		"SHEEPDOG_BRAIN_PROPOSE_TIMEOUT_S": "9", "SHEEPDOG_BRAIN_PRUNE_AFTER_TURNS": "1", "SHEEPDOG_LLM_TIMEOUT_S": "60",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Brain.Enabled || cfg.Brain.Model != "env-model" || cfg.Brain.MaxSteps != 3 || cfg.Brain.AskTimeoutS != 4 || cfg.Brain.ProposeTimeoutS != 9 || cfg.Brain.WindowTokens != 1000 || cfg.Brain.PruneAfterTurns != 1 {
		t.Errorf("brain = %+v", cfg.Brain)
	}
	if cfg.LLM.TimeoutS != 60 {
		t.Errorf("llm.timeout_s = %d", cfg.LLM.TimeoutS)
	}
	if !strings.Contains(cfg.String(), "brain.model=env-model") || !strings.Contains(cfg.String(), "llm.timeout_s=60") {
		t.Errorf("String() = %s", cfg.String())
	}

	for name, env := range map[string]map[string]string{
		"enabled":   {"SHEEPDOG_BRAIN_ENABLED": "maybe"},
		"max_steps": {"SHEEPDOG_BRAIN_MAX_STEPS": "0"},
		"window":    {"SHEEPDOG_BRAIN_WINDOW_TOKENS": "-1"},
		"llm":       {"SHEEPDOG_LLM_TIMEOUT_S": "x"},
	} {
		if _, err := load(filepath.Join(t.TempDir(), "missing.toml"), envFrom(env)); err == nil {
			t.Errorf("%s: bad value accepted", name)
		}
	}
	if err := os.WriteFile(path, []byte("[brain]\nmodel = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(path, noEnv); err == nil {
		t.Error("empty brain.model accepted")
	}
}
