package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/attention"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
	"github.com/kccarlos/herdr-sheepdog/internal/inject"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func marksApp(backend herdr.Backend) *App {
	return &App{log: logging.NopLogger{}, backend: backend, herdr: inject.NewHerdr(nil, nil)}
}

func TestWireMarksUsesTheBackendsLabelsOnHerdrCore(t *testing.T) {
	core := herdrcore.New(hc.NewClient(filepath.Join(t.TempDir(), "hc.sock")), nil)
	a := marksApp(core)
	a.wireMarks(config.Config{DataDir: t.TempDir()}, nil)

	if a.marker != herdr.Marker(core) || a.marksRun == nil {
		t.Fatalf("marker = %T, marksRun set = %v; want the herdr-core backend and its subscription", a.marker, a.marksRun != nil)
	}
	if a.marksPoke != nil || a.herdr.AfterStart != nil {
		t.Fatal("herdr-core keeps marks in labels: it needs no poke and no start hook")
	}
}

func TestWireMarksKeepsAStoreInTheDataDirOnTheOldHerdr(t *testing.T) {
	dir := t.TempDir()
	a := marksApp(herdr.NewClient(filepath.Join(dir, "herdr.sock")))
	a.wireMarks(config.Config{DataDir: dir, SnapshotIntervalS: 5}, nil)

	if _, ok := a.marker.(*attention.Local); !ok {
		t.Fatalf("marker = %T, want attention.Local", a.marker)
	}
	if a.marksRun == nil || a.marksPoke == nil || a.herdr.AfterStart == nil {
		t.Fatal("the old herdr's marks need the refresh loop, a poke and the start hook")
	}
}

func TestWireMarksInMockModeHasNothingToRefresh(t *testing.T) {
	a := marksApp(nil)
	a.wireMarks(config.Config{DataDir: t.TempDir()}, nil)

	if _, ok := a.marker.(*attention.Local); !ok {
		t.Fatalf("marker = %T, want attention.Local", a.marker)
	}
	if a.marksRun != nil || a.marksPoke != nil || a.herdr.AfterStart != nil {
		t.Fatal("the mock has no herdr to refresh from")
	}
}

// An unreadable data directory must not stop the daemon: the marks live in
// memory for this run.
func TestWireMarksFallsBackToMemory(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a := marksApp(herdr.NewClient(filepath.Join(t.TempDir(), "herdr.sock")))
	a.wireMarks(config.Config{DataDir: notADir}, nil)

	if _, ok := a.marker.(*attention.Local); !ok {
		t.Fatalf("marker = %T, want attention.Local", a.marker)
	}
}
