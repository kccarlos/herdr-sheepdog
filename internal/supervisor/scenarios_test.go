package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

func twoAgents(t *testing.T, opts ...func(*Config)) *harness {
	t.Helper()
	h := newHarness(t, opts...)
	h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "w6", "claude", herdr.StateIdle)
	return h
}

// Scenario 1: "what's going on" is answered with one sentence from the
// table — with no brain, the roster sentence itself — and no model call;
// the roster is served from cache in well under 5 ms (every request to
// the brain carries it).
func TestScenario1BriefingNoModelCall(t *testing.T) {
	h := twoAgents(t)
	res := h.say("what's going on")
	want := "Two agents: auth is working on something and docs is idle."
	if res.Spoken != want+" "+noBrainTrailer || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("status = %+v, want %q", res, want)
	}
	if len(h.srv.RequestsFor("pane.read")) != 0 {
		t.Error("the status answer touched herdr")
	}

	// Timed: 200 cached calls, every one under 5 ms.
	var worst time.Duration
	for i := 0; i < 200; i++ {
		start := time.Now()
		if got := h.roster(); got != want {
			t.Fatalf("roster = %q", got)
		}
		if d := time.Since(start); d > worst {
			worst = d
		}
	}
	t.Logf("briefing from cache: worst of 200 calls = %v", worst)
	if worst > 5*time.Millisecond {
		t.Errorf("briefing took %v, want < 5 ms", worst)
	}
}

func TestBriefingCacheInvalidation(t *testing.T) {
	h := twoAgents(t)
	first := h.roster()
	if h.roster() != first {
		t.Error("cache not stable")
	}
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	if got := h.roster(); got != "Two agents: auth is working on something and docs is working on something." {
		t.Errorf("after transition: %q", got)
	}
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	got := h.roster()
	if got != "Two agents: auth is asking would you like to run the following command and docs is working on something. Auth is waiting for your answer." {
		t.Errorf("after block: %q", got)
	}
	h.say("yes")
	if got := h.roster(); strings.Contains(got, "waiting for your answer") {
		t.Errorf("after the answer the pending mention must go: %q", got)
	}
	h.send("w6:p1", "write the readme")
	if got := h.roster(); !strings.Contains(got, `docs is working on "write the readme"`) {
		t.Errorf("after the instruction: %q", got)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateUnknown)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateUnknown)
	if got := h.roster(); got != "Two agents: auth has exited and docs has exited." {
		t.Errorf("after exits: %q", got)
	}
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w5:p1", Closed: true, To: herdr.StateUnknown})
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w6:p1", Closed: true, To: herdr.StateUnknown})
	if got := h.roster(); got != "No agents are running." {
		t.Errorf("after close: %q", got)
	}
}

// Scenario 2: a Codex approval prompt is read, summarised, pushed at idle
// urgency and becomes the pending answer_prompt with the screen's keys.
func TestScenario2BlockedPushesAndCreatesPending(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")

	push := h.pusher.last(t)
	if push.Text != `auth asks to run printf.` || push.Urgency != voice.UrgencyIdle {
		t.Errorf("push = %+v", push)
	}
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingAnswerPrompt || p.PaneID != "w5:p1" || p.Question != "would you like to run the following command" {
		t.Fatalf("pending = %+v ok=%v", p, ok)
	}
	if len(p.Options) != 3 || p.Options[0].Keys[0] != "y" || p.Options[1].Keys[0] != "p" || p.Options[2].Keys[0] != "esc" || p.YesKeys[0] != "y" || p.NoKeys[0] != "esc" {
		t.Errorf("keys = %+v", p)
	}
	if !p.ExpiresAt.IsZero() {
		// It lives while the pane is blocked at this prompt (#8).
		t.Errorf("expires_at = %v, want none", p.ExpiresAt)
	}
	if h.log.count("pending.created") != 1 {
		t.Errorf("pending.created = %d", h.log.count("pending.created"))
	}
	reads := h.srv.RequestsFor("pane.read")
	if len(reads) != 1 || !strings.Contains(string(reads[0].Params), `"recent_unwrapped"`) || !strings.Contains(string(reads[0].Params), `"lines":200`) {
		t.Errorf("pane.read = %+v", reads)
	}
	row, _ := h.store.Get("w5:p1")
	if row.LastBlock == nil || row.LastBlock.Question != "would you like to run the following command" || row.LastBlock.Answered {
		t.Errorf("last_block = %+v", row.LastBlock)
	}
}

