package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// speedNow is the clock of the budget fixtures.
var speedNow = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

// fullFlock is four agents with everything filled in and every free text
// long: tasks, results, questions, a pending prompt, five journal
// entries each (one of them from two days before), plus notes, promises
// and investigations — the worst case the state block must fit.
func fullFlock() ([]AgentInfo, []string, []PromiseInfo, []InvestigationInfo) {
	long := strings.Repeat("Approved — go ahead with the playback-policy fix and report the provider it uses. ", 8)
	var agents []AgentInfo
	for i, alias := range []string{"discoursecopilot-claude", "herdr-sheepdog-claude", "herdr-sheepdog-claude-2", "ucsummary-codex"} {
		a := AgentInfo{Alias: alias, Kind: "claude", State: "working", StateSince: speedNow.Add(-time.Duration(10+i) * time.Minute),
			Task: long, LastTask: long, LastResult: long, LastDetail: long, LastBlock: long, LastInteraction: speedNow.Add(-4 * time.Hour)}
		for j := 0; j < 5; j++ {
			a.Journal = append(a.Journal, JournalEntry{At: speedNow.Add(-time.Duration(5-j) * time.Minute), Event: "done", LastAssistant: long})
		}
		a.Journal[0].At = speedNow.Add(-48 * time.Hour)
		if i == 0 {
			a.State = "blocked"
			a.Pending = &PendingPrompt{ID: "ap-1", Question: long, Options: []Option{{Label: "Yes, proceed with this change", Keys: []string{"enter"}}, {Label: "Yes, and don't ask again for this", Keys: []string{"p"}}, {Label: "No, and tell it what to do differently", Keys: []string{"esc"}}}}
		}
		agents = append(agents, a)
	}
	var notes []string
	for i := 0; i < 8; i++ {
		notes = append(notes, fmt.Sprintf("note %d: %s", i, long))
	}
	promises := []PromiseInfo{
		{ID: "p1", Text: long, Agent: "herdr-sheepdog-claude", Event: PromiseEventDone, Status: PromiseOpen, CreatedAt: speedNow.Add(-20 * time.Minute)},
		{ID: "p2", Text: long, Agent: "ucsummary-codex", Event: PromiseEventBlocked, Status: PromiseOpen, CreatedAt: speedNow.Add(-5 * time.Minute)},
		{ID: "p3", Text: long, Agent: "ucsummary-codex", Event: PromiseEventDone, Status: "kept", CreatedAt: speedNow.Add(-50 * time.Minute)},
	}
	invs := []InvestigationInfo{{ID: "inv-9", Agent: "herdr-sheepdog-claude", Question: long, Status: InvestigationRunning, Started: speedNow.Add(-40 * time.Second)}}
	for i := 1; i <= 4; i++ {
		invs = append(invs, InvestigationInfo{ID: fmt.Sprintf("inv-%d", i), Agent: "ucsummary-codex", Question: long, Summary: long, Status: InvestigationDone, Started: speedNow.Add(-time.Duration(60-i) * time.Minute)})
	}
	return agents, notes, promises, invs
}

// busyFlock is four agents as the captures had them, everything a
// status question draws on filled in: a task or a last task with its
// result, three journal entries each, one pending prompt, three notes,
// an open promise, a running and a finished investigation — all texts
// long.
func busyFlock() ([]AgentInfo, []string, []PromiseInfo, []InvestigationInfo) {
	agents, notes, promises, invs := fullFlock()
	for i := range agents {
		agents[i].LastBlock, agents[i].LastDetail = "", ""
		if i%2 == 1 {
			agents[i].Task, agents[i].State = "", "idle"
		}
	}
	agents[0].Pending.Options = agents[0].Pending.Options[:2]
	return agents, notes[:3], promises[:1], invs[:2]
}

// The state block (roster, ledger, investigations, time) stays within
// 2.5 k characters for four busy agents, all three journal lines kept
// (docs/iteration-2.md WP5: the captures had a 6 k-character ledger).
func TestStateBlockBudgetBusy(t *testing.T) {
	agents, notes, promises, invs := busyFlock()
	body := joinSections(dynamicSections(agents, notes, promises, invs, "", speedNow))
	t.Logf("busy flock: %d chars\n%s", len(body), body)
	if len(body) > stateBudgetChars {
		t.Errorf("dynamic sections = %d chars, want <= %d", len(body), stateBudgetChars)
	}
	if n := strings.Count(body, "\n  - done "); n != 4*journalLedgerEntries {
		t.Errorf("journal lines = %d, want %d", n, 4*journalLedgerEntries)
	}
}

