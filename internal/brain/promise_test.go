package brain_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// A CEO turn records a promise through the promise tool: the Host gets
// the record, the transcript gets a promise message in the turn, the
// echo counts as an action, and the ledger lists it as open.
func TestPromiseToolRecordsAPromise(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{
			braintest.FC(ToolPrompt, map[string]any{"agent": "auth", "text": "run the tests"}),
			braintest.FC(ToolPromise, map[string]any{"agent": "auth", "text": "let me know when it's done", "event": "done"}),
		}},
		braintest.Step{Text: braintest.Reply("Telling auth to run the tests. Sent; I will tell you when auth finishes.", "")},
		braintest.Step{Text: braintest.Reply("ok", "")},
	)
	h := newHarness(t, m, nil)
	h.brain.Heard("run the tests and let me know when it's done", true)
	rep, err := h.brain.Propose(context.Background(), Request{Question: "run the tests and let me know when it's done"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Spoken, "I will tell you when auth finishes") {
		t.Errorf("reply = %+v", rep)
	}
	if len(h.host.Prompts) != 1 || len(h.host.Promised) != 1 {
		t.Fatalf("prompts = %v promised = %v", h.host.Prompts, h.host.Promised)
	}
	p := h.host.Promised[0]
	if p.Text != "let me know when it's done" || p.Agent != "auth" || p.Event != PromiseEventDone || p.Status != PromiseOpen || p.CreatedTurn != 1 {
		t.Errorf("promise = %+v", p)
	}
	if got := joinKinds(h.kinds()); got != "ceo_utterance request brain_tool_call brain_tool_result brain_tool_call brain_tool_result promise reply" {
		t.Errorf("transcript = %s", got)
	}
	msgs := h.brain.Transcript().Messages()
	pm := msgs[6]
	if pm.Kind != KindPromise || pm.Promise == nil || pm.Promise.ID != p.ID || pm.Turn != 1 || pm.Content != `Promise (open): "let me know when it's done", when auth finishes` {
		t.Errorf("promise message = %+v", pm)
	}
	if res := msgs[5]; res.ToolResult == nil || res.Content != "I will tell you when auth finishes." {
		t.Errorf("promise result = %+v", res)
	}
	if p := payload(h.log.ofType("brain.turn")[0]); p["acted"] != 2 {
		t.Errorf("brain.turn acted = %v", p["acted"])
	}
	// The next turn's ledger lists the open promise; once kept it is gone.
	if _, err := h.ask("anything?", "auth"); err != nil {
		t.Fatal(err)
	}
	sys := h.model.Calls()[2].System()
	if !strings.Contains(sys, "Open promises") || !strings.Contains(sys, `"let me know when it's done": when auth finishes`) {
		t.Errorf("ledger lacks the promise:\n%s", sys)
	}
	h.host.SetPromiseStatus(p.ID, PromiseKept)
	m.Add(braintest.Step{Text: braintest.Reply("ok", "")})
	if _, err := h.ask("anything?", "auth"); err != nil {
		t.Fatal(err)
	}
	if sys := h.model.Calls()[3].System(); strings.Contains(sys, "Open promises") {
		t.Errorf("kept promise still in the ledger:\n%s", sys)
	}
}

