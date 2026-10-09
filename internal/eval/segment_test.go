package eval

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
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
	want := []Segment{{Beat: 0, Kind: SegmentAnnouncement, Start: 0, End: 1}, {Beat: 1, Kind: SegmentRequest, Start: 1, End: 5}, {Beat: 2, Kind: SegmentAnnouncement, Start: 5, End: 7}, {Beat: 3, Kind: SegmentRequest, Start: 7, End: 8}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("segments %+v want %+v", got, want)
	}
	// Without beats: cut at each user line that is not an answer.
	nb := []SimLine{{Who: "ceo", Text: "a"}, {Who: "sheepdog", Text: "x?"}, {Who: "ceo", Text: "y", Tag: "answer"}, {Who: "ceo", Text: "b"}}
	if s := SplitSegments(nb); len(s) != 2 || s[0].End != 3 || s[1].Beat != 2 {
		t.Fatalf("no beats: %+v", s)
	}
}

// The items depend on the segment: announcements get no answered or
// right_agent; a request with no reply gets only answered and the action
// items; said_vs_done is asked wherever sheepdog spoke, n/a allowed.
func TestSegmentCriteria(t *testing.T) {
	sc := SimScenario{Agenda: []string{"status?", "<wait>", "approve it"}, Expect: SimExpect{Skip: []string{"language"}},
		Workday: &Workday{Truth: WorkdayTruth{Clarify: []WorkdayClarify{{Beat: 3, Needed: true, Why: "two blocked"}}, Coherence: []WorkdayProbe{{Beat: 1, Answer: "x"}}}}}
	if got := critIDs(SegmentCriteria(sc, Segment{Beat: 1, Kind: SegmentRequest}, true)); got != "answered,no_unintended_action,right_agent,no_hallucination,facts_current,length_fits,ear_friendly,said_vs_done" {
		t.Fatalf("request: %s", got)
	}
	if got := critIDs(SegmentCriteria(sc, Segment{Beat: 2, Kind: SegmentAnnouncement}, true)); got != "no_unintended_action,no_hallucination,facts_current,length_fits,ear_friendly,said_vs_done" {
		t.Fatalf("announcement: %s", got)
	}
	if got := critIDs(SegmentCriteria(sc, Segment{Beat: 3, Kind: SegmentRequest}, false)); got != "answered,no_unintended_action,right_agent" {
		t.Fatalf("no reply: %s", got)
	}
	c := SegmentCriteria(sc, Segment{Beat: 1, Kind: SegmentRequest}, true)
	if !strings.Contains(c[0].Criterion, "By the end of the segment") {
		t.Fatalf("answered is asked of the segment: %s", c[0].Criterion)
	}
	if last := c[len(c)-1]; !last.AllowNA || !strings.Contains(last.Criterion, "NOT claims") {
		t.Fatalf("said_vs_done: %+v", last)
	}
}

func critIDs(cs []SegmentCriterion) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return strings.Join(out, ",")
}

// Beat items go where the probe happened: with declarations, the first
// request segment that performed the item (none: not asked); in an
// older record, every request segment within LocateWindow of the tag,
// n/a allowed.
func TestBeatCriteria(t *testing.T) {
	sc := SimScenario{Agenda: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
		Workday: &Workday{Truth: WorkdayTruth{Clarify: []WorkdayClarify{{Beat: 9, Needed: false, Why: "named"}}, Coherence: []WorkdayProbe{{Beat: 3, Answer: "x"}}}}}
	segs := []Segment{{Beat: 0, Kind: SegmentAnnouncement}, {Beat: 1, Kind: SegmentRequest, Items: []int{1}}, {Beat: 2, Kind: SegmentRequest, Items: []int{2}}, {Beat: 4, Kind: SegmentRequest, Items: []int{3, 4}}, {Beat: 8, Kind: SegmentRequest, Items: []int{8}}}
	got := BeatCriteria(sc, segs, true)
	if critIDs(got[3]) != "coherence_1" || critIDs(got[1])+critIDs(got[2])+critIDs(got[4]) != "" || got[3][0].AllowNA {
		t.Fatalf("declared: %+v", got)
	}
	got = BeatCriteria(sc, segs, false)
	if critIDs(got[1]) != "coherence_1" || critIDs(got[2]) != "coherence_1" || critIDs(got[3]) != "coherence_1" || critIDs(got[4]) != "clarify_1" || critIDs(got[0]) != "" {
		t.Fatalf("located: %+v", got)
	}
	if !got[1][0].AllowNA || !strings.Contains(got[1][0].Criterion, `"n/a" if no CEO line in this SEGMENT says agenda item 3`) {
		t.Fatalf("located criterion: %+v", got[1][0])
	}
}

