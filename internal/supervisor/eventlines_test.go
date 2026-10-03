package supervisor

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// announceRequest is the text of the first announce turn's request the
// model saw, "" when none.
func announceRequest(m *braintest.Model) string {
	for _, c := range m.Calls() {
		if text := c.Text(); strings.Contains(text, "Event from the supervisor (announce)") {
			return text
		}
	}
	return ""
}

// 2026-10-03 00:50:48 (docs/bench-2026-10-03.md #15): bench-claude's
// block spoke its template ("bench-claude is asking do you want to
// proceed.") at announce_wait while the brain had not answered, and the
// brain, told "Nothing has been said about this yet", announced the
// same prompt again. Here the announce turn is queued behind a CEO turn,
// so the template goes out before it starts: its request says so, word
// for word, with the prompt's real state.
func TestQueuedAnnounceIsToldTheTemplateWasSpoken(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(braintest.Step{Block: release, Text: braintest.Reply("Docs is idle.", "")})
	m.EventDefault = braintest.NothingToAdd()
	h := newBrainHarness(t, m, replyWait(20*time.Millisecond), func(c *Config) { c.AnnounceWait = 20 * time.Millisecond })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	assertPending(t, h.say("what about docs"))
	waitFor(t, func() bool {
		h.sup.mu.Lock()
		defer h.sup.mu.Unlock()
		return h.sup.utterance != nil && h.sup.utterance.started
	}, "the CEO turn to run")

	done := make(chan struct{})
	go func() {
		h.block("w5:p1", "codex-approval-prompt.screen.txt")
		close(done)
	}()
	waitFor(t, func() bool { return len(h.pusher.all()) > 0 }, "the template line")
	template := h.pusher.last(t).Text
	close(release)
	<-done
	h.sup.replies.Wait()

	req := announceRequest(m)
	if req == "" {
		t.Fatal("no announce turn ran")
	}
	if !strings.Contains(req, "Already said to the CEO about this event, word for word: "+fmt.Sprintf("%q", template)) {
		t.Errorf("the request does not carry the spoken template %q:\n%s", template, req)
	}
	if strings.Contains(req, "Nothing has been said about this yet.") {
		t.Errorf("the request still claims nothing was said:\n%s", req)
	}
	if !strings.Contains(req, "The agent's prompt is still open, waiting for the CEO's answer.") {
		t.Errorf("the request lacks the prompt's state:\n%s", req)
	}
}

// The template goes out while the announce turn is mid-way (a read tool
// step): the next step is told what was said, as an update.
func TestAnnounceMidTurnIsToldTheTemplateWasSpoken(t *testing.T) {
	release := make(chan struct{})
	m := braintest.NewModel(
		braintest.Step{Block: release, Calls: []*genai.FunctionCall{braintest.FC(brain.ToolReadScreen, map[string]any{"agent": "auth"})}},
		braintest.Step{Text: braintest.Reply("Auth wants to run the diff.", "")},
	)
	m.Default = braintest.NothingToAdd() // keep the script for the event turn
	h := newBrainHarness(t, m, func(c *Config) { c.AnnounceWait = 20 * time.Millisecond })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	done := make(chan struct{})
	go func() {
		h.block("w5:p1", "codex-approval-prompt.screen.txt")
		close(done)
	}()
	waitFor(t, func() bool { return len(h.pusher.all()) > 0 }, "the template line")
	template := h.pusher.last(t).Text
	close(release)
	<-done
	calls := m.Calls()
	if len(calls) < 2 {
		t.Fatalf("model calls = %d", len(calls))
	}
	if first := calls[0].Text(); !strings.Contains(first, "Nothing has been said about this yet.") {
		t.Errorf("first step:\n%s", first)
	}
	if second := calls[1].Text(); !strings.Contains(second, "while you were working the assistant said, about this event: "+fmt.Sprintf("%q", template)) {
		t.Errorf("second step lacks the update:\n%s", second)
	}
}

// The prompt's state is a fact, not a guess: displaced from the one slot
// by another pane's question it is still waiting; answered or left, it
// is not.
func TestPromptStateFact(t *testing.T) {
	h := twoAgents(t)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	p, _ := h.pending()
	row, _ := h.store.Get("w5:p1")
	a := announcement{row: row, event: brain.PromiseEventBlocked, pending: p.ID}
	if got := h.sup.promptState(a); got != brain.PromptWaiting {
		t.Errorf("open: %q", got)
	}
	h.block("w6:p1", "claude-permission-prompt.screen.txt") // takes the slot
	if got := h.sup.promptState(a); got != brain.PromptWaiting {
		t.Errorf("displaced but still on screen: %q", got)
	}
	h.transition("w5:p1", herdr.StateBlocked, herdr.StateWorking)
	if got := h.sup.promptState(a); got != brain.PromptGone {
		t.Errorf("left: %q", got)
	}
	// Answered by the shepherd.
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	p2, _ := h.pending()
	if p2.PaneID != "w5:p1" {
		t.Fatalf("pending = %+v", p2)
	}
	a.pending = p2.ID
	h.clock.Advance(time.Second)
	if res := h.say("yes"); res.Spoken != "Told auth yes." {
		t.Fatalf("answer = %+v", res)
	}
	if got := h.sup.promptState(a); got != brain.PromptAnswered {
		t.Errorf("answered: %q", got)
	}
}