// promise validates its arguments and reports the Host's refusal.
func TestPromiseToolErrors(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolPromise, map[string]any{"agent": "auth", "text": "tell me", "event": "whenever"})}},
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolPromise, map[string]any{"agent": "auth", "text": "", "event": "done"})}},
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolPromise, map[string]any{"agent": "auth", "text": "tell me", "event": "done"})}},
		braintest.Step{Text: braintest.Reply("I could not record that.", "")},
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolPromise, map[string]any{"agent": "auth", "text": "interrupt me when it's done", "event": "done", "due_in_minutes": float64(0)})}},
		braintest.Step{Text: braintest.Reply("I will interrupt you.", "")},
	)
	h := newHarness(t, m, nil)
	h.host.PromiseErr = braintest.ErrBoom
	rep, err := h.brain.Propose(context.Background(), Request{Question: "let me know", Agent: "auth"})
	if err != nil || rep.Spoken != "I could not record that." || len(h.host.Promised) != 0 {
		t.Fatalf("reply = %+v %v promised = %v", rep, err, h.host.Promised)
	}
	// due_in_minutes 0: due now, so the kept push interrupts.
	h.host.PromiseErr = nil
	if rep, err := h.brain.Propose(context.Background(), Request{Question: "interrupt me when it's done", Agent: "auth"}); err != nil || rep.Spoken != "I will interrupt you." {
		t.Fatalf("reply = %+v %v", rep, err)
	}
	if len(h.host.Promised) != 1 || h.host.Promised[0].Due == nil || h.host.Promised[0].Due.After(time.Now()) {
		t.Errorf("due promise = %+v", h.host.Promised)
	}
	var errs []string
	for _, ev := range h.log.ofType("brain.tool_result") {
		e, _ := payload(ev)["error"].(string)
		errs = append(errs, e)
	}
	if len(errs) != 4 || !strings.Contains(errs[0], `promise event "whenever"`) || !strings.Contains(errs[1], "needs the text") || errs[2] != "boom" || errs[3] != "" {
		t.Errorf("errors = %q", errs)
	}
}