// Scenario 3: "yes" sends the prompt's yes keys to that pane and confirms
// (the bare-answer fast path).
func TestScenario3ConfirmSendsKeys(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res := h.say("yes")
	if res.Spoken != "Told auth yes." || res.Clarify {
		t.Errorf("answer = %+v", res)
	}
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "y"} {
		t.Errorf("send_keys = %v", keys)
	}
	if _, ok := h.pending(); ok {
		t.Error("pending not cleared")
	}
	if h.log.count("pending.confirmed") != 1 {
		t.Error("no pending.confirmed event")
	}
	row, _ := h.store.Get("w5:p1")
	if !row.LastBlock.Answered {
		t.Error("last_block not marked answered")
	}
	// A second "yes" finds nothing to answer: no keys, and with no brain
	// the roster is the whole answer.
	if res := h.say("yes"); !strings.HasSuffix(res.Spoken, noBrainTrailer) {
		t.Errorf("second yes = %q", res.Spoken)
	}
	if n := len(h.sendKeysRequests()); n != 1 {
		t.Errorf("second yes sent keys: %d requests", n)
	}
}

// The shepherd's bare answer maps onto the prompt's keys (the fast path:
// yes and no words, an option by number or ordinal, an option's exact
// label). Anything looser ("don't ask again", "accept edits") is the
// brain's to resolve, so with no brain it sends nothing.
func TestConfirmAnswerMapping(t *testing.T) {
	cases := []struct {
		screen, answer, keys, spoken string
	}{
		{"codex-approval-prompt.screen.txt", "yeah", "y", "Told auth yes."},
		{"codex-approval-prompt.screen.txt", "no", "esc", "Told auth no."},
		{"codex-approval-prompt.screen.txt", "option 2", "p", "Told auth option 2."},
		{"codex-approval-prompt.screen.txt", "the second one", "p", "Told auth option 2."},
		{"codex-approval-prompt.screen.txt", "Yes, proceed", "y", "Told auth Yes, proceed."},
		{"claude-permission-prompt.screen.txt", "go ahead", "1", "Told auth yes."},
		{"claude-permission-prompt.screen.txt", "No.", "3", "Told auth no."},
		{"claude-askuserquestion.screen.txt", "go ahead", "1", "Told auth yes."},
		{"claude-askuserquestion.screen.txt", "No.", "2", "Told auth no."},
	}
	for _, c := range cases {
		t.Run(c.screen+"/"+c.answer, func(t *testing.T) {
			h := newHarness(t)
			h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
			h.block("w5:p1", c.screen)
			res := h.say(c.answer)
			if res.Spoken != c.spoken {
				t.Errorf("spoken = %q, want %q", res.Spoken, c.spoken)
			}
			if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0][1] != c.keys {
				t.Errorf("keys = %v, want %q", keys, c.keys)
			}
		})
	}
	for _, answer := range []string{"purple", "don't ask again", "accept edits"} {
		h := newHarness(t)
		h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
		h.block("w5:p1", "codex-approval-prompt.screen.txt")
		h.say(answer)
		if len(h.sendKeysRequests()) != 0 {
			t.Errorf("%q sent keys with no brain", answer)
		}
		if _, ok := h.pending(); !ok {
			t.Errorf("%q dropped the pending question", answer)
		}
	}
}

