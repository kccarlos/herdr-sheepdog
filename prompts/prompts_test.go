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
	if _, err := Render("missing.txt", nil); err == nil {
		t.Error("missing template accepted")
	}
}
