package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// Handler adapts the two voice tools to voice.ToolHandler. Unknown tools
// are an error; missing arguments are treated as empty strings.
func (s *Supervisor) Handler() voice.ToolHandler {
	return func(ctx context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		switch call.Name {
		case ToolToSupervisor:
			text, _ := call.Args["text"].(string)
			return s.ToSupervisor(withCallID(ctx, call.ID), text)
		case ToolStop:
			return s.Stop(ctx, StopTriggerTool)
		default:
			return voice.ToolResult{}, fmt.Errorf("supervisor: unknown tool %q", call.Name)
		}
	}
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
const noBrainTrailer = "The brain is off, so I can only report status."

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
	spoken := s.setPending(s.newAnswerPrompt(row.PaneID, prompt, alias, row.StateSince))
	s.mu.Unlock()
	return voice.ToolResult{Spoken: spoken + " Answer that first.", Clarify: true, Scheduling: voice.UrgencyInterrupt}
}

// answerKeys sends keys to the pending prompt's pane, clears the slot
// (pending.confirmed {via}, with extra fields such as a tap's option) and
// starts the post-answer poll (reprompt.go): herdr may already be showing
// the next prompt within its own poll window with no state change in
// between. ok reports whether the keys were sent. Caller holds s.mu.
func (s *Supervisor) answerKeys(ctx context.Context, p state.PendingAction, keys []string, label, format, via string, extra map[string]any) (voice.ToolResult, bool) {
	alias := s.aliasOf(p.PaneID)
	if err := s.herdr.SendKeys(ctx, p.PaneID, keys...); err != nil {
		s.log.Log(logSource, "supervisor.confirm_error", p.PaneID, map[string]any{"id": p.ID, "keys": keys, "error": err.Error()})
		return voice.ToolResult{Spoken: "I could not send that to " + alias + ".", Scheduling: voice.UrgencyInterrupt}, false
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
	// Answered: the next prompt on this pane appeared after now, even if
	// it reads the same (inputs.go's promptSinceLocked).
	delete(s.promptSeenAt, p.PaneID)
	s.invalidateBriefingLocked()
	s.lastSubject = p.PaneID
	confirmed := map[string]any{"id": p.ID, "answer": label, "keys": keys, "via": via}
	for k, v := range extra {
		confirmed[k] = v
	}
	s.log.Log(logSource, "pending.confirmed", p.PaneID, confirmed)
	s.startReprompt(p.PaneID)
	return voice.ToolResult{Spoken: fmt.Sprintf(format, label), Scheduling: voice.UrgencyInterrupt}, true
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
