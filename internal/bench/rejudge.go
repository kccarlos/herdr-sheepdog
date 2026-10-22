package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// RescoreExplain scores an explain scenario's recorded run again (the
// run record's lines and bot utterances, and the events the campaign
// wrote): a new judge or a changed scorer without paying for the call
// again. The run's explanation failures are replaced and its status
// recomputed.
func RescoreExplain(ctx context.Context, j eval.Judge, sc Scenario, r *Run, events []voiceclient.Event) {
	if sc.Explain == nil || r.Status == eval.RunNotRun || r.EndReason == eval.EndError {
		return
	}
	ex := explainExchanges(sc.Explain, r.User, voiceclient.Recording{Events: events, Bot: r.Bot})
	var kept []eval.Failure
	for _, f := range r.Failures {
		if strings.HasPrefix(f.Detail, "explain_") || strings.HasPrefix(f.Detail, "judge explain_") {
			continue
		}
		kept = append(kept, f)
	}
	r.Explain = eval.ScoreExplain(ctx, j, sc.Explain, sc.Language, ex, true)
	r.Failures = append(kept, r.Explain.Failures()...)
	r.Status = eval.StatusOf(r.Failures)
}

// Rejudge asks a recorded run's judge items again (the scenario's
// criteria as the code states them now, the recorded transcript and
// facts) and replaces its verdicts, the failures they produced and its
// status. Runs without facts (errored, not run) are left alone.
func Rejudge(ctx context.Context, j eval.Judge, sc Scenario, r *Run) error {
	if r.Status == eval.RunNotRun || r.EndReason == eval.EndError || r.Facts == "" || len(r.Verdicts) == 0 {
		return nil
	}
	names, criteria := eval.RubricFor(eval.WithPersona(sc.SimScenario, r.Persona))
	vs := eval.JudgeAll(ctx, j, criteria, eval.RenderTranscript(r.Transcript), r.Facts, true)
	out := make([]eval.NamedVerdict, len(vs))
	for i, v := range vs {
		if v.Err != "" {
			return fmt.Errorf("%s r%d %s: %s", r.Scenario, r.Repeat, names[i], v.Err)
		}
		out[i] = eval.NamedVerdict{Name: names[i], Verdict: v}
	}
	r.Verdicts = out
	r.Failures = eval.RejudgedFailures(r.Failures, out)
	r.Status = eval.StatusOf(r.Failures)
	return nil
}

// RejudgeSegments scores a recorded workday run per segment
// (eval.JudgeSegments) and replaces its segment verdicts; the run-level
// verdicts and the status are untouched.
func RejudgeSegments(ctx context.Context, j eval.Judge, sc Scenario, r *Run) error {
	if sc.Workday == nil || r.Status == eval.RunNotRun || r.EndReason == eval.EndError || r.Facts == "" || len(r.Transcript) == 0 {
		return nil
	}
	sim := eval.WithPersona(sc.SimScenario, r.Persona)
	segs, err := eval.JudgeSegments(ctx, j, sim, r.Transcript, r.Facts, eval.SegmentOptions{Actions: eval.CallActions(r.Actions, nil), Parallel: 4})
	if err != nil {
		return fmt.Errorf("%s r%d: %w", r.Scenario, r.Repeat, err)
	}
	r.Segments = segs
	r.NotExercised = eval.NotExercised(sim, segs)
	return nil
}

// LoadEvents reads a run's events file (events/<id>.r<n>.jsonl).
func LoadEvents(path string) ([]voiceclient.Event, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []voiceclient.Event
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev voiceclient.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}
