package supervisor

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// newAnswerPrompt builds an answer_prompt action from a parsed approval
// prompt. blockedSince is the pane's row.StateSince (when it entered
// blocked), for the one rail's reading of when a first-recorded or
// revived prompt really appeared (inputs.go's promptSinceLocked): a
// synthetic blocked -> blocked transition never moves state_since
// (docs/dev/roles.md "blocked -> blocked is a block"), so this is the true
// time even for a reprompt or a reconcile. Caller holds s.mu.
func (s *Supervisor) newAnswerPrompt(paneID string, prompt herdr.Prompt, alias string, blockedSince time.Time) state.PendingAction {
	now := s.cfg.Now()
	p := state.PendingAction{
		ID:     s.nextID("ap"),
		PaneID: paneID,
		Kind:   state.PendingAnswerPrompt,
		// The prompt's own words reach the ear, so D9 applies to them
		// like any other text from a pane: no paths, hashes or URLs.
		SpokenSummary: prose.Spoken(prompt.SummaryIn(alias, s.ceoChineseLocked()), 0),
		Question:      prompt.Phrase(),
		Command:       prompt.Command,
		YesKeys:       append([]string(nil), prompt.YesKeys...),
		NoKeys:        append([]string(nil), prompt.NoKeys...),
		// No ExpiresAt: an answer_prompt lives while the pane is
		// blocked at this prompt (checkOpenPrompt), not on a clock.
		CreatedAt: now,
	}
	for i, o := range prompt.Options {
		p.Options = append(p.Options, state.Option{Label: o.Label, Keys: o.Keys, Default: i == prompt.Default, Meta: o.Meta,
			Description: o.Description, Recommended: o.Recommended, Title: o.Title})
	}
	// Remembered so the post-answer poll and the reconciler can tell a
	// new prompt from this one (docs/dev/roles.md "blocked -> blocked is a
	// block", reprompt.go): every answer_prompt pending action is built
	// here, whether the block is real or synthetic.
	sig := promptSignature(prompt)
	if _, seen := s.promptSeenAt[paneID]; !seen || s.lastPrompt[paneID] != sig {
		// When the prompt appeared, for the one rail (inputs.go): a
		// revival of the same prompt keeps the first time it was seen; a
		// prompt recorded here for the first time, with the pane already
		// blocked (a revival, a reconcile at startup, or any first sight
		// of a pane that was already at a prompt), appeared when the pane
		// actually went blocked, not at this moment's recording of it.
		seenAt := now
		if !blockedSince.IsZero() && blockedSince.Before(now) {
			seenAt = blockedSince
		}
		s.promptSeenAt[paneID] = seenAt
	}
	s.lastPrompt[paneID] = sig
	return p
}

// newSendInstruction builds a send_instruction action. Caller holds s.mu.
func (s *Supervisor) newSendInstruction(paneID, text, echo string) state.PendingAction {
	now := s.cfg.Now()
	return state.PendingAction{
		ID:            s.nextID("si"),
		PaneID:        paneID,
		Kind:          state.PendingSendInstruction,
		SpokenSummary: echo,
		PayloadText:   text,
		CreatedAt:     now,
		ExpiresAt:     now.Add(s.cfg.PendingTTL),
	}
}

// setPending installs p as the single pending action, logging
// pending.replaced when it displaces another and pending.created always.
// It returns the announcement to speak: for a new question that replaces
// an unanswered question from another pane, the "dropping …; now …"
// sentence; otherwise p.SpokenSummary. Caller holds s.mu.
func (s *Supervisor) setPending(p state.PendingAction) string {
	spoken := p.SpokenSummary
	if old, had := s.store.Pending(); had && old.ID != p.ID {
		s.log.Log(logSource, "pending.replaced", p.PaneID, map[string]any{
			"old_id": old.ID, "old_kind": old.Kind, "old_pane_id": old.PaneID, "new_id": p.ID, "new_kind": p.Kind,
		})
		if old.Kind == state.PendingAnswerPrompt && p.Kind == state.PendingAnswerPrompt && old.PaneID != p.PaneID && s.allows(pushReplacement) {
			spoken = replacedLine(s.sayOf(old.PaneID), p.SpokenSummary, s.ceoChineseLocked())
		}
	}
	if err := s.store.SetPending(p); err != nil {
		s.log.Log(logSource, "supervisor.state_error", p.PaneID, map[string]any{"error": err.Error()})
	}
	if p.Kind == state.PendingAnswerPrompt {
		// Every open question is listed for the app, the one the
		// voice is on and those it moved past alike.
		if err := s.store.SetQuestion(p); err != nil {
			s.log.Log(logSource, "supervisor.state_error", p.PaneID, map[string]any{"error": err.Error()})
		}
	}
	s.invalidateBriefingLocked()
	created := map[string]any{
		"id": p.ID, "kind": p.Kind, "summary": p.SpokenSummary, "question": p.Question,
		"options": PendingOptions(p), "yes_keys": p.YesKeys, "no_keys": p.NoKeys, "agent": s.aliasOf(p.PaneID),
	}
	if !p.ExpiresAt.IsZero() {
		created["expires_at"] = p.ExpiresAt
	}
	if p.Command != "" {
		created["command"] = p.Command
	}
	s.log.Log(logSource, "pending.created", p.PaneID, created)
	return spoken
}

