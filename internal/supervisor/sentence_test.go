package supervisor

import (
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
)

// docs/eval/eval-campaign-2026-10-03.md H1: a brain line ending in Chinese
// punctuation was spoken with an ASCII "." glued on ("要批准 bench-codex
// 跑测试吗？." in j1-status-zh r3).
func TestSentenceIsCJKAware(t *testing.T) {
	cases := map[string]string{
		"bench-codex 还是卡在等你点头那一步。": "bench-codex 还是卡在等你点头那一步。",
		"要批准 bench-codex 跑测试吗？":    "要批准 bench-codex 跑测试吗？",
		"好！":                       "好！",
		"它在等你的下一句话":                "它在等你的下一句话。",
		"Auth is done":             "Auth is done.",
		"Approve?":                 "Approve?",
		"It said \"done.\"":        "It said \"done.\"",
		"  ":                       "",
	}
	for in, want := range cases {
		if got := sentence(in); got != want {
			t.Errorf("sentence(%q) = %q, want %q", in, got, want)
		}
	}
}

// The whole reply path: headline plus ask, both Chinese, ends on the
// ask's own question mark.
func TestToolResultChineseAskNoASCIISeam(t *testing.T) {
	rep := brain.Reply{Spoken: "bench-codex 还是卡在等你点头那一步，就是跑整套测试的事。", Ask: "要批准 bench-codex 跑测试吗？"}
	res := toolResultFrom(rep, prose.EarRunaway)
	for _, seam := range []string{"。.", "？.", "！."} {
		if strings.Contains(res.Spoken, seam) {
			t.Fatalf("spoken %q has the %q seam", res.Spoken, seam)
		}
	}
	spoken, _ := earLine(brain.Reply{Spoken: "好，它会一直等着", Ask: ""})
	if spoken != "好，它会一直等着。" {
		t.Fatalf("earLine = %q", spoken)
	}
}
