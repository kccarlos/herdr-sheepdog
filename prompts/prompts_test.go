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
	if _, err := Render("missing.txt", nil); err == nil {
		t.Error("missing template accepted")
	}
}
