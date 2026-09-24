package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Promised work (docs/roles.md "Promises"): "tell me when it's done" is
// recorded by the brain's promise tool (brainHost.Promise → createPromise),
// persisted in the state file's session so a restart keeps it, and kept
// at the matching worker event by the announcement (announce.go) with a
// line that bypasses the chattiness ceiling — a promise is the user's
// explicit request. Expiry (Config.PromiseTTL) and cancel are the only
// other ways out.

// createPromise records an open promise for paneID and logs
// promise.created. turn is the brain turn that made it (0 outside one).
func (s *Supervisor) createPromise(paneID string, req brain.PromiseRequest) (state.Promise, error) {
	row, ok := s.store.Get(paneID)
	if !ok {
		return state.Promise{}, errors.New("that agent is gone")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	p := state.Promise{ID: s.nextID("pr"), PaneID: paneID, Alias: row.Alias, Text: req.Text, Event: req.Event, Status: state.PromiseOpen, CreatedTurn: req.Turn, CreatedAt: now, Due: req.Due}
	if err := s.store.UpdateSession(func(ss *state.Session) { ss.Promises = append(ss.Promises, p) }); err != nil {
		return state.Promise{}, err
	}
	s.invalidateBriefingLocked()
	payload := map[string]any{"id": p.ID, "text": p.Text, "agent": aliasOr(p.Alias), "event": p.Event, "turn": p.CreatedTurn}
	if p.Due != nil {
		payload["due"] = *p.Due
	}
	s.log.Log(logSource, "promise.created", paneID, payload)
	return p, nil
}

// promises returns the session's promises, open ones first (then the
// rest, oldest first).
func (s *Supervisor) promises() []state.Promise {
	all := s.store.Snapshot().Session.Promises
	out := make([]state.Promise, 0, len(all))
	for _, p := range all {
		if p.Status == state.PromiseOpen {
			out = append(out, p)
		}
	}
	for _, p := range all {
		if p.Status != state.PromiseOpen {
			out = append(out, p)
		}
	}
	return out
}

// openPromises returns the open promises, oldest first.
func (s *Supervisor) openPromises() []state.Promise {
	var out []state.Promise
	for _, p := range s.store.Snapshot().Session.Promises {
		if p.Status == state.PromiseOpen {
			out = append(out, p)
		}
	}
	return out
}

// resolvePromise moves promise id to status (kept, expired, cancelled),
// tells the brain, and returns the updated record; ok is false when it
// is not open any more. Caller holds s.mu.
func (s *Supervisor) resolvePromise(id, status string) (state.Promise, bool) {
	var out state.Promise
	ok := false
	now := s.cfg.Now()
	if err := s.store.UpdateSession(func(ss *state.Session) {
		for i := range ss.Promises {
			p := &ss.Promises[i]
			if p.ID == id && p.Status == state.PromiseOpen {
				p.Status, p.ResolvedAt = status, &now
				out, ok = *p, true
			}
		}
	}); err != nil {
		s.log.Log(logSource, "supervisor.state_error", "", map[string]any{"error": err.Error()})
	}
	if ok {
		s.invalidateBriefingLocked()
		if s.brain != nil {
			s.brain.PromiseEvent(promiseInfo(out))
		}
	}
	return out, ok
}

// promiseMatches reports whether an open promise waits for any of
// events (any_change always matches).
func promiseMatches(p state.Promise, paneID string, events ...string) bool {
	if p.Status != state.PromiseOpen || p.PaneID != paneID {
		return false
	}
	if p.Event == brain.PromiseEventAnyChange {
		return true
	}
	for _, e := range events {
		if p.Event == e {
			return true
		}
	}
	return false
}

// keepPromises keeps the open promises of row that wait for event: a
// working change (never a finish) or an idle change that is not itself
// a finish (done -> idle, blocked -> idle; working -> idle goes through
// onDone instead, which keeps an "idle" promise as part of its own
// announce call). When any matches, the event is announced with
// sentence as a promise-only announcement — one line through the
// brain, or "As you asked: <sentence>" — at every chattiness level.
// Blocks and finishes keep their promises inside their own announce
// call.
func (s *Supervisor) keepPromises(ctx context.Context, row state.Agent, event, sentence string) {
	for _, p := range s.openPromises() {
		if promiseMatches(p, row.PaneID, event) {
			s.announce(ctx, announcement{row: row, kind: pushPromiseOnly, event: event, sentence: sentence})
			return
		}
	}
}

// keepMatching marks every open promise of paneID that waits for any of
// events as kept (the brain sees the record before the announce turn)
// and returns them, oldest first. Marking first means nothing is said
// twice.
func (s *Supervisor) keepMatching(paneID string, events ...string) []state.Promise {
	var kept []state.Promise
	for _, p := range s.openPromises() {
		if !promiseMatches(p, paneID, events...) {
			continue
		}
		s.mu.Lock()
		k, ok := s.resolvePromise(p.ID, state.PromiseKept)
		s.mu.Unlock()
		if ok {
			kept = append(kept, k)
		}
	}
	return kept
}

// logKept emits promise.kept for every promise the spoken line keeps.
func (s *Supervisor) logKept(kept []state.Promise, event, spoken string, u voice.Urgency, via string) {
	for _, p := range kept {
		s.log.Log(logSource, "promise.kept", p.PaneID, map[string]any{"id": p.ID, "text": p.Text, "agent": aliasOr(p.Alias), "event": event, "spoken": spoken, "urgency": u.String(), "via": via, "chattiness": string(s.chattiness())})
	}
}

// ExpirePromises expires open promises older than Config.PromiseTTL
// (promise.expired, no speech). Run calls it on every tick.
func (s *Supervisor) ExpirePromises() {
	now := s.cfg.Now()
	for _, p := range s.openPromises() {
		if now.Sub(p.CreatedAt) < s.cfg.PromiseTTL {
			continue
		}
		s.mu.Lock()
		expired, ok := s.resolvePromise(p.ID, state.PromiseExpired)
		s.mu.Unlock()
		if ok {
			s.log.Log(logSource, "promise.expired", expired.PaneID, map[string]any{"id": expired.ID, "text": expired.Text, "agent": aliasOr(expired.Alias), "event": expired.Event, "age_s": int(now.Sub(expired.CreatedAt).Seconds())})
		}
	}
}

// restorePromises puts the open promises of an earlier run into the
// brain's transcript (which is per run) so they have a card and recall
// finds them. Called once from New.
func (s *Supervisor) restorePromises() {
	if s.brain == nil {
		return
	}
	for _, p := range s.openPromises() {
		s.brain.PromiseEvent(promiseInfo(p))
	}
}

// promiseInfo converts a record for the brain.
func promiseInfo(p state.Promise) brain.PromiseInfo {
	return brain.PromiseInfo{ID: p.ID, Text: p.Text, Agent: p.Alias, Event: p.Event, Status: p.Status, CreatedTurn: p.CreatedTurn, CreatedAt: p.CreatedAt, Due: p.Due, CarriedOver: p.CarriedOver}
}

// promiseTrailer is the briefing's reminder of open promises: "You asked
// me to tell you when auth finishes." (cache-only: no model). A promise
// carried over from an earlier run was already announced once at
// startup, so it is left out here (announceStartupPromises); the brain
// still sees it in the ledger section, noted "(from an earlier run)".
func promiseTrailer(open []state.Promise) string {
	seen := map[string]bool{}
	var parts []string
	for _, p := range open {
		if p.CarriedOver {
			continue
		}
		c := promiseCondition(p)
		if seen[c] {
			continue
		}
		seen[c] = true
		parts = append(parts, c)
	}
	if len(parts) == 0 {
		return ""
	}
	return "You asked me to tell you " + joinSpoken(parts) + "."
}

// announceStartupPromises speaks, once per daemon run, the open promises
// an earlier run left behind (docs/history/roles-status.md,
// docs/history/issues-2026-09-18.md "Smaller items"): this run's transcript has
// no memory of making them, so the brain's announce turns cannot be
// trusted to bring them up, and the shepherd is still owed an answer.
// Called once, the first time SetVoiceConnected(true) fires (a live
// connect, or at once for --voice=off). No model is involved: the
// sentence is composed in code, like every other fallback the daemon can
// say on its own. Marked carried_over (promise.carried_over) so the
// briefing trailer never repeats them and they are never announced
// again, even across another restart.
func (s *Supervisor) announceStartupPromises() {
	var carried []state.Promise
	for _, p := range s.openPromises() {
		if !p.CarriedOver && p.CreatedAt.Before(s.startedAt) {
			carried = append(carried, p)
		}
	}
	if len(carried) == 0 {
		return
	}
	ids := make([]string, 0, len(carried))
	for _, p := range carried {
		ids = append(ids, p.ID)
	}
	if err := s.store.UpdateSession(func(ss *state.Session) {
		for i := range ss.Promises {
			for _, id := range ids {
				if ss.Promises[i].ID == id {
					ss.Promises[i].CarriedOver = true
				}
			}
		}
	}); err != nil {
		s.log.Log(logSource, "supervisor.state_error", "", map[string]any{"error": err.Error(), "via": "carried_over"})
	}
	s.invalidateBriefing()
	s.log.Log(logSource, "promise.carried_over", "", map[string]any{"ids": ids})
	s.pushTagged("", startupPromiseSentence(carried), voice.UrgencyIdle, map[string]any{"startup_promises": true})
}

// startupPromiseSentence is announceStartupPromises' one line: "Still
// watching for: <text> (<alias>), <text> (<alias>), and <text>
// (<alias>), and N more." — up to three named, the rest tallied. promises
// is oldest first (openPromises).
func startupPromiseSentence(promises []state.Promise) string {
	n := len(promises)
	if n > briefingListMax {
		n = briefingListMax
	}
	parts := make([]string, 0, n)
	for _, p := range promises[:n] {
		parts = append(parts, p.Text+" ("+aliasOr(p.Alias)+")")
	}
	sentence := "Still watching for: " + joinSpoken(parts)
	if rest := len(promises) - n; rest > 0 {
		sentence += fmt.Sprintf(", and %d more", rest)
	}
	return sentence + "."
}

// promiseCondition is "when auth finishes" for the ear.
func promiseCondition(p state.Promise) string {
	alias := aliasOr(p.Alias)
	switch p.Event {
	case brain.PromiseEventBlocked:
		return "when " + alias + " asks something"
	case brain.PromiseEventIdle:
		return "when " + alias + " goes idle"
	case brain.PromiseEventAnyChange:
		return "when " + alias + " changes state"
	default:
		return "when " + alias + " finishes"
	}
}

// spokenLine applies D9 to a brain line for the ear: two sentences at
// most, scrubbed, terminated.
func spokenLine(text string) string {
	return sentence(strings.TrimSpace(prose.Spoken(text, 2)))
}
