package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The voice's two tools (docs/dev/roles.md "One decision-maker"):
// to_supervisor carries the user's words verbatim, stop runs the stop
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
			reading, _ := call.Args["reading"].(string)
			return s.ToSupervisorRead(withCallID(ctx, call.ID), text, reading)
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
// is sent to a worker — instructions are the orchestrator's judgment
// (docs/dev/roles.md "One decision-maker"). Within supervisor.repeat_window_s
// of an identical roster, the sentence is prefixed "No change since I
// last checked: …" — there is no orchestrator here to add a note to instead,
// and the roster is the whole answer, so it is delivered by saying it.
func (s *Supervisor) noBrain() voice.ToolResult {
	text, panes := s.briefingText()
	s.markMentioned(panes)
	s.noteNamesSaid(text)
	roster := text
	if _, repeat := s.briefingRepeat(text); repeat {
		text = "No change since I last checked: " + text
	}
	s.markBriefingDelivered(roster)
	return voice.ToolResult{Spoken: text + " " + noBrainTrailer, Scheduling: voice.UrgencyInterrupt}
}

// noBrainTrailer says why the roster is the whole answer.
const noBrainTrailer = "The orchestrator is off, so I can only report status."

// fallback is what an utterance gets when its orchestrator turn produced no
// answer: noBrain when there is no orchestrator at all, else
// brainFailedLine, with the facts behind it in the detail (never read
// aloud): why it failed (err, in plain words), that the user has heard
// nothing yet, and what a re-relay does (rerelay.go).
func (s *Supervisor) fallback(out utteranceOutcome) voice.ToolResult {
	if s.brain == nil {
		return s.noBrain()
	}
	if a, ok := s.brain.AccountAlertFor(out.err); ok {
		// An invalid key, a used-up quota or balance: "say it again"
		// would only fail the same way, so the answer is the problem and
		// its fix, naming the service (the plain-errors rail).
		return voice.ToolResult{Spoken: s.noticeText(a.Spoken), Detail: accountFailedDetail(out), Scheduling: voice.UrgencyInterrupt}
	}
	return voice.ToolResult{Spoken: brainFailedLine, Detail: brainFailedDetail(out), Scheduling: voice.UrgencyInterrupt}
}

// accountFailedDetail is the fallback's facts when the orchestrator's
// provider has an account problem: the spoken line already names it
// and the fix, and calling again does not help.
func accountFailedDetail(out utteranceOutcome) string {
	d := fmt.Sprintf("The orchestrator could not answer the user's words: %s. The line names the service and what the user can do about it. ", failureReason(out.err))
	if out.acted {
		return d + "It had already acted before it failed."
	}
	return d + "Nothing was done; calling to_supervisor again will fail the same way until the account is fixed."
}

// brainFailedDetail is the fallback's facts for the voice: why the turn
// failed, that the user has heard nothing yet, and what calling again
// does (2026-10-08 03:05:00: given only brainFailedLine, the voice said
// nothing and called again, and the call was ignored).
func brainFailedDetail(out utteranceOutcome) string {
	d := fmt.Sprintf("The orchestrator's turn on the user's words failed: %s. The user has heard nothing about it yet. ", failureReason(out.err))
	if out.acted {
		return d + "It had already acted, so it is not run again; it runs on the user's next words."
	}
	return d + "Nothing was done. Calling to_supervisor again, with no new words from the user, retries their words once."
}

