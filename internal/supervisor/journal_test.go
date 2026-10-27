package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// jsonString is s as a JSON string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// withWorkerLog points the transcript reader at a temp home.
func withWorkerLog(dir string) func(*Config) {
	return func(c *Config) {
		c.WorkerLog = workerlog.New(workerlog.Options{ClaudeProjects: filepath.Join(dir, "projects"), CodexSessions: filepath.Join(dir, "sessions")})
	}
}

// claudeTurn writes one Claude Code turn — the instruction (as the
// orchestrator sent it, with the sign-off), an edit, the final message — to a
// transcript file and points the pane's row at it.
func claudeTurn(t *testing.T, h *harness, pane, final string) {
	t.Helper()
	path := filepath.Join(h.dir, "claude-session.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":` + jsonString(withSignOff("Fix the rounding bug")) + `}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Reading /work/w5/calc/total.go first."},{"type":"tool_use","name":"Edit","input":{"file_path":"/work/w5/calc/total.go"}}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":` + jsonString(final) + `}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Update(pane, func(a *state.Agent) {
		a.SessionSource, a.SessionKind, a.SessionValue = "herdr:claude", herdr.SessionKindPath, path
	}); err != nil {
		t.Fatal(err)
	}
}

// A finish appends a journal entry from the worker's own transcript, and
// the announce turn hears what the worker wrote before its screen
// (docs/dev/iteration-2.md WP4: the orchestrator relayed working notes because the
// screen was all it had).
func TestDoneJournalsAndTheAnnounceHearsTheWorkerFirst(t *testing.T) {
	m := braintest.NewModel()
	m.EventDefault = announceReply("Auth fixed the rounding.", "idle", "", "", false)
	dir := t.TempDir()
	h := newBrainHarness(t, m, withWorkerLog(dir))
	h.addAgent("w5:p1", "auth", "claude", herdr.StateWorking)
	final := "The total now rounds half up; see /work/w5/calc/total.go.\nWhat changed: rounding.\nWhat needs a decision: nothing.\nWhat is next: nothing."
	claudeTurn(t, h, "w5:p1", final)
	h.sup.touch("w5:p1")
	doneAuth(h)

	row, _ := h.store.Get("w5:p1")
	if len(row.Journal) != 1 {
		t.Fatalf("journal = %+v", row.Journal)
	}
	e := row.Journal[0]
	if e.Event != "done" || !e.At.Equal(t0) {
		t.Errorf("entry = %+v", e)
	}
	if e.LastAssistant != "The total now rounds half up; see a file\nWhat changed: rounding.\nWhat needs a decision: nothing.\nWhat is next: nothing." {
		t.Errorf("last_assistant = %q (scrubbed line by line)", e.LastAssistant)
	}
	if e.Instruction != "Fix the rounding bug." {
		t.Errorf("instruction = %q (without the sign-off)", e.Instruction)
	}
	if !reflect.DeepEqual(e.FilesTouched, []string{"total.go"}) || !reflect.DeepEqual(e.Tools, map[string]int{"Edit": 1}) {
		t.Errorf("files %v tools %v", e.FilesTouched, e.Tools)
	}
	ev := h.log.ofType("worker.journal")
	if len(ev) != 1 || payload(ev[0])["agent"] != "auth" || payload(ev[0])["event"] != "done" {
		t.Errorf("worker.journal = %v", ev)
	}

	calls := m.Calls()
	if len(calls) == 0 {
		t.Fatal("no announce turn")
	}
	text := calls[0].Text()
	wrote := strings.Index(text, "What the worker wrote")
	screen := strings.Index(text, "The screen the supervisor read for this event")
	if wrote < 0 || screen < 0 || wrote > screen {
		t.Fatalf("worker's words at %d, screen at %d:\n%s", wrote, screen, text)
	}
	if !strings.Contains(text[wrote:screen], "source=transcript>>>") || !strings.Contains(text[wrote:screen], "What is next: nothing.") {
		t.Errorf("the worker's message is not wrapped as worker output:\n%s", text[wrote:screen])
	}

	// done -> idle is the same finish seen again: no second entry. A new
	// block with nothing new in the transcript adds none either.
	h.transition("w5:p1", herdr.StateDone, herdr.StateIdle)
	if row, _ := h.store.Get("w5:p1"); len(row.Journal) != 1 {
		t.Errorf("idle again added an entry: %+v", row.Journal)
	}
}

// No session reported: worker.session_missing once per pane and reason,
// and the entry still records the instruction the row knows.
func TestJournalWithoutASession(t *testing.T) {
	h := newHarness(t, withWorkerLog(t.TempDir()))
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	if _, err := h.store.Update("w5:p1", func(a *state.Agent) { a.CurrentTask = withSignOff("run the tests") }); err != nil {
		t.Fatal(err)
	}
	doneAuth(h)
	h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
	doneAuth(h)
	ev := h.log.ofType("worker.session_missing")
	if len(ev) != 1 || payload(ev[0])["reason"] != workerlog.ReasonNotReported || payload(ev[0])["agent"] != "auth" {
		t.Errorf("worker.session_missing = %v", ev)
	}
	row, _ := h.store.Get("w5:p1")
	if len(row.Journal) != 1 || row.Journal[0].Instruction != "run the tests." || row.Journal[0].LastAssistant != "" {
		t.Errorf("journal = %+v (one entry: the second finish knew nothing new)", row.Journal)
	}
	if row.LastTask != "run the tests." {
		t.Errorf("last_task = %q", row.LastTask)
	}
	// A pane first seen at rest has no news: no entry.
	old := filepath.Join(h.dir, "old.jsonl")
	if err := os.WriteFile(old, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hours-old history."}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.sup.HandleTransition(t.Context(), herdr.Transition{PaneID: "w6:p1", To: herdr.StateDone, At: h.clock.Now(), Agent: "claude",
		Session: &herdr.AgentSession{Source: "herdr:claude", Kind: herdr.SessionKindPath, Value: old}})
	if row, ok := h.store.Get("w6:p1"); !ok || row.SessionValue != old || len(row.Journal) != 0 {
		t.Errorf("discovery journaled: %+v", row.Journal)
	}
}

