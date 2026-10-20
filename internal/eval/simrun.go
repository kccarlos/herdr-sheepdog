package eval

import (
	"bytes"
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
	"github.com/kccarlos/herdr-sheepdog/internal/northstar"
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
	// BrainConfig is the orchestrator's configuration (the daemon's own, from
	// app.BrainConfig, so timeouts and steps are what the user gets).
	BrainConfig brain.Config
	// NewBrainModel returns the orchestrator's model for one run; nil runs the
	// scripted fake (offline).
	NewBrainModel func() brain.Model
	// Fallbacks are more providers registered on the orchestrator next to
	// BrainConfig.Provider (id to model), so its failover is exercised:
	// a 429/5xx/timeout on the primary retries the step on the next one.
	Fallbacks map[string]brain.Model
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
	// Offline marks a harness check (the scripted orchestrator): what reached
	// the workers and the judge's items are not graded, since a scripted
	// orchestrator decides nothing; a run passes when the conversation ran.
	// Scripted delays, waits and the silence cap shrink by TimeScale.
	Offline bool
	// TimeScale multiplies scripted delays, waits and the silence cap
	// (default 1; offline 0.02).
	TimeScale float64
	// Personas are the simulated-user personas to rotate through by
	// repeat (persona.go); empty keeps each scenario's own.
	Personas []string
	// Screen is the simulated phone screen the show tool puts cards on
	// (simscreen.go): "" (none), visible, hidden or alternate.
	Screen string
}

// NamedVerdict is a rubric item's verdict.
type NamedVerdict struct {
	Name string `json:"name"`
	Verdict
}

