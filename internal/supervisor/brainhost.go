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
)

// brainHost implements brain.Host over the Supervisor: the roster and the
// ledger come from the state table, reads go through herdr and the CLI
// query tool, and the actions reuse the propose / confirm / cancel paths
// so the pending-action bookkeeping (and cancel) keep working.
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
	info := brain.AgentInfo{PaneID: a.PaneID, Alias: a.Alias, Kind: a.Kind, State: string(a.State), StateSince: a.StateSince, Task: a.CurrentTask, LastTask: a.LastTask}
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
		p := &brain.PendingPrompt{ID: pending.ID, Question: pending.Question, YesKeys: pending.YesKeys, NoKeys: pending.NoKeys}
		for _, o := range pending.Options {
			p.Options = append(p.Options, brain.Option{Label: o.Label, Keys: o.Keys})
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

// Note implements brain.Host: the notes store the voice's own note tool
// used before D1-revised. The brain's tool result already carries the
// note into the transcript, so it is not ingested a second time.
func (h brainHost) Note(text string) (string, error) {
	if err := h.s.appendNote(text, false); err != nil {
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

// Prompt implements brain.Host: the propose send path (D4), so cancel can
// still interrupt it.
func (h brainHost) Prompt(ctx context.Context, paneID, text string) (string, error) {
	row, ok := h.s.store.Get(paneID)
	if !ok {
		return "", errors.New("that agent is gone")
	}
	echo := "Telling " + aliasOr(row.Alias) + " to " + lowerFirst(strings.TrimRight(strings.TrimSpace(text), ".")) + "."
	res, err := h.s.sendInstruction(ctx, row, text, echo)
	if err != nil {
		return "", fmt.Errorf("%s (%w)", strings.TrimRight(res.Spoken, "."), err)
	}
	return res.Spoken, nil
}

// AnswerPrompt implements brain.Host: the rail's atomic re-check under
// the supervisor's lock — reviving the pending action from the screen
// first when none is recorded but the pane is still blocked
// (docs/issues-2026-09-18.md "an expired approval prompt can never be
// approved") — then the confirm send path.
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
	res := s.answerWithKeys(ctx, p, keys, keysLabel(p, keys), "Told "+s.aliasOf(paneID)+" %s.", "brain")
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

// brainTurn routes one ask / propose through the brain. ok is false when
// the supervisor should answer from its template path instead.
func (s *Supervisor) brainTurn(ctx context.Context, kind brain.Kind, text, agent string) (voice.ToolResult, bool) {
	if s.brain == nil {
		return voice.ToolResult{}, false
	}
	rep, err := s.brain.Turn(ctx, brain.Request{Kind: kind, Trigger: brain.TriggerCEO, Question: text, Agent: agent})
	if err != nil {
		return voice.ToolResult{}, false
	}
	res := toolResultFrom(kind, rep)
	if res.Spoken == "" {
		return voice.ToolResult{}, false
	}
	return res, true
}

// toolResultFrom enforces D9 on a brain reply: spoken cut to two
// sentences and scrubbed, detail and options scrubbed; a propose always
// interrupts (it is the echo), an ask or an utterance only when the CEO
// must act. An utterance is not classified, so it cannot claim the echo
// urgency a propose had; the voice reads the result after its own "on
// it" either way (the tool is NON_BLOCKING).
func toolResultFrom(kind brain.Kind, rep brain.Reply) voice.ToolResult {
	res := voice.ToolResult{Spoken: sentence(prose.Spoken(rep.Spoken, 2)), Detail: prose.Spoken(rep.Detail, 0), Clarify: rep.Clarify, Pending: rep.Pending, Scheduling: voice.UrgencyIdle}
	for _, o := range rep.Options {
		if o = strings.TrimSpace(prose.Spoken(o, 1)); o != "" {
			res.Options = append(res.Options, o)
		}
	}
	if kind == brain.KindPropose || rep.NeedsUser || rep.Clarify {
		res.Scheduling = voice.UrgencyInterrupt
	}
	return res
}

// sentence makes sure s ends with terminal punctuation.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsRune(".!?\"')]", rune(s[len(s)-1])) {
		return s
	}
	return s + "."
}
