package factory

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

const agyIdleBox = "────────────────────────────────────────────────────────────────────────────────\n> Auto mode: edits & safe commands auto-approved (shift+tab to cycle)\n────────────────────────────────────────────────────────────────────────────────\n"

// Pins review-pr2-neon findings 1 and 6: ended runs must not stay cached in
// Engine.Pane (or collide with another project's live coordinator on a recycled
// terminal address), and unused Pane.Role should be removed.
func TestPaneCacheSkipsEndedRunsAndResolvesLiveCoordinatorDeterministically(t *testing.T) {
	if _, hasRole := reflect.TypeFor[Pane]().FieldByName("Role"); hasRole {
		t.Errorf("Pane.Role is unused and should be deleted")
	}
	paths := Paths{DataDir: t.TempDir()}
	// Project A: finished project whose ended worker still carries Run.Address = "t1"
	// (stopWorkerRec clears RunID = "" while leaving Run.Address intact).
	if err := SaveState(paths.State("proj-a"), flow.State{
		Seq: 2, Settings: flow.DefaultSettings(),
		Project: flow.Project{ID: "proj-a", Folder: "/work/alpha", Goal: "Alpha", Started: false, Finished: true},
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
	eng := New(paths, nil)
	if pane, ok := eng.Pane("t1"); ok {
		t.Errorf("Engine.Pane(\"t1\") returned ended worker from finished project: %+v, want not found", pane)
	}

	// Project B: running project whose live coordinator gets recycled address "t1".
	if err := SaveState(paths.State("proj-b"), flow.State{
		Seq: 2, Settings: flow.DefaultSettings(),
		Project: flow.Project{
			ID: "proj-b", Folder: "/work/beta", Goal: "Beta", Started: true, RunID: "t1",
			Run: &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", Address: "t1", State: flow.PhaseIdle},
		},
	}); err != nil {
		t.Fatal(err)
	}
	eng2 := New(paths, nil)
	missed := 0
	for range 100 {
		pane, ok := eng2.Pane("t1")
		if !ok || !pane.CurrentCoordinator || pane.Name != "beta" {
			missed++
		}
	}
	if missed > 0 {
		t.Errorf("Engine.Pane(\"t1\") missed live coordinator beta in %d/100 lookups (collided with ended run in proj-a)", missed)
	}
}

// Pins review-pr2-neon finding 2: wake requeue must not loop on normal replies,
// must bound repeated Esc retries, and must not double-deliver after the
// coordinator already acted and then answered an owner follow-up.
func TestCoordinatorWakeRequeueDoesNotLoopOrDoubleDeliver(t *testing.T) {
	t.Run("agy_blockquote_does_not_loop", func(t *testing.T) {
		h, coord1 := startDeliveredWake(t, "Agy blockquote no loop")
		h.edit(func(s *flow.State) { s.Project.Run.CLI = world.AgyCLI })
		for range 3 {
			h.setCoordWorking(coord1)
			h.srv.SetScreen(coord1, "> Wake: 1 task wake(s) wait on you.\n● Retried T1.\n> Note: T1 retried cleanly.\n"+agyIdleBox)
			h.coordIdleWithStatus(coord1, "idle")
			h.now = h.now.Add(time.Minute)
			h.pass()
		}
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 1 {
			t.Errorf("agy blockquote wake sends = %d, want 1 (must not loop on > quote in reply)", got)
		}
	})

	t.Run("claude_placeholder_without_rule_does_not_loop", func(t *testing.T) {
		h, coord1 := startDeliveredWake(t, "Claude placeholder no loop")
		for range 3 {
			h.setCoordWorking(coord1)
			h.srv.SetScreen(coord1, "❯ Wake: 1 task wake(s) wait on you.\n⏺ Retried T1.\n❯ Try \"check test coverage\"\n")
			h.coordIdleWithStatus(coord1, "idle")
			h.now = h.now.Add(time.Minute)
			h.pass()
		}
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 1 {
			t.Errorf("claude placeholder wake sends = %d, want 1 (must not loop on input placeholder)", got)
		}
	})

	t.Run("repeated_esc_is_bounded", func(t *testing.T) {
		h, coord1 := startDeliveredWake(t, "Repeated Esc bounded")
		for range 6 {
			h.setCoordWorking(coord1)
			h.srv.SetScreen(coord1, "❯ Wake: 1 task wake(s) wait on you.\n  ⎿  Interrupted · What should Claude do instead?\n❯ \n")
			h.coordIdleWithStatus(coord1, "idle")
		}
		st := h.state()
		sends := workerFailedWakeSends(t, h.srv.Requests())
		if st.Project.RunID == coord1 && sends > 4 {
			t.Errorf("repeated Esc on same coordinator %s redelivered wake %d times (want bounded <= 4 or escalation/stop)", coord1, sends)
		}
	})

	t.Run("handled_wake_followed_by_owner_question_does_not_double_deliver", func(t *testing.T) {
		h, coord1 := startDeliveredWake(t, "Owner followup no double delivery")
		h.setCoordWorking(coord1)
		// Coordinator acts on the wake (commits a coordinator log line, moving Seq),
		// and before the next engine pass the owner asks a follow-up in the window.
		h.eng.mu.Lock()
		st := h.state()
		if !h.eng.commitLogLocked(h.id, &st, LogLine{By: "coordinator", Task: "T1", Decision: "retried T1", Why: "handled worker_failed"}) {
			h.eng.mu.Unlock()
			t.Fatal("commitLogLocked failed")
		}
		h.eng.mu.Unlock()
		h.srv.SetScreen(coord1, "❯ Wake: 1 task wake(s) wait on you.\n⏺ Retried T1.\n❯ what did you just do?\n⏺ I retried T1.\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")
		h.now = h.now.Add(time.Minute)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 1 {
			t.Errorf("wake sends = %d after coordinator acted and answered owner follow-up, want 1", got)
		}
	})
}

// Pins review-pr2-neon finding 3: wake recovery must not silently drop in-flight
// wakes on Agy Esc, when an owner reply scrolls the submitted prompt out of the
// read window (Claude >200 lines or Codex lines=0 viewport), or when the screen
// is temporarily unreadable on the idle tick.
func TestCoordinatorWakeSurvivesAgyEscScrolledEvidenceAndUnreadableScreen(t *testing.T) {
	ctx := context.Background()

	t.Run("agy_esc_recovers_wake", func(t *testing.T) {
		h, coord1 := startDeliveredWake(t, "Agy Esc recovery")
		h.edit(func(s *flow.State) { s.Project.Run.CLI = world.AgyCLI })
		h.setCoordWorking(coord1)
		if _, err := h.client.Keys(ctx, coord1, "esc"); err != nil {
			t.Fatal(err)
		}
		h.srv.SetScreen(coord1, "> Wake: 1 task wake(s) wait on you.\nRequest cancelled by user.\n"+agyIdleBox)
		h.coordIdleWithStatus(coord1, "idle")
		for _, step := range []time.Duration{time.Minute, 15 * time.Minute, 15 * time.Minute, time.Minute, 10 * time.Minute, time.Minute} {
			if workerFailedWakeSends(t, h.srv.Requests()) >= 2 {
				return
			}
			h.now = h.now.Add(step)
			h.pass()
			if cur := h.state().Project.RunID; cur != "" && cur != coord1 {
				h.coordIdleWithStatus(cur, "idle")
			}
		}
		st := h.state()
		t.Fatalf("agy Esc dropped wake: sends=%d inFlight=%d wakes=%d", workerFailedWakeSends(t, h.srv.Requests()), len(st.Project.CoordWakesInFlight), len(st.Project.Wakes))
	})

	t.Run("claude_scrolled_owner_reply_recovers_wake", func(t *testing.T) {
		h, coord1 := startDeliveredWake(t, "Scrolled Claude owner turn")
		if _, err := h.client.Send(ctx, coord1, "why did you split T1?", false); err != nil {
			t.Fatal(err)
		}
		h.setCoordWorking(coord1)
		var b strings.Builder
		b.WriteString("❯ why did you split T1?\n")
		for range 210 {
			b.WriteString("Detailed explanation line.\n")
		}
		b.WriteString("❯ \n")
		h.srv.SetScreen(coord1, b.String())
		h.coordIdleWithStatus(coord1, "idle")
		for _, step := range []time.Duration{time.Minute, 15 * time.Minute, 15 * time.Minute, time.Minute, 10 * time.Minute, time.Minute} {
			if workerFailedWakeSends(t, h.srv.Requests()) >= 2 {
				return
			}
			h.now = h.now.Add(step)
			h.pass()
			if cur := h.state().Project.RunID; cur != "" && cur != coord1 {
				h.coordIdleWithStatus(cur, "idle")
			}
		}
		st := h.state()
		t.Fatalf("scrolled >200-line owner reply dropped wake: sends=%d inFlight=%d wakes=%d", workerFailedWakeSends(t, h.srv.Requests()), len(st.Project.CoordWakesInFlight), len(st.Project.Wakes))
	})

	t.Run("codex_viewport_scrolled_owner_reply_recovers_wake", func(t *testing.T) {
		c := newCodexRoles(t, map[string][]flow.Candidate{
			flow.RoleCoordinator: {{CLI: "codex", Model: codexModel, Effort: "medium"}},
		}, true)
		c.pass()
		coord1 := c.state().Project.RunID
		c.edit(func(s *flow.State) {
			s.Project.Wakes = []flow.Wake{{Task: "T1", Key: "worker_failed", Why: "worker needs help", At: *c.now}}
		})
		c.srv.SetScreen(coord1, codexIdleScreen("Initial goal", 15))
		c.turnEnds()
		if got := workerFailedWakeSends(t, c.srv.Requests()); got != 1 {
			t.Fatalf("initial codex wake sends = %d, want 1", got)
		}
		// Owner asks a question; reply pushes `› why T1?` off the visible viewport (ObserveRun reads lines=0 for Codex).
		c.srv.SetScreen(coord1, codexWorkingScreen("why T1?"))
		c.pass()
		c.srv.SetScreen(coord1, "• Long explanation line 1.\n• Long explanation line 2.\n\n  Worked for 12s • 8:44 PM\n\n› Ask Codex to do anything\n\n  gpt-6.2-sol high · 96% left\n")
		c.turnEnds()
		for _, step := range []time.Duration{time.Minute, 15 * time.Minute, 15 * time.Minute, time.Minute, 10 * time.Minute, time.Minute} {
			if workerFailedWakeSends(t, c.srv.Requests()) >= 2 {
				return
			}
			*c.now = c.now.Add(step)
			c.pass()
			if cur := c.state().Project.RunID; cur != "" && cur != coord1 {
				c.srv.SetScreen(cur, codexIdleScreen("Initial goal", 18))
				c.turnEnds()
			}
		}
		st := c.state()
		t.Fatalf("codex viewport-scrolled owner reply dropped wake: sends=%d turn=%d wakeTurn=%d inFlight=%d wakes=%d",
			workerFailedWakeSends(t, c.srv.Requests()), st.Project.Run.Turn, st.Project.CoordWakeTurn, len(st.Project.CoordWakesInFlight), len(st.Project.Wakes))
	})

	t.Run("unreadable_screen_does_not_retire_in_flight_wake", func(t *testing.T) {
		h, coord1 := startDeliveredWake(t, "Unreadable screen preserves wake")
		h.setCoordWorking(coord1)
		if _, err := h.client.Keys(ctx, coord1, "esc"); err != nil {
			t.Fatal(err)
		}
		// Screen temporarily empty/unreadable on the idle transition tick.
		h.srv.SetScreen(coord1, "")
		h.coordIdleWithStatus(coord1, "idle")
		if st := h.state(); len(st.Project.CoordWakesInFlight) == 0 && len(st.Project.Wakes) == 0 && workerFailedWakeSends(t, h.srv.Requests()) == 1 {
			t.Fatalf("empty/unreadable screen on idle tick silently retired CoordWakesInFlight")
		}
		// Once the screen is readable again with the interruption marker, the wake recovers.
		h.srv.SetScreen(coord1, "❯ Wake: 1 task wake(s) wait on you.\n  ⎿  Interrupted · What should Claude do instead?\n❯ \n")
		h.now = h.now.Add(time.Minute)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got < 2 {
			t.Fatalf("wake did not recover once screen became readable: sends=%d", got)
		}
	})
}