// Scenario 4: an instruction is echoed and sent immediately (the brain's
// prompt tool and the fallback both come through sendInstruction).
func TestScenario4InstructionSendsAndEchoes(t *testing.T) {
	h := twoAgents(t)
	res := h.send("w5:p1", "skip the flaky test and rerun")
	if res.Spoken != "Telling auth to skip the flaky test and rerun. Sent." || res.Clarify {
		t.Errorf("send = %+v", res)
	}
	if got := h.promptRequests(); len(got) != 1 || got[0] != [2]string{"w5:p1", "skip the flaky test and rerun"} {
		t.Errorf("prompt requests = %v", got)
	}
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingSendInstruction || p.PaneID != "w5:p1" || p.PayloadText != "skip the flaky test and rerun" {
		t.Errorf("pending = %+v", p)
	}
	row, _ := h.store.Get("w5:p1")
	if row.CurrentTask != "skip the flaky test and rerun" {
		t.Errorf("current_task = %q", row.CurrentTask)
	}
	if h.log.count("herdr.send") != 1 || h.log.count("pending.created") != 1 {
		t.Errorf("events: send=%d created=%d", h.log.count("herdr.send"), h.log.count("pending.created"))
	}
	// The turn finishing clears the in-flight action, moves the task to
	// last_task and announces the result read from the screen.
	h.srv.SetScreen("w5:p1", fixture(t, "codex-after-approval.screen.txt"))
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	if _, ok := h.pending(); ok {
		t.Error("send_instruction survived done")
	}
	if got := h.pusher.last(t); got.Text != "auth finished." || got.Urgency != voice.UrgencyIdle {
		t.Errorf("done push = %+v", got)
	}
	row, _ = h.store.Get("w5:p1")
	if row.CurrentTask != "" || row.LastTask != "skip the flaky test and rerun" {
		t.Errorf("after done: current_task=%q last_task=%q", row.CurrentTask, row.LastTask)
	}
	if row.LastResult == nil || row.LastResult.Spoken != doneResult || !row.LastResult.At.Equal(h.clock.Now()) {
		t.Errorf("last_result = %+v", row.LastResult)
	}
}

// agy (Antigravity CLI 1.2.5) prompts, captured in WP7b: the edit prompt
// disables Esc, so "no" must send "2"; the command prompt's "no" is "4".
func TestAgyPromptsAnswerWithDigits(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "agy-edit-prompt.screen.txt")
	if p, ok := h.pending(); !ok || strings.Join(p.YesKeys, ",") != "1" || strings.Join(p.NoKeys, ",") != "2" {
		t.Fatalf("edit prompt pending = %+v", p)
	}
	if res := h.say("no"); res.Spoken != "Told auth no." {
		t.Errorf("no = %+v", res)
	}
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "2"} {
		t.Errorf("send_keys = %v, want exactly one \"2\" (not esc)", keys)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	h.block("w5:p1", "agy-approval-prompt.screen.txt")
	if res := h.say("yes"); res.Spoken != "Told auth yes." {
		t.Errorf("yes = %+v", res)
	}
	if keys := h.sendKeysRequests(); len(keys) != 2 || keys[1] != [2]string{"w5:p1", "1"} {
		t.Errorf("send_keys = %v, want \"1\" for the command prompt", keys)
	}
}

// Scenario 5: "stop" right after an instruction (the stop procedure's
// interrupt phase) sends Ctrl-C to that pane and nothing else.
func TestScenario5CancelInterruptsCorrectPane(t *testing.T) {
	h := twoAgents(t)
	h.send("w6:p1", "write the readme")
	if got := h.promptRequests(); len(got) != 1 || got[0][0] != "w6:p1" {
		t.Fatalf("prompt requests = %v", got)
	}
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	res := h.say("stop")
	if res.Spoken != "Sent an interrupt to docs." || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("stop = %+v", res)
	}
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w6:p1", "ctrl+c"} {
		t.Errorf("send_keys = %v, want exactly one ctrl+c to w6:p1", keys)
	}
	if n := len(h.promptRequests()); n != 1 {
		t.Errorf("prompt requests after cancel = %d", n)
	}
	if _, ok := h.pending(); ok {
		t.Error("pending not cleared")
	}
	if h.log.count("pending.cancelled") != 1 {
		t.Error("no pending.cancelled")
	}
	if res := h.say("stop"); res.Spoken != "Nothing was in flight." {
		t.Errorf("second stop = %q", res.Spoken)
	}
	if n := len(h.sendKeysRequests()); n != 1 {
		t.Errorf("second stop sent keys: %d", n)
	}
}

// After a stop's Ctrl-C the pane settles working -> done; that is not
// "finished" (WP7b: sheepdog announced the cancelled turn as completed).
func TestCancelSuppressesFinishedPush(t *testing.T) {
	h := twoAgents(t)
	h.send("w6:p1", "write the readme")
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	before := len(h.pusher.all())
	h.say("stop")
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	if got := h.pusher.all(); len(got) != before {
		t.Errorf("push after cancelled turn = %+v, want none", got[len(got)-1])
	}
	if h.log.count("supervisor.finished_suppressed") != 1 {
		t.Error("no supervisor.finished_suppressed")
	}
	// The next real completion is announced again.
	h.transition("w6:p1", herdr.StateDone, herdr.StateWorking)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateDone)
	if got := h.pusher.last(t); !strings.HasPrefix(got.Text, "docs finished") {
		t.Errorf("later done push = %+v", got)
	}
}

