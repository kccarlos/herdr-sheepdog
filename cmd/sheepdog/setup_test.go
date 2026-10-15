package main

import (
	"os"
	"strings"
	"testing"
)

// TestMain keeps every test off the developer's real secret file: the
// implicit env-file fallback (~/.config/sheepdog/.env) is disabled unless
// a test points it at a temp file. HOME and the XDG directories point at
// a throwaway directory too, so no test can reach the developer's real
// config, data dir, pid file, runtime log or login service.
func TestMain(m *testing.M) {
	configEnvPath = func() string { return "" }
	home, err := os.MkdirTemp("", "sheepdog-test-home-")
	if err != nil {
		panic(err)
	}
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		_ = os.Setenv(k, home)
	}
	// The developer's shell may export real keys and SHEEPDOG_* settings:
	// no test may see them.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "SHEEPDOG_") || strings.HasSuffix(k, "_API_KEY") || strings.HasPrefix(k, "GEMINI_API_KEY") || strings.HasPrefix(k, "VERTEX_") {
			_ = os.Unsetenv(k)
		}
	}
	// The default push mode is the deployed shared relay: keep doctor and
	// setup checks from calling it over the network.
	_ = os.Setenv("SHEEPDOG_PUSH_MODE", "off")
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
