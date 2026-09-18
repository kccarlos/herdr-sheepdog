package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestTemplateIsValidTOML proves the config init template parses (every
// content line is a comment or a bare [section] header) and, since every
// value line is commented out, decoding it on top of Default() changes
// nothing.
func TestTemplateIsValidTOML(t *testing.T) {
	tmpl := Template()
	got := Default()
	if _, err := toml.Decode(tmpl, &got); err != nil {
		t.Fatalf("Template() is not valid TOML: %v", err)
	}
	def := Default()
	if got.DataDir != def.DataDir || got.DebounceMS != def.DebounceMS || got.Web.Port != def.Web.Port || got.CLI.Prefer != def.CLI.Prefer || got.State.FullFsync != def.State.FullFsync {
		t.Errorf("an all-commented template should decode to nothing, got %+v want %+v", got, def)
	}
}

// TestTemplateMentionsEveryKey proves every documented config key (see
// CONFIG.md and its own completeness test) also appears in the config
// init template, so a fresh install has every knob visible in one file.
func TestTemplateMentionsEveryKey(t *testing.T) {
	tmpl := Template()
	for _, k := range tomlKeys(configType, "") {
		leaf := k
		if i := strings.LastIndexByte(k, '.'); i >= 0 {
			leaf = k[i+1:]
		}
		if !strings.Contains(tmpl, leaf) {
			t.Errorf("Template() does not mention key %q (leaf %q)", k, leaf)
		}
	}
}
