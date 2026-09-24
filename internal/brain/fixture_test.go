package brain_test

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// update regenerates testdata/brain/sample-run.jsonl:
//
//	go test ./internal/brain -run TestSampleRunFixture -update
var update = flag.Bool("update", false, "rewrite testdata/brain/sample-run.jsonl")

// sampleRunPath is the fixture the /debug Supervisor tab's tests and its
// console handle (sheepdogDebug.loadTranscript) read.
const sampleRunPath = "../../testdata/brain/sample-run.jsonl"

// TestSampleRunFixture drives a realistic run through the brain — a seed
// conversation, an ask that reads a screen, an ask that runs the CLI, a
// propose whose send_keys the rail blocks, filler until the window
// compacts, a note and a recall — over a fixed clock and a scripted model,
// and compares the transcript it wrote with the checked-in fixture, so
// the fixture can never drift from the on-disk format. -update rewrites
// it.
func TestSampleRunFixture(t *testing.T) {
	clock := time.Date(2026, 9, 18, 9, 30, 0, 0, time.UTC)
	now := func() time.Time { clock = clock.Add(400 * time.Millisecond); return clock }

	host := braintest.NewHost()
	host.SetAgent(AgentInfo{PaneID: "w5:p1", Alias: "auth", Kind: "codex", State: "working", Task: "run the full test suite", StateSince: clock})
	host.SetAgent(AgentInfo{PaneID: "w6:p1", Alias: "docs", Kind: "claude", State: "idle", LastResult: "wrote the readme (success)"})
	host.SetScreen("w5:p1", fixture(t, "codex-after-approval.screen.txt"))
	host.CLI = func(paneID, question string) (string, error) {
		return "TestClampUpper fails because clamp() returns max+1 for inputs above the range: internal/math/clamp.go:17 uses `>` where the spec (and the sibling TestClampLower) expect `>=`.\n\n--- FAIL: TestClampUpper (0.00s)\n    clamp_test.go:31: clamp(11, 0, 10) = 11, want 10\nFAIL\nFAIL\tinternal/math\t0.012s\n", nil
	}

	// Scripted answers, one per model call of each turn; the compaction's
	// summariser is recognised by its prompt, whichever turn it follows.
	var script []braintest.Step
	model := &braintest.Model{}
	model.Default = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		if strings.HasPrefix(strings.TrimSpace(call.Text()), "Summarise the following") {
			return braintest.Response(braintest.Step{Text: "The CEO asked for a briefing, told auth to run the full suite and asked why TestClampUpper failed: clamp() uses > instead of >= (auth's screen and the CLI agree). Auth then asked to delete the fixtures directory; the CEO said yes but the SVP could not answer the prompt (enter is not one of its options) and left it to the CEO. Docs has been idle throughout. Nothing else is open."}), nil
		}
		if len(script) == 0 {
			return nil, fmt.Errorf("fixture: unscripted model call: %s", call.Text())
		}
		step := script[0]
		script = script[1:]
		return braintest.Response(step), nil
	}}
	next := func(steps ...braintest.Step) { script = append(script, steps...) }

	dir := t.TempDir()
	b, err := New(Config{DataDir: dir, RunID: "sample-run", Now: now, WindowTokens: 1150, PruneAfterTurns: 4, UtteranceTimeout: 30 * time.Second}, model, &recLog{})
	if err != nil {
		t.Fatal(err)
	}
	b.SetHost(host)
	ctx := context.Background()
	ask := func(q, agent string) Reply {
		t.Helper()
		rep, err := b.Turn(ctx, Request{Kind: KindUtterance, Question: q, Agent: agent})
		if err != nil {
			t.Fatalf("ask %q: %v", q, err)
		}
		b.Quiesce()
		return rep
	}

	// Seed: the run so far, as the voice events fed it.
	b.WorkerEvent("auth", "", "working", false)
	b.WorkerEvent("docs", "", "idle", false)
	b.Heard("What's ", false)
	b.Heard("going on?", true)
	b.Said("Two agents: auth is running the tests, docs is idle.", true)
	b.Heard("Tell auth to run the full suite.", true)
	b.Said("Telling auth to run the full test suite. Sent.", true)
	b.WorkerEvent("auth", "working", "done", false)
	b.Pushed("auth", "auth finished: 122 passed, 1 failed, TestClampUpper.", "idle")
	b.Said("Auth finished: 122 passed, one failed — TestClampUpper.", true)

	// Turn 1: an ask that reads the screen.
	b.Heard("What did it say exactly?", true)
	next(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth", "lines": float64(100)})}, Usage: [2]int32{3100, 24}},
		braintest.Step{Text: braintest.Reply("Auth ran the suite: 122 passed and TestClampUpper failed, off by one at the upper bound.", "The screen shows go test output ending in FAIL for internal/math; the failing assertion is clamp(11, 0, 10) = 11, want 10."), Usage: [2]int32{3600, 60}})
	ask("what did it say exactly?", "auth")
	b.Said("Auth ran the suite: 122 passed and TestClampUpper failed, off by one at the upper bound.", true)

	// Turn 2: an ask that needs the repository ("that one" = the failing test).
	b.Heard("Why did that one fail?", true)
	next(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolRunCLI, map[string]any{"agent": "auth", "question": "Why does TestClampUpper fail after the latest changes in this repository? Quote the failing assertion and the line responsible."})}, Usage: [2]int32{3700, 40}},
		braintest.Step{Text: braintest.Reply("Clamp uses a greater-than where it needs greater-or-equal, so the upper bound comes back one too high.", "The CLI points at internal/math/clamp.go line 17: `>` should be `>=`; TestClampLower expects the inclusive form."), Usage: [2]int32{4200, 58}})
	ask("why did that one fail?", "")
	b.Said("Clamp uses a greater-than where it needs greater-or-equal, so the upper bound comes back one too high.", true)

	// Turn 3: auth blocks on a prompt; the CEO says yes; the model sends
	// a key that is not one of the prompt's options, and the rail refuses.
	host.SetScreen("w5:p1", fixture(t, "codex-approval-prompt.screen.txt"))
	host.SetPending("w5:p1", &PendingPrompt{ID: "ap-7", Question: "whether it can delete the fixtures directory",
		Options: []Option{{Label: "Yes, proceed", Keys: []string{"1"}}, {Label: "No, tell it differently", Keys: []string{"esc"}}}, YesKeys: []string{"1"}, NoKeys: []string{"esc"}})
	b.WorkerEvent("auth", "done", "blocked", false)
	b.Pushed("auth", "auth is asking whether it can delete the fixtures directory.", "interrupt")
	b.Said("Auth is asking whether it can delete the fixtures directory.", true)
	b.Heard("Yes, go ahead.", true)
	next(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"enter"}})}, Usage: [2]int32{4300, 30}},
		braintest.Step{Text: braintest.Reply("I could not answer auth's prompt: enter is not one of its options, it wants 1 or escape.", "The rail refused send_keys with enter; the prompt's options are 1 (yes, proceed) and esc (no). Say 'option one' or 'no'.") + "", Usage: [2]int32{4500, 50}})
	if rep, err := b.Turn(ctx, Request{Kind: KindUtterance, Question: "yes, go ahead and approve it", Agent: "auth"}); err != nil || !strings.HasPrefix(rep.Spoken, "I could not answer") {
		t.Fatalf("propose = %+v, %v", rep, err)
	}
	b.Quiesce()
	b.Said("I could not answer auth's prompt: enter is not one of its options, it wants 1 or escape.", true)
	b.Heard("Option one.", true)
	host.SetPending("w5:p1", nil)
	b.WorkerEvent("auth", "blocked", "working", false)

	// Filler: the window grew past the budget with turn 3 and was
	// compacted after it; the rest keeps the summary company.
	for i := 1; i <= 4; i++ {
		b.Heard(fmt.Sprintf("Is auth still busy? Asking for the %d time.", i), true)
		b.Said("Auth is still working on the fixtures; docs is idle.", true)
	}
	b.WorkerEvent("auth", "working", "done", false)
	b.Pushed("auth", "auth finished: deleted the fixtures directory and re-ran the suite, all green.", "idle")
	b.Said("Auth finished: the fixtures directory is gone and the suite is green.", true)

	// Turn 4: an ask about the roster.
	b.Heard("Is docs still idle?", true)
	next(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolListAgents, nil)}, Usage: [2]int32{5200, 18}},
		braintest.Step{Text: braintest.Reply("Yes, docs is idle; it last wrote the readme.", "auth is done, docs is idle with no task."), Usage: [2]int32{5400, 32}})
	ask("is docs still idle?", "docs")
	b.Said("Yes, docs is idle; it last wrote the readme.", true)

	// A note, then a recall that finds it.
	b.Noted("don't touch the migrations today")
	b.Heard("What did I tell you to remember?", true)
	next(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolRecall, map[string]any{"query": "remember"})}, Usage: [2]int32{2100, 20}},
		braintest.Step{Text: braintest.Reply("Not to touch the migrations today.", "One note: don't touch the migrations today."), Usage: [2]int32{2300, 28}})
	ask("what did I tell you to remember?", "")
	b.Said("Not to touch the migrations today.", true)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if len(script) != 0 {
		t.Fatalf("%d scripted steps unused", len(script))
	}

	got, err := os.ReadFile(b.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	// Shape checks, so the fixture stays useful for the tab's tests.
	msgs, err := ReadTranscript(b.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) < 50 || len(msgs) > 80 {
		t.Errorf("fixture has %d messages, want 50–80", len(msgs))
	}
	seen := map[MessageKind]int{}
	tools := map[string]int{}
	for _, m := range msgs {
		seen[m.Kind]++
		if m.ToolCall != nil {
			tools[m.ToolCall.Name]++
		}
	}
	for _, k := range []MessageKind{KindCEOUtterance, KindAssistantSaid, KindPush, KindWorkerEvent, KindNote, KindRequest, KindToolCall, KindToolResult, KindReply, KindCompaction} {
		if seen[k] == 0 {
			t.Errorf("fixture has no %s", k)
		}
	}
	if seen[KindCompaction] != 1 {
		t.Errorf("fixture has %d compactions, want 1", seen[KindCompaction])
	}
	for _, name := range []string{ToolReadScreen, ToolRunCLI, ToolSendKeys, ToolListAgents, ToolRecall} {
		if tools[name] != 1 {
			t.Errorf("fixture calls %s %d times, want 1", name, tools[name])
		}
	}
	if c, ok := b.LastCompaction(); !ok || c.Dropped == 0 {
		t.Errorf("no compaction recorded: %+v", c)
	}

	if *update {
		if err := os.MkdirAll(filepath.Dir(sampleRunPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sampleRunPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d messages)", sampleRunPath, len(msgs))
		return
	}
	want, err := os.ReadFile(sampleRunPath)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the generated transcript (run with -update if the format changed on purpose)", sampleRunPath)
	}
}
