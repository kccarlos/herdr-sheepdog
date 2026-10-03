package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
			// Meta goes along as a fact: the brain must know the
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
// brain's tool result already carries the note into the transcript.
func (h brainHost) Note(text string) (string, error) {
	if err := h.s.appendNote(text); err != nil {
		h.s.log.Log(logSource, "supervisor.note_error", "", map[string]any{"error": err.Error(), "via": "brain"})
		return "", err
	}
	return "Noted.", nil
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
// (docs/dive-deep.md WP8a). It reads the end of the worker's own session
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
	echo := "Telling " + aliasOr(row.Alias) + " to " + lowerFirst(strings.TrimRight(strings.TrimSpace(text), ".")) + "."
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
// the supervisor's lock — the shepherd's own recorded input must
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
		revived, ok := s.revivePrompt(ctx, paneID, "send_keys")
		if !ok {
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
	// The one rail reads the shepherd's own recorded words, never what the
	// model says they were (inputs.go).
	in, reason := s.railInputLocked(p, keys)
	if reason != "" {
		return "", fmt.Errorf("%w: %s", brain.ErrNotPermitted, reason)
	}
	// A tool result: a fact for the brain, which phrases the reply.
	alias := s.aliasOf(paneID)
	res, sent := s.answerKeys(ctx, p, keys, keysLabel(p, keys), func(l string) string { return toldLine(alias, l, false) }, "brain", nil)
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
// dropped, the next "done" treated as interrupted (like cancel).
func (h brainHost) Interrupt(ctx context.Context, paneID string) (string, error) {
	s := h.s
	s.mu.Lock()
	defer s.mu.Unlock()
	alias := s.aliasOf(paneID)
	if err := s.herdr.Interrupt(ctx, paneID); err != nil {
		s.log.Log(logSource, "supervisor.cancel_error", paneID, map[string]any{"error": err.Error(), "via": "brain"})
		return "", fmt.Errorf("could not interrupt %s: %w", alias, err)
	}
	s.interrupted[paneID] = s.cfg.Now()
	s.dropPendingFor(paneID, "brain_interrupt")
	s.lastSubject = paneID
	return "Interrupted " + alias + ".", nil
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

// toolResultFrom enforces the ear contract and D9 on a brain reply: the
// headline cut to caps (prose.EarRunaway for every line: a runaway guard
// of about 45 s of speech, the prompt decides the length; no dashes, PR
// numbers or hashes),
// the ask appended as the last
// sentence, whatever the cap cut moved to the end of detail (nothing the
// brain wrote is lost, it is just not read aloud), detail and options
// scrubbed; it interrupts only when the CEO must act. An utterance is
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
	res := voice.ToolResult{Spoken: spoken, Detail: detail, Clarify: rep.Clarify, Pending: rep.Pending, Scheduling: voice.UrgencyIdle}
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

// earLine is a brain reply's line for the ear: the headline cut to the
// ear caps, then the reply's ask as the last sentence (prose.EarLine),
// terminated; overflow is what the cap cut. Every brain line bound for
// the voice goes through it: replies, announcements and deliveries.
func earLine(rep brain.Reply) (spoken, overflow string) {
	spoken, overflow = prose.EarLine(rep.Spoken, rep.Ask)
	return sentence(spoken), overflow
}

// sentence makes sure s ends with terminal punctuation: prose's
// CJK-aware terminators, so a Chinese line ending in 。 or ？ gets no
// extra "." (docs/eval-campaign-2026-10-03.md H1: "。." / "？." seams,
// the old check looked at the last byte and knew only ASCII).
func sentence(s string) string { return prose.Terminate(s) }
