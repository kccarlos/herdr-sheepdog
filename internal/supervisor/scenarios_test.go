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

func twoAgents(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
	h.addAgent("w6:p1", "w6", "claude", herdr.StateIdle)
	return h
}

// Scenario 1: briefing is one sentence from the table, no model call,
// served from cache in well under 5 ms.
func TestScenario1BriefingNoModelCall(t *testing.T) {
	h := twoAgents(t)
	res := h.call(ToolBriefing, nil)
	want := "Two agents: auth is working and docs is idle."
	if res.Spoken != want || res.Scheduling != voice.UrgencyInterrupt {
		t.Errorf("briefing = %+v, want %q", res, want)
	}
	if n := h.llm.count("summarize_block") + h.llm.count("resolve_instruction") + h.llm.count("spoken_answer"); n != 0 {
		t.Errorf("briefing made %d model calls", n)
	}
	if h.log.count("llm.call") != 0 || len(h.srv.RequestsFor("pane.read")) != 0 {
		t.Error("briefing touched the model or herdr")
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
	if got := h.call(ToolBriefing, nil).Spoken; got != "Two agents: auth is working and docs is working." {
		t.Errorf("after transition: %q", got)
	}
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	got := h.call(ToolBriefing, nil).Spoken
	if got != "Two agents: auth is blocked, asking whether it can create hello.txt and waiting for your answer, and docs is working." {
		t.Errorf("after block: %q", got)
	}
	h.call(ToolConfirm, map[string]any{"answer": "yes"})
	if got := h.call(ToolBriefing, nil).Spoken; strings.Contains(got, "waiting for your answer") {
		t.Errorf("after confirm the pending mention must go: %q", got)
	}
	h.call(ToolPropose, map[string]any{"instruction": "tell docs to write the readme"})
	if got := h.call(ToolBriefing, nil).Spoken; !strings.Contains(got, `docs is working on "write the readme"`) {
		t.Errorf("after propose: %q", got)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateUnknown)
	h.transition("w6:p1", herdr.StateWorking, herdr.StateUnknown)
	if got := h.call(ToolBriefing, nil).Spoken; got != "Two agents: auth is not running and docs is not running." {
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
	if push.Text != "auth is asking whether it can run a command to create hello.txt." || push.Urgency != voice.UrgencyIdle {
		t.Errorf("push = %+v", push)
	}
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingAnswerPrompt || p.PaneID != "w5:p1" || p.Question != "whether it can create hello.txt" {
		t.Fatalf("pending = %+v ok=%v", p, ok)
	}
	if len(p.Options) != 3 || p.Options[0].Keys[0] != "enter" || p.Options[2].Keys[0] != "esc" || p.YesKeys[0] != "enter" || p.NoKeys[0] != "esc" {
		t.Errorf("keys = %+v", p)
	}
	if !p.ExpiresAt.Equal(t0.Add(120 * time.Second)) {
		t.Errorf("expires_at = %v", p.ExpiresAt)
	}
	if h.log.count("pending.created") != 1 || h.llm.count("summarize_block") != 1 {
		t.Errorf("pending.created=%d summarize=%d", h.log.count("pending.created"), h.llm.count("summarize_block"))
	}
	reads := h.srv.RequestsFor("pane.read")
	if len(reads) != 1 || !strings.Contains(string(reads[0].Params), `"recent_unwrapped"`) || !strings.Contains(string(reads[0].Params), `"lines":80`) {
		t.Errorf("pane.read = %+v", reads)
	}
	row, _ := h.store.Get("w5:p1")
	if row.LastBlock == nil || row.LastBlock.Question != "whether it can create hello.txt" || row.LastBlock.Answered {
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
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "enter"} {
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
		{"codex-approval-prompt.screen.txt", "", "enter", "Told auth yes."},
		{"codex-approval-prompt.screen.txt", "no", "esc", "Told auth no."},
		{"codex-approval-prompt.screen.txt", "option 2", "p", "Told auth option 2."},
		{"codex-approval-prompt.screen.txt", "the second one", "p", "Told auth option 2."},
		{"codex-approval-prompt.screen.txt", "don't ask again", "p", "Told auth Yes, and don't ask again."},
		{"claude-permission-prompt.screen.txt", "go ahead", "1", "Told auth yes."},
		{"claude-permission-prompt.screen.txt", "No.", "3", "Told auth no."},
		{"claude-permission-prompt.screen.txt", "accept edits", "2", "Told auth Yes, and switch to accept edits."},
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
	if !res.Clarify || !strings.Contains(res.Spoken, "The options are 1 Yes, proceed, 2 Yes, and don't ask again, 3 No") {
		t.Errorf("unknown answer = %+v", res)
	}
	if len(h.sendKeysRequests()) != 0 {
		t.Error("unknown answer sent keys")
	}
	if _, ok := h.pending(); !ok {
		t.Error("unknown answer dropped the pending question")
	}
}

// Scenario 4: an unambiguous instruction is echoed and sent immediately.
func TestScenario4ProposeSendsAndEchoes(t *testing.T) {
	h := twoAgents(t)
	res := h.call(ToolPropose, map[string]any{"instruction": "tell the auth one to skip the flaky test and rerun", "agent": "auth"})
	if res.Spoken != "Telling auth to skip the flaky test and rerun. Sent." || res.Clarify {
		t.Errorf("propose = %+v", res)
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
	// The turn finishing clears the in-flight action.
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	if _, ok := h.pending(); ok {
		t.Error("send_instruction survived done")
	}
	if got := h.pusher.last(t); got.Text != "auth finished." || got.Urgency != voice.UrgencyIdle {
		t.Errorf("done push = %+v", got)
	}
}

// Scenario 5: cancel after propose sends Ctrl-C to that pane and nothing
// else.
func TestScenario5CancelInterruptsCorrectPane(t *testing.T) {
	h := twoAgents(t)
	h.call(ToolPropose, map[string]any{"instruction": "tell docs to write the readme"})
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

// Scenario 6: ambiguity returns a clarify question and sends nothing.
func TestScenario6AmbiguousSendsNothing(t *testing.T) {
	h := twoAgents(t)
	res := h.call(ToolPropose, map[string]any{"instruction": "tell it to continue"})
	if !res.Clarify || res.Spoken != "Do you mean auth or docs?" {
		t.Errorf("propose = %+v", res)
	}
	if n := len(h.promptRequests()) + len(h.sendKeysRequests()); n != 0 {
		t.Errorf("ambiguous propose sent %d requests", n)
	}
	if _, ok := h.pending(); ok {
		t.Error("ambiguous propose created a pending action")
	}
	if h.log.count("herdr.send") != 0 {
		t.Error("herdr.send logged")
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
	want := "That question expired; auth was asking whether it can create hello.txt. Should I answer yes?"
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
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "enter"} {
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
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := h.pending(); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending never expired")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h.log.count("pending.expired") != 1 {
		t.Errorf("pending.expired = %d", h.log.count("pending.expired"))
	}
	cancel()
	<-done
}

func TestReplacementAnnouncement(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.block("w6:p1", "claude-permission-prompt.screen.txt")
	push := h.pusher.last(t)
	want := "Dropping the earlier question from auth; now docs is asking whether it can create claude.txt."
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
	for _, screen := range []string{"codex-after-approval.screen.txt", "codex-after-ctrl-c.screen.txt", "codex-trust-dialog.screen.txt", "codex-declined-esc.screen.txt"} {
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
			if push := h.pusher.last(t); push.Urgency != voice.UrgencyIdle || !strings.HasPrefix(push.Text, "auth ") {
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
	if !strings.Contains(h.log.ofType("supervisor.push_skipped")[0].Payload.(map[string]any)["text"].(string), "auth is asking") {
		t.Error("skipped push text not logged")
	}
}

func TestBlockedReadAndModelErrors(t *testing.T) {
	h := twoAgents(t)
	h.llm.fail = errBoom
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if push := h.pusher.last(t); push.Text != "auth is waiting at a prompt." {
		t.Errorf("model error push = %+v", push)
	}
	if _, ok := h.pending(); ok {
		t.Error("pending created on model error")
	}
	h.llm.fail = nil
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
	if push.Urgency != voice.UrgencySilent || push.Text != "Two agents: auth is working and docs is idle." {
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

func TestPeriodicBriefingPush(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.BriefingInterval = 10 * time.Millisecond })
	h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sup.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for len(h.pusher.all()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no periodic push")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if p := h.pusher.all()[0]; p.Urgency != voice.UrgencySilent || p.Text != "One agent: auth is working." {
		t.Errorf("push = %+v", p)
	}
}

func TestProposeAnswerUsesSendKeys(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	res := h.call(ToolPropose, map[string]any{"instruction": "yes go ahead"})
	if res.Spoken != "Telling auth yes. Sent." {
		t.Errorf("propose = %+v", res)
	}
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "enter"} {
		t.Errorf("send_keys = %v", keys)
	}
	if len(h.promptRequests()) != 0 {
		t.Error("agent.prompt used for an answer")
	}
	if _, ok := h.pending(); ok {
		t.Error("pending not cleared")
	}
}

func TestProposeWhenHerdrSaysBlocked(t *testing.T) {
	// The pane is blocked in herdr but sheepdog has not seen it yet.
	h := twoAgents(t)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-approval-prompt.screen.txt"))
	h.srv.SetStatus("w5:p1", herdr.StateBlocked)
	res := h.call(ToolPropose, map[string]any{"instruction": "tell auth to skip the flaky test"})
	if !res.Clarify || res.Spoken != "auth is asking whether it can run a command to create hello.txt. Answer that first." {
		t.Errorf("propose = %+v", res)
	}
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingAnswerPrompt {
		t.Errorf("pending = %+v", p)
	}
	if h.log.count("pending.cancelled") != 1 {
		t.Error("the failed send_instruction should be cancelled")
	}
	h.call(ToolConfirm, nil)
	if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0][1] != "enter" {
		t.Errorf("send_keys = %v", keys)
	}
}

func TestProposeErrors(t *testing.T) {
	h := newHarness(t)
	if res := h.call(ToolPropose, map[string]any{"instruction": "tell auth to stop"}); res.Spoken != "No agents are running, so there is nobody to tell." {
		t.Errorf("no agents = %q", res.Spoken)
	}
	h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
	if res := h.call(ToolPropose, map[string]any{"instruction": "  "}); !res.Clarify {
		t.Errorf("empty = %+v", res)
	}
	h.llm.fail = errBoom
	res := h.call(ToolPropose, map[string]any{"instruction": "tell auth to stop"})
	if !res.Clarify || len(h.promptRequests()) != 0 {
		t.Errorf("model error = %+v", res)
	}
	h.llm.fail = nil
	h.srv.RemoveAgent("w5:p1")
	res = h.call(ToolPropose, map[string]any{"instruction": "tell auth to stop"})
	if res.Spoken != "I could not send that to auth." {
		t.Errorf("send error = %+v", res)
	}
	if _, ok := h.pending(); ok {
		t.Error("failed send left a pending action")
	}
}

func TestAskFromScreen(t *testing.T) {
	h := twoAgents(t)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))
	res := h.call(ToolAsk, map[string]any{"question": "is auth stuck", "agent": "auth"})
	if res.Spoken != "auth is running a sleep command and waiting for it." || res.Scheduling != voice.UrgencyIdle {
		t.Errorf("ask = %+v", res)
	}
	if h.llm.count("spoken_answer") != 1 || len(h.srv.RequestsFor("pane.read")) != 1 {
		t.Error("ask should read once and call spoken_answer once")
	}
	// Unknown alias with several agents: clarify.
	res = h.call(ToolAsk, map[string]any{"question": "what now", "agent": "vps"})
	if !res.Clarify || res.Spoken != "I do not know an agent called vps. The agents are auth and docs." {
		t.Errorf("unknown alias = %+v", res)
	}
	// Partial alias resolves.
	res = h.call(ToolAsk, map[string]any{"question": "is it stuck", "agent": "the auth one"})
	if res.Spoken != "auth is running a sleep command and waiting for it." {
		t.Errorf("partial alias = %+v", res)
	}
	// No alias: the most relevant agent is the one with the pending question.
	h.block("w6:p1", "claude-permission-prompt.screen.txt")
	h.srv.SetScreen("w6:p1", "nothing useful")
	res = h.call(ToolAsk, map[string]any{"question": "what is it doing"})
	if res.Spoken != "The screen does not say." {
		t.Errorf("no alias = %+v", res)
	}
	if res := h.call(ToolAsk, map[string]any{"question": ""}); !res.Clarify {
		t.Errorf("empty question = %+v", res)
	}
	h.llm.fail = errBoom
	if res := h.call(ToolAsk, map[string]any{"question": "why", "agent": "auth"}); res.Spoken != "I could not read auth's screen just now." {
		t.Errorf("model error = %+v", res)
	}
}

func TestAskNoAgents(t *testing.T) {
	h := newHarness(t)
	if res := h.call(ToolAsk, map[string]any{"question": "what's up"}); res.Spoken != "No agents are running." {
		t.Errorf("ask = %+v", res)
	}
}

func TestAskRoutesToQuerier(t *testing.T) {
	h := twoAgents(t)
	q := &fakeQuerier{}
	h.sup.querier = q
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))
	// A "why" question goes straight to the querier with the pane's cwd.
	res := h.call(ToolAsk, map[string]any{"question": "why did auth's tests fail", "agent": "auth"})
	if res.Spoken != "The deep tool says: why did auth's tests fail." {
		t.Errorf("deep ask = %+v", res)
	}
	if len(q.calls) != 1 || q.dirs[0] != "/work/w5:p1" || len(h.srv.RequestsFor("pane.read")) != 0 {
		t.Errorf("querier calls=%v dirs=%v reads=%d", q.calls, q.dirs, len(h.srv.RequestsFor("pane.read")))
	}
	// A screen question the model cannot answer falls through to the querier.
	h.srv.SetScreen("w6:p1", "nothing useful")
	res = h.call(ToolAsk, map[string]any{"question": "what is docs doing", "agent": "docs"})
	if res.Spoken != "The deep tool says: what is docs doing." || len(q.calls) != 2 {
		t.Errorf("fallthrough = %+v calls=%v", res, q.calls)
	}
	// Querier failure keeps the screen answer.
	q.err = errBoom
	res = h.call(ToolAsk, map[string]any{"question": "what is docs doing", "agent": "docs"})
	if res.Spoken != "The screen does not say." || h.log.count("supervisor.query_error") != 1 {
		t.Errorf("querier error = %+v", res)
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
	if _, err := New(nil, h.client, h.llm, nil, nil, nil, Config{}); err == nil {
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
	sup2, err := New(store2, h.client, h.llm, nil, h.pusher, h.log, Config{Now: h.clock.Now})
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
					h.call(ToolPropose, map[string]any{"instruction": "tell auth to continue"})
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
