package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// End reasons of a simulated conversation.
const (
	EndCall     = "end_call"
	EndMaxTurns = "max_turns"
	EndMaxTime  = "max_time"
	EndSilence  = "silence"
	EndError    = "error"
)

// SimOptions configure simulated runs.
type SimOptions struct {
	// BrainConfig is the brain's configuration (the daemon's own, from
	// app.BrainConfig, so timeouts and steps are what the CEO gets).
	BrainConfig brain.Config
	// NewBrainModel returns the brain's model for one run; nil runs the
	// scripted fake (offline).
	NewBrainModel func() brain.Model
	// NewSimUser and NewJudge build the simulated user and the judge for
	// one run, metering on m; nil NewSimUser is the scripted user, nil
	// NewJudge the fake judge (every item yes).
	NewSimUser func(m *Meter) SimUser
	NewJudge   func(m *Meter) Judge
	FixtureDir string
	// EventsDir, when set, gets one JSONL file of daemon events per run.
	EventsDir string
	// Settle is how long to wait after a reply for pushes it set off
	// (default 1.5 s).
	Settle time.Duration
	// Offline marks a harness check (the scripted brain): what reached
	// the workers and the judge's items are not graded, since a scripted
	// brain decides nothing; a run passes when the conversation ran.
	// Scripted delays, waits and the silence cap shrink by TimeScale.
	Offline bool
	// TimeScale multiplies scripted delays, waits and the silence cap
	// (default 1; offline 0.02).
	TimeScale float64
}

// NamedVerdict is a rubric item's verdict.
type NamedVerdict struct {
	Name string `json:"name"`
	Verdict
}

// SimRun is one simulated conversation's record.
type SimRun struct {
	Scenario  string    `json:"scenario"`
	Repeat    int       `json:"repeat"`
	Status    RunStatus `json:"status"`
	Failures  []Failure `json:"failures,omitempty"`
	EndReason string    `json:"end_reason"`
	// EndDetail is end_call's reason, or the error.
	EndDetail string `json:"end_detail,omitempty"`
	// GoalMetSelf is the simulated user's own opinion at end_call:
	// recorded, never graded.
	GoalMetSelf *bool          `json:"goal_met_self,omitempty"`
	Turns       int            `json:"turns"`
	DurationMS  int64          `json:"duration_ms"`
	Transcript  []SimLine      `json:"transcript"`
	Actions     []SimLine      `json:"actions,omitempty"`
	Timeline    []SimLine      `json:"timeline,omitempty"`
	Verdicts    []NamedVerdict `json:"verdicts,omitempty"`
	Fallbacks   []string       `json:"fallbacks,omitempty"`
	ToolCalls   []string       `json:"tool_calls,omitempty"`
	Usage       []MeterLine    `json:"usage,omitempty"`
	CostUSD     float64        `json:"cost_usd_estimate"`
	// Extended is set when the judge said wait at end_call and the
	// harness kept listening for the answer.
	Extended bool `json:"extended,omitempty"`
}

