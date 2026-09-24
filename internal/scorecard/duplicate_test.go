package scorecard

import "testing"

func TestComputeDuplicateSpeech(t *testing.T) {
	events, err := Load("testdata/duplicate", "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := computeDuplicateSpeech(events)
	if r.Count != 1 {
		t.Fatalf("Count = %d, want 1 (only the discoursecopilot push/result pair overlaps)", r.Count)
	}
}
