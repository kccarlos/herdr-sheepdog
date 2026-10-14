package main

import (
	"os"
	"testing"
)

// TestMain keeps every test off the developer's real secret file: the
// implicit env-file fallback (~/.config/sheepdog/.env) is disabled unless
// a test points it at a temp file.
func TestMain(m *testing.M) {
	configEnvPath = func() string { return "" }
	os.Exit(m.Run())
}
