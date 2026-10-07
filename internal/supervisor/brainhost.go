package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

// brainHost implements brain.Host over the Supervisor: the roster and the
// ledger come from the state table, reads go through herdr and the CLI
// query tool, and the actions reuse the daemon's own send and answer
// paths so the pending-action bookkeeping (and the stop procedure) keep
// working.
type brainHost struct{ s *Supervisor }

var _ brain.Host = brainHost{}

// Agents implements brain.Host.
func (h brainHost) Agents() []brain.AgentInfo {
	pending, has := h.s.store.Pending()
	rows := h.s.store.List()
	out := make([]brain.AgentInfo, 0, len(rows))
	for _, a := range rows {
		out = append(out, h.info(a, pending, has))
	}
	return out
}

// info converts one row.
func (h brainHost) info(a state.Agent, pending state.PendingAction, has bool) brain.AgentInfo {
	info := brain.AgentInfo{PaneID: a.PaneID, Alias: a.Alias, Kind: a.Kind, State: string(a.State), StateSince: a.StateSince, Task: withoutSignOff(a.CurrentTask), LastTask: withoutSignOff(a.LastTask)}
	info.Spoken, info.Title, info.SpokenWas = state.Say(a), a.Title, h.s.spokenWasToMention(a)
	info.Journal = journalInfo(a.Journal)
	if a.LastResult != nil && a.LastResult.Spoken != "" {
		info.LastResult = a.LastResult.Spoken
		info.LastDetail = a.LastResult.Detail
	}
	if a.LastBlock != nil && a.LastBlock.Question != "" {
		info.LastBlock = a.LastBlock.Question
		if a.LastBlock.Answered {
			info.LastBlock += " (answered)"
		}
	}
	if a.LastInteraction != nil {
		info.LastInteraction = *a.LastInteraction
	}
	if has && pending.Kind == state.PendingAnswerPrompt && pending.PaneID == a.PaneID {
		p := &brain.PendingPrompt{ID: pending.ID, Question: pending.Question, AskedAt: pending.CreatedAt, YesKeys: pending.YesKeys, NoKeys: pending.NoKeys}
		for _, o := range pending.Options {
			// Meta goes along as a fact: the orchestrator must know the
			// free-text entry is not an answer (the phone card and
			// the yes/no mapping never offer it).
			p.Options = append(p.Options, brain.Option{Label: o.Label, Keys: o.Keys, Meta: o.Meta})
		}
		info.Pending = p
	}
	return info
}

// Resolve implements brain.Host.
func (h brainHost) Resolve(name string) (brain.AgentInfo, bool) {
	row, ok := findAlias(h.s.store.List(), name)
	if !ok {
		return brain.AgentInfo{}, false
	}
	pending, has := h.s.store.Pending()
	return h.info(row, pending, has), true
}

// LanguageLine implements brain.LanguageHost: the user's language
// setting for the state block.
func (h brainHost) LanguageLine() string { return h.s.LanguageLine() }

// Notes implements brain.Host.
func (h brainHost) Notes() []string {
	notes := h.s.store.Snapshot().Session.Notes
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, n.Text)
	}
	return out
}

// Note implements brain.Host: the session's notes and notes.jsonl. The
// orchestrator's tool result already carries the note into the transcript.
func (h brainHost) Note(text string) (string, error) {
	if err := h.s.appendNote(text); err != nil {
		h.s.log.Log(logSource, "supervisor.note_error", "", map[string]any{"error": err.Error(), "via": "brain"})
		return "", err
	}
	return "Noted.", nil
}

// Rename implements brain.Host (names.go).
func (h brainHost) Rename(ctx context.Context, paneID, name string) (string, error) {
	return h.s.rename(ctx, paneID, name)
}

// ReadScreen implements brain.Host. Like ask's screen read, it counts as
// dealing with the agent and makes it the current subject (the fallback
// path's "it").
func (h brainHost) ReadScreen(ctx context.Context, paneID string, lines int) (string, error) {
	if lines <= 0 {
		lines = h.s.cfg.ReadLines
	}
	read, err := h.s.herdr.Read(ctx, paneID, herdr.SourceRecentUnwrapped, lines)
	if err != nil {
		return "", err
	}
	h.s.touch(paneID)
	h.s.setSubject(paneID)
	return read.Text, nil
}

