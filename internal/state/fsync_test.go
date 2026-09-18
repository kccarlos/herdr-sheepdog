package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// TestPersistLatencyIsFast proves the default (full_fsync=false) persist
// path stays well under the 50ms budget. Before this change every
// mutation paid os.File.Sync, which on Darwin transparently upgrades to
// F_FULLFSYNC (~0.5s observed on this machine, per WP5); the plain
// per-write fsync used now (sync_unix.go) is orders of magnitude
// cheaper.
func TestPersistLatencyIsFast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	const n = 50
	start := time.Now()
	for i := 0; i < n; i++ {
		pane := fmt.Sprintf("w%d:p1", i)
		if _, _, err := s.Apply(tr(pane, "", herdr.StateIdle, t0)); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	perCall := elapsed / n
	t.Logf("persist: %d calls in %s (%s/call)", n, elapsed, perCall)
	if perCall > 50*time.Millisecond {
		t.Errorf("persist too slow: %s/call, want < 50ms", perCall)
	}
}

// BenchmarkPersistDefault measures one Apply-triggered persist at a time
// (default, full_fsync=false).
func BenchmarkPersistDefault(b *testing.B) {
	path := filepath.Join(b.TempDir(), "state.json")
	s, err := Open(path, Options{})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pane := fmt.Sprintf("w%d:p1", i)
		if _, _, err := s.Apply(tr(pane, "", herdr.StateIdle, t0)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPersistFullFsync is the same, with the hardware-durable pass
// on (debounced, so back-to-back calls inside one fsyncDebounce window
// still pay for at most one fullSync).
func BenchmarkPersistFullFsync(b *testing.B) {
	path := filepath.Join(b.TempDir(), "state.json")
	s, err := Open(path, Options{FullFsync: true})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pane := fmt.Sprintf("w%d:p1", i)
		if _, _, err := s.Apply(tr(pane, "", herdr.StateIdle, t0)); err != nil {
			b.Fatal(err)
		}
	}
}

// TestFullFsyncCoalescesBurst proves that with Options.FullFsync a burst
// of Apply calls inside one fsyncDebounce window triggers exactly one
// durable fsync pass, not one per call, and that the on-disk table is
// complete (matches the final Apply) once that pass has run.
func TestFullFsyncCoalescesBurst(t *testing.T) {
	// Widen the debounce window well past what a 10-call burst could
	// plausibly take even under -race/-count=2 load, so this stays
	// deterministic instead of occasionally splitting into more than one
	// fullSync pass on a slow/contended machine.
	origDebounce := fsyncDebounce
	fsyncDebounce = 500 * time.Millisecond
	t.Cleanup(func() { fsyncDebounce = origDebounce })

	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, Options{FullFsync: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var count int32
	s.onFullSync = func() { atomic.AddInt32(&count, 1) }

	const n = 10
	for i := 0; i < n; i++ {
		pane := fmt.Sprintf("w%d:p1", i)
		if _, _, err := s.Apply(tr(pane, "", herdr.StateIdle, t0)); err != nil {
			t.Fatal(err)
		}
	}
	// The write+rename is never debounced, so the file already has every
	// row right away, well before the durable-fsync debounce fires.
	if len(s.List()) != n {
		t.Fatalf("in-memory table has %d rows, want %d", len(s.List()), n)
	}
	assertFileHasRows(t, path, n)

	waitForCount(t, &count, 1, 5*time.Second)
	// Give a moment for any extra (wrongly-uncoalesced) fires to show up.
	time.Sleep(2 * fsyncDebounce)
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Errorf("fullSync ran %d times for a %d-call burst, want exactly 1", got, n)
	}
	assertFileHasRows(t, path, n)
}

// TestCloseFlushesPendingFullSync proves Close performs the debounced
// durable fsync immediately (synchronously) rather than waiting out the
// debounce window, and that a second Close is a safe no-op.
func TestCloseFlushesPendingFullSync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, Options{FullFsync: true})
	if err != nil {
		t.Fatal(err)
	}
	var count int32
	s.onFullSync = func() { atomic.AddInt32(&count, 1) }

	if _, _, err := s.Apply(tr("w5:p1", "", herdr.StateIdle, t0)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("fullSync ran %d times synchronously from Close, want 1", got)
	}
	assertFileHasRows(t, path, 1)

	// A second Close (and any further scheduling attempt) is a no-op.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Errorf("fullSync ran again on a second Close: %d", got)
	}
}

// TestCloseNoOpWithoutFullFsync proves Close is harmless when
// full_fsync is off (the default): no debounce timer was ever started.
func TestCloseNoOpWithoutFullFsync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Apply(tr("w5:p1", "", herdr.StateIdle, t0)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertFileHasRows(t, path, 1)
}

func assertFileHasRows(t *testing.T, path string, want int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var tbl Table
	if err := json.Unmarshal(data, &tbl); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if got := len(tbl.Agents); got != want {
		t.Fatalf("on-disk table has %d rows, want %d (data=%s)", got, want, data)
	}
}

func waitForCount(t *testing.T, count *int32, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(count) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("fullSync count never reached %d (stuck at %d)", want, atomic.LoadInt32(count))
}
