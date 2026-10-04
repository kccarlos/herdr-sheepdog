package eval

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// corpusFiles are the L0 sets `make eval` runs offline (docs/iteration-2.md
// WP0b), besides svp.jsonl, which TestRunSVPOffline covers.
func corpusFiles(t *testing.T) []string {
	t.Helper()
	files := []string{"../../testdata/eval/regressions.jsonl", "../../testdata/eval/expanded.jsonl"}
	extracted, err := filepath.Glob("../../testdata/eval/extracted/*.jsonl")
	if err != nil || len(extracted) == 0 {
		t.Fatalf("no extracted set: %v", err)
	}
	return append(files, extracted...)
}

// TestRunCorpusOffline is the L0 gate over the whole corpus: every
// regression, extracted and expanded case runs through the brain with the
// scripted model, and a regression that pins its baseline must still
// catch the logged failure. The corpus must stay fast enough to run on
// every change (docs/iteration-2.md: hundreds of cases, free).
func TestRunCorpusOffline(t *testing.T) {
	start := time.Now()
	total := 0
	seen := map[string]string{}
	for _, f := range corpusFiles(t) {
		cases, err := LoadSVPCases(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			if prev, dup := seen[c.ID]; dup {
				t.Errorf("duplicate id %q in %s and %s", c.ID, prev, f)
			}
			seen[c.ID] = f
			switch brain.Kind(c.Request.Kind) {
			case brain.KindUtterance, brain.KindAnnounce:
			default:
				t.Errorf("%s: bad request.kind %q", c.ID, c.Request.Kind)
			}
		}
		total += len(cases)
		for _, r := range RunSVPOffline(context.Background(), cases, "../../testdata/herdr") {
			if r.Status == StatusFail {
				t.Errorf("%s: %s", r.ID, r.Detail)
			}
		}
	}
	if total < 300 {
		t.Errorf("corpus has %d cases, want hundreds", total)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("corpus took %s offline, want under 30 s", d)
	}
	t.Logf("%d corpus cases in %s", total, time.Since(start).Round(time.Millisecond))
}

// TestRegressionsCatchLoggedFailures checks each regression case against
// its logged baseline directly: those whose failure the logged reply
// shows fail on it, and the finish-line case, whose failure is the
// supervisor's, passes on it (its pending_check says why).
func TestRegressionsCatchLoggedFailures(t *testing.T) {
	cases, err := LoadSVPCases("../../testdata/eval/regressions.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"never-mind-is-not-stop":        true,
		"explain-simpler-second-time":   true,
		"partial-relay-no-fabrication":  true,
		"finish-line-needs-user-spoken": false,
		"status-needs-you-first":        true,
		"detail-question-not-no-change": true,
		// docs/history/dive-deep.md WP8a/WP8b: the logged turns never read the
		// worker's session.
		"detail-what-are-the-seven-glitches": true,
		"detail-what-exactly-did-it-say":     true,
	}
	if len(cases) != len(want) {
		t.Fatalf("got %d regressions, want %d", len(cases), len(want))
	}
	for _, c := range cases {
		wantFail, ok := want[c.ID]
		if !ok {
			t.Errorf("unexpected regression %q", c.ID)
			continue
		}
		if c.Note == "" || c.Source == nil || c.Observed == nil || c.BaselineFails == nil {
			t.Errorf("%s: needs note, source, observed and baseline_fails", c.ID)
			continue
		}
		b := baselineOf(c)
		if got := strings.HasPrefix(b.summary, "fails"); got != wantFail {
			t.Errorf("%s: baseline %q, want failing=%v", c.ID, b.summary, wantFail)
		}
	}
	acases, err := LoadAssistantCases("../../testdata/eval/regressions.assistant.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range RunAssistant(context.Background(), acases, nil, nil, "") {
		if r.Status != StatusPass || !strings.HasPrefix(r.Baseline, "fails") {
			t.Errorf("%s: %+v, want the logged stop to fail the case", r.ID, r)
		}
	}
}

