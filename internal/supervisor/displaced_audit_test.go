package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The displacement rail's second review (displaced.go): what the user was
// told is kept per question, open or recently closed, and the rule is read
// when the words are checked, not when a question was installed.

const shopPane = "w7:p1"

// askedAgain reports whether a spoken push since push index from re-asks
// pane's question (displaced.go's reaskLine) and returns it.
func (h *harness) askedAgain(from int, alias string) (string, bool) {
	all := h.pusher.all()
	for i := from; i < len(all); i++ {
		p := all[i]
		tags := h.pusher.tagsOf(i)
		if p.Urgency != voice.UrgencySilent && tags["kind"] == "re-ask of a question the user had not heard" && strings.Contains(p.Text, alias) {
			return p.Text, true
		}
	}
	return "", false
}

// Review item 1: a question the user was told about, which then closed by
// itself (its pane left blocked), is a new question when its pane asks the
// same thing again. The old telling does not count for it.
func TestSamePromptAgainAfterItsPaneLeftBlockedIsNotTold(t *testing.T) {
	h := toldAuth(t)
	h.srv.SetScreen(authPane, "running the tests")
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)
	h.clock.Advance(time.Second)
	h.speak(blockDocs(t, h))
	h.clock.Advance(time.Second)
	// auth asks the same thing again; its announcement is not out yet.
	h.block(authPane, "codex-approval-prompt.screen.txt")
	if p, _ := h.pending(); p.PaneID != authPane {
		t.Fatalf("pending = %+v, want auth's new question", p)
	}
	h.clock.Advance(300 * time.Millisecond)
	h.say("Yes.")
	if n := h.keysTo(authPane); n != 0 {
		t.Fatalf("a yes said after docs' announcement, before auth's new one, sent auth %d key presses", n)
	}
}

// Review item 2 (quick flip, and likewise after a restart): docs blocks
// before auth's announcement went out, so nothing was told when docs
// became the voice's question. Then only auth's announcement is spoken.
// The user's yes is about auth and must not reach docs. The daemon tells
// them why and asks again, and the next yes answers docs.
func TestQuickFlipBeforeTheFirstAnnouncementKeepsTheYesOffTheSecond(t *testing.T) {
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	authLine := h.lastPushText()
	docsLine := blockDocs(t, h)
	h.speak(authLine)
	h.clock.Advance(time.Second)
	from := len(h.pusher.all())
	res := h.say("Yes.")
	if n := h.keysTo(docsPane); n != 0 {
		t.Fatalf("a yes for auth sent docs %d key presses", n)
	}
	// The user hears why, and docs' question.
	if !strings.Contains(res.Spoken, "did not send that") || !strings.Contains(res.Spoken, "docs") || !strings.Contains(res.Spoken, "printf") {
		t.Fatalf("the refusal said %+v, want why and docs' question", res)
	}
	if _, pushed := h.askedAgain(from, "docs"); pushed {
		t.Fatal("the refusal was said twice (a tool result and a push)")
	}
	h.playText(res.Spoken)
	h.clock.Advance(time.Second)
	h.say("Yes.")
	if n := h.keysTo(docsPane); n != 1 {
		t.Fatalf("keys to docs after it was asked again = %d, want 1", n)
	}
	_ = docsLine
}

// Review item 3 (quick flip of the announcement slot): docs' announcement
// is queued, shop blocks before it goes out, then docs' announcement is
// spoken. It told docs, though docs is no longer the voice's question,
// so the user's answer to docs goes through.
func TestAnnouncementSpokenAfterAnotherBlockStillTellsItsQuestion(t *testing.T) {
	h := toldAuth(t)
	h.addAgent(shopPane, "shop", "codex", herdr.StateWorking)
	docsLine := blockDocs(t, h)
	h.block(shopPane, "codex-approval-prompt.screen.txt")
	if p, _ := h.pending(); p.PaneID != shopPane {
		t.Fatalf("pending = %+v, want shop's question", p)
	}
	h.speak(docsLine)
	h.clock.Advance(time.Second)
	h.hear("Yes, docs can go ahead.")
	q, ok := h.store.Question(firstQuestionOf(h, docsPane))
	if !ok {
		t.Fatal("docs' question is gone")
	}
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, q.YesKeys); err != nil {
		t.Fatalf("send_keys to docs after its announcement was spoken: %v", err)
	}
	if n := h.keysTo(docsPane); n != 1 {
		t.Fatalf("keys to docs = %d, want 1", n)
	}
}

// Review item 3 (refusal): the orchestrator's send_keys refused by the rail
// tells the user why and asks again, once per input, and the answer after
// that goes through.
func TestRefusedSendKeysAsksTheUserAgain(t *testing.T) {
	h := toldAuth(t)
	blockDocs(t, h)
	h.clock.Advance(500 * time.Millisecond)
	h.hear("Yes, go ahead.")
	p, _ := h.pending()
	from := len(h.pusher.all())
	for i := 0; i < 2; i++ {
		_, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, p.YesKeys)
		if err == nil || !strings.Contains(err.Error(), "asked them again") {
			t.Fatalf("send_keys err = %v, want a refusal that says the user was asked again", err)
		}
	}
	line, ok := h.askedAgain(from, "docs")
	if !ok {
		t.Fatalf("no re-ask pushed; pushes = %v", h.pushTexts()[from:])
	}
	n := 0
	for _, text := range h.pushTexts() {
		if text == line {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("re-ask pushed %d times, want once", n)
	}
	h.speak(line)
	h.clock.Advance(time.Second)
	h.hear("Yes.")
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, p.YesKeys); err != nil {
		t.Fatalf("send_keys after the re-ask was spoken: %v", err)
	}
}
