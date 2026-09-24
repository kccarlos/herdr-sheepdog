package supervisor

import (
	"context"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
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
	if s.brain != nil {
		alias := row.Alias
		if alias == "" {
			alias = prev.Alias
		}
		s.brain.WorkerEvent(alias, string(from), string(t.To), t.Closed)
	}

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
		// From is not consulted: a synthetic blocked -> blocked
		// transition (reprompt.go) takes the same path as a real
		// working -> blocked one. onBlocked always re-reads and
		// re-announces, so a new prompt gets its own pending action and
		// push; state_since does not move (Store.Apply is a no-op when
		// the state does not change), so nothing is recorded as a
		// "started on" or a finish (docs/roles.md "blocked -> blocked is
		// a block").
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
			s.pushIf(pushStarted, t.PaneID, alias+" started on "+started+".")
		}
		s.keepPromises(ctx, row, string(t.To), alias+" is working now.")
	case herdr.StateDone, herdr.StateIdle:
		s.mu.Lock()
		if from == herdr.StateBlocked {
			s.dropPendingFor(t.PaneID, "prompt_gone", state.PendingAnswerPrompt)
			s.forgetExpired(t.PaneID)
		}
		s.dropPendingFor(t.PaneID, "finished", state.PendingSendInstruction)
		s.mu.Unlock()
		// Read before finishTask moves the task to last_task: was the
		// agent running an instruction the shepherd gave this run?
		instructed := s.runningShepherdTask(row)
		s.finishTask(t.PaneID)
		// working -> done and working -> idle are both a finish: herdr
		// reports done when the user was not looking at the pane and
		// idle when it was focused when the agent finished
		// (herdr/src/pane/state.rs's "seen" bit), so either edge off
		// working means the agent just finished a turn.
		if from == herdr.StateWorking {
			if s.recentlyInterrupted(t.PaneID) {
				// The user's cancel sent Ctrl-C: this is the
				// interrupted turn settling, not work finishing; a
				// promise to tell when it is done stays open.
				s.log.Log(logSource, "supervisor.finished_suppressed", t.PaneID, map[string]any{"reason": "interrupted"})
			} else {
				s.onDone(ctx, row, instructed)
			}
			return
		}
		// done -> idle ("idle again") and blocked -> idle are never
		// announced as a finish, but a promise for idle (or any change)
		// is still kept.
		if t.To == herdr.StateIdle {
			s.keepPromises(ctx, row, string(t.To), alias+" is idle now.")
		}
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

// onDone reads the pane of a finished agent and hands "<alias> finished."
// to announce: the brain's line replaces it when one lands within
// AnnounceWait, and whichever is spoken becomes last_result. It is
// spoken when the chattiness level allows it for this agent (every finish
// at chatty; at normal only agents the user dealt with within
// RecentWindow; never at quiet), with the finish's open promises kept in
// the same line. instructed is set when the agent was running an
// instruction the shepherd gave this run (runningShepherdTask): the
// finish is then owed to them like a promise (announce.go).
//
// A screen that ends at a prompt is one of two things. agy reports done
// around its own approval prompts mid-task, so for agy it is a block and
// nothing finished. Any other agent's real approval prompts arrive as
// blocked, so a prompt on its finish is one it showed after its turn
// (Claude Code's "teach auto mode about your environment?"): the finish
// is announced — what it found — together with the question, in one line
// (announceFinishAsking).
func (s *Supervisor) onDone(ctx context.Context, row state.Agent, instructed bool) {
	alias := aliasOr(row.Alias)
	kind := s.doneKind(row)
	// herdr reports the finish as idle rather than done when the user
	// had the pane focused (herdr/src/pane/state.rs's "seen" bit): an
	// "idle" promise is kept by this finish too, alongside "done".
	alsoIdle := row.State == herdr.StateIdle
	read, err := s.herdr.Read(ctx, row.PaneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines)
	if err != nil {
		s.log.Log(logSource, "supervisor.done_error", row.PaneID, map[string]any{"stage": "read", "error": err.Error()})
		s.announce(ctx, announcement{row: row, kind: kind, event: brain.PromiseEventDone, alsoIdle: alsoIdle, owed: instructed, sentence: alias + " finished."})
		return
	}
	if herdr.EndsInQuestion(read.Text) {
		if prompt, ok := herdr.ParsePrompt(read.Text); ok && row.Kind != kindAgy {
			s.announceFinishAsking(ctx, row, prompt, read.Text, alsoIdle, instructed)
			return
		}
		s.log.Log(logSource, "supervisor.done_question", row.PaneID, map[string]any{"alias": alias})
		s.touch(row.PaneID)
		s.announceBlock(ctx, row, read.Text)
		return
	}
	s.announce(ctx, announcement{row: row, kind: kind, event: brain.PromiseEventDone, alsoIdle: alsoIdle, owed: instructed, sentence: alias + " finished.", screen: read.Text})
}

// kindAgy is herdr's agent kind label for the Antigravity CLI, which
// reports done (not blocked) around its approval prompts.
const kindAgy = "agy"

