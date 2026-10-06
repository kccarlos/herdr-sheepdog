package eval

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

// NorthStarOptions say how to read simulated runs for the north-star
// block (internal/northstar).
type NorthStarOptions struct {
	// Tier names the report ("1", "3", "3 voice").
	Tier string
	// Scenarios, when given, tell which runs require actions and the
	// workers' kinds (by scenario id).
	Scenarios []SimScenario
	// EventsDir holds each run's event log as <id>.r<n>.jsonl (eval sim
	// --events, bench run --tier 3 --events); empty reads none.
	EventsDir string
	// Voice: the user heard a voice model (tier 3 --voice), so the
	// transcript is the voice side and the orchestrator's text comes from
	// the events.
	Voice bool
	// WallClockEvents: the event logs carry ts, not at_ms (tier 3); they
	// are aligned on the first send (or relayed utterance).
	WallClockEvents bool
}

// NorthStarRun turns a simulated run (tier 1, or tier 3's record) into
// the north-star checks' input; evs is its event log (may be nil).
func NorthStarRun(r SimRun, sc *SimScenario, evs []northstar.Event, o NorthStarOptions) northstar.Run {
	heard := northstar.SideOrchestrator
	if o.Voice {
		heard = northstar.SideVoice
	}
	nr := northstar.Run{
		Tier: o.Tier, Scenario: r.Scenario, Repeat: r.Repeat, Status: string(r.Status), EndReason: r.EndReason,
		GoalMetSelf: r.GoalMetSelf, Turns: r.Turns, DurationMS: r.DurationMS, CostUSD: r.CostUSD,
		Heard: heard, HasRecord: len(r.Transcript) > 0, HasEvents: len(evs) > 0,
	}
	for _, l := range r.Transcript {
		switch l.Who {
		case "ceo":
			nr.User = append(nr.User, northstar.UserLine{AtMS: l.AtMS, Text: l.Text})
		case "sheepdog", "push":
			if strings.TrimSpace(l.Text) != "" {
				nr.Speech = append(nr.Speech, northstar.Speech{AtMS: l.AtMS, Side: heard, Text: l.Text, Push: l.Who == "push"})
			}
		}
		nr.EndMS = max(nr.EndMS, l.AtMS)
	}
	nr.Actions = append(nr.Actions, RecordActions(r.Actions)...)
	for _, l := range r.Timeline {
		if n, ok := northstar.NewsFromTimeline(l.AtMS, l.Agent, l.Text); ok {
			nr.News = append(nr.News, n)
		}
	}
	if len(evs) > 0 {
		if o.WallClockEvents {
			firstAct, firstUser := int64(-1), int64(-1)
			if len(r.Actions) > 0 {
				firstAct = r.Actions[0].AtMS
			}
			if len(nr.User) > 0 {
				firstUser = nr.User[0].AtMS
			}
			northstar.Align(evs, firstAct, firstUser)
		}
		f := northstar.ReadFacts(evs)
		nr.Actions = append(nr.Actions, f.Actions...)
		northstar.AttachAnswers(nr.Actions, f.Answers)
		nr.News = append(nr.News, f.News...)
		if o.Voice {
			nr.Speech = append(nr.Speech, f.Orchestrator...)
		}
		nr.LatencyMS = f.LatencyMS
	}
	for _, v := range r.Verdicts {
		nr.Verdicts = append(nr.Verdicts, northstar.Verdict{Name: v.Name, Answer: verdictAnswer(v.Verdict)})
	}
	nr.Verdicts = append(nr.Verdicts, ExplainVerdicts(r.Explain)...)
	nr.Segments = SegmentsNorthStar(r.Segments)
	for _, f := range r.Failures {
		nr.Failures = append(nr.Failures, northstar.Failure{Type: string(f.Type), Detail: f.Detail})
	}
	agents := map[string]bool{}
	if sc != nil {
		nr.ExpectsActions = len(sc.Expect.Actions) > 0
		for _, a := range sc.Agents {
			nr.Agents = append(nr.Agents, northstar.Agent{Alias: a.Alias, Kind: orDefault(a.Kind, "codex")})
			agents[a.Alias] = true
			switch a.State {
			case "done":
				nr.Initial = append(nr.Initial, northstar.News{Kind: northstar.NewsFinished, Agent: a.Alias, Detail: "at start"})
			case "blocked":
				// A worker waiting on the user when the call starts is news
				// to tell (tier 3 logs it as new → blocked).
				nr.News = append(nr.News, northstar.News{Kind: northstar.NewsBlocked, Agent: a.Alias, Detail: "at start"})
			}
		}
	}
	for _, l := range append(append([]SimLine(nil), r.Actions...), r.Timeline...) {
		if l.Agent != "" && !agents[l.Agent] {
			agents[l.Agent] = true
			nr.Agents = append(nr.Agents, northstar.Agent{Alias: l.Agent, Kind: kindOfAlias(l.Agent)})
		}
	}
	northstar.SortRun(&nr)
	return nr
}

