package supervisor

import (
	"context"
	"errors"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Pins review-pr2-neon finding 1: an ended factory run in a finished project
// must not block window reads, input, /term availability, or user actions on
// an ordinary interactive agent that receives the recycled terminal address.
func TestEndedFactoryRunDoesNotBlockOrdinaryAgentOnRecycledAddress(t *testing.T) {
	ctx := context.Background()
	paths := factory.Paths{DataDir: t.TempDir()}
	if err := factory.SaveState(paths.State("proj-old"), flow.State{
		Seq: 2, Settings: flow.DefaultSettings(),
		Project: flow.Project{ID: "proj-old", Folder: "/work/old", Goal: "Old", Started: false, Finished: true},
		Tasks: []flow.Task{{
			ID: "T1", Title: "done",
			Attempts: []flow.Attempt{{
				BaseCommit: "0",
				RunID:      "",
				Run:        &flow.RunRecord{Role: flow.RoleWorker, CLI: "codex", Address: "t1", State: flow.PhaseEnded, Result: flow.EndStopped},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	h := newBrainHarness(t, braintest.NewModel(), func(c *Config) { c.Factory = factory.New(paths, nil) })
	h.addAgent("t1", "ordinary", "claude", herdr.StateIdle)
	h.srv.SetScreen("t1", "ordinary agent screen")

	if !h.sup.WindowAvailable("t1") {
		t.Errorf("WindowAvailable(ordinary \"t1\") = false, want true")
	}
	if _, err := h.sup.Screen(ctx, "t1", 0); err != nil {
		t.Errorf("Screen(ordinary \"t1\") = %v, want nil", err)
	}
	if _, err := h.sup.WindowPrompt(ctx, "t1", "hello"); err != nil {
		t.Errorf("WindowPrompt(ordinary \"t1\") = %v, want nil", err)
	}
	if _, err := h.sup.UserRename(ctx, "t1", "my-agent"); err != nil {
		t.Errorf("UserRename(ordinary \"t1\") = %v, want nil", err)
	}
	if _, err := h.sup.UserInterrupt(ctx, "t1"); err != nil {
		t.Errorf("UserInterrupt(ordinary \"t1\") = %v, want nil", err)
	}
	if _, err := h.sup.UserClose(ctx, "t1"); err != nil {
		t.Errorf("UserClose(ordinary \"t1\") = %v, want nil", err)
	}
}

// Pins review-pr2-neon finding 4: the shared factory lifecycle rail must hold
// when Factory is nil and labels live only in herdr, must return ErrNotPermitted
// consistently across UserRename/UserInterrupt/UserClose on a live coordinator,
// and must also guard voice Stop() interrupts.
func TestFactoryLifecycleRailNoBypassAndConsistentErrors(t *testing.T) {
	ctx := context.Background()
	noMutationRPCs := func(t *testing.T, h *harness, label string) {
		t.Helper()
		for _, m := range []string{"agent.rename", "pane.send_keys", "pane.close"} {
			if n := len(h.srv.RequestsFor(m)); n != 0 {
				t.Errorf("%s: %d %s RPC(s) reached herdr, want 0", label, n, m)
			}
		}
	}

	t.Run("factory_nil_still_checks_herdr_labels", func(t *testing.T) {
		h := newBrainHarness(t, braintest.NewModel()) // Factory == nil
		h.addAgent("hc:coord-1", "coord", "claude", herdr.StateWorking)
		h.srv.SetAgent(herdr.AgentInfo{
			PaneID: "hc:coord-1", Agent: "claude", Name: "coord", Status: herdr.StateWorking,
			Labels: map[string]string{"sheepdog.project": "proj-notes", "sheepdog.role": flow.RoleCoordinator},
		})
		if _, err := h.sup.UserRename(ctx, "hc:coord-1", "oops"); !errors.Is(err, brain.ErrNotPermitted) {
			t.Errorf("UserRename(Factory==nil, herdr-labeled) = %v, want ErrNotPermitted", err)
		}
		if _, err := h.sup.UserInterrupt(ctx, "hc:coord-1"); !errors.Is(err, brain.ErrNotPermitted) {
			t.Errorf("UserInterrupt(Factory==nil, herdr-labeled) = %v, want ErrNotPermitted", err)
		}
		if _, err := h.sup.UserClose(ctx, "hc:coord-1"); !errors.Is(err, brain.ErrNotPermitted) {
			t.Errorf("UserClose(Factory==nil, herdr-labeled) = %v, want ErrNotPermitted", err)
		}
		noMutationRPCs(t, h, "factory_nil_herdr_labels")
	})

	t.Run("user_routes_consistently_return_not_permitted_on_live_coordinator", func(t *testing.T) {
		h := setupFactoryRailHarness(t, braintest.NewModel())
		h.srv.SetAgent(herdr.AgentInfo{PaneID: "hc:coord-live", Agent: "claude", Status: herdr.StateWorking})
		_, errRen := h.sup.UserRename(ctx, "hc:coord-live", "x")
		_, errInt := h.sup.UserInterrupt(ctx, "hc:coord-live")
		_, errCls := h.sup.UserClose(ctx, "hc:coord-live")
		if !errors.Is(errRen, brain.ErrNotPermitted) || !errors.Is(errInt, brain.ErrNotPermitted) || !errors.Is(errCls, brain.ErrNotPermitted) {
			t.Errorf("live coordinator user routes = (rename:%v, interrupt:%v, close:%v), want ErrNotPermitted on all three", errRen, errInt, errCls)
		}
		noMutationRPCs(t, h, "live_coordinator_user_routes")
	})

	t.Run("stop_procedure_does_not_interrupt_factory_pane", func(t *testing.T) {
		h := setupFactoryRailHarness(t, braintest.NewModel())
		h.addAgent("hc:coord-unlabeled", "unlab", "claude", herdr.StateWorking)
		h.srv.SetScreen("hc:coord-unlabeled", "Claude working")
		bh := brainHost{h.sup}
		if _, err := bh.Prompt(ctx, "hc:coord-unlabeled", "do something"); err != nil {
			t.Fatalf("Prompt setup: %v", err)
		}
		if _, err := h.sup.Stop(ctx, StopTriggerBareWord); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if n := len(h.srv.RequestsFor("pane.send_keys")); n != 0 {
			t.Errorf("Stop() sent %d pane.send_keys RPC(s) to factory pane hc:coord-unlabeled, want 0", n)
		}
	})
}
