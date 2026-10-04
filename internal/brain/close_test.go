package brain_test

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// "把 kvoice Claude 关掉吧" (the CEO's phone session, 2026-10-04) is the
// interrupt tool with close true: a close through the Host, never a
// Ctrl-C; protected_ok goes along as the brain gave it.
func TestInterruptToolCloses(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolInterrupt, map[string]any{"agent": "auth", "close": true})}},
		braintest.Step{Text: braintest.Reply("Closed auth.", "")},
	), nil)
	if _, err := h.ask("把 auth 关掉吧", ""); err != nil {
		t.Fatal(err)
	}
	if len(h.host.Closed) != 1 || h.host.Closed[0].ProtectedOK || h.host.Interrupts != nil {
		t.Fatalf("closed = %+v, interrupts = %v", h.host.Closed, h.host.Interrupts)
	}
	if !strings.Contains(h.model.Calls()[1].Text(), "Closed auth.") {
		t.Errorf("the model did not see the close echo:\n%s", h.model.Calls()[1].Text())
	}
}

// A close the daemon's rail refuses (no unused words of the CEO's, a
// protected agent) is a brain.rail_blocked the model reads.
func TestInterruptToolCloseRailBlocked(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolInterrupt, map[string]any{"agent": "auth", "close": true})}},
		braintest.Step{Text: braintest.Reply("Do you mean that one?", "")},
	), nil)
	h.host.CloseErr = fmt.Errorf("%w: auth works in sheepdog's own directory", ErrNotPermitted)
	if _, err := h.ask("close it", ""); err != nil {
		t.Fatal(err)
	}
	if h.log.count("brain.rail_blocked") != 1 {
		t.Error("no brain.rail_blocked for a refused close")
	}
	if !strings.Contains(h.model.Calls()[1].Text(), "sheepdog's own directory") {
		t.Errorf("the model did not read the refusal:\n%s", h.model.Calls()[1].Text())
	}
}
