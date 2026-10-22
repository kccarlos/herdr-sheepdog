package supervisor

import (
	"context"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// One decision-maker (docs/dev/roles.md): the voice hands over whatever the
// user said and decides nothing. The words every path below runs on are
// the ones the daemon recorded (inputs.go's claimInput) — the voice's
// input transcription, typed text or a tap — never the voice model's
// to_supervisor argument, which paraphrased, invented and cut
// sentences short (2026-09-23/24); the argument is a hint, logged
// beside them. Two deterministic fast paths run here, in code, before
// any model — a bare stop, and a bare answer to the one open prompt —
// and everything else is one orchestrator turn over the words, which resolves
// what they are and acts. With no orchestrator the daemon answers from the
// roster and sends nothing: judging what words mean is the orchestrator's
// work, and there is no second model for it.

// inflightUtterance is the one to_supervisor call being handled, under
// s.mu: what the user said, how to cancel the turn it is (or is about to
// be) in, and whether the orchestrator already has it. The stop procedure's
// first two phases are this slot. It stays installed until the orchestrator's
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
	// start is when the user began saying these words (claim.start;
	// the earlier one when two utterances coalesce): the orchestrator is told
	// what they had heard by then (heard.go).
	start time.Time
	// callID is the voice's tool call id that relayed the words: the
	// orchestrator request and the late push carry it (links.go).
	callID string
	// reading is the voice's own reading of the words (claim.reading).
	reading string
	// echoNote says the words began while the voice's line was still
	// playing (echoNote): a fact for the orchestrator.
	echoNote string
	// ins are the user's inputs these words came from (claim.ins, both
	// utterances' when two coalesce): what a failed turn leaves
	// unanswered, for a re-relay to retry (rerelay.go).
	ins []*shepherdInput
	// retryOf is the utterance a retry runs again (claim.retryOf): its
	// unanswered record gives way to this one's outcome (unanswered.go).
	retryOf string
	// accepted is s.acceptedSeq once this call was accepted: when it has
	// moved by the time a late reply lands, the user asked for
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
	// coalesce looked at it. started is set (by the orchestrator's turn lock,
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
// when there was no orchestrator, or it could not answer, and the daemon must
// speak for itself.
type utteranceOutcome struct {
	res voice.ToolResult
	ok  bool
	// acted: the turn sent something to a worker or recorded something
	// (brain.Reply.Acted).
	acted bool
	// needsUser: the reply says the user must decide or act.
	needsUser bool
	// actedOn: the panes the turn's actions went to (brain.Reply.ActedOn).
	actedOn []string
	// focus is what the reply says about the conversation's focus
	// (focus.go), for the voice once it has spoken the reply.
	focus focusFacts
	// err is why the turn gave no answer (the orchestrator's error),
	// for the fallback's facts; nil when it answered or there is none.
	err error
}

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

// UtteranceOf is the words an utterance runs on (docs/dev/roles.md "One
// decision-maker"): what the daemon recorded the user saying, typing
// or tapping (heard), and the voice model's to_supervisor argument
// (said) only when nothing was recorded — fallback is true then. The
// eval harness uses it to put the same words in front of the orchestrator as
// the daemon does.
func UtteranceOf(heard, said string) (text string, fallback bool) {
	if h := strings.TrimSpace(heard); h != "" {
		return h, false
	}
	return strings.TrimSpace(said), true
}

// ToSupervisor handles the voice's relay tool. said is the model's
// argument; the utterance is what the user said (UtteranceOf).
func (s *Supervisor) ToSupervisor(ctx context.Context, said string) (voice.ToolResult, error) {
	return s.ToSupervisorRead(ctx, said, "")
}

// ToSupervisorRead is ToSupervisor with the voice's own short reading
// of the words (the call's optional "reading" argument), which reaches
// the orchestrator labelled as the voice's hint, never as the user's words.
func (s *Supervisor) ToSupervisorRead(ctx context.Context, said, reading string) (voice.ToolResult, error) {
	said = strings.TrimSpace(said)
	c, fresh := s.claimInput(ctx)
	if !fresh && ctx.Err() == nil {
		// Not when the voice already dropped the call: a retry turn
		// would run detached for nobody (rerelay.go).
		c, fresh = s.claimRetry()
	}
	if !fresh {
		// Nothing the user said, typed or tapped since the previous
		// relay: the call is the voice model's own (2026-09-23: "yes"
		// right after a push, with the mic muted). No orchestrator turn runs,
		// since only the user's words authorise one (the rail). The voice
		// is told so as a fact, silently, instead of a code guess at
		// whether it relayed a briefing back (selfRelay, removed in
		// dual-brain ablation G2).
		s.log.Log(logSource, "supervisor.voice_initiative_ignored", "", map[string]any{"text": said})
		return voice.ToolResult{Spoken: s.noFreshWordsFact(), Scheduling: voice.UrgencySilent}, nil
	}
	text, fallback := UtteranceOf(c.text, said)
	payload := map[string]any{"heard": c.text, "said": said, "inputs": c.n, "input": string(c.in.kind), "utterance_id": c.id}
	if c.retryOf != "" {
		payload["retry_of"] = c.retryOf
		payload["retry_reason"] = c.retryReason
	}
	// waited_ms: from the user's last words (the input's last
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
	c.reading = strings.TrimSpace(reading)
	if c.reading != "" {
		payload["reading"] = c.reading
	}
	res := s.handleUtterance(ctx, c, callIDOf(ctx), false)
	if res.Spoken != "" && !res.Pending {
		// The voice tags the playback of this answer with it
		// (turn.latency's first audio).
		res.UtteranceID = c.id
	}
	return res, nil
}

// handleUtterance runs the user's claimed words: the fast paths,
// then one orchestrator turn. A relay (daemon false) returns the reply as the
// tool result, waiting up to Config.ReplyWait for it; the daemon's own
// relay of an unanswered utterance (relay.go) has no tool call to answer,
// so its turn's reply is always pushed (deliverLate), and only a fast
// path's result is returned, for the caller to push.
func (s *Supervisor) handleUtterance(ctx context.Context, c claim, callID string, daemon bool) voice.ToolResult {
	note := echoNote(c)
	if note != "" {
		s.log.Log(logSource, "supervisor.echo_possible", "", map[string]any{"text": c.text, "utterance_id": c.id})
	}
	text := c.text
	// Bare stop first: "no" is an answer, "no no" is a stop.
	if bareStop(text) {
		res, _ := s.Stop(ctx, StopTriggerBareWord)
		return res
	}
	// The "New conversation" bare-phrase trigger and its confirm
	// (docs/dev/roles.md "Triggers" (b)): a fast path, like the stop word
	// above — no model, and only this claimed user input can ask or
	// confirm it.
	if res, ok := s.resetConfirmFastPath(ctx, text); ok {
		return res
	}
	sync := s.cfg.ReplyWait < 0 && !daemon
	// The turn is detached from the tool call unless it is synchronous:
	// the voice cancels a handler's context as soon as the handler
	// returns, and the turn may outlive it. Like an investigation job it
	// runs on its own context, bounded by the orchestrator's own turn timeout;
	// the stop procedure cancels it through the slot, and shutdown
	// through s.life.
	parent := s.life
	if sync {
		parent = ctx
	}
	turnCtx, u := s.beginUtterance(parent, text, callID, c.id)
	s.mu.Lock()
	u.start = c.start
	u.reading = c.reading
	u.echoNote = note
	u.ins = c.ins
	u.retryOf = c.retryOf
	s.mu.Unlock()
	if s.cfg.bareAnswers || s.brain == nil {
		// With an orchestrator, the orchestrator reads every answer (dual-brain
		// ablation G4); the bare-answer fast path is left for a
		// brain-less daemon (--brain=off), where nothing else could,
		// and for the test harness (Config.bareAnswers).
		if res, ok := s.fastAnswer(turnCtx, c.in, text); ok {
			s.endUtterance(u)
			return res
		}
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
// one's orchestrator turn is still queued behind another turn (docs/dev/roles.md
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
	u.ins = append(append([]*shepherdInput(nil), p.ins...), u.ins...)
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
	// older turn is left to finish (the orchestrator's turn lock serialises them
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
	return voice.ToolResult{Detail: "Merged with what the user said next; one answer follows for both.", Pending: true, Scheduling: voice.UrgencySilent}
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

// utteranceTurn runs one orchestrator turn over the user's words. ok is false
// when there was no orchestrator, or it could not answer, and the daemon must
// speak for itself; a turn the stop procedure cancelled never returns
// ok, and the caller checks utteranceDropped rather than falling through.
func (s *Supervisor) utteranceTurn(ctx context.Context, u *inflightUtterance) utteranceOutcome {
	if s.brain == nil {
		return utteranceOutcome{}
	}
	s.mu.Lock()
	text, start, reading, echo := u.text, u.start, u.reading, u.echoNote
	s.mu.Unlock()
	// The roster sentence travels with the request, so a status question
	// costs no tool call (D1-revised); the pending prompt with its
	// option keys and the open promises are in the orchestrator's own roster
	// and ledger sections.
	brief, _ := s.briefingText()
	req := brain.Request{Kind: brain.KindUtterance, Trigger: brain.TriggerCEO, Question: text, VoiceReading: reading, DaemonNote: echo, Briefing: brief, Link: callLink(u.callID), UtteranceID: u.id}
	// The agent the conversation is on, for words that say "it" (the
	// orchestrator's agentRecords).
	s.mu.Lock()
	subject := s.lastSubject
	s.mu.Unlock()
	if row, ok := s.store.Get(subject); ok {
		req.Focus = row.Alias
	}
	// What the user had heard when they began: evaluated when the
	// turn starts, since it may queue behind an announce turn whose
	// line goes out meanwhile (heard.go).
	req.Facts = func() brain.EventFacts {
		return brain.EventFacts{Heard: s.heardFacts(start), Language: s.ceoLanguage(), OpenPrompt: s.openPromptFact(start)}
	}
	// supervisor.repeat_window_s: an identical roster delivered again
	// inside the window is noise over voice, so the orchestrator gets a note it
	// can act on ("no change since I last checked") instead of the
	// user hearing the same sentence twice. Delivered means answered:
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
		return utteranceOutcome{err: err}
	}
	res := toolResultFrom(rep, prose.EarRunaway)
	if res.Spoken == "" {
		return utteranceOutcome{acted: rep.Acted}
	}
	s.markBriefingDelivered(brief)
	return utteranceOutcome{res: res, ok: true, acted: rep.Acted, actedOn: rep.ActedOn, needsUser: rep.NeedsUser, focus: focusFromReply("", res.Spoken, rep)}
}
