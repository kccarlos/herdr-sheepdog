package supervisor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// TestDonePushCarriesResult runs the captured "after" screens through a
// finish: the pane tail is read (200 lines), summarize_done is called
// once, the push carries the phrase and last_result is stored.
func TestDonePushCarriesResult(t *testing.T) {
	cases := []struct{ screen, want, outcome string }{
		{"codex-after-approval.screen.txt", "auth finished: created hello.txt with the requested line.", "success"},
		{"claude-after-approval.screen.txt", "auth finished: created claude.txt with the line hello from claude.", "success"},
		{"codex-after-ctrl-c.screen.txt", "auth finished: was interrupted before the sleep finished.", "failed"},
		{"codex-declined-esc.screen.txt", "auth finished: stopped after you declined the append to hello.txt.", "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.screen, func(t *testing.T) {
			h := newHarness(t, func(c *Config) { c.Chattiness = string(Chatty) })
			h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
			h.srv.SetScreen("w5:p1", fixture(t, tc.screen))
			h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
			push := h.pusher.last(t)
			if push.Text != tc.want || push.Urgency != voice.UrgencyIdle {
				t.Errorf("push = %+v, want %q", push, tc.want)
			}
			if h.llm.count("summarize_done") != 1 || h.llm.count("summarize_block") != 0 {
				t.Errorf("calls = %v", h.llm.calls)
			}
			reads := h.srv.RequestsFor("pane.read")
			if len(reads) != 1 || !strings.Contains(string(reads[0].Params), `"lines":200`) {
				t.Errorf("pane.read = %+v", reads)
			}
			row, _ := h.store.Get("w5:p1")
			if row.LastResult == nil || row.LastResult.Outcome != tc.outcome || row.State != herdr.StateDone {
				t.Errorf("row = %+v", row)
			}
			if _, ok := h.pending(); ok {
				t.Error("a finish created a pending action")
			}
		})
	}
}

// TestDoneFallsBackToPlainFinished: a model or read failure still
// announces the finish (never silent) and stores no result.
func TestDoneFallsBackToPlainFinished(t *testing.T) {
	h := twoAgents(t)
	h.call(ToolPropose, map[string]any{"instruction": "tell auth to run the tests"})
	h.llm.failDone = errBoom
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	if push := h.pusher.last(t); push.Text != "auth finished." {
		t.Errorf("push after model failure = %+v", push)
	}
	if h.log.count("supervisor.done_error") != 1 {
		t.Error("no supervisor.done_error")
	}
	if row, _ := h.store.Get("w5:p1"); row.LastResult != nil {
		t.Errorf("last_result stored on failure: %+v", row.LastResult)
	}
	// Read failure (pane gone from herdr).
	h.llm.failDone = nil
	h.transition("w6:p1", herdr.StateIdle, herdr.StateWorking)
	h.call(ToolPropose, map[string]any{"instruction": "tell docs to write the readme"})
	h.srv.RemovePane("w6:p1")
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w6:p1", From: herdr.StateWorking, To: herdr.StateDone, At: h.clock.Now()})
	if push := h.pusher.last(t); push.Text != "docs finished." {
		t.Errorf("push after read failure = %+v", push)
	}
	if h.log.count("supervisor.done_error") != 2 {
		t.Error("read failure not logged")
	}
}

// TestDoneSummaryTimeout: summarize_done is bounded by SummaryTimeout;
// a stuck model degrades to the plain push.
func TestDoneSummaryTimeout(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.SummaryTimeout = 30 * time.Millisecond; c.Chattiness = string(Chatty) })
	h.addAgent("w5:p1", "w5", "codex", herdr.StateWorking)
	h.llm.done = func(string, string) llm.DoneSummary { return llm.DoneSummary{Spoken: "late", Outcome: "success"} }
	// The fake ignores ctx, so simulate the timeout through it: the
	// harness's bump sees no error, so wrap with a ctx-aware fake.
	slow := &slowDone{Templates: h.llm, inner: h.llm, delay: 200 * time.Millisecond}
	h.sup.llm = slow
	start := time.Now()
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Errorf("finish took %v; the summary was not bounded", d)
	}
	if push := h.pusher.last(t); push.Text != "auth finished." {
		t.Errorf("push = %+v", push)
	}
	if !errors.Is(slow.lastErr(), context.DeadlineExceeded) {
		t.Errorf("summary error = %v, want deadline exceeded", slow.lastErr())
	}
}

