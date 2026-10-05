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
// for the CEO's answer (the supervisor's pending answer_prompt, a pane
// blocked at an unanswered question), so both brains are told, as a
// fact, at the moment they write or voice an announcement. What to do
// with it is theirs (RULES.md "Announcing", the voice prompt).

// openDecisions lists the agents waiting for the CEO's answer right now,
// by spoken name, with the question when the supervisor recorded one.
func (s *Supervisor) openDecisions() []string {
	rows := s.store.List()
	pending, hasPending := s.store.Pending()
	seen := map[string]bool{}
	var out []string
	add := func(row state.Agent, question string) {
		if seen[row.PaneID] {
			return
		}
		seen[row.PaneID] = true
		name := aliasOr(state.Say(row))
		if q := strings.TrimSpace(question); q != "" {
			out = append(out, fmt.Sprintf("%s is waiting at a prompt for the CEO's answer (%q)", name, clipRunes(q, 80)))
			return
		}
		out = append(out, name+" is waiting at a prompt for the CEO's answer")
	}
	if hasPending && pending.Kind == state.PendingAnswerPrompt {
		for _, row := range rows {
			if row.PaneID == pending.PaneID {
				add(row, pending.Question)
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return state.Say(rows[i]) < state.Say(rows[j]) })
	for _, row := range rows {
		if row.State == herdr.StateBlocked && row.LastBlock != nil && !row.LastBlock.Answered {
			add(row, row.LastBlock.Question)
		}
	}
	return out
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
// model: "none: …" when no agent is waiting for the CEO's answer.
func (s *Supervisor) DecisionsNow() string {
	open := s.openDecisions()
	if len(open) == 0 {
		return "none: as this reached you, no agent is waiting for the CEO's answer"
	}
	return "yes: as this reached you, " + strings.Join(open, "; ")
}

// decisionsLine is the announce turn's decision-open fact
// (brain.EventFacts.Decisions).
func (s *Supervisor) decisionsLine() string {
	open := s.openDecisions()
	if len(open) == 0 {
		return "Decisions open right now: none. No agent is waiting at a prompt for the CEO's answer."
	}
	return "Decisions open right now: " + strings.Join(open, "; ") + "."
}