// The logged case of docs/bench-2026-10-03.md #8: bench-claude's prompt
// was open from 23:55:47 to 00:03:15, but its pending expired at
// 23:57:48 (pending_ttl_s). Now it lives while the pane is blocked at
// the same prompt: minutes later it is still pending, the reconciler
// keeps it, and a tap on its card still answers.
func TestPromptLivesWhileThePaneIsBlocked(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt") // 23:55:47
	p, _ := h.pending()
	for range 7 * 60 / 30 { // to 00:03:15, a reconcile every 30 s
		h.clock.Advance(30 * time.Second)
		h.sup.ExpirePending()
		h.sup.reconcileReprompts(context.Background())
	}
	h.clock.Advance(28 * time.Second)
	h.sup.ExpirePending()
	if got, has := h.pending(); !has || got.ID != p.ID {
		t.Fatalf("pending = %+v, %v; want %s still open", got, has, p.ID)
	}
	if n := h.log.count("pending.expired") + h.log.count("pending.cancelled") + h.log.count("pending.replaced"); n != 0 {
		t.Errorf("expired+cancelled+replaced = %d, want 0", n)
	}
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); err != nil {
		t.Fatalf("tap after 7.5 min: %v", err)
	}
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "y"} {
		t.Errorf("send_keys = %v", keys)
	}
}

// The pending ends with the prompt: when the pane leaves blocked, when
// its screen no longer shows a prompt (the reconciler), and a different
// prompt on screen replaces it. A tap on the old card is then refused.
func TestPromptEndsWhenThePaneLeavesIt(t *testing.T) {
	t.Run("left blocked", func(t *testing.T) {
		h := twoAgents(t)
		h.block("w5:p1", "codex-approval-prompt.screen.txt")
		p, _ := h.pending()
		h.clock.Advance(5 * time.Minute)
		h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
		if _, has := h.pending(); has {
			t.Fatal("pending survived the pane leaving its prompt")
		}
		if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); !errors.Is(err, ErrPendingGone) {
			t.Errorf("tap err = %v, want ErrPendingGone", err)
		}
	})
	t.Run("prompt gone from the screen", func(t *testing.T) {
		h := twoAgents(t)
		h.block("w5:p1", "codex-approval-prompt.screen.txt")
		h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))
		h.clock.Advance(30 * time.Second)
		h.sup.reconcileReprompts(context.Background())
		if _, has := h.pending(); has {
			t.Fatal("pending survived its prompt leaving the screen")
		}
		ev := h.log.ofType("pending.cancelled")
		if len(ev) != 1 || payload(ev[0])["reason"] != "prompt_gone" {
			t.Errorf("pending.cancelled = %v", ev)
		}
		h.say("yes")
		if got := h.sendKeysRequests(); len(got) != 0 {
			t.Errorf("send_keys = %v, want none", got)
		}
	})
	t.Run("a prompt on a pane herdr reports done", func(t *testing.T) {
		// agy reports done around its prompts: the pending lives on while
		// the screen shows the prompt.
		h := newHarness(t)
		h.addAgent("w5:p1", "w5", "agy", herdr.StateWorking)
		h.srv.SetScreen("w5:p1", fixture(t, "agy-approval-prompt.screen.txt"))
		h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
		p, has := h.pending()
		if !has {
			t.Fatal("no pending")
		}
		h.clock.Advance(5 * time.Minute)
		h.sup.reconcileReprompts(context.Background())
		if got, has := h.pending(); !has || got.ID != p.ID {
			t.Errorf("pending = %+v, %v; want %s still open", got, has, p.ID)
		}
	})
	t.Run("a different prompt", func(t *testing.T) {
		h := twoAgents(t)
		h.block("w5:p1", "codex-approval-prompt.screen.txt")
		old, _ := h.pending()
		h.srv.SetScreen("w5:p1", fixture(t, "claude-permission-prompt.screen.txt"))
		h.clock.Advance(30 * time.Second)
		h.sup.reconcileReprompts(context.Background())
		p, has := h.pending()
		if !has || p.ID == old.ID || p.PaneID != "w5:p1" {
			t.Fatalf("pending = %+v, want a new prompt on w5:p1", p)
		}
		if _, err := h.sup.AnswerTap(context.Background(), old.ID, 1); !errors.Is(err, ErrPendingGone) {
			t.Errorf("tap on the old card err = %v, want ErrPendingGone", err)
		}
	})
}

