package supervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// TestDonePushIsTheCodeSentence runs the captured "after" screens
// through a finish with no orchestrator attached: the pane tail is read (200
// lines) so the orchestrator would have it, the daemon speaks its own sentence
// and the record keeps that the agent finished — no model phrases
// anything (docs/roles.md "Hot paths that stay code").
func TestDonePushIsTheCodeSentence(t *testing.T) {
	for _, screen := range []string{"codex-after-approval.screen.txt", "claude-after-approval.screen.txt", "codex-after-ctrl-c.screen.txt", "codex-declined-esc.screen.txt"} {
		t.Run(screen, func(t *testing.T) {
			h := newHarness(t, func(c *Config) { c.Chattiness = string(Chatty) })
			h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
			h.srv.SetScreen("w5:p1", fixture(t, screen))
			h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
			push := h.pusher.last(t)
			if push.Text != "auth finished." || push.Urgency != voice.UrgencyIdle {
				t.Errorf("push = %+v", push)
			}
			reads := h.srv.RequestsFor("pane.read")
			if len(reads) != 1 || !strings.Contains(string(reads[0].Params), `"lines":200`) {
				t.Errorf("pane.read = %+v", reads)
			}
			row, _ := h.store.Get("w5:p1")
			if row.LastResult == nil || row.LastResult.Spoken != doneResult || row.State != herdr.StateDone {
				t.Errorf("row = %+v", row)
			}
			if _, ok := h.pending(); ok {
				t.Error("a finish created a pending action")
			}
		})
	}
}

// TestDoneWithoutAScreenStillAnnounces: the pane is gone from herdr, so
// there is nothing to read — the finish is still announced, never
// silently dropped.
func TestDoneWithoutAScreenStillAnnounces(t *testing.T) {
	h := twoAgents(t)
	h.send("w6:p1", "write the readme")
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	h.srv.RemovePane("w6:p1")
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w6:p1", From: herdr.StateWorking, To: herdr.StateDone, At: h.clock.Now()})
	if push := h.pusher.last(t); push.Text != "docs finished." {
		t.Errorf("push after read failure = %+v", push)
	}
	if h.log.count("supervisor.done_error") != 1 {
		t.Error("read failure not logged")
	}
	if row, _ := h.store.Get("w6:p1"); row.LastResult == nil || row.LastResult.Spoken != doneResult {
		t.Errorf("last_result = %+v", row.LastResult)
	}
}

// TestDoneEndingInQuestionBecomesBlock: agy reaches an approval prompt
// but herdr reports done; the screen ends in a prompt, so the finish is
// handled as a block: a pending answer_prompt, the question spoken at
// every level, and confirm answers it.
func TestDoneEndingInQuestionBecomesBlock(t *testing.T) {
	for _, level := range Levels {
		t.Run(string(level), func(t *testing.T) {
			h := newHarness(t, func(c *Config) { c.Chattiness = string(level) })
			h.addAgent("w5:p1", "w5", "agy", herdr.StateWorking)
			h.srv.SetScreen("w5:p1", fixture(t, "agy-approval-prompt.screen.txt"))
			h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
			if push := h.pusher.last(t); push.Text != "auth is asking run this command." {
				t.Errorf("push = %+v", push)
			}
			p, ok := h.pending()
			if !ok || p.Kind != state.PendingAnswerPrompt || p.Question != "run this command" {
				t.Fatalf("pending = %+v ok=%v", p, ok)
			}
			if h.log.count("supervisor.done_question") != 1 {
				t.Errorf("done_question = %d", h.log.count("supervisor.done_question"))
			}
			row, _ := h.store.Get("w5:p1")
			if row.LastResult != nil || row.LastBlock == nil || row.LastBlock.Question != "run this command" {
				t.Errorf("row = %+v", row)
			}
			if res := h.say("yes"); res.Spoken != "Told auth yes." {
				t.Errorf("answer = %+v", res)
			}
			if keys := h.sendKeysRequests(); len(keys) != 1 || keys[0] != [2]string{"w5:p1", "1"} {
				t.Errorf("send_keys = %v", keys)
			}
			// The agent picks the answer up (done -> working): the prompt is gone.
			h.transition("w5:p1", herdr.StateDone, herdr.StateWorking)
			if _, ok := h.pending(); ok {
				t.Error("answer_prompt survived working")
			}
		})
	}
}

