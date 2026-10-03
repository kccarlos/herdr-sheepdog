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

// The record also finds an agent by its spoken name or its session
// title, the names the CEO actually says.
func TestAgentRecordsBySpokenNameAndTitle(t *testing.T) {
	now := time.Date(2026, 10, 3, 6, 20, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	agents := []AgentInfo{
		{Alias: "sheepdog-claude", Spoken: "README Claude", Title: "README", State: "done"},
		{Alias: "sheepdog-claude-2", Spoken: "Fix flaky test Claude", Title: "Fix flaky test", State: "working"},
	}
	got := agentRecords(agents, "did README Claude push yet?", clock)
	if !strings.Contains(got, `- sheepdog-claude ("README Claude"): done`) || strings.Contains(got, "claude-2") {
		t.Errorf("by spoken name:\n%s", got)
	}
	got = agentRecords(agents, "what did the flaky test one change", clock)
	if got != "" {
		t.Errorf("a partial title matched:\n%s", got)
	}
	got = agentRecords(agents, "is the fix flaky test one done?", clock)
	if !strings.Contains(got, "sheepdog-claude-2") || strings.Contains(got, "- sheepdog-claude (") {
		t.Errorf("by title:\n%s", got)
	}
}

// Tier-2 bench 2026-10-03 #6: "Let me know when bench-claude is done."
// reached the brain as "Oh, I know when Bench Squad is done." and the
// brain invented a rename. The request now states that no agent has the
// heard name, with the closest names, so the brain can ask.
func TestUnmatchedHeardName(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 10, 3, 6, 20, 0, 0, time.UTC) }
	agents := []AgentInfo{
		{Alias: "bench-codex", State: "working"},
		{Alias: "bench-claude", Spoken: "bench-claude", State: "working"},
	}
	for _, words := range []string{
		"Oh, I know when Bench Squad is done.",
		"Oh, I know when Bench Quad is done.",
		"Let me know when Bench Clad is done.",
		"Was Bench Clear done?",
	} {
		got := agentRecords(agents, words, clock)
		if !strings.HasPrefix(got, "No agent is called \"Bench ") || !strings.Contains(got, "none was renamed to that") {
			t.Errorf("%q: line = %q", words, got)
			continue
		}
		if i, j := strings.Index(got, `"bench-claude"`), strings.Index(got, `"bench-codex"`); i < 0 || j < 0 || i > j {
			t.Errorf("%q: closest names not nearest first: %q", words, got)
		}
	}
	if got := agentRecords(agents, "Oh, I know when Bench Squad is done.", clock); got != `No agent is called "Bench Squad", and none was renamed to that; the closest names: "bench-claude", "bench-codex". Speech recognition can mishear a name.` {
		t.Errorf("line = %q", got)
	}
	// Not a name: a lower-case word after the prefix, no prefix at all,
	// or an agent that is named.
	for _, words := range []string{"is the bench done?", "how is everyone doing?", "what about bench-claude-2", "Let me know when Bench Claude is done."} {
		if got := unmatchedNameLine(agents, words); got != "" {
			t.Errorf("%q: line = %q", words, got)
		}
	}
	// A spoken name's first word counts too, and the spoken name is
	// listed with its alias.
	spoken := []AgentInfo{{Alias: "sheepdog-claude", Spoken: "README Claude"}, {Alias: "sheepdog-claude-2", Spoken: "Parser Claude"}}
	if got := unmatchedNameLine(spoken, "Is README Cloud finished?"); !strings.Contains(got, `"README Cloud"`) || !strings.Contains(got, `"README Claude" (sheepdog-claude), "Parser Claude" (sheepdog-claude-2)`) {
		t.Errorf("spoken: %q", got)
	}
}
