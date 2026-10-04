package supervisor

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// One decision-maker (docs/roles.md): the voice hands over whatever the
// CEO said and decides nothing. The words every path below runs on are
// the ones the daemon recorded (inputs.go's claimInput) — the voice's
// input transcription, typed text or a tap — never the voice model's
// to_supervisor argument, which paraphrased, invented and cut
// sentences short (2026-09-23/24); the argument is a hint, logged
// beside them. Two deterministic fast paths run here, in code, before
// any model — a bare stop, and a bare answer to the one open prompt —
// and everything else is one brain turn over the words, which resolves
// what they are and acts. With no brain the daemon answers from the
// roster and sends nothing: judging what words mean is the brain's
// work, and there is no second model for it.

// inflightUtterance is the one to_supervisor call being handled, under
// s.mu: what the CEO said, how to cancel the turn it is (or is about to
// be) in, and whether the brain already has it. The stop procedure's
// first two phases are this slot. It stays installed until the brain's
// turn is over, which may be long after the tool call returned a pending
// result (Config.ReplyWait), so a stop can still cancel that turn.
type inflightUtterance struct {
	text    string
	cancel  context.CancelFunc
	inBrain bool
	// dropped is set by the stop procedure: the reply is discarded
	// rather than answered, so these words reach no worker after all.
	dropped bool

	// id names the utterance on brain.reply_* and on its late push
	// (voice.push {answers_utterance}).
	id string
	// start is when the shepherd began saying these words (claim.start;
	// the earlier one when two utterances coalesce): the brain is told
	// what they had heard by then (heard.go).
	start time.Time
	// callID is the voice's tool call id that relayed the words: the
	// brain request and the late push carry it (links.go).
	callID string
	// accepted is s.acceptedSeq once this call was accepted: when it has
	// moved by the time a late reply lands, the shepherd asked for
	// something else meanwhile and the reply is stale.
	accepted uint64
	// detached is set when the tool call gave up waiting and returned a
	// pending result: the turn's goroutine then delivers the reply as a
	// push. done is set, with out, when the turn finished first; finished
	// is closed then. All three under s.mu, so exactly one side answers.
	detached bool
	done     bool
	out      utteranceOutcome
	finished chan struct{}

	// prev is the utterance whose slot this one took over, until
	// coalesce looked at it. started is set (by the brain's turn lock,
	// brain.WithTurnStart) once this turn is running; coalesced once a
	// newer utterance took its words over while it was still queued, so
	// it delivers nothing. merged counts the utterances in text. All
	// under s.mu.
	prev      *inflightUtterance
	started   bool
	coalesced bool
	merged    int
}

// utteranceOutcome is what one utterance turn produced. ok is false
// when there was no brain, or it could not answer, and the daemon must
// speak for itself.
type utteranceOutcome struct {
	res voice.ToolResult
	ok  bool
	// acted: the turn sent something to a worker or recorded something
	// (brain.Reply.Acted).
	acted bool
	// needsUser: the reply says the shepherd must decide or act.
	needsUser bool
	// actedOn: the panes the turn's actions went to (brain.Reply.ActedOn).
	actedOn []string
	// focus is what the reply says about the conversation's focus
	// (focus.go), for the voice once it has spoken the reply.
	focus focusFacts
}

// replyPendingDetail is the silent pending result's detail: context for
// the voice model's follow-ups, never read aloud. The voice already said
// "on it" as it called the tool, so the result adds no speech of its own.
const replyPendingDetail = "Still working on it; the answer follows on its own."

// callIDKey carries the voice's tool call id from Handler to
// ToSupervisor, for brain.reply_pending.
type callIDKey struct{}

// callIDOf is the voice's tool call id ctx carries ("" when none).
func callIDOf(ctx context.Context) string {
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}

// withCallID returns ctx carrying the voice's tool call id.
func withCallID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, callIDKey{}, id)
}

// UtteranceOf is the words an utterance runs on (docs/roles.md "One
// decision-maker"): what the daemon recorded the shepherd saying, typing
// or tapping (heard), and the voice model's to_supervisor argument
// (said) only when nothing was recorded — fallback is true then. The
// eval harness uses it to put the same words in front of the brain as
// the daemon does.
func UtteranceOf(heard, said string) (text string, fallback bool) {
	if h := strings.TrimSpace(heard); h != "" {
		return h, false
	}
	return strings.TrimSpace(said), true
}