// RecordActions are a record's actions on workers (prompt, keys,
// interrupt), all of which went (a record logs only what was sent).
func RecordActions(acts []SimLine) []northstar.Action {
	var out []northstar.Action
	for _, a := range acts {
		k := northstar.ActKeys
		switch a.Kind {
		case ActionPrompt:
			k = northstar.ActPrompt
		case ActionInterrupt:
			k = northstar.ActInterrupt
		}
		out = append(out, northstar.Action{AtMS: a.AtMS, Kind: k, Agent: a.Agent, Detail: a.Detail, Outcome: northstar.OutcomeOK})
	}
	return out
}

// ExplainVerdicts are an explain score's depth and padding verdicts on
// the side the user heard (the voice when it was scored, else the
// orchestrator); comprehension items are metrics, left out.
func ExplainVerdicts(s *ExplainScore) []northstar.Verdict {
	if s == nil {
		return nil
	}
	side := &s.Orchestrator
	if s.Voice != nil {
		side = s.Voice
	}
	var out []northstar.Verdict
	for _, v := range side.Verdicts {
		if strings.HasPrefix(v.Name, "explain_comprehension") {
			continue
		}
		out = append(out, northstar.Verdict{Name: v.Name, Answer: verdictAnswer(v.Verdict)})
	}
	return out
}

func verdictAnswer(v Verdict) string {
	switch {
	case v.Err != "":
		return "err"
	case v.Passed():
		return "yes"
	}
	return "no"
}

func kindOfAlias(alias string) string {
	for _, k := range []string{"claude", "codex", "agy", "gemini"} {
		if strings.Contains(strings.ToLower(alias), k) {
			return k
		}
	}
	return ""
}

// NorthStarBlock computes the north-star block of simulated runs.
func NorthStarBlock(runs []SimRun, o NorthStarOptions) *northstar.Block {
	return northstar.Summarize(o.Tier, NorthStarRuns(runs, o))
}

// NorthStarRuns turns runs into the checks' input, reading each run's
// event log from o.EventsDir.
func NorthStarRuns(runs []SimRun, o NorthStarOptions) []northstar.Run {
	byID := map[string]*SimScenario{}
	for i := range o.Scenarios {
		byID[o.Scenarios[i].ID] = &o.Scenarios[i]
	}
	out := make([]northstar.Run, 0, len(runs))
	for _, r := range runs {
		var evs []northstar.Event
		if o.EventsDir != "" {
			evs, _ = northstar.ReadEvents(filepath.Join(o.EventsDir, fmt.Sprintf("%s.r%d.jsonl", r.Scenario, r.Repeat)))
		}
		out = append(out, NorthStarRun(r, byID[r.Scenario], evs, o))
	}
	return out
}

// SegmentsNorthStar are a record's segment verdicts as the north-star
// checks read them.
func SegmentsNorthStar(segs []SegmentResult) []northstar.SegmentVerdicts {
	var out []northstar.SegmentVerdicts
	for _, s := range segs {
		sv := northstar.SegmentVerdicts{Beat: s.Beat, Kind: s.Kind}
		for _, v := range s.Verdicts {
			sv.Verdicts = append(sv.Verdicts, northstar.Verdict{Name: v.Name, Answer: verdictAnswer(v.Verdict)})
		}
		out = append(out, sv)
	}
	return out
}
