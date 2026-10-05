package eval

import (
	"context"
	"strings"
	"testing"
)

// The 2026-10-05 phone test, shortened: the orchestrator gives two key
// points, the voice says one; on "more detail" the voice rephrases.
func TestScoreExplainSeparatesTheSides(t *testing.T) {
	spec := &ExplainSpec{
		Turns:     []string{ExplainKindExplain, ExplainKindMore},
		KeyPoints: []string{"about 1,500 characters of regular expressions", "three word lists disagree"},
	}
	ex := []ExplainExchange{
		{Turn: 1, Kind: ExplainKindExplain, Question: "讲讲硬编码规则", Consulted: true,
			Orchestrator: "There are about 1,500 characters of regular expressions. Three word lists disagree.",
			Voice:        "There are about 1,500 characters of regular expressions."},
		{Turn: 2, Kind: ExplainKindMore, Question: "你可以给我一点详细的解释吗？", Consulted: false,
			Voice: "Again: regular expressions."},
	}
	// The fake judge: a point is stated when its key words are in the
	// last answer; depth fails on a rephrase; comprehension yes for the
	// side that mentions the word lists.
	last := func(in JudgeInput) string {
		lines := strings.Split(strings.TrimSpace(in.Transcript), "\n")
		return lines[len(lines)-1]
	}
	j := &FakeJudge{Rules: []FakeRule{
		{Contains: "1,500 characters", Fn: func(in JudgeInput) Answer { return yesIf(strings.Contains(last(in), "1,500")) }},
		{Contains: "word lists disagree", Fn: func(in JudgeInput) Answer { return yesIf(strings.Contains(last(in), "word lists")) }},
		{Contains: "asks for more", Fn: func(in JudgeInput) Answer { return yesIf(!strings.Contains(last(in), "Again")) }},
		{Contains: "listener", Fn: func(in JudgeInput) Answer { return yesIf(strings.Contains(in.Transcript, "word lists")) }},
	}}
	sc := ScoreExplain(context.Background(), j, spec, "zh", ex, true)
	if sc.Orchestrator.Coverage == nil || *sc.Orchestrator.Coverage != 1 {
		t.Fatalf("orchestrator coverage = %v, want 1", sc.Orchestrator.Coverage)
	}
	if sc.Voice.Coverage == nil || *sc.Voice.Coverage != 0.5 || *sc.Voice.OfOrchestrator != 0.5 {
		t.Fatalf("voice coverage = %v of orchestrator %v, want 0.5 and 0.5", sc.Voice.Coverage, sc.Voice.OfOrchestrator)
	}
	if *sc.Orchestrator.Comprehension != 1 || *sc.Voice.Comprehension != 0 {
		t.Fatalf("comprehension = %v / %v", *sc.Orchestrator.Comprehension, *sc.Voice.Comprehension)
	}
	// The orchestrator was not asked the follow-up: only the voice is
	// judged on it, and fails.
	if sc.Orchestrator.DepthAsked != 0 || sc.Voice.DepthAsked != 1 || sc.Voice.DepthPassed != 0 {
		t.Fatalf("depth orchestrator %d/%d voice %d/%d", sc.Orchestrator.DepthPassed, sc.Orchestrator.DepthAsked, sc.Voice.DepthPassed, sc.Voice.DepthAsked)
	}
	if sc.NotConsulted != 1 || sc.FollowUpsNotConsulted != 1 {
		t.Fatalf("not consulted %d, follow-ups %d", sc.NotConsulted, sc.FollowUpsNotConsulted)
	}
	fs := sc.Failures()
	if len(fs) != 1 || fs[0].Type != FailJudgeNo || !strings.Contains(fs[0].Detail, "explain_depth_t2_more (voice)") {
		t.Fatalf("failures = %+v", fs)
	}
	sum := SummarizeExplain([]*ExplainScore{sc, nil})
	if sum.Runs != 1 || sum.Voice == nil || *sum.Voice.WordsZH <= 0 || sum.Orchestrator.WordsEN != nil {
		t.Fatalf("summary = %+v", sum)
	}
	if line := sum.Line(); !strings.Contains(line, "voice coverage 0.50 (0.50 of the orchestrator's points)") || !strings.Contains(line, "1 of 1 follow-ups answered without the orchestrator") {
		t.Fatalf("line = %s", line)
	}
}

func TestScoreExplainControlPaddingOnly(t *testing.T) {
	spec := &ExplainSpec{Turns: []string{ExplainKindSimple}}
	ex := []ExplainExchange{{Turn: 1, Kind: ExplainKindSimple, Question: "Is bench-codex done?", Consulted: true, Orchestrator: "Yes, all 128 tests passed."}}
	j := &FakeJudge{Rules: []FakeRule{{Contains: "simple one", Answer: AnswerNo}}}
	sc := ScoreExplain(context.Background(), j, spec, "en", ex, false)
	if sc.Voice != nil || sc.Orchestrator.Coverage != nil || sc.Orchestrator.Comprehension != nil {
		t.Fatalf("a control has no coverage or comprehension: %+v", sc)
	}
	if sc.Orchestrator.PaddingAsked != 1 || sc.Orchestrator.PaddingPassed != 0 || len(sc.Failures()) != 1 {
		t.Fatalf("padding %d/%d failures %v", sc.Orchestrator.PaddingPassed, sc.Orchestrator.PaddingAsked, sc.Failures())
	}
	if got := len(j.Inputs()); got != 1 {
		t.Fatalf("judge calls = %d, want 1", got)
	}
}

func TestExplainSpecValidate(t *testing.T) {
	base := SimScenario{ID: "x", Goal: "g", Fixed: true, Script: []string{"a", "b"}, Agents: []SimAgent{{Alias: "w", Screen: "s"}}}
	for _, c := range []struct {
		spec ExplainSpec
		ok   bool
	}{
		{ExplainSpec{Turns: []string{"explain", "more"}, KeyPoints: []string{"p"}}, true},
		{ExplainSpec{Turns: []string{"simple", "simple"}}, true},
		{ExplainSpec{Turns: []string{"explain", "more"}}, false},
		{ExplainSpec{Turns: []string{"explain"}, KeyPoints: []string{"p"}}, false},
		{ExplainSpec{Turns: []string{"explain", "huh"}, KeyPoints: []string{"p"}}, false},
	} {
		s := base
		spec := c.spec
		s.Explain = &spec
		if err := s.validate(); (err == nil) != c.ok {
			t.Errorf("%+v: err = %v, want ok %v", c.spec, err, c.ok)
		}
	}
}

func TestCountWords(t *testing.T) {
	for s, want := range map[string]int{"It's 85, not 100.": 4, "备忘录满 5 条": 6, "": 0} {
		if got := CountWords(s); got != want {
			t.Errorf("CountWords(%q) = %d, want %d", s, got, want)
		}
	}
}

func yesIf(b bool) Answer {
	if b {
		return AnswerYes
	}
	return AnswerNo
}
