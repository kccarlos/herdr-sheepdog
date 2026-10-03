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
// One line per event, and when it is spoken depends on the event
// (iteration 2 WP2, the 2026-09-24 scorecard: 48 of 61 brain lines were
// dropped for lateness, 30 of them needs_user):
//   - A finish is not urgent: it waits for the brain's announce turn up to
//     the turn's own budget (brain.announce_turn_timeout_s, or
//     brain.announce_timeout_s when the shepherd is owed the line) and
//     speaks the brain's line alone. Only when the turn fails, times out,
//     yields or is coalesced is the code sentence spoken — once, then; an
//     owed finish (a kept promise, the shepherd's own instruction) that got
//     no line says so in the same sentence.
//   - A block is urgent: the code sentence (the parsed prompt) is spoken
//     at Config.AnnounceWait unless the brain's line lands first. A line
//     that lands later is dropped (brain.push_late) unless it says the
//     shepherd must act (needs_user) — spoken once, at idle — or it is the
//     finding of an owed finish that also asked (announceFinishAsking).
//     A line about a prompt that is no longer open is dropped
//     (brain.push_stale on time, push_decision reason stale when late).

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
	// wait is how the event waited for the brain's line, set by announce
	// for brain.push_decision: waitTurn (a finish, the turn's own budget),
	// waitAnnounce (a block, Config.AnnounceWait), "" (no turn ran).
	wait string
	// waited is how long it waited before its line was decided.
	waited time.Duration
}

// The two ways an announcement waits for the brain (brain.push_decision
// "wait").
const (
	waitTurn     = "turn"
	waitAnnounce = "announce_wait"
)

// urgent reports whether the event must be heard at once — a block, or a
// finish that left a prompt open (announceFinishAsking) — rather than
// waiting for the brain's line: only a prompt cannot wait.
func (a announcement) urgent() bool {
	return a.event == brain.PromiseEventBlocked || a.pending != ""
}

// turnResult is what the announce goroutine hands back.
type turnResult struct {
	rep brain.Reply
	err error
}

// announce keeps the promises the event fulfils and speaks the one line
// the event gets: for a finish, the brain's line when its turn answers, or
// the code sentence when it cannot; for a block, the brain's line when it
// lands within Config.AnnounceWait, else the code sentence.
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
	req := brain.Request{Kind: brain.KindAnnounce, Trigger: brain.TriggerEvent, Agent: aliasOr(a.row.Alias), Event: a.event, Template: a.sentence, Screen: a.screen, Promises: infos, Owed: a.owed, AlsoAsking: a.asking, Link: eventLink(a.eventID), Facts: s.announceFacts(a)}
	res := make(chan turnResult, 1)
	start := s.cfg.Now()
	go func() {
		rep, err := s.brain.Turn(ctx, req)
		res <- turnResult{rep: rep, err: err}
	}()
	if !a.urgent() {
		// A finish is news, not an alarm: the brain's line (what it
		// found) is worth the seconds its turn takes, and the turn's own
		// budget bounds the wait (brain.Turn never outlives it).
		r := <-res
		a.wait, a.waited = waitTurn, s.cfg.Now().Sub(start)
		if r.err != nil {
			s.codePath(a, kept, level, fallbackReason(r.err))
			return
		}
		s.applyResult(a, kept, level, r)
		return
	}
	a.wait = waitAnnounce
	var timer <-chan time.Time
	if s.cfg.AnnounceWait > 0 {
		timer = time.After(s.cfg.AnnounceWait)
	}
	select {
	case r := <-res:
		a.waited = s.cfg.Now().Sub(start)
		s.applyResult(a, kept, level, r)
	case <-timer:
		// The shepherd hears the prompt now; the line the brain is still
		// writing is an addition at most.
		a.waited = s.cfg.AnnounceWait
		s.codePath(a, kept, level, "wait")
		s.dropLate(a, kept, level, <-res, start)
	}
}

