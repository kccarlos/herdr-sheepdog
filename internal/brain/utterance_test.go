package brain_test

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// (1) An utterance turn is a CEO turn over the verbatim words: the full
// tool surface, no agent hint, and the roster sentence in the request.
func TestUtteranceTurnIsACEOTurn(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolPrompt, map[string]any{"agent": "auth", "text": "run the tests"})}},
		braintest.Step{Text: braintest.Reply("Telling auth to run the tests. Sent.", "")},
	)
	h := newHarness(t, m, nil)
	rep, err := h.brain.Utterance(context.Background(), Request{Question: "tell auth to run the tests", Briefing: "One agent: auth is working."})
	if err != nil || rep.Spoken != "Telling auth to run the tests. Sent." {
		t.Fatalf("reply = %+v err = %v", rep, err)
	}
	if got := h.host.Prompts; len(got) != 1 || got[0][1] != "run the tests" {
		t.Fatalf("prompts = %v", got)
	}
	text := h.model.Calls()[0].Text()
	for _, want := range []string{
		`Request from the assistant (utterance): utterance="tell auth to run the tests"`,
		"Status as the daemon has it: One agent: auth is working.",
		"decide what they are",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("request lacks %q:\n%s", want, text)
		}
	}
	if n := len(h.model.Calls()[0].Config.Tools[0].FunctionDeclarations); n != 10 {
		t.Errorf("utterance turn declared %d tools, want the full surface of 10", n)
	}
	if p := payload(h.log.ofType("brain.turn")[0]); p["kind"] != "utterance" || p["trigger"] != "ceo" {
		t.Errorf("brain.turn = %v", p)
	}
}

// (2) The note tool writes through the Host and is an action tool, so a
// read-only turn may not call it.
func TestNoteTool(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolNote, map[string]any{"text": "the migrations are off limits today"})}},
		braintest.Step{Text: braintest.Reply("Noted.", "")},
	)
	h := newHarness(t, m, nil)
	if _, err := h.brain.Utterance(context.Background(), Request{Question: "remember the migrations are off limits today"}); err != nil {
		t.Fatal(err)
	}
	if got := h.host.Noted; len(got) != 1 || got[0] != "the migrations are off limits today" {
		t.Fatalf("noted = %v", got)
	}
	if !IsActionTool(ToolNote) {
		t.Error("note must be an action tool: it writes to the record")
	}
	for _, d := range ToolDeclarations(true)[0].FunctionDeclarations {
		if d.Name == ToolNote {
			t.Error("a read-only turn declares note")
		}
	}
}

// (3) A stop turn is CEO-triggered (the shepherd is waiting) but
// read-only: it reports what the daemon stopped and may send nothing.
func TestStopTurnIsReadOnly(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolPrompt, map[string]any{"agent": "auth", "text": "carry on"})}},
		braintest.Step{Text: braintest.Reply("Stopped the instruction to auth. Shall I tell it something else?", "")},
	)
	h := newHarness(t, m, nil)
	rep, err := h.brain.Turn(context.Background(), Request{Kind: KindStop, Agent: "auth", Stopped: "the instruction to auth was interrupted"})
	if err != nil || !strings.HasPrefix(rep.Spoken, "Stopped") {
		t.Fatalf("reply = %+v err = %v", rep, err)
	}
	if len(h.host.Prompts) != 0 {
		t.Errorf("a stop turn sent something: %v", h.host.Prompts)
	}
	if h.log.count("brain.rail_blocked") != 1 {
		t.Errorf("rail_blocked = %d, want 1", h.log.count("brain.rail_blocked"))
	}
	call := h.model.Calls()[0]
	if n := len(call.Config.Tools[0].FunctionDeclarations); n != 4 {
		t.Errorf("stop turn declared %d tools, want the 4 read tools", n)
	}
	if !strings.Contains(call.Text(), `Request from the assistant (stop): stopped="the instruction to auth was interrupted"`) {
		t.Errorf("model saw:\n%s", call.Text())
	}
}

// (4) A write tool refuses to run once the turn's context is cancelled
// (the shepherd said stop between the model answering and its calls).
func TestActionToolRefusesAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := braintest.NewModel(braintest.Step{Fn: func(braintest.Call) (*genai.GenerateContentResponse, error) {
		cancel()
		return braintest.Response(braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolPrompt, map[string]any{"agent": "auth", "text": "run the tests"})}}), nil
	}})
	m.Default = &braintest.Step{Text: braintest.Reply("I sent nothing.", "")}
	h := newHarness(t, m, nil)
	if _, err := h.brain.Utterance(ctx, Request{Question: "tell auth to run the tests"}); err == nil {
		t.Log("the turn answered after the cancel; what matters is that nothing was sent")
	}
	if len(h.host.Prompts) != 0 {
		t.Errorf("an instruction was sent under a cancelled turn: %v", h.host.Prompts)
	}
}

// (5) pending is parsed out of the model's JSON into the reply.
func TestReplyCarriesPending(t *testing.T) {
	m := braintest.NewModel(braintest.Step{Text: `{"spoken": "Reading the diff, about half a minute.", "detail": "", "needs_user": false, "clarify": false, "pending": true}`})
	h := newHarness(t, m, nil)
	rep, err := h.brain.Utterance(context.Background(), Request{Question: "why did it fail?"})
	if err != nil || !rep.Pending {
		t.Fatalf("reply = %+v err = %v", rep, err)
	}
}
