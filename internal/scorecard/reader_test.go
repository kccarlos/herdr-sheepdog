package scorecard

import (
	"testing"
)

func TestLoadFiltersByDayAndSkipsMalformedLines(t *testing.T) {
	dir := "testdata/dayrange"

	all, err := Load(dir, "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Three day files, one daemon.start each, plus one malformed line in
	// day 3 that must be skipped rather than aborting the read.
	if len(all) != 3 {
		t.Fatalf("Load(unbounded) = %d events, want 3", len(all))
	}

	bounded, err := Load(dir, "2026-01-02", "2026-01-02")
	if err != nil {
		t.Fatalf("Load(bounded): %v", err)
	}
	if len(bounded) != 1 {
		t.Fatalf("Load(2026-01-02..2026-01-02) = %d events, want 1", len(bounded))
	}
	if got := decode[struct {
		Version string `json:"version"`
	}](bounded[0].Payload).Version; got != "day2" {
		t.Fatalf("bounded[0].version = %q, want day2", got)
	}
}

func TestLoadMissingDirIsEmptyNotError(t *testing.T) {
	events, err := Load("testdata/does-not-exist", "", "")
	if err != nil {
		t.Fatalf("Load(missing dir): %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Load(missing dir) = %d events, want 0", len(events))
	}
}

func TestSplitSessions(t *testing.T) {
	events, err := Load("testdata/sessions", "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sessions := SplitSessions(events)
	if len(sessions) != 3 {
		t.Fatalf("SplitSessions = %d sessions, want 3 (pre + 2 daemon.start runs)", len(sessions))
	}
	if sessions[0].Label != "pre-daemon.start" || len(sessions[0].Events) != 1 {
		t.Fatalf("session 0 = %+v, want label pre-daemon.start with 1 event", sessions[0])
	}
	if len(sessions[1].Events) != 2 {
		t.Fatalf("session 1 has %d events, want 2 (its daemon.start + one tool_call)", len(sessions[1].Events))
	}
	if len(sessions[2].Events) != 2 {
		t.Fatalf("session 2 has %d events, want 2", len(sessions[2].Events))
	}
	// Every session after the first must be labelled by its own
	// daemon.start timestamp, not reuse another session's label.
	if sessions[1].Label == sessions[2].Label {
		t.Fatalf("sessions 1 and 2 share a label %q", sessions[1].Label)
	}
}
