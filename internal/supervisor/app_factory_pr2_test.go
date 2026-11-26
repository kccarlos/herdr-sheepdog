package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

func setupFactoryRailHarness(t *testing.T, m *braintest.Model) *harness {
	t.Helper()
	paths := factory.Paths{DataDir: t.TempDir()}
	for _, p := range [][3]string{{"proj-notes", "hc:coord-blocked", flow.PhaseBlocked}, {"proj-unlab", "hc:coord-unlabeled", flow.PhaseWorking}, {"proj-live", "hc:coord-live", flow.PhaseWorking}} {
		if err := factory.SaveState(paths.State(p[0]), flow.State{Seq: 1, Settings: flow.DefaultSettings(), Project: flow.Project{
			ID: p[0], Folder: "/work/" + p[0], Goal: p[0], Started: true, RunID: p[1],
			Run: &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", Address: p[1], State: p[2]},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	h := newBrainHarness(t, m, func(c *Config) { c.Factory = factory.New(paths, nil) })
	h.srv.Handle("pane.close", func(p json.RawMessage) (any, error) {
		var q struct {
			PaneID string `json:"pane_id"`
		}
		_ = json.Unmarshal(p, &q)
		h.srv.RemoveAgent(q.PaneID)
		return map[string]any{"type": "ok"}, nil
	})
	return h
}

func addBlockedCoordinator(t *testing.T, h *harness, pane, name string) {
	t.Helper()
	labels := map[string]string{"sheepdog.project": "proj-notes", "sheepdog.role": flow.RoleCoordinator}
	h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "ws-notes", Label: "notes"})
	h.srv.SetAgent(herdr.AgentInfo{PaneID: pane, WorkspaceID: "ws-notes", Agent: "claude", Name: name, Status: herdr.StateWorking, Cwd: "/work/notes", Labels: labels})
	h.srv.SetScreen(pane, fixture(t, "codex-approval-prompt.screen.txt"))
	sendPaneLabeledTransition(h, pane, name, herdr.StateWorking, herdr.StateBlocked, labels)
	if row, ok := h.store.Get(pane); !ok || row.State != herdr.StateBlocked {
		t.Fatalf("blocked coordinator %s row = (%v, %+v)", pane, ok, row)
	}
}

// Pins app-factory.md §4 (gap 7), §9, review-d10-1 change 3: one shared rail
// refuses rename, interrupt and close on any factory pane (sheepdog.project
// label or a project's current coordinator), blocked or not, on both user
// routes and orchestrator tools.
func TestFactoryPaneRefusedBySharedRailOnUserAndOrchestratorRoutes(t *testing.T) {
	ctx := context.Background()
	wantDenied := func(t *testing.T, name string, fn func() (string, error)) {
		t.Helper()
		if echo, err := fn(); !errors.Is(err, brain.ErrNotPermitted) {
			t.Errorf("%s = (%q, %v), want ErrNotPermitted", name, echo, err)
		}
	}
	noRPCs := func(t *testing.T, h *harness, label string) {
		t.Helper()
		for _, m := range []string{"agent.rename", "pane.send_keys", "pane.close"} {
			if n := len(h.srv.RequestsFor(m)); n != 0 {
				t.Errorf("%s: %d %s RPC(s) reached herdr, want 0", label, n, m)
			}
		}
	}
	checkUser := func(t *testing.T, h *harness, tag, pane, rename string) {
		t.Helper()
		wantDenied(t, "UserRename("+tag+")", func() (string, error) { return h.sup.UserRename(ctx, pane, rename) })
		wantDenied(t, "UserInterrupt("+tag+")", func() (string, error) { return h.sup.UserInterrupt(ctx, pane) })
		wantDenied(t, "UserClose("+tag+")", func() (string, error) { return h.sup.UserClose(ctx, pane) })
	}
	t.Run("blocked_coordinator_user_routes", func(t *testing.T) {
		h := setupFactoryRailHarness(t, braintest.NewModel())
		addBlockedCoordinator(t, h, "hc:coord-blocked", "coord-notes")
		checkUser(t, h, "blocked-coord", "hc:coord-blocked", "renamed")
		noRPCs(t, h, "blocked_coordinator_user_routes")
	})
	t.Run("blocked_coordinator_orchestrator_tools", func(t *testing.T) {
		m := braintest.NewModel(
			braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolNote, map[string]any{"agent": "coord-notes", "name": "renamed"})}},
			braintest.Step{Text: braintest.Reply("Refused.", "")},
			braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolInterrupt, map[string]any{"agent": "coord-notes"})}},
			braintest.Step{Text: braintest.Reply("Refused.", "")},
			closeCall("coord-notes", map[string]any{"protected_ok": true}),
			braintest.Step{Text: braintest.Reply("Refused.", "")},
		)
		h := setupFactoryRailHarness(t, m)
		addBlockedCoordinator(t, h, "hc:coord-blocked", "coord-notes")
		for _, u := range []string{"rename coord-notes to renamed", "interrupt coord-notes", "close coord-notes"} {
			h.say(u)
		}
		noRPCs(t, h, "blocked_coordinator_orchestrator_tools")
	})
	t.Run("engine_named_unlabeled_and_non_blocked_factory_panes", func(t *testing.T) {
		h := setupFactoryRailHarness(t, braintest.NewModel())
		h.addAgent("hc:coord-unlabeled", "unlab", "claude", herdr.StateWorking) // no sheepdog.project label
		h.hear("close unlab")
		checkUser(t, h, "unlab", "hc:coord-unlabeled", "x")
		wantDenied(t, "brain.Rename(unlab)", func() (string, error) { return brainHost{h.sup}.Rename(ctx, "hc:coord-unlabeled", "x") })
		wantDenied(t, "brain.Int(unlab)", func() (string, error) { return brainHost{h.sup}.Interrupt(ctx, "hc:coord-unlabeled") })
		wantDenied(t, "brain.Close(unlab)", func() (string, error) {
			return h.sup.closeAgent(ctx, brain.CloseRequest{PaneID: "hc:coord-unlabeled", ProtectedOK: true})
		})
		wLabels := map[string]string{"sheepdog.project": "proj-notes", "sheepdog.role": flow.RoleWorker}
		h.srv.SetAgent(herdr.AgentInfo{PaneID: "hc:coord-live", Agent: "claude", Status: herdr.StateWorking})
		h.srv.SetAgent(herdr.AgentInfo{PaneID: "hc:worker-live", Agent: "codex", Status: herdr.StateWorking, Labels: wLabels})
		wantDenied(t, "brain.Int(coord-live)", func() (string, error) { return brainHost{h.sup}.Interrupt(ctx, "hc:coord-live") })
		wantDenied(t, "brain.Int(worker-live)", func() (string, error) { return brainHost{h.sup}.Interrupt(ctx, "hc:worker-live") })
		if _, _, err := h.store.Apply(herdr.Transition{PaneID: "hc:worker-row", To: herdr.StateWorking, At: h.clock.Now(), Agent: "codex", WorkspaceID: "w", WorkspaceLabel: "w", Labels: wLabels}); err != nil {
			t.Fatal(err)
		}
		h.srv.SetAgent(herdr.AgentInfo{PaneID: "hc:worker-row", Agent: "codex", Status: herdr.StateWorking, Labels: wLabels})
		checkUser(t, h, "worker-row", "hc:worker-row", "y")
		noRPCs(t, h, "engine_named_and_non_blocked")
	})
}
