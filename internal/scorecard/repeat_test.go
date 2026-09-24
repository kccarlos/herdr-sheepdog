package scorecard

import "testing"

func TestComputeRepeatAsk(t *testing.T) {
	events, err := Load("testdata/repeat", "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := computeRepeatAsk(events)
	if r.Total != 4 {
		t.Fatalf("Total = %d, want 4", r.Total)
	}
	// call2 repeats call1 verbatim (word overlap); call3 is a distinct
	// ask (not a repeat); call4 ("say that again") is a repeat via cue
	// word even though it shares no words with call3.
	if r.Repeats != 2 {
		t.Fatalf("Repeats = %d, want 2", r.Repeats)
	}
	if !approxEqual(r.Rate, 0.5) {
		t.Fatalf("Rate = %v, want 0.5", r.Rate)
	}
}

func TestComputeRepeatAskEmpty(t *testing.T) {
	if r := computeRepeatAsk(nil); r.Total != 0 || r.Rate != 0 {
		t.Fatalf("computeRepeatAsk(nil) = %+v, want zero value", r)
	}
}