// brainFailedLine is the whole answer when the orchestrator errored or timed
// out on an utterance. The code cannot tell what the words asked for —
// that is the orchestrator's work — so it does not answer a question it may not
// have been asked: no roster, no "no change" (2026-09-24 18:16:43, "I
// want to know the detail of the glitches" got "No change since I last
// checked: Four agents: …"), no "the orchestrator is off" (the user then
// asked "do you mean the orchestrator is off?"). One short sentence for the ear
// (prose.EarLine's caps) that says so and offers the retry. The offer is
// no yes/no question on purpose: a bare "yes" answers an open worker
// prompt on the fast path, so "Shall I try again?" could approve one.
//
// The retry offer is addressed to the user ("if you say it again"), not
// phrased as an imperative the voice could take for itself: on
// 2026-10-08 03:05:00 "…but ask me again and I'll try once more." in the
// result's facts was followed by the voice calling to_supervisor again
// instead of speaking (rerelay.go).
const brainFailedLine = "Sorry, I couldn't get an answer just now. If you say it again, I'll try once more."

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
	s.noteNamesSaid(spoken)
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
//
// Before anything is typed it reads the pane's screen (menuOnScreen):
// typed text ends with Enter, and on a menu that Enter picks the option
// under the cursor.
func (s *Supervisor) sendInstruction(ctx context.Context, row state.Agent, text, echo string) (voice.ToolResult, error) {
	alias := aliasOr(state.Say(row))
	if res, err, blocked := s.menuOnScreen(ctx, row); blocked {
		return res, err
	}
	s.mu.Lock()
	s.lastSubject = row.PaneID
	p := s.newSendInstruction(row.PaneID, text, echo)
	s.setPending(p)
	_, err := s.herdr.Prompt(ctx, row.PaneID, text)
	if errors.Is(err, herdr.ErrNoReply) {
		// The text may be typed already; the action's cancel has nothing to interrupt.
		s.clearPending(p, "send_no_reply")
	} else if err != nil {
		s.clearPending(p, "send_failed")
	} else {
		now := s.cfg.Now()
		if _, uerr := s.store.Update(row.PaneID, func(a *state.Agent) { a.CurrentTask, a.LastInteraction = text, &now }); uerr != nil {
			s.log.Log(logSource, "supervisor.state_error", row.PaneID, map[string]any{"error": uerr.Error()})
		}
		s.invalidateBriefingLocked()
	}
	s.mu.Unlock()

	if errors.Is(err, herdr.ErrNoReply) {
		// Written, but herdr's reply never came: the text may well have
		// arrived (live log 2026-10-27: it had, three times). Look at the
		// screen ourselves; duplicates are acceptable, a lost instruction
		// is not, so the model is told to send again if it is not there.
		s.log.Log(logSource, "supervisor.propose_error", row.PaneID, map[string]any{"stage": "send_no_reply", "error": err.Error()})
		return s.unconfirmedSend(ctx, row, text, alias), nil
	}
	if err != nil {
		if errors.Is(err, herdr.ErrAgentBlocked) {
			return s.reportBlocked(ctx, row), err
		}
		s.log.Log(logSource, "supervisor.propose_error", row.PaneID, map[string]any{"stage": "send", "error": err.Error()})
		return voice.ToolResult{Spoken: "I could not send that to " + alias + ".", Scheduling: voice.UrgencyInterrupt}, err
	}
	return voice.ToolResult{Spoken: echo + " Sent.", Scheduling: voice.UrgencyInterrupt}, nil
}

// unconfirmedSend words the outcome of a prompt that was written to herdr
// but never answered. It reads the pane and looks for the start of the
// instruction: on the screen means it arrived; otherwise it may still have.
func (s *Supervisor) unconfirmedSend(ctx context.Context, row state.Agent, text, alias string) voice.ToolResult {
	probe := strings.Join(strings.Fields(withoutSignOff(text)), " ")
	if len(probe) > 60 {
		probe = probe[:60]
	}
	if read, err := s.herdr.Read(ctx, row.PaneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines); err == nil && probe != "" &&
		strings.Contains(strings.Join(strings.Fields(read.Text), " "), probe) {
		return voice.ToolResult{Spoken: "Herdr did not confirm it, but the instruction is on " + alias + "'s screen, so it arrived. Do not send it again.", Scheduling: voice.UrgencyInterrupt}
	}
	return voice.ToolResult{Spoken: "Herdr did not confirm the send to " + alias + ", so the instruction may have arrived. Check its screen with read_screen; if the instruction is not there, send it again.", Scheduling: voice.UrgencyInterrupt}
}

// ErrMenuOnScreen is an instruction that was not typed because the
// pane's screen shows a menu or prompt (menuOnScreen).
var ErrMenuOnScreen = errors.New("menu on screen")

