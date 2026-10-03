package supervisor

import (
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// The logged phrases (docs/bench/tier2-2026-10-03.md #2): the brain sent
// esc to bench-codex's open prompt on each, and Codex's esc cancels the
// command. The rail now refuses the decline and tells the brain why;
// nothing reaches the pane. Plain declines still go through.
func TestDeferralOrHesitationNeverDeclines(t *testing.T) {
	cases := []struct {
		name  string
		heard string
		sent  bool
		why   string
	}{
		{"deferral-zh r3", "先不要。", false, "puts"},
		{"status-zh-tired r3", "先不要跑。文档那边核对好了吗？", false, "puts"},
		{"unheard-approval-requeue r2", "No, don't run that yet. What does bench-codex want?", false, "puts"},
		{"deferral-en-hesitant r2", "No. What does bench Codex want?", false, "ask a question"},
		{"hesitant not now", "Um, not now, let me think about it.", false, "puts"},
		{"wait", "No wait, hold on.", false, "puts"},
		{"等一下", "等一下再说。", false, "puts"},
		{"a lone filler", "Uhh.", false, "filler"},
		{"plain no", "No, don't run it.", true, ""},
		{"plain chinese no", "不要跑这个。", true, ""},
		{"no, tell it", "No, tell it to use the other file.", true, ""},
		// Campaign D N8 and #113: a decline spelled out with its verb
		// still declines when a question comes with it.
		{"campaign D N8", "What does bench-codex want to push? And no, definitely don't approve that.", true, ""},
		{"#113 echo runs", "No, definitely don't approve that, ok?", true, ""},
		{"chinese decline with a question", "不要批准，它还在干什么？", true, ""},
		{"why don't you", "Why don't you run it?", false, "ask a question"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := braintest.NewModel(
				braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"esc"}})}},
				braintest.Step{Text: braintest.Reply("Done.", "")},
			)
			h := newBrainHarness(t, m)
			h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
			h.block("w5:p1", "codex-approval-prompt.screen.txt")
			if _, has := h.pending(); !has {
				t.Fatal("no pending prompt")
			}
			h.say(tc.heard)
			sent := len(h.sendKeysRequests()) == 1
			if sent != tc.sent {
				t.Fatalf("keys sent = %v (%v), want %v", sent, h.sendKeysRequests(), tc.sent)
			}
			if tc.sent {
				return
			}
			if _, has := h.pending(); !has {
				t.Error("the prompt was dropped")
			}
			ev := h.log.ofType("brain.rail_blocked")
			if len(ev) != 1 || !strings.Contains(payload(ev[0])["reason"].(string), tc.why) {
				t.Errorf("rail_blocked = %v", ev)
			}
			if last := m.Calls()[len(m.Calls())-1].Text(); !strings.Contains(last, "not permitted") || !strings.Contains(last, tc.why) {
				t.Errorf("the brain did not read why:\n%s", last)
			}
		})
	}
}

// The fast path still declines on a bare "no", and a bare "yes" approves.
func TestBareNoStillDeclines(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.say("No.")
	if got := h.sendKeysRequests(); len(got) != 1 || got[0] != [2]string{"w5:p1", "esc"} {
		t.Errorf("keys = %v, want esc", got)
	}
}

func TestDeferralWords(t *testing.T) {
	for text, want := range map[string]bool{
		"先不要。": true, "not now": true, "No, don't run that yet.": true, "Later.": true, "等等": true,
		"No.": false, "不要": false, "No, don't run it.": false, "Reject it.": false,
	} {
		if got := deferralInput(text); got != want {
			t.Errorf("deferralInput(%q) = %v, want %v", text, got, want)
		}
	}
	for text, want := range map[string]bool{"Uhh.": true, "嗯…": true, "um, uh": true, "No.": false, "um no": false} {
		if got := fillerOnly(text); got != want {
			t.Errorf("fillerOnly(%q) = %v, want %v", text, got, want)
		}
	}
}
