package supervisor

import (
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Whether words decline, put off or question a prompt is the brain's
// judgment (dual-brain ablation G4): the rail no longer reads deferral,
// question or filler words. It still needs fresh words of the shepherd's
// own, and a "no" still never approves.
func TestRailLeavesTheReadingToTheBrain(t *testing.T) {
	cases := []struct {
		name  string
		heard string
		keys  string
		sent  bool
		why   string
	}{
		{"a decline the brain chose", "No, don't run that yet. What does bench-codex want?", "esc", true, ""},
		{"a no never approves", "No.", "y", false, "cannot approve"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := braintest.NewModel(
				braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{tc.keys}})}},
				braintest.Step{Text: braintest.Reply("Done.", "")},
			)
			h := newBrainHarness(t, m, func(c *Config) { c.bareAnswers = false })
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			h.block("w5:p1", "codex-approval-prompt.screen.txt")
			h.say(tc.heard)
			sent := len(h.sendKeysRequests()) == 1
			if sent != tc.sent {
				t.Fatalf("keys sent = %v (%v), want %v", sent, h.sendKeysRequests(), tc.sent)
			}
			if tc.sent {
				return
			}
			ev := h.log.ofType("brain.rail_blocked")
			if len(ev) != 1 || !strings.Contains(payload(ev[0])["reason"].(string), tc.why) {
				t.Errorf("rail_blocked = %v", ev)
			}
		})
	}
}

// A bare "no" is no longer answered in code: the brain gets the words
// (the bare-answer fast path was removed in ablation G4).
func TestBareAnswerGoesToTheBrain(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("Which one do you mean?", "")})
	h := newBrainHarness(t, m, func(c *Config) { c.bareAnswers = false })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.say("No.")
	if n := h.log.count("supervisor.fast_path"); n != 0 {
		t.Errorf("fast_path = %d", n)
	}
	if len(m.Calls()) == 0 {
		t.Error("the brain never saw the words")
	}
}
