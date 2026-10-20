package brain_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// displayHost is the test Host with a screen.
type displayHost struct {
	*braintest.Host
	mu      sync.Mutex
	visible bool
	shown   []brain.CardRequest
}

func (d *displayHost) ScreenVisible() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.visible
}

func (d *displayHost) Show(_ context.Context, req brain.CardRequest) (brain.CardResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.visible {
		return brain.CardResult{}, brain.ErrNotVisible
	}
	d.shown = append(d.shown, req)
	return brain.CardResult{ID: "c1", Title: req.Title}, nil
}

func newShowHarness(t *testing.T, visible bool, steps ...braintest.Step) (*harness, *displayHost) {
	h := newHarness(t, braintest.NewModel(steps...), nil)
	dh := &displayHost{Host: h.host, visible: visible}
	h.brain.SetHost(dh)
	return h, dh
}

func showCall(args map[string]any) braintest.Step {
	return braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolShow, args)}}
}

func TestShowCardVisible(t *testing.T) {
	md := "| Agent | State |\n|---|---|\n| auth | working |"
	h, dh := newShowHarness(t, true,
		showCall(map[string]any{"kind": "card", "title": "Agents", "markdown": md}),
		showCall(map[string]any{"kind": "card", "title": "Agents", "markdown": md}),
		braintest.Step{Text: braintest.Reply("It's on your screen: two agents.", "")},
	)
	rep, err := h.ask("list the agents", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(dh.shown) != 1 || dh.shown[0].Markdown != md || dh.shown[0].Kind != brain.ShowCard {
		t.Fatalf("shown = %+v (the same card twice in a turn is shown once)", dh.shown)
	}
	if len(rep.Shown) != 1 || rep.Shown[0] != "Agents" {
		t.Errorf("reply.Shown = %v", rep.Shown)
	}
	second := h.model.Calls()[1].Text()
	if !strings.Contains(second, "Shown on the user's screen") || !strings.Contains(second, "Do not read the card aloud") {
		t.Errorf("tool result does not tell the model how to speak:\n%s", second)
	}
	if !strings.Contains(h.model.Calls()[0].Text(), "Screen: the user's screen is visible") {
		t.Error("state block lacks the screen line")
	}
	// The log carries the markdown's size, not the markdown.
	for _, ev := range h.log.ofType("brain.tool_call") {
		args, _ := payload(ev)["args"].(map[string]any)
		if md, _ := args["markdown"].(string); strings.Contains(md, "auth") {
			t.Errorf("brain.tool_call logged the card: %v", args)
		}
	}
}

func TestShowNotVisible(t *testing.T) {
	h, dh := newShowHarness(t, false,
		showCall(map[string]any{"kind": "card", "title": "Agents", "markdown": "- auth\n- docs"}),
		braintest.Step{Text: braintest.Reply("Auth is working and docs is idle.", "")},
	)
	rep, err := h.ask("list the agents", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(dh.shown) != 0 || len(rep.Shown) != 0 {
		t.Fatalf("shown = %v, reply.Shown = %v", dh.shown, rep.Shown)
	}
	if second := h.model.Calls()[1].Text(); !strings.Contains(second, "Not shown: the user's screen is not visible") {
		t.Errorf("result:\n%s", second)
	}
	if !strings.Contains(h.model.Calls()[0].Text(), "Screen: not visible") {
		t.Error("state block lacks the not-visible line")
	}
}

func TestShowAgentKinds(t *testing.T) {
	h, dh := newShowHarness(t, true,
		braintest.Step{Calls: []*genai.FunctionCall{
			braintest.FC(brain.ToolShow, map[string]any{"kind": "excerpt", "agent": "auth", "lines": float64(500)}),
			braintest.FC(brain.ToolShow, map[string]any{"kind": "agent", "agent": "docs"}),
			braintest.FC(brain.ToolShow, map[string]any{"kind": "approval", "agent": "docs"}),
			braintest.FC(brain.ToolShow, map[string]any{"kind": "agent", "agent": "nobody"}),
		}},
		braintest.Step{Text: braintest.Reply("On your screen.", "")},
	)
	if _, err := h.ask("show me", ""); err != nil {
		t.Fatal(err)
	}
	if len(dh.shown) != 2 {
		t.Fatalf("shown = %+v (approval without a pending prompt and an unknown agent are refused)", dh.shown)
	}
	if dh.shown[0].PaneID != "w5:p1" || dh.shown[0].Lines != brain.ShowExcerptMax || dh.shown[1].Kind != brain.ShowAgent || dh.shown[1].PaneID != "w6:p1" {
		t.Errorf("shown = %+v", dh.shown)
	}
	second := h.model.Calls()[1].Text()
	if !strings.Contains(second, "docs has no open approval prompt") || !strings.Contains(second, `no agent called "nobody"`) {
		t.Errorf("errors:\n%s", second)
	}
}

func TestShowRefusedInEventTurn(t *testing.T) {
	h, dh := newShowHarness(t, true,
		showCall(map[string]any{"kind": "card", "title": "Done", "markdown": "- all done"}),
		braintest.Step{Text: braintest.Reply("", "")},
	)
	_, _ = h.brain.Turn(context.Background(), brain.Request{Kind: brain.KindAnnounce, Trigger: brain.TriggerEvent, Agent: "auth", Question: "auth finished"})
	if len(dh.shown) != 0 {
		t.Fatalf("an event turn showed a card: %+v", dh.shown)
	}
	for _, decl := range h.model.Calls()[0].Config.Tools[0].FunctionDeclarations {
		if decl.Name == brain.ToolShow {
			t.Error("show declared in an event turn")
		}
	}
}
