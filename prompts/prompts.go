// Package prompts embeds the prompt templates shipped in the binary (plan
// §7, §8). Templates use {name} placeholders; Render substitutes them.
package prompts

import (
	"embed"
	"fmt"
	"strings"
)

// FS holds every *.txt template.
//
//go:embed *.txt
var FS embed.FS

// SVP holds the supervisor brain's prompt files (docs/roles.md: the
// SOUL/RULES split borrowed from OpenClaw). SOUL.md is the role; RULES.md
// holds the tool guidance with the provenance rule, the one rail and the
// answer contract, one "## " section each. internal/brain assembles them
// into the stable head of its system prompt.
//
//go:embed svp/*.md
var SVP embed.FS

// VoiceSystem is the file name of the voice model's system prompt.
const VoiceSystem = "voice_system.txt"

// SVP prompt file names under svp/.
const (
	SVPSoul  = "svp/SOUL.md"
	SVPRules = "svp/RULES.md"
)

// Render loads the named template and replaces each {key} with vars[key].
// Unknown placeholders are left as-is so a typo is visible in the output.
func Render(name string, vars map[string]string) (string, error) {
	b, err := FS.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("prompts: %w", err)
	}
	s := string(b)
	for k, v := range vars {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return strings.TrimSpace(s), nil
}
