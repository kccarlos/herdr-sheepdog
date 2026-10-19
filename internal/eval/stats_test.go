package eval

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestAggregatePassRateExcludesErroredAndNotRun(t *testing.T) {
	recs := []RunRecord{
		{Case: "a", Status: RunPass},
		{Case: "a", Status: RunFail, Failures: []Failure{{FailJudgeNo, "x"}, {FailTimeout, "y"}}},
		{Case: "a", Status: RunError, Failures: []Failure{{FailError, "429"}}},
		{Case: "a", Status: RunNotRun, Failures: []Failure{{FailNotRun, "budget"}}},
		{Case: "b", Status: RunError, Failures: []Failure{{FailError, "judge"}}},
		{Case: "b", Status: RunNotRun},
	}
	stats, total := Aggregate(recs)
	a, b := stats[0], stats[1]
	if a.Runs != 4 || a.Pass != 1 || a.Fail != 1 || a.Errored != 1 || a.NotRun != 1 || a.PassRate == nil || *a.PassRate != 0.5 {
		t.Fatalf("a = %+v", a)
	}
	if a.Primary[FailTimeout] != 1 || a.Primary[FailJudgeNo] != 0 || a.Labels[FailJudgeNo] != 1 {
		t.Errorf("a labels: primary %v any %v", a.Primary, a.Labels)
	}
	if b.PassRate != nil {
		t.Errorf("b has no graded run, rate = %v", *b.PassRate)
	}
	if total.Runs != 6 || total.Pass != 1 || *total.PassRate != 0.5 {
		t.Errorf("total = %+v", total)
	}
}

func TestResultRunStatus(t *testing.T) {
	for _, tc := range []struct {
		r    Result
		want RunStatus
	}{
		{Result{Status: StatusPass}, RunPass},
		{Result{Status: StatusSkipped}, RunNotRun},
		{Result{Status: StatusFail, Failures: classifySVP(nil, errors.New("brain: all API keys rate limited"))}, RunError},
		{Result{Status: StatusFail, Failures: classifySVP([]string{"spoken has 40 words, more than 20"}, nil)}, RunFail},
	} {
		if got := tc.r.RunStatus(); got != tc.want {
			t.Errorf("%+v: %s, want %s", tc.r, got, tc.want)
		}
	}
}

func TestClassifyReason(t *testing.T) {
	for r, want := range map[string]FailureType{
		reasonFallback + ": brain: fallback (model_error): Error 504, DEADLINE_EXCEEDED":       FailTimeout,
		reasonFallback + ": brain: fallback (model_error): Error 429 Resource exhausted":       FailError,
		reasonFallback + ": brain: fallback (model_error): Error 400 bad":                      FailBrainFallback,
		"none of [prompt] were called (called: read_screen)":                                   FailMissingCall,
		"send_keys completed successfully but must not (a rail-blocked attempt would be fine)": FailUnintendedAction,
		reasonJudgeNo + " (judge): Does it say? — no":                                          FailJudgeNo,
		reasonJudgeError + ": judge error: boom":                                               FailError,
		"needs_user=true, want false":                                                          FailCheck,
	} {
		if got := classifyReason(r); got != want {
			t.Errorf("%q = %s, want %s", r, got, want)
		}
	}
}

// Past the budget, the remaining runs are not_run, never started; the
// coverage stays even (repeat-major order).
func TestCampaignBudgetStopsAsNotRun(t *testing.T) {
	sc := fixScenario()
	sc2 := fixScenario()
	sc2.ID = "fix-test-2"
	costly := func(m *Meter) Judge {
		m.Add("judge", "gemini-3.8-flash", Usage{Calls: 1, In: 1_000_000}) // $0.50 a run
		return &FakeJudge{}
	}
	runs, meter := Campaign(context.Background(), []SimScenario{sc, sc2}, SimOptions{Offline: true, FixtureDir: "../../testdata/herdr", NewJudge: costly}, 3, 1, 0.9, nil)
	if len(runs) != 6 {
		t.Fatalf("%d runs", len(runs))
	}
	statuses := []string{}
	for _, r := range runs {
		statuses = append(statuses, r.Scenario+":"+string(r.Status))
	}
	got := strings.Join(statuses, " ")
	if got != "fix-test:pass fix-test-2:pass fix-test:not_run fix-test-2:not_run fix-test:not_run fix-test-2:not_run" {
		t.Fatalf("statuses = %s", got)
	}
	if meter.Total() < 0.99 || meter.Total() > 1.01 {
		t.Errorf("spend = %v", meter.Total())
	}
	rep := NewCampaignReport(time.Now(), map[string]string{"mode": "test"}, 3, runs, meter)
	var b bytes.Buffer
	rep.WriteMarkdown(&b, 5)
	for _, want := range []string{"| fix-test | 3 | 1 | 0 | 0 | 2 | 100% |", "| not_run | 4 | 4 |", "**TOTAL**"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("markdown lacks %q:\n%s", want, b.String())
		}
	}
}

func TestCostPricesCacheReads(t *testing.T) {
	// 1M input of which 0.8M read from the cache, 0.1M output, on Opus 5.5:
	// 0.2M at the cache-write price ($5) + 0.8M at $0.20 + 0.1M at $20.
	u := Usage{Calls: 1, In: 1_000_000, Cached: 800_000, Out: 100_000}
	if got, want := Cost("claude-opus-5-5", u), 1.0+0.16+2.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("opus cost = %v, want %v", got, want)
	}
	// A model without a cache price bills cached tokens at In, as before.
	if got, want := Cost("deepseek-flash", u), 0.30+0.06; math.Abs(got-want) > 1e-9 {
		t.Errorf("deepseek cost = %v, want %v", got, want)
	}
	if got := stepUsage(map[string]any{"prompt_tokens": int32(10), "completion_tokens": int32(2), "cache_hit_tokens": int32(7)}); got.Cached != 7 || got.In != 10 {
		t.Errorf("stepUsage = %+v", got)
	}
}
