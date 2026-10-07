package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// docs/eval/eval-campaign-2026-10-03.md H4: after the shepherd spoke Chinese,
// the daemon's own code sentences were English (j2-approve-zh,
// j1-status-zh). The block template and a tapped answer follow the
// language of their last words.
func TestCodeSentencesFollowTheShepherdsLanguage(t *testing.T) {
	h := newHarness(t)
	h.hear("现在什么情况？")
	h.clock.Advance(time.Second)
	blockedAuth(t, h)
	p, _ := h.pending()
	if p.SpokenSummary != "auth 请求运行 printf。" {
		t.Errorf("block template = %q, want the Chinese one", p.SpokenSummary)
	}
	h.clock.Advance(time.Second)
	res, err := h.sup.AnswerTap(context.Background(), p.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Spoken != "已回复 auth：Yes, proceed。" {
		t.Errorf("tap confirmation = %q", res.Spoken)
	}
}

// English stays English, and with nothing said yet the daemon speaks
// English.
func TestCodeSentencesDefaultToEnglish(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	if p, _ := h.pending(); p.SpokenSummary != "auth asks to run printf." {
		t.Errorf("block template = %q", p.SpokenSummary)
	}
	h.clock.Advance(2 * time.Second)
	if res := h.say("Yes."); !strings.HasPrefix(res.Spoken, "Told auth yes") {
		t.Errorf("fast path = %+v", res)
	}
}

func TestToldLine(t *testing.T) {
	cases := []struct {
		label string
		zh    bool
		want  string
	}{
		{"yes", false, "Told auth yes."},
		{"yes", true, "已回复 auth：同意。"},
		{"no", true, "已回复 auth：不同意。"},
		{"MIT", true, "已回复 auth：MIT。"},
	}
	for _, c := range cases {
		if got := toldLine("auth", c.label, c.zh); got != c.want {
			t.Errorf("toldLine(%q, %v) = %q, want %q", c.label, c.zh, got, c.want)
		}
	}
}

// A question displacing another pane's keeps one language and no seam.
func TestReplacedLineLanguage(t *testing.T) {
	if got := replacedLine("auth", "docs 需要你做个决定。", true); got != "先放下 auth 之前的问题。现在 docs 需要你做个决定。" {
		t.Errorf("zh = %q", got)
	}
	if got := replacedLine("auth", "docs is asking do you want to create claude.txt.", false); got != "Dropping the earlier question from auth; now docs is asking do you want to create claude.txt." {
		t.Errorf("en = %q", got)
	}
}

// Run C, vertex, j2-approve-zh r1: the brain's send_keys confirmation was
// English after the shepherd said "可以，让它跑吧。", and when the turn
// timed out after sending, that tool result was what they heard.
func TestBrainToolConfirmationFollowsTheShepherdsLanguage(t *testing.T) {
	h := newHarness(t)
	blockedAuth(t, h)
	h.clock.Advance(time.Second)
	h.sup.ShepherdInput(InputVoice, "可以，让它跑吧。")
	h.clock.Advance(time.Second)
	p, _ := h.pending()
	res, err := (brainHost{s: h.sup}).AnswerPrompt(context.Background(), "w5:p1", p.Options[0].Keys)
	if err != nil {
		t.Fatalf("AnswerPrompt: %v", err)
	}
	if !strings.HasPrefix(res, "已回复 auth：") {
		t.Errorf("tool result = %q, want the Chinese confirmation", res)
	}
}

// Run C, deepseek, j2-approve-zh r1: the only line before the CEO's
// "可以，让它跑吧。" was the English block announcement, and the brain
// followed the English context. The request now states the language of
// the CEO's last words as a fact (what to do with it stays the model's).
func TestUtteranceRequestStatesTheCEOsLanguage(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: braintest.Reply("已让 auth 继续。", "")})
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.sup.OnVoiceEvent(voice.EventPush{Text: "auth is ready to run the full test suite with the race detector. Approve the test run?", Urgency: voice.UrgencyIdle})
	h.clock.Advance(time.Second)
	h.say("可以，让它跑吧。")
	calls := m.Calls()
	if len(calls) == 0 {
		t.Fatal("no brain turn")
	}
	if text := calls[0].Text(); !strings.Contains(text, "The user's last words were in Chinese.") {
		t.Errorf("request lacks the language fact:\n%s", text)
	}
}

// The fact follows the CEO's words, and is absent before they spoke.
func TestCEOLanguageFact(t *testing.T) {
	h := newHarness(t)
	if got := h.sup.ceoLanguage(); got != "" {
		t.Errorf("before any words: %q", got)
	}
	h.hear("现在什么情况？")
	if got := h.sup.ceoLanguage(); got != brain.LanguageChinese {
		t.Errorf("zh: %q", got)
	}
	h.hear("What about docs?")
	if got := h.sup.ceoLanguage(); got != brain.LanguageEnglish {
		t.Errorf("en: %q", got)
	}
}
