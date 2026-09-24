package scorecard

import "testing"

func TestComputeCtrlC(t *testing.T) {
	events, err := Load("testdata/ctrlc", "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := computeCtrlC(events)

	if r.Total != 2 {
		t.Fatalf("Total = %d, want 2 (the plain 'y' send must not count)", r.Total)
	}
	if r.PrecededByStopCount != 1 {
		t.Fatalf("PrecededByStopCount = %d, want 1", r.PrecededByStopCount)
	}
	if !r.Sends[0].PrecededByStop {
		t.Fatalf("Sends[0].PrecededByStop = false, want true (a stop tool_call preceded it by 2s)")
	}
	if r.Sends[0].TriggerText != "Stop it, right now" {
		t.Fatalf("Sends[0].TriggerText = %q, want %q", r.Sends[0].TriggerText, "Stop it, right now")
	}
	if r.Sends[1].PrecededByStop {
		t.Fatalf("Sends[1].PrecededByStop = true, want false (no stop tool_call preceded it)")
	}
	if r.Sends[1].TriggerText != "Never mind, cancel that" {
		t.Fatalf("Sends[1].TriggerText = %q, want %q", r.Sends[1].TriggerText, "Never mind, cancel that")
	}
}
