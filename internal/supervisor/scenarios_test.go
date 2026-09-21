package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// Scenario 1: briefing is one sentence from the table, no model call,
// served from cache in well under 5 ms.
func TestScenario1BriefingNoModelCall(t *testing.T) {
	h := twoAgents(t)
	res := h.call(ToolBriefing, nil)
	want := "Two agents: auth is working on something and docs is idle."
	if res.Spoken != want || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("briefing = %+v, want %q", res, want)
	}
	if len(h.srv.RequestsFor("pane.read")) != 0 {
		t.Error("briefing touched herdr")
	}

	// Timed: 200 cached calls, every one under 5 ms.
	var worst time.Duration
	for i := 0; i < 200; i++ {
		start := time.Now()
		if _, err := h.sup.Briefing(context.Background()); err != nil {
			t.Fatal(err)
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
	first := h.call(ToolBriefing, nil).Spoken
	if h.call(ToolBriefing, nil).Spoken != first {
		t.Error("cache not stable")
	}
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	if got := h.call(ToolBriefing, nil).Spoken; got != "Two agents: auth is working on something and docs is working on something." {
		t.Errorf("after transition: %q", got)
	}
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	got := h.call(ToolBriefing, nil).Spoken
	if got != "Two agents: auth is asking would you like to run the following command and docs is working on something. Auth is waiting for your answer." {
		t.Errorf("after block: %q", got)
	}
	h.call(ToolConfirm, map[string]any{"answer": "yes"})
	if got := h.call(ToolBriefing, nil).Spoken; strings.Contains(got, "waiting for your answer") {
		t.Errorf("after confirm the pending mention must go: %q", got)
	}
	h.send("w6:p1", "write the readme")
	if got := h.call(ToolBriefing, nil).Spoken; !strings.Contains(got, `docs is working on "write the readme"`) {
		t.Errorf("after propose: %q", got)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateUnknown)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateUnknown)
	if got := h.call(ToolBriefing, nil).Spoken; got != "Two agents: auth has exited and docs has exited." {
		t.Errorf("after exits: %q", got)
	}
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w5:p1", Closed: true, To: herdr.StateUnknown})
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w6:p1", Closed: true, To: herdr.StateUnknown})
	if got := h.call(ToolBriefing, nil).Spoken; got != "No agents are running." {
		t.Errorf("after close: %q", got)
	}
}

