package brain

import (
	"strings"
	"testing"
	"time"
)

// The Unanswered section is in the state block only when the Host has a
// question on record: the user's words (clipped), how long ago, and what
// happened to the answer, with the id answers_earlier names.
func TestUnansweredSection(t *testing.T) {
	agents, notes, promises, invs := fullFlock()
	if state := stateText(dynamicSections(agents, notes, promises, invs, nil, "", speedNow, "")); strings.Contains(state, "## Unanswered") {
		t.Errorf("an empty record rendered a section:\n%s", state)
	}
	qs := []UnansweredInfo{{ID: "utt-7", Text: "why did the tests fail?\nthe payment ones", Asked: speedNow.Add(-3 * time.Minute), Why: "your turn on it timed out"}}
	state := stateText(dynamicSections(agents, notes, promises, invs, qs, "", speedNow, ""))
	want := `- utt-7, 3 min ago: "why did the tests fail? the payment ones" (your turn on it timed out)`
	if !strings.Contains(state, "## Unanswered") || !strings.Contains(state, want) {
		t.Errorf("state lacks %q:\n%s", want, state)
	}
	if i, j := strings.Index(state, "## Unanswered"), strings.Index(state, "## Now"); i < 0 || j < i {
		t.Errorf("the time is not last")
	}
	long := strings.Repeat("word ", 40)
	if got := renderUnanswered([]UnansweredInfo{{ID: "u", Text: long, Asked: speedNow}}, speedNow); strings.Contains(got, long) {
		t.Errorf("not clipped: %s", got)
	}
}

// answers_earlier is part of the answer contract: it parses, and the
// loop hands it to an UnansweredHost.
type earlierHost struct {
	stubHost
	got [][]string
}

func (h *earlierHost) Unanswered() []UnansweredInfo { return nil }
func (h *earlierHost) AnsweredEarlier(ids []string) { h.got = append(h.got, ids) }

func TestAnswersEarlierReachesTheHost(t *testing.T) {
	r, err := parseReply(`{"spoken": "About your earlier question: green.", "answers_earlier": ["utt-7"]}`, false)
	if err != nil || len(r.AnswersEarlier) != 1 || r.AnswersEarlier[0] != "utt-7" {
		t.Fatalf("parse = %+v, %v", r, err)
	}
	h := &earlierHost{}
	b := &Brain{host: h}
	b.answeredEarlier(nil)
	b.answeredEarlier(r.AnswersEarlier)
	if len(h.got) != 1 || h.got[0][0] != "utt-7" {
		t.Errorf("host got %v", h.got)
	}
	// A host without the interface is fine.
	(&Brain{host: &stubHost{}}).answeredEarlier([]string{"x"})
}