// ReadSession implements brain.Host: read_screen with source session
// (docs/history/dive-deep.md WP8a). It reads the end of the worker's own session
// transcript through the reader the journal uses, with workerlog's Tail,
// which leaves the journal's offset where it is. Like a screen read it
// counts as dealing with the agent. The instruction loses the sign-off;
// when the window holds none, the row's instruction stands in. A session
// that cannot be read is an error carrying the worker.session_missing
// reason, for the model to read.
func (h brainHost) ReadSession(_ context.Context, paneID string) (brain.SessionRead, error) {
	row, ok := h.s.store.Get(paneID)
	if !ok {
		return brain.SessionRead{}, errors.New("that agent is gone")
	}
	alias := aliasOr(row.Alias)
	d, err := h.s.cfg.WorkerLog.Tail(workerSession(row))
	if err != nil {
		reason := workerlog.Reason(err)
		return brain.SessionRead{}, fmt.Errorf("no session record for %s: %s (%s)", alias, reason, sessionMissingWhy[reason])
	}
	h.s.touch(paneID)
	h.s.setSubject(paneID)
	instruction := withoutSignOff(d.Instruction())
	if instruction == "" {
		instruction = withoutSignOff(row.CurrentTask)
	}
	if instruction == "" {
		instruction = withoutSignOff(row.LastTask)
	}
	return brain.SessionRead{LastAssistant: d.LastAssistant, Instruction: instruction, Files: d.Files, Tools: d.Tools}, nil
}

// sessionMissingWhy explains each worker.session_missing reason to the
// model.
var sessionMissingWhy = map[string]string{
	workerlog.ReasonNotReported: "herdr has not reported a session for this pane",
	workerlog.ReasonUnsupported: "this kind of agent keeps no session record sheepdog can read",
	workerlog.ReasonNotFound:    "its session record file was not found",
	workerlog.ReasonUnreadable:  "its session record could not be read",
}

// RunCLI implements brain.Host.
func (h brainHost) RunCLI(ctx context.Context, paneID, question string) (string, error) {
	if h.s.querier == nil {
		return "", brain.ErrNoCLI
	}
	row, ok := h.s.store.Get(paneID)
	if !ok {
		return "", errors.New("that agent is gone")
	}
	h.s.touch(paneID)
	h.s.setSubject(paneID)
	out, err := h.s.querier.Query(ctx, h.s.workspaceDir(ctx, row), question)
	if err != nil {
		h.s.log.Log(logSource, "supervisor.query_error", paneID, map[string]any{"error": err.Error()})
		return "", err
	}
	return out, nil
}

// Investigate implements brain.Host: the background job of
// investigate.go. It returns as soon as the job is recorded and running;
// the answer comes back later as its own event turn.
func (h brainHost) Investigate(ctx context.Context, paneID string, req brain.InvestigationRequest) (brain.InvestigationStart, error) {
	return h.s.startInvestigation(ctx, paneID, req)
}

// Investigations implements brain.Host: the ledger, oldest first.
func (h brainHost) Investigations() []brain.InvestigationInfo {
	all := h.s.investigations()
	out := make([]brain.InvestigationInfo, 0, len(all))
	for _, inv := range all {
		out = append(out, investigationInfo(inv, h.s.resumable(inv)))
	}
	return out
}

// Prompt implements brain.Host: the instruction send path (D4:
// sendInstruction), so the stop procedure can still interrupt it. The
// worker gets the instruction with the SignOff trailer (journal.go); the
// echo and the row's current task are the instruction alone.
func (h brainHost) Prompt(ctx context.Context, paneID, text string) (string, error) {
	row, ok := h.s.store.Get(paneID)
	if !ok {
		return "", errors.New("that agent is gone")
	}
	text = withoutSignOff(text)
	echo := "Telling " + aliasOr(state.Say(row)) + " to " + lowerFirst(strings.TrimRight(strings.TrimSpace(text), ".")) + "."
	h.s.journal.signOff.Do(func() { h.s.log.Log(logSource, "supervisor.signoff", "", map[string]any{"trailer": SignOff}) })
	res, err := h.s.sendInstruction(ctx, row, withSignOff(text), echo)
	if err == nil {
		h.s.unsignTask(paneID)
	}
	if err != nil {
		return "", fmt.Errorf("%s (%w)", strings.TrimRight(res.Spoken, "."), err)
	}
	return res.Spoken, nil
}

