package supervisor

import (
	"context"
	"errors"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/llm"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Summaries with context (docs/roles.md "Delivery" 2): the template path
// announces a block or finish at today's latency, then the brain — when
// one is attached and the chattiness level lets the announcement through
// — runs a summarize event turn with the sentence, the pane read already
// taken and its transcript, and may add what the sentence lacks ("that
// was the rerun you asked for; the same test still fails"). Nothing
// extra is said on timeout or fallback, when the brain answers with an
// empty spoken (the sentence was sufficient), or when the enrichment
// mostly repeats the sentence.

// duplicateOverlap is the normalised token overlap above which an
// enrichment counts as a repeat of the template sentence.
const duplicateOverlap = 0.8

// enrich runs the summarize event turn for an announcement of kind about
// row and pushes the enrichment at idle urgency when it adds information;
// otherwise it logs brain.enrichment_skipped with the reason. The detail
// is stored on last_result.detail either way (when there is one).
func (s *Supervisor) enrich(ctx context.Context, row state.Agent, kind pushKind, event, sentence, screen string) {
	if s.brain == nil {
		return
	}
	alias := aliasOr(row.Alias)
	skip := func(reason string, extra map[string]any) {
		payload := map[string]any{"reason": reason, "event": event, "template": sentence, "chattiness": string(s.chattiness())}
		for k, v := range extra {
			payload[k] = v
		}
		s.log.Log(logSource, "brain.enrichment_skipped", row.PaneID, payload)
	}
	level := s.chattiness()
	if level == Quiet || !level.allows(kind) {
		skip("chattiness", nil)
		return
	}
	rep, err := s.brain.Turn(ctx, brain.Request{Kind: brain.KindSummarize, Trigger: brain.TriggerEvent, Agent: alias, Event: event, Template: sentence, Screen: screen})
	if err != nil {
		reason := "fallback"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "timeout"
		}
		skip(reason, map[string]any{"error": err.Error()})
		return
	}
	spoken := spokenLine(rep.Spoken)
	detail := strings.TrimSpace(llm.Spoken(rep.Detail, 0))
	relates := strings.TrimSpace(llm.Spoken(rep.RelatesTo, 1))
	if detail != "" && event == brain.PromiseEventDone {
		s.recordDetail(row.PaneID, detail)
	}
	switch {
	case spoken == "":
		skip("sufficient", map[string]any{"detail": detail, "relates_to": relates})
	case nearDuplicate(spoken, sentence):
		skip("duplicate", map[string]any{"spoken": spoken, "overlap": tokenOverlap(spoken, sentence)})
	default:
		s.log.Log(logSource, "brain.enrichment", row.PaneID, map[string]any{"event": event, "template": sentence, "spoken": spoken, "detail": detail, "relates_to": relates, "needs_user": rep.NeedsUser})
		s.push(row.PaneID, spoken, voice.UrgencyIdle)
	}
}

// recordDetail stores the enrichment detail on the row's last_result.
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

// nearDuplicate reports whether more than duplicateOverlap of the
// enrichment's distinct tokens already occur in the template sentence.
func nearDuplicate(enrichment, template string) bool {
	return tokenOverlap(enrichment, template) > duplicateOverlap
}

// tokenOverlap is the share of enrichment's distinct normalised tokens
// (lower-case, punctuation stripped) that template also contains; 0
// when the enrichment has no tokens.
func tokenOverlap(enrichment, template string) float64 {
	have := map[string]bool{}
	for _, w := range tokens(template) {
		have[w] = true
	}
	seen := map[string]bool{}
	total, hits := 0, 0
	for _, w := range tokens(enrichment) {
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
