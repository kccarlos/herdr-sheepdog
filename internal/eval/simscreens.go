package eval

import (
	"regexp"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// The simulated worlds (tier 1 here, tier 2 in internal/bench/world) redraw
// a worker's screen after sheepdog's keys the way the real CLIs do.

// optionRow is one row of a numbered menu, with or without the cursor.
var optionRow = regexp.MustCompile(`^\s*([›❯>]\s*)?\d+\.\s`)

// inputRow is the CLI's own input line, by kind.
func inputRow(kind string) string {
	if kind == "claude" {
		return "❯ "
	}
	return "› Ask Codex to do anything"
}

// withoutPrompt is screen with the approval menu at its end taken away:
// a real CLI removes the overlay once it is answered
// (testdata/herdr/codex-declined-esc.screen.txt). Campaign D N6: the sims
// kept the numbered menu above the new input line, so the menu guard
// still saw it and refused the user's follow-up instruction.
func withoutPrompt(screen string) string {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	for len(lines) > 0 {
		if _, ok := herdr.ParsePrompt(strings.Join(lines, "\n")); !ok {
			break
		}
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 {
		last := lines[len(lines)-1]
		if strings.TrimSpace(last) != "" && !optionRow.MatchString(last) {
			break
		}
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// ScreenAfterAnswer is a blocked worker's screen once keys answered its
// prompt: the menu gone, the outcome line, and — after a decline — its
// input line.
func ScreenAfterAnswer(screen, kind string, declined bool) string {
	base := withoutPrompt(screen)
	if declined {
		return base + "\n\n✗ You canceled the request.\n\n• Okay, I won't do that.\n\n" + inputRow(kind) + "\n"
	}
	return base + "\n\n• Running the approved step…\n\n" + inputRow(kind) + "\n"
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