// ToSupervisor handles the voice's relay tool. said is the model's
// argument; the utterance is what the shepherd said (UtteranceOf).
func (s *Supervisor) ToSupervisor(ctx context.Context, said string) (voice.ToolResult, error) {
	said = strings.TrimSpace(said)
	c, fresh := s.claimInput(ctx)
	if !fresh {
		// Nothing the shepherd said, typed or tapped since the previous
		// relay or the last push: the voice model is acting on its own
		// (2026-09-23: "yes" right after a push, with the mic muted; a
		// push relayed back as if the shepherd had said it). Nothing
		// happens and nothing is said (inputs.go) — unless the call
		// repeats the shepherd's own words of the last few seconds,
		// which a push sent in between hid from the gate (rescueInput).
		if s.selfRelay(said) {
			s.log.Log(logSource, "supervisor.self_relay", "", map[string]any{"text": said})
			return voice.ToolResult{Scheduling: voice.UrgencySilent}, nil
		}
		rescued, ok := s.rescueInput(said)
		if !ok {
			s.log.Log(logSource, "supervisor.voice_initiative_ignored", "", map[string]any{"text": said})
			return voice.ToolResult{Scheduling: voice.UrgencySilent}, nil
		}
		s.log.Log(logSource, "supervisor.relay_rescued", "", map[string]any{"heard": rescued.text, "said": said, "inputs": rescued.n, "utterance_id": rescued.id})
		c = rescued
	}
	text, fallback := UtteranceOf(c.text, said)
	payload := map[string]any{"heard": c.text, "said": said, "inputs": c.n, "input": string(c.in.kind), "utterance_id": c.id}
	// waited_ms: from the shepherd's last words (the input's last
	// fragment) to this relay — the voice model's share of the wait
	// (turn.latency's voice_ms).
	if !c.in.at.IsZero() {
		payload["waited_ms"] = s.cfg.Now().Sub(c.in.at).Milliseconds()
	}
	if id := callIDOf(ctx); id != "" {
		payload["call_id"] = id
	}
	if fallback {
		// The recorded input has no words (it cannot today: empty
		// fragments never become inputs), so the argument is all there
		// is. Logged, so it shows if it ever happens.
		s.log.Log(logSource, "supervisor.relay_fallback", "", payload)
	} else {
		s.log.Log(logSource, "supervisor.relay", "", payload)
	}
	if text == "" {
		return voice.ToolResult{Spoken: "Sorry, what was that?", Clarify: true, Scheduling: voice.UrgencyInterrupt}, nil
	}
	c.text = text
	res := s.handleUtterance(ctx, c, callIDOf(ctx), false)
	if res.Spoken != "" && !res.Pending {
		// The voice tags the playback of this answer with it
		// (turn.latency's first audio).
		res.UtteranceID = c.id
	}
	return res, nil
}

// handleUtterance runs the shepherd's claimed words: the fast paths,
// then one brain turn. A relay (daemon false) returns the reply as the
// tool result, waiting up to Config.ReplyWait for it; the daemon's own
// relay of an unanswered utterance (relay.go) has no tool call to answer,
// so its turn's reply is always pushed (deliverLate), and only a fast
// path's result is returned, for the caller to push.
func (s *Supervisor) handleUtterance(ctx context.Context, c claim, callID string, daemon bool) voice.ToolResult {
	if s.echoDropped(&c) {
		return voice.ToolResult{Scheduling: voice.UrgencySilent}
	}
	text := c.text
	// Bare stop first: "no" is an answer, "no no" is a stop.
	if bareStop(text) {
		res, _ := s.Stop(ctx, StopTriggerBareWord)
		return res
	}
	// The "New conversation" bare-phrase trigger and its confirm
	// (docs/roles.md "Triggers" (b)): a fast path, like the stop word
	// above — no model, and only this claimed shepherd input can ask or
	// confirm it.
	if res, ok := s.resetConfirmFastPath(ctx, text); ok {
		return res
	}
	sync := s.cfg.ReplyWait < 0 && !daemon
	// The turn is detached from the tool call unless it is synchronous:
	// the voice cancels a handler's context as soon as the handler
	// returns, and the turn may outlive it. Like an investigation job it
	// runs on its own context, bounded by the brain's own turn timeout;
	// the stop procedure cancels it through the slot, and shutdown
	// through s.life.
	parent := s.life
	if sync {
		parent = ctx
	}
	turnCtx, u := s.beginUtterance(parent, text, callID, c.id)
	s.mu.Lock()
	u.start = c.start
	s.mu.Unlock()
	if res, ok := s.fastAnswer(turnCtx, c.in, text); ok {
		s.endUtterance(u)
		return res
	}
	s.coalesce(u)
	switch {
	case daemon:
		s.mu.Lock()
		u.detached = true
		s.mu.Unlock()
		start := s.cfg.Now()
		s.deliverLate(turnCtx, u, s.utteranceTurn(turnCtx, u), start)
		return voice.ToolResult{}
	case sync:
		// Synchronous: the turn runs under the tool call's own context
		// and its reply is always the tool result.
		defer s.endUtterance(u)
		return s.inlineResult(turnCtx, u, s.utteranceTurn(turnCtx, u))
	default:
		return s.awaitReply(ctx, turnCtx, u)
	}
}

// coalesce merges the utterance u took the slot from into u when that
// one's brain turn is still queued behind another turn (docs/roles.md
// "One decision-maker"): two back-to-back sentences ("Okay, I closed
// the UC summary Codex agent." / "Check the remaining.", 2026-09-24
// 00:41) are one request, and two turns answered them over each other.
// The queued turn is cancelled before it starts, so it delivers nothing
// (coalesced), and u runs once over both texts. A turn already running
// is never cut: it finishes, and its reply is stale if it lands late
// (acceptedSeq moved when u was accepted; deliverLate).
func (s *Supervisor) coalesce(u *inflightUtterance) {
	s.mu.Lock()
	p := u.prev
	u.prev = nil
	if p == nil || !p.inBrain || p.started || p.dropped || p.coalesced || p.done {
		s.mu.Unlock()
		return
	}
	p.coalesced = true
	u.text = p.text + " " + u.text
	u.merged = p.merged + 1
	if !p.start.IsZero() && (u.start.IsZero() || p.start.Before(u.start)) {
		u.start = p.start
	}
	payload := map[string]any{"n": u.merged, "text": u.text, "id": u.id, "replaces": p.id, "utterance_id": u.id}
	cancel := p.cancel
	s.mu.Unlock()
	cancel()
	s.log.Log(logSource, "supervisor.utterance_coalesced", "", payload)
}

