package supervisor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// A send that no utterance owns (a spawn's first instruction, a turn the
// user already replaced) must still end when the user says stop: the
// stop aborts the Prompt in flight instead of waiting out its timeout.
func TestStopAbortsASendNoUtteranceOwns(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	inPrompt := make(chan struct{})
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	base := h.sup.herdr
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onPrompt: func(ctx context.Context, paneID, text string) (herdr.AgentInfo, error) {
			close(inPrompt)
			select {
			case <-ctx.Done():
				return herdr.AgentInfo{}, ctx.Err()
			case <-unblock:
				return herdr.AgentInfo{}, errors.New("timed out")
			}
		},
	}
	row, _ := h.store.Get("w5:p1")
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		_, _ = h.sup.sendInstruction(context.Background(), row, "run the tests", "Telling auth.")
	}()
	select {
	case <-inPrompt:
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt was not called")
	}
	h.clock.Advance(time.Second)

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_, _ = h.sup.Stop(context.Background(), StopTriggerTool)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop waited on the send in flight")
	}
	<-sent
	if got := len(h.srv.RequestsFor("pane.send_keys")); got != 1 {
		t.Errorf("interrupts = %d, want 1: the aborted send may have typed the text", got)
	}
	if p, ok := h.pending(); ok {
		t.Errorf("pending action left behind: %+v", p)
	}
}

// A send is held on its pane's lock the whole time the follow-up screen
// reads run, so the stop would sit behind them: they run unlocked.
func TestSendInstructionReleasesThePaneBeforeItsFollowUpReads(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	base := h.sup.herdr
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onPrompt: func(ctx context.Context, paneID, text string) (herdr.AgentInfo, error) {
			return herdr.AgentInfo{}, herdr.ErrNoReply
		},
		onRead: func(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error) {
			if source == herdr.SourceVisible {
				return base.Read(ctx, paneID, source, lines)
			}
			ps := h.sup.paneSend(paneID)
			got := make(chan struct{})
			go func() {
				ps.Lock()
				close(got)
				ps.Unlock()
			}()
			select {
			case <-got:
			case <-time.After(200 * time.Millisecond):
				t.Error("the pane's send lock is held during the follow-up read")
			}
			return base.Read(ctx, paneID, source, lines)
		},
	}
	h.send("w5:p1", "run the tests")
}

// A transition the send causes can be handled before Prompt returns: the
// row already holds the task, so a quick turn finishes it into last_task.
func TestQuickTurnFinishesTheTaskTheSendRecorded(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	base := h.sup.herdr
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onPrompt: func(ctx context.Context, paneID, text string) (herdr.AgentInfo, error) {
			info, err := base.Prompt(ctx, paneID, text)
			h.transition(paneID, herdr.StateIdle, herdr.StateWorking)
			h.transition(paneID, herdr.StateWorking, herdr.StateDone)
			return info, err
		},
	}
	h.send("w5:p1", "run the tests")
	row, _ := h.store.Get("w5:p1")
	if row.CurrentTask != "" || row.LastTask != "run the tests" {
		t.Errorf("current_task %q last_task %q, want the finished task in last_task only", row.CurrentTask, row.LastTask)
	}
}

// The window's own prompt is recorded the same way.
func TestWindowPromptRecordsTheTaskBeforeTheRPC(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateIdle)
	base := h.sup.herdr
	var during string
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onPrompt: func(ctx context.Context, paneID, text string) (herdr.AgentInfo, error) {
			row, _ := h.store.Get(paneID)
			during = row.CurrentTask
			return base.Prompt(ctx, paneID, text)
		},
	}
	if _, err := h.sup.WindowPrompt(context.Background(), "w5:p1", "fix the build"); err != nil {
		t.Fatal(err)
	}
	if during != "fix the build" {
		t.Errorf("current_task during the RPC = %q, want the prompt", during)
	}
	// A refused prompt leaves no task behind.
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onPrompt: func(ctx context.Context, paneID, text string) (herdr.AgentInfo, error) {
			return herdr.AgentInfo{}, herdr.ErrAgentBlocked
		},
	}
	h.transition("w5:p1", herdr.StateIdle, herdr.StateWorking)
	h.transition("w5:p1", herdr.StateWorking, herdr.StateDone)
	if _, err := h.sup.WindowPrompt(context.Background(), "w5:p1", "second"); err == nil {
		t.Fatal("want the refusal")
	}
	if row, _ := h.store.Get("w5:p1"); row.CurrentTask != "" {
		t.Errorf("current_task = %q after a refused prompt", row.CurrentTask)
	}
}

// The answer is recorded before the keys go out: the transition they
// cause can be handled before SendKeys returns, and it must see the
// block answered (finishedAfterApproval reads that).
func TestAnswerIsRecordedBeforeTheKeysGoOut(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	p, ok := h.pending()
	if !ok || p.Kind != state.PendingAnswerPrompt {
		t.Fatalf("pending = %+v, %v", p, ok)
	}
	base := h.sup.herdr
	var answered bool
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onSendKeys: func(ctx context.Context, paneID string, keys ...string) error {
			row, _ := h.store.Get(paneID)
			answered = row.LastBlock != nil && row.LastBlock.Answered
			return base.SendKeys(ctx, paneID, keys...)
		},
	}
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); err != nil {
		t.Fatal(err)
	}
	if !answered {
		t.Error("the block was not marked answered when the keys went out")
	}
}

// A prompt that comes up while the answer's keys are in flight is its own
// question: the answer's bookkeeping must not mark it answered.
func TestNewPromptDuringAnswerKeepsItsOwnRecord(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	p, _ := h.pending()
	base := h.sup.herdr
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onSendKeys: func(ctx context.Context, paneID string, keys ...string) error {
			err := base.SendKeys(ctx, paneID, keys...)
			h.transition(paneID, herdr.StateBlocked, herdr.StateWorking)
			h.block(paneID, "codex-approval-prompt.screen.txt")
			return err
		},
	}
	if _, err := h.sup.AnswerTap(context.Background(), p.ID, 1); err != nil {
		t.Fatal(err)
	}
	row, _ := h.store.Get("w5:p1")
	if row.LastBlock == nil || row.LastBlock.Answered {
		t.Errorf("the new prompt's block = %+v, want it open", row.LastBlock)
	}
}

// A failed send leaves the block as it was.
func TestFailedAnswerKeysLeaveTheBlockOpen(t *testing.T) {
	h := newHarness(t)
	h.addAgent("w5:p1", "auth", "codex", herdr.StateWorking)
	h.block("w5:p1", "codex-approval-prompt.screen.txt")
	p, _ := h.pending()
	base := h.sup.herdr
	h.sup.herdr = &interceptHerdr{
		Herdr: base,
		onSendKeys: func(ctx context.Context, paneID string, keys ...string) error {
			return errors.New("socket gone")
		},
	}
	_, _ = h.sup.AnswerTap(context.Background(), p.ID, 1)
	row, _ := h.store.Get("w5:p1")
	if row.LastBlock == nil || row.LastBlock.Answered || row.LastBlock.Answer != "" {
		t.Errorf("block = %+v after a failed send, want it open", row.LastBlock)
	}
}