// An event turn (summarize) is read-only: only the four read tools are
// declared, every action tool is refused by the executor (brain.rail_blocked),
// the enrich timeout applies, the supervisor's screen is in the window as
// an event_screen message (pruned later like a tool result), and an empty
// spoken is a valid "nothing to add".
func TestEventTurnIsReadOnly(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{
			braintest.FC(ToolPrompt, map[string]any{"agent": "auth", "text": "rerun"}),
			braintest.FC(ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"enter"}}),
			braintest.FC(ToolPromise, map[string]any{"agent": "auth", "text": "x", "event": "done"}),
			braintest.FC(ToolInterrupt, map[string]any{"agent": "auth"}),
			braintest.FC(ToolRecall, map[string]any{"query": "tests"}),
		}},
		braintest.Step{Text: `{"spoken": "", "detail": "The template was sufficient.", "relates_to": "the rerun"}`},
	)
	h := newHarness(t, m, func(c *Config) { c.EnrichTimeout = 6 * time.Second; c.PruneAfterTurns = 1 })
	h.host.SetPending("w5:p1", approvalPrompt())
	h.brain.Heard("rerun the tests", true)
	rep, err := h.brain.Turn(context.Background(), Request{Kind: KindSummarize, Trigger: TriggerCEO, Agent: "auth", Event: "done", Template: "auth finished: ran the tests, 1 failed.", Screen: "$ go test ./...\n--- FAIL: TestLoginRateLimit\nFAIL"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Spoken != "" || rep.Detail != "The template was sufficient." || rep.RelatesTo != "the rerun" {
		t.Errorf("reply = %+v", rep)
	}
	if len(h.host.Prompts)+len(h.host.Keys)+len(h.host.Promised)+len(h.host.Interrupts) != 0 {
		t.Errorf("an event turn acted: %v %v %v %v", h.host.Prompts, h.host.Keys, h.host.Promised, h.host.Interrupts)
	}
	blocked := h.log.ofType("brain.rail_blocked")
	if len(blocked) != 4 {
		t.Fatalf("rail_blocked = %d", len(blocked))
	}
	for _, ev := range blocked {
		p := payload(ev)
		if p["trigger"] != "event" || !strings.Contains(p["reason"].(string), "never on an event") {
			t.Errorf("rail_blocked = %v", p)
		}
	}
	call := h.model.Calls()[0]
	if len(call.Config.Tools[0].FunctionDeclarations) != 4 {
		t.Errorf("event turn declared %d tools", len(call.Config.Tools[0].FunctionDeclarations))
	}
	for _, d := range call.Config.Tools[0].FunctionDeclarations {
		if IsActionTool(d.Name) {
			t.Errorf("event turn declares %s", d.Name)
		}
	}
	text := call.Text()
	if !strings.Contains(text, `Event from the supervisor (summarize): agent="auth" event="done" template="auth finished: ran the tests, 1 failed."`) || !strings.Contains(text, "The screen the supervisor read for this event:\n<<<BEGIN_WORKER_OUTPUT agent=auth source=screen>>>\n$ go test") {
		t.Errorf("model saw:\n%s", text)
	}
	turn := payload(h.log.ofType("brain.turn")[0])
	if turn["trigger"] != "event" || turn["kind"] != "summarize" || turn["fallback"] != false || turn["acted"] != 0 {
		t.Errorf("brain.turn = %v", turn)
	}
	if got := joinKinds(h.kinds()); got != "ceo_utterance request event_screen brain_tool_call brain_tool_result brain_tool_call brain_tool_result brain_tool_call brain_tool_result brain_tool_call brain_tool_result brain_tool_call brain_tool_result reply" {
		t.Errorf("transcript = %s", got)
	}
	// The screen is pruned like a tool result; the transcript keeps it.
	m.Add(braintest.Step{Text: braintest.Reply("ok", "")})
	if _, err := h.ask("?", "auth"); err != nil {
		t.Fatal(err)
	}
	h.brain.Quiesce()
	m.Add(braintest.Step{Text: braintest.Reply("ok", "")})
	if _, err := h.ask("?", "auth"); err != nil {
		t.Fatal(err)
	}
	h.brain.Quiesce()
	for _, w := range h.brain.Window() {
		if w.Kind == KindEventScreen && (!w.Pruned || w.Stub != "[screen of auth at done, 3 lines, pruned]") {
			t.Errorf("event screen not pruned: %+v", w)
		}
	}
	if full := h.brain.Transcript().Messages()[2]; !strings.Contains(full.Content, "TestLoginRateLimit") {
		t.Errorf("transcript lost the screen: %+v", full)
	}
}

// The enrich timeout bounds an event turn: a slow model means fallback,
// never a late answer.
func TestEventTurnTimeout(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	m := braintest.NewModel(braintest.Step{Block: block})
	h := newHarness(t, m, func(c *Config) { c.EnrichTimeout = 50 * time.Millisecond; c.FinalGrace = -1 })
	start := time.Now()
	_, err := h.brain.Turn(context.Background(), Request{Kind: KindKeepPromise, Agent: "auth", Event: "done", Template: "auth finished.", Promise: &PromiseInfo{ID: "pr-1", Text: "tell me", Agent: "auth", Event: "done"}})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
	fb := payload(h.log.ofType("brain.fallback")[0])
	if fb["reason"] != "timeout" || fb["kind"] != "keep_promise" || fb["trigger"] != "event" {
		t.Errorf("fallback = %v", fb)
	}
	if text := h.model.Calls()[0].Text(); !strings.Contains(text, `Event from the supervisor (keep_promise): agent="auth" event="done" template="auth finished." promise="tell me" condition="when auth finishes"`) {
		t.Errorf("request = %s", text)
	}
}

// recall finds promises through the Host (current status), and
// PromiseEvent puts a kept/expired record into the transcript.
func TestRecallFindsPromises(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolRecall, map[string]any{"query": "tests done"})}},
		braintest.Step{Text: braintest.Reply("You asked to be told when auth finishes the tests.", "")},
	)
	h := newHarness(t, m, nil)
	p, _ := h.host.Promise(context.Background(), "w5:p1", PromiseRequest{Text: "tell me when the tests are done", Event: "done"})
	h.host.SetPromiseStatus(p.ID, PromiseKept)
	kept := p
	kept.Status = PromiseKept
	h.brain.PromiseEvent(kept)
	if _, err := h.ask("did I ask you anything?", ""); err != nil {
		t.Fatal(err)
	}
	res := h.model.Calls()[1].Text()
	if !strings.Contains(res, `Promise (kept): "tell me when the tests are done", when auth finishes`) {
		t.Errorf("recall result:\n%s", res)
	}
	if strings.Count(res, "output: Promise (kept)") != 1 || strings.Count(res, "Promise (kept)") != 2 {
		t.Errorf("promise listed more than once by recall (the window has its own line):\n%s", res)
	}
	msgs := h.brain.Transcript().Messages()
	if msgs[0].Kind != KindPromise || msgs[0].Promise == nil || msgs[0].Promise.Status != PromiseKept || msgs[0].Turn != 0 {
		t.Errorf("PromiseEvent message = %+v", msgs[0])
	}
}