// A located item keeps the first segment that answered yes or no; an
// item scored nowhere is not exercised; a partial rejudge replaces only
// its families.
func TestResolveLocatedAndNotExercised(t *testing.T) {
	nv := func(n string, a Answer) NamedVerdict { return NamedVerdict{Name: n, Verdict: Verdict{Answer: a}} }
	segs := []SegmentResult{
		{Verdicts: []NamedVerdict{nv("answered", AnswerYes), nv("coherence_1", AnswerNA)}},
		{Verdicts: []NamedVerdict{nv("coherence_1", AnswerNo), nv("said_vs_done", AnswerNA)}},
		{Verdicts: []NamedVerdict{nv("coherence_1", AnswerYes)}},
	}
	ResolveLocated(segs)
	if len(segs[0].Verdicts) != 1 || len(segs[1].Verdicts) != 2 || segs[1].Verdicts[0].Answer != AnswerNo || len(segs[2].Verdicts) != 0 {
		t.Fatalf("resolved %+v", segs)
	}
	sc := SimScenario{Agenda: []string{"a", "b"}, Workday: &Workday{Truth: WorkdayTruth{Coherence: []WorkdayProbe{{Beat: 1}, {Beat: 2}}}}}
	if ne := NotExercised(sc, segs); len(ne) != 1 || ne[0] != "coherence_2" {
		t.Fatalf("not exercised %v", ne)
	}
	old := []SegmentResult{{Beat: 1, Verdicts: []NamedVerdict{nv("answered", AnswerYes), nv("coherence_1", AnswerNo)}}}
	fresh := []SegmentResult{{Beat: 1, Verdicts: []NamedVerdict{nv("coherence_1", AnswerYes), nv("said_vs_done", AnswerNA)}}}
	m := MergeSegments(old, fresh, []string{"coherence", "said_vs_done"})
	if len(m) != 1 || len(m[0].Verdicts) != 3 || m[0].Verdicts[0].Name != "answered" || m[0].Verdicts[1].Answer != AnswerYes {
		t.Fatalf("merged %+v", m)
	}
	if ItemFamily("coherence_12") != "coherence" || ItemFamily("said_vs_done") != "said_vs_done" || ItemFamily("no_unintended_action") != "no_unintended_action" {
		t.Fatal("item family")
	}
}

// n/a is read only where the criterion allows it; elsewhere it is no
// answer (asked again), never a pass.
func TestSegmentNA(t *testing.T) {
	crit := []SegmentCriterion{{ID: "said_vs_done", Criterion: "a", AllowNA: true}, {ID: "answered", Criterion: "b"}}
	vs, err := parseSegmentVerdicts(`{"verdicts":[{"id":"said_vs_done","verdict":"n/a","confidence":1,"evidence":"none: no claim"},{"id":"answered","verdict":"yes","confidence":1,"evidence":"x"}]}`, crit)
	if err != nil || vs[0].Answer != AnswerNA || vs[0].Passed() {
		t.Fatalf("%+v %v", vs, err)
	}
	if _, err := parseSegmentVerdicts(`{"verdicts":[{"id":"said_vs_done","verdict":"yes","confidence":1,"evidence":"x"},{"id":"answered","verdict":"n/a","confidence":1,"evidence":"x"}]}`, crit); err == nil {
		t.Fatal("n/a accepted where not allowed")
	}
}

// The action list: sends with the pending answer, checks with their
// outcome, rail refusals and tool timeouts; the segment sees its span
// and the earlier actions without screen reads.
func TestCallActions(t *testing.T) {
	acts := []SimLine{{AtMS: 5000, Who: "action", Agent: "bench-codex", Kind: ActionKeys, Detail: "esc"}}
	evs := []northstar.Event{
		{AtMS: 1000, Type: "brain.tool_call", Payload: map[string]any{"id": "c1", "name": "read_screen", "args": map[string]any{"agent": "bench-codex"}}},
		{AtMS: 1200, Type: "brain.tool_result", Payload: map[string]any{"id": "c1", "name": "read_screen", "result": "screen"}},
		{AtMS: 2000, Type: "brain.tool_call", Payload: map[string]any{"id": "c2", "name": "run_cli", "args": map[string]any{"agent": "bench-codex"}}},
		{AtMS: 9000, Type: "brain.tool_timeout", Payload: map[string]any{"id": "c2", "name": "run_cli", "agent": "bench-codex"}},
		{AtMS: 9001, Type: "brain.tool_result", Payload: map[string]any{"id": "c2", "name": "run_cli", "result": "timed out"}},
		{AtMS: 5001, Type: "pending.confirmed", Pane: "p1", Payload: map[string]any{"answer": "no", "keys": []any{"esc"}}},
		{AtMS: 6000, Type: "brain.rail_blocked", Payload: map[string]any{"agent": "bench-codex", "keys": []any{"y"}, "reason": "not permitted"}},
	}
	got := CallActions(acts, evs)
	all := ""
	for _, a := range got {
		all += a.Text + "\n"
	}
	for _, want := range []string{"read bench-codex's screen: ok", "answered bench-codex's prompt with keys esc (answer: no): ok", "REFUSED by a rail", "tool run_cli on bench-codex TIMED OUT", "ran a check (run_cli) on bench-codex: TIMED OUT"} {
		if !strings.Contains(all, want) {
			t.Fatalf("lacks %q:\n%s", want, all)
		}
	}
	r := RenderSegmentActions(got, 5500, 5800)
	if strings.Contains(r, "read bench-codex") || !strings.Contains(r, "Earlier in the call (screen reads left out):\n- [t+5.0s] answered") || !strings.Contains(r, "[t+6.0s] tried") || strings.Contains(r, "t+9.0s") {
		t.Fatalf("segment render:\n%s", r)
	}
	if r := RenderSegmentActions(nil, 0, -1); !strings.Contains(r, "none: sheepdog sent") {
		t.Fatalf("empty render: %s", r)
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
	segs, err := JudgeSegments(context.Background(), f, sc, segConvo(), "facts", SegmentOptions{Parallel: 2})
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
	for _, id := range append(defaultRubricOrder, "clarify_1", "coherence_1", "said_vs_done") {
		parts = append(parts, `{"id":"`+id+`","verdict":"yes","confidence":1,"evidence":"e"}`)
	}
	all := braintest.Step{Text: `{"verdicts":[` + strings.Join(parts, ",") + `]}`}
	steps := []braintest.Step{all, all, all, all}
	j := &LLMJudge{Model: braintest.NewModel(steps...), ModelID: "j"}
	segs, err = JudgeSegments(context.Background(), j, sc, segConvo(), "facts", SegmentOptions{Parallel: 1})
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
