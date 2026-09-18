package supervisor

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Push judgment (docs/roles.md "Push judgment"): on a worker's block or
// finish the brain — when one is attached — runs one announce event turn
// with the template sentence, the pane read already taken, the promises
// the event keeps and its transcript, and decides *whether* the assistant
// says anything (skip, silent) and *how urgently* (idle, interrupt); the
// assistant still decides the moment (the push queue). Code keeps the
// ceiling the brain cannot lift: the chattiness level (with kept promises
// the one exception), interrupt only for a due promise or a decision the
// CEO must take, and one line per finish. The template path stays the
// fallback — brain off, timeout, fallback, yield — and the first audible
// announcement of a finish never waits for the brain beyond
// Config.AnnounceWait: past it the template is spoken and the brain's
// line, if any and if it adds information, follows. A block's question
// is pushed before the brain is even asked; its line is an addition.

// duplicateOverlap is the normalised token overlap above which a brain
// line counts as a repeat of the sentence already spoken.
const duplicateOverlap = 0.8

// announcement is one worker event the supervisor announces: the
// template path's sentence and what the brain gets.
type announcement struct {
	row state.Agent
	// kind is the policy class of the sentence (pushDoneInvolved,
	// pushQuestion, …); pushPromiseOnly for a working/idle event whose
	// sentence is spoken only as a kept promise.
	kind pushKind
	// event is the worker event (brain.PromiseEventDone, …).
	event string
	// sentence is the template sentence.
	sentence string
	// screen is the pane read taken for the event, when one was.
	screen string
	// spoken: the sentence was already pushed before announce was called
	// (a question is never delayed); the brain's line is an addition.
	spoken bool
}

// turnResult is what the announce goroutine hands back.
type turnResult struct {
	rep brain.Reply
	err error
}

// announce keeps the promises the event fulfils and speaks the
// announcement: through the brain's announce turn under the chattiness
// ceiling when one is attached and the level leaves anything to decide,
// else exactly as the template path did before the brain existed.
func (s *Supervisor) announce(ctx context.Context, a announcement) {
	kept := s.keepMatching(a.row.PaneID, a.event)
	level := s.chattiness()
	if s.brain == nil {
		s.templatePath(a, kept, level, "no_brain", false)
		return
	}
	if ceilingForbidsAll(level, a, kept) {
		// Nothing the brain could say would be spoken: no model call.
		s.logDecision(a, level, decision{via: "ceiling", requested: "", applied: brain.UrgencySkip, reason: "ceiling"}, brain.Reply{}, kept, false)
		if !a.spoken && a.kind != pushPromiseOnly {
			s.log.Log(logSource, "supervisor.push_quiet", a.row.PaneID, map[string]any{"text": a.sentence, "chattiness": string(level)})
		}
		return
	}
	res := make(chan turnResult, 1)
	go func() {
		infos := make([]brain.PromiseInfo, 0, len(kept))
		for _, p := range kept {
			infos = append(infos, promiseInfo(p))
		}
		rep, err := s.brain.Turn(ctx, brain.Request{Kind: brain.KindAnnounce, Trigger: brain.TriggerEvent, Agent: aliasOr(a.row.Alias), Event: a.event, Template: a.sentence, TemplateSpoken: a.spoken, Screen: a.screen, Promises: infos})
		res <- turnResult{rep: rep, err: err}
	}()
	if a.spoken {
		// The question is out; the brain's line can only add to it.
		r := <-res
		s.applyResult(a, kept, level, r, false)
		return
	}
	var timer <-chan time.Time
	if s.cfg.AnnounceWait > 0 {
		timer = time.After(s.cfg.AnnounceWait)
	}
	select {
	case r := <-res:
		s.applyResult(a, kept, level, r, false)
	case <-timer:
		// The CEO hears the news now; the brain's line may follow.
		s.templatePath(a, kept, level, "wait", false)
		a.spoken = true
		r := <-res
		s.applyResult(a, kept, level, r, true)
	}
}

