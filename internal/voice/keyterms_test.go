package voice

import (
	"testing"
	"time"
)

// KeyTermCoverage on run-2-like lines (docs/bench-2026-10-03.md #17,
// #19), in both languages: a paraphrase that keeps the agent, the
// numbers and the choice is heard; a reply about something else, or
// nothing at all, is not.
func TestKeyTermCoverage(t *testing.T) {
	names := []string{"bench-codex", "bench-claude", "auth"}
	cases := []struct {
		name, push, said string
		heard            bool
	}{
		{
			name:  "en paraphrase with number words",
			push:  "bench-codex: the test still fails (3, should be 5), unlike the earlier all pass.",
			said:  "Heads up: bench-codex says the test is still failing. It gets three where it should get five, even though it all passed earlier.",
			heard: true,
		},
		{
			name:  "zh paraphrase with Chinese numerals",
			push:  "bench-codex：测试还是失败（结果是3，应该是5），跟之前说的全部通过不一样。",
			said:  "bench-codex 那边测试还是没过，算出来是三，应该是五，和之前说的全过不一样。",
			heard: true,
		},
		{
			name:  "zh line voiced in digits",
			push:  "bench-codex：测试还是失败（结果是3，应该是5）。",
			said:  "提醒一下，bench-codex 的测试还没过，结果 3，应该 5。",
			heard: true,
		},
		{
			name: "the answer to the shepherd instead (user turn)",
			push: "bench-codex: the test still fails (3, should be 5), unlike the earlier all pass.",
			said: "Sure, I'll ask bench-claude to look at the README.",
		},
		{
			name: "zh: something else said",
			push: "bench-codex：测试还是失败（结果是3，应该是5）。",
			said: "好的，我让 bench-claude 去看一下文档。",
		},
		{
			name: "not voiced",
			push: "bench-codex：测试还是失败（结果是3，应该是5）。",
			said: "",
		},
		{
			name:  "approval with options, choices read",
			push:  "bench-claude wants to run go test. Options: Yes; Yes, and don't ask again; No.",
			said:  "bench-claude would like to run go test. You can say yes, yes and don't ask again, or no.",
			heard: true,
		},
		{
			name:  "zh approval, English labels translated",
			push:  "bench-claude 想运行 go test。Options: Yes; Yes, and don't ask again; No.",
			said:  "bench-claude 想跑一下 go test，可以选允许、允许并且不再问，或者拒绝。",
			heard: true,
		},
		{
			name: "approval: another agent's question voiced",
			push: "bench-claude wants to run go test. Options: Yes; No.",
			said: "auth is still working on the login page.",
		},
		{
			name:  "plain news paraphrased (no key term)",
			push:  "The deploy finished and the site is serving the new homepage.",
			said:  "Good news: the deploy is done, and the new homepage is live on the site.",
			heard: true,
		},
		{
			name: "plain news not voiced, an unrelated reply instead",
			push: "The deploy finished and the site is serving the new homepage.",
			said: "Sure, what would you like me to check next?",
		},
		{
			name:  "longest name only: README Claude",
			push:  "README Claude finished the install section.",
			said:  "README Claude is done with the install part.",
			heard: true,
		},
	}
	all := append([]string{"README Claude", "Claude"}, names...)
	for _, c := range cases {
		cov := KeyTermCoverage(c.push, all, c.said)
		if got := cov >= HeardMinCoverage; got != c.heard {
			t.Errorf("%s: coverage %.2f, heard %v, want %v", c.name, cov, got, c.heard)
		}
	}
}

func TestSpokenNumbers(t *testing.T) {
	for text, want := range map[string]string{
		"三": "3", "十五": "15", "二十五": "25", "三百二十": "320", "两": "2", "一二三": "123",
		"five": "5", "twenty": "20", "42": "42", "007": "7",
	} {
		got := spokenNumbers(text)
		found := false
		for _, n := range got {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("spokenNumbers(%q) = %v, want %s among them", text, got, want)
		}
	}
}

// A tracker with a Coverage judge uses it (GPT-Live), and a reset keeps
// it.
func TestHeardTrackerCoverageJudge(t *testing.T) {
	var calls int
	h := HeardTracker{Coverage: func(text string, tags map[string]any, said string) float64 {
		calls++
		return KeyTermCoverage(text, []string{"auth"}, said)
	}}
	h.Reset()
	now := time.Date(2026, 10, 3, 0, 48, 38, 0, time.UTC)
	h.Pushed(now, "auth: 3 of 5 pass.", nil, false)
	h.Said("auth has three out of five passing.")
	h.TurnComplete()
	if v := h.Settle(now, false); len(v) != 0 || calls != 1 {
		t.Fatalf("paraphrase judged unheard: %+v (calls %d)", v, calls)
	}
	h.Pushed(now, "auth: 3 of 5 pass.", map[string]any{"needs_user": true}, false)
	h.UserInput()
	h.Said("Sure, checking docs.")
	h.TurnComplete()
	v := h.Settle(now, false)
	if len(v) != 1 || v[0].Reason != UnheardUserTurn {
		t.Fatalf("verdicts = %+v, want one user_turn", v)
	}
}