// AnswerPrompt implements brain.Host: the rail's atomic re-check under
// the supervisor's lock — the user's own recorded input must
// authorise these keys (inputs.go) — reviving the pending action from the screen
// first when none is recorded but the pane is still blocked
// (docs/history/issues-2026-09-18.md "an expired approval prompt can never be
// approved") — then the answer send path (answerKeys).
func (h brainHost) AnswerPrompt(ctx context.Context, paneID string, keys []string) (string, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p, has := s.store.Pending()
	if !has || p.Kind != state.PendingAnswerPrompt || p.PaneID != paneID {
		revived, ok, why := s.revivePrompt(ctx, paneID, "send_keys")
		if !ok {
			if why != "" {
				return "", fmt.Errorf("%w: %s; nothing was sent", brain.ErrNotPermitted, why)
			}
			return "", fmt.Errorf("%w: %s has no pending approval prompt now", brain.ErrNotPermitted, s.aliasOf(paneID))
		}
		p = revived
	}
	allowed := allowedKeys(p)
	for _, k := range keys {
		if !allowed[k] {
			return "", fmt.Errorf("%w: key %q is not in the prompt's option set", brain.ErrNotPermitted, k)
		}
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("%w: no keys", brain.ErrNotPermitted)
	}
	if !selectedBy(p, keys) {
		return "", fmt.Errorf("%w: keys %v mix more than one option", brain.ErrNotPermitted, keys)
	}
	// The one rail reads the user's own recorded words, never what the
	// model says they were (inputs.go).
	in, reason := s.railInputLocked(p, keys)
	if reason != "" {
		return "", fmt.Errorf("%w: %s", brain.ErrNotPermitted, reason)
	}
	// A tool result: a fact for the orchestrator, which phrases the reply.
	// In the user's language, like the fast path and taps: when the
	// turn times out after sending, this result is what they hear
	// (docs/eval/eval-campaign-2026-10-03.md run C, H4).
	alias, zh := s.sayOf(paneID), s.ceoChineseLocked()
	res, sent := s.answerKeys(ctx, p, keys, keysLabel(p, keys), func(l string) string { return toldLine(alias, l, zh) }, "brain", nil)
	if sent {
		in.used = true
	}
	if res.Clarify {
		return "", errors.New(res.Spoken)
	}
	return res.Spoken, nil
}

// allowedKeys is the option set of a pending prompt.
func allowedKeys(p state.PendingAction) map[string]bool {
	out := map[string]bool{}
	for _, o := range p.Options {
		for _, k := range o.Keys {
			out[k] = true
		}
	}
	for _, k := range append(append([]string(nil), p.YesKeys...), p.NoKeys...) {
		out[k] = true
	}
	return out
}

// selectedBy reports whether keys, as a whole sequence, exactly matches
// one of p's options (or its yes/no shorthand): allowedKeys's per-key
// membership check is not enough on its own — "y" and "enter" can each
// belong to a different option, and sending both could answer this
// prompt and then confirm whatever the pane asks next.
func selectedBy(p state.PendingAction, keys []string) bool {
	same := func(a []string) bool {
		if len(a) != len(keys) {
			return false
		}
		for i := range a {
			if a[i] != keys[i] {
				return false
			}
		}
		return true
	}
	if same(p.YesKeys) || same(p.NoKeys) {
		return true
	}
	for _, o := range p.Options {
		if same(o.Keys) {
			return true
		}
	}
	return false
}

// keysLabel names the answer the keys select ("yes", "no", the option's
// label, or the keys themselves).
func keysLabel(p state.PendingAction, keys []string) string {
	joined := strings.Join(keys, " ")
	switch {
	case len(p.YesKeys) > 0 && strings.Join(p.YesKeys, " ") == joined:
		return "yes"
	case len(p.NoKeys) > 0 && strings.Join(p.NoKeys, " ") == joined:
		return "no"
	}
	for _, o := range p.Options {
		if strings.Join(o.Keys, " ") == joined {
			return o.Label
		}
	}
	return joined
}