// With every field of every agent filled in, the journal gives way (one
// line per agent) and the block stays near the budget; everything else
// is still there, clipped.
func TestStateBlockBudget(t *testing.T) {
	agents, notes, promises, invs := fullFlock()
	state := stateText(dynamicSections(agents, notes, promises, invs, "", speedNow))
	body := joinSections(dynamicSections(agents, notes, promises, invs, "", speedNow))
	t.Logf("state block: %d chars (sections %d, header %d)\n%s", len(state), len(body), len(StateHeader), state)
	if len(body) > 2600 {
		t.Errorf("dynamic sections = %d chars, want <= ~2.5 k (2.6 k worst case)", len(body))
	}
	for _, want := range []string{"## Agents", "## Ledger", "## Investigations", "## Now", "WAITING FOR AN ANSWER", "Open promises", "inv-9", "inv-3", "inv-4", "note 7"} {
		if !strings.Contains(state, want) {
			t.Errorf("state block lacks %q", want)
		}
	}
	for _, gone := range []string{"inv-1 ", "inv-2 ", "note 2", "48 h"} {
		if strings.Contains(state, gone) {
			t.Errorf("state block still has %q", gone)
		}
	}
	if n := strings.Count(state, "\n  - done "); n != 4 {
		t.Errorf("journal lines = %d, want one per agent", n)
	}
	if strings.Count(renderRoster(agents, speedNow), "\n") != len(agents)+1 {
		t.Errorf("roster is not one line per agent:\n%s", renderRoster(agents, speedNow))
	}
	if !strings.HasSuffix(state, speedNow.Format("Mon 2006-01-02 15:04:05 MST")) {
		t.Errorf("the time is not the last thing in the state block")
	}
}

// An announce turn's system prompt plus state block for the full flock:
// the prompt files (SOUL.md, RULES.md, USER.md) are the fixed part and
// the state block adds at most ~2.5 k on top.
func TestAnnouncePromptBudget(t *testing.T) {
	agents, notes, promises, invs := fullFlock()
	static := joinSections(append(stableSections(nil), userSection(nil, mergeUserVars(agents, nil))))
	state := stateText(dynamicSections(agents, notes, promises, invs, "", speedNow))
	t.Logf("system %d chars + state %d chars = %d chars (~%d tokens)", len(static), len(state), len(static)+len(state), EstimateTokens(static+state))
	if len(state) > 3000 {
		t.Errorf("state = %d chars", len(state))
	}
	// +50 for the voice's reading hint (dual-brain); +125 for the
	// decision-open, deferral, English-terms and report rules (baseline
	// 2026-10-05 information fixes); +100 for unified roles (SOUL.md's
	// herdr explanation, RULES.md's fit-the-content length rule).
	if EstimateTokens(static+state) > 4975 {
		t.Errorf("system + state = %d tokens", EstimateTokens(static+state))
	}
}

// The system text holds nothing per-turn: two prompts assembled a
// minute apart for different flocks share the whole system text, and the
// state block — everything that changes — follows it.
func TestSystemTextIsStable(t *testing.T) {
	agents, notes, promises, invs := fullFlock()
	b1 := &Brain{cfg: Config{Now: func() time.Time { return speedNow }}.withDefaults(), log: nopLog{}, tools: toolDeclarations(false)}
	b1.host = &stubHost{agents: agents, notes: notes, promises: promises, invs: invs}
	sys1, secs, state1 := b1.systemPrompt()
	b1.cfg.Now = func() time.Time { return speedNow.Add(time.Minute) }
	// The same agents (USER.md lists their aliases, which change only
	// when one joins or leaves) in other states, fewer notes.
	moved := append([]AgentInfo(nil), agents...)
	for i := range moved {
		moved[i].State, moved[i].Task, moved[i].Pending = "idle", "", nil
	}
	b1.host = &stubHost{agents: moved, notes: notes[:1]}
	sys2, _, state2 := b1.systemPrompt()
	if sys1 != sys2 {
		t.Errorf("system text changed with the state")
	}
	if state1 == state2 || !strings.HasPrefix(state1, StateHeader) {
		t.Errorf("state blocks: %q / %q", state1[:40], state2[:40])
	}
	for _, s := range secs {
		if s.Source == SourceDynamic {
			t.Errorf("dynamic section %q in the system text", s.Name)
		}
	}
	if strings.Contains(sys1, "## Agents") || strings.Contains(sys1, "## Now") {
		t.Errorf("roster or time in the system text")
	}
}

