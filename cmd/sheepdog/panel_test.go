package main

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/panelsync"
)

type eventRecorder struct{ types []string }

func (r *eventRecorder) Log(_, typ, _ string, _ any) { r.types = append(r.types, typ) }

func panelTrackerFixture(t *testing.T, auto bool) (*panelTracker, string) {
	t.Helper()
	home := t.TempDir()
	old := panelsync.Source{Panel: fstest.MapFS{"mods/sheepdog/main.js": {Data: []byte("old")}}, Layout: []byte("l1"), Version: "v1"}
	dir := filepath.Join(home, "mods", "sheepdog")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.js"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := panelsync.Record(home, old); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Panel.AutoUpdate = auto
	tr := newPanelTracker(cfg)
	tr.home = home
	tr.src = panelsync.Source{Panel: fstest.MapFS{"mods/sheepdog/main.js": {Data: []byte("new")}}, Layout: []byte("l2"), Version: "v2"}
	return tr, dir
}

func TestPanelTrackerUpdatesAndLogs(t *testing.T) {
	tr, dir := panelTrackerFixture(t, true)
	if s := tr.State(); !s.UpdateAvailable || s.Modified || !s.AutoUpdate {
		t.Fatalf("before: %+v", s)
	}
	rec := &eventRecorder{}
	if line := tr.Sync(rec); line == "" {
		t.Fatal("no line printed")
	}
	if len(rec.types) != 1 || rec.types[0] != "panel.updated" {
		t.Fatalf("events = %v", rec.types)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "main.js")); string(b) != "new" {
		t.Fatalf("main.js = %q", b)
	}
	if s := tr.State(); s.UpdateAvailable || s.Version != "v2" {
		t.Fatalf("after: %+v", s)
	}
}

func TestPanelTrackerDisabledByConfig(t *testing.T) {
	tr, dir := panelTrackerFixture(t, false)
	rec := &eventRecorder{}
	if line := tr.Sync(rec); line != "" || len(rec.types) != 0 {
		t.Fatalf("line %q events %v", line, rec.types)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "main.js")); string(b) != "old" {
		t.Fatalf("main.js = %q", b)
	}
	if s := tr.State(); !s.UpdateAvailable || s.Modified || s.AutoUpdate {
		t.Fatalf("state: %+v", s)
	}
}
