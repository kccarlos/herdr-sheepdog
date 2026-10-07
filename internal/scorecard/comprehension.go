package scorecard

import (
	"regexp"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// reComprehension matches a "what does that mean"-class follow-up (J5 in
// docs/iteration-2.md). The apostrophe class covers both a straight and a
// curly quote, since transcript text comes from a speech model and either
// can appear.
var reComprehension = regexp.MustCompile(`(?i)what does that mean|what do you mean|what is that|say again|repeat|explain|i don[’']t understand|什么意思|再说一遍`)

// computeComprehension counts user utterances (voice.tool_call
// to_supervisor texts) that read as a comprehension follow-up.
func computeComprehension(events []eventlog.Event) int {
	n := 0
	for _, t := range utteranceTexts(events) {
		if reComprehension.MatchString(t.text) {
			n++
		}
	}
	return n
}
