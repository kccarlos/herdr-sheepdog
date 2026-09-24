// Package gen expands the eval corpus offline (docs/iteration-2.md WP0b):
// paraphrases of the shepherd's utterance per product job, and the
// transcript noise the voice models' speech recognition produces. Both
// are checked-in tables and a seeded generator — no model call — so the
// expanded set is reproducible and free.
package gen

import (
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// Paraphrases are ways the shepherd says each job's utterance, taken from
// the 2026-09-18…24 sessions where one exists and otherwise written in
// the same register: hesitations ("um", "so, uh"), half sentences, and
// the trailing tags speech recognition leaves on. English only:
// prompts/assistant.md does not claim any other language.
//
// A key is a job of docs/iteration-2.md with the sub-intent where
// paraphrases would otherwise change what is right: a "yes" is not a
// "no", and "never mind" must never become "stop" (J6). "{alias}" is
// replaced by the case's target agent.
var Paraphrases = map[string][]string{
	"J1": {
		"What's going on?",
		"What's the current status?",
		"Um, what's happening right now?",
		"So, uh, where are we?",
		"How are the agents doing?",
		"Any updates?",
		"What's everyone up to?",
		"Give me a quick status.",
		"Anything need me?",
		"So what's the latest?",
		"Hey, what's the status of my agents?",
		"How's it going over there?",
		"Status check.",
		"Is anyone waiting on me?",
		"What are they all doing, um,",
	},
	"J2-yes": {
		"Yes.",
		"Yeah, go ahead.",
		"Yep, do it.",
		"Sure, approve it.",
		"Okay, yes.",
		"Um, yes, let it.",
	},
	"J2-no": {
		"No.",
		"Nope, don't do that.",
		"No, deny it.",
		"Uh, no, not now.",
		"Don't let it.",
	},
	"J2-option": {
		"The second one.",
		"Option two.",
		"Go with the first option.",
		"Um, pick the last one.",
	},
	"J3": {
		"Tell {alias} to run the tests.",
		"Ask {alias} to run the tests and let me know when it's done.",
		"Have {alias} fix the failing test.",
		"Um, tell {alias} to commit that.",
		"So, uh, can you get {alias} to open a PR?",
		"Tell {alias} to keep going.",
		"Let {alias} continue.",
		"Ask {alias} to check the build, please.",
		"{alias}, run the linter.",
		"Tell {alias} the voice still lags on the phone.",
		"Have {alias} look into why the mute switches on its own.",
		"Tell {alias} to, um, add a test for that.",
		"Please ask {alias} to review the last change, read-only.",
		"Get {alias} to rebase on main.",
		"Tell {alias} to stop after the tests and report back.",
	},
	"J4": {
		"What did it find?",
		"What does it say?",
		"What did {alias} find?",
		"So what was the result?",
		"Um, what did it come back with?",
		"Any findings?",
		"What did the review say?",
		"What's the verdict?",
		"Did the tests pass?",
		"What happened with {alias}?",
		"Can you tell me what it found?",
		"So, uh, what's in the report?",
		"What did it conclude?",
		"What's the summary of what it did?",
		"What does the result say?",
	},
	"J5": {
		"What does that mean?",
		"Why?",
		"Say that again?",
		"Can you say again what the result says?",
		"Um, what do you mean?",
		"In plain words?",
		"Explain that more simply.",
		"Sorry, I didn't get that.",
		"Why did it fail?",
		"So, uh, what does that mean for me?",
		"What's that?",
		"Can you put it simpler?",
		"Wait, what does that mean?",
		"Why is that a problem?",
		"Say again?",
	},
	"J6-stop": {
		"Stop.",
		"Stop that.",
		"Stop {alias}.",
		"Cancel that instruction.",
		"Abort, abort.",
		"Um, stop it, stop.",
		"Kill that, it's taking too long.",
		"Stop what you just sent.",
	},
	"J6-nevermind": {
		"Never mind.",
		"Oh, never mind.",
		"Forget it.",
		"Nah, never mind.",
		"Um, never mind that.",
		"Never mind, it's fine.",
		"Oh wait, forget it.",
	},
}

// Paraphrase returns up to n variants of c (n ≤ 0: all) with the
// request's utterance replaced by the paraphrases of c's job, skipping
// the one equal to the original. A case without a job, or one that is
// not an utterance, has none.
func Paraphrase(c eval.SVPCase, n int) []eval.SVPCase {
	if !isUtterance(c) {
		return nil
	}
	table := Paraphrases[c.Job]
	alias := targetAlias(c)
	var out []eval.SVPCase
	for i, p := range table {
		if n > 0 && len(out) >= n {
			break
		}
		text := strings.ReplaceAll(p, "{alias}", alias)
		if strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(c.Request.Question)) {
			continue
		}
		v := derive(c, "~p"+strconv.Itoa(i), "paraphrase")
		v.Request.Question = text
		out = append(out, v)
	}
	return out
}

// ParaphraseAssistant is Paraphrase for an assistant case: the utterance
// is what the voice hears.
func ParaphraseAssistant(c eval.AssistantCase, n int) []eval.AssistantCase {
	table := Paraphrases[c.Job]
	alias := "auth"
	if len(c.Aliases) > 0 {
		alias = c.Aliases[0]
	}
	var out []eval.AssistantCase
	for i, p := range table {
		if n > 0 && len(out) >= n {
			break
		}
		text := strings.ReplaceAll(p, "{alias}", alias)
		if strings.EqualFold(strings.TrimSpace(text), strings.TrimSpace(c.Utterance)) {
			continue
		}
		v := c
		v.ID = c.ID + "~p" + strconv.Itoa(i)
		v.Parent, v.Generator = c.ID, "paraphrase"
		v.Observed, v.BaselineFails, v.PendingCheck = nil, nil, ""
		v.Utterance = text
		out = append(out, v)
	}
	return out
}

func isUtterance(c eval.SVPCase) bool {
	k := c.Request.Kind
	return (k == "" || k == "utterance") && strings.TrimSpace(c.Request.Question) != ""
}

// targetAlias is the agent a job's "{alias}" names: the request's hint,
// else the agent with a pending prompt, else the first one ("auth" for a
// case with the synthetic primary).
func targetAlias(c eval.SVPCase) string {
	if c.Request.Agent != "" {
		return c.Request.Agent
	}
	if !c.RosterOnly || len(c.Agents) == 0 {
		return "auth"
	}
	for _, a := range c.Agents {
		if a.Pending != nil {
			return a.Alias
		}
	}
	return c.Agents[0].Alias
}

// derive copies c as a generated child: a new id, the parent recorded,
// and the logged baseline dropped (it belongs to the original words).
func derive(c eval.SVPCase, suffix, generator string) eval.SVPCase {
	v := c
	v.ID = c.ID + suffix
	if v.Parent == "" {
		v.Parent = c.ID
	}
	if v.Generator == "" {
		v.Generator = generator
	} else {
		v.Generator += "+" + generator
	}
	// The baseline and the pending note belong to the original words
	// and its logged run; the parent id leads back to both.
	v.Observed, v.BaselineFails, v.PendingCheck = nil, nil, ""
	if c.Source != nil {
		s := *c.Source
		s.Heard = ""
		v.Source = &s
	}
	// Slices are shared with the parent; the generators only replace
	// the request's text and trim expectations, never mutate in place.
	return v
}