// menuOnScreen reads the pane before an instruction is typed. Tier-3
// bench 2026-10-03, finding 4: herdr reported Codex's "update available"
// menu as idle, the instruction was typed into it, and the Enter that
// ends every typed instruction chose "Update now": Codex ran its
// installer. Whatever a menu looks like — an approval prompt, a CLI's
// own update, "what's new" or model picker screen — ParsePrompt sees a
// cursor on one of two or more options at the end of the screen. Then
// nothing is typed. On a pane herdr reports blocked the menu becomes the
// pending question (as reportBlocked does after agent_blocked); on any
// other pane the fact goes back to the orchestrator, which decides what to tell
// the user (supervisor.instruction_blocked_by_menu). A screen that cannot
// be read does not stop the instruction: herdr's own agent_blocked
// refusal still stands behind it.
//
// Campaign D N6: right after the user declined a prompt, the old menu
// may still be drawn (the tier-1 sim kept it; a real CLI may not have
// redrawn yet), and the guard refused the follow-up instruction 20 times
// in 7 of 130 runs. So within declineRecheckWindow of a decline on the
// pane the screen is read once more after declineRecheckDelay; if the
// menu is gone the instruction goes, and if the declined menu is still
// there the orchestrator is told exactly that — it may be a stale picture —
// instead of a bare "showing a menu".
func (s *Supervisor) menuOnScreen(ctx context.Context, row state.Agent) (voice.ToolResult, error, bool) {
	prompt, ok := s.menuOf(ctx, row.PaneID)
	if !ok {
		return voice.ToolResult{}, nil, false
	}
	if s.blockedNow(ctx, row.PaneID) {
		return s.pendingFromPrompt(row, prompt), herdr.ErrAgentBlocked, true
	}
	s.mu.Lock()
	mark, declined := s.declined[row.PaneID]
	s.mu.Unlock()
	since := s.cfg.Now().Sub(mark.at)
	declined = declined && since <= declineRecheckWindow
	if declined {
		select {
		case <-ctx.Done():
		case <-time.After(declineRecheckDelay):
		}
		if prompt, ok = s.menuOf(ctx, row.PaneID); !ok {
			s.log.Log(logSource, "supervisor.menu_gone_after_decline", row.PaneID, map[string]any{"agent": aliasOr(row.Alias)})
			return voice.ToolResult{}, nil, false
		}
	}
	alias := aliasOr(state.Say(row))
	var labels []string
	for _, o := range prompt.Answers() {
		labels = append(labels, fmt.Sprintf("%q", o.Label))
	}
	fact := alias + " is showing a menu"
	if q := strings.TrimSpace(prompt.Question); q != "" {
		fact += ", " + fmt.Sprintf("%q", q)
	}
	fact += ", with the options " + strings.Join(labels, ", ") + "; nothing was typed."
	stale := declined && sameQuestion(prompt.Phrase(), mark.question)
	if stale {
		fact += fmt.Sprintf(" It is the menu the user declined %d s ago, still on screen after the decline was sent: it may be a stale picture that has not redrawn, and herdr reports %s %s, not blocked.", int(since.Seconds()), alias, row.State)
	}
	s.log.Log(logSource, "supervisor.instruction_blocked_by_menu", row.PaneID, map[string]any{
		"agent": aliasOr(row.Alias), "state": string(row.State), "question": prompt.Question, "options": labels, "after_decline": stale,
	})
	return voice.ToolResult{Spoken: fact, Scheduling: voice.UrgencyInterrupt}, ErrMenuOnScreen, true
}

// declineMark is a prompt the user declined: when, and its question
// (the pending action's, herdr.Prompt.Phrase).
type declineMark struct {
	at       time.Time
	question string
}

// declineRecheckWindow is how long after a decline the menu guard reads
// the screen twice; declineRecheckDelay is the wait between the reads
// (the sims redraw 300 ms after keys).
const (
	declineRecheckWindow = 30 * time.Second
	declineRecheckDelay  = 500 * time.Millisecond
)

// menuOf reads the pane and parses a menu at its end.
func (s *Supervisor) menuOf(ctx context.Context, paneID string) (herdr.Prompt, bool) {
	read, err := s.herdr.Read(ctx, paneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines)
	if err != nil {
		return herdr.Prompt{}, false
	}
	return herdr.ParsePrompt(read.Text)
}

// sameQuestion reports whether two prompt questions read the same
// (case and spacing aside); two empty questions count as the same.
func sameQuestion(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	return norm(a) == norm(b)
}

// blockedNow reports whether the pane is blocked: in the state table, or
// in herdr's own list when the table has not seen the block yet.
func (s *Supervisor) blockedNow(ctx context.Context, paneID string) bool {
	if cur, ok := s.store.Get(paneID); ok && cur.State == herdr.StateBlocked {
		return true
	}
	agents, err := s.herdr.ListAgents(ctx)
	if err != nil {
		return false
	}
	for _, a := range agents {
		if a.PaneID == paneID {
			return a.Status == herdr.StateBlocked
		}
	}
	return false
}

