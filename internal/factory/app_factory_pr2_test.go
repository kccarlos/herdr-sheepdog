package factory

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func wakeSendsMatching(t *testing.T, reqs []hctest.Request, needle string) (n int) {
	t.Helper()
	for _, r := range reqs {
		var p struct {
			Text string `json:"text"`
		}
		if r.Method == "terminal.send" && json.Unmarshal(r.Params, &p) == nil && strings.Contains(p.Text, needle) {
			n++
		}
	}
	return n
}

func startDeliveredWake(t *testing.T, goal string) (*projectHarness, string) {
	t.Helper()
	h := newProjectHarness(t, goal)
	h.pass() // J7 starts coord1
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open"}}
		s.Project.Wakes = []flow.Wake{{Key: "worker_failed", Task: "T1", Why: "N23", Facts: "title x", At: h.now}}
	})
	h.coordIdleWithStatus(coord1, "idle") // J12 delivers wake
	if got := wakeSendsMatching(t, h.srv.Requests(), "worker_failed"); got != 1 {
		t.Fatalf("initial wake sends = %d, want 1", got)
	}
	return h, coord1
}

// Pins app-factory.md §10 ("PR 2 is done when") & review-d10-1 change 4:
// wakes in flight are still handled or delivered again after (a) an Esc that
// interrupts a coordinator turn and (b) a turn the owner started by typing.
func TestCoordinatorWakeRecoveryAfterEscAndOwnerTurn(t *testing.T) {
	ctx := context.Background()
	assertRecovered := func(t *testing.T, h *projectHarness, coord1, label string) {
		t.Helper()
		for _, step := range []time.Duration{time.Minute, 15 * time.Minute, 15 * time.Minute, time.Minute, 10 * time.Minute, time.Minute} {
			if wakeSendsMatching(t, h.srv.Requests(), "worker_failed") >= 2 {
				return
			}
			h.now = h.now.Add(step)
			h.pass()
			if cur := h.state().Project.RunID; cur != "" && cur != coord1 {
				h.coordIdleWithStatus(cur, "idle")
			}
		}
		st := h.state()
		t.Fatalf("%s: wake sends = %d (want >= 2); runID=%q turn=%d wakeTurn=%d wakeAt=%v inFlight=%d wakes=%d",
			label, wakeSendsMatching(t, h.srv.Requests(), "worker_failed"), st.Project.RunID, st.Project.Run.Turn,
			st.Project.CoordWakeTurn, st.Project.CoordWakeAt, len(st.Project.CoordWakesInFlight), len(st.Project.Wakes))
	}
	t.Run("esc_interrupts_coordinator_turn", func(t *testing.T) {
		t.Parallel()
		h, coord1 := startDeliveredWake(t, "Esc wake recovery")
		h.setCoordWorking(coord1)
		if _, err := h.client.Keys(ctx, coord1, "esc"); err != nil {
			t.Fatal(err)
		}
		h.srv.SetScreen(coord1, "  Interrupted · What should Claude do instead?\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")
		assertRecovered(t, h, coord1, "after Esc")
	})
	t.Run("owner_typed_turn_while_wake_in_flight", func(t *testing.T) {
		t.Parallel()
		h, coord1 := startDeliveredWake(t, "Owner turn wake recovery")
		for _, d := range []time.Duration{time.Minute, 25 * time.Second} {
			h.now = h.now.Add(d)
			h.pass()
		}
		if pre := h.state(); pre.Project.CoordWakeAt.IsZero() || !slices.ContainsFunc(pre.Project.CoordWakesInFlight, func(w flow.Wake) bool { return w.Key == "worker_failed" }) || pre.Project.Run.Turn != pre.Project.CoordWakeTurn {
			t.Fatalf("precondition: wakeAt=%v inFlight=%+v turn=%d wakeTurn=%d", pre.Project.CoordWakeAt, pre.Project.CoordWakesInFlight, pre.Project.Run.Turn, pre.Project.CoordWakeTurn)
		}
		if _, err := h.client.Send(ctx, coord1, "why did you split T1?", false); err != nil {
			t.Fatal(err)
		}
		h.setCoordWorking(coord1)
		h.srv.SetScreen(coord1, "❯ why did you split T1?\nTwo parts.\n❯ \n")
		h.coordIdleWithStatus(coord1, "idle")
		assertRecovered(t, h, coord1, "after owner turn")
	})
}
