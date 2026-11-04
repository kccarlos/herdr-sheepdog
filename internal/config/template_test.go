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
// CONFIG.md and its own completeness test) also appears in its own
// [section] of the config init template, so a fresh install has every
// knob visible in one file.
func TestTemplateMentionsEveryKey(t *testing.T) {
	sections := map[string]string{}
	cur := ""
	for _, line := range strings.Split(Template(), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			cur = strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			continue
		}
		sections[cur] += line + "\n"
	}
	for _, k := range tomlKeys(configType, "") {
		sec, leaf := "", k
		if i := strings.LastIndexByte(k, '.'); i >= 0 {
			sec, leaf = k[:i], k[i+1:]
		}
		block, ok := sections[sec]
		if !ok {
			t.Errorf("Template() is missing section [%s] for key %q", sec, k)
			continue
		}
		if !strings.Contains(block, leaf) {
			t.Errorf("Template() section [%s] does not mention key %q (leaf %q)", sec, k, leaf)
		}
	}
}