// The checks only a live run can apply are listed as skipped offline,
// never passed silently; offline, the deterministic ones still apply.
func TestGradeSVPOfflineSkipsLiveChecks(t *testing.T) {
	yes := true
	exp := SVPExpect{
		SpokenMaxWords: 5, SpokenNoJargon: true, FirstSentenceContainsAnyOf: []string{"auth"},
		NeedsUser: &yes, UrgencyNot: []string{"skip"}, PromptMustNotContain: []string{"["},
	}
	rec := newRecorder()
	rec.calls = []string{brain.ToolPrompt}
	rec.args = []map[string]any{{"agent": "auth", "text": "fix the mute [invented]"}}
	reasons, skipped := gradeSVP(exp, rec, brain.Reply{Spoken: "The daemon's fast path took 120 ms on v2.1.0 and more."}, true)
	if len(reasons) != 1 || !strings.Contains(reasons[0], `"["`) {
		t.Errorf("offline reasons = %v, want only the prompt check", reasons)
	}
	if want := "spoken_max_words,spoken_no_jargon,first_sentence_contains_any_of,needs_user,urgency_not"; strings.Join(skipped, ",") != want {
		t.Errorf("skipped = %v, want %s", skipped, want)
	}
	reasons, skipped = gradeSVP(exp, rec, brain.Reply{Spoken: "The daemon's fast path took 120 ms on v2.1.0 and more."}, false)
	if len(skipped) != 0 || len(reasons) != 5 {
		t.Errorf("live: reasons = %v (want 5: all but urgency, which a spoken line with no urgency meets), skipped = %v", reasons, skipped)
	}
	reasons, _ = gradeSVP(exp, newRecorder(), brain.Reply{Spoken: "Auth needs you. Approve?", NeedsUser: true, Urgency: "idle"}, false)
	if len(reasons) != 0 {
		t.Errorf("a good live reply failed: %v", reasons)
	}
}

func TestJargonIn(t *testing.T) {
	for s, want := range map[string]bool{
		"It's a write-up of how the two layers work.":          false,
		"The daemon owns the push queue.":                      true,
		"It merged PR 51 and CI is green.":                     true,
		"Version 2.1.0 is out.":                                true,
		"It took 300 ms.":                                      true,
		"Two sheepdog agents are idle; one needs your answer.": false,
	} {
		if got := len(jargonIn(s)) > 0; got != want {
			t.Errorf("jargonIn(%q) = %v, want jargon=%v", s, jargonIn(s), want)
		}
	}
}

// An urgency_not of skip also rules out an empty line with no urgency,
// which the supervisor treats as a skip.
func TestUrgencyNotSkipCoversEmptyLine(t *testing.T) {
	exp := SVPExpect{UrgencyNot: []string{brain.UrgencySkip}}
	if r, _ := gradeSVP(exp, newRecorder(), brain.Reply{}, false); len(r) != 1 {
		t.Errorf("empty reply: reasons = %v", r)
	}
	if r, _ := gradeSVP(exp, newRecorder(), brain.Reply{Spoken: "Auth finished."}, false); len(r) != 0 {
		t.Errorf("spoken reply: reasons = %v", r)
	}
}

// not_stop_word checks the daemon's bare-stop list, offline: "never
// mind" reaches the brain (iteration 2 WP1, regression
// never-mind-is-not-stop), a real stop word never does.
func TestNotStopWord(t *testing.T) {
	for q, fails := range map[string]bool{"Never mind.": false, "never mind": false, "Oh, never mind.": false, "Stop.": true, "hold on": true} {
		c := SVPCase{ID: "x", Request: SVPRequest{Kind: "utterance", Question: q}, Expect: SVPExpect{NotStopWord: true}}
		if got := len(stopWordReasons(c)) > 0; got != fails {
			t.Errorf("%q: failed = %v, want %v", q, got, fails)
		}
	}
	if r := stopWordReasons(SVPCase{Request: SVPRequest{Question: "stop"}}); r != nil {
		t.Errorf("without not_stop_word: %v", r)
	}
}

// ask_empty and reply_language are live-only checks: skipped offline, and
// live they fail an ask on a plain status line and a Chinese reply after
// the shepherd switched to English (bench #14, #15a).
func TestGradeAskEmptyAndReplyLanguage(t *testing.T) {
	en := SVPExpect{AskEmpty: true, ReplyLanguage: "en"}
	reasons, skipped := gradeSVP(en, newRecorder(), brain.Reply{Spoken: "好的", Ask: "要不要？"}, true)
	if len(reasons) != 0 || strings.Join(skipped, ",") != "ask_empty,reply_language" {
		t.Errorf("offline: reasons %v skipped %v", reasons, skipped)
	}
	reasons, _ = gradeSVP(en, newRecorder(), brain.Reply{Spoken: "Both are idle.", Ask: "Shall I rerun the checks?"}, false)
	if len(reasons) != 1 || !strings.Contains(reasons[0], "ask") {
		t.Errorf("an ask on a status line: %v", reasons)
	}
	reasons, _ = gradeSVP(en, newRecorder(), brain.Reply{Spoken: "两个都闲着。"}, false)
	if len(reasons) != 1 || !strings.Contains(reasons[0], "English") {
		t.Errorf("Chinese after English: %v", reasons)
	}
	if reasons, _ = gradeSVP(en, newRecorder(), brain.Reply{Spoken: "Both are idle."}, false); len(reasons) != 0 {
		t.Errorf("a good English status failed: %v", reasons)
	}
	zh := SVPExpect{ReplyLanguage: "zh"}
	if reasons, _ = gradeSVP(zh, newRecorder(), brain.Reply{Spoken: "Both are idle."}, false); len(reasons) != 1 {
		t.Errorf("English after Chinese: %v", reasons)
	}
}
