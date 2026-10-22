package supervisor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The bare fast-path words (docs/dev/roles.md "One decision-maker"). *Bare*
// means the whole utterance, so "don't stop the tests" never matches a
// stop and "yes please run it" never answers a prompt — only these exact
// (normalised) phrasings do, including the user's polite ones.
//
// BareStopWords is exported so every bare-stop check in the daemon reads
// the same list: internal/inject's Router (GPT-Live's Classify path)
// used to keep its own regex, which disagreed with this one on "abort"
// and "halt" (audit B8) — the same word stopped GPT-Live outright but
// only became an ordinary orchestrator turn on Gemini.
var (
	//
	// "never mind" is not one (docs/dev/iteration-2.md J6, eval case
	// never-mind-is-not-stop): it is ordinary speech that goes to the
	// orchestrator, which decides whether it withdraws anything. As a stop word
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

// fastAnswer is the bare-answer fast path: the user's whole
// utterance (what was heard, typed or tapped — never the tool argument)
// answers the one open answer_prompt, so the keys go straight to the
// pane without a model, when the one rail's reading of in, the newest
// input, allows it (inputs.go). ok is false for anything else, which
// goes to the orchestrator.
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
		// something else the user heard since: the orchestrator gets the
		// words, with the prompt and how long it has waited in its
		// roster, and decides (docs/dev/history/bench-2026-10-03.md #8).
		s.log.Log(logSource, "supervisor.fast_path_skipped", p.PaneID, map[string]any{"reason": "prompt_old", "age_s": int(age.Seconds()), "text": text, "id": p.ID})
		return voice.ToolResult{}, false
	}
	if rin, reason := s.railInputLocked(p, keys); reason != "" || rin != in {
		if reason == "" {
			reason = "the user said something newer than the words being answered"
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
		s.brain.Daemon(fmt.Sprintf("fast path: the user said %q; %s", text, lowerFirst(res.Spoken)))
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
// knob: past it the orchestrator decides, so a longer or shorter window changes
// only how often a model call is spent on a bare word.
const fastAnswerWindow = 60 * time.Second
