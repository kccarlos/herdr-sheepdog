package eval

import (
	"context"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

func segConvo() []SimLine {
	return []SimLine{
		{AtMS: 100, Who: "push", Text: "bench-codex is done."},
		{AtMS: 200, Who: "ceo", Text: "status?", Beat: 1},
		{AtMS: 300, Who: "sheepdog", Text: "Two workers are busy. Which one?"},
		{AtMS: 400, Who: "ceo", Text: "the codex one", Tag: "answer"},
		{AtMS: 500, Who: "sheepdog", Text: "bench-codex finished the tests."},
		{AtMS: 600, Who: "system", Text: simWaitMarker, Beat: 2},
		{AtMS: 700, Who: "push", Text: "docs-claude is blocked."},
		{AtMS: 800, Who: "ceo", Text: "approve it", Beat: 3},
		{AtMS: 900, Who: "system", Text: simWaitMarker, Beat: 4},
	}
}

// Segments start at each beat; follow-ups and pushes stay in the segment
// they land in; pushes before the first beat are segment 0; a silence
// with nothing said is dropped.
func TestSplitSegments(t *testing.T) {
	got := SplitSegments(segConvo())
	want := []Segment{{0, SegmentAnnouncement, 0, 1}, {1, SegmentRequest, 1, 5}, {2, SegmentAnnouncement, 5, 7}, {3, SegmentRequest, 7, 8}}
	if len(got) != len(want) {
		t.Fatalf("segments %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("segment %d: %+v want %+v", i, got[i], want[i])
		}
	}
	// Without beats: cut at each CEO line that is not an answer.
	nb := []SimLine{{Who: "ceo", Text: "a"}, {Who: "sheepdog", Text: "x?"}, {Who: "ceo", Text: "y", Tag: "answer"}, {Who: "ceo", Text: "b"}}
	if s := SplitSegments(nb); len(s) != 2 || s[0].End != 3 || s[1].Beat != 2 {
		t.Fatalf("no beats: %+v", s)
	}
}

// The items depend on the segment: announcements get no answered or
// right_agent; a request with no reply gets only answered and the action
// items; a beat's clarify and coherence items go to that beat only.
func TestSegmentCriteria(t *testing.T) {
	sc := SimScenario{Agenda: []string{"status?", "<wait>", "approve it"}, Expect: SimExpect{Skip: []string{"language"}},
		Workday: &Workday{Truth: WorkdayTruth{Clarify: []WorkdayClarify{{Beat: 3, Needed: true, Why: "two blocked"}}, Coherence: []WorkdayProbe{{Beat: 1, Answer: "x"}}}}}
	ids := func(cs []SegmentCriterion) string {
		var out []string
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(SegmentCriteria(sc, Segment{Beat: 1, Kind: SegmentRequest}, true)); got != "answered,no_unintended_action,right_agent,no_hallucination,facts_current,length_fits,ear_friendly,coherence_1" {
		t.Fatalf("request: %s", got)
	}
	if got := ids(SegmentCriteria(sc, Segment{Beat: 2, Kind: SegmentAnnouncement}, true)); got != "no_unintended_action,no_hallucination,facts_current,length_fits,ear_friendly" {
		t.Fatalf("announcement: %s", got)
	}
	if got := ids(SegmentCriteria(sc, Segment{Beat: 3, Kind: SegmentRequest}, false)); got != "answered,no_unintended_action,right_agent,clarify_1" {
		t.Fatalf("no reply: %s", got)
	}
	c := SegmentCriteria(sc, Segment{Beat: 1, Kind: SegmentRequest}, true)
	if !strings.Contains(c[0].Criterion, "By the end of the segment") {
		t.Fatalf("answered is asked of the segment: %s", c[0].Criterion)
	}
}

// One call per segment; verdicts are matched by id; a missing id is an
// error, never a pass.
func TestJudgeSegmentParse(t *testing.T) {
	crit := []SegmentCriterion{{ID: "answered", Criterion: "a"}, {ID: "length_fits", Criterion: "b"}}
	vs, err := parseSegmentVerdicts(`{"verdicts":[{"id":"length_fits","verdict":"no","confidence":0.8,"evidence":"Sheepdog: Done."},{"id":"answered","verdict":"yes","confidence":1,"evidence":"CEO: status?"}]}`, crit)
	if err != nil || vs[0].Answer != AnswerYes || vs[1].Answer != AnswerNo || vs[1].Rationale != "Sheepdog: Done." {
		t.Fatalf("%+v %v", vs, err)
	}
	if _, err := parseSegmentVerdicts(`{"verdicts":[{"id":"answered","verdict":"yes","confidence":1,"evidence":"x"}]}`, crit); err == nil || !strings.Contains(err.Error(), "length_fits") {
		t.Fatalf("missing id: %v", err)
	}
}

func TestJudgeSegments(t *testing.T) {
	sc := SimScenario{Agenda: []string{"status?", "<wait>", "approve it", "<wait>"}, Workday: &Workday{}}
	f := &FakeJudge{Rules: []FakeRule{{Contains: "length of each", Answer: AnswerNo}}}
	segs, err := JudgeSegments(context.Background(), f, sc, segConvo(), "facts", 2)
	if err != nil || len(segs) != 4 {
		t.Fatalf("%v %+v", err, segs)
	}
	if segs[1].Beat != 1 || segs[1].StartMS != 200 || segs[1].EndMS != 500 || segs[1].Verdicts[0].Name != "answered" {
		t.Fatalf("segment 1 %+v", segs[1])
	}
	seen := false
	for _, in := range f.Inputs() {
		if strings.Contains(in.Transcript, "Sheepdog: bench-codex finished the tests.") && !strings.Contains(in.Transcript, "docs-claude") {
			seen = true
		}
	}
	if !seen {
		t.Fatal("segment 1's lines were not judged on their own")
	}
	// The LLM judge: one model call per segment, prompt carries the
	// earlier conversation as context.
	// Calls run concurrently, so every answer carries every id (extra ids
	// are ignored).
	var parts []string
	for _, id := range append(defaultRubricOrder, "clarify_1", "coherence_1") {
		parts = append(parts, `{"id":"`+id+`","verdict":"yes","confidence":1,"evidence":"e"}`)
	}
	all := braintest.Step{Text: `{"verdicts":[` + strings.Join(parts, ",") + `]}`}
	steps := []braintest.Step{all, all, all, all}
	j := &LLMJudge{Model: braintest.NewModel(steps...), ModelID: "j"}
	segs, err = JudgeSegments(context.Background(), j, sc, segConvo(), "facts", 1)
	if err != nil || len(segs) != 4 {
		t.Fatalf("llm: %v %+v", err, segs)
	}
	for _, s := range segs {
		for _, v := range s.Verdicts {
			if !v.Passed() {
				t.Fatalf("verdict %+v", v)
			}
		}
	}
	if !strings.Contains(segmentPrompt(SegmentInput{Earlier: "[t+0s] CEO: hi", Segment: "x", Criteria: []SegmentCriterion{{ID: "a", Criterion: "c"}}}), "EARLIER CONVERSATION") {
		t.Fatal("no earlier context")
	}
}

// The calibration set in segment mode: one call per item, one verdict
// per (item, rubric id), same shape as JudgeCalib.
func TestJudgeCalibSegments(t *testing.T) {
	f := CalibFile{Version: 1, Rubric: CalibRubric(), Items: []CalibItem{
		{ID: "i1", Source: "s", Lang: "en", Context: "c", Turns: []CalibTurn{{Who: "user", T: "0", Text: "hi"}}, RubricIDs: []string{"answered", "length_fits"}},
	}}
	j := &FakeJudge{Rules: []FakeRule{{Contains: "length", Answer: AnswerNo}}}
	vs := JudgeCalibSegments(context.Background(), j, f, 2, nil)
	if len(vs) != 2 || vs[0].Verdict != "pass" || vs[1].Verdict != "fail" || vs[1].RubricID != "length_fits" {
		t.Fatalf("%+v", vs)
	}
}

// An answer missing an id is asked once more.
func TestJudgeSegmentRetry(t *testing.T) {
	crit := []SegmentCriterion{{ID: "answered", Criterion: "a"}, {ID: "language", Criterion: "b"}}
	j := &LLMJudge{Model: braintest.NewModel(
		braintest.Step{Text: `{"verdicts":[{"id":"answered","verdict":"yes","confidence":1,"evidence":"x"}]}`},
		braintest.Step{Text: `{"verdicts":[{"id":"answered","verdict":"yes","confidence":1,"evidence":"x"},{"id":"language","verdict":"no","confidence":1,"evidence":"y"}]}`},
	), ModelID: "j"}
	vs, err := j.JudgeSegment(context.Background(), SegmentInput{Criteria: crit, Segment: "s"})
	if err != nil || len(vs) != 2 || vs[1].Answer != AnswerNo {
		t.Fatalf("%+v %v", vs, err)
	}
}
