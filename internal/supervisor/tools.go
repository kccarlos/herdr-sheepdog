package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The voice's two tools (docs/roles.md "One decision-maker"):
// to_supervisor carries the CEO's words verbatim, stop runs the stop
// procedure. Nothing else is declared to the voice model.
const (
	ToolToSupervisor = voice.ToolToSupervisor
	ToolStop         = voice.ToolStop
)

// The older entry points. They were the voice's tools until D1-revised
// and are no longer declared to any model; `sheepdog brain-test` and the
// tests drive the matching methods directly.
const (
	ToolBriefing = "briefing"
	ToolAsk      = "ask"
	ToolPropose  = "propose"
	ToolConfirm  = "confirm"
	ToolCancel   = "cancel"
	ToolNote     = "note"
)

// Handler adapts the two voice tools to voice.ToolHandler. Unknown tools
// are an error; missing arguments are treated as empty strings.
func (s *Supervisor) Handler() voice.ToolHandler {
	return func(ctx context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		switch call.Name {
		case ToolToSupervisor:
			text, _ := call.Args["text"].(string)
			return s.ToSupervisor(ctx, text)
		case ToolStop:
			return s.Stop(ctx, StopTriggerTool)
		default:
			return voice.ToolResult{}, fmt.Errorf("supervisor: unknown tool %q", call.Name)
		}
	}
}

// Briefing returns the one-sentence status from the state table. It never
// calls a model and is served from a cache invalidated on every change.
// The agents it names count as dealt with (the normal level then
// announces their finish); that is kept in memory, not persisted, so the
// briefing never waits on the state file.
func (s *Supervisor) Briefing(context.Context) (voice.ToolResult, error) {
	text, panes := s.briefingText()
	s.markMentioned(panes)
	return voice.ToolResult{Spoken: text, Scheduling: voice.UrgencyInterrupt}, nil
}

// Ask answers a question about an agent through the brain; with no brain
// the daemon can only report the roster (noBrain). It is not declared to
// any model since D1-revised: `sheepdog brain-test --kind ask` and the
// evals drive it.
func (s *Supervisor) Ask(ctx context.Context, question, agent string) (voice.ToolResult, error) {
	if res, ok := s.brainTurn(ctx, brain.KindAsk, strings.TrimSpace(question), agent); ok {
		return res, nil
	}
	return s.noBrain(), nil
}

// workspaceDir returns the pane's working directory from herdr, or "".
func (s *Supervisor) workspaceDir(ctx context.Context, row state.Agent) string {
	agents, err := s.herdr.ListAgents(ctx)
	if err != nil {
		return ""
	}
	for _, a := range agents {
		if a.PaneID == row.PaneID {
			return a.Cwd
		}
	}
	return ""
}

// Propose sends the user's words to one agent through the brain (D4:
// echo, then send, then cancellable); with no brain nothing is sent and
// the daemon says so. Like Ask, it is no longer declared to any model:
// `sheepdog brain-test --kind propose` and the evals drive it.
func (s *Supervisor) Propose(ctx context.Context, instruction, agent string) (voice.ToolResult, error) {
	// The brain reads the CEO's own words from its transcript, so an
	// empty instruction (a bare "let me know when it's done") still
	// reaches it.
	if res, ok := s.brainTurn(ctx, brain.KindPropose, strings.TrimSpace(instruction), agent); ok {
		return res, nil
	}
	return s.noBrain(), nil
}

// noBrain is everything the daemon can say without one: the roster
// sentence composeBriefing already keeps, and why that is all. Nothing
// is sent to a worker — instructions are the brain's judgment
// (docs/roles.md "One decision-maker"). Within supervisor.repeat_window_s
// of an identical roster, the sentence is prefixed "No change since I
// last checked: …" — there is no brain here to add a note to instead.
func (s *Supervisor) noBrain() voice.ToolResult {
	text, panes := s.briefingText()
	s.markMentioned(panes)
	if _, repeat := s.briefingRepeat(text); repeat {
		text = "No change since I last checked: " + text
	}
	return voice.ToolResult{Spoken: text + " " + noBrainTrailer, Scheduling: voice.UrgencyInterrupt}
}

