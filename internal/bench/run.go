package bench

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/world"
	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// RunOptions configure the runs of a campaign.
type RunOptions struct {
	// Config is the daemon configuration the scratch daemons start from.
	Config config.Config
	// BrainProvider and BrainModel pick the scratch daemons' orchestrator
	// (default deepseek, deepseek-flash; DaemonOptions).
	BrainProvider, BrainModel string
	// Scratch is where each run's daemon directory goes.
	Scratch string
	// FixtureDir is testdata/herdr.
	FixtureDir string
	// Synth renders the user's lines.
	Synth speech.Synth
	// NewSimUser and NewJudge build the simulated user and the judge for
	// one run, metering on m (nil: the scenario's script and the fake
	// judge).
	NewSimUser func(m *eval.Meter) eval.SimUser
	NewJudge   func(m *eval.Meter) eval.Judge
	// Out, when set, gets each run's record and events.
	Out string
	// SaveAudio, when set, gets each run's microphone and bot audio as
	// WAV (keep it outside the repository).
	SaveAudio string
	Log       io.Writer
	// Personas are the simulated-user personas rotated by repeat
	// (eval.PersonaFor); empty keeps each scenario's own.
	Personas []string
}

// UserLine is one thing the simulated user said.
type UserLine struct {
	AtMS  int64  `json:"at_ms"`
	Said  string `json:"said"`
	Heard string `json:"heard_by_voice"`
	// WER of what the voice model heard (CER for Chinese).
	WER       float64 `json:"wer"`
	Voice     string  `json:"voice"`
	Style     string  `json:"style,omitempty"`
	Source    string  `json:"tts_source"`
	DurMS     int     `json:"dur_ms"`
	BargeIn   bool    `json:"barge_in,omitempty"`
	Collision bool    `json:"collision,omitempty"`
}

// VoiceStats are a run's voice-layer counters.
type VoiceStats struct {
	BotUtterances int `json:"bot_utterances"`
	Pushes        int `json:"pushes"`
	// Flushes are the daemon's flushes, LocalFlushes the phone's own
	// barge-in cuts.
	Flushes      int     `json:"flushes"`
	LocalFlushes int     `json:"local_flushes"`
	Unheard      int     `json:"push_unheard"`
	Requeued     int     `json:"push_requeued"`
	Deferred     int     `json:"push_deferred"`
	Held         int     `json:"push_held"`
	Interrupted  int     `json:"interrupted"`
	Stalled      int     `json:"stalled"`
	Reconnects   int     `json:"voice_reconnects"`
	ToolCalls    int     `json:"tool_calls"`
	MeanWER      float64 `json:"mean_wer"`
	NotHeard     int     `json:"lines_not_heard"`
}

// Run is one voice conversation's record.
type Run struct {
	Scenario    string         `json:"scenario"`
	Category    string         `json:"category"`
	Repeat      int            `json:"repeat"`
	Persona     string         `json:"persona,omitempty"`
	HeldOut     bool           `json:"held_out,omitempty"`
	Port        int            `json:"port"`
	Status      eval.RunStatus `json:"status"`
	Failures    []eval.Failure `json:"failures,omitempty"`
	EndReason   string         `json:"end_reason"`
	EndDetail   string         `json:"end_detail,omitempty"`
	GoalMetSelf *bool          `json:"goal_met_self,omitempty"`
	Turns       int            `json:"turns"`
	DurationMS  int64          `json:"duration_ms"`
	// Transcript is the call as the user lived it: what they said and
	// what they heard (cut where they cut the bot off).
	Transcript []eval.SimLine             `json:"transcript"`
	User       []UserLine                 `json:"user_lines"`
	Bot        []voiceclient.BotUtterance `json:"bot_utterances"`
	Actions    []eval.SimLine             `json:"actions,omitempty"`
	Timeline   []eval.SimLine             `json:"timeline,omitempty"`
	Verdicts   []eval.NamedVerdict        `json:"verdicts,omitempty"`
	Facts      string                     `json:"facts,omitempty"`
	// Judge is the judge model of Verdicts when they were given again
	// (sheepdog bench rejudge).
	Judge string `json:"judge,omitempty"`
	// Segments are the segment judge's verdicts (workday scenarios,
	// eval.JudgeSegments); SegmentJudge is their judge model when given by
	// sheepdog bench rejudge.
	Segments     []eval.SegmentResult `json:"segments,omitempty"`
	SegmentJudge string               `json:"segment_judge,omitempty"`
	// NotExercised: beat items no segment scored (eval.NotExercised).
	NotExercised []string         `json:"not_exercised,omitempty"`
	Latency      []map[string]any `json:"latency,omitempty"`
	Voice        VoiceStats       `json:"voice"`
	Failovers    []string         `json:"failovers,omitempty"`
	Fallbacks    []string         `json:"fallbacks,omitempty"`
	Proxy        *ProxyStats      `json:"proxy,omitempty"`
	Spend        Spend            `json:"spend"`
	CostUSD      float64          `json:"cost_usd_estimate"`
	Extended     bool             `json:"extended,omitempty"`
	// Explain is the explanation scores of a scenario with an explain
	// block: the orchestrator's text and the voice's speech separately.
	Explain *eval.ExplainScore `json:"explain,omitempty"`
	// Orchestrator are the content items asked of the orchestrator's own
	// text (bench report --rescore-orchestrator; orchestrator.go): the
	// voice side's are Verdicts.
	Orchestrator []eval.NamedVerdict `json:"orchestrator_verdicts,omitempty"`
	// Excerpt is a compact event timeline for the report.
	Excerpt []string `json:"excerpt,omitempty"`
}

