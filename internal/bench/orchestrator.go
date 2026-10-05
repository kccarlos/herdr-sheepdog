package bench

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// Orchestrator vs voice (workday scenarios): the run's judge items grade
// what the CEO heard (the voice model's speech). The same items asked of
// the orchestrator's own text — every to_supervisor answer (spoken,
// detail, options) and every push it handed the voice — show what the
// voice layer lost or added: an item the orchestrator passes and the
// voice fails is lost in the voice layer.

// orchestratorItem reports whether a judge item is asked of the
// orchestrator side too: the ones about content (answers, news, claims,
// coherence), not the ones about delivery or actions.
func orchestratorItem(name string) bool {
	return name == "answered" || name == eval.WorkdayClaimItem || strings.HasPrefix(name, eval.WorkdayNewsItem+"_") || strings.HasPrefix(name, eval.WorkdayCoherenceItem+"_")
}

// OrchestratorTranscript is the call as the orchestrator told it: the
// CEO's lines (as said) and, in time order, the orchestrator's answers
// and spoken pushes (silent context pushes left out).
func OrchestratorTranscript(r Run, events []voiceclient.Event) []eval.SimLine {
	var lines []eval.SimLine
	for _, l := range r.Transcript {
		if l.Who == "ceo" || l.Who == "system" {
			lines = append(lines, l)
		}
	}
	for _, ev := range events {
		switch ev.Type {
		case "voice.tool_result":
			spoken, _ := ev.Payload["spoken"].(string)
			detail, _ := ev.Payload["detail"].(string)
			var opts []string
			if raw, ok := ev.Payload["options"].([]any); ok {
				for _, o := range raw {
					if s, ok := o.(string); ok {
						opts = append(opts, s)
					}
				}
			}
			text := strings.TrimSpace(eval.JoinOrchestrator(spoken, detail, opts))
			if text != "" {
				lines = append(lines, eval.SimLine{AtMS: ev.AtMS, Who: "sheepdog", Text: text})
			}
		case "voice.push":
			if s, _ := ev.Payload["scheduling_sent"].(string); s == "silent" {
				continue
			}
			if text, _ := ev.Payload["text"].(string); strings.TrimSpace(text) != "" {
				lines = append(lines, eval.SimLine{AtMS: ev.AtMS, Who: "push", Text: text})
			}
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].AtMS < lines[j].AtMS })
	return lines
}

// RescoreOrchestrator asks the run's content items of the orchestrator
// transcript and stores them in r.Orchestrator (the run's status is not
// changed: its verdicts stay the voice side's).
func RescoreOrchestrator(ctx context.Context, j eval.Judge, sc Scenario, r *Run, events []voiceclient.Event) error {
	if r.Status == eval.RunNotRun || r.EndReason == eval.EndError {
		return nil
	}
	names, criteria := eval.RubricFor(sc.SimScenario)
	var keepN, keepC []string
	for i, n := range names {
		if orchestratorItem(n) {
			keepN, keepC = append(keepN, n), append(keepC, criteria[i])
		}
	}
	if len(keepC) == 0 {
		return nil
	}
	tr := eval.RenderTranscript(OrchestratorTranscript(*r, events))
	tr = "(This is the assistant's text as its orchestrator wrote it, before the voice model spoke it.)\n" + tr
	r.Orchestrator = nil
	for i, v := range eval.JudgeAll(ctx, j, keepC, tr, r.Facts, true) {
		if v.Err != "" {
			return fmt.Errorf("%s r%d %s: %s", r.Scenario, r.Repeat, keepN[i], v.Err)
		}
		r.Orchestrator = append(r.Orchestrator, eval.NamedVerdict{Name: keepN[i], Verdict: v})
	}
	return nil
}
