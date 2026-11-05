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
	if err != nil || res.LayoutUpdated || !res.LayoutSidecar || !res.Updated {
		t.Fatalf("%+v %v", res, err)
	}
	if got := read(t, filepath.Join(home, "layouts", "sheepdog.json")); got != `{"mine":true}` {
		t.Fatalf("layout = %q", got)
	}
	if got := read(t, filepath.Join(home, "layouts", "sheepdog.json.new")); got != `{"v":2}` {
		t.Fatalf("layout sidecar = %q", got)
	}
	// If the user deletes sheepdog.json.new after reviewing it, subsequent daemon starts of the same release must not recreate it.
	if err := os.Remove(filepath.Join(home, "layouts", "sheepdog.json.new")); err != nil {
		t.Fatal(err)
	}
	res2, err := Sync(home, newer())
	if err != nil || res2.LayoutSidecar {
		t.Fatalf("second sync recreated deleted sidecar: %+v %v", res2, err)
	}
	if _, err := os.Stat(filepath.Join(home, "layouts", "sheepdog.json.new")); !os.IsNotExist(err) {
		t.Fatalf("deleted sidecar came back: %v", err)
	}
	// A later release with a newer layout writes the sidecar once for that new version.
	v3 := Source{Panel: panelFS("new main", ""), Layout: []byte(`{"v":3}`), Version: "v1.2.0"}
	res3, err := Sync(home, v3)
	if err != nil || !res3.LayoutSidecar {
		t.Fatalf("v3 sync did not write sidecar: %+v %v", res3, err)
	}
	if got := read(t, filepath.Join(home, "layouts", "sheepdog.json.new")); got != `{"v":3}` {
		t.Fatalf("v3 sidecar = %q", got)
	}
}

func TestResizedOrToggledLayoutStillUpdatesCleanly(t *testing.T) {
	home := t.TempDir()
	v1Layout := `{"id":"root","type":"row","children":[{"id":"sidebar","type":"block","size":{"px":240},"hidden":false,"content":{"mod":"workspace-chrome"}},{"id":"sheepdog","type":"block","size":{"px":340},"min":44,"max":720,"content":{"mod":"sheepdog"}}]}`
	old := Source{Panel: panelFS("old main", ""), Layout: []byte(v1Layout), Version: "v1.0.0"}
	if err := InstallPanel(home, old.Panel); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(home, "layouts", "sheepdog.json"), v1Layout)
	if err := Record(home, old); err != nil {
		t.Fatal(err)
	}

	// Simulate herdr-app mutating runtime fields (pane size, hidden, active) and formatting.
	resized := "{\n  \"id\": \"root\",\n  \"type\": \"row\",\n  \"active\": \"sheepdog\",\n  \"children\": [\n    {\"id\": \"sidebar\", \"type\": \"block\", \"size\": {\"px\": 300}, \"hidden\": true, \"content\": {\"mod\": \"workspace-chrome\"}},\n    {\"id\": \"sheepdog\", \"type\": \"block\", \"size\": {\"px\": 480}, \"min\": 44, \"max\": 720, \"content\": {\"mod\": \"sheepdog\"}}\n  ]\n}\n"
	put(t, filepath.Join(home, "layouts", "sheepdog.json"), resized)

	// When the shipped layout's structure has NOT changed, Sync keeps the user's resized file as-is.
	sameStruct := Source{Panel: panelFS("new main", ""), Layout: []byte(v1Layout), Version: "v1.0.1"}
	res, err := Sync(home, sameStruct)
	if err != nil || !res.Updated || res.LayoutUpdated || res.LayoutSidecar {
		t.Fatalf("same-structure sync: %+v, %v", res, err)
	}
	if got := read(t, filepath.Join(home, "layouts", "sheepdog.json")); got != resized {
		t.Fatalf("resized layout was clobbered when structure did not change: %q", got)
	}

	// When a later release changes layout structure (e.g. min: 48), Sync recognizes the resized layout as unmodified and updates it.
	v2Layout := `{"id":"root","type":"row","children":[{"id":"sidebar","type":"block","size":{"px":240},"hidden":false,"content":{"mod":"workspace-chrome"}},{"id":"sheepdog","type":"block","size":{"px":340},"min":48,"max":720,"content":{"mod":"sheepdog"}}]}`
	put(t, filepath.Join(home, "layouts", "sheepdog.json.new"), "stale")
	nextStruct := Source{Panel: panelFS("v2 main", ""), Layout: []byte(v2Layout), Version: "v1.1.0"}
	res, err = Sync(home, nextStruct)
	if err != nil || !res.Updated || !res.LayoutUpdated || res.LayoutSidecar {
		t.Fatalf("new-structure sync: %+v, %v", res, err)
	}
	if got := read(t, filepath.Join(home, "layouts", "sheepdog.json")); got != v2Layout {
		t.Fatalf("updated layout = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "layouts", "sheepdog.json.new")); !os.IsNotExist(err) {
		t.Fatalf("stale .new sidecar stayed: %v", err)
	}
}