// noBrainTrailer says why the roster is the whole answer.
const noBrainTrailer = "— the brain is off, I can only report status."

// markMentioned records that the panes were just spoken about: the
// normal level announces their finish for RecentWindow afterwards.
func (s *Supervisor) markMentioned(panes []string) {
	now := s.cfg.Now()
	s.mu.Lock()
	for _, id := range panes {
		s.mentioned[id] = now
	}
	s.mu.Unlock()
}

// mentionedIn marks every agent the sentence names by alias, so a reply
// that talked about an agent counts as dealing with it exactly as a
// spoken briefing does.
func (s *Supervisor) mentionedIn(spoken string) {
	if spoken == "" {
		return
	}
	lower := strings.ToLower(spoken)
	var panes []string
	for _, row := range s.store.List() {
		if row.Alias != "" && strings.Contains(lower, strings.ToLower(row.Alias)) {
			panes = append(panes, row.PaneID)
		}
	}
	s.markMentioned(panes)
}

// sendInstruction creates the in-flight send_instruction action and
// sends text to the pane as typed input (D4: echo, send, cancellable);
// the action is created before the send so a cancel racing it still
// interrupts the pane. It returns what to say and, on failure, the send
// error (an agent at a prompt is reported through reportBlocked).
func (s *Supervisor) sendInstruction(ctx context.Context, row state.Agent, text, echo string) (voice.ToolResult, error) {
	alias := aliasOr(row.Alias)
	s.mu.Lock()
	s.lastSubject = row.PaneID
	p := s.newSendInstruction(row.PaneID, text, echo)
	s.setPending(p)
	_, err := s.herdr.Prompt(ctx, row.PaneID, text)
	if err != nil {
		s.clearPending(p, "send_failed")
	} else {
		now := s.cfg.Now()
		if _, uerr := s.store.Update(row.PaneID, func(a *state.Agent) { a.CurrentTask, a.LastInteraction = text, &now }); uerr != nil {
			s.log.Log(logSource, "supervisor.state_error", row.PaneID, map[string]any{"error": uerr.Error()})
		}
		s.invalidateBriefingLocked()
	}
	s.mu.Unlock()

	if err != nil {
		if errors.Is(err, herdr.ErrAgentBlocked) {
			return s.reportBlocked(ctx, row), err
		}
		s.log.Log(logSource, "supervisor.propose_error", row.PaneID, map[string]any{"stage": "send", "error": err.Error()})
		return voice.ToolResult{Spoken: "I could not send that to " + alias + ".", Scheduling: voice.UrgencyInterrupt}, err
	}
	return voice.ToolResult{Spoken: echo + " Sent.", Scheduling: voice.UrgencyInterrupt}, nil
}

// reportBlocked handles agent.prompt being refused with agent_blocked: the
// agent is at a prompt we have not announced yet. It reads the screen and
// turns it into the pending question.
func (s *Supervisor) reportBlocked(ctx context.Context, row state.Agent) voice.ToolResult {
	alias := aliasOr(row.Alias)
	read, err := s.herdr.Read(ctx, row.PaneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines)
	if err != nil {
		return voice.ToolResult{Spoken: alias + " is waiting at a prompt first, but I could not read it.", Scheduling: voice.UrgencyInterrupt}
	}
	prompt, ok := herdr.ParsePrompt(read.Text)
	if !ok {
		return voice.ToolResult{Spoken: alias + " is waiting at a prompt first; answer that before a new instruction.", Scheduling: voice.UrgencyInterrupt}
	}
	s.recordBlock(row.PaneID, prompt.Phrase(), false)
	s.mu.Lock()
	spoken := s.setPending(s.newAnswerPrompt(row.PaneID, prompt, alias))
	s.mu.Unlock()
	return voice.ToolResult{Spoken: spoken + " Answer that first.", Clarify: true, Scheduling: voice.UrgencyInterrupt}
}

