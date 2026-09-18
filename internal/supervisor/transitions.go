package supervisor

import (
	"context"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// HandleTransition folds one debounced herdr transition into the state
// table and reacts to it (plan §3.6). It blocks for the pane read and the
// model call on `blocked`, so callers that must not stall run it in a
// goroutine per transition.
func (s *Supervisor) HandleTransition(ctx context.Context, t herdr.Transition) {
	prev, _ := s.store.Get(t.PaneID)
	row, _, err := s.store.Apply(t)
	if err != nil {
		s.log.Log(logSource, "supervisor.state_error", t.PaneID, map[string]any{"error": err.Error()})
	}
	s.invalidateBriefing()

	from := t.From
	if from == "" {
		from = prev.State
	}
	wasBusy := from == herdr.StateWorking || from == herdr.StateBlocked

	if t.Closed {
		s.mu.Lock()
		s.dropPendingFor(t.PaneID, "pane_closed")
		s.forgetExpired(t.PaneID)
		s.mu.Unlock()
		if wasBusy {
			s.push(t.PaneID, aliasOr(prev.Alias)+" has gone away; its pane closed.", voice.UrgencyInterrupt)
		}
		return
	}

	alias := aliasOr(row.Alias)
	switch t.To {
	case herdr.StateBlocked:
		s.onBlocked(ctx, row)
	case herdr.StateUnknown:
		s.mu.Lock()
		s.dropPendingFor(t.PaneID, "agent_exited")
		s.forgetExpired(t.PaneID)
		s.mu.Unlock()
		if wasBusy {
			s.push(t.PaneID, alias+" has exited while it was busy.", voice.UrgencyInterrupt)
		}
	case herdr.StateWorking:
		s.mu.Lock()
		// Whatever the previous state, a working pane is no longer at a
		// prompt (agy reports done, not blocked, around its prompts).
		s.dropPendingFor(t.PaneID, "prompt_gone", state.PendingAnswerPrompt)
		s.forgetExpired(t.PaneID)
		started := s.startedInstruction(t.PaneID, from)
		s.mu.Unlock()
		if started != "" {
			s.pushIf(pushStarted, t.PaneID, alias+" started on "+started+".", voice.UrgencyIdle)
		}
	case herdr.StateDone, herdr.StateIdle:
		s.mu.Lock()
		if from == herdr.StateBlocked {
			s.dropPendingFor(t.PaneID, "prompt_gone", state.PendingAnswerPrompt)
			s.forgetExpired(t.PaneID)
		}
		s.dropPendingFor(t.PaneID, "finished", state.PendingSendInstruction)
		s.mu.Unlock()
		s.finishTask(t.PaneID)
		if from == herdr.StateWorking && t.To == herdr.StateDone {
			if s.recentlyInterrupted(t.PaneID) {
				// The user's cancel sent Ctrl-C: this "done" is the
				// interrupted turn settling, not work finishing.
				s.log.Log(logSource, "supervisor.finished_suppressed", t.PaneID, map[string]any{"reason": "interrupted"})
			} else {
				s.onDone(ctx, row)
			}
		}
		// done -> idle ("idle again") is never announced.
	}
}

// startedInstruction returns the spoken form of the instruction the user
// sent to paneID when its move to working means the agent picked it up
// (a send_instruction is pending for it and it did not just leave a
// prompt), else "". Caller holds s.mu.
func (s *Supervisor) startedInstruction(paneID string, from herdr.State) string {
	if from == herdr.StateBlocked {
		return ""
	}
	p, has := s.store.Pending()
	if !has || p.Kind != state.PendingSendInstruction || p.PaneID != paneID {
		return ""
	}
	if t := shortTask(p.PayloadText); t != "" {
		return t
	}
	return "the instruction"
}

// finishTask moves the row's current task to last_task.
func (s *Supervisor) finishTask(paneID string) {
	_, err := s.store.Update(paneID, func(a *state.Agent) {
		if a.CurrentTask != "" {
			a.LastTask, a.CurrentTask = a.CurrentTask, ""
		}
	})
	if err != nil {
		s.log.Log(logSource, "supervisor.state_error", paneID, map[string]any{"error": err.Error()})
	}
	s.invalidateBriefing()
}

// onDone announces a finished agent when the chattiness level allows it
// for this agent (every finish at chatty; at normal only agents the user
// dealt with within RecentWindow; never at quiet).
func (s *Supervisor) onDone(_ context.Context, row state.Agent) {
	s.pushIf(s.doneKind(row), row.PaneID, aliasOr(row.Alias)+" finished.", voice.UrgencyIdle)
}

// onBlocked reads the pane, summarises the prompt, records the question
// and — when the agent is really asking — creates the pending action and
// announces it.
func (s *Supervisor) onBlocked(ctx context.Context, row state.Agent) {
	alias := aliasOr(row.Alias)
	s.touch(row.PaneID)
	read, err := s.herdr.Read(ctx, row.PaneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines)
	if err != nil {
		s.log.Log(logSource, "supervisor.block_error", row.PaneID, map[string]any{"stage": "read", "error": err.Error()})
		// It may well be a question: every level hears this.
		s.push(row.PaneID, alias+" is blocked, but I could not read its screen.", voice.UrgencyIdle)
		return
	}
	sum, err := s.llm.SummarizeBlock(ctx, read.Text, alias)
	if err != nil {
		s.log.Log(logSource, "supervisor.block_error", row.PaneID, map[string]any{"stage": "summarize", "error": err.Error()})
		s.push(row.PaneID, alias+" is waiting at a prompt.", voice.UrgencyIdle)
		return
	}
	s.recordBlock(row.PaneID, sum)
	if !sum.IsQuestion {
		s.log.Log(logSource, "supervisor.blocked_no_question", row.PaneID, map[string]any{"spoken": sum.Spoken})
		s.pushIf(pushBlockedInfo, row.PaneID, sum.Spoken, voice.UrgencyIdle)
		return
	}
	s.mu.Lock()
	spoken := s.setPending(s.newAnswerPrompt(row.PaneID, sum))
	s.mu.Unlock()
	s.push(row.PaneID, spoken, voice.UrgencyIdle)
}

// recentlyInterrupted reports (and consumes) a cancel-sent Ctrl-C for the
// pane within interruptGrace.
func (s *Supervisor) recentlyInterrupted(paneID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.interrupted[paneID]
	if !ok {
		return false
	}
	delete(s.interrupted, paneID)
	return s.cfg.Now().Sub(at) <= interruptGrace
}

// recordBlock stores the question on the row's last_block.
func (s *Supervisor) recordBlock(paneID string, sum llm.BlockSummary) {
	_, err := s.store.Update(paneID, func(a *state.Agent) {
		if a.LastBlock == nil {
			a.LastBlock = &state.Block{SeenAt: s.cfg.Now()}
		}
		a.LastBlock.Question = sum.Question
		a.LastBlock.Answered = !sum.IsQuestion
	})
	if err != nil {
		s.log.Log(logSource, "supervisor.state_error", paneID, map[string]any{"error": err.Error()})
	}
	s.invalidateBriefing()
}

// forgetExpired drops the remembered expired question of paneID. Caller
// holds s.mu.
func (s *Supervisor) forgetExpired(paneID string) {
	if s.expired != nil && s.expired.PaneID == paneID {
		s.expired = nil
	}
}

// aliasOf returns the alias of a pane, or "another agent" when unknown
// (never a pane id: decision D9).
func (s *Supervisor) aliasOf(paneID string) string {
	if a, ok := s.store.Get(paneID); ok && a.Alias != "" {
		return a.Alias
	}
	return "another agent"
}

func aliasOr(alias string) string {
	if alias == "" {
		return "an agent"
	}
	return alias
}