// clearPending drops the pending action with a pending.cancelled event.
// Caller holds s.mu.
func (s *Supervisor) clearPending(p state.PendingAction, reason string) {
	if err := s.store.ClearPending(); err != nil {
		s.log.Log(logSource, "supervisor.state_error", p.PaneID, map[string]any{"error": err.Error()})
	}
	if p.Kind == state.PendingAnswerPrompt {
		s.dropQuestionLocked(p)
	}
	s.invalidateBriefingLocked()
	s.log.Log(logSource, "pending.cancelled", p.PaneID, map[string]any{"id": p.ID, "kind": p.Kind, "reason": reason})
}

// dropQuestionLocked removes p from the open questions when it is
// still its pane's question. Caller holds s.mu.
func (s *Supervisor) dropQuestionLocked(p state.PendingAction) {
	if q, ok := s.store.Question(p.ID); !ok || q.PaneID != p.PaneID {
		return
	}
	if err := s.store.DropQuestion(p.PaneID); err != nil {
		s.log.Log(logSource, "supervisor.state_error", p.PaneID, map[string]any{"error": err.Error()})
	}
}

// questionByID finds an open answer_prompt by id: the one the voice is
// on, or one it moved past. slot says it is the former.
func (s *Supervisor) questionByID(id string) (p state.PendingAction, slot, ok bool) {
	if cur, has := s.store.Pending(); has && cur.ID == id && cur.Kind == state.PendingAnswerPrompt {
		return cur, true, true
	}
	if q, has := s.store.Question(id); has {
		return q, false, true
	}
	return state.PendingAction{}, false, false
}

// dropPendingFor cancels the pending action of paneID when it has one of
// the given kinds (all kinds when none given). Caller holds s.mu.
func (s *Supervisor) dropPendingFor(paneID, reason string, kinds ...state.PendingKind) {
	p, has := s.store.Pending()
	if !has || p.PaneID != paneID {
		return
	}
	if len(kinds) > 0 {
		match := false
		for _, k := range kinds {
			if p.Kind == k {
				match = true
			}
		}
		if !match {
			return
		}
	}
	s.clearPending(p, reason)
}

// ExpirePending drops a send_instruction pending action whose TTL has
// passed, logging pending.expired. Run calls this on every tick.
//
// An answer_prompt never expires on the clock (docs/dev/history/bench-2026-10-03.md
// #8): on 2026-10-02 bench-claude's "do you want to proceed" stayed on
// screen from 23:55:47 to 00:03:15, but its pending expired at 23:57:48
// after pending_ttl_s, and the phone card with it. It lives while the
// pane is blocked at the same prompt: a transition off blocked ends it
// (transitions.go), and the reconciler checks the screen
// (checkOpenPrompt). A tap on its card is valid all that time.
func (s *Supervisor) ExpirePending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, has := s.store.Pending()
	if !has || p.Kind == state.PendingAnswerPrompt || s.cfg.Now().Before(p.ExpiresAt) {
		return
	}
	if err := s.store.ClearPending(); err != nil {
		s.log.Log(logSource, "supervisor.state_error", p.PaneID, map[string]any{"error": err.Error()})
	}
	s.invalidateBriefingLocked()
	s.log.Log(logSource, "pending.expired", p.PaneID, map[string]any{"id": p.ID, "kind": p.Kind, "question": p.Question})
}

