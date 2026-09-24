package scorecard

import (
	"math"
	"testing"
)

func approxEqual(a, b float64) bool { return math.Abs(a-b) < 0.001 }

func TestComputeLatency(t *testing.T) {
	events, err := Load("testdata/latency", "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := computeLatency(events)

	if r.AnswerLatencyMS.N != 2 {
		t.Fatalf("AnswerLatencyMS.N = %d, want 2", r.AnswerLatencyMS.N)
	}
	if !approxEqual(r.AnswerLatencyMS.Mean, 3500) {
		t.Fatalf("AnswerLatencyMS.Mean = %v, want 3500 (one id-matched pair at 4000ms, one order-matched pair at 3000ms)", r.AnswerLatencyMS.Mean)
	}

	if r.TimeToFirstWordMS.N != 2 {
		t.Fatalf("TimeToFirstWordMS.N = %d, want 2", r.TimeToFirstWordMS.N)
	}
	if !approxEqual(r.TimeToFirstWordMS.Mean, 3000) {
		t.Fatalf("TimeToFirstWordMS.Mean = %v, want 3000", r.TimeToFirstWordMS.Mean)
	}

	announce, ok := r.BrainTurnDurByKind["announce"]
	if !ok || announce.N != 1 || !approxEqual(announce.Mean, 1500) {
		t.Fatalf("BrainTurnDurByKind[announce] = %+v, ok=%v, want N=1 Mean=1500", announce, ok)
	}
	utterance, ok := r.BrainTurnDurByKind["utterance"]
	if !ok || utterance.N != 1 || !approxEqual(utterance.Mean, 800) {
		t.Fatalf("BrainTurnDurByKind[utterance] = %+v, ok=%v, want N=1 Mean=800", utterance, ok)
	}

	if r.BrainStepPromptTokens.N != 2 || !approxEqual(r.BrainStepPromptTokens.Mean, 400) {
		t.Fatalf("BrainStepPromptTokens = %+v, want N=2 Mean=400", r.BrainStepPromptTokens)
	}
}

func TestComputeStatsEmpty(t *testing.T) {
	s := computeStats(nil)
	if s != (Stats{}) {
		t.Fatalf("computeStats(nil) = %+v, want zero value", s)
	}
}

func TestPercentileSingleValue(t *testing.T) {
	if got := percentile([]float64{42}, 90); got != 42 {
		t.Fatalf("percentile single value = %v, want 42", got)
	}
}