// Interrupt implements brain.Host: Ctrl-C to the pane, its pending action
// dropped, the next "done" treated as interrupted (like cancel) — and then
// checked. Campaign D N5 (tier 3, t3-stop-queued-instruction): Claude Code
// was busy with an instruction queued behind its turn, one Ctrl-C left it
// working (it most likely cleared the queued message), and the result
// "Interrupted Claude." let the orchestrator tell the user it had stopped. The
// user asked for a stop, so finishing it is not a judgement: when the pane
// still works after interruptCheck and its screen still shows a running
// turn, a second Ctrl-C goes out. The result says what happened — stopped,
// stopped after the second, or still working after two — and a stop that
// did not take leaves the finish announced (no interrupted mark).
//
// The second Ctrl-C needs the screen's "… to interrupt" hint as well as
// herdr's working state: a CLI already back at its input takes a second
// Ctrl-C as the first half of "exit".
func (h brainHost) Interrupt(ctx context.Context, paneID string) (string, error) {
	s := h.s
	alias, err := h.sendInterrupt(ctx, paneID, true)
	if err != nil {
		return "", err
	}
	if !h.stillWorking(ctx, paneID) {
		return "Interrupted " + alias + "; it stopped.", nil
	}
	if ctx.Err() != nil {
		h.unmarkInterrupted(paneID)
		s.log.Log(logSource, "supervisor.interrupt_unconfirmed", paneID, map[string]any{"agent": alias, "why": "turn_ended"})
		return "Sent Ctrl-C to " + alias + "; the turn ran out of time before I could confirm it stopped.", nil
	}
	running, read := h.screenShowsRunningTurn(ctx, paneID)
	if !read {
		h.unmarkInterrupted(paneID)
		s.log.Log(logSource, "supervisor.interrupt_unconfirmed", paneID, map[string]any{"agent": alias, "why": "screen_unreadable"})
		return "Sent Ctrl-C to " + alias + "; herdr still reports it working, and I could not read its screen to confirm, so I sent no second one.", nil
	}
	if !running {
		s.log.Log(logSource, "supervisor.interrupt_unconfirmed", paneID, map[string]any{"agent": alias, "why": "no_running_turn"})
		return "Sent Ctrl-C to " + alias + "; herdr still reports it working, but its screen shows no running turn.", nil
	}
	s.log.Log(logSource, "supervisor.interrupt_retry", paneID, map[string]any{"agent": alias})
	if _, err := h.sendInterrupt(ctx, paneID, false); err != nil {
		return "", fmt.Errorf("the first Ctrl-C did not stop %s, and the second could not be sent: %w", alias, err)
	}
	if !h.stillWorking(ctx, paneID) {
		return "The first Ctrl-C did not stop " + alias + "; a second one did, and it stopped.", nil
	}
	h.unmarkInterrupted(paneID)
	s.log.Log(logSource, "supervisor.interrupt_failed", paneID, map[string]any{"agent": alias, "ctrl_c": 2})
	return "Sent Ctrl-C to " + alias + " twice, and it is still working: it has not stopped.", nil
}

// sendInterrupt sends one Ctrl-C under the supervisor's lock and marks the
// pane interrupted before the lock is released, so the working -> idle
// transition it causes (another goroutine) is not announced as a finish.
// first also drops the pane's pending action and makes it the subject.
func (h brainHost) sendInterrupt(ctx context.Context, paneID string, first bool) (string, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	alias := s.sayOf(paneID)
	if err := s.herdr.Interrupt(ctx, paneID); err != nil {
		s.log.Log(logSource, "supervisor.cancel_error", paneID, map[string]any{"error": err.Error(), "via": "brain"})
		return alias, fmt.Errorf("could not interrupt %s: %w", alias, err)
	}
	s.interrupted[paneID] = s.cfg.Now()
	if first {
		s.dropPendingFor(paneID, "brain_interrupt")
		s.lastSubject = paneID
	}
	return alias, nil
}

// interruptCheckDefault is how long a Ctrl-C gets to take: herdr reported
// Claude idle 0.3 s after the Ctrl-C that did stop it (campaign D, tier 3).
const interruptCheckDefault = 2500 * time.Millisecond

// interruptPoll is how often the check looks.
const interruptPoll = 100 * time.Millisecond

// stillWorking waits up to Config.interruptCheck for the pane to leave
// working: the state table (herdr's socket events) or herdr's own list.
// It reports whether the pane was still working when the time ran out
// (or the turn's context ended first).
func (h brainHost) stillWorking(ctx context.Context, paneID string) bool {
	s := h.s
	deadline := time.Now().Add(s.cfg.interruptCheck)
	for {
		if !h.workingNow(ctx, paneID) {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(interruptPoll):
		}
	}
}

