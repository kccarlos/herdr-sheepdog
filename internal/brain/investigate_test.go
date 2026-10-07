package brain_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// ledger builds a finished job for the Host's investigation ledger.
func ledger(id, agent, question, summary string, started time.Time) InvestigationInfo {
	finished := started.Add(30 * time.Second)
	return InvestigationInfo{
		ID: id, Agent: agent, Question: question, Source: InvestigationSourceCLI, Backend: "codex",
		Status: InvestigationDone, Started: started, Finished: &finished,
		Result: summary + "\nMore detail the ledger does not keep.", Summary: summary, Resumable: true,
	}
}

// An explanation the screen cannot answer: the orchestrator starts a background
// job and acknowledges with pending true, the answer following later.
func TestInvestigateStartsAJobAndAnswersPending(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolInvestigate, map[string]any{"agent": "auth", "question": "why did the clamp test fail?"})}},
		braintest.Step{Text: `{"spoken": "Reading the diff, about half a minute.", "detail": "Started a look at auth.", "pending": true}`},
	), nil)
	rep, err := h.brain.Turn(context.Background(), Request{Kind: KindUtterance, Question: "explain why the tests failed"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pending || rep.Spoken == "" {
		t.Errorf("reply = %+v (want pending true with a spoken acknowledgement)", rep)
	}
	if len(h.host.Investigated) != 1 {
		t.Fatalf("jobs = %+v", h.host.Investigated)
	}
	job := h.host.Investigated[0]
	if job.Question != "why did the clamp test fail?" || job.Asked != "explain why the tests failed" {
		t.Errorf("job = %+v (the user's own words travel with it)", job)
	}
	// The tool result is the job and the estimate, nothing else: the
	// answer is not in this turn.
	res := h.log.ofType("brain.tool_result")
	if len(res) != 1 {
		t.Fatalf("tool results = %v", res)
	}
	var start InvestigationStart
	out, _ := payload(res[0])["result"].(string)
	if err := json.Unmarshal([]byte(out), &start); err != nil || start.Job != job.ID || start.ETASeconds != 30 {
		t.Errorf("tool result = %q (%v)", out, err)
	}
}

// investigate is an action tool: an event turn may not start one, even
// when the model asks.
func TestInvestigateRefusedOnAReadOnlyTurn(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolInvestigate, map[string]any{"agent": "auth", "question": "why?"})}},
		braintest.Step{Text: `{"spoken": "auth finished.", "detail": "", "urgency": "idle"}`},
	), nil)
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindAnnounce, Agent: "auth", Event: "done", Template: "auth finished."}); err != nil {
		t.Fatal(err)
	}
	if len(h.host.Investigated) != 0 {
		t.Fatalf("an event turn started a job: %+v", h.host.Investigated)
	}
	blocked := h.log.ofType("brain.rail_blocked")
	if len(blocked) != 1 || payload(blocked[0])["trigger"] != string(TriggerEvent) {
		t.Fatalf("brain.rail_blocked = %v", blocked)
	}
	if reason, _ := payload(blocked[0])["reason"].(string); !strings.Contains(reason, ToolInvestigate) {
		t.Errorf("rail_blocked reason = %q", reason)
	}
	// The read-only surface does not even declare it.
	for _, fd := range h.model.Calls()[0].Config.Tools[0].FunctionDeclarations {
		if fd.Name == ToolInvestigate {
			t.Error("investigate is declared to an event turn")
		}
	}
}

// The delivery turn: the job's answer arrives as an event turn with the
// question, the user's words and the result wrapped as worker output.
func TestInvestigationDeliveryTurn(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Text: `{"spoken": "The clamp test asserts the wrong bound, so the rerun failed the same way. Shall I have auth fix it?", "detail": "From the look.", "urgency": "idle"}`},
	), nil)
	inv := ledger("inv-1", "auth", "why did the clamp test fail?", "the clamp test asserts 99, not 100", time.Now())
	inv.Asked = "explain why the tests failed"
	rep, err := h.brain.Turn(context.Background(), Request{
		Kind: KindInvestigation, Agent: "auth", Event: "investigation.done", Question: inv.Question,
		Template: "The look into auth is done; ask me what it found.", Result: inv.Result, Investigation: &inv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Spoken, "clamp test") {
		t.Errorf("reply = %+v", rep)
	}
	text := h.model.Calls()[0].Text()
	for _, want := range []string{
		`Event from the supervisor (investigation): agent="auth" event="investigation.done"`,
		`You asked it: "why did the clamp test fail?"`,
		`What the user actually said: "explain why the tests failed"`,
		"source=investigation>>>",
		"the outcome for the ear, as complete as the finding needs",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("request lacks %q:\n%s", want, text)
		}
	}
	// It is an event turn: read tools only, and the trigger says so.
	if n := len(h.model.Calls()[0].Config.Tools[0].FunctionDeclarations); n != 4 {
		t.Errorf("delivery turn declared %d tools, want the 4 read tools", n)
	}
	if p := payload(h.log.ofType("brain.turn")[0]); p["kind"] != string(KindInvestigation) || p["trigger"] != string(TriggerEvent) {
		t.Errorf("brain.turn = %v", p)
	}
}