// announceFinishAsking handles a finish whose screen ends at a prompt the
// agent showed after its turn: the question is recorded and made the
// pending answer_prompt exactly as a block's would be, and one announce
// turn covers both — the finish (its promises for done, idle and blocked
// are kept by it) and the question. The code sentence is "<alias>
// finished, and it's asking <question>."; a question is heard at every
// level.
func (s *Supervisor) announceFinishAsking(ctx context.Context, row state.Agent, prompt herdr.Prompt, screen string, alsoIdle, instructed bool) {
	alias := aliasOr(row.Alias)
	s.log.Log(logSource, "supervisor.done_asking", row.PaneID, map[string]any{"alias": alias, "question": prompt.Phrase()})
	s.touch(row.PaneID)
	s.recordBlock(row.PaneID, prompt.Phrase(), false)
	s.mu.Lock()
	p := s.newAnswerPrompt(row.PaneID, prompt, alias, row.StateSince)
	s.setPending(p)
	s.mu.Unlock()
	sentence := alias + " finished, and it's asking " + prompt.Phrase() + "."
	if prompt.Phrase() == "" {
		sentence = alias + " finished, and it's waiting at a prompt."
	}
	s.announce(ctx, announcement{row: row, kind: pushQuestion, event: brain.PromiseEventDone, alsoIdle: alsoIdle, alsoBlocked: true, owed: instructed,
		sentence: sentence, asking: prompt.Phrase(), screen: screen, pending: p.ID})
}

// runningShepherdTask reports whether row's current task is an
// instruction the shepherd sent this run (sendInstruction sets the task
// and the interaction time together).
func (s *Supervisor) runningShepherdTask(row state.Agent) bool {
	return row.CurrentTask != "" && row.LastInteraction != nil && !row.LastInteraction.Before(s.startedAt)
}

// doneResult is what the record keeps of a finish the brain did not
// phrase (state.Result.Spoken completes "it last …").
const doneResult = "finished"

// recordResult stores what was announced about the finish as the row's
// last_result.
func (s *Supervisor) recordResult(paneID, spoken string) {
	now := s.cfg.Now()
	_, err := s.store.Update(paneID, func(a *state.Agent) {
		a.LastResult = &state.Result{Spoken: spoken, At: now}
	})
	if err != nil {
		s.log.Log(logSource, "supervisor.state_error", paneID, map[string]any{"error": err.Error()})
	}
	s.invalidateBriefing()
}

// onBlocked reads the pane, parses the prompt, records the question
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
	s.announceBlock(ctx, row, read.Text)
}

// announceBlock parses the prompt already read from the pane, records the
// question and — when the screen really is an approval prompt — creates
// the pending action whose keys the fast path and the rail answer.
// announce then keeps the block's promises and speaks the one line the
// block gets: the brain's, or the parsed prompt's own sentence.
func (s *Supervisor) announceBlock(ctx context.Context, row state.Agent, screen string) {
	alias := aliasOr(row.Alias)
	prompt, ok := herdr.ParsePrompt(screen)
	if !ok {
		// Blocked at something with no options to send: the user has to
		// look. Nothing is pending, so nothing can be answered for them.
		s.log.Log(logSource, "supervisor.blocked_no_question", row.PaneID, map[string]any{"alias": alias})
		s.recordBlock(row.PaneID, "", true)
		s.announce(ctx, announcement{row: row, kind: pushQuestion, event: brain.PromiseEventBlocked, sentence: alias + " is waiting at a prompt.", screen: screen})
		return
	}
	s.recordBlock(row.PaneID, prompt.Phrase(), false)
	s.mu.Lock()
	p := s.newAnswerPrompt(row.PaneID, prompt, alias, row.StateSince)
	sentence := s.setPending(p)
	s.mu.Unlock()
	s.announce(ctx, announcement{row: row, kind: pushQuestion, event: brain.PromiseEventBlocked, sentence: sentence, screen: screen, pending: p.ID})
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

// recordBlock stores the question on the row's last_block; answered is
// set for a block with nothing to answer, so the roster does not claim
// the agent is waiting on the shepherd.
func (s *Supervisor) recordBlock(paneID, question string, answered bool) {
	_, err := s.store.Update(paneID, func(a *state.Agent) {
		if a.LastBlock == nil {
			a.LastBlock = &state.Block{SeenAt: s.cfg.Now()}
		}
		a.LastBlock.Question = question
		a.LastBlock.Answered = answered
	})
	if err != nil {
		s.log.Log(logSource, "supervisor.state_error", paneID, map[string]any{"error": err.Error()})
	}
	s.invalidateBriefing()
}

// forgetExpired drops the remembered expired question of paneID, and when
// its prompt first appeared. Caller holds s.mu.
func (s *Supervisor) forgetExpired(paneID string) {
	if s.expired != nil && s.expired.PaneID == paneID {
		s.expired = nil
	}
	// The pane left its prompt (answered in the terminal, say): a prompt
	// that reads the same later appeared then, not now (inputs.go).
	delete(s.promptSeenAt, paneID)
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
