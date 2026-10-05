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

// shortBudget leaves a tool the 1 s minimum (the turn's deadline minus
// StepReserve is under it), so a hung tool times out fast.
func shortBudget(c *Config) {
	c.UtteranceTimeout, c.FinalGrace, c.StepReserve = time.Second, 2*time.Second, 2500*time.Millisecond
}

// toolTimeoutCase runs a turn whose first step calls call against a hung
// tool and checks that the model's next step got a "timed out" result
// and answered, and that brain.tool_timeout was logged.
func toolTimeoutCase(t *testing.T, h *harness, name string) {
	t.Helper()
	t0 := time.Now()
	rep, err := h.ask("what is auth doing?", "auth")
	if err != nil {
		t.Fatalf("turn fell back: %v", err)
	}
	if rep.Spoken != "Auth is not responding." {
		t.Errorf("reply = %+v", rep)
	}
	// The timeout reaches the voice as a fact beside the answer
	// (Reply.NotVerified), and the agent as one the answer is about.
	if len(rep.NotVerified) != 1 || !strings.Contains(rep.NotVerified[0], name+" about auth timed out after 1 s") {
		t.Errorf("NotVerified = %v", rep.NotVerified)
	}
	if len(rep.Agents) != 1 || rep.Agents[0] != "auth" {
		t.Errorf("Agents = %v", rep.Agents)
	}
	if d := time.Since(t0); d > 2500*time.Millisecond {
		t.Errorf("turn took %v", d)
	}
	calls := h.model.Calls()
	if len(calls) != 2 {
		t.Fatalf("model calls = %d", len(calls))
	}
	if txt := calls[1].Text(); !strings.Contains(txt, name+" timed out after 1 s") {
		t.Errorf("second step did not see the timeout:\n%s", txt)
	}
	ev := h.log.ofType("brain.tool_timeout")
	if len(ev) != 1 {
		t.Fatalf("brain.tool_timeout = %v", ev)
	}
	if p := payload(ev[0]); p["name"] != name || p["timeout_ms"] != int64(1000) || p["agent"] != "auth" {
		t.Errorf("brain.tool_timeout = %v", p)
	}
	if tr := payload(h.log.ofType("brain.tool_result")[0]); !strings.Contains(tr["error"].(string), "timed out after 1 s") {
		t.Errorf("brain.tool_result = %v", tr)
	}
}

func timeoutModel(call *genai.FunctionCall) *braintest.Model {
	return braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{call}},
		braintest.Step{Text: braintest.Reply("Auth is not responding.", "")},
	)
}

// A read_screen whose host call hangs until its context ends: the tool's
// own deadline ends it and the model answers from the timeout.
func TestReadScreenTimesOut(t *testing.T) {
	h := newHarness(t, timeoutModel(braintest.FC(ToolReadScreen, map[string]any{"agent": "auth"})), shortBudget)
	h.host.ReadHook = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	toolTimeoutCase(t, h, ToolReadScreen)
}

// A run_cli whose host ignores its context: the read is abandoned at the
// deadline rather than waited for.
func TestRunCLIAbandonedAtTimeout(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h := newHarness(t, timeoutModel(braintest.FC(ToolRunCLI, map[string]any{"agent": "auth", "question": "which test failed?"})), shortBudget)
	h.host.CLI = func(string, string) (string, error) { <-release; return "too late", nil }
	toolTimeoutCase(t, h, ToolRunCLI)
}

// A tool that answers in time is untouched: no brain.tool_timeout.
func TestToolWithinTimeoutIsNotFlagged(t *testing.T) {
	h := newHarness(t, timeoutModel(braintest.FC(ToolReadScreen, map[string]any{"agent": "auth"})), nil)
	if _, err := h.ask("what is auth doing?", "auth"); err != nil {
		t.Fatal(err)
	}
	if n := h.log.count("brain.tool_timeout"); n != 0 {
		t.Errorf("brain.tool_timeout logged %d times", n)
	}
}