// Confirm answers the pending question (D3: no id means the most recent).
// An expired question is revived and asked again (scenario 8).
func (s *Supervisor) Confirm(ctx context.Context, answer string) (voice.ToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, has := s.store.Pending()
	if !has {
		return s.confirmExpired(answer), nil
	}
	if p.Kind != state.PendingAnswerPrompt {
		return voice.ToolResult{Spoken: "Nothing is waiting for an answer; the instruction to " + s.aliasOf(p.PaneID) + " was already sent.", Scheduling: voice.UrgencyInterrupt}, nil
	}
	return s.answerPending(ctx, p, answer, "Told "+s.aliasOf(p.PaneID)+" %s.", "confirm"), nil
}

// confirmExpired implements scenario 8: a confirm after the question
// expired re-creates it and asks before sending. Caller holds s.mu.
func (s *Supervisor) confirmExpired(answer string) voice.ToolResult {
	none := voice.ToolResult{Spoken: "Nothing is waiting for an answer.", Scheduling: voice.UrgencyInterrupt}
	e := s.expired
	if e == nil {
		return none
	}
	row, ok := s.store.Get(e.PaneID)
	if !ok || row.State == herdr.StateWorking || row.State == herdr.StateUnknown {
		// Moved on (or gone): the prompt is no longer there to answer. A
		// done row may still be at a prompt herdr did not flag (agy).
		s.expired = nil
		return none
	}
	revived := *e
	revived.ID = s.nextID("ap")
	revived.CreatedAt = s.cfg.Now()
	revived.ExpiresAt = revived.CreatedAt.Add(s.cfg.PendingTTL)
	s.setPending(revived)
	s.log.Log(logSource, "pending.revived", e.PaneID, map[string]any{"old_id": e.ID, "id": revived.ID})
	word := "yes"
	if _, label, err := mapAnswer(revived, answer); err == nil {
		word = label
	}
	q := revived.Question
	if q == "" {
		q = "something"
	}
	return voice.ToolResult{
		Spoken:     fmt.Sprintf("That question expired; %s was asking %s. Should I answer %s?", aliasOr(row.Alias), q, word),
		Clarify:    true,
		Scheduling: voice.UrgencyInterrupt,
	}
}

// answerPending maps the answer onto keys, sends them and clears the slot.
// format receives the spoken label ("yes", "option 2"); via names the
// path for pending.confirmed. Caller holds s.mu.
func (s *Supervisor) answerPending(ctx context.Context, p state.PendingAction, answer, format, via string) voice.ToolResult {
	alias := s.aliasOf(p.PaneID)
	keys, label, err := mapAnswer(p, answer)
	if err != nil {
		spoken := fmt.Sprintf("I do not know how to answer %q for %s.", strings.TrimSpace(answer), alias)
		if len(p.Options) > 0 {
			spoken += " The options are " + optionList(p.Options) + "."
		}
		return voice.ToolResult{Spoken: spoken, Clarify: true, Scheduling: voice.UrgencyInterrupt}
	}
	return s.answerWithKeys(ctx, p, keys, label, format, via)
}

