package brain

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The journal adds at most ~800 characters to the ledger for four agents
// with full journals (docs/dev/iteration-2.md WP4), every prompt.
func TestJournalLedgerBudget(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	long := strings.Repeat("The worker wrote a long careful summary of what it did. ", 12)
	var bare, full []AgentInfo
	for i, alias := range []string{"auth", "docs", "vps-one", "calc"} {
		a := AgentInfo{Alias: alias, Kind: "claude", State: "idle", LastTask: "run the tests", LastResult: "finished"}
		bare = append(bare, a)
		for j := 0; j < 20; j++ {
			a.Journal = append(a.Journal, JournalEntry{At: now.Add(-time.Duration(i*20+j) * time.Minute), Event: "blocked", LastAssistant: long, Instruction: long, Files: []string{"a.go", "b.go"}, Tools: map[string]int{"Edit": 3}})
		}
		full = append(full, a)
	}
	before := renderLedger(bare, nil, nil, now)
	after := renderLedger(full, nil, nil, now)
	delta := utf8.RuneCountInString(after) - utf8.RuneCountInString(before)
	t.Logf("ledger: %d -> %d chars (+%d for 4 agents x %d entries)", utf8.RuneCountInString(before), utf8.RuneCountInString(after), delta, journalLedgerEntries)
	if delta > 800 {
		t.Errorf("the journal grew the ledger by %d chars:\n%s", delta, after)
	}
	if strings.Count(after, "\n  - blocked ") != 4*journalLedgerEntries {
		t.Errorf("want %d journal lines:\n%s", 4*journalLedgerEntries, after)
	}
}

func TestJournalLedgerLines(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	j := []JournalEntry{
		{At: now.Add(-9 * time.Minute), Event: "done", LastAssistant: "oldest, not shown"},
		{At: now.Add(-3 * time.Minute), Event: "done", LastAssistant: "Fixed the rounding."},
		{At: now.Add(-2 * time.Minute), Event: "blocked", Instruction: "run the migrations"},
		{At: now.Add(-1 * time.Minute), Event: "idle"},
	}
	got := journalLedgerLines(j, now)
	want := "  - done 3 min ago: \"Fixed the rounding.\"\n  - blocked 2 min ago: \"asked: run the migrations\"\n"
	if got != want {
		t.Errorf("lines =\n%q\nwant\n%q", got, want)
	}
	// An agent with only a journal still gets its ledger line.
	ledger := renderLedger([]AgentInfo{{Alias: "auth", Journal: j}}, nil, nil, now)
	if !strings.Contains(ledger, "- auth: recent work\n  - done 3 min ago") {
		t.Errorf("ledger:\n%s", ledger)
	}
}

func TestWorkerWroteBelongsToTheCurrentState(t *testing.T) {
	since := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	a := AgentInfo{StateSince: since, Journal: []JournalEntry{{At: since, Event: "done", LastAssistant: "Fixed it."}}}
	if got := workerWrote(a); got != "Fixed it." {
		t.Errorf("workerWrote = %q", got)
	}
	a.StateSince = since.Add(time.Second)
	if got := workerWrote(a); got != "" {
		t.Errorf("an entry from before the current state was taken: %q", got)
	}
	if got := workerWrote(AgentInfo{StateSince: since}); got != "" {
		t.Errorf("no journal: %q", got)
	}
}

func TestJournalRecallLine(t *testing.T) {
	e := JournalEntry{At: time.Date(2026, 9, 24, 10, 4, 0, 0, time.UTC), Event: "done", LastAssistant: "Fixed the rounding.", Instruction: "fix it", Files: []string{"total.go"}, Tools: map[string]int{"Edit": 2, "Bash": 1}}
	want := `auth journal (10:04, done): wrote "Fixed the rounding."; instruction "fix it"; files total.go; tools Bash×1, Edit×2`
	if got := journalRecallLine("auth", e); got != want {
		t.Errorf("recall line =\n%s\nwant\n%s", got, want)
	}
}

func TestStopErrorFormattingInBrain(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 5, 0, 0, time.UTC)

	st := sessionText(SessionRead{
		Instruction: "Audit the auth handler.",
		StopError:   "stopped because the model provider returned 529 overloaded",
		StopDetail:  "API Error: 529 Overloaded",
	})
	if !strings.Contains(st, "How its last turn ended: stopped because the model provider returned 529 overloaded (API Error: 529 Overloaded)\n") {
		t.Errorf("sessionText missing stop error:\n%s", st)
	}

	reqText := eventRequestText(Request{
		Kind:      KindAnnounce,
		Agent:     "auth",
		Event:     "done",
		Template:  "auth stopped because the model provider returned 529 overloaded.",
		Owed:      true,
		StopError: "stopped because the model provider returned 529 overloaded",
	})
	if !strings.HasPrefix(reqText, `Event from the supervisor (announce): agent="auth" event="done" template="auth stopped because the model provider returned 529 overloaded."`+"\n") {
		t.Errorf("first line shape changed:\n%s", reqText)
	}
	if !strings.Contains(reqText, "did not finish cleanly") || strings.Contains(reqText, "not only that it finished") {
		t.Errorf("eventRequestText did not frame stop error honestly:\n%s", reqText)
	}

	j := []JournalEntry{
		{At: now.Add(-2 * time.Minute), Event: "done", Error: "stopped because it hit its usage limit, resets 3pm", Instruction: "refactor billing"},
		{At: now.Add(-1 * time.Minute), Event: "done", Error: "stopped because the model provider returned 529 overloaded", LastAssistant: "Checked two files first."},
	}
	ledger := journalLedgerLines(j, now)
	if !strings.Contains(ledger, "stopped because it hit its") || !strings.Contains(ledger, "stopped because the model") {
		t.Errorf("ledger hid stop errors:\n%s", ledger)
	}
	recall := journalRecallLine("auth", j[0])
	if !strings.Contains(recall, "stopped because it hit its usage limit, resets 3pm") {
		t.Errorf("recall missing stop error:\n%s", recall)
	}
	rec := agentRecords([]AgentInfo{{
		Alias:      "auth",
		State:      "done",
		StateSince: now.Add(-time.Minute),
		LastResult: "stopped because the model provider returned 529 overloaded",
		Journal:    j,
	}}, "how is auth doing?", "", func() time.Time { return now })
	if !strings.Contains(rec, "stopped because the model provider returned 529 overloaded") {
		t.Errorf("agentRecords missing stop error:\n%s", rec)
	}
}
