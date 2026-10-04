package scorecard

import "testing"

func TestComputeResumeRetention(t *testing.T) {
	events, err := Load("testdata/resume", "", "")
	if err != nil {
		t.Fatal(err)
	}
	r := computeResumeRetention(events)
	if r.Resumes != 2 || len(r.Days) != 1 {
		t.Fatalf("resumes = %d, days = %d, want 2 and 1 (a fresh session is not a resume)", r.Resumes, len(r.Days))
	}
	first, second := r.Rows[0], r.Rows[1]
	if first.Source != "derived" || first.Before != 10000 || first.After != 7000 || first.HandlePrompt != 10000 || !approxEqual(first.Ratio, 0.7) {
		t.Errorf("derived row = %+v", first)
	}
	if second.Source != "resume_context" || second.Before != 8000 || second.After != 6500 || second.AfterByMod["AUDIO"] != 5000 {
		t.Errorf("resume_context row = %+v (the derived row of the same resume must be replaced)", second)
	}
	d := r.Days[0]
	if first.Generation != 1 || second.Generation != 2 || d.FirstN != 1 || d.LaterN != 1 || !approxEqual(d.FirstRatio, 0.7) {
		t.Errorf("generations: %d %d, day %+v", first.Generation, second.Generation, d)
	}
	if d.Day != "2026-10-04" || d.Before != 18000 || d.After != 13500 || d.Derived != 1 || !approxEqual(d.MinRatio, 0.7) || d.ModalityAfter["AUDIO"] != 5000 {
		t.Errorf("day = %+v", d)
	}
}

// TestResumeRetentionFreshAfterResume: a fresh session opened instead of
// resuming a resumed one (voice.handle_dropped resumed_twice) is no
// resume row; it is counted per day, and the next resume is a first
// resume again (the daemon's logged generation).
func TestResumeRetentionFreshAfterResume(t *testing.T) {
	events, err := Load("testdata/resume-fresh", "", "")
	if err != nil {
		t.Fatal(err)
	}
	r := computeResumeRetention(events)
	if r.Resumes != 2 || len(r.Days) != 1 {
		t.Fatalf("resumes = %d, days = %d, want 2 and 1", r.Resumes, len(r.Days))
	}
	d := r.Days[0]
	if d.FreshAfterResume != 1 || d.FirstN != 2 || d.LaterN != 0 || !approxEqual(d.MinRatio, 1.0125) {
		t.Errorf("day = %+v", d)
	}
	for _, row := range r.Rows {
		if row.Generation != 1 {
			t.Errorf("row %+v: want generation 1", row)
		}
	}
}