// askingSentence is the code sentence for claude-result-then-prompt:
// a finish whose screen ends at Claude Code's own post-turn prompt.
const askingSentence = "auth finished, and it's asking teach auto mode about your environment."

// TestFinishWithATrailingPromptAnnouncesBoth: 2026-09-23 23:56:17 —
// Claude Code finished an investigation and at once showed its own
// "teach auto mode about your environment?" prompt; the finish became a
// block and the findings were never read. Now (any agent but agy) the
// finish is announced with the question in one line, the question is
// the pending answer_prompt, and the finish keeps the promises for done
// (and blocked).
func TestFinishWithATrailingPromptAnnouncesBoth(t *testing.T) {
	for _, level := range Levels {
		t.Run(string(level), func(t *testing.T) {
			h := newHarness(t, func(c *Config) { c.Chattiness = string(level) })
			h.addAgent("w5:p1", "w5", "claude", herdr.StateWorking)
			if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when the investigation reports back", Event: "done"}); err != nil {
				t.Fatal(err)
			}
			h.srv.SetScreen("w5:p1", fixture(t, "claude-result-then-prompt.screen.txt"))
			h.transition("w5:p1", herdr.StateWorking, herdr.StateIdle)
			if got := h.pushTexts(); len(got) != 1 || got[0] != "As you asked: "+askingSentence {
				t.Fatalf("pushes = %v", got)
			}
			p, ok := h.pending()
			if !ok || p.Kind != state.PendingAnswerPrompt || p.Question != "teach auto mode about your environment" || len(p.Options) != 2 {
				t.Fatalf("pending = %+v ok=%v", p, ok)
			}
			if h.log.count("supervisor.done_asking") != 1 || h.log.count("supervisor.done_question") != 0 {
				t.Errorf("done_asking = %d, done_question = %d", h.log.count("supervisor.done_asking"), h.log.count("supervisor.done_question"))
			}
			if kept := h.log.ofType("promise.kept"); len(kept) != 1 {
				t.Errorf("promise.kept = %v", kept)
			}
			row, _ := h.store.Get("w5:p1")
			if row.LastResult == nil || row.LastBlock == nil || row.LastBlock.Question != "teach auto mode about your environment" {
				t.Errorf("row = %+v", row)
			}
		})
	}
	// No promise: a question is heard at every level, quiet included.
	h := newHarness(t, func(c *Config) { c.Chattiness = string(Quiet) })
	h.addAgent("w5:p1", "w5", "claude", herdr.StateWorking)
	h.srv.SetScreen("w5:p1", fixture(t, "claude-result-then-prompt.screen.txt"))
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	if got := h.pushTexts(); len(got) != 1 || got[0] != askingSentence {
		t.Fatalf("quiet: pushes = %v", got)
	}
}

// With the orchestrator attached the one announce turn is told the finish also
// asks, and its line — the finding, then the question as the reply's ask,
// heard last — is the event's one push, tagged as a decision the
// user must take.
func TestFinishWithATrailingPromptBrainLineCoversBoth(t *testing.T) {
	headline := "Auth found only the phone was connected, so the flips were taps. It's also asking whether to teach auto mode about your environment."
	ask := "Yes or not now?"
	line := headline + " " + ask
	m := braintest.NewModel()
	m.EventDefault = withAsk(announceReply(headline, "interrupt", "", "", true), ask)
	h := newBrainHarness(t, m)
	h.addAgent("w5:p1", "w5", "claude", herdr.StateWorking)
	h.srv.SetScreen("w5:p1", fixture(t, "claude-result-then-prompt.screen.txt"))
	h.transition("w5:p1", herdr.StateWorking, herdr.StateIdle)
	got := h.pusher.all()
	if len(got) != 1 || got[0].Text != line || got[0].Urgency != voice.UrgencyInterrupt || h.pusher.tagsOf(0)["needs_user"] != true {
		t.Fatalf("pushes = %+v tags = %v", got, h.pusher.tagsOf(0))
	}
	calls := m.Calls()
	if len(calls) != 1 || !strings.Contains(calls[0].Text(), `also showing a prompt of its own: "teach auto mode about your environment"`) || !strings.Contains(calls[0].Text(), `template="`+askingSentence+`"`) {
		t.Errorf("announce request = %s", calls[0].Text())
	}
	if row, _ := h.store.Get("w5:p1"); row.LastResult == nil || row.LastResult.Spoken != line {
		t.Errorf("last_result = %+v", row.LastResult)
	}
}

