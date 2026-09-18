package prompts

import (
	"strings"
	"testing"
)

func TestRenderVoiceSystem(t *testing.T) {
	s, err := Render(VoiceSystem, map[string]string{"aliases": "auth, docs"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "aliases: auth, docs.") || strings.Contains(s, "{aliases}") {
		t.Errorf("aliases not substituted: %q", s)
	}
	for _, tool := range []string{"briefing", "ask", "propose", "confirm", "cancel", "note"} {
		if !strings.Contains(s, tool) {
			t.Errorf("prompt does not mention %s", tool)
		}
	}
	// The push prefixes of internal/voice/gemini (pushTurn) must be
	// explained to the model: system events are spoken, context is not.
	for _, prefix := range []string{"[system event, not spoken by the user]", "[system context, not spoken by the user]"} {
		if !strings.Contains(s, prefix) {
			t.Errorf("prompt does not explain the push prefix %q", prefix)
		}
	}
	// Rules from the headset sessions (cheap guard against the prompt
	// drifting): status questions always go through a tool, propose / ask
	// never guess the agent, questions for the dog are ask (never sent to
	// the worker as propose), and only the latest tool result may be
	// reused for a follow-up.
	for _, rule := range []string{
		"MUST start with a tool call",
		"Never answer such a question from memory",
		"if a tool has not answered yet, say you are still waiting",
		"fill agent only when the user actually said an alias",
		"leave agent empty",
		"Do not guess the agent",
		`"ask you to explain …"`,
		`"what did it say"`,
		`"what does that mean"`,
		`"explain …"`,
		"only when the user tells an agent to DO something",
		"never a question for you",
		"If unsure whether it is a question or an instruction, use ask",
		"most recent tool result",
		"anything older needs a new call",
	} {
		if !strings.Contains(s, rule) {
			t.Errorf("prompt lost the rule %q", rule)
		}
	}
	if words := len(strings.Fields(s)); words > 350 {
		t.Errorf("prompt is %d words; keep it short (<= 350)", words)
	}
	if _, err := Render("missing.txt", nil); err == nil {
		t.Error("missing template accepted")
	}
}
