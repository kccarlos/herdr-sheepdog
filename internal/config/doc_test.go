package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

// configType is Config's reflect.Type, shared by the CONFIG.md
// completeness test and the config-init template test.
var configType = reflect.TypeOf(Config{})

// reBacktick matches a backticked `key.name` token in CONFIG.md.
var reBacktick = regexp.MustCompile("`([a-zA-Z0-9_.]+)`")

// tomlKeys walks t's fields depth-first, returning every toml-tagged leaf
// key, dotted with its parent's tag for nested structs (e.g.
// "cli.prefer" for CLI.Prefer nested under Config.CLI). A struct-typed
// field is treated as a section to recurse into, never a leaf itself;
// Config has no other kind of nesting.
func tomlKeys(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("toml")
		if tag == "" || tag == "-" {
			continue
		}
		name := tag
		if prefix != "" {
			name = prefix + "." + tag
		}
		if f.Type.Kind() == reflect.Struct {
			out = append(out, tomlKeys(f.Type, name)...)
			continue
		}
		out = append(out, name)
	}
	return out
}

// TestConfigMDDocumentsEveryKey fails if a Config struct field with a
// toml tag is missing from CONFIG.md (as a backticked `key.name`), so the
// doc cannot silently drift from the code (WP7a acceptance).
func TestConfigMDDocumentsEveryKey(t *testing.T) {
	keys := tomlKeys(configType, "")
	if len(keys) == 0 {
		t.Fatal("tomlKeys found nothing; the reflection walk itself is broken")
	}
	path := filepath.Join("..", "..", "CONFIG.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	doc := string(data)
	backticked := map[string]bool{}
	for _, m := range reBacktick.FindAllStringSubmatch(doc, -1) {
		backticked[m[1]] = true
	}
	for _, k := range keys {
		if !backticked[k] {
			t.Errorf("CONFIG.md is missing key %q (add a `%s` entry to its table)", k, k)
		}
	}
}
