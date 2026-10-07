package supervisor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// focusLines are the current-focus lines pushed so far, with their
// urgency checked: every one is silent and tagged voice.TagFocus.
func (h *harness) focusLines() []string {
	h.t.Helper()
	var out []string
	for i, p := range h.pusher.all() {
		if !voice.IsFocus(h.pusher.tagsOf(i)) {
			continue
		}
		if p.Urgency != voice.UrgencySilent {
			h.t.Fatalf("focus line pushed at %s: %q", p.Urgency, p.Text)
		}
		out = append(out, p.Text)
	}
	return out
}

func replyJSON(r brain.Reply) string {
	b, _ := json.Marshal(r)
	return string(b)
}

// After an orchestrator reply the voice gets one silent line with the agent in
// focus (its spoken name), the open question with its options and the
// answer's key points — once its turn is over, and only when the line
// changed.
func TestFocusLineAfterReplyOncePerChange(t *testing.T) {
	auth := brain.Reply{Spoken: "auth ran the tests: 3 of 5 pass.", Ask: "Should auth retry the two failing ones?", Detail: "Two tests fail on the login check. The rest are fine.", Options: []string{"Retry", "Leave them"}}
	docs := brain.Reply{Spoken: "docs finished the README.", Detail: "It added an install section."}
	m := braintest.NewModel(
		braintest.Step{Text: replyJSON(auth)},
		braintest.Step{Text: replyJSON(auth)},
		braintest.Step{Text: replyJSON(docs)},
	)
	h := newBrainHarness(t, m, replyWait(5*time.Second))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)

	if res := h.say("how are the tests going"); !strings.Contains(res.Spoken, "3 of 5 pass") {
		t.Fatalf("result = %+v", res)
	}
	if got := h.focusLines(); len(got) != 0 {
		t.Fatalf("focus line before the voice spoke the answer: %q", got)
	}
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	lines := h.focusLines()
	if len(lines) != 1 {
		t.Fatalf("focus lines after the answer = %q, want one", lines)
	}
	for _, want := range []string{"Agent in focus: auth.", "Open question you asked: Should auth retry the two failing ones? Options: Retry; Leave them.", "(1) auth ran the tests: 3 of 5 pass.", "(2) Two tests fail on the login check."} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("focus line lacks %q:\n%s", want, lines[0])
		}
	}
	if len([]rune(lines[0])) > focusMaxRunes+1 {
		t.Errorf("focus line is %d runes", len([]rune(lines[0])))
	}
	if h.log.count("supervisor.focus") != 1 {
		t.Errorf("supervisor.focus logged %d times", h.log.count("supervisor.focus"))
	}

	// The same answer again: nothing new to say.
	h.say("and the tests?")
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	if got := h.focusLines(); len(got) != 1 {
		t.Fatalf("an unchanged focus was sent again: %q", got)
	}

	// Another agent: a new line, ready for "it" to mean docs. A barge-in
	// ends the voice's turn just as well.
	h.say("what about docs")
	h.sup.OnVoiceEvent(voice.EventInterrupted{})
	lines = h.focusLines()
	if len(lines) != 2 || !strings.Contains(lines[1], "Agent in focus: docs.") || strings.Contains(lines[1], "Open question you asked") || !strings.Contains(lines[1], "install section") {
		t.Fatalf("focus lines after the docs answer = %q", lines)
	}
	// Turn ends with no new reply push nothing.
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	if got := h.focusLines(); len(got) != 2 {
		t.Errorf("a turn with no new reply pushed a focus line: %q", got)
	}

	// "New conversation" forgets the focus: the same answer afterwards is
	// news to the fresh conversation.
	if _, _, err := h.sup.ResetConversation(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	h.sup.noteFocus(focusFromReply("w6:p1", docs.Spoken, docs), "")
	h.sup.OnVoiceEvent(voice.EventTurnComplete{})
	if got := h.focusLines(); len(got) != 3 || got[2] != lines[1] {
		t.Errorf("focus after a reset = %q", got)
	}
}

// The worker's open approval prompt is a fact of the focus too: its
// question and answer options, never the free-text entry.
func TestFocusLineNamesTheOpenPrompt(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	p, ok := h.pending()
	if !ok {
		t.Fatal("no pending prompt")
	}
	h.sup.noteFocus(focusFacts{paneID: "w5:p1", spoken: "auth wants to run a command."}, "")
	h.sup.flushFocus()
	lines := h.focusLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "auth is waiting for an answer:") || !strings.Contains(lines[0], "Options: "+p.Options[0].Label) {
		t.Fatalf("focus lines = %q (pending %+v)", lines, p)
	}
}

// A long Chinese answer stays inside the bound (and GPT-Live's
// 1600-byte append limit).
func TestFocusLineBounded(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	long := strings.Repeat("测试还是失败了，结果是三，应该是五。", 40)
	h.sup.noteFocus(focusFacts{paneID: "w5:p1", spoken: long, detail: long, ask: "要重试吗？", options: []string{"重试", "先不要"}}, "")
	h.sup.flushFocus()
	lines := h.focusLines()
	if len(lines) != 1 || len([]rune(lines[0])) > focusMaxRunes+1 || len(lines[0]) > 1600 {
		t.Fatalf("focus line = %d runes, %d bytes", len([]rune(lines[0])), len(lines[0]))
	}
	if !strings.Contains(lines[0], "要重试吗？ Options: 重试; 先不要.") {
		t.Errorf("focus line lacks the question: %s", lines[0])
	}
}