// A live tool result enters the window capped to its tail with the
// provenance delimiters intact; one that fits is untouched.
func TestCapResult(t *testing.T) {
	var lines []string
	for i := 0; i < 400; i++ {
		lines = append(lines, fmt.Sprintf("line %03d of the screen with some output on it", i))
	}
	wrapped := wrapWorker("auth", "screen", strings.Join(lines, "\n"))
	got, ok := capResult(wrapped)
	if !ok || len(got) > windowResultBytes {
		t.Fatalf("capped = %v, %d bytes", ok, len(got))
	}
	if !strings.HasPrefix(unnonce(got), "<<<BEGIN_WORKER_OUTPUT agent=auth source=screen>>>\n"+resultCutNote) || !strings.HasSuffix(unnonce(got), "<<<END_WORKER_OUTPUT>>>") || !strings.Contains(got, "line 399") || strings.Contains(got, "line 000") {
		t.Errorf("capped result:\n%s", got)
	}
	if small, ok := capResult("short"); ok || small != "short" {
		t.Errorf("small result changed: %q", small)
	}
	plain, ok := capResult(strings.Join(lines, "\n"))
	if !ok || len(plain) > windowResultBytes || !strings.HasPrefix(plain, resultCutNote) {
		t.Errorf("unwrapped result: %d bytes %.60q", len(plain), plain)
	}
}

// A compaction summary is capped at summaryMaxTokens whatever the model
// returns.
func TestCompactionSummaryCap(t *testing.T) {
	long := strings.Repeat("auth was told to run the tests and it asked about the migrations.\n", 200)
	e := NewDefaultEngine(2, func(context.Context, string) (string, error) { return long, nil }, nil)
	for i := 0; i < 40; i++ {
		e.Ingest(Message{Kind: KindCEOUtterance, Content: strings.Repeat("x", 400), Tokens: 100}, i)
	}
	c, ok, err := e.Compact(context.Background(), 1000)
	if err != nil || !ok {
		t.Fatalf("compact: %v %v", ok, err)
	}
	if EstimateTokens(c.Summary.Content) > summaryMaxTokens || c.Summary.Tokens > summaryMaxTokens || !strings.HasSuffix(c.Summary.Content, "migrations.") {
		t.Errorf("summary = %d tokens, ends %q", EstimateTokens(c.Summary.Content), c.Summary.Content[max(0, len(c.Summary.Content)-20):])
	}
	if got := capSummary(strings.Repeat("word ", 1000)); EstimateTokens(got) > summaryMaxTokens || !strings.HasSuffix(got, "…") {
		t.Errorf("single-line summary = %d tokens", EstimateTokens(got))
	}
}

// Journal entries more than a day older than the agent's newest are left
// out of the ledger.
func TestJournalLedgerAge(t *testing.T) {
	j := []JournalEntry{
		{At: speedNow.Add(-30 * time.Hour), Event: "done", LastAssistant: "stale work"},
		{At: speedNow.Add(-2 * time.Minute), Event: "done", LastAssistant: "fresh work"},
	}
	got := journalLedgerLines(j, speedNow)
	if strings.Contains(got, "stale") || !strings.Contains(got, "fresh") {
		t.Errorf("lines:\n%s", got)
	}
}

type nopLog struct{}

func (nopLog) Log(string, string, string, any) {}

// stubHost serves the roster and ledger for systemPrompt.
type stubHost struct {
	Host
	agents   []AgentInfo
	notes    []string
	promises []PromiseInfo
	invs     []InvestigationInfo
}

func (h *stubHost) Agents() []AgentInfo                 { return h.agents }
func (h *stubHost) Notes() []string                     { return h.notes }
func (h *stubHost) Promises() []PromiseInfo             { return h.promises }
func (h *stubHost) Investigations() []InvestigationInfo { return h.invs }

// The tool declarations are byte-stable from one build to the next, in
// both providers' shapes, so they never break the prefix cache (the
// schemas hold maps; encoding/json sorts their keys).
func TestToolDeclarationsByteStable(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		compat, _ := json.Marshal(toolsOf(toolDeclarations(readOnly)))
		gemini, _ := json.Marshal(toolDeclarations(readOnly))
		for i := 0; i < 50; i++ {
			c, _ := json.Marshal(toolsOf(toolDeclarations(readOnly)))
			g, _ := json.Marshal(toolDeclarations(readOnly))
			if !bytes.Equal(c, compat) || !bytes.Equal(g, gemini) {
				t.Fatalf("read-only %v: declarations differ between builds", readOnly)
			}
		}
	}
}
