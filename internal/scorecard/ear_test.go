package scorecard

import "testing"

func TestComputeEar(t *testing.T) {
	events, err := Load("testdata/ear", "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := computeEar(events)

	// Spoken pool: the tool_result.spoken line and the one push whose
	// urgency is not silent; the silent push is excluded.
	if r.Spoken.N != 2 {
		t.Fatalf("Spoken.N = %d, want 2", r.Spoken.N)
	}
	if !approxEqual(r.Spoken.MeanWords, 8) {
		t.Fatalf("Spoken.MeanWords = %v, want 8", r.Spoken.MeanWords)
	}

	// Dropped pool: only the push_late with added:false and non-empty
	// spoken text; the added:true line and the empty-spoken line are
	// excluded.
	if r.DroppedBrainLines.N != 1 {
		t.Fatalf("DroppedBrainLines.N = %d, want 1", r.DroppedBrainLines.N)
	}
	if r.DroppedBrainLines.ShareWithDash != 1 {
		t.Fatalf("DroppedBrainLines.ShareWithDash = %v, want 1 (the one dropped line has an em dash)", r.DroppedBrainLines.ShareWithDash)
	}
}
