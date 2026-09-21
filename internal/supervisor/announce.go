package supervisor

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Push judgment (docs/roles.md "Pushes"): on a worker's block or finish
// the brain — when one is attached — runs one announce event turn with
// the daemon's own sentence, the pane read already taken, the promises
// the event keeps and its transcript, and decides *whether* the voice
// says anything (skip, silent) and *how urgently* (idle, interrupt); the
// voice still decides the moment (the push queue). Code keeps the
// ceiling the brain cannot lift: the chattiness level (with kept promises
// the one exception) and interrupt only for a due promise or a decision
// the shepherd must take.
//
// One line per event, never two: the event waits up to
// Config.AnnounceWait for the brain's line and speaks it alone; past the
// wait the code sentence is spoken and the brain's line, whenever it
// lands, is dropped (brain.push_late) — unless it says the shepherd must
// act and the block is still open, the one addition worth interrupting a
// silence for. A line about a block whose prompt is already gone is
// dropped too (brain.push_stale).

// announcement is one worker event the supervisor announces: the code
// sentence and what the brain gets.
type announcement struct {
	row state.Agent
	// kind is the policy class of the sentence (pushDoneInvolved,
	// pushQuestion, …); pushPromiseOnly for a working/idle event whose
	// sentence is spoken only as a kept promise.
	kind pushKind
	// event is the worker event (brain.PromiseEventDone, …).
	event string
	// alsoIdle: the finish landed on idle rather than done (the user
	// had the pane focused), so an open "idle" promise is kept by this
	// announcement too, alongside event and any_change.
	alsoIdle bool
	// sentence is the code sentence: the parsed prompt for a block,
	// "<alias> finished." for a finish.
	sentence string
	// screen is the pane read taken for the event, when one was.
	screen string
	// pending is the id of the answer_prompt this block created, when it
	// created one: a line about a prompt that is no longer open is
	// dropped.
	pending string
}

// turnResult is what the announce goroutine hands back.
type turnResult struct {
	rep brain.Reply
	err error
}

// announce keeps the promises the event fulfils and speaks the one line
// the event gets: the brain's when it lands within Config.AnnounceWait,
// else the code sentence.
func (s *Supervisor) announce(ctx context.Context, a announcement) {
	events := []string{a.event}
	if a.alsoIdle {
		events = append(events, brain.PromiseEventIdle)
	}
	kept := s.keepMatching(a.row.PaneID, events...)
	level := s.chattiness()
	if s.brain == nil {
		s.codePath(a, kept, level, "no_brain")
		return
	}
	if ceilingForbidsAll(level, a, kept) {
		// Nothing the brain could say would be spoken: no model call.
		s.logDecision(a, level, decision{via: "ceiling", requested: "", applied: brain.UrgencySkip, reason: "ceiling"}, brain.Reply{}, kept, false)
		s.recordFinish(a, doneResult)
		if a.kind != pushPromiseOnly {
			s.log.Log(logSource, "supervisor.push_quiet", a.row.PaneID, map[string]any{"text": a.sentence, "chattiness": string(level)})
		}
		return
	}
	infos := make([]brain.PromiseInfo, 0, len(kept))
	for _, p := range kept {
		infos = append(infos, promiseInfo(p))
	}
	req := brain.Request{Kind: brain.KindAnnounce, Trigger: brain.TriggerEvent, Agent: aliasOr(a.row.Alias), Event: a.event, Template: a.sentence, Screen: a.screen, Promises: infos}
	res := make(chan turnResult, 1)
	start := s.cfg.Now()
	go func() {
		rep, err := s.brain.Turn(ctx, req)
		res <- turnResult{rep: rep, err: err}
	}()
	var timer <-chan time.Time
	if s.cfg.AnnounceWait > 0 {
		timer = time.After(s.cfg.AnnounceWait)
	}
	select {
	case r := <-res:
		s.applyResult(a, kept, level, r)
	case <-timer:
		// The shepherd hears the news now; the line the brain is still
		// writing is too late to be this event's one line.
		s.codePath(a, kept, level, "wait")
		s.dropLate(a, level, <-res, start)
	}
}

