package panelsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	herdrsheepdog "github.com/kccarlos/herdr-sheepdog"
)

func panelFS(main, extra string) fstest.MapFS {
	m := fstest.MapFS{
		"mods/sheepdog/main.js":     {Data: []byte(main)},
		"mods/sheepdog/mod.json":    {Data: []byte(`{"title":"Sheepdog"}`)},
		"mods/sheepdog/model/a.js":  {Data: []byte("export const a = 1;")},
		"mods/sheepdog/ui/panel.js": {Data: []byte("export const p = 1;")},
	}
	if extra != "" {
		m["mods/sheepdog/model/"+extra] = &fstest.MapFile{Data: []byte("x")}
	}
	return m
}

func put(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// installOld writes the old version's panel and layout into a temp home
// through the same path setup uses, and returns the home.
func installOld(t *testing.T) (string, Source) {
	t.Helper()
	home := t.TempDir()
	old := Source{Panel: panelFS("old main", "gone.js"), Layout: []byte(`{"v":1}`), Version: "v1.0.0"}
	emb, err := old.embedded()
	if err != nil {
		t.Fatal(err)
	}
	for rel, f := range emb {
		put(t, filepath.Join(home, "mods", "sheepdog", filepath.FromSlash(rel)), string(f.data))
	}
	put(t, filepath.Join(home, "layouts", "sheepdog.json"), string(old.Layout))
	put(t, filepath.Join(home, "mods", "sheepdog", "gpui-kit.d.ts"), "herdr-app's")
	if err := Record(home, old); err != nil {
		t.Fatal(err)
	}
	return home, old
}

func newer() Source {
	return Source{Panel: panelFS("new main", ""), Layout: []byte(`{"v":2}`), Version: "v1.1.0"}
}

func TestUnmodifiedIsReplaced(t *testing.T) {
	home, _ := installOld(t)
	dir := filepath.Join(home, "mods", "sheepdog")
	if s := Evaluate(home, newer()); !s.Installed || s.Modified || !s.UpdateAvailable || s.Version != "v1.0.0" {
		t.Fatalf("before: %+v", s)
	}
	res, err := Sync(home, newer())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.From != "v1.0.0" || res.To != "v1.1.0" {
		t.Fatalf("result %+v", res)
	}
	if got := read(t, filepath.Join(dir, "main.js")); got != "new main" {
		t.Fatalf("main.js = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "model", "gone.js")); !os.IsNotExist(err) {
		t.Fatalf("a file the new version dropped stayed: %v", err)
	}
	if got := read(t, filepath.Join(dir, "gpui-kit.d.ts")); got != "herdr-app's" {
		t.Fatalf("a file that is not ours was lost: %q", got)
	}
	if m, err := ReadMarker(dir); err != nil || m.Version != "v1.1.0" {
		t.Fatalf("marker %+v %v", m, err)
	}
	if s := Evaluate(home, newer()); s.Modified || s.UpdateAvailable || s.Version != "v1.1.0" {
		t.Fatalf("after: %+v", s)
	}
	// No staging leftovers in home, and a second sync is a no-op.
	ents, _ := os.ReadDir(home)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".sheepdog-") {
			t.Fatalf("leftover %s", e.Name())
		}
	}
	if res, err := Sync(home, newer()); err != nil || res.Updated || res.LayoutUpdated {
		t.Fatalf("second sync %+v %v", res, err)
	}
}

func TestLayoutIsReplacedWithThePanel(t *testing.T) {
	home, _ := installOld(t)
	res, err := Sync(home, newer())
	if err != nil || !res.LayoutUpdated {
		t.Fatalf("%+v %v", res, err)
	}
	if got := read(t, filepath.Join(home, "layouts", "sheepdog.json")); got != `{"v":2}` {
		t.Fatalf("layout = %q", got)
	}
	if m, _ := ReadMarker(filepath.Join(home, "mods", "sheepdog")); m.Layout != sum([]byte(`{"v":2}`)) {
		t.Fatalf("marker layout hash not updated")
	}
}

func TestModifiedLayoutIsKeptButPanelStillUpdates(t *testing.T) {
	home, _ := installOld(t)
	put(t, filepath.Join(home, "layouts", "sheepdog.json"), `{"mine":true}`)
	res, err := Sync(home, newer())
	if err != nil || res.LayoutUpdated || !res.Updated {
		t.Fatalf("%+v %v", res, err)
	}
	if got := read(t, filepath.Join(home, "layouts", "sheepdog.json")); got != `{"mine":true}` {
		t.Fatalf("layout = %q", got)
	}
}

func TestModifiedPanelIsUntouched(t *testing.T) {
	home, _ := installOld(t)
	dir := filepath.Join(home, "mods", "sheepdog")
	put(t, filepath.Join(dir, "main.js"), "my edit")
	res, err := Sync(home, newer())
	if err != nil || res.Updated {
		t.Fatalf("%+v %v", res, err)
	}
	if got := read(t, filepath.Join(dir, "main.js")); got != "my edit" {
		t.Fatalf("main.js = %q", got)
	}
	if s := res.State; !s.Installed || !s.Modified || !s.UpdateAvailable {
		t.Fatalf("state %+v", s)
	}
}

func TestMissingFileCountsAsModified(t *testing.T) {
	home, _ := installOld(t)
	_ = os.Remove(filepath.Join(home, "mods", "sheepdog", "ui", "panel.js"))
	if s := Evaluate(home, newer()); !s.Modified || !s.UpdateAvailable {
		t.Fatalf("%+v", s)
	}
}