// answerWithKeys sends keys to the pending prompt's pane, clears the
// slot (pending.confirmed {via}) and starts the post-answer poll
// (reprompt.go): herdr may already be showing the next prompt within its
// own poll window with no state change in between. Caller holds s.mu.
func (s *Supervisor) answerWithKeys(ctx context.Context, p state.PendingAction, keys []string, label, format, via string) voice.ToolResult {
	alias := s.aliasOf(p.PaneID)
	if err := s.herdr.SendKeys(ctx, p.PaneID, keys...); err != nil {
		s.log.Log(logSource, "supervisor.confirm_error", p.PaneID, map[string]any{"id": p.ID, "keys": keys, "error": err.Error()})
		return voice.ToolResult{Spoken: "I could not send that to " + alias + ".", Scheduling: voice.UrgencyInterrupt}
	}
	if err := s.store.ClearPending(); err != nil {
		s.log.Log(logSource, "supervisor.state_error", p.PaneID, map[string]any{"error": err.Error()})
	}
	now := s.cfg.Now()
	if _, err := s.store.Update(p.PaneID, func(a *state.Agent) {
		if a.LastBlock != nil {
			a.LastBlock.Answered = true
		}
		a.LastInteraction = &now
	}); err != nil {
		s.log.Log(logSource, "supervisor.state_error", p.PaneID, map[string]any{"error": err.Error()})
	}
	s.expired = nil
	s.invalidateBriefingLocked()
	s.lastSubject = p.PaneID
	s.log.Log(logSource, "pending.confirmed", p.PaneID, map[string]any{"id": p.ID, "answer": label, "keys": keys, "via": via})
	s.startReprompt(p.PaneID)
	return voice.ToolResult{Spoken: fmt.Sprintf(format, label), Scheduling: voice.UrgencyInterrupt}
}

// Cancel interrupts the in-flight instruction (Ctrl-C to its pane) or
// drops the pending question; with nothing pending, it cancels the most
// recently made open promise ("Cancelled the reminder.").
func (s *Supervisor) Cancel(ctx context.Context) (voice.ToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, has := s.store.Pending()
	if !has {
		s.expired = nil
		if _, ok := s.cancelLatestPromise(); ok {
			return voice.ToolResult{Spoken: "Cancelled the reminder.", Scheduling: voice.UrgencyInterrupt}, nil
		}
		return voice.ToolResult{Spoken: "Nothing to cancel.", Scheduling: voice.UrgencyInterrupt}, nil
	}
	spoken := "Cancelled."
	if p.Kind != state.PendingAnswerPrompt {
		if err := s.herdr.Interrupt(ctx, p.PaneID); err != nil {
			s.log.Log(logSource, "supervisor.cancel_error", p.PaneID, map[string]any{"id": p.ID, "error": err.Error()})
			spoken = "Cancelled, but I could not interrupt " + s.aliasOf(p.PaneID) + "."
		} else {
			s.interrupted[p.PaneID] = s.cfg.Now()
		}
	}
	s.clearPending(p, "user")
	return voice.ToolResult{Spoken: spoken, Scheduling: voice.UrgencyInterrupt}, nil
}

// Note records something the user wants remembered.
func (s *Supervisor) Note(_ context.Context, text string) (voice.ToolResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return voice.ToolResult{Spoken: "What should I remember?", Clarify: true, Scheduling: voice.UrgencyInterrupt}, nil
	}
	if err := s.appendNote(text, true); err != nil {
		s.log.Log(logSource, "supervisor.note_error", "", map[string]any{"error": err.Error()})
		return voice.ToolResult{Spoken: "I could not save that note.", Scheduling: voice.UrgencyInterrupt}, nil
	}
	return voice.ToolResult{Spoken: "Noted.", Scheduling: voice.UrgencyInterrupt}, nil
}

// findAlias matches an alias exactly, then by slug, then by prefix or
// containment when that is unambiguous.
func findAlias(rows []state.Agent, name string) (state.Agent, bool) {
	lname := strings.ToLower(name)
	slug := state.Slug(name)
	for _, r := range rows {
		if strings.ToLower(r.Alias) == lname || (slug != "" && r.Alias == slug) {
			return r, true
		}
	}
	var hit state.Agent
	hits := 0
	for _, r := range rows {
		if slug != "" && (strings.HasPrefix(r.Alias, slug) || strings.Contains(r.Alias, slug) || strings.Contains(slug, r.Alias)) {
			hit, hits = r, hits+1
		}
	}
	if hits == 1 {
		return hit, true
	}
	return state.Agent{}, false
}
