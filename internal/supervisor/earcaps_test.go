package supervisor

import (
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
)

// fourGlitches is a detail reply of four sentences, 41 words.
const fourGlitches = "It listed seven glitches, starting with the mic button that stays red after a call. The listening banner flickers when it reconnects. The haptic buzz fires twice on a push. Hold-to-talk drops your first word, and three smaller ones follow."

// One high runaway cap for every spoken line (bench #13): a reply to the
// shepherd's own words and a finish announcement are both heard whole
// unless they run away; detail is never cut, only scrubbed; what a
// runaway cut moves to the end of detail.
func TestUtteranceAndAnnounceShareTheRunawayCap(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolReadScreen, map[string]any{"agent": "auth"})}},
		braintest.Step{Text: braintest.Reply(fourGlitches, "")},
	)
	m.EventDefault = announceReply(fourGlitches, "idle", "", "", false)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)

	res := h.say("what were the seven glitches?")
	if res.Spoken != fourGlitches {
		t.Errorf("utterance reply cut:\n got  %q\n want %q", res.Spoken, fourGlitches)
	}

	h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
	h.sup.touch("w5:p1")
	doneAuth(h)
	pushes := h.pushTexts()
	if len(pushes) != 1 || pushes[0] != fourGlitches {
		t.Fatalf("announce line = %q, want it whole: %q", pushes, fourGlitches)
	}

	// detail: scrubbed, never cut, plus whatever the runaway cap moved there.
	detail := strings.TrimSpace(strings.Repeat("One plain sentence about a term. ", 8))
	runaway := strings.TrimSpace(strings.Repeat(strings.Repeat("word ", 9)+"end. ", 14))
	for name, caps := range map[string]prose.EarCaps{"reply": prose.EarReply, "brief": prose.EarBrief} {
		got := toolResultFrom(brain.Reply{Spoken: runaway, Detail: detail + " See /a/b/c.go."}, caps)
		if n := len(strings.Fields(got.Spoken)); n > prose.EarWords || n < 90 || !strings.HasPrefix(got.Detail, detail+" See a file ") || !strings.HasSuffix(got.Detail, "end.") {
			t.Errorf("%s: toolResultFrom = %q / %q", name, got.Spoken, got.Detail)
		}
	}
}
