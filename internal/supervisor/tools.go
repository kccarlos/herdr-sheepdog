package supervisor

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Tool names (plan §5).
const (
	ToolBriefing = "briefing"
	ToolAsk      = "ask"
	ToolPropose  = "propose"
	ToolConfirm  = "confirm"
	ToolCancel   = "cancel"
	ToolNote     = "note"
)

// Handler adapts the five tools to voice.ToolHandler. Unknown tools are an
// error; missing arguments are treated as empty strings.
func (s *Supervisor) Handler() voice.ToolHandler {
	return func(ctx context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		arg := func(name string) string {
			v, _ := call.Args[name].(string)
			return v
		}
		switch call.Name {
		case ToolBriefing:
			return s.Briefing(ctx)
		case ToolAsk:
			return s.Ask(ctx, arg("question"), arg("agent"))
		case ToolPropose:
			return s.Propose(ctx, arg("instruction"), arg("agent"))
		case ToolConfirm:
			return s.Confirm(ctx, arg("answer"))
		case ToolCancel:
			return s.Cancel(ctx)
		case ToolNote:
			return s.Note(ctx, arg("text"))
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
	now := s.cfg.Now()
	s.mu.Lock()
	for _, id := range panes {
		s.mentioned[id] = now
	}
	s.mu.Unlock()
	return voice.ToolResult{Spoken: text, Scheduling: voice.UrgencyInterrupt}, nil
}

// deepQuestion matches questions that need the repository, not the
// screen (plan WP4 routing rule).
var deepQuestion = regexp.MustCompile(`(?i)\b(why|diff|log|logs|fail|failed|failing|failure|changed|changes|broke|broken)\b`)

// Ask answers a question about an agent: through the brain when one is
// attached, else (or on its fallback) from the screen tail, or — when a
// Querier is wired and the question needs the repo (or the screen was not
// enough) — from the read-only CLI tool's output.
func (s *Supervisor) Ask(ctx context.Context, question, agent string) (voice.ToolResult, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return voice.ToolResult{Spoken: "What would you like to know?", Clarify: true, Scheduling: voice.UrgencyInterrupt}, nil
	}
	if res, ok := s.brainTurn(ctx, brain.KindAsk, question, agent); ok {
		return res, nil
	}
	row, res, ok := s.target(agent)
	if !ok {
		return res, nil
	}
	s.touch(row.PaneID)
	s.setSubject(row.PaneID)
	alias := aliasOr(row.Alias)
	var ans llm.Answer
	var err error
	deep := s.querier != nil && deepQuestion.MatchString(question)
	if !deep {
		var read herdr.ReadResult
		read, err = s.herdr.Read(ctx, row.PaneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines)
		if err == nil {
			ans, err = s.llm.SpokenAnswer(ctx, read.Text, question, alias)
		}
		deep = s.querier != nil && (err != nil || !ans.CanAnswer)
	}
	if deep {
		if out, qerr := s.querier.Query(ctx, s.workspaceDir(ctx, row), question); qerr == nil {
			if deepAns, aerr := s.llm.SpokenAnswer(ctx, out, question, alias); aerr == nil {
				ans, err = deepAns, nil
			} else if err == nil {
				err = aerr
			}
		} else {
			s.log.Log(logSource, "supervisor.query_error", row.PaneID, map[string]any{"error": qerr.Error()})
		}
	}
	if err != nil {
		s.log.Log(logSource, "supervisor.ask_error", row.PaneID, map[string]any{"error": err.Error()})
		return voice.ToolResult{Spoken: "I could not read " + alias + "'s screen just now.", Scheduling: voice.UrgencyIdle}, nil
	}
	return voice.ToolResult{Spoken: ans.Spoken, Scheduling: voice.UrgencyIdle}, nil
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

// Propose resolves the user's words onto one agent and sends them (D4:
// echo, then send, then cancellable): through the brain when one is
// attached, else (or on its fallback) with resolve_instruction. Ambiguity
// returns a clarify question and sends nothing (D6). An answer to a
// pending question goes through send_keys; anything else through
// agent.prompt.
func (s *Supervisor) Propose(ctx context.Context, instruction, agent string) (voice.ToolResult, error) {
	instruction = strings.TrimSpace(instruction)
	// The brain reads the CEO's own words from its transcript, so an
	// empty instruction (a bare "let me know when it's done") still
	// reaches it; only the template path needs the text.
	if res, ok := s.brainTurn(ctx, brain.KindPropose, instruction, agent); ok {
		return res, nil
	}
	if instruction == "" {
		return voice.ToolResult{Spoken: "What should I tell it?", Clarify: true, Scheduling: voice.UrgencyInterrupt}, nil
	}
	summaries := s.agentSummaries()
	if len(summaries) == 0 {
		return voice.ToolResult{Spoken: "No agents are running, so there is nobody to tell.", Scheduling: voice.UrgencyInterrupt}, nil
	}
	res, err := s.llm.ResolveInstruction(ctx, instruction, summaries, agent)
	if err != nil {
		s.log.Log(logSource, "supervisor.propose_error", "", map[string]any{"stage": "resolve", "error": err.Error()})
		return voice.ToolResult{Spoken: "I could not work out what to send; please say it again.", Clarify: true, Scheduling: voice.UrgencyInterrupt}, nil
	}
	if res.Ambiguous {
		s.log.Log(logSource, "supervisor.propose_ambiguous", "", map[string]any{"instruction": instruction, "clarify": res.ClarifyQuestion})
		return voice.ToolResult{Spoken: res.ClarifyQuestion, Clarify: true, Scheduling: voice.UrgencyInterrupt}, nil
	}
	row, ok := s.store.Get(res.PaneID)
	if !ok {
		return voice.ToolResult{Spoken: "That agent is gone.", Scheduling: voice.UrgencyInterrupt}, nil
	}
	alias := aliasOr(row.Alias)

	s.mu.Lock()
	if pending, has := s.store.Pending(); res.IsAnswer && has && pending.Kind == state.PendingAnswerPrompt && pending.PaneID == row.PaneID {
		s.lastSubject = row.PaneID
		out := s.answerPending(ctx, pending, res.Answer, "Telling "+alias+" %s. Sent.")
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	out, _ := s.sendInstruction(ctx, row, res.InstructionText, res.Echo)
	return out, nil
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
		s.briefing = ""
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
	sum, err := s.llm.SummarizeBlock(ctx, read.Text, alias)
	if err != nil {
		return voice.ToolResult{Spoken: alias + " is waiting at a prompt first; answer that before a new instruction.", Scheduling: voice.UrgencyInterrupt}
	}
	s.recordBlock(row.PaneID, sum)
	if !sum.IsQuestion {
		return voice.ToolResult{Spoken: alias + " is waiting at a prompt first. " + sum.Spoken, Scheduling: voice.UrgencyInterrupt}
	}
	s.mu.Lock()
	spoken := s.setPending(s.newAnswerPrompt(row.PaneID, sum))
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
	return s.answerPending(ctx, p, answer, "Told "+s.aliasOf(p.PaneID)+" %s."), nil
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
// format receives the spoken label ("yes", "option 2"). Caller holds s.mu.
func (s *Supervisor) answerPending(ctx context.Context, p state.PendingAction, answer, format string) voice.ToolResult {
	alias := s.aliasOf(p.PaneID)
	keys, label, err := mapAnswer(p, answer)
	if err != nil {
		spoken := fmt.Sprintf("I do not know how to answer %q for %s.", strings.TrimSpace(answer), alias)
		if len(p.Options) > 0 {
			spoken += " The options are " + optionList(p.Options) + "."
		}
		return voice.ToolResult{Spoken: spoken, Clarify: true, Scheduling: voice.UrgencyInterrupt}
	}
	return s.answerWithKeys(ctx, p, keys, label, format)
}

// answerWithKeys sends keys to the pending prompt's pane and clears the
// slot (pending.confirmed). Caller holds s.mu.
func (s *Supervisor) answerWithKeys(ctx context.Context, p state.PendingAction, keys []string, label, format string) voice.ToolResult {
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
	s.briefing = ""
	s.lastSubject = p.PaneID
	s.log.Log(logSource, "pending.confirmed", p.PaneID, map[string]any{"id": p.ID, "answer": label, "keys": keys})
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
	if err := s.appendNote(text); err != nil {
		s.log.Log(logSource, "supervisor.note_error", "", map[string]any{"error": err.Error()})
		return voice.ToolResult{Spoken: "I could not save that note.", Scheduling: voice.UrgencyInterrupt}, nil
	}
	return voice.ToolResult{Spoken: "Noted.", Scheduling: voice.UrgencyInterrupt}, nil
}

// agentSummaries renders the state table for resolve_instruction.
func (s *Supervisor) agentSummaries() []llm.AgentSummary {
	pending, _ := s.store.Pending()
	rows := s.store.List()
	out := make([]llm.AgentSummary, 0, len(rows))
	for _, a := range rows {
		sum := llm.AgentSummary{Alias: a.Alias, PaneID: a.PaneID, Kind: a.Kind, State: string(a.State), Task: a.CurrentTask}
		if a.State == herdr.StateBlocked {
			if pending.Kind == state.PendingAnswerPrompt && pending.PaneID == a.PaneID && pending.Question != "" {
				sum.Question = pending.Question
			} else if a.LastBlock != nil && a.LastBlock.Question != "" {
				sum.Question = a.LastBlock.Question
			} else {
				sum.Question = "something at a prompt"
			}
		}
		out = append(out, sum)
	}
	return out
}

// target picks the agent for ask: the named alias, else the only agent,
// else the one most recently discussed (the last push subject or tool
// target), else a clarify question. The ToolResult is what to say when no
// agent fits.
func (s *Supervisor) target(agent string) (state.Agent, voice.ToolResult, bool) {
	rows := s.store.List()
	if len(rows) == 0 {
		return state.Agent{}, voice.ToolResult{Spoken: "No agents are running.", Scheduling: voice.UrgencyInterrupt}, false
	}
	if agent = strings.TrimSpace(agent); agent != "" {
		if row, ok := findAlias(rows, agent); ok {
			return row, voice.ToolResult{}, true
		}
		if len(rows) > 1 {
			names := make([]string, 0, len(rows))
			for _, r := range rows {
				names = append(names, r.Alias)
			}
			return state.Agent{}, voice.ToolResult{Spoken: fmt.Sprintf("I do not know an agent called %s. The agents are %s.", agent, joinSpoken(names)), Clarify: true, Scheduling: voice.UrgencyInterrupt}, false
		}
	}
	if len(rows) == 1 {
		return rows[0], voice.ToolResult{}, true
	}
	s.mu.Lock()
	subject := s.lastSubject
	s.mu.Unlock()
	if row, ok := s.store.Get(subject); ok {
		return row, voice.ToolResult{}, true
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Alias)
	}
	choice := strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
	return state.Agent{}, voice.ToolResult{Spoken: "Which agent do you mean, " + choice + "?", Clarify: true, Scheduling: voice.UrgencyInterrupt}, false
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
