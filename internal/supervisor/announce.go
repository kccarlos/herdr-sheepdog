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
// One line per event, as a rule: the event waits up to
// Config.AnnounceWait for the brain's line and speaks it alone; past the
// wait the code sentence is spoken and the brain's line, whenever it
// lands, is dropped (brain.push_late) — with two exceptions, each at most
// one addition. A finish the shepherd is owed (it keeps a promise, or
// ends an instruction they gave this run; or the late line itself says
// which of their requests it fulfils, relates_to) is announced at the
// wait as "<alias> finished — here's what it found." and the brain's
// line with the finding follows whenever it lands, within the longer
// brain.announce_timeout_s (the 2026-09-23 run dropped the one answer
// the shepherd was waiting for). And a line that says the shepherd must
// act on a block that is still open. A line about a block whose prompt is
// already gone is dropped (brain.push_stale).

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
	// alsoBlocked: the finish left a prompt on screen
	// (announceFinishAsking), so an open "blocked" promise is kept too.
	alsoBlocked bool
	// owed: the finish ends an instruction the shepherd gave this run
	// (runningShepherdTask). Like a kept promise it makes the brain's
	// line worth waiting for: the code sentence says "here's what it
	// found" and the line is added whenever it lands.
	owed bool
	// asking is the question of a prompt the agent showed after its
	// turn, for a finish that also asks (announceFinishAsking).
	asking string
	// sentence is the code sentence: the parsed prompt for a block,
	// "<alias> finished." for a finish.
	sentence string
	// screen is the pane read taken for the event, when one was.
	screen string
	// pending is the id of the answer_prompt this block created, when it
	// created one: a line about a prompt that is no longer open is
	// dropped.
	pending string
	// eventID is the worker event's id (links.go), set by announce: the
	// turn's request and every push about the event carry it.
	eventID string
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
	a.eventID = s.eventIDOf(a.row.PaneID)
	events := []string{a.event}
	if a.alsoIdle {
		events = append(events, brain.PromiseEventIdle)
	}
	if a.alsoBlocked {
		events = append(events, brain.PromiseEventBlocked)
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
	req := brain.Request{Kind: brain.KindAnnounce, Trigger: brain.TriggerEvent, Agent: aliasOr(a.row.Alias), Event: a.event, Template: a.sentence, Screen: a.screen, Promises: infos, Owed: a.owed, AlsoAsking: a.asking, Link: eventLink(a.eventID)}
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
		s.dropLate(a, kept, level, <-res, start)
	}
}

// owedFinish reports whether a is a finish the shepherd is waiting on:
// it keeps a promise or ends their own instruction.
func owedFinish(a announcement, kept []state.Promise) bool {
	return a.event == brain.PromiseEventDone && (len(kept) > 0 || a.owed)
}

// foundSentence is the code sentence of an owed finish spoken before the
// brain's line lands: it promises the finding that line carries.
func foundSentence(a announcement) string {
	return aliasOr(a.row.Alias) + " finished — here's what it found."
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
	duePassed := s.duePassed(kept)
	d := applyCeiling(level, a.kind, requested, ceilingInput{kept: len(kept) > 0, duePassed: duePassed, needsUser: rep.NeedsUser})
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
	s.pushTagged(a.row.PaneID, spoken, u, a.tags(announceTags(u, duePassed, rep.NeedsUser, a.pending)))
}

// announceTags marks a push as one of the exceptions that keep INTERRUPT
// scheduling even while the model is speaking (docs/roles.md "Pushes"):
// a kept promise past its due time, or a decision on a block whose
// pending action this event created — not an addition to one already
// spoken. nil when u is not interrupt, or neither applies.
func announceTags(u voice.Urgency, duePassed, needsUser bool, pending string) map[string]any {
	if u != voice.UrgencyInterrupt {
		return nil
	}
	tags := map[string]any{}
	if duePassed {
		tags["due_promise"] = true
	}
	if needsUser && pending != "" {
		tags["needs_user"] = true
	}
	if len(tags) == 0 {
		return nil
	}
	return tags
}

