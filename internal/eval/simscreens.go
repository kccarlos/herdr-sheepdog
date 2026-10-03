package eval

import (
	"strings"
)

// The simulated worlds (tier 1 here, tier 2 in internal/bench/world) redraw
// a worker's screen after sheepdog's keys the way the real CLIs do.

// inputRow is the CLI's own input line, by kind.
func inputRow(kind string) string {
	if kind == "claude" {
		return "❯ "
	}
	return "› Ask Codex to do anything"
}

// ScreenAfterInterrupt is a working worker's screen once a Ctrl-C stopped
// its turn: the running-turn line ("… esc to interrupt") gone, the CLI's
// interrupted line, and its input line.
func ScreenAfterInterrupt(screen, kind string) string {
	var kept []string
	for _, l := range strings.Split(strings.TrimRight(screen, "\n"), "\n") {
		if !strings.Contains(strings.ToLower(l), "to interrupt") {
			kept = append(kept, l)
		}
	}
	line := "■ Conversation interrupted - tell the model what to do differently."
	if kind == "claude" {
		line = "  ⎿  Interrupted · What should Claude do instead?"
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\n") + "\n" + line + "\n\n" + inputRow(kind) + "\n"
}