// awaitReply runs the utterance turn detached and waits up to
// Config.ReplyWait for it (docs/history/roles-status.md). A reply inside the
// wait is the tool result, exactly as a synchronous call's would be.
// Past it the call returns a silent pending result at once — the voice
// said "on it" as it called the tool, and a second acknowledgement would
// only repeat it — and the reply is pushed when it lands (deliverLate).
// A voice that drops the call meanwhile (the shepherd barged in) gets
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
// is one, else the fallback (the roster with no brain, else the line
// that says it could not answer).
func (s *Supervisor) inlineResult(turnCtx context.Context, u *inflightUtterance, out utteranceOutcome) voice.ToolResult {
	if s.utteranceCoalesced(u) {
		return coalescedResult()
	}
	if s.utteranceDropped(u) || turnCtx.Err() != nil {
		// Stopped, or the voice cancelled the call: the stop procedure
		// speaks, and the brain's reply is discarded.
		return droppedResult()
	}
	if out.ok {
		// A reply that named an agent counts as dealing with it, like a
		// spoken briefing does (the normal level's chattiness rule).
		s.mentionedIn(out.res.Spoken)
		s.noteFocus(out.focus, u.text)
		return out.res
	}
	return s.fallback()
}

// deliverLate pushes the reply of a turn that outlived its tool call,
// tagged answers_utterance, and ends its slot. A stopped turn delivers
// nothing. A reply is stale once the shepherd's next request was
// accepted (acceptedSeq moved): the shepherd has moved on, so a turn
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
		if !out.ok || !out.acted {
			return
		}
	}
	var paneID, line string
	urgency := voice.UrgencyIdle
	if out.ok {
		paneID, line = s.lateLine(out.res, u.text, out.actedOn)
		if out.needsUser && s.answerPromptOpen() {
			// The shepherd must decide on an open prompt: the reply may
			// ask for the floor, but it is not tagged needs_user — that
			// exception is a decision on a block the push itself brings
			// (announce.go's announceTags), so the voice still waits out
			// its own sentence (docs/roles.md "Pushes").
			urgency = voice.UrgencyInterrupt
		}
	} else {
		line = s.fallback().Spoken
	}
	if stale {
		urgency = voice.UrgencySilent
	} else if out.ok {
		s.mentionedIn(out.res.Spoken)
	}
	tags := map[string]any{"answers_utterance": u.id, "utterance_id": u.id}
	if u.callID != "" {
		tags[tagCallID] = u.callID
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
// the shepherd's words named. Several means no single subject. Campaign D
// N4: the brain told bench-codex to fix its bug and the reply mentioned
// only bench-claude ("we still have bench-claude going in the
// background"), so the CEO heard "About bench-claude: Sent. It is fixing
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
// for the shepherd.
func (s *Supervisor) answerPromptOpen() bool {
	p, has := s.store.Pending()
	return has && p.Kind == state.PendingAnswerPrompt
}

// beginUtterance installs the in-flight slot and returns the turn's
// cancellable context, derived from parent (the stop procedure cancels
// it).
func (s *Supervisor) beginUtterance(parent context.Context, text, callID, id string) (context.Context, *inflightUtterance) {
	turnCtx, cancel := context.WithCancel(parent)
	u := &inflightUtterance{text: text, cancel: cancel, finished: make(chan struct{}), callID: callID, merged: 1, id: id}
	s.mu.Lock()
	if u.id == "" {
		u.id = s.nextID("utt")
	}
	u.accepted = s.acceptedSeq
	u.prev = s.utterance
	// A second utterance while one is in flight takes the slot over: the
	// older turn is left to finish (the brain's turn lock serialises them
	// anyway) unless it is still queued, when coalesce merges the two.
	s.utterance = u
	s.mu.Unlock()
	return turnCtx, u
}

// endUtterance clears the slot when u still owns it and releases its
// context.
func (s *Supervisor) endUtterance(u *inflightUtterance) {
	s.mu.Lock()
	if s.utterance == u {
		s.utterance = nil
	}
	s.mu.Unlock()
	u.cancel()
}

// abandonUtterance cancels the utterance in flight, if any, so it
// delivers nothing: the daemon is going down, or (reset.go) a "New
// conversation" must not let a reply from before the reset land as a
// push in the fresh one.
func (s *Supervisor) abandonUtterance() {
	s.mu.Lock()
	u := s.utterance
	if u != nil {
		u.dropped = true
		s.utterance = nil
	}
	s.mu.Unlock()
	if u != nil {
		u.cancel()
	}
}

// utteranceCoalesced reports whether a newer utterance took u's words
// over (coalesce).
func (s *Supervisor) utteranceCoalesced(u *inflightUtterance) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return u.coalesced
}

// coalescedResult is what a coalesced utterance's call returns: its
// words run with the next utterance's, whose reply answers both.
func coalescedResult() voice.ToolResult {
	return voice.ToolResult{Detail: "Merged with what the shepherd said next; one answer follows for both.", Pending: true, Scheduling: voice.UrgencySilent}
}

// utteranceDropped reports whether the stop procedure dropped u.
func (s *Supervisor) utteranceDropped(u *inflightUtterance) bool {
	if u == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return u.dropped
}

// droppedResult is what a stopped utterance returns: the stop procedure
// speaks, so this only tells the voice model what became of its call.
func droppedResult() voice.ToolResult {
	return voice.ToolResult{Spoken: "Dropped: you said stop.", Scheduling: voice.UrgencySilent}
}