// TestFinishEdges: herdr reports a finish as "done" when the user was
// not looking at the pane and "idle" when it was focused
// (herdr/src/pane/state.rs's "seen" bit), so working -> done and
// working -> idle are both a finish, and either keeps whichever "done"
// and "idle" promises are open in the same line; done -> idle ("idle
// again") is never a finish; a finish suppressed by a recent Ctrl-C
// stays silent whichever state it lands on.
func TestFinishEdges(t *testing.T) {
	t.Run("working_to_done_unchanged", func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.Chattiness = string(Chatty) })
		h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
		h.srv.SetScreen("w5:p1", fixture(t, "codex-after-approval.screen.txt"))
		h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
		if push := h.pusher.last(t); push.Text != doneSentence {
			t.Errorf("push = %+v", push)
		}
	})

	t.Run("working_to_idle_announces_and_keeps_both_promises", func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.Chattiness = string(Chatty) })
		h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
		donePromise, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"})
		if err != nil {
			t.Fatal(err)
		}
		idlePromise, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it goes idle", Event: "idle"})
		if err != nil {
			t.Fatal(err)
		}
		h.srv.SetScreen("w5:p1", fixture(t, "codex-after-approval.screen.txt"))
		h.transition("w5:p1", herdr.StateWorking, herdr.StateIdle)
		// One line, not two: the kept promise carries the news.
		if pushes := h.pusher.all(); len(pushes) != 1 || pushes[0].Text != "As you asked: "+doneSentence || pushes[0].Urgency != voice.UrgencyIdle {
			t.Errorf("pushes = %+v", pushes)
		}
		status := map[string]string{}
		for _, p := range h.store.Snapshot().Session.Promises {
			status[p.ID] = p.Status
		}
		if status[donePromise.ID] != state.PromiseKept || status[idlePromise.ID] != state.PromiseKept {
			t.Errorf("statuses = %v", status)
		}
		if h.log.count("promise.kept") != 2 {
			t.Errorf("promise.kept = %d", h.log.count("promise.kept"))
		}
	})

	t.Run("done_to_idle_not_a_finish", func(t *testing.T) {
		h := newHarness(t, func(c *Config) { c.Chattiness = string(Chatty) })
		h.addAgent("w5:p1", "auth", "codex", herdr.StateDone)
		before := len(h.pusher.all())
		h.transition("w5:p1", herdr.StateDone, herdr.StateIdle)
		if len(h.pusher.all()) != before {
			t.Errorf("done -> idle pushed %+v", h.pusher.all()[before:])
		}
	})

	t.Run("working_to_idle_after_ctrl_c_suppressed", func(t *testing.T) {
		h := newHarness(t)
		h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
		h.send("w5:p1", "run the tests")
		if _, err := h.sup.createPromise("w5:p1", brain.PromiseRequest{Text: "tell me when it's done", Event: "done"}); err != nil {
			t.Fatal(err)
		}
		h.say("stop")
		h.srv.SetScreen("w5:p1", fixture(t, "codex-after-ctrl-c.screen.txt"))
		before := len(h.pusher.all())
		h.transition("w5:p1", herdr.StateWorking, herdr.StateIdle)
		if len(h.pusher.all()) != before {
			t.Errorf("suppressed finish pushed %+v", h.pusher.all()[before:])
		}
		if h.log.count("supervisor.finished_suppressed") != 1 || h.log.count("promise.kept") != 0 || len(h.sup.openPromises()) != 1 {
			t.Errorf("suppressed=%d kept=%d open=%d", h.log.count("supervisor.finished_suppressed"), h.log.count("promise.kept"), len(h.sup.openPromises()))
		}
	})
}
