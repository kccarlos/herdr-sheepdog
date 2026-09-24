package supervisor

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// One decision-maker (docs/roles.md): the voice relays whatever the CEO
// said verbatim and decides nothing. Two deterministic fast paths run
// here, in code, before any model — a bare stop, and a bare answer to
// the one open prompt — and everything else is one brain turn over the
// words, which resolves what they are and acts. With no brain the
// daemon answers from the roster and sends nothing: judging what words
// mean is the brain's work, and there is no second model for it.

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

// ToSupervisor handles the voice's relay tool: the CEO's words, verbatim.
func (s *Supervisor) ToSupervisor(ctx context.Context, text string) (voice.ToolResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return voice.ToolResult{Spoken: "Sorry, what was that?", Clarify: true, Scheduling: voice.UrgencyInterrupt}, nil
	}
	if s.isEcho(text) {
		// The voice relayed a push back through to_supervisor as if the
		// CEO had said it (a wasted turn at best; with different phrasing
		// it could look like authorisation). Nothing was said: no fast
		// path, no brain turn, and the rail never sees these words as the
		// CEO's own (docs/history/issues-2026-09-18.md Bug 2).
		s.log.Log(logSource, "supervisor.echo_ignored", "", map[string]any{"text": text})
		return voice.ToolResult{Scheduling: voice.UrgencySilent}, nil
	}
	in, fresh := s.claimInput(ctx)
	if !fresh {
		// Nothing the shepherd said, typed or tapped since the previous
		// relay or the last push: the voice model is acting on its own
		// (2026-09-23: "yes" right after a push, with the mic muted).
		// Nothing happens and nothing is said (inputs.go).
		s.log.Log(logSource, "supervisor.voice_initiative_ignored", "", map[string]any{"text": text})
		return voice.ToolResult{Scheduling: voice.UrgencySilent}, nil
	}
	// Bare stop first: "no" is an answer, "no no" is a stop.
	if bareStop(text) {
		return s.Stop(ctx, StopTriggerBareWord)
	}
	// The "New conversation" bare-phrase trigger and its confirm
	// (docs/roles.md "Triggers" (b)): a fast path, like the stop word
	// above — no model, and only this claimed shepherd input can ask or
	// confirm it.
	if res, ok := s.resetConfirmFastPath(ctx, text); ok {
		return res, nil
	}
	if s.cfg.ReplyWait < 0 {
		// Synchronous: the turn runs under the tool call's own context
		// and its reply is always the tool result.
		turnCtx, u := s.beginUtterance(ctx, text, callIDOf(ctx))
		defer s.endUtterance(u)
		if res, ok := s.fastAnswer(turnCtx, in); ok {
			return res, nil
		}
		return s.inlineResult(turnCtx, u, s.utteranceTurn(turnCtx, u, text)), nil
	}
	// The turn is detached from the tool call: the voice cancels a
	// handler's context as soon as the handler returns, and the turn may
	// outlive it. Like an investigation job it runs on its own context,
	// bounded by the brain's own turn timeout; the stop procedure cancels
	// it through the slot.
	turnCtx, u := s.beginUtterance(context.Background(), text, callIDOf(ctx))
	if res, ok := s.fastAnswer(turnCtx, in); ok {
		s.endUtterance(u)
		return res, nil
	}
	return s.awaitReply(ctx, turnCtx, u, text), nil
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
func (s *Supervisor) awaitReply(ctx, turnCtx context.Context, u *inflightUtterance, text string) voice.ToolResult {
	start := s.cfg.Now()
	s.replies.Add(1)
	go func() {
		defer s.replies.Done()
		out := s.utteranceTurn(turnCtx, u, text)
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
	s.mu.Lock()
	switch {
	case !u.done:
		u.detached = true
		s.mu.Unlock()
	case ctx.Err() != nil:
		// The reply landed just as the voice dropped the call: the tool
		// result would be discarded, so it is pushed instead.
		out := u.out
		s.mu.Unlock()
		s.replies.Add(1)
		go func() {
			defer s.replies.Done()
			s.deliverLate(turnCtx, u, out, start)
		}()
	default:
		out := u.out
		s.mu.Unlock()
		defer s.endUtterance(u)
		return s.inlineResult(turnCtx, u, out)
	}
	payload := map[string]any{"id": u.id, "waited_ms": s.cfg.Now().Sub(start).Milliseconds(), "cancelled": ctx.Err() != nil}
	if id, ok := ctx.Value(callIDKey{}).(string); ok {
		payload["call_id"] = id
	}
	s.log.Log(logSource, "brain.reply_pending", "", payload)
	return voice.ToolResult{Detail: replyPendingDetail, Pending: true, Scheduling: voice.UrgencySilent}
}

// inlineResult turns a finished turn into the tool result: dropped when
// the stop procedure (or the voice) cancelled it, the reply when there
// is one, else the roster sentence.
func (s *Supervisor) inlineResult(turnCtx context.Context, u *inflightUtterance, out utteranceOutcome) voice.ToolResult {
	if s.utteranceDropped(u) || turnCtx.Err() != nil {
		// Stopped, or the voice cancelled the call: the stop procedure
		// speaks, and the brain's reply is discarded.
		return droppedResult()
	}
	if out.ok {
		// A reply that named an agent counts as dealing with it, like a
		// spoken briefing does (the normal level's chattiness rule).
		s.mentionedIn(out.res.Spoken)
		return out.res
	}
	return s.noBrain()
}

// deliverLate pushes the reply of a turn that outlived its tool call,
// tagged answers_utterance, and ends its slot. A stopped turn delivers
// nothing. A reply is stale once the shepherd's next request was
// accepted (acceptedSeq moved): the shepherd has moved on, so a turn
// that did nothing is dropped, and one that acted is pushed silent — the
// voice must still know what was done, but it is no longer the answer to
// what is being discussed. Otherwise the reply is spoken at the next
// pause, with its subject in front when it is about one agent, and a
// failed turn gives the roster sentence exactly as the inline path does.
func (s *Supervisor) deliverLate(turnCtx context.Context, u *inflightUtterance, out utteranceOutcome, start time.Time) {
	cancelled := turnCtx.Err() != nil
	s.mu.Lock()
	dropped := u.dropped
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
		s.log.Log(logSource, "brain.reply_stale", "", map[string]any{"id": u.id, "acted": out.acted})
		if !out.ok || !out.acted {
			return
		}
	}
	var paneID, line string
	urgency := voice.UrgencyIdle
	if out.ok {
		paneID, line = s.lateLine(out.res, u.text)
		if out.needsUser && s.answerPromptOpen() {
			// The shepherd must decide on an open prompt: the reply may
			// ask for the floor, but it is not tagged needs_user — that
			// exception is a decision on a block the push itself brings
			// (announce.go's announceTags), so the voice still waits out
			// its own sentence (docs/roles.md "Pushes").
			urgency = voice.UrgencyInterrupt
		}
	} else {
		line = s.noBrain().Spoken
	}
	if stale {
		urgency = voice.UrgencySilent
	} else if out.ok {
		s.mentionedIn(out.res.Spoken)
	}
	tags := map[string]any{"answers_utterance": u.id}
	if u.callID != "" {
		tags[tagCallID] = u.callID
	}
	s.pushTagged(paneID, line, urgency, tags)
	s.log.Log(logSource, "brain.reply_delivered", paneID, map[string]any{
		"id": u.id, "dur_ms": s.cfg.Now().Sub(start).Milliseconds(), "urgency": urgency.String(), "acted": out.acted, "fallback": !out.ok,
	})
}