// The bare-answer fast path applies only within fastAnswerWindow of the
// announcement; a "yes" minutes later goes to the brain with the prompt
// and how long it has waited in the roster, and the brain decides (here
// it answers through the rail).
func TestLateBareYesGoesToTheBrain(t *testing.T) {
	m := braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(brain.ToolSendKeys, map[string]any{"agent": "auth", "keys": []any{"y"}})}},
		braintest.Step{Text: braintest.Reply("Told auth yes.", "")},
	)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	calls := len(m.Calls())
	h.clock.Advance(7 * time.Minute)
	res := h.say("yes")
	if res.Spoken != "Told auth yes." {
		t.Errorf("result = %+v", res)
	}
	ev := h.log.ofType("supervisor.fast_path_skipped")
	if len(ev) != 1 || payload(ev[0])["reason"] != "prompt_old" {
		t.Errorf("fast_path_skipped = %v", ev)
	}
	if len(m.Calls()) != calls+2 {
		t.Fatalf("model calls = %d, want the utterance turn's two", len(m.Calls())-calls)
	}
	// The brain renders the age on its own clock (the real one here).
	if req := m.Calls()[calls].System(); !strings.Contains(req, "WAITING FOR AN ANSWER (asked ") {
		t.Errorf("the brain was not told how long the prompt waited:\n%s", req)
	}
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "y"} {
		t.Errorf("send_keys = %v", keys)
	}
}

// pending_ttl_s still expires an instruction's pending action.
func TestRunExpiresViaTicker(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.PendingTTL = 30 * time.Millisecond
		c.ExpiryTick = 5 * time.Millisecond
		c.Now = nil
	})
	h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sup.Run(ctx); close(done) }()
	h.send("w5:p1", "run the tests")
	// Wait for the event, not for the slot: ExpirePending clears the
	// slot a moment before it logs, so polling the slot can see the gap.
	deadline := time.Now().Add(2 * time.Second)
	for h.log.count("pending.expired") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("pending never expired")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := h.pending(); ok {
		t.Error("the expired instruction is still pending")
	}
	cancel()
	<-done
}

func TestReplacementAnnouncement(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.block("w6:p1", "claude-permission-prompt.screen.txt")
	push := h.pusher.last(t)
	want := "Dropping the earlier question from auth; now docs is asking do you want to create claude.txt."
	if push.Text != want {
		t.Errorf("push = %q, want %q", push.Text, want)
	}
	p, _ := h.pending()
	if p.PaneID != "w6:p1" {
		t.Errorf("pending pane = %s", p.PaneID)
	}
	if h.log.count("pending.replaced") != 1 || h.log.count("pending.created") != 2 {
		t.Errorf("replaced=%d created=%d", h.log.count("pending.replaced"), h.log.count("pending.created"))
	}
	// A bare answer goes to the newest question.
	h.say("yes")
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w6:p1", "1"} {
		t.Errorf("send_keys = %v", keys)
	}
}

func TestAgentExitPushesInterrupt(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateUnknown)
	push := h.pusher.last(t)
	if push.Text != "auth has exited while it was busy." || push.Urgency != voice.UrgencyInterrupt {
		t.Errorf("push = %+v", push)
	}
	if _, ok := h.pending(); ok {
		t.Error("pending survived the exit")
	}
	// Idle → unknown is not worth interrupting.
	before := len(h.pusher.all())
	h.transition("w6:p1", herdr.StateIdle, herdr.StateUnknown)
	if len(h.pusher.all()) != before {
		t.Error("idle exit pushed")
	}
}

