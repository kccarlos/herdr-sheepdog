package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// TestMain keeps every test off the developer's real secret file: the
// implicit env-file fallback (~/.config/sheepdog/.env) is disabled unless
// a test points it at a temp file. HOME and the XDG directories point at
// a throwaway directory too, so no test can reach the developer's real
// config, data dir, pid file, runtime log or login service.
//
// The tests that build the binary run `go build` under that HOME, so the
// Go caches are pinned to the developer's real ones first: otherwise the
// build downloads every module into HOME/go/pkg/mod, whose read-only
// files os.RemoveAll cannot delete (about 120 MB left behind per run).
func TestMain(m *testing.M) {
	configEnvPath = func() string { return "" }
	pinGoCaches()
	home, err := os.MkdirTemp("", "sheepdog-test-home-")
	if err != nil {
		panic(err)
	}
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		_ = os.Setenv(k, home)
	}
	// Commands that inspect the host during setup see a Linux test home
	// with no herdr tools. Focused setup tests inject their own probe.
	setupEnvironmentProbe = func() onboard.Probe {
		return onboard.Probe{
			GOOS: "linux",
			Home: home,
			Getenv: func(string) string {
				return ""
			},
			LookPath: func(string) (string, error) {
				return "", os.ErrNotExist
			},
			Stat:          os.Stat,
			ReadFile:      os.ReadFile,
			SocketAnswers: func(string) bool { return false },
		}
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
	// No test asks GitHub: the daily check points at a closed port.
	releaseAPI = "http://127.0.0.1:9"
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// pinGoCaches sets GOCACHE, GOMODCACHE and GOPATH to the values the go
// command resolves now, before HOME moves. Without a go command it does
// nothing: only the tests that build the binary need it.
func pinGoCaches() {
	keys := []string{"GOCACHE", "GOMODCACHE", "GOPATH"}
	out, err := exec.Command("go", append([]string{"env"}, keys...)...).Output()
	if err != nil {
		return
	}
	vals := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(vals) != len(keys) {
		return
	}
	for i, k := range keys {
		_ = os.Setenv(k, vals[i])
	}
}