// revivePrompt re-reads paneID's screen and, when it is still showing an
// approval prompt, records it as a fresh answer_prompt pending action:
// the send_keys path (brainHost.AnswerPrompt) calls this when no pending
// is recorded for the pane but it is still blocked — its answer_prompt
// was dropped without being answered (another pane's block or an
// instruction took the one slot), or was never recorded at all — so the
// prompt still on screen can still be answered
// (docs/dev/history/issues-2026-09-18.md "an expired approval prompt can never be
// approved"). Revival always re-reads the screen rather than trusting
// what was dropped: the pane may already be asking something else.
//
// A menu on a pane herdr does not report blocked (Codex's update menu
// shows on an idle pane; tools.go's menuOnScreen tells the orchestrator about
// it) is revived too, but only when the user's newest words are
// about it (wordsAboutMenuLocked): they name one of its options or words
// of its question. The rail then checks those words as for any prompt.
// Caller holds s.mu; ok is false when the pane is gone or working, its
// screen does not parse as a prompt, or — not blocked — the user's
// words are not about the menu (why says so).
func (s *Supervisor) revivePrompt(ctx context.Context, paneID, source string) (p state.PendingAction, ok bool, why string) {
	row, found := s.store.Get(paneID)
	if !found || row.State == herdr.StateWorking {
		return state.PendingAction{}, false, ""
	}
	blocked := row.State == herdr.StateBlocked
	read, err := s.herdr.Read(ctx, paneID, herdr.SourceRecentUnwrapped, s.cfg.ReadLines)
	if err != nil {
		return state.PendingAction{}, false, ""
	}
	prompt, parsed := herdr.ParsePrompt(read.Text)
	if !parsed {
		return state.PendingAction{}, false, ""
	}
	if !blocked && !s.wordsAboutMenuLocked(prompt, row) {
		return state.PendingAction{}, false, fmt.Sprintf("%s is showing a menu, but the user's last words are not about it", aliasOr(state.Say(row)))
	}
	p = s.newAnswerPrompt(paneID, prompt, aliasOr(state.Say(row)), row.StateSince)
	s.setPending(p)
	s.log.Log(logSource, "pending.revived", paneID, map[string]any{"id": p.ID, "source": source, "state": string(row.State)})
	return p, true, ""
}

// menuStopWords are words too common to tie the user's words to a
// menu.
var menuStopWords = map[string]bool{"the": true, "and": true, "for": true, "you": true, "your": true, "this": true, "that": true, "with": true, "now": true, "what": true, "want": true, "would": true, "like": true, "tell": true, "can": true, "please": true, "it's": true, "its": true}

// wordsAboutMenuLocked reports whether the user's newest words are
// about prompt, a menu on row's idle pane: they share a word (three
// letters or more, not the agent's own name or a common word) with one
// of its answer options or its question, or pick an option by number.
// Caller holds s.mu.
func (s *Supervisor) wordsAboutMenuLocked(prompt herdr.Prompt, row state.Agent) bool {
	in := s.lastInputLocked()
	if in == nil || strings.TrimSpace(in.text) == "" {
		return false
	}
	if n := optionNumber(normalizeAnswer(in.text)); n > 0 && n <= len(prompt.Options) {
		return true
	}
	skip := map[string]bool{}
	for _, name := range []string{row.Alias, state.Say(row), row.Kind} {
		for _, w := range menuWords(name) {
			skip[w] = true
		}
	}
	menu := map[string]bool{}
	for _, w := range menuWords(prompt.Question) {
		menu[w] = true
	}
	for _, o := range prompt.Answers() {
		for _, w := range menuWords(o.Label) {
			menu[w] = true
		}
	}
	for _, w := range menuWords(in.text) {
		if menu[w] && !skip[w] && !menuStopWords[w] {
			return true
		}
	}
	return false
}