// Scenario 2: a Codex approval prompt is read, summarised, pushed at idle
// urgency and becomes the pending answer_prompt with the screen's keys.
func TestScenario2BlockedPushesAndCreatesPending(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")

	push := h.pusher.last(t)
	if push.Text != `auth asks to run printf '%s\n' 'hello from codex.' > hello.txt.` || push.Urgency != voice.UrgencyIdle {
		t.Errorf("push = %+v", push)
	}
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingAnswerPrompt || p.PaneID != "w5:p1" || p.Question != "would you like to run the following command" {
		t.Fatalf("pending = %+v ok=%v", p, ok)
	}
	if len(p.Options) != 3 || p.Options[0].Keys[0] != "y" || p.Options[1].Keys[0] != "p" || p.Options[2].Keys[0] != "esc" || p.YesKeys[0] != "y" || p.NoKeys[0] != "esc" {
		t.Errorf("keys = %+v", p)
	}
	if !p.ExpiresAt.Equal(t0.Add(120 * time.Second)) {
		t.Errorf("expires_at = %v", p.ExpiresAt)
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

// Scenario 3: "yes" sends the prompt's yes keys to that pane and confirms.
func TestScenario3ConfirmSendsKeys(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res := h.call(ToolConfirm, map[string]any{"answer": "yes"})
	if res.Spoken != "Told auth yes." || res.Clarify {
		t.Errorf("confirm = %+v", res)
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
	if res := h.call(ToolConfirm, nil); res.Spoken != "Nothing is waiting for an answer." {
		t.Errorf("second confirm = %q", res.Spoken)
	}
}

func TestConfirmAnswerMapping(t *testing.T) {
	cases := []struct {
		screen, answer, keys, spoken string
	}{
		{"codex-approval-prompt.screen.txt", "", "y", "Told auth yes."},
		{"codex-approval-prompt.screen.txt", "no", "esc", "Told auth no."},
		{"codex-approval-prompt.screen.txt", "option 2", "p", "Told auth option 2."},
		{"codex-approval-prompt.screen.txt", "the second one", "p", "Told auth option 2."},
		{"codex-approval-prompt.screen.txt", "don't ask again", "p", "Told auth Yes, and don't ask again for commands that start with `printf '%s\\n' 'hello from codex.' > hello.txt`."},
		{"claude-permission-prompt.screen.txt", "go ahead", "1", "Told auth yes."},
		{"claude-permission-prompt.screen.txt", "No.", "3", "Told auth no."},
		{"claude-permission-prompt.screen.txt", "accept edits", "2", "Told auth Yes, and switch to accept edits (auto-approve file edits and common file commands) for this session (shift+tab)."},
	}
	for _, c := range cases {
		t.Run(c.screen+"/"+c.answer, func(t *testing.T) {
			h := newHarness(t)
			h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
			h.block("w5:p1", c.screen)
			res := h.call(ToolConfirm, map[string]any{"answer": c.answer})
			if res.Spoken != c.spoken {
				t.Errorf("spoken = %q, want %q", res.Spoken, c.spoken)
			}
			if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0][1] != c.keys {
				t.Errorf("keys = %v, want %q", keys, c.keys)
			}
		})
	}
	h := newHarness(t)
	h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res := h.call(ToolConfirm, map[string]any{"answer": "purple"})
	if !res.Clarify || !strings.Contains(res.Spoken, "The options are 1 Yes, proceed, 2 Yes, and don't ask again") {
		t.Errorf("unknown answer = %+v", res)
	}
	if len(h.sendKeysRequests()) != 0 {
		t.Error("unknown answer sent keys")
	}
	if _, ok := h.pending(); !ok {
		t.Error("unknown answer dropped the pending question")
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
	if res := h.call(ToolConfirm, map[string]any{"answer": "no"}); res.Spoken != "Told auth no." {
		t.Errorf("confirm no = %+v", res)
	}
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "2"} {
		t.Errorf("send_keys = %v, want exactly one \"2\" (not esc)", keys)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	h.block("w5:p1", "agy-approval-prompt.screen.txt")
	if res := h.call(ToolConfirm, map[string]any{"answer": "yes"}); res.Spoken != "Told auth yes." {
		t.Errorf("confirm yes = %+v", res)
	}
	if keys := h.sendKeysRequests(); len(keys) != 2 || keys[1] != [2]string{"w5:p1", "1"} {
		t.Errorf("send_keys = %v, want \"1\" for the command prompt", keys)
	}
}

// Scenario 5: cancel after propose sends Ctrl-C to that pane and nothing
// else.
func TestScenario5CancelInterruptsCorrectPane(t *testing.T) {
	h := twoAgents(t)
	h.send("w6:p1", "write the readme")
	if got := h.promptRequests(); len(got) != 1 || got[0][0] != "w6:p1" {
		t.Fatalf("prompt requests = %v", got)
	}
	res := h.call(ToolCancel, nil)
	if res.Spoken != "Cancelled." || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("cancel = %+v", res)
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
	if res := h.call(ToolCancel, nil); res.Spoken != "Nothing to cancel." {
		t.Errorf("second cancel = %q", res.Spoken)
	}
	if n := len(h.sendKeysRequests()); n != 1 {
		t.Errorf("second cancel sent keys: %d", n)
	}
}

// After a cancel's Ctrl-C the pane settles working -> done; that is not
// "finished" (WP7b: sheepdog announced the cancelled turn as completed).
func TestCancelSuppressesFinishedPush(t *testing.T) {
	h := twoAgents(t)
	h.send("w6:p1", "write the readme")
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	before := len(h.pusher.all())
	h.call(ToolCancel, nil)
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

// Scenario 8: the question expires; a later confirm revives it and asks
// before sending; the next confirm sends.
func TestScenario8ExpiryThenConfirm(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.clock.Advance(121 * time.Second)
	h.sup.ExpirePending()
	if _, ok := h.pending(); ok {
		t.Fatal("pending did not expire")
	}
	if h.log.count("pending.expired") != 1 {
		t.Error("no pending.expired event")
	}
	res := h.call(ToolConfirm, map[string]any{"answer": "yes"})
	want := "That question expired; auth was asking would you like to run the following command. Should I answer yes?"
	if res.Spoken != want || !res.Clarify {
		t.Errorf("confirm after expiry = %+v, want %q", res, want)
	}
	if len(h.sendKeysRequests()) != 0 {
		t.Error("expired confirm sent keys")
	}
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingAnswerPrompt || !p.ExpiresAt.Equal(h.clock.Now().Add(120*time.Second)) {
		t.Errorf("revived pending = %+v", p)
	}
	res = h.call(ToolConfirm, map[string]any{"answer": "yes"})
	if res.Spoken != "Told auth yes." {
		t.Errorf("second confirm = %+v", res)
	}
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "y"} {
		t.Errorf("send_keys = %v", keys)
	}
}

func TestExpiredQuestionForgottenWhenAgentMovesOn(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.clock.Advance(121 * time.Second)
	h.sup.ExpirePending()
	// The user answered on the keyboard: the agent left the prompt.
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	if res := h.call(ToolConfirm, nil); res.Spoken != "Nothing is waiting for an answer." {
		t.Errorf("confirm = %q", res.Spoken)
	}
}

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
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
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
		t.Error("the expired question is still pending")
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
	// confirm goes to the newest question.
	h.call(ToolConfirm, nil)
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
			if res := h.call(ToolConfirm, nil); res.Spoken != "Nothing is waiting for an answer." {
				t.Errorf("confirm = %q", res.Spoken)
			}
			if got := h.call(ToolBriefing, nil).Spoken; got != "One agent: auth is blocked at a prompt." {
				t.Errorf("briefing = %q", got)
			}
		})
	}
}