// The anti-loop cap: a fourth spoken line about one worker event within
// 30 s is dropped and logged; silent context and other events are not
// counted, and after the window the event may speak again.
func TestFourthLineAboutOneEventIsDropped(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	tags := map[string]any{tagEventID: "evt-1"}
	for i, text := range []string{"auth asks to run the diff.", "Auth wants to run the diff; shall I?", "silent context", "Auth is still waiting.", "Auth asks again."} {
		u := voice.UrgencyIdle
		if i == 2 {
			u = voice.UrgencySilent
		}
		h.sup.pushTagged("w5:p1", text, u, tags)
		h.clock.Advance(2 * time.Second)
	}
	h.sup.pushTagged("w5:p1", "Docs finished.", voice.UrgencyIdle, map[string]any{tagEventID: "evt-2"})
	got := h.pusher.all()
	if len(got) != 5 || got[3].Text != "Auth is still waiting." || got[4].Text != "Docs finished." {
		t.Errorf("pushes = %+v", got)
	}
	ev := h.log.ofType("supervisor.push_loop_dropped")
	if len(ev) != 1 || payload(ev[0])["text"] != "Auth asks again." || payload(ev[0])["event_id"] != "evt-1" {
		t.Errorf("push_loop_dropped = %v", ev)
	}
	h.clock.Advance(30 * time.Second)
	h.sup.pushTagged("w5:p1", "Auth has waited a minute.", voice.UrgencyIdle, tags)
	if last := h.pusher.last(t); last.Text != "Auth has waited a minute." {
		t.Errorf("after the window: last push = %+v", last)
	}
}

// Run C (docs/eval-campaign-2026-10-03.md, 27 of 55 Vertex runs that
// started blocked): the announce turn answered in one slow step, so the
// template went out at announce_wait while that step ran and the "already
// spoken" fact, read only before a later step, never reached it. Its line
// — the same block as news, needs_user — was added late: the CEO heard
// the block twice within 2–6 s. Now the turn is asked once more with the
// fact, and the model's skip is final: one line.
func TestOneStepAnnounceIsToldTheTemplateWentOutBeforeItsLineCounts(t *testing.T) {
	const news = "auth wants to run the diff and needs your approval. Shall I let it?"
	release := make(chan struct{})
	m := braintest.NewModel()
	m.Default = braintest.NothingToAdd()
	var calls int
	m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
		calls++
		if calls == 1 {
			<-release // a Vertex step slower than announce_wait
			return braintest.Response(*announceReply(news, "interrupt", "", "", true)), nil
		}
		if !strings.Contains(call.Text(), "while you were working the assistant said, about this event:") {
			return braintest.Response(*announceReply(news, "interrupt", "", "", true)), nil
		}
		return braintest.Response(*braintest.NothingToAdd()), nil
	}}
	h := newBrainHarness(t, m, func(c *Config) { c.AnnounceWait = 20 * time.Millisecond })
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	pusher := h.pusher
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for len(pusher.all()) < 1 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		close(release)
	}()
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	if got := h.pushTexts(); len(got) != 1 || !strings.HasPrefix(got[0], "auth asks to run") {
		t.Fatalf("pushes = %v, want the template alone", got)
	}
	if calls != 2 {
		t.Errorf("model calls = %d, want 2 (the answer, then the recheck)", calls)
	}
	if n := h.log.count("brain.recheck"); n != 1 {
		t.Errorf("brain.recheck = %d", n)
	}
	late := h.log.ofType("brain.push_late")
	if len(late) != 1 || payload(late[0])["added"] != false {
		t.Errorf("brain.push_late = %v", late)
	}
}

// The model may still add to the template after the recheck (that is
// its call); the anti-loop cap stays the only guard. And a turn that
// answers inside the wait is not asked twice.
func TestAnnounceRecheckLeavesTheJudgementToTheModel(t *testing.T) {
	t.Run("adds after being told", func(t *testing.T) {
		const added = "That diff touches the file you said to keep small."
		release := make(chan struct{})
		m := braintest.NewModel()
		m.Default = braintest.NothingToAdd()
		var calls int
		m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
			calls++
			if calls == 1 {
				<-release
			}
			return braintest.Response(*announceReply(added, "idle", "", "", true)), nil
		}}
		h := newBrainHarness(t, m, func(c *Config) { c.AnnounceWait = 20 * time.Millisecond })
		h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
		pusher := h.pusher
		go func() {
			for len(pusher.all()) < 1 {
				time.Sleep(time.Millisecond)
			}
			close(release)
		}()
		h.block("w5:p1", "codex-approval-prompt.screen.txt")
		if got := h.pushTexts(); len(got) != 2 || got[1] != added || calls != 2 {
			t.Errorf("pushes = %v, calls = %d", got, calls)
		}
	})
	t.Run("answered inside the wait", func(t *testing.T) {
		const line = "auth wants to run the diff. Shall I let it?"
		m := braintest.NewModel()
		m.Default = braintest.NothingToAdd()
		var calls int
		m.EventDefault = &braintest.Step{Fn: func(call braintest.Call) (*genai.GenerateContentResponse, error) {
			calls++
			return braintest.Response(*announceReply(line, "interrupt", "", "", true)), nil
		}}
		h := newBrainHarness(t, m, func(c *Config) { c.AnnounceWait = 5 * time.Second })
		h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
		h.block("w5:p1", "codex-approval-prompt.screen.txt")
		if got := h.pushTexts(); len(got) != 1 || got[0] != line || calls != 1 || h.log.count("brain.recheck") != 0 {
			t.Errorf("pushes = %v, calls = %d", got, calls)
		}
	})
}
