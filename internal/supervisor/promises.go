package supervisor

import (
	"context"
	"errors"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Promised work (docs/roles.md "Promises"): "tell me when it's done" is
// recorded by the brain's promise tool (brainHost.Promise → createPromise),
// persisted in the state file's session so a restart keeps it, and kept
// here at the matching worker event with a push that bypasses the
// chattiness ceiling — a promise is the user's explicit request. Expiry
// (Config.PromiseTTL) and cancel are the only other ways out.

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
	s.briefing = ""
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
		s.briefing = ""
		if s.brain != nil {
			s.brain.PromiseEvent(promiseInfo(out))
		}
	}
	return out, ok
}

// promiseMatches reports whether an open promise waits for event.
func promiseMatches(p state.Promise, paneID, event string) bool {
	return p.Status == state.PromiseOpen && p.PaneID == paneID && (p.Event == event || p.Event == brain.PromiseEventAnyChange)
}

// keepPromises keeps every open promise of paneID that waits for event:
// one spoken line each — through the brain when one is attached (a
// keep_promise event turn with the promise and the fresh template
// sentence), else "As you asked: <sentence>" — pushed whatever the
// chattiness level, at idle urgency, or interrupt when the promise is
// past its due time. The promise is marked kept first (promise.kept), so
// nothing is said twice.
func (s *Supervisor) keepPromises(ctx context.Context, row state.Agent, event, sentence string) {
	for _, p := range s.openPromises() {
		if !promiseMatches(p, row.PaneID, event) {
			continue
		}
		s.mu.Lock()
		kept, ok := s.resolvePromise(p.ID, state.PromiseKept)
		s.mu.Unlock()
		if !ok {
			continue
		}
		spoken, via := "As you asked: "+sentence, "template"
		if s.brain != nil {
			info := promiseInfo(kept)
			rep, err := s.brain.Turn(ctx, brain.Request{Kind: brain.KindKeepPromise, Trigger: brain.TriggerEvent, Agent: aliasOr(row.Alias), Event: event, Template: sentence, Promise: &info})
			if err == nil {
				if line := spokenLine(rep.Spoken); line != "" {
					spoken, via = line, "brain"
				}
			}
		}
		urgency := voice.UrgencyIdle
		if kept.Due != nil && !s.cfg.Now().Before(*kept.Due) {
			urgency = voice.UrgencyInterrupt
		}
		s.log.Log(logSource, "promise.kept", row.PaneID, map[string]any{"id": kept.ID, "text": kept.Text, "agent": aliasOr(kept.Alias), "event": event, "spoken": spoken, "urgency": urgency.String(), "via": via, "chattiness": string(s.chattiness())})
		s.push(row.PaneID, spoken, urgency)
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

// cancelLatestPromise cancels the most recently made open promise
// (promise.cancelled); ok is false when none is open. Caller holds s.mu.
func (s *Supervisor) cancelLatestPromise() (state.Promise, bool) {
	open := s.openPromises()
	if len(open) == 0 {
		return state.Promise{}, false
	}
	p := open[len(open)-1]
	cancelled, ok := s.resolvePromise(p.ID, state.PromiseCancelled)
	if ok {
		s.log.Log(logSource, "promise.cancelled", cancelled.PaneID, map[string]any{"id": cancelled.ID, "text": cancelled.Text, "agent": aliasOr(cancelled.Alias), "event": cancelled.Event, "reason": "user"})
	}
	return cancelled, ok
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
	return brain.PromiseInfo{ID: p.ID, Text: p.Text, Agent: p.Alias, Event: p.Event, Status: p.Status, CreatedTurn: p.CreatedTurn, CreatedAt: p.CreatedAt, Due: p.Due}
}

// promiseTrailer is the briefing's reminder of open promises: "You asked
// me to tell you when auth finishes." (cache-only: no model).
func promiseTrailer(open []state.Promise) string {
	if len(open) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var parts []string
	for _, p := range open {
		c := promiseCondition(p)
		if seen[c] {
			continue
		}
		seen[c] = true
		parts = append(parts, c)
	}
	return "You asked me to tell you " + joinSpoken(parts) + "."
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
	return sentence(strings.TrimSpace(llm.Spoken(text, 2)))
}