// dropLate handles the brain's line arriving after the code sentence was
// spoken. As a rule it is not a second line about the same event
// (brain.push_late); it is added, once, at idle when
//   - the event is a finish the shepherd is owed (a kept promise, their
//     own instruction) — the code sentence said "here's what it found"
//     and this line is the finding — or the line relates the finish to a
//     request of theirs (relates_to), which the code could not know; or
//   - it says the shepherd must act and the block is still open.
//
// An owed finish whose line never comes (the turn failed or timed out)
// gets a short closing sentence instead, so "here's what it found" is not
// left hanging.
func (s *Supervisor) dropLate(a announcement, kept []state.Promise, level Chattiness, r turnResult, start time.Time) {
	dur := s.cfg.Now().Sub(start).Milliseconds()
	owed := owedFinish(a, kept) && a.asking == ""
	audible := len(kept) > 0 || level.allows(a.kind)
	if r.err != nil {
		s.logDecision(a, level, decision{via: "template", requested: "", applied: brain.UrgencySkip, reason: fallbackReason(r.err), late: true, err: r.err}, brain.Reply{}, kept, false)
		// Not for yielded or coalesced: a newer turn about the same agent
		// is about to speak.
		if reason := fallbackReason(r.err); owed && audible && (reason == "timeout" || reason == "fallback") {
			s.pushTagged(a.row.PaneID, "I couldn't get "+aliasOr(a.row.Alias)+"'s findings in time — ask me and I'll read its screen.", voice.UrgencyIdle, a.tags(nil))
		}
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
	requested := requestedUrgency(rep.Urgency, spoken)
	related := a.event == brain.PromiseEventDone && strings.TrimSpace(rep.RelatesTo) != ""
	reason := lateReason(open)
	var add bool
	switch {
	case spoken == "" || !open:
	case (owedFinish(a, kept) || related) && audible && (len(kept) > 0 || requested != brain.UrgencySkip):
		// The finding the shepherd is waiting for. The brain may skip an
		// unpromised finish (they already heard it), never a promise.
		add, reason = true, "owed"
	case rep.NeedsUser && a.pending != "" && level.allows(a.kind):
		add = true
	}
	s.logDecision(a, level, decision{via: "brain", requested: rep.Urgency, applied: appliedIf(add), reason: reason, late: true}, rep, kept, true)
	s.log.Log(logSource, "brain.push_late", a.row.PaneID, map[string]any{"turn": rep.Turn, "dur_ms": dur, "spoken": spoken, "needs_user": rep.NeedsUser, "relates_to": strings.TrimSpace(rep.RelatesTo), "added": add})
	if add {
		s.pushTagged(a.row.PaneID, spoken, voice.UrgencyIdle, a.tags(nil))
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
// still the pending one; true for anything but a block — a finish that
// also asked (announceFinishAsking) still has its finding to report
// after the prompt is answered.
func (s *Supervisor) pendingStillOpen(a announcement) bool {
	if a.pending == "" || a.event != brain.PromiseEventBlocked {
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
	sentence := a.sentence
	if reason == "wait" && owedFinish(a, kept) && a.asking == "" {
		// The brain is still writing the finding: say it is coming
		// (dropLate adds it).
		sentence = foundSentence(a)
	}
	if len(kept) == 0 {
		if a.kind != pushPromiseOnly {
			s.pushIfTagged(a.kind, a.row.PaneID, sentence, a.tags(nil))
		}
		return
	}
	line := "As you asked: " + sentence
	duePassed := s.duePassed(kept)
	u := voice.UrgencyIdle
	if duePassed {
		u = voice.UrgencyInterrupt
	}
	s.logKept(kept, a.event, line, u, "template")
	s.pushTagged(a.row.PaneID, line, u, a.tags(announceTags(u, duePassed, false, "")))
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
