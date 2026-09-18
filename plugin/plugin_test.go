// Package plugin holds no Go code (the herdr plugin shim is POSIX sh
// plus a launchd plist template); this file is regression coverage for
// those files, runnable portably via `go test ./...` (unlike plutil
// -lint, which is macOS-only and so belongs in the manual acceptance
// checklist, not CI).
package plugin

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// manifest mirrors the herdr-plugin.toml shape (docs/herdr-protocol.md
// §8) closely enough to catch a malformed manifest.
type manifest struct {
	ID              string `toml:"id"`
	Name            string `toml:"name"`
	Version         string `toml:"version"`
	MinHerdrVersion string `toml:"min_herdr_version"`
	Events          []struct {
		On      string   `toml:"on"`
		Command []string `toml:"command"`
	} `toml:"events"`
}

// TestShellScriptsParse runs `sh -n` on every .sh file: syntax-only, no
// execution, safe in any environment (see also the manual SHEEPDOG_DRY_RUN
// acceptance run, which additionally proves the dry-run path never
// shells out to herdr/launchctl for real).
func TestShellScriptsParse(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	for _, f := range []string{"lib.sh", "install.sh", "uninstall.sh", "hooks/forward-event.sh"} {
		f := f
		t.Run(f, func(t *testing.T) {
			out, err := exec.Command(sh, "-n", f).CombinedOutput()
			if err != nil {
				t.Errorf("sh -n %s: %v\n%s", f, err, out)
			}
		})
	}
}

// TestManifestShape proves herdr-plugin.toml parses, has the mandatory
// min_herdr_version, and every [[events]] hook's command points at a
// script that exists relative to the plugin directory.
func TestManifestShape(t *testing.T) {
	data, err := os.ReadFile("herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if _, err := toml.Decode(string(data), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m.ID == "" || m.MinHerdrVersion == "" {
		t.Fatalf("id and min_herdr_version are mandatory: %+v", m)
	}
	if len(m.Events) == 0 {
		t.Fatal("no [[events]] hooks declared")
	}
	for _, ev := range m.Events {
		if ev.On == "" || len(ev.Command) == 0 {
			t.Errorf("event with empty on/command: %+v", ev)
			continue
		}
		script := ev.Command[len(ev.Command)-1]
		if _, err := os.Stat(script); err != nil {
			t.Errorf("event %q command %v: script missing: %v", ev.On, ev.Command, err)
		}
	}
}

// TestLaunchdTemplateRendersValidXML substitutes the three placeholders
// and proves the result is well-formed XML with the values in place
// (plutil -lint, exercised manually, additionally checks it is a valid
// plist specifically; that tool is macOS-only, so it does not run here).
func TestLaunchdTemplateRendersValidXML(t *testing.T) {
	tmplPath := filepath.Join("launchd", "dev.sheepdog.daemon.plist.tmpl")
	data, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := string(data)
	for _, ph := range []string{"__BINARY__", "__LOG_DIR__", "__PORT__"} {
		if !strings.Contains(tmpl, ph) {
			t.Errorf("template missing placeholder %s", ph)
		}
	}
	rendered := strings.NewReplacer(
		"__BINARY__", "/opt/sheepdog/bin/sheepdog",
		"__LOG_DIR__", "/opt/sheepdog/logs",
		"__PORT__", "7777",
	).Replace(tmpl)
	var doc struct {
		XMLName xml.Name `xml:"plist"`
	}
	if err := xml.Unmarshal([]byte(rendered), &doc); err != nil {
		t.Fatalf("rendered plist is not well-formed XML: %v", err)
	}
	for _, want := range []string{"/opt/sheepdog/bin/sheepdog", "/opt/sheepdog/logs/stdout.log", "<string>7777</string>"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered plist missing %q", want)
		}
	}
	if strings.Contains(rendered, "__") {
		t.Error("a placeholder was left unsubstituted")
	}
}
