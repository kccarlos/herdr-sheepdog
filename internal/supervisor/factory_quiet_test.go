package supervisor

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

func sendLabeledTransition(h *harness, from, to herdr.State, closed bool, labels map[string]string) {
	h.t.Helper()
	const pane = "w5:p1"
	if !closed {
		h.srv.SetStatus(pane, to)
	}
	h.sup.HandleTransition(context.Background(), herdr.Transition{
		PaneID:         pane,
		From:           from,
		To:             to,
		At:             h.clock.Now(),
		Agent:          "codex",
		WorkspaceID:    "demo",
		WorkspaceLabel: "demo",
		Closed:         closed,
		Labels:         labels,
	})
}

func TestFactoryQuietSupervisor(t *testing.T) {
	for _, role := range []string{"worker", "reviewer", "checks", "helper"} {
		t.Run(role, func(t *testing.T) {
			fn := &fakeNotifier{}
			h := newHarness(t, withNotifier(fn), func(c *Config) { c.Chattiness = string(Chatty) })
			pane := "w5:p1"
			labels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": role}

			h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "demo", Label: "demo"})
			h.srv.SetAgent(herdr.AgentInfo{
				PaneID: pane, WorkspaceID: "demo", TabID: "demo:t1",
				Agent: "codex", Status: herdr.StateIdle, Cwd: "/work/demo", Labels: labels,
			})
			sendLabeledTransition(h, "", herdr.StateIdle, false, labels)
			h.clock.Advance(time.Second)
			sendLabeledTransition(h, herdr.StateIdle, herdr.StateWorking, false, labels)
			h.clock.Advance(time.Second)

			// Blocked with an approval prompt must still stay completely silent.
			h.srv.SetScreen(pane, fixture(t, "codex-approval-prompt.screen.txt"))
			sendLabeledTransition(h, herdr.StateWorking, herdr.StateBlocked, false, labels)
			h.clock.Advance(time.Second)

			// Done and closed while busy must also stay completely silent.
			h.srv.SetScreen(pane, "Done.\n")
			sendLabeledTransition(h, herdr.StateWorking, herdr.StateDone, false, labels)
			h.clock.Advance(time.Second)
			sendLabeledTransition(h, herdr.StateDone, herdr.StateWorking, false, labels)
			h.clock.Advance(time.Second)
			sendLabeledTransition(h, herdr.StateWorking, herdr.StateUnknown, true, labels)

			if got := h.pusher.all(); len(got) != 0 {
				t.Errorf("role=%s produced %d voice pushes, want 0: %+v", role, len(got), got)
			}
			if got := fn.all(); len(got) != 0 {
				t.Errorf("role=%s produced %d phone pushes, want 0: %+v", role, len(got), got)
			}
		})
	}

	t.Run("coordinator", func(t *testing.T) {
		fn := &fakeNotifier{}
		h := newHarness(t, withNotifier(fn), func(c *Config) { c.Chattiness = string(Chatty) })
		pane := "w5:p1"
		labels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": "coordinator"}

		h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "demo", Label: "demo"})
		h.srv.SetAgent(herdr.AgentInfo{
			PaneID: pane, WorkspaceID: "demo", TabID: "demo:t1",
			Agent: "codex", Status: herdr.StateIdle, Cwd: "/work/demo", Labels: labels,
		})
		sendLabeledTransition(h, "", herdr.StateIdle, false, labels)
		h.clock.Advance(time.Second)
		sendLabeledTransition(h, herdr.StateIdle, herdr.StateWorking, false, labels)
		h.clock.Advance(time.Second)
		h.srv.SetScreen(pane, "All tasks planned.\n")
		sendLabeledTransition(h, herdr.StateWorking, herdr.StateDone, false, labels)
		h.clock.Advance(time.Second)
		sendLabeledTransition(h, herdr.StateDone, herdr.StateIdle, false, labels)

		if got := h.pusher.all(); len(got) != 0 {
			t.Fatalf("coordinator non-blocked transitions produced %d voice pushes, want 0: %+v", len(got), got)
		}
		if got := fn.all(); len(got) != 0 {
			t.Fatalf("coordinator non-blocked transitions produced %d phone pushes, want 0: %+v", len(got), got)
		}

		// When the coordinator is blocked, it produces both a voice line and a phone push.
		h.clock.Advance(time.Second)
		sendLabeledTransition(h, herdr.StateIdle, herdr.StateWorking, false, labels)
		h.clock.Advance(time.Second)
		h.srv.SetScreen(pane, fixture(t, "codex-approval-prompt.screen.txt"))
		sendLabeledTransition(h, herdr.StateWorking, herdr.StateBlocked, false, labels)

		if got := h.pusher.all(); len(got) != 1 {
			t.Errorf("coordinator blocked voice pushes = %d, want 1: %+v", len(got), got)
		}
		if got := fn.all(); len(got) != 1 {
			t.Errorf("coordinator blocked phone pushes = %d, want 1: %+v", len(got), got)
		}
	})

	t.Run("non_factory", func(t *testing.T) {
		fn := &fakeNotifier{}
		h := newHarness(t, withNotifier(fn), func(c *Config) { c.Chattiness = string(Chatty) })
		h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
		h.clock.Advance(time.Second)
		h.srv.SetScreen("w5:p1", "Finished tests.\n")
		h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)

		if got := h.pusher.all(); len(got) == 0 {
			t.Fatal("non-factory done produced 0 voice pushes, want >= 1")
		}

		beforeVoice := len(h.pusher.all())
		h.clock.Advance(time.Second)
		h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
		h.clock.Advance(time.Second)
		h.block("w5:p1", "codex-approval-prompt.screen.txt")

		if got := len(h.pusher.all()); got <= beforeVoice {
			t.Errorf("non-factory blocked produced no voice push (before=%d, after=%d)", beforeVoice, got)
		}
		if got := fn.all(); len(got) != 1 {
			t.Errorf("non-factory blocked phone pushes = %d, want 1", len(got))
		}
	})
}