// The acceptance run of 2026-10-02: Claude Code's AskUserQuestion
// widget (a description line under each option) went blocked, but the
// parser saw no prompt — supervisor.blocked_no_question — so send_keys
// was refused as "has no pending approval prompt now". The blocked
// chain must record the question and the brain's send_keys ["1"] must
// pass. In the widget the number key selects at once, no Enter (checked
// against Claude Code 2.1.287).
func TestAskUserQuestionBlockCreatesPendingAndSendKeysPasses(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "w5", "claude", herdr.StateWorking)
	h.block("w5:p1", "claude-askuserquestion.screen.txt")
	if h.log.count("supervisor.blocked_no_question") != 0 || h.log.count("pending.created") != 1 {
		t.Fatalf("blocked_no_question=%d created=%d", h.log.count("supervisor.blocked_no_question"), h.log.count("pending.created"))
	}
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingAnswerPrompt || p.PaneID != "w5:p1" || len(p.Options) != 3 {
		t.Fatalf("pending = %+v", p)
	}
	if p.Question != "this is a test approval prompt: do you want to proceed" {
		t.Errorf("question = %q", p.Question)
	}
	h.clock.Advance(time.Second)
	h.sup.ShepherdInput(InputTyped, "yes")
	res, err := (brainHost{s: h.sup}).AnswerPrompt(context.Background(), "w5:p1", []string{"1"})
	if err != nil {
		t.Fatalf("AnswerPrompt: %v", err)
	}
	if !strings.HasPrefix(res, "Told auth") {
		t.Errorf("result = %q", res)
	}
	if got := h.sendKeysRequests(); len(got) != 1 || got[0] != [2]string{"w5:p1", "1"} {
		t.Errorf("keys = %v", got)
	}
}

func TestNonQuestionScreensCreateNoPending(t *testing.T) {
	for _, screen := range []string{"codex-after-approval.screen.txt", "codex-after-ctrl-c.screen.txt", "codex-declined-esc.screen.txt"} {
		t.Run(screen, func(t *testing.T) {
			h := newHarness(t)
			h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
			h.block("w5:p1", screen)
			if _, ok := h.pending(); ok {
				t.Error("pending created for a non-question")
			}
			if h.log.count("pending.created") != 0 || h.log.count("supervisor.blocked_no_question") != 1 {
				t.Error("events wrong")
			}
			if push := h.pusher.last(t); push.Urgency != voice.UrgencyIdle || push.Text != "auth is waiting at a prompt." {
				t.Errorf("push = %+v", push)
			}
			h.say("yes")
			if got := h.sendKeysRequests(); len(got) != 0 {
				t.Errorf("yes with no question sent %v", got)
			}
			if got := h.roster(); got != "One agent: auth is blocked at a prompt." {
				t.Errorf("roster = %q", got)
			}
		})
	}
}

// TestBlockedWhileReconnectingStillReachesPusher: a pane blocking while
// the voice session is reconnecting (session.voice_connected false, but
// a session still exists) still hands its push to the voice layer,
// which is trusted to queue it until the connection is back — not
// dropped as supervisor.push_skipped (audit B3: a push_skipped here lost
// a kept promise or a block line for good, since the tool-call promise
// was already kept).
func TestBlockedWhileReconnectingStillReachesPusher(t *testing.T) {
	h := twoAgents(t)
	h.sup.SetVoiceConnected(false)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if len(h.pusher.all()) != 1 {
		t.Fatalf("pushes = %d, want 1 handed to the session to queue while reconnecting", len(h.pusher.all()))
	}
	if !strings.Contains(h.pusher.last(t).Text, "auth asks to run") {
		t.Errorf("push text = %q", h.pusher.last(t).Text)
	}
	if h.log.count("supervisor.push_skipped") != 0 || h.log.count("pending.created") != 1 {
		t.Errorf("push_skipped=%d created=%d", h.log.count("supervisor.push_skipped"), h.log.count("pending.created"))
	}
}

// TestBlockedWithoutASessionOnlyLogs: with no voice session at all
// (--voice=off, pusher nil) a push is skipped and logged, since there is
// no queue anywhere to hold it.
func TestBlockedWithoutASessionOnlyLogs(t *testing.T) {
	h := twoAgents(t)
	h.sup.pusher = nil
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if len(h.pusher.all()) != 0 {
		t.Error("pushed without a voice session")
	}
	if h.log.count("supervisor.push_skipped") != 1 || h.log.count("pending.created") != 1 {
		t.Errorf("push_skipped=%d created=%d", h.log.count("supervisor.push_skipped"), h.log.count("pending.created"))
	}
	if !strings.Contains(h.log.ofType("supervisor.push_skipped")[0].Payload.(map[string]any)["text"].(string), "auth asks to run") {
		t.Error("skipped push text not logged")
	}
}

