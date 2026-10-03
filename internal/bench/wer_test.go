package bench

import (
	"math"
	"testing"
)

func TestErrorRate(t *testing.T) {
	for _, c := range []struct {
		ref, hyp string
		want     float64
	}{
		{"What's the codex one doing?", "what's the Codex one doing", 0},
		{"Um, approve the tests.", "approve the tests", 0},
		{"approve the tests", "approve tests", 1.0 / 3},
		{"approve the tests", "a proof the test", 1},
		{"先不要批准", "先不要批准。", 0},
		{"先不要批准", "先不要比准", 0.2},
		{"", "", 0},
		{"", "noise", 1},
	} {
		if got := ErrorRate(c.ref, c.hyp); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("ErrorRate(%q, %q) = %.3f, want %.3f", c.ref, c.hyp, got, c.want)
		}
	}
	if got := Tokens("嗯，让 bench-codex 跑测试"); len(got) != 6 || got[0] != "让" || got[1] != "bench" || got[2] != "codex" {
		t.Fatalf("tokens = %q", got)
	}
}