// RunSim runs one scenario once (repeat is the run's index, for the
// record). Every model call is metered on a per-run meter that the
// record carries and campaign merges.
func RunSim(ctx context.Context, sc SimScenario, o SimOptions, repeat int) SimRun {
	t0 := time.Now()
	caps := sc.Caps.withDefaults()
	meter := NewMeter()
	run := SimRun{Scenario: sc.ID, Repeat: repeat}
	finish := func() SimRun {
		run.DurationMS = time.Since(t0).Milliseconds()
		run.Usage, run.CostUSD = meter.Lines(), meter.Total()
		run.Status = statusOf(run.Failures)
		return run
	}

	var model brain.Model
	if o.NewBrainModel != nil {
		model = o.NewBrainModel()
	} else {
		fake := braintest.NewModel()
		fake.Default = &braintest.Step{Text: braintest.Reply("Offline scripted answer.", "")}
		fake.EventDefault = braintest.NothingToAdd()
		model = fake
	}
	var user SimUser = ScriptedSimUser{}
	if o.NewSimUser != nil && !sc.Fixed {
		user = o.NewSimUser(meter)
	}
	var judge Judge = &FakeJudge{}
	if o.NewJudge != nil {
		judge = o.NewJudge(meter)
	}

	sink, closeSink := eventSink(o.EventsDir, sc.ID, repeat)
	defer closeSink()
	scale := o.TimeScale
	if scale <= 0 {
		scale = 1
		if o.Offline {
			scale = 0.02
		}
	}
	w, err := newSimWorld(ctx, sc, simWorldOptions{brainCfg: o.BrainConfig, model: model, fixtureDir: o.FixtureDir, meter: meter, events: sink, timeScale: scale})
	if err != nil {
		run.EndReason, run.EndDetail = EndError, err.Error()
		run.Failures = append(run.Failures, Failure{FailError, "world: " + err.Error()})
		return finish()
	}
	initial := map[string]string{}
	w.mu.Lock()
	for k, v := range w.screens {
		initial[k] = v
	}
	w.mu.Unlock()

	settle := o.Settle
	if settle == 0 {
		settle = time.Duration(float64(1500*time.Millisecond) * scale)
	}
	deadline := t0.Add(time.Duration(caps.MaxSeconds) * time.Second)
	silence := time.Duration(float64(caps.SilenceSeconds) * float64(time.Second) * scale)
	minWait := time.Duration(float64(5*time.Second) * scale)
	var convo []SimLine
	convo = append(convo, w.push.drain()...)
	silent := time.Duration(0)
	waits := 0

loop:
	for {
		if time.Now().After(deadline) {
			run.EndReason = EndMaxTime
			break
		}
		var t SimTurn
		if run.Turns == 0 && sc.Opening != "" {
			t = SimTurn{Kind: TurnSay, Text: sc.Opening}
		} else {
			uctx, cancel := context.WithDeadline(ctx, deadline)
			t, err = user.Next(uctx, sc, convo)
			cancel()
			if err != nil {
				if time.Now().After(deadline) {
					run.EndReason = EndMaxTime
					break
				}
				run.EndReason, run.EndDetail = EndError, err.Error()
				run.Failures = append(run.Failures, Failure{FailError, firstLine(err.Error())})
				break
			}
		}
		switch t.Kind {
		case TurnEnd:
			run.EndReason, run.EndDetail = EndCall, t.Reason
			met := t.GoalMet
			run.GoalMetSelf = &met
			break loop
		case TurnWait:
			waits++
			if waits > caps.MaxTurns {
				run.EndReason = EndMaxTurns
				break loop
			}
			convo = append(convo, SimLine{AtMS: w.since(), Who: "system", Text: simWaitMarker})
			d := clampDur(time.Duration(float64(t.WaitSeconds)*float64(time.Second)*scale), minWait, silence-silent)
			got := waitPushes(w, d, settle, deadline)
			if len(got) == 0 {
				silent += d
				if silent >= silence {
					run.EndReason = EndSilence
					break loop
				}
				convo = append(convo, SimLine{AtMS: w.since(), Who: "system", Text: fmt.Sprintf("%d seconds of silence so far.", int(silent.Seconds()/scale))})
				continue
			}
			silent = 0
			convo = append(convo, got...)
		case TurnSay:
			if run.Turns >= caps.MaxTurns {
				run.EndReason = EndMaxTurns
				break loop
			}
			run.Turns++
			convo = append(convo, SimLine{AtMS: w.since(), Who: "ceo", Text: t.Text})
			res, serr := w.say(ctx, t.Text)
			reply := strings.TrimSpace(res.Spoken)
			if len(res.Options) > 0 {
				reply = strings.TrimSpace(reply + " Options: " + strings.Join(res.Options, "; ") + ".")
			}
			if serr != nil {
				w.rec.mu.Lock()
				w.rec.fallbacks = append(w.rec.fallbacks, "to_supervisor: "+firstLine(serr.Error()))
				w.rec.mu.Unlock()
			}
			got := append(w.push.drain(), SimLine{AtMS: w.since(), Who: "sheepdog", Text: reply})
			w.afterTurn(run.Turns)
			got = append(got, waitPushes(w, settle, settle, deadline)...)
			sortLines(got)
			convo = append(convo, got...)
			if reply != "" {
				silent = 0
			}
		default:
			run.EndReason, run.EndDetail = EndError, "unknown turn kind "+t.Kind
			run.Failures = append(run.Failures, Failure{FailError, run.EndDetail})
			break loop
		}
	}

	// Wait = continue: the CEO hung up while the answer was still on its
	// way ("I'm checking"); listen once more for it before the final
	// judgement, within the time cap.
	names, criteria := rubricFor(sc)
	if run.EndReason == EndCall && len(criteria) > 0 && names[0] == "answered" && time.Now().Before(deadline) {
		v, jerr := judge.Judge(ctx, JudgeInput{Criterion: criteria[0], Transcript: renderSim(convo), Facts: w.facts(initial), Final: false})
		if jerr == nil && v.Answer == AnswerWait {
			run.Extended = true
			convo = append(convo, waitPushes(w, minDur(silence, time.Until(deadline)), settle, deadline)...)
		}
	}
	w.brain.Quiesce()
	convo = append(convo, w.push.drain()...)
	sortLines(convo)
	facts := w.facts(initial)
	run.Transcript = convo
	run.Actions = w.rec.actions()
	w.mu.Lock()
	run.Timeline = append([]SimLine(nil), w.timeline...)
	w.mu.Unlock()
	w.rec.mu.Lock()
	run.Fallbacks = append([]string(nil), w.rec.fallbacks...)
	run.ToolCalls = append([]string(nil), w.rec.toolCalls...)
	timeouts := w.rec.timeouts
	w.rec.mu.Unlock()
	w.close()

	if run.EndReason == EndError {
		return finish()
	}
	if o.Offline {
		// A harness check: the conversation ran end to end on the real
		// supervisor with the scenario's screens and cues.
		return finish()
	}
	for _, v := range judgeAll(ctx, judge, criteria, renderSim(convo), facts, true) {
		run.Verdicts = append(run.Verdicts, NamedVerdict{Verdict: v})
	}
	for i := range run.Verdicts {
		run.Verdicts[i].Name = names[i]
	}
	run.Failures = append(run.Failures, simFailures(sc, run, timeouts)...)
	return finish()
}