func TestBlockedReadAndParseErrors(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-working.screen.txt")
	if push := h.pusher.last(t); push.Text != "auth is waiting at a prompt." {
		t.Errorf("unparsed prompt push = %+v", push)
	}
	if _, ok := h.pending(); ok {
		t.Error("pending created for a screen with no options")
	}
	h.srv.RemovePane("w6:p1")
	h.transition("w6:p1", herdr.StateIdle, herdr.StateBlocked)
	if push := h.pusher.last(t); push.Text != "docs is blocked, but I could not read its screen." {
		t.Errorf("read error push = %+v", push)
	}
}

func TestResumePushesSilentBriefing(t *testing.T) {
	h := twoAgents(t)
	h.sup.OnVoiceEvent(voice.EventResumed{Handle: "h", ContextLost: true})
	push := h.pusher.last(t)
	if push.Urgency != voice.UrgencySilent || push.Text != "Two agents: auth is working on something and docs is idle." {
		t.Errorf("push = %+v", push)
	}
	h.sup.OnVoiceEvent(voice.EventStatus{Status: voice.StatusClosed})
	if h.store.Snapshot().Session.VoiceConnected {
		t.Error("closed status left voice_connected")
	}
	h.sup.OnVoiceEvent(voice.EventStatus{Status: voice.StatusConnected})
	if !h.store.Snapshot().Session.VoiceConnected {
		t.Error("connected status not recorded")
	}
}

func TestInstructionWhenHerdrSaysBlocked(t *testing.T) {
	// The pane is blocked in herdr but sheepdog has not seen it yet.
	h := twoAgents(t)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-approval-prompt.screen.txt"))
	h.srv.SetStatus("w5:p1", herdr.StateBlocked)
	res := h.send("w5:p1", "skip the flaky test")
	if !res.Clarify || res.Spoken != `auth asks to run printf. Answer that first.` {
		t.Errorf("send = %+v", res)
	}
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingAnswerPrompt {
		t.Errorf("pending = %+v", p)
	}
	if h.log.count("pending.cancelled") != 1 {
		t.Error("the failed send_instruction should be cancelled")
	}
	h.say("yes")
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0][1] != "y" {
		t.Errorf("send_keys = %v", keys)
	}
}

func TestInstructionSendError(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
	h.srv.RemoveAgent("w5:p1")
	if res := h.send("w5:p1", "stop"); res.Spoken != "I could not send that to auth." {
		t.Errorf("send error = %+v", res)
	}
	if _, ok := h.pending(); ok {
		t.Error("failed send left a pending action")
	}
}

// The brain's note tool (brainHost.Note) writes the note file and the
// session.
func TestNoteWritesFileAndSession(t *testing.T) {
	h := newHarness(t)
	echo, err := brainHost{h.sup}.Note("don't touch the migrations today")
	if err != nil || echo == "" {
		t.Errorf("note = %q, %v", echo, err)
	}
	data, err := os.ReadFile(filepath.Join(h.dir, NotesFile))
	if err != nil {
		t.Fatal(err)
	}
	var n state.Note
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &n); err != nil || n.Text != "don't touch the migrations today" || !n.At.Equal(t0) {
		t.Errorf("notes.jsonl = %q (%v)", data, err)
	}
	if notes := h.store.Snapshot().Session.Notes; len(notes) != 1 || notes[0].Text != n.Text {
		t.Errorf("session notes = %+v", notes)
	}
	if h.log.count("note.created") != 1 {
		t.Error("no note.created event")
	}
}

func TestHandlerRejectsUnknownTool(t *testing.T) {
	h := newHarness(t)
	if _, err := h.sup.Handler()(context.Background(), voice.ToolCall{Name: "reboot"}); err == nil {
		t.Error("unknown tool accepted")
	}
	if _, err := New(nil, h.client, nil, nil, nil, Config{}); err == nil {
		t.Error("nil store accepted")
	}
}