// The prompt's Investigations section: what is running (with how long)
// and what earlier looks found; recall searches the same ledger.
func TestInvestigationsPromptSectionAndRecall(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolRecall, map[string]any{"query": "clamp"})}},
		braintest.Step{Text: braintest.Reply("The clamp test asserts the wrong bound.", "")},
	), nil)
	now := time.Now()
	h.host.SetInvestigation(ledger("inv-1", "auth", "why did the clamp test fail?", "the clamp test asserts 99, not 100", now.Add(-5*time.Minute)))
	h.host.SetInvestigation(InvestigationInfo{ID: "inv-2", Agent: "docs", Question: "what is it waiting for?", Source: InvestigationSourceCLI, Status: InvestigationRunning, Started: now.Add(-40 * time.Second)})

	var section PromptSection
	for _, s := range h.brain.PromptSections() {
		if s.Name == "investigations" {
			section = s
		}
	}
	if section.Text == "" {
		t.Fatal("no investigations section")
	}
	for _, want := range []string{"## Investigations", "Running now", `inv-2 (docs): "what is it waiting for?", 40 s so far`, "Already looked into", `investigation inv-1 (auth, 5 min ago): "why did the clamp test fail?" → the clamp test asserts 99, not 100`} {
		if !strings.Contains(section.Text, want) {
			t.Errorf("section lacks %q:\n%s", want, section.Text)
		}
	}
	if section.Source != SourceDynamic {
		t.Errorf("section source = %q", section.Source)
	}

	if _, err := h.ask("what did that look find?", "auth"); err != nil {
		t.Fatal(err)
	}
	res := h.log.ofType("brain.tool_result")
	if len(res) != 1 {
		t.Fatalf("tool results = %v", res)
	}
	if out, _ := payload(res[0])["result"].(string); !strings.Contains(out, "investigation inv-1 (auth,") || !strings.Contains(out, "the clamp test asserts 99") {
		t.Errorf("recall did not find the ledger line: %q", out)
	}
}

// A delivery turn coalesces only with a newer delivery: a worker event
// about the same agent must not drop the answer the user is waiting for.
func TestInvestigationDeliveryDoesNotCoalesceWithAnnounce(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Block: release, Text: braintest.Reply("first", "")},
		braintest.Step{Text: `{"spoken": "The look found the clamp bound.", "detail": "", "urgency": "idle"}`},
		braintest.Step{Text: `{"spoken": "auth finished.", "detail": "", "urgency": "idle"}`},
	), nil)
	// One turn holds the lock; a delivery and an announce queue behind it.
	go func() { _, _ = h.ask("what is auth doing?", "auth") }()
	waitForCalls(t, h, 1)

	inv := ledger("inv-1", "auth", "why did it fail?", "the clamp bound", time.Now())
	type result struct {
		rep Reply
		err error
	}
	deliveries := make(chan result, 1)
	announces := make(chan result, 1)
	go func() {
		rep, err := h.brain.Turn(context.Background(), Request{Kind: KindInvestigation, Agent: "auth", Event: "investigation.done", Question: inv.Question, Result: inv.Result, Investigation: &inv})
		deliveries <- result{rep, err}
	}()
	waitForQueue(t, h, 1)
	go func() {
		rep, err := h.brain.Turn(context.Background(), Request{Kind: KindAnnounce, Agent: "auth", Event: "done", Template: "auth finished."})
		announces <- result{rep, err}
	}()
	waitForQueue(t, h, 2)
	close(release)

	d := <-deliveries
	if d.err != nil || !strings.Contains(d.rep.Spoken, "clamp") {
		t.Errorf("delivery = %+v, %v (an announce about the same agent must not drop it)", d.rep, d.err)
	}
	if a := <-announces; a.err != nil {
		t.Errorf("announce = %v", a.err)
	}
}

// waitForCalls waits until the model has recorded n calls (the first
// turn is in flight, holding the lock).
func waitForCalls(t *testing.T, h *harness, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.model.Calls()) >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d model calls", n)
}

// waitForQueue waits until n turns are queued behind the running one.
func waitForQueue(t *testing.T, h *harness, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ceo, event := h.brain.Queued()
		if ceo+event >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d queued turns", n)
}

// "Ask it why" goes to the worker's own memory: the tool's source reaches
// the Host, which forks the worker's session (docs/history/dive-deep.md §3).
func TestInvestigateSourceWorkerReachesTheHost(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolInvestigate, map[string]any{"agent": "auth", "question": "why did you skip the migration?", "source": "worker"})}},
		braintest.Step{Text: `{"spoken": "Asking auth itself, about half a minute.", "detail": "", "pending": true}`},
	), nil)
	if _, err := h.brain.Turn(context.Background(), Request{Kind: KindUtterance, Question: "ask it why it skipped the migration"}); err != nil {
		t.Fatal(err)
	}
	if len(h.host.Investigated) != 1 || h.host.Investigated[0].Source != InvestigationSourceWorker {
		t.Fatalf("jobs = %+v (want one asking the worker itself)", h.host.Investigated)
	}
}
