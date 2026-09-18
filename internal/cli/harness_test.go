package cli

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// fakeBinPath returns the absolute path of testdata/fakebin.sh (see that
// file for the behaviors it supports).
func fakeBinPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "fakebin.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("testdata/fakebin.sh: %v", err)
	}
	return abs
}

// writeFakeBin copies fakebin.sh into dir under name (e.g. "codex"),
// executable, and returns its path. Used for the PATH-lookup test.
func writeFakeBin(t *testing.T, dir, name string) string {
	t.Helper()
	src, err := os.ReadFile(fakeBinPath(t))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, name)
	if err := os.WriteFile(dst, src, 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

// recLog records structured events (mirrors internal/supervisor's test
// double; kept local so internal/cli has no test-only dependency on it).
type recLog struct {
	mu     sync.Mutex
	events []logging.Line
}

func (l *recLog) Log(source, typ, paneID string, payload any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, logging.Line{Source: source, Type: typ, PaneID: paneID, Payload: payload})
}

func (l *recLog) ofType(typ string) []logging.Line {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []logging.Line
	for _, e := range l.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// processAlive reports whether pid is a live process (works without
// waiting for it, unlike os.FindProcess+Wait on Unix).
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