// simFailures labels a finished run: the brain's fallbacks, the code
// checks on what reached the workers, the judge's verdicts, and — when
// the run failed anyway — the cap that ended it.
func simFailures(sc SimScenario, run SimRun, timeouts int) []Failure {
	var fs []Failure
	for _, fb := range run.Fallbacks {
		switch {
		case isRateLimited(errorString(fb)):
			fs = append(fs, Failure{FailError, "brain rate limited: " + fb})
		case isTimeoutText(fb):
		default:
			fs = append(fs, Failure{FailBrainFallback, fb})
		}
	}
	if timeouts > 0 {
		fs = append(fs, Failure{FailTimeout, fmt.Sprintf("%d brain turn(s) timed out and fell back", timeouts)})
	}
	fs = append(fs, actionFailures(sc.Expect, run.Actions)...)
	for _, v := range run.Verdicts {
		switch {
		case v.Err != "":
			fs = append(fs, Failure{FailError, "judge " + v.Name + ": " + v.Err})
		case !v.Passed():
			fs = append(fs, Failure{FailJudgeNo, v.Name + ": " + orDefault(firstLine(v.Rationale), "no rationale")})
		}
	}
	if len(fs) > 0 {
		switch run.EndReason {
		case EndMaxTime:
			fs = append(fs, Failure{FailTimeout, "the conversation hit its time cap"})
		case EndSilence:
			fs = append(fs, Failure{FailSilence, "the CEO waited in silence past the cap"})
		case EndMaxTurns:
			fs = append(fs, Failure{FailMaxTurns, "every turn used without the call ending"})
		}
	}
	return fs
}