// SimRun is one simulated conversation's record.
// SimFaultHit is one turn a scenario fault hit: the user's words, the
// answer, and what the user heard of it.
type SimFaultHit struct {
	Turn   int    `json:"turn"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Spoken string `json:"spoken,omitempty"`
	Heard  string `json:"heard,omitempty"`
}

type SimRun struct {
	// Faults are the turns the scenario's faults hit (simfaults.go).
	Faults    []SimFaultHit `json:"faults,omitempty"`
	Scenario  string        `json:"scenario"`
	Repeat    int           `json:"repeat"`
	Persona   string        `json:"persona,omitempty"`
	HeldOut   bool          `json:"held_out,omitempty"`
	Status    RunStatus     `json:"status"`
	Failures  []Failure     `json:"failures,omitempty"`
	EndReason string        `json:"end_reason"`
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
	// Facts is the ground truth the judge got (so the run can be judged
	// again later; absent before 2026-10-06). Judge is the judge model
	// of Verdicts when they were given again (sheepdog bench rejudge).
	Facts string `json:"facts,omitempty"`
	Judge string `json:"judge,omitempty"`
	// Segments are the segment judge's verdicts (workday scenarios: one
	// call per agenda beat, JudgeSegments); SegmentJudge is their judge
	// model when given by sheepdog bench rejudge.
	Segments     []SegmentResult `json:"segments,omitempty"`
	SegmentJudge string          `json:"segment_judge,omitempty"`
	// NotExercised are the workday beat items (clarify_N, coherence_N)
	// whose agenda item no user line performed, or the judge found in no
	// segment: not scored, neither a pass nor a fail.
	NotExercised []string `json:"not_exercised,omitempty"`
	Fallbacks    []string `json:"fallbacks,omitempty"`
	// Failovers counts the orchestrator's failovers to another provider
	// (brain.failover events), listed in FailoverEvents; AnsweredBy is
	// the provider of the last answered orchestrator step and StepsByProvider
	// how many steps each provider answered.
	Failovers       int            `json:"failovers"`
	FailoverEvents  []string       `json:"failover_events,omitempty"`
	AnsweredBy      string         `json:"answered_by,omitempty"`
	StepsByProvider map[string]int `json:"steps_by_provider,omitempty"`
	ToolCalls       []string       `json:"tool_calls,omitempty"`
	Usage           []MeterLine    `json:"usage,omitempty"`
	CostUSD         float64        `json:"cost_usd_estimate"`
	// Extended is set when the judge said wait at end_call and the
	// harness kept listening for the answer.
	Extended bool `json:"extended,omitempty"`
	// Explain is the explanation scores of a scenario with an explain
	// block (orchestrator side only: tier 1 has no voice model).
	Explain *ExplainScore `json:"explain,omitempty"`
	// Screen is the run's simulated screen (visible, hidden; absent
	// with none) and Cards what the orchestrator showed on it.
	Screen string    `json:"screen,omitempty"`
	Cards  []SimCard `json:"cards,omitempty"`
}

// RunSim runs one scenario once (repeat is the run's index, for the
// record). Every model call is metered on a per-run meter that the
// record carries and campaign merges.
func RunSim(ctx context.Context, sc SimScenario, o SimOptions, repeat int) SimRun {
	t0 := time.Now()
	persona := PersonaFor(o.Personas, sc.ID, repeat)
	if sc.Fixed {
		persona = "" // a fixed script's exact words are the point
	}
	sc = WithPersona(sc, persona)
	caps := sc.Caps.withDefaults()
	meter := NewMeter()
	run := SimRun{Scenario: sc.ID, Repeat: repeat, Persona: persona, HeldOut: sc.Workday != nil && sc.Workday.HeldOut, Screen: ScreenFor(o.Screen, sc.ID, repeat)}
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

	fileSink, closeSink := eventSink(o.EventsDir, sc.ID, repeat)
	defer closeSink()
	sink, logged := teeEvents(fileSink)
	scale := o.TimeScale
	if scale <= 0 {
		scale = 1
		if o.Offline {
			scale = 0.02
		}
	}
	w, err := newSimWorld(ctx, sc, simWorldOptions{brainCfg: o.BrainConfig, model: model, fallbacks: o.Fallbacks, fixtureDir: o.FixtureDir, meter: meter, events: sink, timeScale: scale, screen: run.Screen})
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
	var exchanges []ExplainExchange
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
			t = AgendaOpening(sc)
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
			// A wait sends nothing to the orchestrator: the marker is for
			// the simulated user and the judge only.
			convo = append(convo, SimLine{AtMS: w.since(), Who: "system", Text: simWaitMarker, Beat: t.Beat, Decl: t.Decl, Items: t.Items})
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
			at := w.since()
			convo = append(convo, SimLine{AtMS: at, Who: "ceo", Text: t.Text, Beat: t.Beat, Tag: t.Tag, Decl: t.Decl, Items: t.Items})
			f := sc.Faults.on(run.Turns)
			res, heard, serr := w.say(ctx, t.Text, f, sc.Faults.cutWords())
			if f != faultNone {
				run.Faults = append(run.Faults, SimFaultHit{Turn: run.Turns, Kind: string(f), Text: t.Text, Spoken: strings.TrimSpace(res.Spoken), Heard: heard})
			}
			if sc.Explain != nil {
				exchanges = append(exchanges, ExplainExchange{Turn: run.Turns, AtMS: at, Kind: sc.Explain.Kind(run.Turns), Question: t.Text, Consulted: true, Orchestrator: JoinOrchestrator(res.Spoken, res.Detail, res.Options), Spoken: strings.TrimSpace(res.Spoken)})
			}
			reply := strings.TrimSpace(res.Spoken)
			if len(res.Options) > 0 {
				reply = strings.TrimSpace(reply + " Options: " + strings.Join(res.Options, "; ") + ".")
			}
			if f == faultCut || f == faultUnvoiced {
				// The user heard only this much of it (simfaults.go).
				reply = heard
			}
			if serr != nil {
				w.rec.mu.Lock()
				w.rec.fallbacks = append(w.rec.fallbacks, "to_supervisor: "+firstLine(serr.Error()))
				w.rec.mu.Unlock()
			}
			got := append(w.push.drain(), SimLine{AtMS: w.since(), Who: "sheepdog", Text: reply})
			w.afterTurn(run.Turns, t.CueBeats())
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

	// Wait = continue: the user hung up while the answer was still on its
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
	run.Facts = facts
	run.Transcript = convo
	run.Actions = w.rec.actions()
	if w.screen != nil {
		run.Cards = w.screen.shown()
	}
	w.mu.Lock()
	run.Timeline = append([]SimLine(nil), w.timeline...)
	w.mu.Unlock()
	w.rec.mu.Lock()
	run.Fallbacks = append([]string(nil), w.rec.fallbacks...)
	run.ToolCalls = append([]string(nil), w.rec.toolCalls...)
	timeouts := w.rec.timeouts
	run.FailoverEvents = append([]string(nil), w.rec.failovers...)
	run.Failovers = len(run.FailoverEvents)
	run.AnsweredBy = w.rec.answeredBy
	if len(w.rec.steps) > 0 {
		run.StepsByProvider = map[string]int{}
		for k, v := range w.rec.steps {
			run.StepsByProvider[k] = v
		}
	}
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
	if sc.Workday != nil {
		// Segment verdicts are measurement only: a failed segment call
		// leaves them out (bench rejudge can add them later).
		if segs, err := JudgeSegments(ctx, judge, sc, convo, facts, SegmentOptions{Actions: CallActions(run.Actions, logged()), Parallel: 4}); err == nil {
			run.Segments = segs
			run.NotExercised = NotExercised(sc, segs)
		}
	}
	if sc.Explain != nil {
		run.Explain = ScoreExplain(ctx, judge, sc.Explain, sc.Language, withPushes(exchanges, convo), false)
		run.Failures = append(run.Failures, run.Explain.Failures()...)
	}
	return finish()
}

// simFailures labels a finished run: the orchestrator's fallbacks, the code
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

// withPushes adds each push to the orchestrator's text of the question
// it followed (tier 1: a push is the orchestrator's own line).
func withPushes(ex []ExplainExchange, convo []SimLine) []ExplainExchange {
	for _, l := range convo {
		if l.Who != "push" {
			continue
		}
		for i := len(ex) - 1; i >= 0; i-- {
			if l.AtMS >= ex[i].AtMS {
				ex[i].Orchestrator = strings.TrimSpace(ex[i].Orchestrator + " " + l.Text)
				ex[i].Spoken = strings.TrimSpace(ex[i].Spoken + " " + l.Text)
				break
			}
		}
	}
	return ex
}

// renderSim is the conversation as the user lived it, for the judge.
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
		case "screen":
			b.WriteString(at + "(Sheepdog put a card on the CEO's phone screen, seen, not heard: " + l.Text + ")")
		default:
			continue
		}
		b.WriteString("\n")
	}
	return b.String()
}

// teeEvents passes each event to sink (when set) and keeps it; logged
// returns the kept events as the north-star checks read them (the
// segment judge's action list).
func teeEvents(sink func(SimLogEvent)) (func(SimLogEvent), func() []northstar.Event) {
	var mu sync.Mutex
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	return func(e SimLogEvent) {
			if sink != nil {
				sink(e)
			}
			mu.Lock()
			defer mu.Unlock()
			_ = enc.Encode(e)
		}, func() []northstar.Event {
			mu.Lock()
			defer mu.Unlock()
			evs, _ := northstar.ParseEvents(bytes.NewReader(buf.Bytes()))
			return evs
		}
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
