package gptlive

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The ear contract: the question is heard last, so a result's options go
// in before a closing question, never after it.
func TestSpokenTextKeepsTheQuestionLast(t *testing.T) {
	for _, c := range []struct {
		res  voice.ToolResult
		want string
	}{
		{voice.ToolResult{Spoken: "Auth finished."}, "Auth finished."},
		{voice.ToolResult{Spoken: "Auth finished.", Options: []string{"run the tests", "stop here"}}, "Auth finished. Options: run the tests; stop here."},
		{voice.ToolResult{Spoken: "Auth wants to push to main. Approve?", Options: []string{"yes", "no"}}, "Auth wants to push to main. Options: yes; no. Approve?"},
		{voice.ToolResult{Spoken: "Which one?", Options: []string{"auth", "docs"}}, "Options: auth; docs. Which one?"},
	} {
		if got := spokenText(c.res); got != c.want {
			t.Errorf("spokenText(%+v) = %q, want %q", c.res, got, c.want)
		}
	}
}
