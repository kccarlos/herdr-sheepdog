package voice

import (
	"strings"
	"testing"
)

// TestRecapRecentKeepsTheNewestWithinBudget: newest first within the
// budget, returned oldest first, the oldest included cut to its tail.
func TestRecapRecentKeepsTheNewestWithinBudget(t *testing.T) {
	r := Recap{Max: 3}
	r.Add(RecapUser, "dropped by Max")
	r.Add(RecapUser, "0123456789")
	r.Add(RecapAssistant, "abcde")
	r.Add(RecapSupervisor, "  ")
	r.Add(RecapSupervisor, "XYZ")
	if r.Len() != 3 {
		t.Fatalf("Len = %d, want 3", r.Len())
	}
	got := r.Recent(12)
	if len(got) != 3 || got[0].Text != "6789" || got[1].Text != "abcde" || got[2].Text != "XYZ" {
		t.Errorf("Recent = %+v", got)
	}
	if lines := RecapLines(got); lines != "Shepherd: 6789\nYou: abcde\nSupervisor update: XYZ" {
		t.Errorf("RecapLines = %q", lines)
	}
}

// TestRecapCutKeepsWhatWasHeard: a cut reply is replaced by its heard
// part; a reply heard not at all leaves the recap.
func TestRecapCutKeepsWhatWasHeard(t *testing.T) {
	r := Recap{}
	r.Add(RecapAssistant, "Auth is running the tests and then it deploys.")
	if !r.Cut("Auth is running the tests and then it deploys.", "Auth is running", 1500) {
		t.Fatal("no entry cut")
	}
	if lines := RecapLines(r.Recent(100)); !strings.HasPrefix(lines, "You (cut off after 2 s): Auth is running") {
		t.Errorf("lines = %q", lines)
	}
	r.Add(RecapAssistant, "Docs finished.")
	r.Cut("Docs finished.", "", 0)
	if got := r.Recent(100); len(got) != 1 {
		t.Errorf("an unheard reply stayed: %+v", got)
	}
}
