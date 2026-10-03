package supervisor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// pollOpts shrinks the post-answer poll to a few milliseconds so these
// tests run fast and deterministically: real timers (time.Ticker), not
// the harness's fake clock, drive its cadence (herdr.Watcher's debounce
// timer does the same).
func pollOpts(c *Config) {
	c.RepromptPoll = 30 * time.Millisecond
	c.RepromptPollInterval = 5 * time.Millisecond
}

// reprompts returns the outcome fields of every supervisor.reprompt_poll
// event logged so far.
func (h *harness) lastPollOutcome(t *testing.T) string {
	t.Helper()
	ev := h.log.ofType("supervisor.reprompt_poll")
	if len(ev) == 0 {
		t.Fatal("no supervisor.reprompt_poll logged")
	}
	p, _ := payload(ev[len(ev)-1])["outcome"].(string)
	return p
}

// TestPollRepromptRaisesOnDifferentPrompt: a confirmed prompt is
// immediately followed, on the same (still blocked) pane, by a
// genuinely different one — the poll notices within its window and
// raises a synthetic blocked -> blocked transition.
func TestPollRepromptRaisesOnDifferentPrompt(t *testing.T) {
	h := twoAgents(t, pollOpts)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	first, _ := h.pending()

	h.say("yes")
	// The mock never moves the pane on its own; swap the screen in while
	// it stays "blocked" the way a real worker's next prompt would.
	h.srv.SetScreen("w5:p1", fixture(t, "codex-git-prompt.screen.txt"))

	waitFor(t, func() bool { return h.log.count("supervisor.reprompt") == 1 }, "the reprompt to be raised")
	waitFor(t, func() bool { return h.log.count("supervisor.reprompt_poll") == 1 }, "the poll to end")
	if outcome := h.lastPollOutcome(t); outcome != "raised" {
		t.Errorf("outcome = %q", outcome)
	}
	ev := h.log.ofType("supervisor.reprompt")
	if p := payload(ev[0]); p["source"] != "reprompt" || ev[0].PaneID != "w5:p1" {
		t.Errorf("supervisor.reprompt = %v (pane_id %s)", p, ev[0].PaneID)
	}
	second, ok := h.pending()
	if !ok || second.ID == first.ID || second.PaneID != "w5:p1" {
		t.Fatalf("no new pending created: %+v", second)
	}
	pushes := h.pusher.all()
	if len(pushes) != 2 || pushes[1].Text != "auth asks to run git diff." {
		t.Errorf("pushes = %+v", pushes)
	}
}

// TestPollRepromptSamePromptNotRaised: the screen never changes after the
// answer, so every tick sees the prompt just answered — nothing is
// raised and the poll ends "same_prompt".
func TestPollRepromptSamePromptNotRaised(t *testing.T) {
	h := twoAgents(t, pollOpts)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.say("yes")

	waitFor(t, func() bool { return h.log.count("supervisor.reprompt_poll") == 1 }, "the poll to end")
	if outcome := h.lastPollOutcome(t); outcome != "same_prompt" {
		t.Errorf("outcome = %q", outcome)
	}
	if h.log.count("supervisor.reprompt") != 0 {
		t.Errorf("supervisor.reprompt = %d, want 0", h.log.count("supervisor.reprompt"))
	}
	if _, ok := h.pending(); ok {
		t.Error("an unanswered prompt appeared out of nowhere")
	}
}

// TestPollRepromptStopsWhenLeavesBlocked: the pane picks the answer up
// (blocked -> working) before the poll's window closes; it stops at
// once instead of running to the deadline.
func TestPollRepromptStopsWhenLeavesBlocked(t *testing.T) {
	h := twoAgents(t, pollOpts)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.say("yes")
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)

	waitFor(t, func() bool { return h.log.count("supervisor.reprompt_poll") == 1 }, "the poll to end")
	if outcome := h.lastPollOutcome(t); outcome != "left_blocked" {
		t.Errorf("outcome = %q", outcome)
	}
	ev := h.log.ofType("supervisor.reprompt_poll")
	if p := payload(ev[0]); p["polls"].(int) > 2 {
		t.Errorf("polls = %v, want it to stop almost at once", p["polls"])
	}
}