// applyResult turns the brain's answer (or failure) into pushes and
// logs; late means the template was already spoken because the wait
// elapsed (the promises were then kept by the template line too).
func (s *Supervisor) applyResult(a announcement, kept []state.Promise, level Chattiness, r turnResult, late bool) {
	if r.err != nil {
		reason := "fallback"
		switch {
		case errors.Is(r.err, brain.ErrYielded):
			reason = "yielded"
		case errors.Is(r.err, brain.ErrCoalesced):
			reason = "coalesced"
		case errors.Is(r.err, context.DeadlineExceeded):
			reason = "timeout"
		}
		if late {
			s.logDecision(a, level, decision{via: "template", requested: "", applied: brain.UrgencySkip, reason: reason, late: true, err: r.err}, brain.Reply{}, nil, false)
			return
		}
		s.templatePath(a, kept, level, reason, false)
		return
	}
	rep := r.rep
	spoken := spokenLine(rep.Spoken)
	detail := strings.TrimSpace(llm.Spoken(rep.Detail, 0))
	if detail != "" && a.event == brain.PromiseEventDone {
		s.recordDetail(a.row.PaneID, detail)
	}
	requested := requestedUrgency(rep.Urgency, spoken)
	if late {
		kept = nil // the template line already kept them
	}
	if len(kept) > 0 && (requested == brain.UrgencySkip || requested == brain.UrgencySilent) {
		// The brain may not silence a promise: the template keeps it.
		s.logDecision(a, level, decision{via: "template", requested: requested, applied: brain.UrgencySkip, reason: "promise"}, rep, kept, false)
		s.templatePath(a, kept, level, "promise", true)
		return
	}
	d := applyCeiling(level, a.kind, requested, ceilingInput{kept: len(kept) > 0, duePassed: s.duePassed(kept), needsUser: rep.NeedsUser, pendingOpen: s.pendingOpen(a.row.PaneID), addition: a.spoken})
	d.via = "brain"
	d.late = late
	if a.spoken && d.applied != brain.UrgencySkip && d.applied != brain.UrgencySilent && nearDuplicate(spoken, a.sentence) {
		d.applied, d.reason = brain.UrgencySkip, "duplicate"
	}
	s.logDecision(a, level, d, rep, kept, true)
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

// templatePath speaks the announcement as phase 2 did: the sentence when
// the level allows it, then "As you asked: <sentence>" for every kept
// promise at every level (idle, or interrupt past a due time).
// sentenceSpoken skips the sentence (already pushed).
func (s *Supervisor) templatePath(a announcement, kept []state.Promise, level Chattiness, reason string, sentenceSpoken bool) {
	if !a.spoken && !sentenceSpoken && a.kind != pushPromiseOnly {
		s.pushIf(a.kind, a.row.PaneID, a.sentence)
	}
	if s.brain != nil && reason != "promise" {
		applied := brain.UrgencySkip
		if !a.spoken && level.allows(a.kind) {
			applied = brain.UrgencyIdle
		}
		s.logDecision(a, level, decision{via: "template", requested: "", applied: applied, reason: reason}, brain.Reply{}, kept, false)
	}
	if len(kept) == 0 {
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
	payload := map[string]any{"via": d.via, "urgency_requested": d.requested, "urgency_applied": d.applied, "ceiling": string(level), "reason": d.reason, "agent": aliasOr(a.row.Alias), "event": a.event, "template": a.sentence, "template_spoken": a.spoken, "late": d.late}
	if withSpoken {
		payload["spoken"] = spokenLine(rep.Spoken)
		payload["detail"] = strings.TrimSpace(llm.Spoken(rep.Detail, 0))
		payload["relates_to"] = strings.TrimSpace(llm.Spoken(rep.RelatesTo, 1))
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
	kept        bool // the announcement keeps a promise
	duePassed   bool // a kept promise is past its due time
	needsUser   bool // the brain says the CEO must decide or act
	pendingOpen bool // the agent's question is still pending
	addition    bool // the sentence was already spoken; this line adds to it
}

// applyCeiling caps what the brain asked for under the chattiness level:
// skip and silent pass as asked (context is not speech); a kept promise
// is spoken at every level, at idle, or interrupt when its due time
// passed or the brain says the CEO must act; otherwise the level's
// policy table decides whether the line is spoken at all (quiet never
// enriches; normal keeps the recent_minutes rule for finishes), and
// interrupt is allowed only for a decision the CEO must take — for an
// addition to a question, only while that question is still open.
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
	if !level.allows(kind) || (in.addition && level == Quiet) {
		d.applied, d.reason = brain.UrgencySkip, "ceiling"
		return d
	}
	if requested == brain.UrgencyInterrupt {
		allowed := in.needsUser
		if in.addition {
			allowed = allowed && in.pendingOpen
		}
		if allowed {
			d.applied = brain.UrgencyInterrupt
		} else {
			d.reason = "ceiling"
		}
	}
	return d
}

// ceilingForbidsAll reports that the level would silence every audible
// line about a: no promise is kept, and the sentence's class is not
// spoken at this level (or, for an addition, the level is quiet).
func ceilingForbidsAll(level Chattiness, a announcement, kept []state.Promise) bool {
	if len(kept) > 0 {
		return false
	}
	if a.spoken {
		return level == Quiet || !level.allows(a.kind)
	}
	return !level.allows(a.kind)
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

// pendingOpen reports whether paneID's question is still pending.
func (s *Supervisor) pendingOpen(paneID string) bool {
	p, has := s.store.Pending()
	return has && p.Kind == state.PendingAnswerPrompt && p.PaneID == paneID
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

// nearDuplicate reports whether more than duplicateOverlap of the line's
// distinct tokens already occur in the sentence.
func nearDuplicate(line, sentence string) bool {
	return tokenOverlap(line, sentence) > duplicateOverlap
}

// tokenOverlap is the share of line's distinct normalised tokens
// (lower-case, punctuation stripped) that sentence also contains; 0
// when the line has no tokens.
func tokenOverlap(line, sentence string) float64 {
	have := map[string]bool{}
	for _, w := range tokens(sentence) {
		have[w] = true
	}
	seen := map[string]bool{}
	total, hits := 0, 0
	for _, w := range tokens(line) {
		if seen[w] {
			continue
		}
		seen[w] = true
		total++
		if have[w] {
			hits++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// tokens splits text into lower-case words of letters and digits.
func tokens(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
}
