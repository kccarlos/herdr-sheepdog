package eval

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
)

const calibItems = "../../docs/eval/calibration/2026-10-05/items.json"

// The calibration set loads, and its rubric is the production rubric
// (CalibRubric), so the user labels exactly what the judge is asked.
func TestCalibItemsMatchRubric(t *testing.T) {
	f, err := LoadCalibFile(calibItems)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Rubric, CalibRubric()) {
		t.Fatalf("items.json rubric differs from CalibRubric(); regenerate it with `sheepdog eval calib rubric`")
	}
	if n := len(f.Items); n < 24 || n > 30 {
		t.Errorf("%d items, want 24-30", n)
	}
	for _, it := range f.Items {
		if len(it.Turns) > 25 {
			t.Errorf("%s: %d turns, want at most 25", it.ID, len(it.Turns))
		}
	}
}

func TestCalibTranscriptRendering(t *testing.T) {
	it := CalibItem{Turns: []CalibTurn{{"user", "00:01", "status?"}, {"orchestrator", "00:02", "All idle."}}}
	if got := it.Transcript(); !strings.Contains(got, "[00:02] Sheepdog: All idle.") {
		t.Errorf("tier 1 transcript:\n%s", got)
	}
	it.Turns = append(it.Turns, CalibTurn{"voice", "00:03", "Everyone is idle."})
	got := it.Transcript()
	if !strings.Contains(got, "not heard by the CEO): All idle.") || !strings.Contains(got, "Sheepdog (voice, heard by the CEO): Everyone is idle.") {
		t.Errorf("voice transcript:\n%s", got)
	}
}

func TestJudgeCalib(t *testing.T) {
	f := CalibFile{Version: 1, Rubric: CalibRubric(), Items: []CalibItem{{ID: "a", Source: "s", Lang: "en", Context: "c",
		Turns: []CalibTurn{{"user", "00:00", "hi"}}, RubricIDs: []string{"answered", "length_fits"}}}}
	j := &FakeJudge{Rules: []FakeRule{{Contains: "length of each", Answer: AnswerNo}}}
	vs := JudgeCalib(context.Background(), j, f, 2, nil)
	if len(vs) != 2 || vs[0].Verdict != "pass" || vs[1].Verdict != "fail" || vs[1].RubricID != "length_fits" || vs[0].Evidence == "" {
		t.Fatalf("verdicts %+v", vs)
	}
	if in := j.Inputs()[0]; in.Facts != "c" || !in.Final || !strings.Contains(in.Criterion, "Fail when:") {
		t.Errorf("judge input %+v", in)
	}
}

func TestCohenKappa(t *testing.T) {
	// The textbook 2x2 case: 20 yes/yes, 5 yes/no, 10 no/yes, 15 no/no.
	var a, b []string
	add := func(n int, x, y string) {
		for i := 0; i < n; i++ {
			a, b = append(a, x), append(b, y)
		}
	}
	add(20, "pass", "pass")
	add(5, "pass", "fail")
	add(10, "fail", "pass")
	add(15, "fail", "fail")
	k := CohenKappa(a, b)
	if k == nil || math.Abs(*k-0.4) > 1e-9 {
		t.Fatalf("kappa = %v, want 0.4", k)
	}
	if CohenKappa([]string{"pass", "pass"}, []string{"pass", "pass"}) != nil || CohenKappa(nil, nil) != nil {
		t.Error("kappa must be undefined with one label throughout or no pairs")
	}
}

func TestAgreement(t *testing.T) {
	labels := []CalibLabel{
		{"i1", "answered", "pass", ""}, {"i1", "language", "fail", "zh answer"},
		{"i2", "answered", "fail", ""}, {"i2", "language", "na", ""},
		{"i3", "answered", "pass", ""},
	}
	verdicts := []CalibVerdict{
		{ItemID: "i1", RubricID: "answered", Verdict: "pass"}, {ItemID: "i1", RubricID: "language", Verdict: "pass", Evidence: "Sheepdog: 好的"},
		{ItemID: "i2", RubricID: "answered", Verdict: "fail"}, {ItemID: "i2", RubricID: "language", Verdict: "pass"},
		{ItemID: "i4", RubricID: "answered", Verdict: "pass"},
	}
	rep := Agreement(labels, verdicts)
	if rep.Overall.N != 3 || rep.Overall.Agree != 2 || rep.Overall.CEONA != 1 || rep.Overall.Missing != 2 {
		t.Fatalf("overall %+v", rep.Overall)
	}
	if len(rep.Disagreements) != 1 || rep.Disagreements[0].Evidence != "Sheepdog: 好的" || rep.Disagreements[0].Note != "zh answer" {
		t.Fatalf("disagreements %+v", rep.Disagreements)
	}
	var b strings.Builder
	WriteAgreement(&b, rep)
	if !strings.Contains(b.String(), "| answered | 2/2 (100 %) |") {
		t.Errorf("report:\n%s", b.String())
	}
}
