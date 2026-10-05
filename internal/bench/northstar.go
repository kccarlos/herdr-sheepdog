package bench

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

// NorthStarRun turns a tier-2 record into the north-star checks' input:
// the user hears the voice model (what the phone played), the
// orchestrator's text comes from the events (voice.tool_result spoken,
// spoken voice.push), and evs adds checks, spawns, closes,
// investigations and the pending answers.
func NorthStarRun(r Run, sc *Scenario, evs []northstar.Event) northstar.Run {
	nr := northstar.Run{
		Tier: "2", Scenario: r.Scenario, Repeat: r.Repeat, Status: string(r.Status), EndReason: r.EndReason,
		GoalMetSelf: r.GoalMetSelf, Turns: r.Turns, DurationMS: r.DurationMS, CostUSD: r.CostUSD,
		Heard: northstar.SideVoice, HasRecord: len(r.Transcript) > 0 || len(r.Bot) > 0, HasEvents: len(evs) > 0,
	}
	for _, u := range r.User {
		nr.User = append(nr.User, northstar.UserLine{AtMS: u.AtMS, Text: u.Said})
	}
	if len(r.User) == 0 {
		for _, l := range r.Transcript {
			if l.Who == "ceo" {
				nr.User = append(nr.User, northstar.UserLine{AtMS: l.AtMS, Text: l.Text})
			}
		}
	}
	for _, b := range r.Bot {
		if t := strings.TrimSpace(b.Heard); t != "" {
			nr.Speech = append(nr.Speech, northstar.Speech{AtMS: b.StartMS, Side: northstar.SideVoice, Text: t, Push: b.Push})
		}
		nr.EndMS = max(nr.EndMS, b.EndMS)
	}
	for _, l := range r.Transcript {
		nr.EndMS = max(nr.EndMS, l.AtMS)
	}
	nr.Actions = eval.RecordActions(r.Actions)
	for _, l := range r.Timeline {
		if n, ok := northstar.NewsFromTimeline(l.AtMS, l.Agent, l.Text); ok {
			nr.News = append(nr.News, n)
		}
	}
	if len(evs) > 0 {
		f := northstar.ReadFacts(evs)
		nr.Actions = append(nr.Actions, f.Actions...)
		northstar.AttachAnswers(nr.Actions, f.Answers)
		nr.News = append(nr.News, f.News...)
		nr.Speech = append(nr.Speech, f.Orchestrator...)
	}
	for _, l := range r.Latency {
		if v, ok := l["total_ms"].(float64); ok {
			nr.LatencyMS = append(nr.LatencyMS, v)
		}
	}
	for _, v := range r.Verdicts {
		nr.Verdicts = append(nr.Verdicts, northstar.Verdict{Name: v.Name, Answer: answerOf(v.Verdict)})
	}
	nr.Verdicts = append(nr.Verdicts, eval.ExplainVerdicts(r.Explain)...)
	for _, f := range r.Failures {
		nr.Failures = append(nr.Failures, northstar.Failure{Type: string(f.Type), Detail: f.Detail})
	}
	if sc != nil {
		nr.ExpectsActions = len(sc.Expect.Actions) > 0
		for _, a := range sc.Agents {
			nr.Agents = append(nr.Agents, northstar.Agent{Alias: a.Alias, Kind: orStr(a.Kind, "codex")})
			if a.State == "done" {
				nr.Initial = append(nr.Initial, northstar.News{Kind: northstar.NewsFinished, Agent: a.Alias, Detail: "at start"})
			}
		}
	}
	northstar.SortRun(&nr)
	return nr
}

func answerOf(v eval.Verdict) string {
	switch {
	case v.Err != "":
		return "err"
	case v.Passed():
		return "yes"
	}
	return "no"
}

// SummaryRun is a run known only from a report's summary (a campaign
// whose runs/ were not kept): its status and failure labels. Understood
// reads the judge_no labels (`answered`, depth and padding items; the
// voice side's when both were scored), Safe the action labels; the rest
// is not computed.
func SummaryRun(s RunSummary, sc *Scenario) northstar.Run {
	nr := northstar.Run{Tier: "2", Scenario: s.Scenario, Repeat: s.Repeat, Status: string(s.Status), EndReason: s.EndReason, Turns: s.Turns, CostUSD: s.CostUSD, Heard: northstar.SideVoice}
	asked := true
	if sc != nil {
		nr.ExpectsActions = len(sc.Expect.Actions) > 0
		for _, k := range sc.Expect.Skip {
			if k == "answered" {
				asked = false
			}
		}
	}
	answered := "yes"
	for _, f := range s.Failures {
		nr.Failures = append(nr.Failures, northstar.Failure{Type: string(f.Type), Detail: f.Detail})
		if f.Type != eval.FailJudgeNo {
			continue
		}
		name, _, _ := strings.Cut(f.Detail, ":")
		name = strings.TrimSpace(name)
		if strings.HasSuffix(name, "(orchestrator)") {
			// The user hears the voice; the orchestrator's explain
			// verdicts are diagnostics.
			continue
		}
		name = strings.TrimSpace(strings.TrimSuffix(name, "(voice)"))
		switch {
		case name == "answered" || name == "no_unintended_action":
			if name == "answered" {
				answered = "no"
			}
			nr.Verdicts = append(nr.Verdicts, northstar.Verdict{Name: name, Answer: "no"})
		case strings.HasPrefix(name, "explain_depth") || strings.HasPrefix(name, "explain_padding") || northstar.DepthItems.MatchString(name):
			nr.Verdicts = append(nr.Verdicts, northstar.Verdict{Name: name, Answer: "no"})
		}
	}
	if asked && answered == "yes" && (s.Status == eval.RunPass || s.Status == eval.RunFail) {
		nr.Verdicts = append(nr.Verdicts, northstar.Verdict{Name: "answered", Answer: "yes"})
	}
	return nr
}

// NorthStarRuns turns a campaign's records into the checks' input,
// reading each run's events from eventsDir/<id>.r<n>.jsonl.
func NorthStarRuns(runs []Run, scs []Scenario, eventsDir string) []northstar.Run {
	byID := scenarioIndex(scs)
	out := make([]northstar.Run, 0, len(runs))
	for _, r := range runs {
		var evs []northstar.Event
		if eventsDir != "" {
			evs, _ = northstar.ReadEvents(filepath.Join(eventsDir, fmt.Sprintf("%s.r%d.jsonl", r.Scenario, r.Repeat)))
		}
		out = append(out, NorthStarRun(r, byID[r.Scenario], evs))
	}
	return out
}

// SummaryRuns turns a report's run summaries into the checks' input.
func SummaryRuns(sums []RunSummary, scs []Scenario) []northstar.Run {
	byID := scenarioIndex(scs)
	out := make([]northstar.Run, 0, len(sums))
	for _, s := range sums {
		out = append(out, SummaryRun(s, byID[s.Scenario]))
	}
	return out
}

func scenarioIndex(scs []Scenario) map[string]*Scenario {
	m := map[string]*Scenario{}
	for i := range scs {
		m[scs[i].ID] = &scs[i]
	}
	return m
}