// utteranceTurn runs one brain turn over the CEO's words. ok is false
// when there was no brain, or it could not answer, and the daemon must
// speak for itself; a turn the stop procedure cancelled never returns
// ok, and the caller checks utteranceDropped rather than falling through.
func (s *Supervisor) utteranceTurn(ctx context.Context, u *inflightUtterance) utteranceOutcome {
	if s.brain == nil {
		return utteranceOutcome{}
	}
	s.mu.Lock()
	text, start := u.text, u.start
	s.mu.Unlock()
	// The roster sentence travels with the request, so a status question
	// costs no tool call (D1-revised); the pending prompt with its
	// option keys and the open promises are in the brain's own roster
	// and ledger sections.
	brief, _ := s.briefingText()
	req := brain.Request{Kind: brain.KindUtterance, Trigger: brain.TriggerCEO, Question: text, Briefing: brief, Link: callLink(u.callID), UtteranceID: u.id}
	// The agent the conversation is on, for words that say "it" (the
	// brain's agentRecords).
	s.mu.Lock()
	subject := s.lastSubject
	s.mu.Unlock()
	if row, ok := s.store.Get(subject); ok {
		req.Focus = row.Alias
	}
	// What the shepherd had heard when they began: evaluated when the
	// turn starts, since it may queue behind an announce turn whose
	// line goes out meanwhile (heard.go).
	req.Facts = func() brain.EventFacts {
		return brain.EventFacts{Heard: s.heardFacts(start), Language: s.ceoLanguage(), OpenPrompt: s.openPromptFact(start)}
	}
	// supervisor.repeat_window_s: an identical roster delivered again
	// inside the window is noise over voice, so the brain gets a note it
	// can act on ("no change since I last checked") instead of the
	// shepherd hearing the same sentence twice. Delivered means answered:
	// the mark is set below, after a reply, never for a failed turn.
	if age, repeat := s.briefingRepeat(brief); repeat {
		req.RosterUnchangedFor = age
		s.log.Log(logSource, "supervisor.briefing_repeat", "", map[string]any{"roster": brief, "age_s": int(age.Seconds())})
	}
	s.mu.Lock()
	u.inBrain = true
	s.mu.Unlock()
	// The turn lock says when the turn really starts: until then a newer
	// utterance may still take these words over (coalesce).
	started := brain.WithTurnStart(ctx, func() {
		s.mu.Lock()
		u.started = true
		s.mu.Unlock()
	})
	rep, err := s.brain.Turn(started, req)
	if err != nil {
		return utteranceOutcome{}
	}
	res := toolResultFrom(rep, prose.EarRunaway)
	if res.Spoken == "" {
		return utteranceOutcome{acted: rep.Acted}
	}
	s.markBriefingDelivered(brief)
	return utteranceOutcome{res: res, ok: true, acted: rep.Acted, actedOn: rep.ActedOn, needsUser: rep.NeedsUser, focus: focusFromReply("", res.Spoken, rep)}
}

// The bare fast-path words (docs/roles.md "One decision-maker"). *Bare*
// means the whole utterance, so "don't stop the tests" never matches a
// stop and "yes please run it" never answers a prompt — only these exact
// (normalised) phrasings do, including the shepherd's polite ones.
//
// BareStopWords is exported so every bare-stop check in the daemon reads
// the same list: internal/inject's Router (GPT-Live's Classify path)
// used to keep its own regex, which disagreed with this one on "abort"
// and "halt" (audit B8) — the same word stopped GPT-Live outright but
// only became an ordinary brain turn on Gemini.
var (
	//
	// "never mind" is not one (docs/iteration-2.md J6, eval case
	// never-mind-is-not-stop): it is ordinary speech that goes to the
	// brain, which decides whether it withdraws anything. As a stop word
	// it sent Ctrl-C to a worker that had just been given a task.
	BareStopWords = map[string]bool{"stop": true, "stop stop": true, "wait": true, "cancel": true, "abort": true, "halt": true, "no no": true, "hold on": true}
	bareYesWords  = map[string]bool{
		"yes": true, "yeah": true, "yep": true, "yup": true, "sure": true, "ok": true, "okay": true,
		"approve": true, "approve it": true, "please approve": true, "do it": true,
		"go ahead": true, "yeah go ahead": true, "yes go ahead": true,
		"yes please": true, "yes continue": true,
	}
	bareNoWords = map[string]bool{"no": true, "nope": true, "no thanks": true, "don't": true}
	// bareOptionWords covers the numeral, word and ordinal forms of a
	// pick; bareAnswer only gates on membership here, mapAnswer resolves
	// the actual option (pending.go's extended ordinals).
	bareOptionWords = map[string]int{
		"option one": 1, "option two": 2, "option three": 3,
		"option 1": 1, "option 2": 2, "option 3": 3,
		"first": 1, "second": 2, "third": 3,
		"first one": 1, "second one": 2, "third one": 3,
		"the first": 1, "the second": 2, "the third": 3,
		"the first one": 1, "the second one": 2, "the third one": 3,
	}
)

// BareWord lower-cases the utterance and strips punctuation from each
// word, so "yes, please" and "yes please" are the same bare word — but
// the fast paths still only match a whole utterance: an extra word (a
// comma-free "but", say) makes it a different one.
func BareWord(text string) string {
	fields := strings.Fields(text)
	words := make([]string, 0, len(fields))
	for _, f := range fields {
		if w := strings.Trim(strings.ToLower(f), ".,!?;:"); w != "" {
			words = append(words, w)
		}
	}
	return strings.Join(words, " ")
}

// bareStop reports whether the whole utterance is one of the stop words.
func bareStop(text string) bool { return BareStopWords[BareWord(text)] }

