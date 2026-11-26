package factory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func hardWrapLineRunes(line string, width int) string {
	r := []rune(line)
	if len(r) <= width || width <= 0 {
		return line
	}
	var b strings.Builder
	for i := 0; i < len(r); i += width {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(string(r[i:min(i+width, len(r))]))
	}
	return b.String()
}

// TestPR2bFix1AgyEscHandledRealCaptureConfirmed pins the real-capture composer
// concatenation bug observed in live check 833-live (raw/esc-handled-screen.jsonl)
// and 829-live-r3 (world/testdata/agy-esc-handled.screen.txt):
//   - On Esc retry into a populated composer, agy submits `<owner draft>Wake: #<seq> ...`
//     on one prompt line and replies normally. The engine must confirm the wake
//     instead of falling back to the earlier stale wake and logging J12 exhaustion.
//   - On an initial wake delivered behind an unsubmitted composer draft, the engine
//     must confirm the wake instead of double-delivering it.
func TestPR2bFix1AgyEscHandledRealCaptureConfirmed(t *testing.T) {
	ctx := context.Background()
	activeScreen := readWorldScreenFixture(t, "agy-esc-active.screen.txt")
	interruptedScreen := readWorldScreenFixture(t, "agy-esc-interrupted.screen.txt")
	handled833 := readWorldScreenFixture(t, "agy-esc-handled-833.screen.txt")

	t.Run("esc_retry_real_833_capture_confirms_without_exhaustion", func(t *testing.T) {
		r, coord := startAgyDeliveredWake(t)
		send1 := lastSendTo(t, r.srv.Requests(), coord)

		// First delivery is interrupted by Esc and retried once.
		agyStepWorking(r, coord, bindAgyCaptureWake(activeScreen, send1))
		if _, err := r.cl.Keys(ctx, coord, "esc"); err != nil {
			t.Fatal(err)
		}
		agyStepIdle(r, coord, "idle", bindAgyCaptureWake(interruptedScreen, send1))
		if got := prClosedWakeSends(t, r.srv.Requests()); got != 2 {
			t.Fatalf("after 1st Esc: pr_closed sends = %d, want 2", got)
		}

		// Retry delivery (#send2) is concatenated by agy onto the queued owner prompt in the
		// composer, exactly as captured in 833-live/raw/esc-handled-screen.jsonl.
		send2 := lastSendTo(t, r.srv.Requests(), coord)
		h1, _, _ := strings.Cut(strings.TrimRight(send1, "\n"), "\n")
		h2, _, _ := strings.Cut(strings.TrimRight(send2, "\n"), "\n")
		boundHandled := strings.Replace(handled833, "> Wake: #25 1 task wake(s) wait on you.", "> "+h1, 1)
		boundHandled = strings.Replace(boundHandled, "Answer in one line.Wake: #27 1 task wake(s) wait on you.", "Answer in one line."+h2, 1)

		agyStepWorking(r, coord, bindAgyCaptureWake(activeScreen, send2))
		agyStepIdle(r, coord, "done", boundHandled)

		st := r.state()
		if len(st.Project.CoordWakesInFlight) != 0 || len(st.Project.Wakes) != 0 {
			t.Fatalf("real handled capture left wakes behind: inFlight=%+v wakes=%+v", st.Project.CoordWakesInFlight, st.Project.Wakes)
		}
		j12 := logRules(t, r.paths.Log(r.id), "J12")
		if len(j12) == 0 || !strings.Contains(j12[len(j12)-1].Decision, "delivered") {
			t.Fatalf("real handled capture agy-esc-handled-833.screen.txt was misclassified as unconfirmed: %+v", j12[len(j12)-1])
		}
	})

	t.Run("initial_wake_behind_composer_draft_confirms_without_double_delivery", func(t *testing.T) {
		r, coord := startAgyDeliveredWake(t)
		send1 := lastSendTo(t, r.srv.Requests(), coord)
		h1, _, _ := strings.Cut(strings.TrimRight(send1, "\n"), "\n")

		// Derive from real agy-esc-handled-833.screen.txt by dropping the earlier #25 block
		// so the composer-concatenated wake is the initial delivery on screen.
		const rule = "\n────────────────────────────────────────────────────────────\n"
		w25Idx := strings.Index(handled833, rule+"> Wake: #25 ")
		w27Idx := strings.Index(handled833, rule+"> What are you doing next? Answer in one line.Wake: #27 ")
		if w25Idx < 0 || w27Idx < 0 || w27Idx <= w25Idx {
			t.Fatalf("agy-esc-handled-833.screen.txt missing expected #25/#27 markers: w25=%d w27=%d", w25Idx, w27Idx)
		}
		initialHandled := handled833[:w25Idx] + handled833[w27Idx:]
		initialHandled = strings.Replace(initialHandled, "Answer in one line.Wake: #27 1 task wake(s) wait on you.", "Answer in one line."+h1, 1)

		agyStepWorking(r, coord, bindAgyCaptureWake(activeScreen, send1))
		agyStepIdle(r, coord, "done", initialHandled)

		if got := prClosedWakeSends(t, r.srv.Requests()); got != 1 {
			t.Fatalf("handled initial wake behind composer draft double-delivered: sends=%d, want 1", got)
		}
		if st := r.state(); len(st.Project.CoordWakesInFlight) != 0 {
			t.Fatalf("handled initial wake behind composer draft left inFlight=%+v", st.Project.CoordWakesInFlight)
		}
	})
}

