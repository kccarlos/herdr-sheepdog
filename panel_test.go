package herdrsheepdog

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

var (
	jsFromImport = regexp.MustCompile(`(?m)\bfrom\s+["']([^"']+)["']`)
	jsBareImport = regexp.MustCompile(`(?m)^\s*import\s+["']([^"']+)["']`)
)

// TestPanelEmbedsTransitiveImports catches a new model, net or ui module
// that main.js reaches but the embed patterns leave out.
func TestPanelEmbedsTransitiveImports(t *testing.T) {
	seen := map[string]bool{}
	queue := []string{"mods/sheepdog/main.js"}
	for len(queue) > 0 {
		file := queue[0]
		queue = queue[1:]
		if seen[file] {
			continue
		}
		seen[file] = true
		data, err := fs.ReadFile(Panel, file)
		if err != nil {
			t.Fatalf("main.js imports %s, but it is not embedded: %v", file, err)
		}
		for _, re := range []*regexp.Regexp{jsFromImport, jsBareImport} {
			for _, match := range re.FindAllStringSubmatch(string(data), -1) {
				spec := match[1]
				if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
					continue
				}
				queue = append(queue, path.Clean(path.Join(path.Dir(file), spec)))
			}
		}
	}
	if len(seen) < 2 {
		t.Fatal("main.js had no local imports; the checker did not inspect the panel")
	}
}

func TestPanelLeavesOutTestsAndDocs(t *testing.T) {
	err := fs.WalkDir(Panel, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".test.mjs") || strings.HasSuffix(p, "/README.md") || p == "mods/sheepdog/.gitignore" {
			t.Errorf("runtime panel unexpectedly embeds %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"mods/sheepdog/main.js", "mods/sheepdog/mod.json", "mods/sheepdog/logo.svg"} {
		if _, err := fs.Stat(Panel, p); err != nil {
			t.Errorf("runtime asset %s is not embedded: %v", p, err)
		}
	}
}

func TestPanelHistoryApplyNewestUpperBound(t *testing.T) {
	historyJS, err := fs.ReadFile(Panel, "mods/sheepdog/model/history.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(historyJS), "Math.max(items[items.length - 1]?.id ?? -Infinity, requestedAt)") {
		t.Fatalf("history.js applyNewest does not bound upper with requestedAt when page.items is non-empty")
	}
	storeJS, err := fs.ReadFile(Panel, "mods/sheepdog/model/store.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(storeJS), "Math.max(now() * 1000,") {
		t.Fatalf("store.js round does not bound requestedAt by the highest observed event ID")
	}
}

func TestPanelWindowTimerRefreshesOutboxLease(t *testing.T) {
	mainJS, err := fs.ReadFile(Panel, "mods/sheepdog/main.js")
	if err != nil {
		t.Fatal(err)
	}
	timerRe := regexp.MustCompile(`(?s)cx\.timer\.every\(250,\s*\(cx\)\s*=>\s*\{(.*?)\n\s*\}\);`)
	m := timerRe.FindStringSubmatch(string(mainJS))
	if m == nil {
		t.Fatal("main.js has no 250ms timer block")
	}
	body := m[1]
	if !strings.Contains(body, "setShared(PANEL_WINDOW_KEY, Date.now())") {
		t.Fatalf("main.js 250ms timer does not refresh PANEL_WINDOW_KEY:\n%s", body)
	}
	if !strings.Contains(body, "this.syncOutbox()") {
		t.Fatalf("main.js 250ms timer does not call this.syncOutbox():\n%s", body)
	}
}
