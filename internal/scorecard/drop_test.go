package scorecard

import "testing"

func TestComputeDrop(t *testing.T) {
	events, err := Load("testdata/drop", "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := computeDrop(events)

	if r.Total != 4 {
		t.Fatalf("Total = %d, want 4", r.Total)
	}
	if r.Dropped != 3 {
		t.Fatalf("Dropped = %d, want 3 (all but the added:true line)", r.Dropped)
	}
	if !approxEqual(r.DropRate, 0.75) {
		t.Fatalf("DropRate = %v, want 0.75", r.DropRate)
	}
	if r.DroppedNeedsUser != 2 {
		t.Fatalf("DroppedNeedsUser = %d, want 2", r.DroppedNeedsUser)
	}
	if r.DroppedNotNeedsUser != 1 {
		t.Fatalf("DroppedNotNeedsUser = %d, want 1", r.DroppedNotNeedsUser)
	}
	if r.DroppedEmptySpoken != 1 {
		t.Fatalf("DroppedEmptySpoken = %d, want 1", r.DroppedEmptySpoken)
	}

	want := map[string]int{"timeout": 2, "rate_limited": 1, "unknown": 1}
	if len(r.FallbackByReason) != len(want) {
		t.Fatalf("FallbackByReason = %v, want %v", r.FallbackByReason, want)
	}
	for reason, n := range want {
		if r.FallbackByReason[reason] != n {
			t.Fatalf("FallbackByReason[%s] = %d, want %d", reason, r.FallbackByReason[reason], n)
		}
	}
}