func TestAtomicSwapAndFallbackKeepPanelPresent(t *testing.T) {
	home, _ := installOld(t)
	dir := filepath.Join(home, "mods", "sheepdog")

	// 1. Native atomic swap (darwin RENAME_SWAP / linux RENAME_EXCHANGE).
	stop := make(chan struct{})
	missing := make(chan string, 1)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := os.Stat(filepath.Join(dir, "main.js")); err != nil {
					select {
					case missing <- err.Error():
					default:
					}
					return
				}
			}
		}
	}()
	if res, err := Sync(home, newer()); err != nil || !res.Updated {
		close(stop)
		t.Fatalf("atomic sync: %+v, %v", res, err)
	}
	close(stop)
	select {
	case msg := <-missing:
		t.Fatalf("panel disappeared during atomic swap: %s", msg)
	default:
	}

	// 2. Fallback when atomic swap returns ENOSYS.
	origSwap := swapDirsFn
	swapDirsFn = func(string, string) error { return os.ErrInvalid }
	t.Cleanup(func() { swapDirsFn = origSwap })
	v3 := Source{Panel: panelFS("v3 main", ""), Layout: []byte(`{"v":2}`), Version: "v1.2.0"}
	if res, err := Sync(home, v3); err != nil || !res.Updated {
		t.Fatalf("fallback sync: %+v, %v", res, err)
	}
	if got := read(t, filepath.Join(dir, "main.js")); got != "v3 main" {
		t.Fatalf("after fallback: %q", got)
	}
}

