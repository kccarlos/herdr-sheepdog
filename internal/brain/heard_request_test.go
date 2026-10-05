package brain

import (
	"strings"
	"testing"
	"time"
)

// docs/eval/eval-campaign-2026-10-03.md H6: the utterance request states what
// the CEO had not heard when they began speaking, and a line given to
// the voice during the turn is reported before the next step.
func TestHeardFactsInUtteranceRequest(t *testing.T) {
	start := time.Date(2026, 10, 3, 9, 0, 9, 0, time.Local)
	h := &HeardFacts{SpokeAt: start, Before: &PushedLine{Text: "Shall I let you know when it finishes?", At: start.Add(-4 * time.Second)}}
	req := Request{Kind: KindUtterance, Question: "Yes, let me know when it's done."}
	req.facts = EventFacts{Heard: h}
	if s := requestText(req); strings.Contains(s, "began saying") {
		t.Errorf("nothing newer was pushed, yet the request says:\n%s", s)
	}
	h.After = []PushedLine{{Text: "Want it to fix those two links?", At: start.Add(time.Second)}}
	s := requestText(req)
	for _, want := range []string{
		"The user began saying these words at 09:00:09.",
		`The last line the voice was given before that (09:00:05): "Shall I let you know when it finishes?".`,
		`so not heard when they spoke: [09:00:10, 1 s after] "Want it to fix those two links?"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("request lacks %q:\n%s", want, s)
		}
	}
	if !strings.HasPrefix(s, "Request from the assistant (utterance): utterance=") {
		t.Errorf("first line shape changed:\n%s", s)
	}
	later := &HeardFacts{SpokeAt: start, After: append(append([]PushedLine(nil), h.After...), PushedLine{Text: "docs finished.", At: start.Add(3 * time.Second), Unheard: true})}
	note := factsUpdate(EventFacts{Heard: h}, EventFacts{Heard: later})
	if !strings.Contains(note, `[09:00:12, 3 s after] "docs finished." (judged not heard)`) || strings.Contains(note, "two links") {
		t.Errorf("update = %q", note)
	}
}
