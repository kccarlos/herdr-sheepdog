package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

func prClosedWakeSends(t *testing.T, reqs []hctest.Request) (n int) {
	t.Helper()
	for _, r := range reqs {
		var p struct {
			Text string `json:"text"`
		}
		if r.Method == "terminal.send" && json.Unmarshal(r.Params, &p) == nil && strings.Contains(p.Text, "pr_closed") {
			n++
		}
	}
	return n
}

func readWorldScreenFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("world", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func agyStepIdle(r *agyRig, addr, status, screen string) {
	r.t.Helper()
	r.srv.SetScreen(addr, screen)
	r.srv.SetStatus(addr, &hc.Status{App: "antigravity", State: status})
	*r.now = r.now.Add(5 * time.Second)
	r.pass() // R9/R17 records StatusSince
	*r.now = r.now.Add(25 * time.Second)
	r.pass() // R20 -> PhaseIdle
}

func agyStepWorking(r *agyRig, addr, screen string) {
	r.t.Helper()
	r.srv.SetScreen(addr, screen)
	r.srv.SetStatus(addr, &hc.Status{App: "antigravity", State: "working"})
	*r.now = r.now.Add(5 * time.Second)
	r.pass()
}

// startAgyDeliveredWake boots an agy coordinator, advances it to idle at Turn 2
// (matching the live check state before wake delivery), queues T1:pr_closed,
// and lets J12 deliver the batch once.
func startAgyDeliveredWake(t *testing.T) (*agyRig, string) {
	t.Helper()
	r := newAgyRig(t, map[string][]flow.Candidate{
		flow.RoleCoordinator: {{CLI: world.AgyCLI, Model: agyCoordModel, Effort: "medium"}},
	})
	r.pass() // J7 starts agy coordinator
	coord := r.state().Project.RunID
	if coord == "" {
		t.Fatal("agy coordinator did not start")
	}

	// Complete two prior turns (initial plan + owner question) to reach Turn 2 idle/done,
	// matching live check 829-live-r2 before delivery of T1:pr_closed.
	agyStepWorking(r, coord, "working turn 1")
	agyStepIdle(r, coord, "done", "planned T1")
	agyStepWorking(r, coord, "working turn 2")
	agyStepIdle(r, coord, "done", "answered owner question")

	r.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "chore(nochecks): add live829 check file", Gate: flow.GateOwner}}
		s.Project.Wakes = []flow.Wake{{
			Key:   "pr_closed",
			Task:  "T1",
			Why:   "N4: the PR was closed without merging",
			Facts: `title "chore(nochecks): add live829 check file"; PR #171`,
			At:    *r.now,
		}}
	})
	*r.now = r.now.Add(5 * time.Second)
	r.pass() // J12 delivers T1:pr_closed

	st := r.state()
	if got := prClosedWakeSends(t, r.srv.Requests()); got != 1 {
		t.Fatalf("initial pr_closed wake sends = %d, want 1", got)
	}
	if len(st.Project.CoordWakesInFlight) != 1 || st.Project.CoordWakeTurn != 2 || st.Project.CoordWakeRetried {
		t.Fatalf("post-delivery state = inFlight:%d wakeTurn:%d retried:%v, want 1/2/false",
			len(st.Project.CoordWakesInFlight), st.Project.CoordWakeTurn, st.Project.CoordWakeRetried)
	}
	return r, coord
}