// fastAnswer is the bare-answer fast path: the shepherd's whole
// utterance (what was heard, typed or tapped — never the tool argument)
// answers the one open answer_prompt, so the keys go straight to the
// pane without a model, when the one rail's reading of in, the newest
// input, allows it (inputs.go). ok is false for anything else, which
// goes to the brain.
func (s *Supervisor) fastAnswer(ctx context.Context, in *shepherdInput, text string) (voice.ToolResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text = strings.TrimSpace(text)
	p, has := s.store.Pending()
	if !has || p.Kind != state.PendingAnswerPrompt {
		return voice.ToolResult{}, false
	}
	keys, label, ok := bareAnswer(p, text)
	if !ok {
		return voice.ToolResult{}, false
	}
	if age := s.cfg.Now().Sub(p.CreatedAt); age > fastAnswerWindow {
		// Minutes after the announcement a bare "yes" may be about
		// something else the shepherd heard since: the brain gets the
		// words, with the prompt and how long it has waited in its
		// roster, and decides (docs/bench-2026-10-03.md #8).
		s.log.Log(logSource, "supervisor.fast_path_skipped", p.PaneID, map[string]any{"reason": "prompt_old", "age_s": int(age.Seconds()), "text": text, "id": p.ID})
		return voice.ToolResult{}, false
	}
	if rin, reason := s.railInputLocked(p, keys); reason != "" || rin != in {
		if reason == "" {
			reason = "the shepherd said something newer than the words being answered"
		}
		s.railBlockedLocked(p, keys, "fast_path", reason)
		return voice.ToolResult{}, false
	}
	alias := s.sayOf(p.PaneID)
	s.log.Log(logSource, "supervisor.fast_path", p.PaneID, map[string]any{"path": "answer", "text": text, "answer": label, "id": p.ID, "input": string(in.kind)})
	zh := s.ceoChineseLocked()
	res, sent := s.answerKeys(ctx, p, keys, label, func(l string) string { return toldLine(alias, l, zh) }, "fast_path", nil)
	if sent {
		in.used = true
	}
	if s.brain != nil {
		// No model saw this, so the transcript would otherwise lose it.
		s.brain.Daemon(fmt.Sprintf("fast path: the CEO said %q; %s", text, lowerFirst(res.Spoken)))
	}
	return res, true
}

// bareAnswer maps a whole-utterance answer onto the pending prompt's
// keys: yes, no, "option two", or an on-screen option's label.
func bareAnswer(p state.PendingAction, text string) (keys []string, label string, ok bool) {
	word := BareWord(text)
	switch {
	case word == "":
		return nil, "", false
	case bareYesWords[word], bareNoWords[word]:
	case bareOptionWords[word] > 0:
		n := bareOptionWords[word]
		if n > len(p.Options) {
			return nil, "", false
		}
	default:
		// A label match here is exact (both sides normalised the same
		// way), never a fuzzy overlap: "let me know when
		// auth is done" must not answer a prompt because "know" contains
		// a "No" option's label.
		for _, o := range p.Options {
			if BareWord(o.Label) == word {
				return o.Keys, o.Label, true
			}
		}
		return nil, "", false
	}
	keys, label, err := mapAnswer(p, word)
	if err != nil {
		return nil, "", false
	}
	return keys, label, true
}

// fastAnswerWindow is how long after a prompt was announced (its pending
// action created) a bare answer still goes straight to its keys. Not a
// knob: past it the brain decides, so a longer or shorter window changes
// only how often a model call is spent on a bare word.
const fastAnswerWindow = 60 * time.Second

// hasListener reports whether a line spoken now can be heard (Listener);
// true for a Pusher that cannot tell. Invariant it serves in awaitReply:
// a reply the shepherd is owed is heard, or held until someone can hear
// it, never spoken into an empty room. The page can leave between the
// tool call and its result, so this cannot be made unrepresentable; it
// is checked when the result would be returned.
func (s *Supervisor) hasListener() bool {
	if l, ok := s.pusher.(Listener); ok {
		return l.HasListener()
	}
	return true
}

// The echo guard. Invariant it protects (docs/iteration-2.md rule 5):
// an utterance is the shepherd's own words — the brain and the one rail
// both stand on recorded shepherd input — and the assistant's speech
// coming back through the phone's microphone is not. On 2026-09-24
// 18:23:50 the iOS speaker played "No change, all four are idle…", the
// mic picked it up, Gemini interrupted itself and transcribed "new
// change" 1.7 s after its last output transcript, and the daemon relayed
// that to the brain as the shepherd's question. The architecture cannot
// make this unrepresentable from here: the client may have no echo
// cancellation and the voice model reports only text, so text and
// timing are all the daemon has to tell the two apart.
//
// On speakerphone it is worse (docs/bench/tier2-2026-10-03.md #3): with
// the bot's audio leaking back at −15 to −18 dB the voice model
// transcribed its own speech, often glued and without spaces ("Sent
// it.I'mwaitingI'll let you…"), sometimes after the shepherd's own words
// in the same input, and the daemon relayed it. So each voice input of a
// claimed run is checked on its own:
//
//   - only an input that began while the voice was speaking (or the
//     client still playing it) or within echoWindow of the end of its
//     playback can be an echo: its saidBefore is the voice's speech of
//     that window, the current run and the runs that ended within it
//     (the echoed audio trails the transcript). An input that began
//     before is never touched — a barge-in over the voice keeps working
//     as long as its words differ from what was played;
//   - a bare fast-path phrase (yes/no/stop, an option pick, the reset
//     phrases) is never an echo, even when it repeats the assistant;
//   - the input is an echo when its words line up with a run of what was
//     played (echoOf: word by word, 80 % covered, one short word
//     forgiven — "new" heard for "no"), or when, compared letter by
//     letter with spaces and punctuation gone (so glued words and
//     Chinese compare the same way), stretches of what was played cover
//     at least echoCoverMin of it (echoCover);
//   - an input that opens or ends with a long stretch of what was played
//     (echoGlueMin) and has other words too keeps only those words: the
//     echo is cut off, the shepherd's part is relayed.
//
// A whole echo is logged (supervisor.echo_dropped, both texts), never
// relayed, and leaves the input ledger, so it can neither authorise an
// answer to a prompt nor be glued onto the next utterance; the call gets
// a silent result. It also leaves the brain's window (Brain.Echo): the
// words reached the brain as the CEO's before the guard saw them, and
// left there a later turn would read `CEO said: "new change"`. An
// input with the echo cut off is logged with partial and the words kept.

