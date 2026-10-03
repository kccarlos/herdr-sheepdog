package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"
)

// docs/eval-campaign-2026-10-03.md H4: after the shepherd spoke Chinese,
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
	if got := replacedLine("auth", "docs 在问：Do you want to create claude.txt?", true); got != "先放下 auth 之前的问题。现在 docs 在问：Do you want to create claude.txt?" {
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