// "Stop" with a question open neither interrupts the pane nor answers or
// drops the question: stop is not "no" (docs/roles.md "Stop").
func TestCancelPendingQuestionDoesNotInterrupt(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res := h.say("stop")
	if res.Spoken != "Nothing to stop; auth is still waiting for your answer, so I left its question open." {
		t.Errorf("stop = %+v", res)
	}
	if len(h.sendKeysRequests()) != 0 {
		t.Error("a stop must not send Ctrl-C (or anything) to the prompt")
	}
	if _, ok := h.pending(); !ok {
		t.Error("stop dropped the open question")
	}
}

func TestKeyboardAnswerDropsPending(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	if _, ok := h.pending(); ok {
		t.Error("pending survived the agent leaving its prompt")
	}
	if ev := h.log.ofType("pending.cancelled"); len(ev) != 1 || ev[0].Payload.(map[string]any)["reason"] != "prompt_gone" {
		t.Errorf("cancelled events = %+v", ev)
	}
}

func TestPersistedPendingSurvivesRestart(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	store2, err := state.Open(h.store.Path(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sup2, err := New(store2, h.client, nil, h.pusher, h.log, Config{Now: h.clock.Now, inputGrace: -1, relayAfter: -1})
	if err != nil {
		t.Fatal(err)
	}
	sup2.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "no", Final: true})
	res, _ := sup2.ToSupervisor(context.Background(), "no")
	if res.Spoken != "Told auth no." {
		t.Errorf("answer after restart = %+v", res)
	}
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0][1] != "esc" {
		t.Errorf("send_keys = %v", keys)
	}
}

// Tools and transitions may run concurrently; this exercises the locking
// under -race.
func TestConcurrentToolsAndTransitions(t *testing.T) {
	h := twoAgents(t)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-approval-prompt.screen.txt"))
	h.srv.SetScreen("w6:p1", fixture(t, "claude-permission-prompt.screen.txt"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sup.Run(ctx); close(done) }()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				switch (i + j) % 6 {
				case 0:
					h.say("what's going on")
				case 1:
					h.say("yes")
				case 2:
					h.say("stop")
				case 3:
					h.send("w5:p1", "continue")
				case 4:
					h.transition("w5:p1", herdr.StateWorking, herdr.StateBlocked)
					h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
				case 5:
					h.transition("w6:p1", herdr.StateIdle, herdr.StateBlocked)
					h.sup.OnVoiceEvent(voice.EventResumed{})
					h.transition("w6:p1", herdr.StateBlocked, herdr.StateDone)
				}
			}
		}(i)
	}
	wg.Wait()
	cancel()
	<-done
}

// "Type something." (bench #2) is not an answer: the phone card, the push
// buttons, the by-number mapping and the label match never offer it, while
// the rail still accepts its key so the brain can type a dictated answer.
func TestAskUserQuestionMetaOptionIsRailOnly(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "w5", "claude", herdr.StateWorking)
	h.block("w5:p1", "claude-askuserquestion.screen.txt")
	p, ok := h.pending()
	if !ok || len(p.Options) != 3 || !p.Options[2].Meta || p.Options[0].Meta {
		t.Fatalf("pending options = %+v", p.Options)
	}
	card := PendingOptions(p)
	if len(card) != 2 || card[0].Label != "Yes, proceed" || card[1].Label != "No, decline" {
		t.Errorf("phone card = %+v", card)
	}
	if !allowedKeys(p)["3"] || !selectedBy(p, []string{"3"}) {
		t.Errorf("the rail must still accept Type something (key 3)")
	}
	if _, _, err := mapAnswer(p, "option 3"); err == nil {
		t.Error("option 3 (Type something.) mapped from words")
	}
	if _, _, err := mapAnswer(p, "type something"); err == nil {
		t.Error("the label of the meta option matched")
	}
	if keys, _, err := mapAnswer(p, "yes"); err != nil || strings.Join(keys, " ") != "1" {
		t.Errorf("yes = %v %v", keys, err)
	}
	if keys, _, err := mapAnswer(p, "no"); err != nil || strings.Join(keys, " ") != "2" {
		t.Errorf("no = %v %v", keys, err)
	}
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 3); err == nil {
		t.Error("a tap on the meta option was accepted")
	}
}