// echoWindow is how long after the voice's last output a new input may
// still be its echo. The logged pair was 1.7 s apart: the transcript of
// the voice's speech runs ahead of the audio the phone plays, and the
// input transcription arrives after the echoed audio ends.
//
// The window is anchored to the end of playback when the client reports
// it (docs/ws-contract.md "Playback reports": the web page, and the iOS
// app since herdr-sheepdog-ios #14): Gemini generates ~4x real time, so
// its turnComplete comes seconds before a long reply's tail is played,
// and an echo of that tail used to arrive after a window anchored at
// turnComplete had closed (docs/timelines.md C5). While the client says
// the reply is still playing (started, no drained yet) the voice counts
// as speaking, for up to echoPlayingMax after it stopped generating. A
// client without reports keeps the anchor at turnComplete.
const echoWindow = 2500 * time.Millisecond

// echoPlayingMax bounds how long a "started" report with no drained after
// it keeps the voice counted as speaking (a lost report must not keep
// the guard on forever): a 30 s reply generated at 4x plays ~22 s past
// its turnComplete.
const echoPlayingMax = 30 * time.Second

// echoKeepRuns is how many earlier speech runs the guard remembers.
const echoKeepRuns = 3

// echoWatch is what the voice is saying or last said, under s.mu.
type echoWatch struct {
	// said is the voice's output transcript since it last started
	// speaking; speaking is true until its turn completes or is
	// interrupted; at is when it last spoke (or stopped speaking);
	// since is when the previous speech stopped (a playback report
	// after it is about this speech).
	said     string
	speaking bool
	at       time.Time
	since    time.Time
	// start is when the current run began; prev are earlier runs,
	// oldest first.
	start time.Time
	prev  []echoRun
}

// echoRun is an earlier speech run and when it ended (its playback end
// when the client reported one).
type echoRun struct {
	said       string
	start, end time.Time
}

// echoSpokeLocked records a fragment of the voice's speech. Caller holds
// s.mu.
func (s *Supervisor) echoSpokeLocked(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	now := s.cfg.Now()
	if !s.echo.speaking {
		// A playback report after the previous speech ended is about this
		// one: the client's "started" often comes back before the first
		// output transcript fragment does.
		if said := strings.TrimSpace(s.echo.said); said != "" {
			s.echo.prev = append(s.echo.prev, echoRun{said: said, start: s.echo.start, end: s.echo.at})
			if len(s.echo.prev) > echoKeepRuns {
				s.echo.prev = s.echo.prev[len(s.echo.prev)-echoKeepRuns:]
			}
		}
		s.echo.said, s.echo.since, s.echo.start = "", s.echo.at, now
	}
	s.echo.said += text
	s.echo.speaking, s.echo.at = true, now
}

// voiceStopped records that the voice's turn completed or was
// interrupted: the window for an echo of it starts now.
func (s *Supervisor) voiceStopped() {
	s.mu.Lock()
	if s.echo.speaking {
		s.echo.speaking, s.echo.at = false, s.cfg.Now()
	}
	s.mu.Unlock()
}

