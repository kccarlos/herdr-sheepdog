package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
)

// Rejudging: asking a recorded run's judge items again with another
// judge (or a changed rubric) without paying for the conversation again.
// The criteria come from the scenario as the code states them now; the
// transcript and the facts come from the record.

// RejudgeSim asks every judge item of a recorded tier-1 run again and
// replaces its verdicts, the failures they produced and its status.
// The facts are the record's own when it has them (runs from
// 2026-10-06 on); older records get them rebuilt by ReplayFacts.
func RejudgeSim(ctx context.Context, j Judge, sc SimScenario, run *SimRun, fixtureDir string) error {
	if run.EndReason == EndError || len(run.Verdicts) == 0 {
		return nil
	}
	sc = WithPersona(sc, run.Persona)
	facts := run.Facts
	if facts == "" {
		f, err := ReplayFacts(sc, *run, fixtureDir)
		if err != nil {
			return err
		}
		facts = f
	}
	names, criteria := rubricFor(sc)
	vs := judgeAll(ctx, j, criteria, renderSim(run.Transcript), facts, true)
	out := make([]NamedVerdict, len(vs))
	for i, v := range vs {
		if v.Err != "" {
			return fmt.Errorf("%s r%d %s: %s", run.Scenario, run.Repeat, names[i], v.Err)
		}
		out[i] = NamedVerdict{Name: names[i], Verdict: v}
	}
	run.Verdicts = out
	run.Failures = RejudgedFailures(run.Failures, out)
	run.Status = statusOf(run.Failures)
	return nil
}

// RejudgeSimSegments scores a recorded tier-1 workday run per segment
// (JudgeSegments) and replaces its segment verdicts; the run-level
// verdicts and the status are untouched. Facts as in RejudgeSim; evs is
// the run's event log (the said_vs_done item's action list; nil gives
// the record's sends only). With only, just those item families are
// asked again and merged into the old verdicts (MergeSegments).
func RejudgeSimSegments(ctx context.Context, j Judge, sc SimScenario, run *SimRun, fixtureDir string, evs []northstar.Event, only []string) error {
	if sc.Workday == nil || run.EndReason == EndError || len(run.Verdicts) == 0 || len(run.Transcript) == 0 {
		return nil
	}
	sc = WithPersona(sc, run.Persona)
	facts := run.Facts
	if facts == "" {
		f, err := ReplayFacts(sc, *run, fixtureDir)
		if err != nil {
			return err
		}
		facts = f
	}
	segs, err := JudgeSegments(ctx, j, sc, run.Transcript, facts, SegmentOptions{Actions: CallActions(run.Actions, evs), Only: only, Parallel: 4})
	if err != nil {
		return fmt.Errorf("%s r%d: %w", run.Scenario, run.Repeat, err)
	}
	if len(only) > 0 {
		segs = MergeSegments(run.Segments, segs, only)
	}
	run.Segments = segs
	run.NotExercised = NotExercised(sc, segs)
	return nil
}

// RejudgedFailures is fs with the failures of the old verdicts (judge
// no, judge errors) replaced by those of vs.
func RejudgedFailures(fs []Failure, vs []NamedVerdict) []Failure {
	var out []Failure
	for _, f := range fs {
		if f.Type == FailJudgeNo || (f.Type == FailError && strings.HasPrefix(f.Detail, "judge ")) {
			continue
		}
		out = append(out, f)
	}
	for _, v := range vs {
		switch {
		case v.Err != "":
			out = append(out, Failure{FailError, "judge " + v.Name + ": " + v.Err})
		case !v.Passed():
			out = append(out, Failure{FailJudgeNo, v.Name + ": " + orDefault(firstLine(v.Rationale), "no rationale")})
		}
	}
	return out
}

// ReplayFacts rebuilds the judge's facts for a tier-1 record that lacks
// them: each worker's screen at the start from the scenario, then the
// recorded timeline replayed against the scenario's events (each
// timeline line is matched to the event whose screen tail it shows) to
// get the screen at the end, the recorded actions, and the final states.
// What the replay cannot see: a prompt's echo on a worker's screen and
// the menu a default answer or interrupt removes (the actions list
// carries both).
func ReplayFacts(sc SimScenario, run SimRun, fixtureDir string) (string, error) {
	resolved, err := ResolveScreens(sc, fixtureDir)
	if err != nil {
		return "", err
	}
	ws := WorldState{Agents: sc.Agents, Kinds: map[string]string{}, States: map[string]string{}, Initial: map[string]string{}, Screens: map[string]string{}, Timeline: run.Timeline, Actions: run.Actions}
	for _, a := range sc.Agents {
		ws.Kinds[a.Alias] = orDefault(a.Kind, "codex")
		ws.States[a.Alias] = orDefault(a.State, "idle")
		ws.Initial[a.Alias] = resolved[a.Screen]
		ws.Screens[a.Alias] = resolved[a.Screen]
	}
	fired := map[int]bool{}
	for _, l := range run.Timeline {
		target := l.Agent
		st, tail, ok := parseTimelineLine(l.Text, target)
		if !ok {
			continue
		}
		ws.States[target] = st
		for i, e := range sc.Events {
			if fired[i] && !e.Repeat {
				continue
			}
			if e.Agent != "" && e.Agent != target {
				continue
			}
			screen := ws.Screens[target]
			if e.Screen != "" {
				screen = resolved[e.Screen]
			}
			if e.AppendScreen != "" {
				screen = strings.TrimRight(screen, "\n") + "\n" + e.AppendScreen + "\n"
			}
			if oneLineTail(screen, 6) == tail {
				fired[i] = true
				ws.Screens[target] = screen
				break
			}
		}
	}
	return WorldFacts(ws), nil
}

// parseTimelineLine reads "<agent> is now <state>; its screen ends: <tail>".
func parseTimelineLine(text, agent string) (state, tail string, ok bool) {
	rest, ok := strings.CutPrefix(text, agent+" is now ")
	if !ok {
		return "", "", false
	}
	state, tail, ok = strings.Cut(rest, "; its screen ends: ")
	return state, tail, ok
}