// TestDoneEndingInQuestionBecomesBlock: agy reaches an approval prompt
// but herdr reports done; summarize_done flags asked_question and the
// finish is handled as a block: summarize_block, pending answer_prompt,
// the question is spoken (at every level) and confirm answers it.
func TestDoneEndingInQuestionBecomesBlock(t *testing.T) {
	for _, level := range Levels {
		t.Run(string(level), func(t *testing.T) {
			h := newHarness(t, func(c *Config) { c.Chattiness = string(level) })
			h.addAgent("w5:p1", "w5", "agy", herdr.StateWorking)
			h.srv.SetScreen("w5:p1", fixture(t, "agy-approval-prompt.screen.txt"))
			h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
			if push := h.pusher.last(t); push.Text != "auth is asking for permission to run tests." {
				t.Errorf("push = %+v", push)
			}
			p, ok := h.pending()
			if !ok || p.Kind != state.PendingAnswerPrompt || p.Question != "whether it can run tests" {
				t.Fatalf("pending = %+v ok=%v", p, ok)
			}
			if h.llm.count("summarize_done") != 1 || h.llm.count("summarize_block") != 1 || h.log.count("supervisor.done_question") != 1 {
				t.Errorf("calls=%v done_question=%d", h.llm.calls, h.log.count("supervisor.done_question"))
			}
			row, _ := h.store.Get("w5:p1")
			if row.LastResult == nil || row.LastResult.Spoken != "stopped to ask for permission" || row.LastBlock == nil || row.LastBlock.Question != "whether it can run tests" {
				t.Errorf("row = %+v", row)
			}
			if res := h.call(ToolConfirm, nil); res.Spoken != "Told auth yes." {
				t.Errorf("confirm = %+v", res)
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

// slowDone delays SummarizeDone until ctx ends or delay elapses.
type slowDone struct {
	llm.Templates
	inner *fakeLLM
	delay time.Duration
	err   error
}

func (s *slowDone) SummarizeDone(ctx context.Context, screen, alias string) (llm.DoneSummary, error) {
	select {
	case <-ctx.Done():
		s.err = ctx.Err()
		return llm.DoneSummary{}, ctx.Err()
	case <-time.After(s.delay):
		return s.inner.SummarizeDone(ctx, screen, alias)
	}
}

func (s *slowDone) lastErr() error { return s.err }

// TestAskTargetsLastSubject: ask with no alias goes to the only agent, else
// to the agent most recently discussed (last push subject or tool target),
// else asks which one.
func TestAskTargetsLastSubject(t *testing.T) {
	h := twoAgents(t)
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))
	h.srv.SetScreen("w6:p1", "nothing useful")
	res := h.call(ToolAsk, map[string]any{"question": "what is it doing"})
	if !res.Clarify || res.Spoken != "Which agent do you mean, auth or docs?" {
		t.Fatalf("no subject yet = %+v", res)
	}
	if h.llm.count("spoken_answer") != 0 {
		t.Fatal("clarify called the model")
	}
	// A propose makes its target the subject.
	h.call(ToolPropose, map[string]any{"instruction": "tell docs to write the readme"})
	if res := h.call(ToolAsk, map[string]any{"question": "what is it doing"}); res.Spoken != "The screen does not say." {
		t.Errorf("after propose to docs = %+v", res)
	}
	// A push about auth (its question) makes auth the subject.
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	h.srv.SetScreen("w5:p1", fixture(t, "codex-working.screen.txt"))
	if res := h.call(ToolAsk, map[string]any{"question": "is it stuck"}); res.Spoken != "auth is running a sleep command and waiting for it." {
		t.Errorf("after a push about auth = %+v", res)
	}
	// A named alias wins and becomes the subject; the subject survives
	// its own finish and is dropped when the pane goes away.
	h.srv.SetScreen("w6:p1", "nothing useful")
	if res := h.call(ToolAsk, map[string]any{"question": "what now", "agent": "docs"}); res.Spoken != "The screen does not say." {
		t.Errorf("named docs = %+v", res)
	}
	if res := h.call(ToolAsk, map[string]any{"question": "and now"}); res.Spoken != "The screen does not say." {
		t.Errorf("subject after naming docs = %+v", res)
	}
	h.sup.HandleTransition(context.Background(), herdr.Transition{PaneID: "w6:p1", From: herdr.StateIdle, To: herdr.StateUnknown, Closed: true, At: h.clock.Now()})
	// One agent left: it is the target whatever the subject was.
	if res := h.call(ToolAsk, map[string]any{"question": "is it stuck"}); res.Spoken != "auth is running a sleep command and waiting for it." {
		t.Errorf("single agent = %+v", res)
	}
}
