//go:build unix

package fsaccess

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestProtected(t *testing.T) {
	home := "/Users/x"
	for path, want := range map[string]string{
		"/Users/x/Documents":                        "/Users/x/Documents",
		"/Users/x/Documents/app/src":                "/Users/x/Documents",
		"/Users/x/Library/Mobile Documents/com~a/b": "/Users/x/Library/Mobile Documents",
		"/Users/x/Downloads/z":                      "/Users/x/Downloads",
		"/Users/x/DocumentsOld":                     "",
		"/Users/x/code":                             "",
		"/tmp/Documents":                            "",
	} {
		if got := Protected(home, path); got != want {
			t.Errorf("Protected(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestProbeReadableAndMissing(t *testing.T) {
	if b := Probe(t.TempDir()); b != nil {
		t.Errorf("readable folder blocked: %+v", b)
	}
	if b := Probe(filepath.Join(t.TempDir(), "nope")); b != nil {
		t.Errorf("missing folder blocked: %+v", b)
	}
}

func TestProbeDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if b := Probe(dir); b == nil || b.Reason != Denied || b.Path != dir {
		t.Errorf("Probe = %+v", b)
	}
}

// A read that never answers (a FIFO nobody writes to stands in for the
// macOS prompt) is reported as slow within the timeout, and a second
// request joins the stuck read instead of starting another.
func TestProbeSlow(t *testing.T) {
	old := Timeout
	Timeout = 50 * time.Millisecond
	t.Cleanup(func() { Timeout = old })
	fifo := filepath.Join(t.TempDir(), "stuck")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no fifo:", err)
	}
	start := time.Now()
	if b := Probe(fifo); b == nil || b.Reason != Slow {
		t.Fatalf("Probe = %+v", b)
	}
	if b := Probe(fifo); b == nil || b.Reason != Slow {
		t.Fatalf("second Probe = %+v", b)
	}
	mu.Lock()
	joined := len(waiting[fifo])
	mu.Unlock()
	if joined < 2 {
		t.Errorf("did not join the stuck read: %d waiters", joined)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("took too long")
	}
	// Free the stuck goroutine.
	if f, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
	}
}

func TestSetProbesOnce(t *testing.T) {
	s := NewSet()
	dir := t.TempDir()
	first := s.OK(dir)
	if !first || !s.OK(dir) || len(s.Blocked()) != 0 {
		t.Errorf("blocked = %+v", s.Blocked())
	}
}