// TestPR2bFix1NarrowInterruptedWakeRetries pins narrow-pane (44-column)
// interruption handling after partial tool output for both Claude (real capture
// testdata/herdr/claude-bash-prompt-44col.screen.txt) and agy (derived by
// 44-column hard-wrapping real agy-esc-interrupted.screen.txt). Neither may
// falsely confirm an interrupted wake.
func TestPR2bFix1NarrowInterruptedWakeRetries(t *testing.T) {
	t.Run("claude_44col_real_capture", func(t *testing.T) {
		h, coord1 := startPR2bDeliveredWake(t, "Claude 44-col interrupted wake")
		send1 := lastSendTo(t, h.srv.Requests(), coord1)

		claude44 := readHerdrFixture(t, "claude-bash-prompt-44col.screen.txt")
		turnEnd := strings.Index(claude44, "\n❯ Use the Bash tool")
		if turnEnd < 0 {
			t.Fatal("claude-bash-prompt-44col.screen.txt missing second prompt boundary")
		}

		h.setCoordWorking(coord1)
		screen := formatPromptEcho("❯", send1) + "\n" + claude44[:turnEnd] + "\n" +
			"────────────────────────────────────────────\n❯ \n────────────────────────────────────────────\n"
		h.srv.SetScreen(coord1, screen)
		h.coordIdleWithStatus(coord1, "idle")

		st := h.state()
		if got := workerFailedWakeSends(t, h.srv.Requests()); got != 2 {
			t.Fatalf("44-column interrupted Claude wake falsely confirmed instead of retrying: sends=%d, want 2 (inFlight=%d wakes=%d)",
				got, len(st.Project.CoordWakesInFlight), len(st.Project.Wakes))
		}
	})

	t.Run("agy_44col_wrapped_from_real_capture", func(t *testing.T) {
		r, coord := startAgyDeliveredWake(t)
		send1 := lastSendTo(t, r.srv.Requests(), coord)
		activeScreen := readWorldScreenFixture(t, "agy-esc-active.screen.txt")
		interruptedScreen := readWorldScreenFixture(t, "agy-esc-interrupted.screen.txt")

		const realInterrupt = "  ⎿  Interrupted · What should Antigravity CLI do instead?"
		var realToolLine string
		for _, ln := range strings.Split(interruptedScreen, "\n") {
			if strings.HasPrefix(ln, "● Bash(") {
				realToolLine = ln
				break
			}
		}
		if realToolLine == "" || !strings.Contains(interruptedScreen, realInterrupt) {
			t.Fatal("agy-esc-interrupted.screen.txt missing real tool or interrupt line")
		}
		wrappedInterrupted := strings.Replace(
			bindAgyCaptureWake(interruptedScreen, send1),
			realInterrupt,
			hardWrapLineRunes(realToolLine, 44)+"\n"+hardWrapLineRunes(realInterrupt, 44),
			1,
		)

		agyStepWorking(r, coord, bindAgyCaptureWake(activeScreen, send1))
		agyStepIdle(r, coord, "idle", wrappedInterrupted)

		st := r.state()
		if got := prClosedWakeSends(t, r.srv.Requests()); got != 2 {
			t.Fatalf("44-column wrapped interrupted agy wake falsely confirmed instead of retrying: sends=%d, want 2 (inFlight=%d wakes=%d)",
				got, len(st.Project.CoordWakesInFlight), len(st.Project.Wakes))
		}
	})
}