// reportBlocked handles agent.prompt being refused with agent_blocked: the
// agent is at a prompt we have not announced yet. It reads the screen and
// turns it into the pending question.
func (s *Supervisor) reportBlocked(ctx context.Context, row state.Agent) voice.ToolResult {
	alias := aliasOr(state.Say(row))
	read, err := s.herdr.Read(ctx, row.PaneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines)
	if err != nil {
		return voice.ToolResult{Spoken: alias + " is waiting at a prompt first, but I could not read it.", Scheduling: voice.UrgencyInterrupt}
	}
	prompt, ok := herdr.ParsePrompt(read.Text)
	if !ok {
		return voice.ToolResult{Spoken: alias + " is waiting at a prompt first; answer that before a new instruction.", Scheduling: voice.UrgencyInterrupt}
	}
	return s.pendingFromPrompt(row, prompt)
}

// pendingFromPrompt records the prompt on a blocked pane as the pending
// question and says it, asking for its answer first.
func (s *Supervisor) pendingFromPrompt(row state.Agent, prompt herdr.Prompt) voice.ToolResult {
	alias := aliasOr(state.Say(row))
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
func (s *Supervisor) answerKeys(ctx context.Context, p state.PendingAction, keys []string, label string, say func(label string) string, via string, extra map[string]any) (voice.ToolResult, bool) {
	alias := s.sayOf(p.PaneID)
	if err := s.herdr.SendKeys(ctx, p.PaneID, keys...); err != nil {
		s.log.Log(logSource, "supervisor.confirm_error", p.PaneID, map[string]any{"id": p.ID, "keys": keys, "error": err.Error()})
		return voice.ToolResult{Spoken: "I could not send that to " + alias + ".", Scheduling: voice.UrgencyInterrupt}, false
	}
	// The voice's slot is cleared only when it held this question: a
	// tap on a question the voice moved past leaves the voice on its own.
	if cur, has := s.store.Pending(); has && cur.ID == p.ID {
		if err := s.store.ClearPending(); err != nil {
			s.log.Log(logSource, "supervisor.state_error", p.PaneID, map[string]any{"error": err.Error()})
		}
	}
	s.dropQuestionLocked(p)
	now := s.cfg.Now()
	answerVia := "voice"
	if via == "tap" {
		answerVia = "tap"
	}
	if _, err := s.store.Update(p.PaneID, func(a *state.Agent) {
		if a.LastBlock != nil {
			a.LastBlock.Answered = true
			a.LastBlock.Answer = label
			a.LastBlock.Via = answerVia
		}
		a.LastInteraction = &now
	}); err != nil {
		s.log.Log(logSource, "supervisor.state_error", p.PaneID, map[string]any{"error": err.Error()})
	}
	// Answered: the next prompt on this pane appeared after now, even if
	// it reads the same (inputs.go's promptSinceLocked).
	delete(s.promptSeenAt, p.PaneID)
	if s.answeredPrompt == nil {
		s.answeredPrompt = map[string]string{}
	}
	s.answeredPrompt[p.PaneID] = p.ID
	if declines(p, keys) {
		if s.declined == nil {
			s.declined = map[string]declineMark{}
		}
		s.declined[p.PaneID] = declineMark{at: now, question: p.Question}
	}
	s.invalidateBriefingLocked()
	s.lastSubject = p.PaneID
	confirmed := map[string]any{"id": p.ID, "answer": label, "keys": keys, "via": via}
	for k, v := range extra {
		confirmed[k] = v
	}
	s.log.Log(logSource, "pending.confirmed", p.PaneID, confirmed)
	s.startReprompt(p.PaneID)
	return voice.ToolResult{Spoken: say(label), Scheduling: voice.UrgencyInterrupt}, true
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
	// The spoken name ("README Claude"), then the session title alone
	// ("the README one"), when exactly one agent has it.
	for _, key := range []func(state.Agent) string{state.Say, func(a state.Agent) string { return a.Title }} {
		var hit state.Agent
		hits := 0
		for _, r := range rows {
			if k := key(r); k != "" && (strings.EqualFold(k, strings.TrimSpace(name)) || (slug != "" && state.Slug(k) == slug)) {
				hit, hits = r, hits+1
			}
		}
		if hits == 1 {
			return hit, true
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