// ProxyStats are the fault proxy's counters.
type ProxyStats struct {
	Requests int `json:"requests"`
	Failed   int `json:"failed"`
}

// Record is the run's eval.RunRecord (stats, taxonomy).
func (r Run) Record() eval.RunRecord {
	return eval.RunRecord{Case: r.Scenario, Repeat: r.Repeat, Status: r.Status, Failures: r.Failures, DurationMS: r.DurationMS, CostUSD: r.CostUSD}
}

// runner is one conversation in progress.
type runner struct {
	sc     Scenario
	o      RunOptions
	rng    *rand.Rand
	c      *voiceclient.Client
	d      *Daemon
	w      *world.World
	user   eval.SimUser
	meter  *SpendMeter
	run    *Run
	convo  []eval.SimLine
	lines  []UserLine
	voice  string
	bargeN int
	// answering: hear is listening for the answer to the user (the only
	// time the user's barge-in habit applies; not the connect settle).
	answering bool

	eventFails []eval.Failure // the scenario's voice event checks
	exchanges  []eval.ExplainExchange

	fireMu sync.Mutex
	fireAt time.Time // the collision's event landed
	collid bool
}

// RunScenario runs sc once on port (repeat is the run's index).
func RunScenario(ctx context.Context, sc Scenario, repeat, port int, o RunOptions) Run {
	t0 := time.Now()
	persona := eval.PersonaFor(o.Personas, sc.ID, repeat)
	if sc.Fixed {
		persona = "" // a fixed script's exact words are the point
	}
	sc.SimScenario = eval.WithPersona(sc.SimScenario, persona)
	run := Run{Scenario: sc.ID, Category: sc.Category, Repeat: repeat, Persona: persona, HeldOut: sc.Workday != nil && sc.Workday.HeldOut, Port: port}
	meter := NewSpendMeter(o.Config.Gemini.Model)
	r := &runner{sc: sc, o: o, rng: rand.New(rand.NewSource(sc.Seed + int64(repeat)*7919 + 1)), meter: meter, run: &run}
	fail := func(t eval.FailureType, format string, args ...any) {
		run.Failures = append(run.Failures, eval.Failure{Type: t, Detail: fmt.Sprintf(format, args...)})
	}
	finish := func() Run {
		run.DurationMS = time.Since(t0).Milliseconds()
		run.Spend = meter.Total()
		run.CostUSD = run.Spend.TotalUSD
		run.Status = eval.StatusOf(run.Failures)
		r.save()
		return run
	}

	// The scratch daemon, behind the fault proxy when the scenario asks.
	dir := filepath.Join(o.Scratch, fmt.Sprintf("%s.r%d", sc.ID, repeat))
	_ = os.RemoveAll(dir)
	dopts := DaemonOptions{Config: o.Config, Port: port, Dir: dir, Querier: world.Querier{Agents: sc.Agents}, Log: io.Discard, BrainProvider: o.BrainProvider, BrainModel: o.BrainModel}
	var proxy *FaultProxy
	if sc.Faults.Active() {
		p, err := StartFaultProxy(o.Config.DeepSeek.BaseURL, sc.Faults)
		if err != nil {
			fail(eval.FailError, "fault proxy: %v", err)
			return finish()
		}
		proxy = p
		defer p.Close()
		dopts.DeepSeekBaseURL = p.URL
	}
	d, err := StartDaemon(ctx, dopts)
	if err != nil {
		run.EndReason = eval.EndError
		fail(eval.FailError, "daemon: %v", err)
		return finish()
	}
	defer func() {
		d.Close()
		_ = os.RemoveAll(dir) // the daemon's data: the run keeps its own copy of the events
	}()

	// The phone.
	bed, err := NewBed(kindOf(sc.Audio.Noise), sc.Seed+int64(repeat))
	if err != nil {
		fail(eval.FailError, "noise: %v", err)
		return finish()
	}
	floor := sc.Audio.FloorDBFS
	if floor == 0 {
		floor = -75
	}
	bed.SetFloor(floor, sc.Seed)
	copts := voiceclient.Options{BaseURL: d.URL, Bed: bed.Next, Echo: sc.Audio.Echo, Gate: sc.Audio.Gate}
	if b := sc.Behavior.BargeIn; b != nil && b.Local {
		copts.LocalBargeIn = true
	}
	c, err := voiceclient.Dial(ctx, copts)
	if err != nil {
		fail(eval.FailError, "phone: %v", err)
		return finish()
	}
	r.c = c
	r.d = d
	var rec voiceclient.Recording
	closed := false
	closeClient := func() {
		if !closed {
			rec = c.Close()
			closed = true
		}
	}
	defer closeClient()
	if err := c.WaitConnected(ctx, 45*time.Second); err != nil {
		closeClient()
		r.collect(rec, nil)
		run.EndReason = eval.EndError
		fail(eval.FailError, "voice: %v", err)
		return finish()
	}

	// The scripted workers.
	wopts := world.Options{BaseURL: d.URL, FixtureDir: o.FixtureDir, Clock0: time.Now().Add(-time.Duration(c.SinceMS()) * time.Millisecond)}
	if col := sc.Behavior.Collision; col != nil {
		wopts.OnFire = func(i int, _ eval.SimEvent) {
			if i == col.Event {
				r.fireMu.Lock()
				if r.fireAt.IsZero() {
					r.fireAt = time.Now()
				}
				r.fireMu.Unlock()
			}
		}
	}
	w, err := world.New(sc.SimScenario, wopts)
	if err == nil {
		err = w.Start(ctx)
	}
	if err != nil {
		closeClient()
		r.collect(rec, nil)
		run.EndReason = eval.EndError
		fail(eval.FailError, "world: %v", err)
		return finish()
	}
	r.w = w
	defer w.Close()

	r.user = eval.ScriptedSimUser{}
	if o.NewSimUser != nil && !sc.Fixed {
		r.user = o.NewSimUser(meter.Meter)
	}
	var judge eval.Judge = &eval.FakeJudge{}
	if o.NewJudge != nil {
		judge = o.NewJudge(meter.Meter)
	}
	r.voice = VoiceFor(sc)

	r.converse(ctx)

	// Wait = continue: the user hung up while the answer was on its way.
	names, criteria := eval.RubricFor(sc.SimScenario)
	caps := capsOf(sc)
	deadline := t0.Add(time.Duration(caps.MaxSeconds) * time.Second)
	if run.EndReason == eval.EndCall && len(criteria) > 0 && names[0] == "answered" && time.Now().Before(deadline) {
		v, jerr := judge.Judge(ctx, eval.JudgeInput{Criterion: criteria[0], Transcript: eval.RenderTranscript(r.convo), Facts: w.Facts(), Final: false})
		if jerr == nil && v.Answer == eval.AnswerWait {
			run.Extended = true
			r.hear(ctx, 3*time.Second, minDur(time.Duration(caps.SilenceSeconds)*time.Second, time.Until(deadline)), false)
		}
	}
	// Trailing pushes, then hang up.
	r.hear(ctx, 2500*time.Millisecond, 12*time.Second, false)
	closeClient()
	facts := w.Facts()
	run.Facts = facts
	run.Actions, run.Timeline = w.Actions(), w.Timeline()
	for _, e := range w.Errors() {
		fail(eval.FailError, "world: %s", e)
	}
	r.collect(rec, proxy)
	eval.SortLines(r.convo)
	run.Transcript = r.convo

	if run.EndReason == eval.EndError {
		return finish()
	}
	for i, v := range eval.JudgeAll(ctx, judge, criteria, eval.RenderTranscript(r.convo), facts, true) {
		run.Verdicts = append(run.Verdicts, eval.NamedVerdict{Name: names[i], Verdict: v})
	}
	run.Failures = append(run.Failures, r.failures()...)
	if sc.Workday != nil {
		// Measurement only: a failed segment call leaves them out.
		if segs, err := eval.JudgeSegments(ctx, judge, sc.SimScenario, r.convo, facts, eval.SegmentOptions{Actions: eval.CallActions(run.Actions, nil), Parallel: 4}); err == nil {
			run.Segments = segs
			run.NotExercised = eval.NotExercised(sc.SimScenario, segs)
		}
	}
	if sc.Explain != nil {
		run.Explain = eval.ScoreExplain(ctx, judge, sc.Explain, sc.Language, r.exchanges, true)
		run.Failures = append(run.Failures, run.Explain.Failures()...)
	}
	return finish()
}

func kindOf(n *NoiseCond) string {
	if n == nil {
		return ""
	}
	return n.Kind
}

func capsOf(sc Scenario) eval.SimCaps {
	c := sc.Caps
	if c.MaxTurns <= 0 {
		c.MaxTurns = 6
	}
	if c.MaxSeconds <= 0 {
		c.MaxSeconds = 240
	}
	if c.SilenceSeconds <= 0 {
		c.SilenceSeconds = 40
	}
	return c
}

func (r *runner) since() int64 { return r.c.SinceMS() }

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

func hasHanText(s string) bool {
	return len(Tokens(s)) > 0 && strings.ContainsFunc(s, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff })
}

// sortRuns orders runs by scenario then repeat.
func sortRuns(runs []Run) {
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].Scenario != runs[j].Scenario {
			return runs[i].Scenario < runs[j].Scenario
		}
		return runs[i].Repeat < runs[j].Repeat
	})
}