// Pins live check 829-live-r2 check d (and check e's queued owner question) on
// real Antigravity CLI 1.5.18 screen captures:
//  1. After Esc interrupts an active agy wake, the real screen shows
//     "  ⎿  Interrupted · What should Antigravity CLI do instead?". The engine
//     must requeue the in-flight batch once and redeliver it (never silently
//     empty CoordWakesInFlight), and a second Esc on the same coordinator must
//     stay bounded (no second requeue on coord1).
//  2. When an owner question was queued behind the active wake and then
//     submitted and answered after Esc, the interrupted wake must still be
//     requeued and redelivered, and handling the redelivered wake must clear
//     in-flight state without looping.
func TestAgyCoordinatorRealEscWakeRecovery(t *testing.T) {
	ctx := context.Background()
	activeScreen := readWorldScreenFixture(t, "agy-esc-active.screen.txt")
	interruptedScreen := readWorldScreenFixture(t, "agy-esc-interrupted.screen.txt")

	t.Run("real_esc_screen_requeues_once_and_redelivers", func(t *testing.T) {
		r, coord := startAgyDeliveredWake(t)

		// Active wake with owner question queued behind it (esc-confirm-active-screen.jsonl).
		agyStepWorking(r, coord, activeScreen)

		// Esc interrupts the wake; agy shows real esc-verified-screen.jsonl and goes idle (Turn 3).
		if _, err := r.cl.Keys(ctx, coord, "esc"); err != nil {
			t.Fatal(err)
		}
		agyStepIdle(r, coord, "idle", interruptedScreen)

		st := r.state()
		if got := prClosedWakeSends(t, r.srv.Requests()); got != 2 {
			t.Fatalf("after real agy Esc screen: pr_closed sends = %d, want 2 (turn=%d wakeTurn=%d inFlight=%d wakes=%d retried=%v)",
				got, st.Project.Run.Turn, st.Project.CoordWakeTurn, len(st.Project.CoordWakesInFlight), len(st.Project.Wakes), st.Project.CoordWakeRetried)
		}
		if st.Project.RunID != coord || !st.Project.CoordWakeRetried || len(st.Project.CoordWakesInFlight) != 1 || len(st.Project.Wakes) != 0 {
			t.Fatalf("after first requeue+redelivery: runID=%q retried=%v inFlight=%d wakes=%d, want same coord, retried=true, inFlight=1, wakes=0",
				st.Project.RunID, st.Project.CoordWakeRetried, len(st.Project.CoordWakesInFlight), len(st.Project.Wakes))
		}

		// A second Esc on the redelivered wake must not requeue again on the same coordinator;
		// instead the batch stays in flight and arms J9 replacement.
		agyStepWorking(r, coord, activeScreen)
		if _, err := r.cl.Keys(ctx, coord, "esc"); err != nil {
			t.Fatal(err)
		}
		agyStepIdle(r, coord, "idle", interruptedScreen)

		st2 := r.state()
		if got := prClosedWakeSends(t, r.srv.Requests()); got != 2 {
			t.Fatalf("second Esc on same agy coordinator redelivered again: sends = %d, want 2 (requeue once per coordinator)", got)
		}
		if len(st2.Project.CoordWakesInFlight) != 1 || len(st2.Project.Wakes) != 0 || !st2.Project.CoordWakeRetried || st2.Project.CoordWakeAt.IsZero() {
			t.Fatalf("after second Esc: inFlight=%d wakes=%d retried=%v wakeAt=%v, want inFlight=1 wakes=0 retried=true wakeAt armed for J9",
				len(st2.Project.CoordWakesInFlight), len(st2.Project.Wakes), st2.Project.CoordWakeRetried, st2.Project.CoordWakeAt)
		}
	})

	t.Run("owner_question_queued_behind_wake_submitted_after_esc_redelivers", func(t *testing.T) {
		r, coord := startAgyDeliveredWake(t)

		// Active wake with owner question queued behind it, then Esc interrupts the wake.
		agyStepWorking(r, coord, activeScreen)
		if _, err := r.cl.Send(ctx, coord, "What are you doing next? Answer in one line.", false); err != nil {
			t.Fatal(err)
		}
		if _, err := r.cl.Keys(ctx, coord, "esc"); err != nil {
			t.Fatal(err)
		}

		// The queued owner question that Esc placed in the composer is submitted and answered
		// before the idle grace period finishes; the transcript still carries the interrupted
		// wake above the submitted owner prompt and its one-line answer.
		boxStart := strings.Index(interruptedScreen, "\n────────────────────────────────────────────────────────────────────────────────")
		if boxStart < 0 {
			t.Fatal("real fixture missing bottom input box rule")
		}
		submittedAfterEsc := interruptedScreen[:boxStart] + "\n\n" +
			"────────────────────────────────────────────────────────────\n" +
			"> What are you doing next? Answer in one line.\n\n" +
			"  Waiting for your go-ahead before retrying T1 after PR #171 was closed.\n" +
			strings.Replace(interruptedScreen[boxStart+1:], "> What are you doing next? Answer in one line.", ">", 1)

		agyStepIdle(r, coord, "done", submittedAfterEsc)

		st := r.state()
		if got := prClosedWakeSends(t, r.srv.Requests()); got != 2 {
			t.Fatalf("after owner question submitted post-Esc: pr_closed sends = %d, want 2 (turn=%d wakeTurn=%d inFlight=%d wakes=%d retried=%v)",
				got, st.Project.Run.Turn, st.Project.CoordWakeTurn, len(st.Project.CoordWakesInFlight), len(st.Project.Wakes), st.Project.CoordWakeRetried)
		}
		if !st.Project.CoordWakeRetried || len(st.Project.CoordWakesInFlight) != 1 {
			t.Fatalf("after post-Esc owner turn redelivery: retried=%v inFlight=%d, want true and 1",
				st.Project.CoordWakeRetried, len(st.Project.CoordWakesInFlight))
		}

		// Once the coordinator handles the redelivered wake normally, the newer wake prompt
		// supersedes the older interruption and does not loop.
		handledRedelivery := interruptedScreen[:boxStart] + "\n\n" +
			"────────────────────────────────────────────────────────────\n" +
			"> Wake: 1 task wake(s) wait on you.\n" +
			"  - T1: N4: the PR was closed without merging.\n\n" +
			"  Retried T1 after reviewing the closed PR.\n" +
			strings.Replace(interruptedScreen[boxStart+1:], "> What are you doing next? Answer in one line.", ">", 1)
		agyStepWorking(r, coord, activeScreen)
		agyStepIdle(r, coord, "done", handledRedelivery)
		*r.now = r.now.Add(time.Minute)
		r.pass()

		stDone := r.state()
		if got := prClosedWakeSends(t, r.srv.Requests()); got != 2 {
			t.Fatalf("handled redelivery looped: pr_closed sends = %d, want 2", got)
		}
		if len(stDone.Project.CoordWakesInFlight) != 0 || len(stDone.Project.Wakes) != 0 {
			t.Fatalf("handled redelivery left wakes behind: inFlight=%d wakes=%d",
				len(stDone.Project.CoordWakesInFlight), len(stDone.Project.Wakes))
		}
	})
}