// menuWords are the lower-case words of text, three letters or more.
func menuWords(text string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	}) {
		if len([]rune(w)) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

var (
	reOptionNumber = regexp.MustCompile(`^(?:option|number|choice|the)?\s*(\d+)(?:st|nd|rd|th)?$`)
	// ordinals covers both the ordinal ("second") and cardinal ("two")
	// word forms of an option pick, so "option two" and "the second one"
	// both resolve (optionNumber).
	ordinals = map[string]int{
		"first": 1, "second": 2, "third": 3, "fourth": 4, "fifth": 5, "sixth": 6, "seventh": 7, "eighth": 8, "ninth": 9,
		"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
	}
	yesWords = map[string]bool{
		"": true, "yes": true, "y": true, "yeah": true, "yep": true, "yup": true, "ok": true, "okay": true, "sure": true,
		"go": true, "go ahead": true, "yeah go ahead": true, "yes go ahead": true, "do it": true, "proceed": true,
		"approve": true, "approved": true, "approve it": true, "please approve": true, "confirm": true, "accept": true,
		"allow": true, "fine": true, "please": true, "yes please": true, "yes continue": true,
	}
	noWords = map[string]bool{"no": true, "n": true, "nope": true, "nah": true, "no thanks": true, "deny": true, "decline": true, "reject": true, "don't": true, "do not": true, "dont": true, "cancel": true, "stop": true, "skip": true, "negative": true}
)

// mapAnswer turns the user's answer into keystrokes for the pending
// prompt and the word to speak back ("yes", "no", "option 2", a label).
func mapAnswer(p state.PendingAction, answer string) ([]string, string, error) {
	a := normalizeAnswer(answer)
	switch {
	case yesWords[a]:
		keys := p.YesKeys
		if len(keys) == 0 {
			if ans := answerOptions(p); len(ans) > 0 {
				keys = ans[0].Keys
			}
		}
		if len(keys) == 0 {
			keys = []string{"enter"}
		}
		return keys, "yes", nil
	case noWords[a]:
		keys := p.NoKeys
		if len(keys) == 0 {
			keys = []string{"esc"}
		}
		return keys, "no", nil
	}
	if n := optionNumber(a); n > 0 {
		if n > len(p.Options) || p.Options[n-1].Meta {
			return nil, "", fmt.Errorf("there is no option %d", n)
		}
		return p.Options[n-1].Keys, "option " + strconv.Itoa(n), nil
	}
	if o, ok := matchLabel(answerOptions(p), a); ok {
		return o.Keys, o.Label, nil
	}
	return nil, "", fmt.Errorf("no option matches %q", answer)
}

// normalizeAnswer lowercases and strips punctuation and filler.
func normalizeAnswer(a string) string {
	a = strings.ToLower(strings.TrimSpace(a))
	a = strings.Trim(a, ".!?,;: ")
	for _, prefix := range []string{"answer ", "say ", "tell it ", "tell them ", "tell him ", "tell her "} {
		a = strings.TrimPrefix(a, prefix)
	}
	return strings.TrimSpace(a)
}

// optionNumber extracts an option index from "2", "option 2", "the
// second one", or 0.
func optionNumber(a string) int {
	a = strings.TrimSuffix(a, " one")
	if m := reOptionNumber.FindStringSubmatch(a); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	for word, n := range ordinals {
		if a == word || a == "the "+word || a == "option "+word {
			return n
		}
	}
	return 0
}

// matchLabel finds the option whose label best matches the answer:
// exact, containment either way, then most overlapping words (ties are
// ambiguous).
func matchLabel(opts []state.Option, a string) (state.Option, bool) {
	if a == "" {
		return state.Option{}, false
	}
	best, bestScore, tie := state.Option{}, 0, false
	words := strings.Fields(a)
	for _, o := range opts {
		l := strings.ToLower(strings.Trim(o.Label, ".!?, "))
		if l == a {
			return o, true
		}
		score := 0
		if strings.Contains(l, a) || strings.Contains(a, l) {
			score = 100
		} else {
			for _, w := range words {
				if len(w) > 2 && strings.Contains(l, w) {
					score++
				}
			}
		}
		switch {
		case score > bestScore:
			best, bestScore, tie = o, score, false
		case score == bestScore && score > 0:
			tie = true
		}
	}
	if bestScore == 0 || tie {
		return state.Option{}, false
	}
	return best, true
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// answerOptions are p's options that answer the question: the Meta ones
// ("Type something.") stay in p.Options for the rail only.
func answerOptions(p state.PendingAction) []state.Option {
	var out []state.Option
	for _, o := range p.Options {
		if !o.Meta {
			out = append(out, o)
		}
	}
	return out
}

// replacedLine is the spoken line for a question that displaces another
// pane's: in the user's language, like the summary it carries (no
// "。." seam, docs/dev/eval/eval-campaign-2026-10-03.md H1/H4).
func replacedLine(oldAlias, summary string, zh bool) string {
	if zh {
		return "先放下 " + oldAlias + " 之前的问题。现在 " + prose.Terminate(summary)
	}
	return fmt.Sprintf("Dropping the earlier question from %s; now %s.", oldAlias, lowerFirst(strings.TrimRight(summary, ".。")))
}
