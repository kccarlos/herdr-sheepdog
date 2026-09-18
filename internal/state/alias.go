package state

import (
	"strconv"
	"strings"
	"unicode"
)

// Slug turns free text into a spoken-friendly alias: lowercase ASCII
// letters, digits and single dashes, no leading/trailing dash. Non-ASCII
// letters are dropped. An input with nothing usable yields "".
func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// deriveAlias builds the default alias for an agent: the workspace label
// followed by the agent name (when the user named it) or its kind, e.g.
// "auth-service-codex". Empty parts are skipped; with nothing at all it
// falls back to "agent".
func deriveAlias(workspaceLabel, name, kind string) string {
	parts := []string{}
	if p := Slug(workspaceLabel); p != "" {
		parts = append(parts, p)
	}
	tail := name
	if tail == "" {
		tail = kind
	}
	if p := Slug(tail); p != "" {
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return "agent"
	}
	return strings.Join(parts, "-")
}

// uniqueAlias appends -2, -3, … until alias is not taken by another pane.
func uniqueAlias(alias string, taken func(string) bool) string {
	if !taken(alias) {
		return alias
	}
	for i := 2; ; i++ {
		candidate := alias + "-" + strconv.Itoa(i)
		if !taken(candidate) {
			return candidate
		}
	}
}
