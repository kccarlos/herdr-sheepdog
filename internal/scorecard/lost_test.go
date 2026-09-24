package scorecard

import "testing"

func TestComputeLost(t *testing.T) {
	events, err := Load("testdata/lost", "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	r := computeLost(events)
	if r.Total != 3 {
		t.Fatalf("Total = %d, want 3 (two voice.turn fallback boundaries, one final:true)", r.Total)
	}
	if r.Lost != 1 {
		t.Fatalf("Lost = %d, want 1 (only the first boundary has no tool_call/transcript.out within 5s)", r.Lost)
	}
	if !approxEqual(r.Rate, 1.0/3.0) {
		t.Fatalf("Rate = %v, want 1/3", r.Rate)
	}
	if r.VoiceInitiativeIgnored != 2 {
		t.Fatalf("VoiceInitiativeIgnored = %d, want 2", r.VoiceInitiativeIgnored)
	}
	if r.EchoIgnored != 1 {
		t.Fatalf("EchoIgnored = %d, want 1", r.EchoIgnored)
	}
}