// applyResult turns the brain's answer (or failure) into the event's one
// push and its logs.
func (s *Supervisor) applyResult(a announcement, kept []state.Promise, level Chattiness, r turnResult) {
	if r.err != nil {
		s.codePath(a, kept, level, fallbackReason(r.err))
		return
	}
	rep := r.rep
	spoken := spokenLine(rep.Spoken)
	if !s.pendingStillOpen(a) {
		// The prompt was answered or went away while the brain wrote
		// about it: whatever it says now is about something gone.
		s.logDecision(a, level, decision{via: "brain", requested: rep.Urgency, applied: brain.UrgencySkip, reason: "stale"}, rep, kept, true)
		s.log.Log(logSource, "brain.push_stale", a.row.PaneID, map[string]any{"turn": rep.Turn, "pending": a.pending, "spoken": spoken})
		return
	}
	requested := requestedUrgency(rep.Urgency, spoken)
	if len(kept) > 0 && (requested == brain.UrgencySkip || requested == brain.UrgencySilent) {
		// The brain may not silence a promise: the code sentence keeps it.
		s.logDecision(a, level, decision{via: "template", requested: requested, applied: brain.UrgencySkip, reason: "promise"}, rep, kept, true)
		s.codePath(a, kept, level, "promise")
		return
	}
	d := applyCeiling(level, a.kind, requested, ceilingInput{kept: len(kept) > 0, duePassed: s.duePassed(kept), needsUser: rep.NeedsUser})
	d.via = "brain"
	s.logDecision(a, level, d, rep, kept, true)
	// Whatever the ceiling does with it, the brain's line is what the
	// record keeps of the finish.
	s.recordFinish(a, spoken)
	s.recordAnnounceDetail(a, rep)
	if d.applied == brain.UrgencySkip {
		if d.reason == "ceiling" {
			s.log.Log(logSource, "supervisor.push_quiet", a.row.PaneID, map[string]any{"text": spoken, "chattiness": string(level)})
		}
		return
	}
	u := voiceUrgency(d.applied)
	s.logKept(kept, a.event, spoken, u, "brain")
	s.push(a.row.PaneID, spoken, u)
}

// dropLate handles the brain's line arriving after the code sentence was
// spoken: it is never a second line about the same event (brain.push_late)
// — except when it says the shepherd must act and the block is still
// open, which is the one thing the code sentence could not know.
func (s *Supervisor) dropLate(a announcement, level Chattiness, r turnResult, start time.Time) {
	dur := s.cfg.Now().Sub(start).Milliseconds()
	if r.err != nil {
		s.logDecision(a, level, decision{via: "template", requested: "", applied: brain.UrgencySkip, reason: fallbackReason(r.err), late: true, err: r.err}, brain.Reply{}, nil, false)
		return
	}
	open := s.pendingStillOpen(a)
	rep := r.rep
	spoken := spokenLine(rep.Spoken)
	if spoken != "" && open {
		// The record keeps the fuller line even when nobody hears it.
		s.recordFinish(a, spoken)
		s.recordAnnounceDetail(a, rep)
	}
	add := spoken != "" && rep.NeedsUser && open && a.pending != "" && level.allows(a.kind)
	s.logDecision(a, level, decision{via: "brain", requested: rep.Urgency, applied: appliedIf(add), reason: lateReason(open), late: true}, rep, nil, true)
	s.log.Log(logSource, "brain.push_late", a.row.PaneID, map[string]any{"turn": rep.Turn, "dur_ms": dur, "spoken": spoken, "needs_user": rep.NeedsUser, "added": add})
	if add {
		s.push(a.row.PaneID, spoken, voice.UrgencyIdle)
	}
}

// appliedIf renders the late line's outcome for brain.push_decision.
func appliedIf(added bool) string {
	if added {
		return brain.UrgencyIdle
	}
	return brain.UrgencySkip
}

// lateReason names why a late line was dropped (or kept).
func lateReason(pendingOpen bool) string {
	if !pendingOpen {
		return "stale"
	}
	return "late"
}

