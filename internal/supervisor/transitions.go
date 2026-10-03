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
	if !t.Closed {
		// The worker's own session title, read as a fact: it may
		// change the spoken names (names.go).
		s.refreshTitle(row)
		if r, ok := s.store.Get(t.PaneID); ok {
			row = r
		}
	}
	defer s.syncNames()
	s.invalidateBriefing()
	if t.Renamed && !t.Closed && (t.From == "" || t.From == t.To) {
		// A name-only update (herdr's own rename, found by the
		// snapshot diff): the row has the new name and the voice gets
		// the names line; it is not a state change, so nothing is
		// announced, journalled or told to the brain as a worker event.
		if state.Say(prev) != state.Say(row) {
			// A voice rename comes back from herdr on the next
			// snapshot with the name the row already has: no event.
			s.log.Log(logSource, "supervisor.renamed", t.PaneID, map[string]any{"from": state.Say(prev), "to": state.Say(row), "name": t.Name})
		}
		return
	}

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
		s.brain.WorkerEventLinked(alias, string(from), string(t.To), t.Closed, eventLink(s.newEventID(t.PaneID)))
	}

	if t.Closed {
		s.mu.Lock()
		s.dropPendingFor(t.PaneID, "pane_closed")
		s.forgetPrompt(t.PaneID)
		s.mu.Unlock()
		s.cfg.WorkerLog.Forget(t.PaneID)
		s.journal.clearMissing(t.PaneID)
		if wasBusy {
			s.push(t.PaneID, aliasOr(state.Say(prev))+" has gone away; its pane closed.", voice.UrgencyInterrupt)
		}
		return
	}

	alias := aliasOr(state.Say(row))
	switch t.To {
	case herdr.StateBlocked:
		// From is not consulted: a synthetic blocked -> blocked
		// transition (reprompt.go) takes the same path as a real
		// working -> blocked one. onBlocked always re-reads and
		// re-announces, so a new prompt gets its own pending action and
		// push; state_since does not move (Store.Apply is a no-op when
		// the state does not change), so nothing is recorded as a
		// "started on" or a finish (docs/roles.md "blocked -> blocked is
		// a block"). A new block adds a journal entry first, so its
		// announce turn hears what the worker last wrote.
		if from != herdr.StateBlocked {
			s.recordJournal(row, string(herdr.StateBlocked), false)
		}
		s.onBlocked(ctx, row)
	case herdr.StateUnknown:
		s.mu.Lock()
		s.dropPendingFor(t.PaneID, "agent_exited")
		s.forgetPrompt(t.PaneID)
		s.mu.Unlock()
		if wasBusy {
			s.push(t.PaneID, alias+" has exited while it was busy.", voice.UrgencyInterrupt)
		}
	case herdr.StateWorking:
		s.mu.Lock()
		// Whatever the previous state, a working pane is no longer at a
		// prompt (agy reports done, not blocked, around its prompts).
		s.dropPendingFor(t.PaneID, "prompt_gone", state.PendingAnswerPrompt)
		s.forgetPrompt(t.PaneID)
		started, justSent := s.startedInstruction(t.PaneID, from)
		s.mu.Unlock()
		if started != "" {
			s.pushStarted(t.PaneID, alias+" started on "+started+".", justSent)
		}
		s.keepPromises(ctx, row, string(t.To), alias+" is working now.")
	case herdr.StateDone, herdr.StateIdle:
		s.mu.Lock()
		if from == herdr.StateBlocked {
			s.dropPendingFor(t.PaneID, "prompt_gone", state.PendingAnswerPrompt)
			s.forgetPrompt(t.PaneID)
		}
		s.dropPendingFor(t.PaneID, "finished", state.PendingSendInstruction)
		s.mu.Unlock()
		// Read before finishTask moves the task to last_task: was the
		// agent running an instruction the shepherd gave this run?
		instructed := s.runningShepherdTask(row)
		if from != herdr.StateDone && from != "" {
			// The journal entry comes before the announce turn (onDone),
			// which carries the worker's last message; done -> idle is
			// the same finish seen again and adds none, and a pane first
			// seen at rest has no news (its transcript is history).
			s.recordJournal(row, string(t.To), from == herdr.StateWorking)
		}
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
// prompt), else "". justSent is set when that instruction went out
// within Config.CancelWindow — the same "instructed at" the stop
// procedure reads (stop.go). Caller holds s.mu.
func (s *Supervisor) startedInstruction(paneID string, from herdr.State) (started string, justSent bool) {
	if from == herdr.StateBlocked {
		return "", false
	}
	p, has := s.store.Pending()
	if !has || p.Kind != state.PendingSendInstruction || p.PaneID != paneID {
		return "", false
	}
	justSent = s.cfg.Now().Sub(p.CreatedAt) <= s.cfg.CancelWindow
	if t := shortTask(withoutSignOff(p.PayloadText)); t != "" {
		return t, justSent
	}
	return "the instruction", justSent
}

// pushStarted pushes "<alias> started on …" under the chatty level
// (docs/roles.md "Pushes"). When the brain's own prompt went out just now
// the push is silent, context only: the tool result already told the
// shepherd it was sent, and "started on" as well was the duplicate
// speech of 2026-09-24 00:13:02. An instruction picked up long after it
// was sent (the worker was busy) is news, spoken at idle as before.
func (s *Supervisor) pushStarted(paneID, text string, justSent bool) {
	if !justSent {
		s.pushIf(pushStarted, paneID, text)
		return
	}
	if level := s.chattiness(); !level.allows(pushStarted) {
		s.log.Log(logSource, "supervisor.push_quiet", paneID, map[string]any{"text": text, "chattiness": string(level)})
		return
	}
	s.pushTagged(paneID, text, voice.UrgencySilent, nil)
}

// finishTask moves the row's current task to last_task.
func (s *Supervisor) finishTask(paneID string) {
	_, err := s.store.Update(paneID, func(a *state.Agent) {
		if a.CurrentTask != "" {
			a.LastTask, a.CurrentTask = withoutSignOff(a.CurrentTask), ""
		}
	})
	if err != nil {
		s.log.Log(logSource, "supervisor.state_error", paneID, map[string]any{"error": err.Error()})
	}
	s.invalidateBriefing()
}

// onDone reads the pane of a finished agent and hands "<alias> finished."
// to announce: the finish waits for the brain's line, which replaces it
// unless the turn fails, and whichever is spoken becomes last_result. It is
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
// blocked (testdata/herdr: claude-permission-prompt and
// codex-approval-prompt report agent_status "blocked", agy's prompts
// "done"), so a prompt on its finish is one it showed after its turn —
// unless it shows a command to approve, which stays a block for anyone
// (Claude Code's "teach auto mode about your environment?"): the finish
// is announced — what it found — together with the question, in one line
// (announceFinishAsking).
func (s *Supervisor) onDone(ctx context.Context, row state.Agent, instructed bool) {
	alias := aliasOr(state.Say(row))
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
		if prompt, ok := herdr.ParsePrompt(read.Text); ok && row.Kind != kindAgy && prompt.Command == "" {
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
	alias := aliasOr(state.Say(row))
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
	alias := aliasOr(state.Say(row))
	s.touch(row.PaneID)
	read, err := s.herdr.Read(ctx, row.PaneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines)
	if err != nil {
		s.log.Log(logSource, "supervisor.block_error", row.PaneID, map[string]any{"stage": "read", "error": err.Error()})
		// It may well be a question: every level hears this.
		line := alias + " is blocked, but I could not read its screen."
		s.notify(announcement{row: row, event: brain.PromiseEventBlocked, eventID: s.eventIDOf(row.PaneID)}, nil, line)
		s.push(row.PaneID, line, voice.UrgencyIdle)
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
	alias := aliasOr(state.Say(row))
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

// forgetPrompt drops when paneID's prompt first appeared. Caller holds
// s.mu.
func (s *Supervisor) forgetPrompt(paneID string) {
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