// echoCandidateLocked is what a voice input starting now may be an echo
// of: the voice's speech while it speaks (or the client still plays it)
// or within echoWindow after, with the earlier runs that ended within
// echoWindow before it, else "". playback is the client's last playback
// report (zero: none). Caller holds s.mu.
func (s *Supervisor) echoCandidateLocked(playback voice.PlaybackReport, onset time.Time) string {
	now := s.cfg.Now()
	if s.spokeAfterPlaybackLocked(now, playback, onset) {
		return ""
	}
	cur := s.currentEchoLocked(now, playback)
	// Earlier runs: those that ended within echoWindow, and — while the
	// current run may be echoed — the ones chained to it by gaps under
	// echoWindow (the tail of one reply plays on as the next begins).
	var parts []string
	next := now
	if cur != "" {
		next = s.echo.start
	}
	for i := len(s.echo.prev) - 1; i >= 0; i-- {
		r := s.echo.prev[i]
		if now.Sub(r.end) > echoWindow && (cur == "" || next.Sub(r.end) > echoWindow) {
			break
		}
		parts = append([]string{r.said}, parts...)
		next = r.start
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	return strings.Join(parts, " ")
}

// echoTail is how long after the end of playback an echo can still
// begin: the speaker-to-microphone delay and the client detector's
// attack.
const echoTail = 300 * time.Millisecond

// spokeAfterPlaybackLocked reports whether the client's speech detector
// opened (onset, zero when the client reports none) after the voice had
// stopped and the client had finished playing it, echoTail allowed: an
// echo begins while the speaker plays, so words begun after it are the
// shepherd's — even when they repeat the voice ("fix those two links"
// to "Shall I fix those two links?"). Caller holds s.mu.
func (s *Supervisor) spokeAfterPlaybackLocked(now time.Time, playback voice.PlaybackReport, onset time.Time) bool {
	if onset.IsZero() || s.echo.speaking || s.echo.said == "" {
		return false
	}
	end := s.echo.at
	if playback.State != "" && playback.At.After(s.echo.since) {
		switch playback.State {
		case voice.PlaybackStarted:
			if now.Sub(s.echo.at) <= echoPlayingMax {
				return false // still playing
			}
		case voice.PlaybackDrained, voice.PlaybackFlushed:
			if playback.At.After(end) {
				end = playback.At
			}
		}
	}
	return onset.After(end.Add(echoTail))
}

// lastSpeechOnset is when the client's speech detector last opened, when
// the voice session knows (voice.Switcher).
func (s *Supervisor) lastSpeechOnset() (time.Time, bool) {
	if p, ok := s.pusher.(interface{ LastSpeechOnset() (time.Time, bool) }); ok {
		return p.LastSpeechOnset()
	}
	return time.Time{}, false
}

// currentEchoLocked is the current (or last) speech run when an input
// starting now may be its echo, else "". Caller holds s.mu.
func (s *Supervisor) currentEchoLocked(now time.Time, playback voice.PlaybackReport) string {
	if s.echo.said == "" {
		return ""
	}
	said := strings.TrimSpace(s.echo.said)
	if s.echo.speaking {
		return said
	}
	anchor := s.echo.at
	if playback.State != "" && playback.At.After(s.echo.since) {
		switch playback.State {
		case voice.PlaybackStarted:
			if now.Sub(s.echo.at) <= echoPlayingMax {
				return said // the shepherd is still hearing it
			}
		case voice.PlaybackDrained, voice.PlaybackFlushed:
			if playback.At.After(anchor) {
				anchor = playback.At
			}
		}
	}
	if now.Sub(anchor) > echoWindow {
		return ""
	}
	return said
}

// lastPlayback is the voice client's last playback report, when the
// voice session keeps them (voice.Switcher); zero otherwise.
func (s *Supervisor) lastPlayback() voice.PlaybackReport {
	if p, ok := s.pusher.(interface{ LastPlayback() voice.PlaybackReport }); ok {
		return p.LastPlayback()
	}
	return voice.PlaybackReport{}
}

// echoDropped applies the echo guard (above) to c's inputs. It reports
// true when nothing of c is left — the call is answered silently —
// and otherwise leaves c.text as the words that were not echo.
func (s *Supervisor) echoDropped(c *claim) bool {
	if c.in == nil || fastPathPhrase(c.text) {
		return false
	}
	ins := c.ins
	if len(ins) == 0 {
		ins = []*shepherdInput{c.in}
	}
	type cut struct{ heard, said, kept string }
	var cuts []cut
	var kept []string
	var newest *shepherdInput // the newest input left after the guard
	s.mu.Lock()
	for _, in := range ins {
		text := strings.TrimSpace(in.text)
		if in.kind != InputVoice || in.saidBefore == "" || text == "" || fastPathPhrase(text) {
			if text != "" {
				kept = append(kept, text)
			}
			newest = in
			continue
		}
		rest, echo := stripEcho(text, in.saidBefore)
		if !echo {
			kept = append(kept, text)
			newest = in
			continue
		}
		cuts = append(cuts, cut{heard: text, said: in.saidBefore, kept: rest})
		if rest == "" {
			in.used = true
			s.dropInputLocked(in)
			continue
		}
		in.text = rest
		kept = append(kept, rest)
		newest = in
	}
	s.mu.Unlock()
	if len(cuts) == 0 {
		return false
	}
	for _, x := range cuts {
		p := map[string]any{"heard": x.heard, "said": x.said}
		if x.kept != "" {
			p["partial"], p["kept"] = true, x.kept
		}
		if c.id != "" {
			p["utterance_id"] = c.id
		}
		s.log.Log(logSource, "supervisor.echo_dropped", "", p)
		if x.kept == "" && s.brain != nil {
			s.brain.Echo(x.heard)
		}
	}
	if len(kept) == 0 {
		return true
	}
	c.text = strings.Join(kept, " ")
	if newest != nil {
		// The rail reads the newest input left: a dropped echo after the
		// shepherd's "yes" must not stand in for it.
		c.in = newest
	}
	return false
}

// dropInputLocked removes in from the input ledger (a whole echo is not
// the shepherd's input). Caller holds s.mu.
func (s *Supervisor) dropInputLocked(in *shepherdInput) {
	for i, x := range s.inputs {
		if x == in {
			s.inputs = append(s.inputs[:i:i], s.inputs[i+1:]...)
			return
		}
	}
}

// fastPathPhrase reports whether text, as a whole, is one of the bare
// phrases the fast paths answer: never taken for an echo.
func fastPathPhrase(text string) bool {
	w := BareWord(text)
	_, option := bareOptionWords[w]
	return BareStopWords[w] || bareYesWords[w] || bareNoWords[w] || option || bareResetTriggerWords[w] || bareResetConfirmWords[w]
}

// echoOf reports whether heard lines up with a run of said: at least two
// words, at least 80% of them equal to said's at the same offsets, with
// one more differing word forgiven when both words are short (≤ 4
// letters: "new" heard for "no") or two edits apart ("the" heard for
// "those", echo-barge-in r3).
func echoOf(heard, said string) bool {
	in, out := echoWords(heard), echoWords(said)
	n := len(in)
	if n < 2 || n > len(out) {
		return false
	}
	allowed := n / 5 // 80% covered
	for p := 0; p+n <= len(out); p++ {
		hard, soft := 0, 0
		for k := range in {
			switch {
			case in[k] == out[p+k]:
			case len([]rune(in[k])) <= 4 && len([]rune(out[p+k])) <= 4,
				editDistance([]rune(in[k]), []rune(out[p+k])) <= 2:
				soft++
			default:
				hard++
			}
		}
		if hard <= allowed && hard+soft <= allowed+1 {
			return true
		}
	}
	return false
}

// echoWords lower-cases s and keeps its words' letters and digits.
func echoWords(s string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ToLower(s)) {
		w := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return r
			}
			return -1
		}, f)
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

