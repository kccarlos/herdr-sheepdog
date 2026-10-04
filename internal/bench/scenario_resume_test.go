package bench

import "testing"

// TestResumeAt: behavior.resume recycles after after_turn and after each
// of after_turns (two resumes with a turn between them).
func TestResumeAt(t *testing.T) {
	var none *Resume
	if none.At(1) {
		t.Error("nil resume recycles")
	}
	r := &Resume{AfterTurns: []int{2, 3}}
	for turn, want := range map[int]bool{1: false, 2: true, 3: true, 4: false} {
		if r.At(turn) != want {
			t.Errorf("At(%d) = %v", turn, !want)
		}
	}
	if !(&Resume{AfterTurn: 2}).At(2) {
		t.Error("after_turn ignored")
	}
}