// workingNow reports whether the pane is working: the state table first,
// herdr's list when the table still says working.
func (h brainHost) workingNow(ctx context.Context, paneID string) bool {
	s := h.s
	row, ok := s.store.Get(paneID)
	if !ok || row.State != herdr.StateWorking {
		return false
	}
	agents, err := s.herdr.ListAgents(ctx)
	if err != nil {
		return true
	}
	for _, a := range agents {
		if a.PaneID == paneID {
			return a.Status == herdr.StateWorking
		}
	}
	return false
}

// screenShowsRunningTurn reports whether the pane's last lines carry the
// CLI's running-turn hint ("esc to interrupt": Claude Code and Codex);
// read is false when the screen could not be read.
func (h brainHost) screenShowsRunningTurn(ctx context.Context, paneID string) (running, read bool) {
	r, err := h.s.herdr.Read(ctx, paneID, herdr.SourceRecentUnwrapped, 12)
	if err != nil {
		return false, false
	}
	return strings.Contains(strings.ToLower(r.Text), "to interrupt"), true
}

// unmarkInterrupted drops the pane's interrupted mark: when the stop is
// not confirmed, the pane's next finish is announced rather than hidden.
func (h brainHost) unmarkInterrupted(paneID string) {
	h.s.mu.Lock()
	delete(h.s.interrupted, paneID)
	h.s.mu.Unlock()
}

// Promise implements brain.Host: the promise is recorded in the session
// (promise.created) and kept by keepPromises at the matching event.
func (h brainHost) Promise(_ context.Context, paneID string, req brain.PromiseRequest) (brain.PromiseInfo, error) {
	p, err := h.s.createPromise(paneID, req)
	if err != nil {
		return brain.PromiseInfo{}, err
	}
	return promiseInfo(p), nil
}

// Promises implements brain.Host: open promises first.
func (h brainHost) Promises() []brain.PromiseInfo {
	all := h.s.promises()
	out := make([]brain.PromiseInfo, 0, len(all))
	for _, p := range all {
		out = append(out, promiseInfo(p))
	}
	return out
}

// toolResultFrom enforces the ear contract and D9 on an orchestrator reply: the
// headline cut to caps (prose.EarRunaway for every line: a runaway guard
// of about 45 s of speech, the prompt decides the length; no dashes, PR
// numbers or hashes),
// the ask appended as the last
// sentence, whatever the cap cut moved to the end of detail (nothing the
// orchestrator wrote is lost, it is just not read aloud), detail and options
// scrubbed; it interrupts only when the user must act. An utterance is
// not classified, so it cannot claim an echo's urgency; the voice reads
// the result after its own "on it" either way (the tool is
// NON_BLOCKING).
func toolResultFrom(rep brain.Reply, caps prose.EarCaps) voice.ToolResult {
	spoken, overflow := prose.EarLineWith(caps, rep.Spoken, rep.Ask)
	spoken = sentence(spoken)
	detail := prose.Spoken(rep.Detail, 0)
	if overflow != "" {
		detail = strings.TrimSpace(detail + " " + overflow)
	}
	res := voice.ToolResult{Spoken: spoken, Detail: detail, Clarify: rep.Clarify, Pending: rep.Pending, Scheduling: voice.UrgencyIdle, NotVerified: rep.NotVerified, Agents: rep.Agents}
	for _, o := range rep.Options {
		if o = strings.TrimSpace(prose.Spoken(o, 1)); o != "" {
			res.Options = append(res.Options, o)
		}
	}
	if rep.NeedsUser || rep.Clarify {
		res.Scheduling = voice.UrgencyInterrupt
	}
	return res
}

// earLine is an orchestrator reply's line for the ear: the headline cut to the
// ear caps, then the reply's ask as the last sentence (prose.EarLine),
// terminated; overflow is what the cap cut. Every orchestrator line bound for
// the voice goes through it: replies, announcements and deliveries.
func earLine(rep brain.Reply) (spoken, overflow string) {
	spoken, overflow = prose.EarLine(rep.Spoken, rep.Ask)
	return sentence(spoken), overflow
}

// sentence makes sure s ends with terminal punctuation: prose's
// CJK-aware terminators, so a Chinese line ending in 。 or ？ gets no
// extra "." (docs/eval/eval-campaign-2026-10-03.md H1: "。." / "？." seams,
// the old check looked at the last byte and knew only ASCII).
func sentence(s string) string { return prose.Terminate(s) }
