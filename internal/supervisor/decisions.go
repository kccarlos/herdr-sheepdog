package supervisor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Decision-open fact (information; baseline 2026-10-05 cluster 3): an
// announcement of a finish with no prompt on screen ended "Shall I have
// it run the tests?" (reconnect-held-push, 2 of 3 runs), opening a
// decision nobody had. The daemon knows whether any agent is waiting
// for the user's answer (the supervisor's pending answer_prompt, a pane
// blocked at an unanswered question), so the voice and the orchestrator are told, as a
// fact, at the moment they write or voice an announcement. What to do
// with it is theirs (RULES.md "Announcing", the voice prompt).

// openDecision is one agent waiting at a prompt for the user's answer.
type openDecision struct {
	name, kind, question, command string
}

// openDecisions lists the agents waiting for the user's answer right now,
// by spoken name, with the question (and the command it asks to run)
// when the supervisor recorded one.
func (s *Supervisor) openDecisions() []openDecision {
	rows := s.store.List()
	pending, hasPending := s.store.Pending()
	seen := map[string]bool{}
	var out []openDecision
	add := func(row state.Agent, question, command string) {
		if seen[row.PaneID] {
			return
		}
		seen[row.PaneID] = true
		out = append(out, openDecision{name: aliasOr(state.Say(row)), kind: row.Kind, question: strings.TrimSpace(question), command: strings.TrimSpace(command)})
	}
	// No supervisor lock here: the voice session calls this under its
	// own. An answer clears the pending prompt and marks the block
	// answered at once (answerKeys), so both reads are current.
	if hasPending && pending.Kind == state.PendingAnswerPrompt {
		for _, row := range rows {
			if row.PaneID == pending.PaneID {
				add(row, pending.Question, pending.Command)
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return state.Say(rows[i]) < state.Say(rows[j]) })
	for _, row := range rows {
		if row.State == herdr.StateBlocked && row.LastBlock != nil && !row.LastBlock.Answered {
			add(row, row.LastBlock.Question, row.LastBlock.Command)
		}
	}
	return out
}

// voiceText is the decision as the voice gets it: name and question.
func (d openDecision) voiceText() string {
	if d.question != "" {
		return fmt.Sprintf("%s is waiting at a prompt for the user's answer (%q)", d.name, clipRunes(d.question, 80))
	}
	return d.name + " is waiting at a prompt for the user's answer"
}

// orchestratorText is the decision as the orchestrator gets it: also the
// worker's kind and the command the prompt asks to run, which tell two
// prompts with the same question apart (harness iteration 5: "approve
// the codex one" with cache-codex and etl-codex both asking "would you
// like to run the following command").
func (d openDecision) orchestratorText() string {
	s := d.name
	if d.kind != "" {
		s += " (" + d.kind + ")"
	}
	s += " is waiting at a prompt for the user's answer"
	if d.question != "" {
		s += fmt.Sprintf(" (%q)", clipRunes(d.question, 80))
	}
	if d.command != "" {
		s += fmt.Sprintf(", asking to run %q", clipRunes(d.command, 80))
	}
	return s
}

// clipRunes cuts s to n runes with an ellipsis.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// DecisionsNow is the voice layer's decision-open fact for an
// announcement briefing (gemini.Config.DecisionsNow), as it reaches the
// model: "none: …" when no agent is waiting for the user's answer.
func (s *Supervisor) DecisionsNow() string {
	open := s.openDecisions()
	if len(open) == 0 {
		return "none: as this reached you, no agent is waiting for the user's answer"
	}
	parts := make([]string, len(open))
	for i, d := range open {
		parts[i] = d.voiceText()
	}
	return "yes: as this reached you, " + strings.Join(parts, "; ")
}

// decisionsLine is the announce turn's decision-open fact
// (brain.EventFacts.Decisions).
func (s *Supervisor) decisionsLine() string {
	open := s.openDecisions()
	if len(open) == 0 {
		return "Decisions open right now: none. No agent is waiting at a prompt for the user's answer."
	}
	return "Decisions open right now: " + orchestratorDecisions(open) + "."
}

// orchestratorDecisions joins the open decisions for the orchestrator.
func orchestratorDecisions(open []openDecision) string {
	parts := make([]string, len(open))
	for i, d := range open {
		parts[i] = d.orchestratorText()
	}
	return strings.Join(parts, "; ")
}

// openPromptsLine is the utterance turn's fact about several prompts open
// at once (brain.EventFacts.OpenPrompts; harness iteration 5): every one,
// with its worker's kind and command, and which of them share a kind;
// "" when fewer than two are open. The roster marks only the supervisor's
// pending prompt as waiting, so without it the other one is easy to
// miss. It is a fact about the state, never a reading of the user's
// words: whether their words pick one is the orchestrator's to decide
// (RULES.md "Intent").
func (s *Supervisor) openPromptsLine() string {
	open := s.openDecisions()
	if len(open) < 2 {
		return ""
	}
	line := fmt.Sprintf("%d prompts are open at once, each a separate decision: %s.", len(open), orchestratorDecisions(open))
	byKind := map[string][]string{}
	var kinds []string
	for _, d := range open {
		if d.kind == "" {
			continue
		}
		if byKind[d.kind] == nil {
			kinds = append(kinds, d.kind)
		}
		byKind[d.kind] = append(byKind[d.kind], d.name)
	}
	for _, k := range kinds {
		if names := byKind[k]; len(names) > 1 {
			all := "all"
			if len(names) == 2 {
				all = "both"
			}
			line += fmt.Sprintf(" %s are %s %s workers.", strings.Join(names, " and "), all, k)
		}
	}
	return line
}