// TestPollRepromptTimesOut: the pane stays blocked but its screen never
// shows a parseable prompt again — the poll runs out its whole window
// and ends "timeout".
func TestPollRepromptTimesOut(t *testing.T) {
	h := twoAgents(t, pollOpts)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.say("yes")
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))

	waitFor(t, func() bool { return h.log.count("supervisor.reprompt_poll") == 1 }, "the poll to end")
	if outcome := h.lastPollOutcome(t); outcome != "timeout" {
		t.Errorf("outcome = %q", outcome)
	}
	if h.log.count("supervisor.reprompt") != 0 {
		t.Errorf("supervisor.reprompt = %d, want 0", h.log.count("supervisor.reprompt"))
	}
}

// TestReconcileRaisesForAnsweredBlockedWithPrompt: a pane stays blocked,
// nothing is pending and last_block.answered is true (the poll's window
// already closed, or the daemon just restarted) — the reconciler sweep
// finds the new prompt on screen and raises it.
func TestReconcileRaisesForAnsweredBlockedWithPrompt(t *testing.T) {
	h := twoAgents(t) // no poll: isolates the reconciler
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.say("yes")
	h.srv.SetScreen("w5:p1", fixture(t, "codex-git-prompt.screen.txt"))

	h.sup.reconcileReprompts(context.Background())

	ev := h.log.ofType("supervisor.reprompt")
	if len(ev) != 1 || payload(ev[0])["source"] != "reconcile" {
		t.Fatalf("supervisor.reprompt = %v", ev)
	}
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingAnswerPrompt || p.PaneID != "w5:p1" {
		t.Fatalf("pending = %+v ok=%v", p, ok)
	}
	if push := h.pusher.last(t); push.Text != "auth asks to run git diff." {
		t.Errorf("push = %+v", push)
	}
}

// TestReconcileSkipsOpenPending: a pane blocked with a real, still-open
// question (last_block.answered is false) is left alone — the shepherd
// has not answered it yet, so there is nothing new to say.
func TestReconcileSkipsOpenPending(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	before, _ := h.pending()

	h.sup.reconcileReprompts(context.Background())

	if h.log.count("supervisor.reprompt") != 0 {
		t.Errorf("supervisor.reprompt = %d, want 0", h.log.count("supervisor.reprompt"))
	}
	after, ok := h.pending()
	if !ok || after.ID != before.ID {
		t.Errorf("pending changed: before=%+v after=%+v ok=%v", before, after, ok)
	}
}

// TestReconcileNeverRaisesSamePromptTwice: after the reconciler raises a
// prompt and the shepherd answers it, a later sweep over the same
// (unmoved) screen must not raise it again.
func TestReconcileNeverRaisesSamePromptTwice(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.say("yes")
	h.srv.SetScreen("w5:p1", fixture(t, "codex-git-prompt.screen.txt"))

	h.sup.reconcileReprompts(context.Background())
	if n := h.log.count("supervisor.reprompt"); n != 1 {
		t.Fatalf("first sweep: supervisor.reprompt = %d", n)
	}
	h.say("yes") // answers the git prompt too

	h.sup.reconcileReprompts(context.Background()) // the screen never moved on
	if n := h.log.count("supervisor.reprompt"); n != 1 {
		t.Errorf("second sweep raised the same prompt again: supervisor.reprompt = %d", n)
	}
}

// TestPollAndReconcileNeverDoubleRaiseTheSamePrompt races the post-answer
// poll (started by the fast path's answer) against concurrent reconciler
// sweeps on the same pane, both noticing the same new prompt on screen.
// Before the fix, each checked promptSignature against s.lastPrompt,
// released the lock, and only then raised — so both could read the same
// stale signature and both raise it, producing two pending.created
// events and two pushes for one prompt (audit B5). Run with
// `-race -count=20` to also catch the data race the fix's atomic claim
// (claimPrompt) closes.
func TestPollAndReconcileNeverDoubleRaiseTheSamePrompt(t *testing.T) {
	h := twoAgents(t, pollOpts)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.say("yes") // starts the post-answer poll
	h.srv.SetScreen("w5:p1", fixture(t, "codex-git-prompt.screen.txt"))

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h.sup.reconcileReprompts(context.Background())
				}
			}
		}()
	}
	waitFor(t, func() bool { return h.log.count("supervisor.reprompt_poll") == 1 }, "the poll to end")
	close(stop)
	wg.Wait()

	if n := h.log.count("supervisor.reprompt"); n != 1 {
		t.Errorf("supervisor.reprompt logged %d times, want exactly 1", n)
	}
	newPrompts := 0
	for _, p := range h.pusher.all() {
		if p.Text == "auth asks to run git diff." {
			newPrompts++
		}
	}
	if newPrompts != 1 {
		t.Errorf("the new prompt was pushed %d times, want exactly 1", newPrompts)
	}
}

