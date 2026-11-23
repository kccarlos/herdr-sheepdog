package supervisor

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
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

func sendPaneLabeledTransition(h *harness, pane, ws string, from, to herdr.State, labels map[string]string) {
	h.t.Helper()
	h.srv.SetStatus(pane, to)
	h.sup.HandleTransition(context.Background(), herdr.Transition{
		PaneID:         pane,
		From:           from,
		To:             to,
		At:             h.clock.Now(),
		Agent:          "codex",
		Name:           ws,
		WorkspaceID:    ws,
		WorkspaceLabel: ws,
		Labels:         labels,
	})
}

func TestFactoryQuietBlockedDecisionsAndBriefing(t *testing.T) {
	for _, role := range []string{"worker", "reviewer", "checks", "helper"} {
		t.Run(role, func(t *testing.T) {
			fn := &fakeNotifier{}
			h := newHarness(t, withNotifier(fn), func(c *Config) { c.Chattiness = string(Chatty) })

			// Keep a non-factory idle agent alongside the quiet factory terminal.
			h.addAgent("w6:p1", "docs", "claude", herdr.StateIdle)

			pane := "f:" + role
			ws := "f" + role
			labels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": role}
			h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: ws, Label: ws})
			h.srv.SetAgent(herdr.AgentInfo{
				PaneID: pane, WorkspaceID: ws, TabID: ws + ":t1",
				Agent: "codex", Name: ws, Status: herdr.StateIdle, Cwd: "/work/" + ws, Labels: labels,
			})
			sendPaneLabeledTransition(h, pane, ws, "", herdr.StateIdle, labels)
			h.clock.Advance(time.Second)
			sendPaneLabeledTransition(h, pane, ws, herdr.StateIdle, herdr.StateWorking, labels)
			h.clock.Advance(time.Second)
			h.srv.SetScreen(pane, fixture(t, "codex-approval-prompt.screen.txt"))
			sendPaneLabeledTransition(h, pane, ws, herdr.StateWorking, herdr.StateBlocked, labels)

			if got := h.sup.DecisionsNow(); !strings.HasPrefix(got, "none:") || strings.Contains(got, ws) || strings.Contains(got, "waiting at a prompt") {
				t.Errorf("role=%s DecisionsNow() = %q, want none: with no factory %s", role, got, role)
			}
			if line := h.sup.decisionsLine(); !strings.HasPrefix(line, "Decisions open right now: none.") || strings.Contains(line, ws) {
				t.Errorf("role=%s decisionsLine() = %q, want none", role, line)
			}

			brief, panes := h.sup.briefingText()
			if strings.Contains(brief, ws) || strings.Contains(brief, "blocked") || strings.Contains(brief, "asking") || slices.Contains(panes, pane) {
				t.Errorf("role=%s briefingText() = %q (panes=%v), want %s excluded from voice briefing", role, brief, panes, ws)
			}

			h.sup.OnVoiceEvent(voice.EventResumed{})
			resumed := h.pusher.last(t).Text
			if strings.Contains(resumed, ws) || strings.Contains(resumed, "blocked") || strings.Contains(resumed, "asking") {
				t.Errorf("role=%s resumed voice briefing = %q, want %s excluded", role, resumed, ws)
			}
		})
	}

	t.Run("coordinator", func(t *testing.T) {
		fn := &fakeNotifier{}
		h := newHarness(t, withNotifier(fn), func(c *Config) { c.Chattiness = string(Chatty) })

		pane := "f:coord"
		ws := "fcoord"
		labels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": "coordinator"}
		h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: ws, Label: ws})
		h.srv.SetAgent(herdr.AgentInfo{
			PaneID: pane, WorkspaceID: ws, TabID: ws + ":t1",
			Agent: "codex", Name: ws, Status: herdr.StateIdle, Cwd: "/work/" + ws, Labels: labels,
		})
		sendPaneLabeledTransition(h, pane, ws, "", herdr.StateIdle, labels)
		h.clock.Advance(time.Second)
		sendPaneLabeledTransition(h, pane, ws, herdr.StateIdle, herdr.StateWorking, labels)
		h.clock.Advance(time.Second)
		h.srv.SetScreen(pane, fixture(t, "codex-approval-prompt.screen.txt"))
		sendPaneLabeledTransition(h, pane, ws, herdr.StateWorking, herdr.StateBlocked, labels)

		if got := h.sup.DecisionsNow(); !strings.HasPrefix(got, "yes:") || !strings.Contains(got, ws) || !strings.Contains(got, "waiting at a prompt") {
			t.Errorf("blocked coordinator DecisionsNow() = %q, want yes: naming %s", got, ws)
		}
		brief, panes := h.sup.briefingText()
		if !strings.Contains(brief, ws) || !slices.Contains(panes, pane) {
			t.Errorf("blocked coordinator briefingText() = %q (panes=%v), want %s included", brief, panes, ws)
		}
	})
}

