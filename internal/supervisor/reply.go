package supervisor

import (
	"context"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// replyPendingDetail is the silent pending result's detail: context for
// the voice model's follow-ups, never read aloud. The voice already said
// "on it" as it called the tool, so the result adds no speech of its own.
const replyPendingDetail = "Still working on it; the answer follows on its own."

// awaitReply runs the utterance turn detached and waits up to
// Config.ReplyWait for it (docs/dev/history/roles-status.md). A reply inside the
// wait is the tool result, exactly as a synchronous call's would be.
// Past it the call returns a silent pending result at once — the voice
// said "on it" as it called the tool, and a second acknowledgement would
// only repeat it — and the reply is pushed when it lands (deliverLate).
// A voice that drops the call meanwhile (the user barged in) gets
// the same treatment: its result would be discarded, so the reply goes
// the late way too.
func (s *Supervisor) awaitReply(ctx, turnCtx context.Context, u *inflightUtterance) voice.ToolResult {
	start := s.cfg.Now()
	if !s.track() {
		// The daemon is going down: no turn starts that would outlive it.
		s.endUtterance(u)
		return voice.ToolResult{Scheduling: voice.UrgencySilent}
	}
	go func() {
		defer s.replies.Done()
		out := s.utteranceTurn(turnCtx, u)
		s.mu.Lock()
		if !u.detached {
			u.out, u.done = out, true
			s.mu.Unlock()
			close(u.finished)
			return
		}
		s.mu.Unlock()
		s.deliverLate(turnCtx, u, out, start)
	}()
	timer := time.NewTimer(s.cfg.ReplyWait)
	defer timer.Stop()
	select {
	case <-u.finished:
	case <-timer.C:
	case <-ctx.Done():
	}
	listening := s.hasListener()
	s.mu.Lock()
	switch {
	case !u.done:
		u.detached = true
		s.mu.Unlock()
	case ctx.Err() != nil || !listening || s.acceptedSeq != u.accepted:
		// The reply landed just as the voice dropped the call: the tool
		// result would be discarded, so it is pushed instead. Likewise
		// when the page left during the wait: as a tool result the voice
		// would speak it to nobody at once, while a push is held until a
		// client attaches (voice.push_held, PR #64). And when a newer
		// request was accepted while the call waited (a long
		// brain.reply_wait_s): the reply is stale, and deliverLate drops
		// it, or pushes it silent if the turn acted, as for any late
		// reply.
		out := u.out
		stale := ctx.Err() == nil && listening && s.acceptedSeq != u.accepted
		s.mu.Unlock()
		if s.track() {
			go func() {
				defer s.replies.Done()
				s.deliverLate(turnCtx, u, out, start)
			}()
		} else {
			s.endUtterance(u)
		}
		if stale {
			// No reply follows for this call (deliverLate logs
			// brain.reply_stale): it is answered silently, not pending.
			return voice.ToolResult{Scheduling: voice.UrgencySilent}
		}
	default:
		out := u.out
		s.mu.Unlock()
		defer s.endUtterance(u)
		return s.inlineResult(turnCtx, u, out)
	}
	payload := map[string]any{"id": u.id, "utterance_id": u.id, "waited_ms": s.cfg.Now().Sub(start).Milliseconds(), "cancelled": ctx.Err() != nil, "no_listener": !listening}
	if id, ok := ctx.Value(callIDKey{}).(string); ok {
		payload["call_id"] = id
	}
	s.log.Log(logSource, "brain.reply_pending", "", payload)
	return voice.ToolResult{Detail: replyPendingDetail, Pending: true, Scheduling: voice.UrgencySilent}
}

// inlineResult turns a finished turn into the tool result: dropped when
// the stop procedure (or the voice) cancelled it, the reply when there
// is one, else the fallback (the roster with no orchestrator, else the line
// that says it could not answer).
func (s *Supervisor) inlineResult(turnCtx context.Context, u *inflightUtterance, out utteranceOutcome) voice.ToolResult {
	if s.utteranceCoalesced(u) {
		return coalescedResult()
	}
	if s.utteranceDropped(u) || turnCtx.Err() != nil {
		// Stopped, or the voice cancelled the call: the stop procedure
		// speaks, and the orchestrator's reply is discarded.
		return droppedResult()
	}
	s.noteOutcome(u, out) // rerelay.go
	if out.ok {
		// A reply that named an agent counts as dealing with it, like a
		// spoken briefing does (the normal level's chattiness rule).
		s.mentionedIn(out.res.Spoken)
		s.noteFocus(out.focus, u.text)
		s.answered(u, out) // unanswered.go
		return out.res
	}
	s.fellBack(u, out) // unanswered.go
	return s.fallback(out)
}

// deliverLate pushes the reply of a turn that outlived its tool call,
// tagged answers_utterance, and ends its slot. A stopped turn delivers
// nothing. A reply is stale once the user's next request was
// accepted (acceptedSeq moved): the user has moved on, so a turn
// that did nothing is dropped, and one that acted is pushed silent — the
// voice must still know what was done, but it is no longer the answer to
// what is being discussed. Otherwise the reply is spoken at the next
// pause, with its subject in front when it is about one agent, and a
// failed turn gives the fallback exactly as the inline path does.
func (s *Supervisor) deliverLate(turnCtx context.Context, u *inflightUtterance, out utteranceOutcome, start time.Time) {
	cancelled := turnCtx.Err() != nil
	s.mu.Lock()
	dropped := u.dropped || u.coalesced
	stale := s.acceptedSeq != u.accepted
	if s.utterance == u {
		s.utterance = nil
	}
	s.mu.Unlock()
	u.cancel()
	if dropped || cancelled {
		return
	}
	if stale {
		s.log.Log(logSource, "brain.reply_stale", "", map[string]any{"id": u.id, "utterance_id": u.id, "acted": out.acted})
		// Dropped unsaid: the user's words got no answer (unanswered.go).
		switch {
		case !out.ok:
			s.fellBack(u, out)
		case !out.acted:
			s.recordUnanswered(u.id, u.text, u.start, whyStale, "stale")
		}
		if !out.ok || !out.acted {
			return
		}
	}
	if !stale {
		s.noteOutcome(u, out) // rerelay.go
		if out.ok {
			s.answered(u, out) // unanswered.go
		} else {
			s.fellBack(u, out)
		}
	}
	var paneID, line string
	urgency := voice.UrgencyIdle
	if out.ok {
		paneID, line = s.lateLine(out.res, u.text, out.actedOn)
		if out.needsUser && s.answerPromptOpen() {
			// The user must decide on an open prompt: the reply may
			// ask for the floor, but it is not tagged needs_user — that
			// exception is a decision on a block the push itself brings
			// (announce.go's announceTags), so the voice still waits out
			// its own sentence (docs/dev/roles.md "Pushes").
			urgency = voice.UrgencyInterrupt
		}
	} else {
		line = s.fallback(out).Spoken
	}
	if stale {
		urgency = voice.UrgencySilent
	} else if out.ok {
		s.mentionedIn(out.res.Spoken)
	}
	tags := map[string]any{"answers_utterance": u.id, "utterance_id": u.id, "answers_text": u.text}
	if !u.start.IsZero() {
		tags["asked_at"] = u.start.Format("15:04:05")
	}
	if u.callID != "" {
		tags[tagCallID] = u.callID
	}
	if out.ok {
		// Facts the voice cannot see: checks that did not finish, and
		// the agents the answer is about (their state at delivery).
		if len(out.res.NotVerified) > 0 {
			tags[voice.TagNotVerified] = out.res.NotVerified
		}
		if len(out.res.Agents) > 0 {
			tags[voice.TagAgents] = out.res.Agents
		}
		if len(out.res.Shown) > 0 {
			tags[voice.TagOnScreen] = out.res.Shown
		}
	}
	s.pushTagged(paneID, line, urgency, tags)
	if out.ok && !stale {
		// A stale reply is context, not what is being discussed: it
		// does not move the focus.
		if paneID != "" {
			out.focus.paneID = paneID
		}
		s.noteFocus(out.focus, u.text)
	}
	s.log.Log(logSource, "brain.reply_delivered", paneID, map[string]any{
		"id": u.id, "utterance_id": u.id, "dur_ms": s.cfg.Now().Sub(start).Milliseconds(), "urgency": urgency.String(), "acted": out.acted, "fallback": !out.ok,
	})
}

// lateLine is the pushed form of a late reply: its subject in front when
// it is about one agent ("About auth: …") unless it already opens with
// that agent's name — heard out of the blue, it must say what it is
// about — and its options as a choice, as the voice would have read them
// from a tool result. The spoken text and options are already D9-scrubbed
// (toolResultFrom); detail stays out, since everything pushed is spoken.
// actedOn is the panes the turn acted on (subjectOf).
func (s *Supervisor) lateLine(res voice.ToolResult, asked string, actedOn []string) (paneID, line string) {
	line = res.Spoken
	if len(res.Options) > 0 {
		line += " Options: " + strings.Join(res.Options, "; ") + "."
	}
	row, ok := s.subjectOf(res.Spoken, asked, actedOn)
	if !ok {
		return "", line
	}
	alias := aliasOr(state.Say(row))
	if !strings.HasPrefix(strings.ToLower(res.Spoken), strings.ToLower(alias)) {
		line = "About " + alias + ": " + line
	}
	return row.PaneID, line
}

// subjectOf is the one agent a reply is about: the one agent the turn
// acted on, else the one it names, else — when it names none — the one
// the user's words named. Several means no single subject. Campaign D
// N4: the orchestrator told bench-codex to fix its bug and the reply mentioned
// only bench-claude ("we still have bench-claude going in the
// background"), so the user heard "About bench-claude: Sent. It is fixing
// the adding step now".
func (s *Supervisor) subjectOf(spoken, asked string, actedOn []string) (state.Agent, bool) {
	if len(actedOn) > 1 {
		return state.Agent{}, false
	}
	if len(actedOn) == 1 {
		if row, ok := s.store.Get(actedOn[0]); ok {
			return row, true
		}
	}
	for _, text := range []string{spoken, asked} {
		lower := strings.ToLower(text)
		var named []state.Agent
		for _, row := range s.store.List() {
			if row.Alias != "" && strings.Contains(lower, strings.ToLower(row.Alias)) {
				named = append(named, row)
			}
		}
		if len(named) > 0 {
			return named[0], len(named) == 1
		}
	}
	return state.Agent{}, false
}

// answerPromptOpen reports whether a worker's approval prompt is waiting
// for the user.
func (s *Supervisor) answerPromptOpen() bool {
	p, has := s.store.Pending()
	return has && p.Kind == state.PendingAnswerPrompt
}

// hasListener reports whether a line spoken now can be heard (Listener);
// true for a Pusher that cannot tell. Invariant it serves in awaitReply:
// a reply the user is owed is heard, or held until someone can hear
// it, never spoken into an empty room. The page can leave between the
// tool call and its result, so this cannot be made unrepresentable; it
// is checked when the result would be returned.
func (s *Supervisor) hasListener() bool {
	if l, ok := s.pusher.(Listener); ok {
		return l.HasListener()
	}
	return true
}
