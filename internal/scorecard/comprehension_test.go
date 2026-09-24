package scorecard

import "testing"

func TestComputeComprehension(t *testing.T) {
	events, err := Load("testdata/comprehension", "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := computeComprehension(events); got != 3 {
		t.Fatalf("computeComprehension = %d, want 3 (\"what does that mean\", \"explain\", the Chinese phrase)", got)
	}
}
