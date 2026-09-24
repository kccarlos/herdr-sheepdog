package scorecard

import (
	"os"
	"strings"
	"testing"
)

// TestAnalyzeOverRealE2EFixture is the one non-synthetic test the WP0a
// brief asks for: run Analyze over a real recorded fixture and confirm it
// never crashes or errors, even though the fixture (from before the brain
// package existed: it logs "llm.call", not "brain.*", and carries no
// event ids at all) is a good example of the schema drift
// docs/architecture.md warns about. Per the brief, this test is skipped
// if the fixture cannot be parsed at all rather than failing the suite.
func TestAnalyzeOverRealE2EFixture(t *testing.T) {
	const path = "../../testdata/e2e/2026-09-17-e2e.jsonl"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	if len(data) == 0 {
		t.Skip("fixture is empty")
	}

	tmp := t.TempDir()
	if err := os.WriteFile(tmp+"/2026-09-17.jsonl", data, 0o600); err != nil {
		t.Fatalf("stage fixture: %v", err)
	}

	events, err := Load(tmp, "", "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) == 0 {
		t.Skip("fixture did not parse as any events")
	}

	report := Analyze("2026-09-17-e2e", events)
	if report.Events != len(events) {
		t.Fatalf("report.Events = %d, want %d", report.Events, len(events))
	}
	// The point of this test is that nothing above panicked or errored on
	// a log that predates half of today's event types; a sanity check
	// that markdown/JSON rendering also survives it.
	var sb strings.Builder
	if err := WriteMarkdown(&sb, []Report{report}, "test"); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	if sb.Len() == 0 {
		t.Fatal("WriteMarkdown produced no output")
	}
}
