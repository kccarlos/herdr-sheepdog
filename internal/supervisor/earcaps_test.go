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

// fourGlitches is a detail reply of four sentences, 41 words: over the
// brief cap, within the reply cap.
const fourGlitches = "It listed seven glitches, starting with the mic button that stays red after a call. The listening banner flickers when it reconnects. The haptic buzz fires twice on a push. Hold-to-talk drops your first word, and three smaller ones follow."

// A reply to the shepherd's own words is cut at the reply cap (four
// sentences, sixty words), so a detail they asked for is heard whole;
// the same line as a finish announcement keeps the brief cap (two
// sentences, thirty words, #60), and detail is never cut, only scrubbed
// (docs/dive-deep.md WP8b).
func TestUtteranceReplyCapIsWiderThanAnnounce(t *testing.T) {
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
	if len(pushes) != 1 {
		t.Fatalf("pushes = %q", pushes)
	}
	if want := "It listed seven glitches, starting with the mic button that stays red after a call. The listening banner flickers when it reconnects."; pushes[0] != want {
		t.Errorf("announce line = %q, want the brief cap %q", pushes[0], want)
	}

	// detail: scrubbed, never cut, plus whatever the cap moved there.
	detail := strings.TrimSpace(strings.Repeat("One plain sentence about a term. ", 8))
	five := fourGlitches + " Dark mode loses the status colour."
	got := toolResultFrom(brain.Reply{Spoken: five, Detail: detail + " See /a/b/c.go."}, prose.EarReply)
	if got.Spoken != fourGlitches || got.Detail != detail+" See a file Dark mode loses the status colour." {
		t.Errorf("toolResultFrom = %q / %q", got.Spoken, got.Detail)
	}
}
