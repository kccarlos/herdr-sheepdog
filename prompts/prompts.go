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

// VoiceSystem is the file name of the voice model's system prompt.
const VoiceSystem = "voice_system.txt"

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
