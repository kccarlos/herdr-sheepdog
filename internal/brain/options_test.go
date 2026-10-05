package brain

import (
	"encoding/json"
	"strings"
	"testing"
)

// The reply that failed status-en pass 1 (baseline 2026-10-05, 08:33:31Z:
// "cannot unmarshal object into Reply.options.0 of type string") now
// parses: objects give their label, strings stay as they are, and the
// options encode back as plain strings for the voice.
func TestParseReplyTolerantOptions(t *testing.T) {
	cases := map[string][]string{
		`{"spoken":"Codex wants to run the tests.","options":[{"label":"Approve"},{"label":"Decline","keys":["esc"]}]}`: {"Approve", "Decline"},
		`{"spoken":"x","options":["Yes","No"]}`:                                 {"Yes", "No"},
		`{"spoken":"x","options":[{"text":"Run them"},"Wait",{"id":3},""]}`:     {"Run them", "Wait"},
		`{"spoken":"x","options":"Approve"}`:                                    {"Approve"},
		`{"spoken":"x","options":null}`:                                         {},
		`{"spoken":"x"}`:                                                        {},
		`{"spoken":"x","options":[{"title":"A"},{"option":"B"},{"value":"C"}]}`: {"A", "B", "C"},
	}
	for in, want := range cases {
		r, err := parseReply(in, false)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if strings.Join(r.Options, "|") != strings.Join(want, "|") {
			t.Errorf("%s: options = %q, want %q", in, r.Options, want)
		}
	}
	r, _ := parseReply(`{"spoken":"x","options":[{"label":"Approve"}]}`, false)
	out, err := json.Marshal(r)
	if err != nil || !strings.Contains(string(out), `"options":["Approve"]`) {
		t.Errorf("encoded = %s, %v", out, err)
	}
	if r, err := parseReply(`{"spoken":"x","options":42}`, false); err != nil || len(r.Options) != 0 {
		t.Errorf("a number as options: %+v, %v; want no options and no error", r, err)
	}
}