// TestHandleTransitionBlockedToBlocked: a synthetic blocked -> blocked
// transition is handled exactly like working -> blocked (a new pending
// action, one push), without recording a "started on" or a finish.
func TestHandleTransitionBlockedToBlocked(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	first, _ := h.pending()
	row, _ := h.store.Get("w5:p1")
	stateSince := row.StateSince

	h.srv.SetScreen("w5:p1", fixture(t, "codex-git-prompt.screen.txt"))
	h.sup.HandleTransition(context.Background(), herdr.Transition{
		PaneID: "w5:p1", From: herdr.StateBlocked, To: herdr.StateBlocked, At: h.clock.Now(),
		Agent: "codex", WorkspaceID: "w5", WorkspaceLabel: "w5", Source: "reprompt",
	})

	second, ok := h.pending()
	if !ok || second.ID == first.ID {
		t.Fatalf("no new pending created: %+v", second)
	}
	pushes := h.pusher.all()
	if len(pushes) != 2 || pushes[1].Text != "auth asks to run git diff." || pushes[1].Urgency != voice.UrgencyIdle {
		t.Errorf("pushes = %+v", pushes)
	}
	row, _ = h.store.Get("w5:p1")
	if !row.StateSince.Equal(stateSince) {
		t.Errorf("state_since moved: %v -> %v", stateSince, row.StateSince)
	}
	if row.LastTask != "" || row.CurrentTask != "" || row.LastResult != nil {
		t.Errorf("blocked -> blocked recorded a started-on / finish: %+v", row)
	}
}

// TestBriefingNamesNewQuestionAfterReprompt: composeBriefing reads the
// live pending action, so a reprompt's new question is what it names,
// with no cache invalidation of its own to add.
func TestBriefingNamesNewQuestionAfterReprompt(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "agy-approval-prompt.screen.txt")
	h.srv.SetScreen("w5:p1", fixture(t, "agy-edit-prompt.screen.txt"))
	h.sup.HandleTransition(context.Background(), herdr.Transition{
		PaneID: "w5:p1", From: herdr.StateBlocked, To: herdr.StateBlocked, At: h.clock.Now(),
		Agent: "codex", WorkspaceID: "w5", Source: "reprompt",
	})

	text, _ := h.sup.briefingText()
	if !strings.Contains(text, "accept this file edit") {
		t.Errorf("briefing = %q, want it to name the new question", text)
	}
}

// TestFastPathAnswerRepromptsToNewPrompt is the flagship round trip
// (docs/history/issues-2026-09-18.md "blocked -> blocked is a block"): the
// bare-word fast path answers a prompt with keys, the pane stays
// "blocked" exactly as herdr would report it within its own poll window,
// the post-answer poll notices the next prompt and raises it, and the
// bare-word fast path answers that one too.
func TestFastPathAnswerRepromptsToNewPrompt(t *testing.T) {
	h := twoAgents(t, pollOpts)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")

	res := h.say("yes")
	if res.Spoken != "Told auth yes." {
		t.Fatalf("first answer = %+v", res)
	}
	keys := h.sendKeysRequests()
	if len(keys) != 1 || keys[0] != [2]string{"w5:p1", "y"} {
		t.Fatalf("send_keys = %v", keys)
	}

	h.srv.SetScreen("w5:p1", fixture(t, "codex-git-prompt.screen.txt"))
	// pending() flips before the block's push is recorded (announceBlock
	// sets the pending action, then calls announce), so wait for the
	// push itself, not just the pending action.
	waitFor(t, func() bool { return len(h.pusher.all()) == 2 }, "the reprompt's push")
	if h.log.count("supervisor.reprompt") != 1 {
		t.Fatalf("supervisor.reprompt = %d", h.log.count("supervisor.reprompt"))
	}
	if _, ok := h.pending(); !ok {
		t.Fatal("no pending action for the new prompt")
	}

	res2 := h.say("yes")
	if res2.Spoken != "Told auth yes." {
		t.Fatalf("second answer = %+v", res2)
	}
	keys = h.sendKeysRequests()
	if len(keys) != 2 || keys[1] != [2]string{"w5:p1", "y"} {
		t.Fatalf("send_keys after the reprompt = %v", keys)
	}
}