func TestBlockedWithoutVoiceOnlyLogs(t *testing.T) {
	h := twoAgents(t)
	h.sup.SetVoiceConnected(false)
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
	sess := h.store.Snapshot().Session
	if sess.LastBriefing != push.Text || sess.LastBriefingAt == nil {
		t.Errorf("session = %+v", sess)
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
	if !res.Clarify || res.Spoken != `auth asks to run printf '%s\n' 'hello from codex.' > hello.txt. Answer that first.` {
		t.Errorf("send = %+v", res)
	}
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingAnswerPrompt {
		t.Errorf("pending = %+v", p)
	}
	if h.log.count("pending.cancelled") != 1 {
		t.Error("the failed send_instruction should be cancelled")
	}
	h.call(ToolConfirm, nil)
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

func TestNoteWritesFileAndSession(t *testing.T) {
	h := newHarness(t)
	res := h.call(ToolNote, map[string]any{"text": "don't touch the migrations today"})
	if res.Spoken != "Noted." {
		t.Errorf("note = %+v", res)
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
	if res := h.call(ToolNote, map[string]any{"text": " "}); !res.Clarify {
		t.Errorf("empty note = %+v", res)
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

func TestCancelPendingQuestionDoesNotInterrupt(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res := h.call(ToolCancel, nil)
	if res.Spoken != "Cancelled." {
		t.Errorf("cancel = %+v", res)
	}
	if len(h.sendKeysRequests()) != 0 {
		t.Error("cancelling a question must not send Ctrl-C to the prompt")
	}
	if _, ok := h.pending(); ok {
		t.Error("pending not cleared")
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
	sup2, err := New(store2, h.client, nil, h.pusher, h.log, Config{Now: h.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := sup2.Confirm(context.Background(), "no")
	if res.Spoken != "Told auth no." {
		t.Errorf("confirm after restart = %+v", res)
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
					h.call(ToolBriefing, nil)
				case 1:
					h.call(ToolConfirm, map[string]any{"answer": "yes"})
				case 2:
					h.call(ToolCancel, nil)
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