const (
	// echoCoverMin is the share of an input's letters that stretches of
	// what was played must cover for the whole input to be an echo.
	echoCoverMin = 0.8
	// echoStretchLatin / echoStretchHan are the least letters a stretch
	// must have to count (a whole short input counts at
	// echoWholeLatin / echoWholeHan): a Chinese character carries about
	// what four Latin letters do.
	echoStretchLatin = 8
	echoStretchHan   = 3
	echoWholeLatin   = 5
	echoWholeHan     = 2
	// echoGlueLatin / echoGlueHan are the least letters an echo stretch
	// at the start or end of an input must have to be cut off it, the
	// rest relayed.
	echoGlueLatin = 12
	echoGlueHan   = 4
)

// stripEcho decides whether heard (an input that began in the echo
// window) is said coming back: a whole echo (rest "", echo true), an
// echo glued to the shepherd's words at its start or end (rest is their
// words, echo true), or not an echo (echo false).
func stripEcho(heard, said string) (rest string, echo bool) {
	if echoOf(heard, said) || assistantAck(heard) {
		return "", true
	}
	h, offs := normWordsAt(heard)
	sd := string(normWords(said))
	if len(h) == 0 || sd == "" {
		return heard, false
	}
	covered := make([]bool, len(h))
	n := 0
	for i := 0; i < len(h); {
		l := longestIn(h[i:], sd)
		if l > 0 && (l >= stretchMin(h[i:i+l], echoStretchLatin, echoStretchHan) || (i == 0 && l == len(h) && l >= stretchMin(h, echoWholeLatin, echoWholeHan))) {
			for k := i; k < i+l; k++ {
				covered[k] = true
			}
			n += l
			i += l
			continue
		}
		i++
	}
	if n == 0 {
		return heard, false
	}
	if float64(n) >= echoCoverMin*float64(len(h)) {
		return "", true
	}
	// Glued: a long echo stretch at the start and/or the end.
	lead := 0
	for lead < len(h) && covered[lead] {
		lead++
	}
	trail := 0
	for trail < len(h)-lead && covered[len(h)-1-trail] {
		trail++
	}
	if lead < stretchMin(h[:lead], echoGlueLatin, echoGlueHan) {
		lead = 0
	}
	if trail < stretchMin(h[len(h)-trail:], echoGlueLatin, echoGlueHan) {
		trail = 0
	}
	if lead == 0 && trail == 0 {
		return heard, false
	}
	from, to := 0, len(heard)
	if lead > 0 {
		_, size := utf8.DecodeRuneInString(heard[offs[lead-1]:])
		from = offs[lead-1] + size
	}
	if trail > 0 {
		to = offs[len(h)-trail]
		// A stretch can begin inside the shepherd's last word ("me?
		// Bench…" matches "done. Bench…" from its "e"): give the word
		// back, a few letters at most.
		for k := 0; k < 3 && to < len(heard) && to > 0; k++ {
			prev, _ := utf8.DecodeLastRuneInString(heard[:to])
			next, size := utf8.DecodeRuneInString(heard[to:])
			if !unicode.IsLetter(prev) || !unicode.IsLetter(next) || unicode.Is(unicode.Han, next) {
				break
			}
			to += size
		}
	}
	if from >= to {
		return "", true
	}
	rest = strings.TrimFunc(heard[from:to], func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if len(normWords(rest)) < 2 {
		return "", true
	}
	return rest, true
}

// assistantAckCues are what only the assistant says: "On it.",
// "Checking.", "I'll let you know", 我查一下. In echo-barge-in the voice
// said "On " — its transcript cut there, its audio did not — and the
// microphone brought back "On it."; the daemon relayed it and the brain
// took it for the CEO's go-ahead to an offer (an instruction nobody
// gave). Heard in the echo window, such words are the voice's own.
var assistantAckCues = regexp.MustCompile(`(?i)^\W*(on it|checking( on it| now| that)?|let me check( that)?|i'?ll (check|look into it|find out|let you know|tell it|pass it on)|one moment|just a (moment|sec|second)|right away|looking into it|working on it|我(来|去)?(查|看)一下|稍等|马上|正在(查|看))\W*$`)

// assistantAck reports whether heard is only an assistant-style
// acknowledgement (assistantAckCues).
func assistantAck(heard string) bool { return assistantAckCues.MatchString(heard) }

// normWordsAt is normWords with the byte offset of each kept rune in s.
func normWordsAt(s string) ([]rune, []int) {
	var out []rune
	var offs []int
	for i, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out = append(out, unicode.ToLower(r))
			offs = append(offs, i)
		}
	}
	return out, offs
}

// longestIn is the length of the longest prefix of h found in s.
func longestIn(h []rune, s string) int {
	lo, hi := 0, len(h)
	// Prefix containment is monotone: binary search the length.
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if strings.Contains(s, string(h[:mid])) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// stretchMin is the least length a stretch like r must have: han for a
// stretch with a Chinese character in it, latin otherwise.
func stretchMin(r []rune, latin, han int) int {
	for _, x := range r {
		if unicode.Is(unicode.Han, x) {
			return han
		}
	}
	return latin
}