// fallbackReason names why a turn produced no reply.
func fallbackReason(err error) string {
	switch {
	case errors.Is(err, brain.ErrYielded):
		return "yielded"
	case errors.Is(err, brain.ErrCoalesced):
		return "coalesced"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "fallback"
	}
}

// pendingStillOpen reports whether the answer_prompt a block created is
// still the pending one; true for anything but a block.
func (s *Supervisor) pendingStillOpen(a announcement) bool {
	if a.pending == "" {
		return true
	}
	p, has := s.store.Pending()
	return has && p.ID == a.pending
}

// codePath speaks the daemon's own sentence for the event — the parsed
// prompt for a block, "<alias> finished." for a finish — when the level
// allows it, or as "As you asked: <sentence>" for every kept promise at
// every level (idle, or interrupt past a due time). It is the fallback
// whenever the brain is off, late, or could not answer.
func (s *Supervisor) codePath(a announcement, kept []state.Promise, level Chattiness, reason string) {
	s.recordFinish(a, doneResult)
	if s.brain != nil && reason != "promise" {
		applied := brain.UrgencySkip
		if level.allows(a.kind) {
			applied = brain.UrgencyIdle
		}
		s.logDecision(a, level, decision{via: "template", requested: "", applied: applied, reason: reason}, brain.Reply{}, kept, false)
	}
	if len(kept) == 0 {
		if a.kind != pushPromiseOnly {
			s.pushIf(a.kind, a.row.PaneID, a.sentence)
		}
		return
	}
	line := "As you asked: " + a.sentence
	u := voice.UrgencyIdle
	if s.duePassed(kept) {
		u = voice.UrgencyInterrupt
	}
	s.logKept(kept, a.event, line, u, "template")
	s.push(a.row.PaneID, line, u)
}

// recordAnnounceDetail stores an announce turn's detail alongside the
// finish it phrased (for the ledger and recall).
func (s *Supervisor) recordAnnounceDetail(a announcement, rep brain.Reply) {
	if a.event != brain.PromiseEventDone {
		return
	}
	if detail := strings.TrimSpace(prose.Spoken(rep.Detail, 0)); detail != "" {
		s.recordDetail(a.row.PaneID, detail)
	}
}

// recordFinish stores what was announced about a finish as the agent's
// last result (docs/roles.md: "last_result is the brain's finish line");
// with no line — the brain was off, late or silent — the record says
// only that the agent finished.
func (s *Supervisor) recordFinish(a announcement, spoken string) {
	if a.event != brain.PromiseEventDone {
		return
	}
	if spoken == "" {
		spoken = doneResult
	}
	s.recordResult(a.row.PaneID, spoken)
}

// decision is one push judgment as logged (brain.push_decision).
type decision struct {
	via       string // brain | template | ceiling (skipped before any turn)
	requested string // what the brain asked for ("" on the template path)
	applied   string // what was done: interrupt, idle, silent, skip
	reason    string // brain, ceiling, duplicate, promise, promise_due, wait, timeout, fallback, yielded, coalesced
	late      bool   // the template was spoken before the brain answered
	err       error
}

// logDecision emits brain.push_decision.
func (s *Supervisor) logDecision(a announcement, level Chattiness, d decision, rep brain.Reply, kept []state.Promise, withSpoken bool) {
	payload := map[string]any{"via": d.via, "urgency_requested": d.requested, "urgency_applied": d.applied, "ceiling": string(level), "reason": d.reason, "agent": aliasOr(a.row.Alias), "event": a.event, "template": a.sentence, "late": d.late}
	if withSpoken {
		payload["spoken"] = spokenLine(rep.Spoken)
		payload["detail"] = strings.TrimSpace(prose.Spoken(rep.Detail, 0))
		payload["relates_to"] = strings.TrimSpace(prose.Spoken(rep.RelatesTo, 1))
		payload["needs_user"] = rep.NeedsUser
		if len(rep.KeepsPromiseIDs) > 0 {
			payload["keeps_promise_ids"] = rep.KeepsPromiseIDs
		}
	}
	if len(kept) > 0 {
		ids := make([]string, 0, len(kept))
		for _, p := range kept {
			ids = append(ids, p.ID)
		}
		payload["kept"] = ids
	}
	if d.err != nil {
		payload["error"] = d.err.Error()
	}
	s.log.Log(logSource, "brain.push_decision", a.row.PaneID, payload)
}

