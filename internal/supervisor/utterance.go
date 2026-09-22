package supervisor

import (
	"context"
	"fmt"
	"strings"
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
// first two phases are this slot.
type inflightUtterance struct {
	text    string
	cancel  context.CancelFunc
	inBrain bool
	// dropped is set by the stop procedure: the reply is discarded
	// rather than answered, so these words reach no worker after all.
	dropped bool
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
		// CEO's own (docs/issues-2026-09-18.md Bug 2).
		s.log.Log(logSource, "supervisor.echo_ignored", "", map[string]any{"text": text})
		return voice.ToolResult{Scheduling: voice.UrgencySilent}, nil
	}
	// Bare stop first: "no" is an answer, "no no" is a stop.
	if bareStop(text) {
		return s.Stop(ctx, StopTriggerBareWord)
	}
	turnCtx, u := s.beginUtterance(ctx, text)
	defer s.endUtterance(u)
	if res, ok := s.fastAnswer(turnCtx, text); ok {
		return res, nil
	}
	res, ok := s.utteranceTurn(turnCtx, u, text)
	if u.stopped() || turnCtx.Err() != nil {
		// Stopped, or the voice cancelled the call: the stop procedure
		// speaks, and the brain's reply is discarded.
		return droppedResult(), nil
	}
	if ok {
		// A reply that named an agent counts as dealing with it, like a
		// spoken briefing does (the normal level's chattiness rule).
		s.mentionedIn(res.Spoken)
		return res, nil
	}
	return s.noBrain(), nil
}

// beginUtterance installs the in-flight slot and returns the turn's
// cancellable context (the stop procedure cancels it).
func (s *Supervisor) beginUtterance(ctx context.Context, text string) (context.Context, *inflightUtterance) {
	turnCtx, cancel := context.WithCancel(ctx)
	u := &inflightUtterance{text: text, cancel: cancel}
	s.mu.Lock()
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

// stopped reports whether the stop procedure dropped this utterance.
func (u *inflightUtterance) stopped() bool {
	if u == nil {
		return false
	}
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
// ok, and the caller checks u.stopped() rather than falling through.
func (s *Supervisor) utteranceTurn(ctx context.Context, u *inflightUtterance, text string) (voice.ToolResult, bool) {
	if s.brain == nil {
		return voice.ToolResult{}, false
	}
	// The roster sentence travels with the request, so a status question
	// costs no tool call (D1-revised); the pending prompt with its
	// option keys and the open promises are in the brain's own roster
	// and ledger sections.
	brief, _ := s.briefingText()
	req := brain.Request{Kind: brain.KindUtterance, Trigger: brain.TriggerCEO, Question: text, Briefing: brief}
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
		return voice.ToolResult{}, false
	}
	res := toolResultFrom(brain.KindUtterance, rep)
	if res.Spoken == "" {
		return voice.ToolResult{}, false
	}
	return res, true
}

// The bare fast-path words (docs/roles.md "One decision-maker"). *Bare*
// means the whole utterance, so "don't stop the tests" never matches a
// stop and "yes please run it" never answers a prompt.
var (
	bareStopWords   = map[string]bool{"stop": true, "stop stop": true, "wait": true, "cancel": true, "never mind": true, "nevermind": true, "no no": true, "hold on": true}
	bareYesWords    = map[string]bool{"yes": true, "yeah": true, "yep": true, "approve": true, "go ahead": true}
	bareNoWords     = map[string]bool{"no": true, "nope": true}
	bareOptionWords = map[string]int{"option one": 1, "option two": 2, "option three": 3, "option 1": 1, "option 2": 2, "option 3": 3}
)

// bareWord lower-cases the utterance and strips punctuation, so the
// fast paths compare whole utterances only.
func bareWord(text string) string {
	return strings.Trim(strings.ToLower(strings.Join(strings.Fields(text), " ")), ".!?,;: ")
}

// bareStop reports whether the whole utterance is one of the stop words.
func bareStop(text string) bool { return bareStopWords[bareWord(text)] }

// isEcho reports whether text equals, or is contained in, a push spoken
// within echoWindow (case/punctuation-insensitive): the voice relaying
// its own announcement back through to_supervisor rather than something
// the CEO said (docs/issues-2026-09-18.md Bug 2).
func (s *Supervisor) isEcho(text string) bool {
	norm := normalizeEcho(text)
	if norm == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.cfg.Now().Add(-echoWindow)
	for _, p := range s.recentPushes {
		if p.at.Before(cutoff) {
			continue
		}
		if pn := normalizeEcho(p.text); pn != "" && strings.Contains(pn, norm) {
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

// fastAnswer is the bare-answer fast path: the whole utterance answers
// the one open answer_prompt, so the keys go straight to the pane
// without a model (the CEO's verbatim word is what the rail asks for).
// ok is false for anything else, which goes to the brain.
func (s *Supervisor) fastAnswer(ctx context.Context, text string) (voice.ToolResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, has := s.store.Pending()
	switch {
	case has && p.Kind == state.PendingAnswerPrompt && s.cfg.Now().Before(p.ExpiresAt):
		// Live and unexpired: the ordinary case.
	case !has && s.expired != nil:
		// The pane's answer_prompt pending expired while nobody answered,
		// but the prompt may still be on screen: revive it before mapping
		// the bare word onto its keys (docs/issues-2026-09-18.md "an
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
	alias := s.aliasOf(p.PaneID)
	s.log.Log(logSource, "supervisor.fast_path", p.PaneID, map[string]any{"path": "answer", "text": text, "answer": label, "id": p.ID})
	res := s.answerWithKeys(ctx, p, keys, label, "Told "+alias+" %s.", "fast_path")
	if s.brain != nil {
		// No model saw this, so the transcript would otherwise lose it.
		s.brain.Daemon(fmt.Sprintf("fast path: the CEO said %q; %s", text, lowerFirst(res.Spoken)))
	}
	return res, true
}

// bareAnswer maps a whole-utterance answer onto the pending prompt's
// keys: yes, no, "option two", or an on-screen option's label.
func bareAnswer(p state.PendingAction, text string) (keys []string, label string, ok bool) {
	word := bareWord(text)
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
		// A label match here is exact, never the fuzzy overlap confirm
		// uses: "let me know when auth is done" must not answer a
		// prompt because "know" contains a "No" option's label.
		for _, o := range p.Options {
			if strings.ToLower(strings.Trim(o.Label, ".!?, ")) == word {
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
