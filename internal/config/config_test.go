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
