package supervisor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// closeWorld is a brain harness with kvoice Claude (w2:p1) and an agent
// in the daemon's own directory (w1:p1), and a herdr that closes panes.
func closeWorld(t *testing.T, m *braintest.Model) *harness {
	t.Helper()
	h := newBrainHarness(t, m, func(c *Config) { c.ProtectedDirs = []string{"/Users/kc/Documents/herdr-sheepdog"} })
	h.srv.Handle("pane.close", func(p json.RawMessage) (any, error) {
		var params struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(p, &params)
		h.srv.RemoveAgent(params.PaneID)
		return map[string]any{"type": "ok"}, nil
	})
	for _, a := range []struct{ pane, ws, cwd string }{
		{"w1:p1", "herdr-sheepdog", "/Users/kc/Documents/herdr-sheepdog"},
		{"w2:p1", "kvoice", "/Users/kc/Documents/kvoice"},
	} {
		h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: strings.Split(a.pane, ":")[0], Label: a.ws})
		h.srv.SetAgent(herdr.AgentInfo{PaneID: a.pane, WorkspaceID: strings.Split(a.pane, ":")[0], Agent: "claude", Status: herdr.StateIdle, Cwd: a.cwd})
		h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: a.pane, To: herdr.StateIdle, At: h.clock.Now(), Agent: "claude", WorkspaceID: strings.Split(a.pane, ":")[0], WorkspaceLabel: a.ws, Cwd: a.cwd})
	}
	return h
}

func closeCall(agent string, extra map[string]any) braintest.Step {
	args := map[string]any{"agent": agent, "close": true}
	for k, v := range extra {
		args[k] = v
	}
	return braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolInterrupt, args)}}
}

// "把 kvoice Claude 关掉吧" (the CEO's phone session, 2026-10-04): the brain
// closes that agent with the interrupt tool's close; herdr's pane.close
// gets its pane, supervisor.close is logged, and the CEO's words are
// spent: a second close in the same breath is refused.
func TestCloseAgentByVoice(t *testing.T) {
	m := braintest.NewModel(
		closeCall("kvoice-claude", nil),
		braintest.Step{Text: braintest.Reply("kvoice Claude is closed.", "")},
	)
	h := closeWorld(t, m)
	res := h.say("把 kvoice Claude 关掉吧")
	if !strings.Contains(res.Spoken, "closed") {
		t.Errorf("result = %+v", res)
	}
	closed := h.srv.RequestsFor("pane.close")
	if len(closed) != 1 || string(closed[0].Params) != `{"pane_id":"w2:p1"}` {
		t.Fatalf("pane.close requests = %v", closed)
	}
	ev := h.log.ofType("supervisor.close")
	if len(ev) != 1 || ev[0].PaneID != "w2:p1" {
		t.Fatalf("supervisor.close = %+v", ev)
	}
	if !strings.Contains(m.Calls()[1].Text(), "Closed kvoice Claude; its session has ended.") {
		t.Errorf("the brain did not read the echo:\n%s", m.Calls()[1].Text())
	}
	// The same words cannot close a second agent.
	_, err := h.sup.closeAgent(context.Background(), brain.CloseRequest{PaneID: "w1:p1", ProtectedOK: true})
	if err == nil || !strings.Contains(err.Error(), "already answered a prompt or started or closed an agent") {
		t.Errorf("a second close on the same words = %v", err)
	}
	if n := len(h.srv.RequestsFor("pane.close")); n != 1 {
		t.Errorf("pane.close requests = %d after spent words", n)
	}
}

// No words of the CEO's behind it (an event turn cannot call it at all;
// a CEO turn with nothing said, e.g. the voice relaying on its own)
// closes nothing.
func TestCloseNeedsTheCEOsWords(t *testing.T) {
	h := closeWorld(t, braintest.NewModel())
	_, err := h.sup.closeAgent(context.Background(), brain.CloseRequest{PaneID: "w2:p1"})
	if err == nil || !strings.Contains(err.Error(), "the CEO has said nothing that asks for closing an agent") {
		t.Fatalf("close without words = %v", err)
	}
	if n := len(h.srv.RequestsFor("pane.close")); n != 0 {
		t.Errorf("pane.close requests = %d", n)
	}
}

// The agent working in the daemon's own directory (where the CEO's
// orchestrator session runs) is refused unless the brain says the CEO
// named it, with the reason for the brain to ask; with protected_ok it
// closes.
func TestCloseProtectsTheOrchestratorsPane(t *testing.T) {
	h := closeWorld(t, braintest.NewModel())
	h.hear("帮把这个 sheepdog Claude 的 session 给关掉吧")
	_, err := h.sup.closeAgent(context.Background(), brain.CloseRequest{PaneID: "w1:p1"})
	if err == nil || !strings.Contains(err.Error(), "sheepdog's own directory") || !strings.Contains(err.Error(), "protected_ok") {
		t.Fatalf("close of the protected agent = %v", err)
	}
	if ev := h.log.ofType("supervisor.close_refused"); len(ev) != 1 || ev[0].Payload.(map[string]any)["reason"] != "protected" {
		t.Fatalf("close_refused = %+v", ev)
	}
	// The refusal did not spend the words: the CEO's confirmation does.
	h.hear("对，就是 sheepdog Claude，关掉")
	if _, err := h.sup.closeAgent(context.Background(), brain.CloseRequest{PaneID: "w1:p1", ProtectedOK: true}); err != nil {
		t.Fatal(err)
	}
	if closed := h.srv.RequestsFor("pane.close"); len(closed) != 1 || string(closed[0].Params) != `{"pane_id":"w1:p1"}` {
		t.Errorf("pane.close requests = %v", closed)
	}
}

// The pane the daemon itself runs in is protected the same way.
func TestCloseProtectsTheDaemonsOwnPane(t *testing.T) {
	h := closeWorld(t, braintest.NewModel())
	h.sup.cfg.OwnPane = "w2:p1"
	h.hear("关掉 kvoice Claude")
	if _, err := h.sup.closeAgent(context.Background(), brain.CloseRequest{PaneID: "w2:p1"}); err == nil || !strings.Contains(err.Error(), "pane sheepdog itself runs in") {
		t.Fatalf("close of the daemon's own pane = %v", err)
	}
}