func TestFactoryQuietSurvivesRepromptAndRestart(t *testing.T) {
	for _, role := range []string{"worker", "reviewer", "checks", "helper"} {
		t.Run(role, func(t *testing.T) {
			fn := &fakeNotifier{}
			h := newHarness(t, pollOpts, withNotifier(fn), func(c *Config) { c.Chattiness = string(Chatty) })

			pane := "f:" + role
			ws := "f" + role
			labels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": role}
			h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: ws, Label: ws})
			h.srv.SetAgent(herdr.AgentInfo{
				PaneID: pane, WorkspaceID: ws, TabID: ws + ":t1",
				Agent: "codex", Name: ws, Status: herdr.StateIdle, Cwd: "/work/" + ws, Labels: labels,
			})
			sendPaneLabeledTransition(h, pane, ws, "", herdr.StateIdle, labels)
			h.clock.Advance(time.Second)
			sendPaneLabeledTransition(h, pane, ws, herdr.StateIdle, herdr.StateWorking, labels)
			h.clock.Advance(time.Second)
			h.srv.SetScreen(pane, fixture(t, "codex-approval-prompt.screen.txt"))
			sendPaneLabeledTransition(h, pane, ws, herdr.StateWorking, herdr.StateBlocked, labels)

			// 1. Reprompt poll and reconcileReprompts must preserve terminal labels and stay quiet.
			h.sup.pollReprompt(pane)
			h.srv.SetScreen(pane, fixture(t, "codex-git-prompt.screen.txt"))
			_, _ = h.store.Update(pane, func(a *state.Agent) {
				if a.LastBlock != nil {
					a.LastBlock.Answered = true
				}
			})
			h.sup.reconcileReprompts(context.Background())

			if got := h.pusher.all(); len(got) != 0 {
				t.Fatalf("role=%s reprompt produced %d voice pushes, want 0: %+v", role, len(got), got)
			}
			if got := fn.all(); len(got) != 0 {
				t.Fatalf("role=%s reprompt produced %d phone notifications, want 0: %+v", role, len(got), got)
			}
			if p, ok := h.pending(); ok {
				t.Fatalf("role=%s reprompt created pending action %+v, want none", role, p)
			}

			// 2. Restarting Supervisor from persisted state.json must keep labels so startup
			// reconcileReprompts, DecisionsNow, and briefingText stay quiet.
			store2, err := state.Open(filepath.Join(h.dir, "state.json"), state.Options{})
			if err != nil {
				t.Fatalf("reopen state.json: %v", err)
			}
			t.Cleanup(func() { _ = store2.Close() })
			pusher2 := &fakePusher{}
			fn2 := &fakeNotifier{}
			sup2, err := New(store2, h.client, nil, pusher2, h.log, Config{
				PendingTTL:     120 * time.Second,
				Now:            h.clock.Now,
				DataDir:        h.dir,
				Chattiness:     string(Chatty),
				Notifier:       fn2,
				WorkerLog:      h.sup.cfg.WorkerLog,
				inputGrace:     -1,
				relayAfter:     -1,
				interruptCheck: 40 * time.Millisecond,
				RepromptPoll:   30 * time.Millisecond,
				bareAnswers:    true,
			})
			if err != nil {
				t.Fatalf("New supervisor after restart: %v", err)
			}
			t.Cleanup(sup2.shutdown)
			sup2.SetVoiceConnected(true)

			h.srv.SetScreen(pane, fixture(t, "agy-approval-prompt.screen.txt"))
			sup2.reconcileReprompts(context.Background())

			if got := pusher2.all(); len(got) != 0 {
				t.Errorf("role=%s post-restart reconcile produced %d voice pushes, want 0: %+v", role, len(got), got)
			}
			if got := fn2.all(); len(got) != 0 {
				t.Errorf("role=%s post-restart reconcile produced %d phone pushes, want 0: %+v", role, len(got), got)
			}
			if got := sup2.DecisionsNow(); !strings.HasPrefix(got, "none:") || strings.Contains(got, ws) {
				t.Errorf("role=%s post-restart DecisionsNow() = %q, want none:", role, got)
			}
			if brief, panes := sup2.briefingText(); strings.Contains(brief, ws) || slices.Contains(panes, pane) {
				t.Errorf("role=%s post-restart briefingText() = %q (panes=%v), want %s excluded", role, brief, panes, ws)
			}
		})
	}
}