// owedFinish reports whether a is a finish the shepherd is waiting on:
// it keeps a promise or ends their own instruction.
func owedFinish(a announcement, kept []state.Promise) bool {
	return a.event == brain.PromiseEventDone && (len(kept) > 0 || a.owed)
}

// noFindingSentence is the code sentence of an owed finish whose brain
// line never came (the turn timed out): the shepherd is waiting
// for what it found, so the sentence says it could not be had and how to
// get it.
func noFindingSentence(a announcement) string {
	return aliasOr(state.Say(a.row)) + " finished, but I couldn't get its findings in time. Ask me and I'll read its screen."
}

// applyResult turns the brain's answer (or failure) into the event's one
// push and its logs.
func (s *Supervisor) applyResult(a announcement, kept []state.Promise, level Chattiness, r turnResult) {
	if r.err != nil {
		s.codePath(a, kept, level, fallbackReason(r.err))
		return
	}
	rep := r.rep
	spoken, overflow := earLine(rep)
	if !s.pendingStillOpen(a) {
		// The prompt was answered or went away while the brain wrote
		// about it: whatever it says now is about something gone.
		s.logDecision(a, level, decision{via: "brain", requested: rep.Urgency, applied: brain.UrgencySkip, reason: "stale"}, rep, kept, true)
		s.log.Log(logSource, "brain.push_stale", a.row.PaneID, map[string]any{"turn": rep.Turn, "pending": a.pending, "spoken": spoken})
		if len(kept) > 0 {
			// The promises were marked kept before the turn ran
			// (keepMatching) and their line is now dropped with the
			// stale block: say so, or the promise ends without a trace
			// (docs/timelines.md C20).
			s.log.Log("voice", "voice.push_dropped", a.row.PaneID, map[string]any{
				"reason": "stale_block", "text": spoken, "pending": a.pending, "agent": aliasOr(a.row.Alias), voice.TagKeepsPromiseIDs: promiseIDs(kept),
			})
		}
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
	s.recordAnnounceDetail(a, rep, overflow)
	if d.applied == brain.UrgencySkip {
		if d.reason == "ceiling" {
			s.log.Log(logSource, "supervisor.push_quiet", a.row.PaneID, map[string]any{"text": spoken, "chattiness": string(level)})
		}
		return
	}
	u := voiceUrgency(d.applied)
	s.logKept(kept, a.event, spoken, u, "brain")
	s.notify(a, kept, spoken)
	s.pushTagged(a.row.PaneID, spoken, u, a.tags(keptTags(announceTags(u, duePassed, rep.NeedsUser, a.pending), kept)))
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

// keptTags adds keeps_promise_ids — the promises a line keeps — to
// tags: a kept promise is owed to the shepherd, so the voice push queue
// lets it pass plain idle news (voice/pushclass.go, docs/timelines.md
// C29). tags is returned as is when nothing is kept.
func keptTags(tags map[string]any, kept []state.Promise) map[string]any {
	if len(kept) == 0 {
		return tags
	}
	out := make(map[string]any, len(tags)+1)
	for k, v := range tags {
		out[k] = v
	}
	out[voice.TagKeepsPromiseIDs] = promiseIDs(kept)
	return out
}

// promiseIDs lists the ids of ps.
func promiseIDs(ps []state.Promise) []string {
	ids := make([]string, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	return ids
}

// dropLate handles the brain's line arriving after the code sentence of
// an urgent event (a block, or a finish that asks) was spoken. As a rule
// it is not a second line about the same event (brain.push_late); it is
// added, once, at idle when
//   - it says the shepherd must act (needs_user), whatever the event, and
//     the prompt it is about is still open; or
//   - the event is a finish the shepherd is owed (a kept promise, their
//     own instruction) that also asked, so this line is the finding, or
//     the line relates the finish to a request of theirs (relates_to),
//     which the code could not know.
func (s *Supervisor) dropLate(a announcement, kept []state.Promise, level Chattiness, r turnResult, start time.Time) {
	dur := s.cfg.Now().Sub(start).Milliseconds()
	audible := len(kept) > 0 || level.allows(a.kind)
	if r.err != nil {
		s.logDecision(a, level, decision{via: "template", requested: "", applied: brain.UrgencySkip, reason: fallbackReason(r.err), late: true, err: r.err}, brain.Reply{}, kept, false)
		return
	}
	open := s.pendingStillOpen(a)
	rep := r.rep
	spoken, overflow := earLine(rep)
	if spoken != "" && open {
		// The record keeps the fuller line even when nobody hears it.
		s.recordFinish(a, spoken)
		s.recordAnnounceDetail(a, rep, overflow)
	}
	requested := requestedUrgency(rep.Urgency, spoken)
	related := a.event == brain.PromiseEventDone && strings.TrimSpace(rep.RelatesTo) != ""
	reason := lateReason(open)
	var add bool
	switch {
	case spoken == "" || !open || !audible:
	case rep.NeedsUser:
		// A decision the shepherd must take is never lost to lateness.
		add, reason = true, "needs_user"
	case (owedFinish(a, kept) || related) && (len(kept) > 0 || requested != brain.UrgencySkip):
		// The finding the shepherd is waiting for. The brain may skip an
		// unpromised finish (they already heard it), never a promise.
		add, reason = true, "owed"
	}
	s.logDecision(a, level, decision{via: "brain", requested: rep.Urgency, applied: appliedIf(add), reason: reason, late: true}, rep, kept, true)
	s.log.Log(logSource, "brain.push_late", a.row.PaneID, map[string]any{"turn": rep.Turn, "dur_ms": dur, "spoken": spoken, "needs_user": rep.NeedsUser, "relates_to": strings.TrimSpace(rep.RelatesTo), "added": add})
	if add {
		s.notify(a, kept, spoken)
		s.pushTagged(a.row.PaneID, spoken, voice.UrgencyIdle, a.tags(keptTags(nil, kept)))
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
// whenever the brain is off, late on a block, or could not answer. An
// owed finish whose turn timed out says in the same sentence that its
// findings could not be had in time (not after a yield or coalesce: a
// newer turn about the same agent is about to speak; nor after a model
// error, which is no matter of time).
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
	if owedFinish(a, kept) && a.asking == "" && reason == "timeout" {
		sentence = noFindingSentence(a)
	}
	if len(kept) == 0 {
		if a.kind != pushPromiseOnly {
			s.notify(a, kept, sentence)
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
	s.notify(a, kept, line)
	s.pushTagged(a.row.PaneID, line, u, a.tags(keptTags(announceTags(u, duePassed, false, ""), kept)))
}

// recordAnnounceDetail stores an announce turn's detail alongside the
// finish it phrased (for the ledger and recall), followed by overflow:
// whatever the ear cap cut from the spoken line (earLine), so nothing
// the brain wrote is lost.
func (s *Supervisor) recordAnnounceDetail(a announcement, rep brain.Reply, overflow string) {
	if a.event != brain.PromiseEventDone {
		return
	}
	if detail := strings.TrimSpace(prose.Spoken(rep.Detail, 0) + " " + overflow); detail != "" {
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
	reason    string // brain, ceiling, promise, promise_due, stale, late, owed, needs_user, wait, timeout, fallback, yielded, coalesced
	late      bool   // the template was spoken before the brain answered
	err       error
}

// logDecision emits brain.push_decision.
func (s *Supervisor) logDecision(a announcement, level Chattiness, d decision, rep brain.Reply, kept []state.Promise, withSpoken bool) {
	payload := map[string]any{"via": d.via, "urgency_requested": d.requested, "urgency_applied": d.applied, "ceiling": string(level), "reason": d.reason, "agent": aliasOr(a.row.Alias), "event": a.event, "template": a.sentence, "late": d.late}
	if a.wait != "" {
		// How the event waited for the brain's line and for how long: the
		// scorecard tells "brain line after the wait" (via brain) from
		// "code sentence after the turn failed" (via template).
		payload["wait"] = a.wait
		payload["waited_ms"] = a.waited.Milliseconds()
	}
	if withSpoken {
		payload["spoken"], _ = earLine(rep)
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