func TestInstallPanelDropsOldMarkerFilesAndRefusesSymlink(t *testing.T) {
	home, _ := installOld(t)
	dir := filepath.Join(home, "mods", "sheepdog")
	if err := InstallPanel(home, panelFS("installed v2", "")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(dir, "main.js")); got != "installed v2" {
		t.Fatalf("main.js = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "model", "gone.js")); !os.IsNotExist(err) {
		t.Fatalf("dropped file survived InstallPanel: %v", err)
	}
	if got := read(t, filepath.Join(dir, "gpui-kit.d.ts")); got != "herdr-app's" {
		t.Fatalf("extra file lost: %q", got)
	}

	// Refuses symlinked mods/sheepdog.
	symHome := t.TempDir()
	target := t.TempDir()
	put(t, filepath.Join(target, "main.js"), "dev checkout")
	if err := os.MkdirAll(filepath.Join(symHome, "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(symHome, "mods", "sheepdog")); err != nil {
		t.Fatal(err)
	}
	if err := InstallPanel(symHome, panelFS("clobber", "")); err == nil {
		t.Fatal("expected error installing over symlinked mods/sheepdog")
	}
	if got := read(t, filepath.Join(target, "main.js")); got != "dev checkout" {
		t.Fatalf("symlink target modified: %q", got)
	}
}

func TestKeybindingAndCleanUninstall(t *testing.T) {
	home, src := installOld(t)
	put(t, filepath.Join(home, "shared.json"), `{"layout":"sheepdog","sheepdog.url":"http://127.0.0.1:7777"}`)
	if err := RecordPreviousLayout(home, "work"); err != nil {
		t.Fatal(err)
	}

	// Existing keys.json with another binding on a different key and a conflicting binding test.
	put(t, filepath.Join(home, "keys.json"), "{\n  \"global\": {\n    \"cmd-t\": \"tab.new\",\n    \"cmd-shift-s\": \"app.palette\"\n  },\n  \"terminal\": {\n    \"cmd-c\": \"terminal.copy\"\n  }\n}\n")
	ks, err := InspectKeybinding(home, "cmd-shift-s")
	if err != nil || !ks.FileExists || !ks.Bound || ks.Current != "app.palette" {
		t.Fatalf("inspect conflict: %+v, %v", ks, err)
	}
	if err := InstallKeybinding(home, "cmd-shift-s"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(home, "keys.json.bak")); !strings.Contains(got, "app.palette") {
		t.Fatalf("backup missing previous binding: %q", got)
	}
	ks, err = InspectKeybinding(home, "cmd-shift-s")
	if err != nil || !ks.AlreadyOurs {
		t.Fatalf("inspect after install: %+v, %v", ks, err)
	}

	// Marker metadata survives Sync!
	next := newer()
	if res, err := Sync(home, next); err != nil || !res.Updated {
		t.Fatalf("sync after keybinding: %+v, %v", res, err)
	}
	put(t, filepath.Join(home, "layouts", "sheepdog.json.new"), "leftover")

	// Dry run touches nothing.
	plan, err := Uninstall(home, next, true)
	if err != nil || len(plan.Actions) == 0 {
		t.Fatalf("dry-run uninstall: %+v, %v", plan, err)
	}
	if _, err := os.Stat(filepath.Join(home, "mods", "sheepdog", "main.js")); err != nil {
		t.Fatalf("dry-run deleted panel: %v", err)
	}

	// Real uninstall removes panel, layout, .new, restores keys.json to app.palette, and switches shared.json back to work.
	plan, err = Uninstall(home, next, false)
	if err != nil || plan.RestoredLayout != "work" {
		t.Fatalf("uninstall: %+v, %v", plan, err)
	}
	if _, err := os.Stat(filepath.Join(home, "mods", "sheepdog")); !os.IsNotExist(err) {
		t.Fatalf("mods/sheepdog still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "layouts", "sheepdog.json")); !os.IsNotExist(err) {
		t.Fatalf("layouts/sheepdog.json still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "layouts", "sheepdog.json.new")); !os.IsNotExist(err) {
		t.Fatalf("layouts/sheepdog.json.new still exists: %v", err)
	}
	keysAfter := read(t, filepath.Join(home, "keys.json"))
	if !strings.Contains(keysAfter, `"app.palette"`) || !strings.Contains(keysAfter, `"terminal.copy"`) || strings.Contains(keysAfter, "sheepdog.focus") {
		t.Fatalf("keys.json not restored cleanly:\n%s", keysAfter)
	}
	if _, err := os.Stat(filepath.Join(home, "keys.json.bak")); !os.IsNotExist(err) {
		t.Fatalf("keys.json.bak left behind after restoring binding: %v", err)
	}
	sharedAfter := read(t, filepath.Join(home, "shared.json"))
	if !strings.Contains(sharedAfter, `"layout":"work"`) && !strings.Contains(sharedAfter, `"layout": "work"`) {
		t.Fatalf("shared.json layout not restored:\n%s", sharedAfter)
	}

	// Also verify keys.json.bak is removed when no prior binding on that key existed in a pre-existing keys.json.
	home2, _ := installOld(t)
	put(t, filepath.Join(home2, "keys.json"), `{"global":{"cmd-t":"tab.new"}}`)
	if err := InstallKeybinding(home2, "cmd-shift-s"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home2, "keys.json.bak")); err != nil {
		t.Fatalf("expected keys.json.bak before uninstall: %v", err)
	}
	if _, err := Uninstall(home2, src, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home2, "keys.json.bak")); !os.IsNotExist(err) {
		t.Fatalf("keys.json.bak left behind after removing added binding: %v", err)
	}

	// InstallKeybinding refuses to mutate keys.json without an install marker.
	noMarkerHome := t.TempDir()
	put(t, filepath.Join(noMarkerHome, "keys.json"), `{"global":{"cmd-t":"tab.new"}}`)
	if err := InstallKeybinding(noMarkerHome, "cmd-shift-s"); err == nil {
		t.Fatal("expected error from InstallKeybinding without an install marker")
	}
	if got := read(t, filepath.Join(noMarkerHome, "keys.json")); strings.Contains(got, "sheepdog.focus") {
		t.Fatalf("InstallKeybinding mutated keys.json without marker: %s", got)
	}
	_ = src
}

func TestUninstallPreservesUserEditsAndOtherShownLayout(t *testing.T) {
	home, src := installOld(t)
	put(t, filepath.Join(home, "mods", "sheepdog", "main.js"), "user custom panel")
	put(t, filepath.Join(home, "layouts", "sheepdog.json"), `{"custom_structure":true}`)
	put(t, filepath.Join(home, "shared.json"), `{"layout":"terminals"}`)
	if err := InstallKeybinding(home, "cmd-shift-s"); err != nil {
		t.Fatal(err)
	}
	// User later rebounds cmd-shift-s to something else.
	put(t, filepath.Join(home, "keys.json"), `{"global":{"cmd-shift-s":"space.new"}}`)

	plan, err := Uninstall(home, src, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RestoredLayout != "" {
		t.Fatalf("switched away from terminals layout: %+v", plan)
	}
	if got := read(t, filepath.Join(home, "mods", "sheepdog", "main.js")); got != "user custom panel" {
		t.Fatalf("modified panel lost: %q", got)
	}
	if got := read(t, filepath.Join(home, "layouts", "sheepdog.json")); got != `{"custom_structure":true}` {
		t.Fatalf("modified layout lost: %q", got)
	}
	if got := read(t, filepath.Join(home, "keys.json")); !strings.Contains(got, `"space.new"`) {
		t.Fatalf("user's rebound key overwritten: %q", got)
	}
	if got := read(t, filepath.Join(home, "shared.json")); !strings.Contains(got, `"terminals"`) {
		t.Fatalf("shared.json changed when not on sheepdog: %q", got)
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
