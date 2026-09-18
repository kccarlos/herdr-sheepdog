package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "" +
		"# a comment, and a blank line follow\n" +
		"\n" +
		"GEMINI_API_KEY=abc123\n" +
		"SHEEPDOG_LLM_MODEL=\"quoted value\"\n" +
		"SHEEPDOG_CLI_PREFER='codex'\n" +
		"   \n" +
		"# SHEEPDOG_WEB_PORT=9999 (commented out, must not apply)\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"GEMINI_API_KEY", "SHEEPDOG_LLM_MODEL", "SHEEPDOG_CLI_PREFER", "SHEEPDOG_WEB_PORT"} {
		orig, had := os.LookupEnv(k)
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, orig)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}

	if err := LoadEnvFile(path); err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if v := os.Getenv("GEMINI_API_KEY"); v != "abc123" {
		t.Errorf("GEMINI_API_KEY = %q", v)
	}
	if v := os.Getenv("SHEEPDOG_LLM_MODEL"); v != "quoted value" {
		t.Errorf("SHEEPDOG_LLM_MODEL = %q, want unquoted", v)
	}
	if v := os.Getenv("SHEEPDOG_CLI_PREFER"); v != "codex" {
		t.Errorf("SHEEPDOG_CLI_PREFER = %q, want unquoted", v)
	}
	if v, set := os.LookupEnv("SHEEPDOG_WEB_PORT"); set {
		t.Errorf("commented-out line should not apply, got %q", v)
	}
}

// TestLoadEnvFileRealEnvWins proves a variable already set in the process
// environment is left alone by the file.
func TestLoadEnvFileRealEnvWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("SHEEPDOG_WEB_PORT=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHEEPDOG_WEB_PORT", "9090")
	if err := LoadEnvFile(path); err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if v := os.Getenv("SHEEPDOG_WEB_PORT"); v != "9090" {
		t.Errorf("SHEEPDOG_WEB_PORT = %q, want the real env value 9090 to win", v)
	}
}

func TestLoadEnvFileErrors(t *testing.T) {
	dir := t.TempDir()

	if err := LoadEnvFile(filepath.Join(dir, "missing.env")); err == nil {
		t.Error("missing file should be an error")
	}

	bad := filepath.Join(dir, "bad.env")
	if err := os.WriteFile(bad, []byte("not a key value line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(bad); err == nil {
		t.Error("a line without = should be an error")
	}

	emptyKey := filepath.Join(dir, "emptykey.env")
	if err := os.WriteFile(emptyKey, []byte("=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(emptyKey); err == nil {
		t.Error("an empty key should be an error")
	}
}