// The ledger shows the last journal entries and recall finds all of them.
func TestJournalInTheLedgerAndRecall(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolRecall, map[string]any{"query": "rounding"})}},
		braintest.Step{Text: braintest.Reply("It fixed the rounding.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "claude", herdr.StateIdle)
	for i, text := range []string{"Set up the project.", "Added the parser.", "Wrote the docs.", "Fixed the rounding in the total."} {
		if err := h.store.AppendJournal("w5:p1", state.JournalEntry{At: t0.Add(time.Duration(i) * time.Minute), Event: "done", LastAssistant: text}); err != nil {
			t.Fatal(err)
		}
	}
	h.clock.Advance(5 * time.Minute)
	h.say("what did auth fix?")
	calls := m.Calls()
	if len(calls) < 2 {
		t.Fatalf("calls = %d", len(calls))
	}
	system := calls[0].System()
	if strings.Contains(system, "Set up the project") || !strings.Contains(system, `ago: "Fixed the rounding in the total."`) || !strings.Contains(system, `"Added the parser."`) {
		t.Errorf("ledger:\n%s", system)
	}
	if !strings.Contains(calls[1].Text(), `auth journal (16:58, done): wrote "Fixed the rounding in the total."`) {
		t.Errorf("recall result:\n%s", calls[1].Text())
	}
}

// Every instruction the orchestrator sends carries the sign-off; nothing the
// user hears or the roster keeps does.
func TestPromptCarriesTheSignOff(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "Run the tests"})}},
		braintest.Step{Text: braintest.Reply("Telling auth to run the tests.", "")},
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolPrompt, map[string]any{"agent": "auth", "text": "Now the docs."})}},
		braintest.Step{Text: braintest.Reply("Telling auth to do the docs.", "")},
	)
	h := newBrainHarness(t, m, func(c *Config) { c.Chattiness = string(Chatty) })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	h.say("tell auth to run the tests")
	got := h.promptRequests()
	if len(got) != 1 || got[0][1] != "Run the tests. "+SignOff {
		t.Fatalf("prompts = %v", got)
	}
	p, _ := h.pending()
	if p.SpokenSummary != "Telling auth to run the tests." {
		t.Errorf("pending summary = %q", p.SpokenSummary)
	}
	row, _ := h.store.Get("w5:p1")
	if row.CurrentTask != "Run the tests." {
		t.Errorf("current task = %q", row.CurrentTask)
	}
	h.clock.Advance(time.Second)
	h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
	// Silent context here (just sent); its text is what matters.
	if all := h.pusher.all(); len(all) == 0 || all[len(all)-1].Text != `auth started on "Run the tests".` {
		t.Errorf("started push = %+v", all)
	}
	h.transition("w5:p1", herdr.StateWorking, herdr.StateIdle)
	h.say("now the docs")
	if got := h.promptRequests(); len(got) != 2 || !strings.HasSuffix(got[1][1], SignOff) {
		t.Errorf("second prompt = %v", got)
	}
	if n := h.log.count("supervisor.signoff"); n != 1 {
		t.Errorf("supervisor.signoff logged %d times, want once", n)
	}
	if withoutSignOff(withSignOff("Run the tests")) != "Run the tests." || withSignOff(withSignOff("x.")) != "x. "+SignOff {
		t.Error("withSignOff / withoutSignOff do not round-trip")
	}
}

// Tier-3 bench 2026-10-03 (run A, herdr.send): a Chinese instruction
// reached the worker as "请运行 shop 相关的测试。. When you finish, …".
// The sign-off join respects CJK terminators.
func TestSignOffJoinRespectsCJKTerminators(t *testing.T) {
	for in, want := range map[string]string{
		"请运行 shop 相关的测试。": "请运行 shop 相关的测试。 " + SignOff,
		"请运行 shop 相关的测试":  "请运行 shop 相关的测试。 " + SignOff,
		"能修好吗？":           "能修好吗？ " + SignOff,
		"Run the tests":   "Run the tests. " + SignOff,
		"Run the tests!":  "Run the tests! " + SignOff,
	} {
		if got := withSignOff(in); got != want {
			t.Errorf("withSignOff(%q) = %q, want %q", in, got, want)
		}
	}
}

// A slash command goes to the worker bare, without trailing punctuation;
// prose, even starting with "/", keeps the trailer.
func TestSignOffSkipsSlashCommands(t *testing.T) {
	for in, want := range map[string]string{
		"/permissions":         "/permissions",
		"/permissions.":        "/permissions",
		"/permissions。":        "/permissions",
		"  /model gpt-5  ":     "/model gpt-5",
		"/model gpt-5!":        "/model gpt-5",
		"/compact":             "/compact",
		"/compact\nthen go on": "/compact\nthen go on. " + SignOff,
		"Run the tests":        "Run the tests. " + SignOff,
		"  Run the tests  ":    "Run the tests. " + SignOff,
	} {
		if got := withSignOff(in); got != want {
			t.Errorf("withSignOff(%q) = %q, want %q", in, got, want)
		}
	}
	if withoutSignOff(withSignOff("/permissions")) != "/permissions" {
		t.Error("a slash command's task text is not the command")
	}
}
