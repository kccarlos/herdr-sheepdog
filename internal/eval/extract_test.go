package eval

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

const extractFixture = "../../testdata/eval/extract/2026-01-02.jsonl"

func extractFixtureCases(t *testing.T, opt ExtractOptions) []SVPCase {
	t.Helper()
	events, err := ReadDayFile(extractFixture)
	if err != nil {
		t.Fatal(err)
	}
	return Extract(events, "2026-01-02", opt)
}

// The synthetic day covers what extraction reconstructs: the roster with
// aliases from the supervisor's events, an open approval prompt (and not
// a send_instruction record), the screen from the orchestrator's own read, the
// merged transcript, a partial relay, the logged baseline and the
// provenance; the voice's stop call is not a case.
func TestExtract(t *testing.T) {
	cases := extractFixtureCases(t, ExtractOptions{})
	if len(cases) != 3 {
		ids := make([]string, 0, len(cases))
		for _, c := range cases {
			ids = append(ids, c.ID)
		}
		t.Fatalf("got %d cases %v, want 3 (two relays, one finish)", len(cases), ids)
	}
	utt, done, status := cases[0], cases[1], cases[2]

	if utt.ID != "x-2026-01-02-100007-utt" || utt.Request.Question != "the tests" || utt.Request.Kind != "utterance" {
		t.Errorf("utterance case = %s %+v", utt.ID, utt.Request)
	}
	if utt.Source == nil || utt.Source.EventID != 12 || utt.Source.Day != "2026-01-02" || utt.Source.Heard != "Tell docs to run the tests" {
		t.Errorf("source = %+v", utt.Source)
	}
	if len(utt.Agents) != 2 || utt.Agents[0].Alias != "auth" || utt.Agents[1].Alias != "docs" {
		t.Fatalf("agents = %+v (the unknown pane is not a roster row)", utt.Agents)
	}
	docs := utt.Agents[1]
	if docs.Pending == nil || docs.Pending.Question != "May I run npm test?" || len(docs.Pending.Options) != 2 {
		t.Errorf("docs pending = %+v", docs.Pending)
	}
	for _, leak := range []string{"bob@example.com", "AIzaSy", "/Users/someone"} {
		if strings.Contains(docs.Screen, leak) {
			t.Errorf("screen not scrubbed of %q: %q", leak, docs.Screen)
		}
	}
	if !strings.HasPrefix(docs.Screen, "May I run npm test?") || strings.Contains(docs.Screen, "WORKER_OUTPUT") {
		t.Errorf("screen = %q", docs.Screen)
	}
	if got := strings.Join(utt.Transcript, " | "); got != "CEO: Tell docs to run the tests" {
		t.Errorf("transcript = %q", got)
	}
	if !containsStr(utt.Expect.PromptMustNotContain, "[") || !utt.Expect.QuestionIsHeard || !containsStr(utt.Expect.MustNotCall, brain.ToolInterrupt) {
		t.Errorf("expect = %+v, want the partial-relay and no-interrupt invariants", utt.Expect)
	}
	if utt.Observed == nil || len(utt.Observed.ToolCalls) != 1 || utt.Observed.ToolCalls[0].Name != "prompt" || utt.Observed.Spoken != "Sending it now." {
		t.Errorf("observed = %+v", utt.Observed)
	}
	if b := baselineOf(utt); !strings.HasPrefix(b.summary, "fails") {
		t.Errorf("the logged bracketed prompt should fail the baseline: %q", b.summary)
	}

	if done.Request.Kind != "announce" || done.Request.Agent != "auth" || done.Request.Event != "done" || !done.Request.Owed || done.Request.Template != "auth finished." {
		t.Errorf("announce request = %+v", done.Request)
	}
	if done.Observed == nil || done.Observed.Spoken != "Auth finished; all 12 tests pass." || done.Observed.Urgency != "idle" {
		t.Errorf("announce observed = %+v", done.Observed)
	}
	if done.Job != "J4" || !containsStr(done.Expect.MustNotCall, brain.ToolPrompt) {
		t.Errorf("announce job/expect = %s %+v", done.Job, done.Expect)
	}

	if status.Job != "J1" || status.Observed != nil {
		t.Errorf("status case job=%s observed=%+v (no brain turn logged)", status.Job, status.Observed)
	}
	for _, a := range status.Agents {
		if a.Pending != nil {
			t.Errorf("%s: pending %+v was confirmed before this relay", a.Alias, a.Pending)
		}
	}
	lines := strings.Join(status.Transcript, " | ")
	for _, want := range []string{"Dog: Sending it now.", "CEO: Oh, never mind."} {
		if !strings.Contains(lines, want) {
			t.Errorf("transcript %q lacks %q", lines, want)
		}
	}

	// Every extracted case runs offline; the announce case really reaches
	// the executor (the scripted prompt is attempted and refused on an
	// event turn), rather than passing because the turn fell back.
	for _, r := range RunSVPOffline(context.Background(), cases, "../../testdata/herdr") {
		if r.Status == StatusFail {
			t.Errorf("%s: %s", r.ID, r.Detail)
		}
		if _, fell := r.Got["fallback"]; fell {
			t.Errorf("%s fell back: %v", r.ID, r.Got["fallback"])
		}
		if r.ID == done.ID && r.Got["tools_called"] != brain.ToolPrompt {
			t.Errorf("announce case called %v, want the scripted prompt attempt", r.Got["tools_called"])
		}
	}
}

func TestExtractWindowAndRedact(t *testing.T) {
	from := time.Date(2026, 1, 2, 10, 0, 15, 0, time.UTC)
	cases := extractFixtureCases(t, ExtractOptions{From: from, Redact: []string{"docs"}})
	if len(cases) != 2 {
		t.Fatalf("got %d cases after %s, want 2", len(cases), from)
	}
	for _, c := range cases {
		for _, a := range c.Agents {
			if a.Alias == "docs" {
				t.Errorf("%s: alias not redacted", c.ID)
			}
		}
	}
}

func TestClassifyJob(t *testing.T) {
	for s, want := range map[string]string{
		"Oh, never mind.":                  "J6-nevermind",
		"Stop auth.":                       "J6-stop",
		"What does that mean?":             "J5",
		"Can you say again what it said?":  "J5",
		"What did it find?":                "J4",
		"What's the current status?":       "J1",
		"Yes, please.":                     "J2-yes",
		"Can you approve ucsummary-codex?": "J2-yes",
		"No.":                              "J2-no",
		"Tell docs to run the tests.":      "J3",
		"はい":                               "",
	} {
		if got := classifyJob(s); got != want {
			t.Errorf("classifyJob(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestScrub(t *testing.T) {
	in := `mail kc@example.org, key sk-abcdefghijklmnop1234, token: abcdefghijklmnopqrstu, ghp_abcdefghijklmnopqrstuvwxyz12, /Users/kc/Documents`
	out := scrub(in)
	for _, leak := range []string{"kc@example.org", "sk-abcdef", "abcdefghijklmnopqrstu", "ghp_", "/Users/kc"} {
		if strings.Contains(out, leak) {
			t.Errorf("scrub left %q in %q", leak, out)
		}
	}
}