func errorString(s string) error { return fmt.Errorf("%s", s) }

// actionFailures checks what sheepdog sent to the workers against the
// scenario's expectations.
func actionFailures(exp SimExpect, acts []SimLine) []Failure {
	var fs []Failure
	for _, want := range exp.Actions {
		if !anyAction(acts, want) {
			fs = append(fs, Failure{FailMissingCall, fmt.Sprintf("no %s to %s%s", want.Kind, orDefault(want.Agent, "any worker"), matchNote(want.Matches))})
		}
	}
	for _, bad := range exp.Forbid {
		for _, a := range acts {
			if actionMatches(a, bad) {
				fs = append(fs, Failure{FailUnintendedAction, a.Text})
			}
		}
	}
	if exp.NoActions {
		for _, a := range acts {
			fs = append(fs, Failure{FailUnintendedAction, a.Text + " (nothing should have been sent)"})
		}
	}
	if exp.Agent != "" {
		for _, a := range acts {
			if a.Agent != exp.Agent {
				fs = append(fs, Failure{FailWrongAgent, a.Text + " (the CEO meant " + exp.Agent + ")"})
			}
		}
	}
	return fs
}

func matchNote(m string) string {
	if m == "" {
		return ""
	}
	return " matching " + m
}

func anyAction(acts []SimLine, want SimAction) bool {
	for _, a := range acts {
		if actionMatches(a, want) {
			return true
		}
	}
	return false
}

func actionMatches(a SimLine, want SimAction) bool {
	if a.Kind != want.Kind || (want.Agent != "" && a.Agent != want.Agent) {
		return false
	}
	if want.Matches == "" {
		return true
	}
	if want.Kind == ActionPrompt {
		return regexp.MustCompile("(?i)" + want.Matches).MatchString(a.Detail)
	}
	for _, k := range strings.Fields(a.Detail) {
		if strings.EqualFold(k, want.Matches) {
			return true
		}
	}
	return false
}

// waitPushes waits up to d for a push; after the first, it keeps
// listening for settle more (an announcement often comes in a pair).
func waitPushes(w *simWorld, d, settle time.Duration, deadline time.Time) []SimLine {
	if until := time.Until(deadline); d > until {
		d = until
	}
	if d <= 0 {
		return w.push.drain()
	}
	var out []SimLine
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case l := <-w.push.ch:
			out = append(out, l)
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(settle)
		case <-timer.C:
			return out
		}
	}
}

// clampDur bounds d to [lo, hi]; hi wins when they cross.
func clampDur(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		d = lo
	}
	if d > hi {
		d = hi
	}
	return d
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// renderSim is the conversation as the CEO lived it, for the judge.
func renderSim(convo []SimLine) string {
	var b strings.Builder
	for _, l := range convo {
		at := fmt.Sprintf("[t+%ds] ", l.AtMS/1000)
		switch l.Who {
		case "ceo":
			b.WriteString(at + "CEO: " + l.Text)
		case "sheepdog":
			b.WriteString(at + "Sheepdog: " + orDefault(l.Text, "(says nothing)"))
		case "push":
			b.WriteString(at + "Sheepdog (unprompted): " + l.Text)
		case "system":
			b.WriteString(at + "(" + l.Text + ")")
		default:
			continue
		}
		b.WriteString("\n")
	}
	return b.String()
}

// eventSink opens the run's JSONL event file under dir (none when dir
// is empty).
func eventSink(dir, id string, repeat int) (func(SimLogEvent), func()) {
	if dir == "" {
		return nil, func() {}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, func() {}
	}
	f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s.r%d.jsonl", id, repeat)))
	if err != nil {
		return nil, func() {}
	}
	var mu sync.Mutex
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	return func(e SimLogEvent) {
			mu.Lock()
			defer mu.Unlock()
			_ = enc.Encode(e)
		}, func() {
			mu.Lock()
			defer mu.Unlock()
			_ = f.Close()
		}
}