func TestNoMarkerIsTreatedAsModified(t *testing.T) {
	home, _ := installOld(t)
	dir := filepath.Join(home, "mods", "sheepdog")
	if err := os.Remove(filepath.Join(dir, MarkerName)); err != nil {
		t.Fatal(err)
	}
	res, err := Sync(home, newer())
	if err != nil || res.Updated || res.LayoutUpdated {
		t.Fatalf("%+v %v", res, err)
	}
	if got := read(t, filepath.Join(dir, "main.js")); got != "old main" {
		t.Fatalf("main.js = %q", got)
	}
	if s := res.State; !s.Modified || !s.UpdateAvailable || s.Version != "" {
		t.Fatalf("state %+v", s)
	}
	// Identical to this build: nothing to report.
	if s := Evaluate(home, Source{Panel: panelFS("old main", "gone.js"), Version: "v1.0.0"}); s.Modified || s.UpdateAvailable {
		t.Fatalf("identical, no marker: %+v", s)
	}
}

func TestMissingFolderCreatesNothing(t *testing.T) {
	home := t.TempDir()
	res, err := Sync(home, newer())
	if err != nil || res.Updated || res.State.Installed {
		t.Fatalf("%+v %v", res, err)
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Fatalf("created %v", ents)
	}
	// Not even when the home itself is missing.
	if _, err := Sync(filepath.Join(home, "nope"), newer()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "nope")); !os.IsNotExist(err) {
		t.Fatalf("home created: %v", err)
	}
}

func TestSymlinkedPanelIsNeverReplaced(t *testing.T) {
	home, _ := installOld(t)
	dir := filepath.Join(home, "mods", "sheepdog")
	checkout := filepath.Join(t.TempDir(), "checkout")
	if err := os.Rename(dir, checkout); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(checkout, dir); err != nil {
		t.Fatal(err)
	}
	res, err := Sync(home, newer())
	if err != nil || res.Updated || !res.State.Modified {
		t.Fatalf("%+v %v", res, err)
	}
	if got := read(t, filepath.Join(checkout, "main.js")); got != "old main" {
		t.Fatalf("checkout changed: %q", got)
	}
}

func TestRecordPartialPanelWritesNoMarker(t *testing.T) {
	home, old := installOld(t)
	dir := filepath.Join(home, "mods", "sheepdog")
	put(t, filepath.Join(dir, "main.js"), "edited")
	if err := Record(home, old); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, MarkerName)); !os.IsNotExist(err) {
		t.Fatalf("marker kept for a changed panel: %v", err)
	}
}

func TestRecordKeepsADifferentLayoutOutOfTheMarker(t *testing.T) {
	home, old := installOld(t)
	put(t, filepath.Join(home, "layouts", "sheepdog.json"), "custom")
	if err := Record(home, old); err != nil {
		t.Fatal(err)
	}
	if m, _ := ReadMarker(filepath.Join(home, "mods", "sheepdog")); m.Layout != "" {
		t.Fatalf("layout hash recorded for a layout setup did not write")
	}
}

func TestFailedSwapPutsTheOldPanelBack(t *testing.T) {
	home, _ := installOld(t)
	dir := filepath.Join(home, "mods", "sheepdog")
	// Make the parent of the folder read-only so the first rename fails.
	mods := filepath.Dir(dir)
	if err := os.Chmod(mods, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(mods, 0o755) }()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if _, err := Sync(home, newer()); err == nil {
		t.Fatal("expected an error")
	}
	if got := read(t, filepath.Join(dir, "main.js")); got != "old main" {
		t.Fatalf("main.js = %q", got)
	}
	if ents, _ := os.ReadDir(home); len(ents) != 2 { // layouts, mods
		t.Fatalf("staging left behind: %v", ents)
	}
}

// The embedded copy is what the repository's panel is: the sync-layout copy
// and the embed cannot drift from mods/sheepdog unnoticed.
func TestEmbeddedPanelMatchesTheModFolder(t *testing.T) {
	src := Source{Panel: herdrsheepdog.Panel, Layout: herdrsheepdog.Layout}
	emb, err := src.embedded()
	if err != nil || len(emb) == 0 {
		t.Fatalf("embedded: %d files, %v", len(emb), err)
	}
	for rel, f := range emb {
		disk, err := os.ReadFile(filepath.Join("..", "..", "mods", "sheepdog", filepath.FromSlash(rel)))
		if err != nil || string(disk) != string(f.data) {
			t.Errorf("%s differs from mods/sheepdog (%v)", rel, err)
		}
	}
	// And nothing runnable in the folder is missing from the embed.
	err = filepath.WalkDir(filepath.Join("..", "..", "mods", "sheepdog"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join("..", "..", "mods", "sheepdog"), p)
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".test.mjs") || rel == "README.md" || rel == ".gitignore" {
			return nil
		}
		if _, ok := emb[rel]; !ok {
			t.Errorf("%s is in mods/sheepdog but not embedded", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	layout, err := os.ReadFile(filepath.Join("..", "..", "sheepdog-layout", "layouts", "sheepdog.json"))
	if err != nil || string(layout) != string(src.Layout) {
		t.Errorf("layout differs from sheepdog-layout (%v)", err)
	}
}