// lateLine is the pushed form of a late reply: its subject in front when
// it is about one agent ("About auth: …") unless it already opens with
// that agent's name — heard out of the blue, it must say what it is
// about — and its options as a choice, as the voice would have read them
// from a tool result. The spoken text and options are already D9-scrubbed
// (toolResultFrom); detail stays out, since everything pushed is spoken.
func (s *Supervisor) lateLine(res voice.ToolResult, asked string) (paneID, line string) {
	line = res.Spoken
	if len(res.Options) > 0 {
		line += " Options: " + strings.Join(res.Options, "; ") + "."
	}
	row, ok := s.subjectOf(res.Spoken, asked)
	if !ok {
		return "", line
	}
	alias := aliasOr(row.Alias)
	if !strings.HasPrefix(strings.ToLower(res.Spoken), strings.ToLower(alias)) {
		line = "About " + alias + ": " + line
	}
	return row.PaneID, line
}

// subjectOf is the one agent a reply is about: the one it names, else —
// when it names none — the one the shepherd's words named. Several named
// means no single subject.
func (s *Supervisor) subjectOf(spoken, asked string) (state.Agent, bool) {
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
func (s *Supervisor) beginUtterance(parent context.Context, text, callID string) (context.Context, *inflightUtterance) {
	turnCtx, cancel := context.WithCancel(parent)
	u := &inflightUtterance{text: text, cancel: cancel, finished: make(chan struct{}), callID: callID}
	s.mu.Lock()
	u.id = s.nextID("utt")
	u.accepted = s.acceptedSeq
	// A second utterance while one is in flight only takes the slot over:
	// the older turn is left to finish (the brain's turn lock serialises
	// them anyway); cancelling it would lose what it was doing.
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
func (s *Supervisor) utteranceTurn(ctx context.Context, u *inflightUtterance, text string) utteranceOutcome {
	if s.brain == nil {
		return utteranceOutcome{}
	}
	// The roster sentence travels with the request, so a status question
	// costs no tool call (D1-revised); the pending prompt with its
	// option keys and the open promises are in the brain's own roster
	// and ledger sections.
	brief, _ := s.briefingText()
	req := brain.Request{Kind: brain.KindUtterance, Trigger: brain.TriggerCEO, Question: text, Briefing: brief, Link: callLink(u.callID)}
	// supervisor.repeat_window_s: an identical roster delivered again
	// inside the window is noise over voice, so the brain gets a note it
	// can act on ("no change since I last checked") instead of the
	// shepherd hearing the same sentence twice.
	if age, repeat := s.briefingRepeat(brief); repeat {
		req.RosterUnchangedFor = age
		s.log.Log(logSource, "supervisor.briefing_repeat", "", map[string]any{"roster": brief, "age_s": int(age.Seconds())})
	}
	s.mu.Lock()
	u.inBrain = true
	s.mu.Unlock()
	rep, err := s.brain.Turn(ctx, req)
	if err != nil {
		return utteranceOutcome{}
	}
	res := toolResultFrom(rep)
	if res.Spoken == "" {
		return utteranceOutcome{acted: rep.Acted}
	}
	return utteranceOutcome{res: res, ok: true, acted: rep.Acted, needsUser: rep.NeedsUser}
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

// isEcho reports whether text equals a push spoken within echoWindow
// (case/punctuation-insensitive), or is a long contiguous whole-word run
// taken from one: the voice relaying its own announcement back through
// to_supervisor rather than something the CEO said (docs/issues-2026-09-
// 18.md Bug 2). A bare short answer (yes, no, approve, an option pick, a
// stop word) is never an echo, however it overlaps a recent push's own
// words — a substring match without word boundaries let a bare "no"
// count as an echo of any push containing "now", "not", "know" or
// "another", and let "approve" match a brain block line ending
// "— approve?" (audit B1).
func (s *Supervisor) isEcho(text string) bool {
	word := BareWord(text)
	if BareStopWords[word] || bareYesWords[word] || bareNoWords[word] || bareOptionWords[word] > 0 {
		return false
	}
	norm := normalizeEcho(text)
	if norm == "" {
		return false
	}
	words := strings.Fields(norm)
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.cfg.Now().Add(-echoWindow)
	for _, p := range s.recentPushes {
		if p.at.Before(cutoff) {
			continue
		}
		pn := normalizeEcho(p.text)
		if pn == "" {
			continue
		}
		if pn == norm {
			return true
		}
		pushWords := strings.Fields(pn)
		// A whole-word run of at least 4 words, covering at least 60% of
		// the push, taken from it contiguously: enough to be recognisably
		// "the push, spoken back", not an incidental word or two shared
		// with a genuinely new instruction.
		if len(words) >= 4 && float64(len(words)) >= 0.6*float64(len(pushWords)) && containsContiguousWords(pushWords, words) {
			return true
		}
	}
	return false
}

// containsContiguousWords reports whether needle appears as a contiguous,
// in-order run within haystack.
func containsContiguousWords(haystack, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j, w := range needle {
			if haystack[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// normalizeEcho lower-cases text and collapses every run of non-letter,
// non-digit characters to a single space, so the echo comparison ignores
// case and punctuation.
func normalizeEcho(text string) string {
	var b strings.Builder
	space := true // trims the leading run too
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// fastAnswer is the bare-answer fast path: the shepherd's whole input
// (what was heard, typed or tapped — never the tool argument) answers
// the one open answer_prompt, so the keys go straight to the pane
// without a model, when the one rail's reading of that input allows it
// (inputs.go). ok is false for anything else, which goes to the brain.
func (s *Supervisor) fastAnswer(ctx context.Context, in *shepherdInput) (voice.ToolResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := strings.TrimSpace(in.text)
	p, has := s.store.Pending()
	switch {
	case has && p.Kind == state.PendingAnswerPrompt && s.cfg.Now().Before(p.ExpiresAt):
		// Live and unexpired: the ordinary case.
	case !has && s.expired != nil:
		// The pane's answer_prompt pending expired while nobody answered,
		// but the prompt may still be on screen: revive it before mapping
		// the bare word onto its keys (docs/history/issues-2026-09-18.md "an
		// expired approval prompt can never be approved"). s.expired only
		// says which pane; revival always re-reads the screen.
		revived, ok := s.revivePrompt(ctx, s.expired.PaneID, "fast_path")
		if !ok {
			return voice.ToolResult{}, false
		}
		p = revived
	default:
		return voice.ToolResult{}, false
	}
	keys, label, ok := bareAnswer(p, text)
	if !ok {
		return voice.ToolResult{}, false
	}
	if rin, reason := s.railInputLocked(p, keys); reason != "" || rin != in {
		if reason == "" {
			reason = "the shepherd said something newer than the words being answered"
		}
		s.railBlockedLocked(p, keys, "fast_path", reason)
		return voice.ToolResult{}, false
	}
	alias := s.aliasOf(p.PaneID)
	s.log.Log(logSource, "supervisor.fast_path", p.PaneID, map[string]any{"path": "answer", "text": text, "answer": label, "id": p.ID, "input": string(in.kind)})
	res, sent := s.answerKeys(ctx, p, keys, label, "Told "+alias+" %s.", "fast_path", nil)
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
