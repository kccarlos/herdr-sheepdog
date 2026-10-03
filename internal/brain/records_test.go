package brain

import (
	"strings"
	"testing"
	"time"
)

// Run C, H7 (5/5 Vertex runs of bench-unknown-fact): asked "Did
// bench-claude push its changes yet?", the brain said it had not, from a
// screen that says nothing about pushing. The utterance request now
// carries what the daemon has on record for the agent the words name,
// with times, and says outright when there is no journal entry.
func TestAgentRecordsForTheNamedAgent(t *testing.T) {
	now := time.Date(2026, 10, 3, 6, 20, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	agents := []AgentInfo{
		{Alias: "bench-claude", State: "done", StateSince: now.Add(-2 * time.Minute)},
		{Alias: "bench-codex", State: "working", Journal: []JournalEntry{{At: now.Add(-time.Minute), Event: "done", LastAssistant: "All tests pass.", Files: []string{"clamp.go"}, Tools: map[string]int{"Bash": 2}}}},
	}
	got := agentRecords(agents, "Did bench-claude push its changes yet?", clock)
	for _, want := range []string{
		"On record (from the worker's screen and its own session transcript, the daemon's only sources; what is not here is not on record, and a run_cli or investigate look can check it):",
		"- bench-claude: done since 06:18:00 (2 min ago); no journal entry",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("record lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "bench-codex") {
		t.Errorf("an agent the words do not name is in the record:\n%s", got)
	}
	got = agentRecords(agents, "what did bench codex do", clock)
	if !strings.Contains(got, `- bench-codex: working; journal: [06:19:00 done] wrote "All tests pass." files clamp.go tools Bash×2;`) {
		t.Errorf("journal record:\n%s", got)
	}
	if got := agentRecords(agents, "how is everyone doing?", clock); got != "" {
		t.Errorf("no name, record = %q", got)
	}
	if got := agentRecords(agents, "what about bench-claude-2", clock); got != "" {
		t.Errorf("a longer name matched: %q", got)
	}
}

func TestRequestTextCarriesTheRecord(t *testing.T) {
	req := Request{Kind: KindUtterance, Question: "Did bench-claude push?", records: "On record (x):\n- bench-claude: done; no journal entry: beyond this and its screen, nothing it did is on record."}
	if text := requestText(req); !strings.Contains(text, req.records) {
		t.Errorf("request:\n%s", text)
	}
}