// TestPR2bFix1NewGoalDuringSilentDeliveryTurnDoesNotDeadlock pins the bug where
// Engine.Start calls clearCoordTrack during a Row 1 silent delivery turn
// (Turn <= CoordWakeTurn, CoordWakesInFlight non-empty), zeroing CoordWakeAt
// while leaving CoordWakesInFlight non-empty and deadlocking J9/J12/J13/J4 in J14.
func TestPR2bFix1NewGoalDuringSilentDeliveryTurnDoesNotDeadlock(t *testing.T) {
	h, coord1 := startPR2bDeliveredWake(t, "Goal 1")

	// Coordinator stays idle on the same delivery turn (Row 1: Turn == CoordWakeTurn,
	// CoordWakesInFlight non-empty, CoordWakeAt armed).
	h.coordIdleWithStatus(coord1, "idle")
	pre := h.state()
	if pre.Project.Run.State != flow.PhaseIdle || pre.Project.Run.Turn > pre.Project.CoordWakeTurn || pre.Project.CoordWakeAt.IsZero() {
		t.Fatalf("precondition failed: state=%s turn=%d wakeTurn=%d wakeAt=%v",
			pre.Project.Run.State, pre.Project.Run.Turn, pre.Project.CoordWakeTurn, pre.Project.CoordWakeAt)
	}

	// Owner gives the running project a new goal while coord1 is still silent on the delivery turn.
	h.now = h.now.Add(time.Minute)
	if err := h.eng.Start(h.repo, "Goal 2", 2); err != nil {
		t.Fatalf("Engine.Start(Goal 2): %v", err)
	}

	afterStart := h.state()
	if afterStart.Project.CoordWakeAt.IsZero() {
		t.Errorf("Engine.Start cleared CoordWakeAt while CoordWakesInFlight=%+v is non-empty (J9 watch disarmed; wakes=%+v)",
			afterStart.Project.CoordWakesInFlight, afterStart.Project.Wakes)
	}

	// Advance past coordinator_silent_minutes: J9 must stop silent coord1 rather than deadlocking in J14.
	h.now = h.now.Add(time.Duration(afterStart.Settings.CoordinatorSilentMinutes+5) * time.Minute)
	h.pass()

	st := h.state()
	j9 := logRules(t, h.paths.Log(h.id), "J9")
	if len(j9) != 1 || st.Project.RunID != "" {
		t.Fatalf("project deadlocked after new goal during silent delivery turn: runID=%q inFlight=%+v wakes=%+v wakeAt=%v j9=%d",
			st.Project.RunID, st.Project.CoordWakesInFlight, st.Project.Wakes, st.Project.CoordWakeAt, len(j9))
	}
	if len(st.Project.Wakes) != 2 || st.Project.Wakes[0].Key != "worker_failed" || st.Project.Wakes[1].Key != "new_goal" {
		t.Fatalf("after J9 stop: wakes = %+v, want [worker_failed, new_goal] preserved in order", st.Project.Wakes)
	}
}
