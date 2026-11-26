package factory

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
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

// Pins review-pr2-neon finding 2 and PR 2b (docs/dev/design/app-factory.md §10):
// wake requeue must not loop on normal replies, must bound repeated Esc retries
// to two sends plus a logged unconfirmed outcome, and must not double-deliver
// when a completed wake is followed by an owner follow-up.
func TestCoordinatorWakeRequeueDoesNotLoopOrDoubleDeliver(t *testing.T) {
	t.Run("agy_blockquote_does_not_loop", func(t *testing.T) {
		h, coord1 := startDeliveredWake(t, "Agy blockquote no loop")
		h.edit(func(s *flow.State) { s.Project.Run.CLI = world.AgyCLI })
		send1 := lastSendTo(t, h.srv.Requests(), coord1)
		for range 3 {
			h.setCoordWorking(coord1)
			h.srv.SetScreen(coord1, formatPromptEcho(">", send1)+"\n● Retried T1.\n> Note: T1 retried cleanly.\n"+agyIdleBox)
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
		send1 := lastSendTo(t, h.srv.Requests(), coord1)
		for range 3 {
			h.setCoordWorking(coord1)
			h.srv.SetScreen(coord1, formatPromptEcho("❯", send1)+"\n⏺ Retried T1.\n❯ Try \"check test coverage\"\n")
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
			sent := lastSendTo(t, h.srv.Requests(), coord1)
			h.setCoordWorking(coord1)
			h.srv.SetScreen(coord1, formatPromptEcho("❯", sent)+"\n  ⎿  Interrupted · What should Claude do instead?\n❯ \n")
			h.coordIdleWithStatus(coord1, "idle")
		}
		st := h.state()
		sends := workerFailedWakeSends(t, h.srv.Requests())
		if sends != 2 {
			t.Fatalf("repeated Esc on same coordinator %s wake sends = %d, want exactly 2 (initial + 1 retry)", coord1, sends)
		}
		if len(st.Project.CoordWakesInFlight) != 0 || len(st.Project.Wakes) != 0 || !st.Project.CoordWakeAt.IsZero() {
			t.Fatalf("after repeated Esc exhaustion: inFlight=%d wakes=%d wakeAt=%v, want all clear",
				len(st.Project.CoordWakesInFlight), len(st.Project.Wakes), st.Project.CoordWakeAt)
		}
		j12 := logRules(t, h.paths.Log(h.id), "J12")
		last := j12[len(j12)-1]
		if len(last.Asked) == 0 || last.Decision == "" || last.Why == "" || strings.Contains(last.Decision, "delivered") {
			t.Fatalf("repeated Esc missing J12 unconfirmed exhaustion log: %+v", j12)
		}
	})

	t.Run("handled_wake_followed_by_owner_question_does_not_double_deliver", func(t *testing.T) {
		h, coord1 := startDeliveredWake(t, "Owner followup no double delivery")
		send1 := lastSendTo(t, h.srv.Requests(), coord1)
		h.setCoordWorking(coord1)
		// Wake is confirmed purely by matching echoed wake + reply on screen,
		// even when followed by an owner question before the next engine pass
		// (no CoordWakeActed / coordinator commit required).
		h.srv.SetScreen(coord1, formatPromptEcho("❯", send1)+"\n⏺ Retried T1.\n❯ what did you just do?\n⏺ I retried T1.\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")
		h.now = h.now.Add(time.Minute)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 1 {
			t.Errorf("wake sends = %d after handled wake and owner follow-up, want 1", got)
		}
	})
}

// Pins review-pr2-neon finding 3 and PR 2b (docs/dev/design/app-factory.md §10):
// wake recovery must not silently drop in-flight wakes on Agy Esc, when an owner
// reply scrolls the wake out of the 200-line evidence read (Claude or Codex),
// or when the screen is temporarily unreadable on the idle tick.
func TestCoordinatorWakeSurvivesAgyEscScrolledEvidenceAndUnreadableScreen(t *testing.T) {
	ctx := context.Background()

	t.Run("claude_scrolled_owner_reply_recovers_wake", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Scrolled Claude owner turn")
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
		h.now = h.now.Add(time.Minute)
		h.pass()

		st := h.state()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("scrolled >200-line owner reply wake sends = %d, want exactly 2 (1 retry): inFlight=%d wakes=%d",
				got, len(st.Project.CoordWakesInFlight), len(st.Project.Wakes))
		}
		if st.Project.RunID != coord1 || len(st.Project.CoordWakesInFlight) != 1 || !st.Project.CoordWakesInFlight[0].Retried {
			t.Fatalf("after scrolled Claude retry: runID=%q inFlight=%+v, want same coord with 1 retried wake",
				st.Project.RunID, st.Project.CoordWakesInFlight)
		}
	})

	t.Run("codex_viewport_scrolled_owner_reply_recovers_wake", func(t *testing.T) {
		c := newCodexRoles(t, map[string][]flow.Candidate{
			flow.RoleCoordinator: {{CLI: "codex", Model: codexModel, Effort: "medium"}},
		}, true)
		proxy := newHCReadProxy(t, c.srv.Path, 24)
		c.cl = hc.NewClient(proxy.path)
		c.eng.HC = c.cl

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
		// Owner asks a question whose >200-line reply pushes both `› why T1?` and the wake
		// above both the 24-line viewport and the 200-line scrollback read.
		c.srv.SetScreen(coord1, codexWorkingScreen("why T1?"))
		c.pass()
		var b strings.Builder
		b.WriteString("› why T1?\n\n")
		for i := range 210 {
			fmt.Fprintf(&b, "• Long explanation line %d.\n", i+1)
		}
		b.WriteString("\n  Worked for 12s • 8:44 PM\n\n› Ask Codex to do anything\n\n  gpt-6.2-sol high · 96% left\n")
		c.srv.SetScreen(coord1, b.String())
		c.turnEnds()
		*c.now = c.now.Add(time.Minute)
		c.pass()

		st := c.state()
		if got := workerFailedWakeSends(t, c.srv.Requests()); got != 2 {
			t.Fatalf("codex scrolled owner reply wake sends = %d, want exactly 2 (1 retry): turn=%d wakeTurn=%d inFlight=%d wakes=%d",
				got, st.Project.Run.Turn, st.Project.CoordWakeTurn, len(st.Project.CoordWakesInFlight), len(st.Project.Wakes))
		}
		if st.Project.RunID != coord1 || len(st.Project.CoordWakesInFlight) != 1 || !st.Project.CoordWakesInFlight[0].Retried {
			t.Fatalf("after scrolled Codex retry: runID=%q inFlight=%+v, want same coord with 1 retried wake",
				st.Project.RunID, st.Project.CoordWakesInFlight)
		}
	})

	t.Run("unreadable_screen_does_not_retire_in_flight_wake", func(t *testing.T) {
		h, coord1 := startDeliveredWake(t, "Unreadable screen preserves wake")
		send1 := lastSendTo(t, h.srv.Requests(), coord1)
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
		h.srv.SetScreen(coord1, formatPromptEcho("❯", send1)+"\n  ⎿  Interrupted · What should Claude do instead?\n❯ \n")
		h.now = h.now.Add(time.Minute)
		h.pass()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("wake did not recover once screen became readable: sends=%d, want 2", got)
		}
	})
}