// ceilingInput is what the ceiling weighs besides the level and kind.
type ceilingInput struct {
	kept      bool // the announcement keeps a promise
	duePassed bool // a kept promise is past its due time
	needsUser bool // the brain says the CEO must decide or act
}

// applyCeiling caps what the brain asked for under the chattiness level:
// skip and silent pass as asked (context is not speech); a kept promise
// is spoken at every level, at idle, or interrupt when its due time
// passed or the brain says the CEO must act; otherwise the level's
// policy table decides whether the line is spoken at all (normal keeps
// the recent_minutes rule for finishes), and interrupt is allowed only
// for a decision the CEO must take.
func applyCeiling(level Chattiness, kind pushKind, requested string, in ceilingInput) decision {
	switch requested {
	case brain.UrgencySkip, brain.UrgencySilent:
		return decision{requested: requested, applied: requested, reason: "brain"}
	}
	d := decision{requested: requested, applied: brain.UrgencyIdle, reason: "brain"}
	if in.kept {
		switch {
		case requested == brain.UrgencyInterrupt && (in.duePassed || in.needsUser):
			d.applied = brain.UrgencyInterrupt
		case in.duePassed:
			d.applied, d.reason = brain.UrgencyInterrupt, "promise_due"
		case requested == brain.UrgencyInterrupt:
			d.reason = "ceiling"
		}
		return d
	}
	if !level.allows(kind) {
		d.applied, d.reason = brain.UrgencySkip, "ceiling"
		return d
	}
	if requested == brain.UrgencyInterrupt {
		if in.needsUser {
			d.applied = brain.UrgencyInterrupt
		} else {
			d.reason = "ceiling"
		}
	}
	return d
}

// ceilingForbidsAll reports that the level would silence every audible
// line about a: no promise is kept and the sentence's class is not
// spoken at this level.
func ceilingForbidsAll(level Chattiness, a announcement, kept []state.Promise) bool {
	return len(kept) == 0 && !level.allows(a.kind)
}

// requestedUrgency normalises the brain's urgency: an unknown or empty
// value is idle with a line and skip without one; silent without a line
// is skip.
func requestedUrgency(urgency, spoken string) string {
	u := strings.ToLower(strings.TrimSpace(urgency))
	switch u {
	case brain.UrgencyInterrupt, brain.UrgencyIdle, brain.UrgencySilent, brain.UrgencySkip:
	default:
		u = ""
	}
	if spoken == "" {
		return brain.UrgencySkip
	}
	if u == "" {
		return brain.UrgencyIdle
	}
	return u
}

// voiceUrgency maps an applied urgency onto the push queue's.
func voiceUrgency(applied string) voice.Urgency {
	switch applied {
	case brain.UrgencyInterrupt:
		return voice.UrgencyInterrupt
	case brain.UrgencySilent:
		return voice.UrgencySilent
	default:
		return voice.UrgencyIdle
	}
}

// duePassed reports whether any kept promise is past its due time.
func (s *Supervisor) duePassed(kept []state.Promise) bool {
	now := s.cfg.Now()
	for _, p := range kept {
		if p.Due != nil && !now.Before(*p.Due) {
			return true
		}
	}
	return false
}

// recordDetail stores the brain's detail on the row's last_result.
func (s *Supervisor) recordDetail(paneID, detail string) {
	_, err := s.store.Update(paneID, func(a *state.Agent) {
		if a.LastResult != nil {
			a.LastResult.Detail = detail
		}
	})
	if err != nil {
		s.log.Log(logSource, "supervisor.state_error", paneID, map[string]any{"error": err.Error()})
	}
	s.invalidateBriefing()
}