// The reflex model records promises from the words, and answers event
// turns without tools.
func TestReflexPromisesAndEvents(t *testing.T) {
	h := newHarness(t, braintest.Reflex(), nil)
	// Unnamed: the roster is listed and the words pick auth.
	rep, err := h.brain.Propose(context.Background(), Request{Question: "tell auth to run the tests and let me know when it's done"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Spoken != "Telling auth to run the tests. Sent; I will tell you when auth finishes." {
		t.Errorf("combined propose = %+v", rep)
	}
	if got := joinKinds(h.kinds()); got != "request brain_tool_call brain_tool_result brain_tool_call brain_tool_result brain_tool_call brain_tool_result promise reply" {
		t.Errorf("transcript = %s", got)
	}
	if len(h.host.Prompts) != 1 || h.host.Prompts[0][1] != "run the tests" || len(h.host.Promised) != 1 || h.host.Promised[0].Event != PromiseEventDone {
		t.Errorf("prompts = %v promised = %+v", h.host.Prompts, h.host.Promised)
	}
	// Bare promise, blocked condition, named agent.
	rep, err = h.brain.Propose(context.Background(), Request{Question: "let me know if it asks anything", Agent: "docs"})
	if err != nil || rep.Spoken != "I will tell you when docs asks something." || len(h.host.Prompts) != 1 || len(h.host.Promised) != 2 || h.host.Promised[1].Event != PromiseEventBlocked {
		t.Errorf("bare promise = %+v %v prompts=%v promised=%+v", rep, err, h.host.Prompts, h.host.Promised)
	}
	// summarize relates the finish to the ledger's last task.
	h.host.SetAgent(AgentInfo{PaneID: "w5:p1", Alias: "auth", Kind: "codex", State: "done", LastTask: "run the tests", LastResult: "ran the tests, 1 failed (failed)"})
	rep, err = h.brain.Turn(context.Background(), Request{Kind: KindSummarize, Agent: "auth", Event: "done", Template: "auth finished: ran the tests, 1 failed.", Screen: "FAIL"})
	if err != nil || rep.Spoken != `That was the "run the tests" you asked for.` || rep.RelatesTo != "run the tests" {
		t.Errorf("summarize = %+v %v", rep, err)
	}
	// Nothing to relate: empty spoken.
	rep, err = h.brain.Turn(context.Background(), Request{Kind: KindSummarize, Agent: "docs", Event: "blocked", Template: "docs is asking something."})
	if err != nil || rep.Spoken != "" {
		t.Errorf("summarize with nothing to add = %+v %v", rep, err)
	}
	rep, err = h.brain.Turn(context.Background(), Request{Kind: KindKeepPromise, Agent: "auth", Event: "done", Template: "auth finished: ran the tests, 1 failed.", Promise: &h.host.Promised[0]})
	if err != nil || rep.Spoken != "As you asked: auth finished: ran the tests, 1 failed." {
		t.Errorf("keep_promise = %+v %v", rep, err)
	}
	if n := len(h.host.Prompts) + len(h.host.Keys) + len(h.host.Interrupts); n != 1 || len(h.host.Promised) != 2 {
		t.Errorf("event turns acted: %d actions, %d promises", n, len(h.host.Promised))
	}
}
