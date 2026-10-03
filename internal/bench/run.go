package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
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
	// Scratch is where each run's daemon directory goes.
	Scratch string
	// FixtureDir is testdata/herdr.
	FixtureDir string
	// Synth renders the CEO's lines.
	Synth speech.Synth
	// NewSimUser and NewJudge build the simulated CEO and the judge for
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
}

// UserLine is one thing the simulated CEO said.
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
	Port        int            `json:"port"`
	Status      eval.RunStatus `json:"status"`
	Failures    []eval.Failure `json:"failures,omitempty"`
	EndReason   string         `json:"end_reason"`
	EndDetail   string         `json:"end_detail,omitempty"`
	GoalMetSelf *bool          `json:"goal_met_self,omitempty"`
	Turns       int            `json:"turns"`
	DurationMS  int64          `json:"duration_ms"`
	// Transcript is the call as the CEO lived it: what they said and
	// what they heard (cut where they cut the bot off).
	Transcript []eval.SimLine             `json:"transcript"`
	User       []UserLine                 `json:"user_lines"`
	Bot        []voiceclient.BotUtterance `json:"bot_utterances"`
	Actions    []eval.SimLine             `json:"actions,omitempty"`
	Timeline   []eval.SimLine             `json:"timeline,omitempty"`
	Verdicts   []eval.NamedVerdict        `json:"verdicts,omitempty"`
	Facts      string                     `json:"facts,omitempty"`
	Latency    []map[string]any           `json:"latency,omitempty"`
	Voice      VoiceStats                 `json:"voice"`
	Failovers  []string                   `json:"failovers,omitempty"`
	Fallbacks  []string                   `json:"fallbacks,omitempty"`
	Proxy      *ProxyStats                `json:"proxy,omitempty"`
	Spend      Spend                      `json:"spend"`
	CostUSD    float64                    `json:"cost_usd_estimate"`
	Extended   bool                       `json:"extended,omitempty"`
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
	w      *world.World
	user   eval.SimUser
	meter  *SpendMeter
	run    *Run
	convo  []eval.SimLine
	lines  []UserLine
	voice  string
	bargeN int
	// answering: hear is listening for the answer to the CEO (the only
	// time the CEO's barge-in habit applies; not the connect settle).
	answering bool

	eventFails []eval.Failure // the scenario's voice event checks

	fireMu sync.Mutex
	fireAt time.Time // the collision's event landed
	collid bool
}

// RunScenario runs sc once on port (repeat is the run's index).
func RunScenario(ctx context.Context, sc Scenario, repeat, port int, o RunOptions) Run {
	t0 := time.Now()
	run := Run{Scenario: sc.ID, Category: sc.Category, Repeat: repeat, Port: port}
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
	dopts := DaemonOptions{Config: o.Config, Port: port, Dir: dir, Querier: world.Querier{Agents: sc.Agents}, Log: io.Discard}
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

	// Wait = continue: the CEO hung up while the answer was on its way.
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

// converse is the call: the opening, then the simulated CEO's turns.
func (r *runner) converse(ctx context.Context) {
	run, sc := r.run, r.sc
	caps := capsOf(sc)
	deadline := time.Now().Add(time.Duration(caps.MaxSeconds) * time.Second)
	silence := time.Duration(caps.SilenceSeconds) * time.Second
	// The phone connects to a flock already running: hear what it says
	// first (the initial blocks' announcements).
	r.hear(ctx, 2500*time.Millisecond, 25*time.Second, false)
	r.w.Arm()
	silent := time.Duration(0)
	waits := 0
	for {
		if ctx.Err() != nil || time.Now().After(deadline) {
			run.EndReason = eval.EndMaxTime
			return
		}
		var t eval.SimTurn
		if run.Turns == 0 && sc.Opening != "" {
			t = eval.SimTurn{Kind: eval.TurnSay, Text: sc.Opening}
		} else {
			uctx, cancel := context.WithDeadline(ctx, deadline)
			var err error
			t, err = r.user.Next(uctx, sc.SimScenario, r.convo)
			cancel()
			if err != nil {
				if time.Now().After(deadline) {
					run.EndReason = eval.EndMaxTime
					return
				}
				run.EndReason, run.EndDetail = eval.EndError, err.Error()
				run.Failures = append(run.Failures, eval.Failure{Type: eval.FailError, Detail: eval.FirstLine(err.Error())})
				return
			}
		}
		switch t.Kind {
		case eval.TurnEnd:
			run.EndReason, run.EndDetail = eval.EndCall, t.Reason
			met := t.GoalMet
			run.GoalMetSelf = &met
			return
		case eval.TurnWait:
			waits++
			if waits > caps.MaxTurns {
				run.EndReason = eval.EndMaxTurns
				return
			}
			r.convo = append(r.convo, eval.SimLine{AtMS: r.since(), Who: "system", Text: eval.SimWaitMarker})
			d := clampDur(time.Duration(t.WaitSeconds)*time.Second, 5*time.Second, silence-silent)
			r.answering = true
			got := r.hear(ctx, 2*time.Second, d, true)
			r.answering = false
			if got == 0 {
				silent += d
				if silent >= silence {
					run.EndReason = eval.EndSilence
					return
				}
				r.convo = append(r.convo, eval.SimLine{AtMS: r.since(), Who: "system", Text: fmt.Sprintf("%d seconds of silence so far.", int(silent.Seconds()))})
				continue
			}
			silent = 0
		case eval.TurnSay:
			if run.Turns >= caps.MaxTurns {
				run.EndReason = eval.EndMaxTurns
				return
			}
			if run.Turns > 0 {
				r.think()
			}
			if err := r.say(ctx, t.Text, false, false); err != nil {
				run.EndReason, run.EndDetail = eval.EndError, err.Error()
				run.Failures = append(run.Failures, eval.Failure{Type: eval.FailError, Detail: "speech: " + eval.FirstLine(err.Error())})
				return
			}
			// The answer: the first words within 25 s (a brain turn can
			// take that long), then until quiet.
			r.answering = true
			if got := r.hear(ctx, 1500*time.Millisecond, 25*time.Second, true); got > 0 {
				silent = 0
				r.hear(ctx, 1500*time.Millisecond, 20*time.Second, false)
			}
			r.answering = false
			r.w.AfterTurn(run.Turns)
			if rc := sc.Behavior.Reconnect; rc != nil && rc.AfterTurn == run.Turns {
				r.reconnect(ctx, rc)
			}
		default:
			run.EndReason, run.EndDetail = eval.EndError, "unknown turn kind "+t.Kind
			run.Failures = append(run.Failures, eval.Failure{Type: eval.FailError, Detail: run.EndDetail})
			return
		}
	}
}

func (r *runner) think() {
	lo, hi := 500, 2000
	if t := r.sc.Behavior.ThinkMS; len(t) == 2 {
		lo, hi = t[0], t[1]
	}
	time.Sleep(time.Duration(lo+r.rng.Intn(hi-lo+1)) * time.Millisecond)
}

// say renders text and speaks it (async: the bot may still be talking;
// barge-in and collisions call it from inside hear).
func (r *runner) say(ctx context.Context, text string, barge, collision bool) error {
	lang := r.sc.Language
	if hasHanText(text) {
		lang = "zh"
	} else if lang == "zh" && !hasHanText(text) {
		lang = "en"
	}
	style := r.sc.Audio.Style
	if style == "mixed" {
		style = speech.Styles[r.rng.Intn(len(speech.Styles))]
	}
	spoken := text
	if r.sc.Audio.Disfluency > 0 && (r.run.Turns != 0 || text != r.sc.Opening) {
		spoken = speech.Disfluent(text, lang, r.sc.Audio.Disfluency, r.rng)
	}
	req := speech.Request{Text: spoken, Lang: lang, Voice: r.voice, Style: style}
	if r.run.Turns == 0 && text == r.sc.Opening {
		req = OpeningRequest(r.sc) // the pinned clip (testdata/bench/openings.json)
		style = req.Style
	}
	clip, err := r.o.Synth.Synthesize(ctx, req)
	if err != nil {
		return err
	}
	r.meter.TTS(clip)
	samples := speech.Trim(clip.Samples, 60, 150)
	r.run.Turns++
	at := r.since()
	src := clip.Source
	if clip.Model != "" {
		src += "/" + clip.Model
	}
	r.lines = append(r.lines, UserLine{AtMS: at, Said: spoken, Voice: clip.Voice, Style: style, Source: src, DurMS: pcm.DurationMS(len(samples), speech.Rate), BargeIn: barge, Collision: collision})
	r.convo = append(r.convo, eval.SimLine{AtMS: at, Who: "ceo", Text: spoken})
	done, err := r.c.SayAsync(spoken, samples)
	if err != nil {
		return err
	}
	if barge || collision {
		return nil
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// hear collects the bot's finished utterances into the conversation
// until it has been quiet for quiet (with at least one utterance when
// needAny), or max passes; meanwhile it plays the CEO's barge-in and
// collision habits. It returns how many utterances it heard.
func (r *runner) hear(ctx context.Context, quiet, max time.Duration, needAny bool) int {
	end := time.Now().Add(max)
	hard := end.Add(30 * time.Second) // never walk away mid-sentence, within reason
	n := 0
	lastHeard := time.Now()
	barge := r.armBarge()
	for ctx.Err() == nil {
		now := time.Now()
		if now.After(hard) || (now.After(end) && !r.c.BotActive() && !r.c.UserSpeaking()) {
			break
		}
		if u, ok := r.c.NextUtterance(ctx, 100*time.Millisecond); ok {
			n++
			lastHeard = time.Now()
			r.addBot(u)
		}
		if barge != nil && r.c.PlayingFor() >= barge.after {
			r.bargeIn(ctx, barge)
			barge = nil
		}
		r.maybeCollide(ctx)
		if (n > 0 || !needAny) && !r.c.BotActive() && !r.c.UserSpeaking() && r.c.QuietFor() >= quiet && time.Since(lastHeard) >= quiet {
			return n
		}
	}
	return n
}

type bargePlan struct {
	after time.Duration
	b     *BargeIn
}

func (r *runner) armBarge() *bargePlan {
	b := r.sc.Behavior.BargeIn
	if b == nil || !r.answering || r.bargeN >= max(b.Max, 1) || r.rng.Float64() >= b.Prob {
		return nil
	}
	lo, hi := 1500, 4000
	if len(b.AfterMS) == 2 {
		lo, hi = b.AfterMS[0], b.AfterMS[1]
	}
	return &bargePlan{after: time.Duration(lo+r.rng.Intn(hi-lo+1)) * time.Millisecond, b: b}
}

// bargeIn talks over the answer in progress.
func (r *runner) bargeIn(ctx context.Context, p *bargePlan) {
	r.bargeN++
	text := p.b.Line
	if text == "" {
		partial := append(append([]eval.SimLine(nil), r.convo...), eval.SimLine{AtMS: r.since(), Who: "sheepdog", Text: r.c.CurrentText() + " … (still talking)"})
		t, err := r.user.Next(ctx, r.sc.SimScenario, partial)
		if err != nil || t.Kind != eval.TurnSay {
			return
		}
		text = t.Text
	}
	_ = r.say(ctx, text, true, false)
}

func (r *runner) maybeCollide(ctx context.Context) {
	col := r.sc.Behavior.Collision
	if col == nil || r.collid {
		return
	}
	r.fireMu.Lock()
	at := r.fireAt
	r.fireMu.Unlock()
	if at.IsZero() || time.Since(at) < time.Duration(col.OffsetMS)*time.Millisecond {
		return
	}
	r.collid = true
	_ = r.say(ctx, col.Line, false, true)
}

// addBot appends a bot utterance as the CEO heard it.
func (r *runner) addBot(u voiceclient.BotUtterance) {
	text := strings.TrimSpace(u.Heard)
	if text == "" {
		return
	}
	if (u.End == voiceclient.EndFlushed || u.End == voiceclient.EndLocalFlush) && u.PlayedMS < u.AudioMS {
		text += " — (cut off)"
	}
	who := "sheepdog"
	if u.Push {
		who = "push"
	}
	r.convo = append(r.convo, eval.SimLine{AtMS: u.StartMS, Who: who, Text: text})
}

// reconnect: the phone goes away (screen locked, tunnel) and comes back.
func (r *runner) reconnect(ctx context.Context, rc *Reconnect) {
	r.c.Detach()
	r.convo = append(r.convo, eval.SimLine{AtMS: r.since(), Who: "system", Text: fmt.Sprintf("The CEO's phone lost the connection for %d seconds.", rc.AwayMS/1000)})
	select {
	case <-time.After(time.Duration(rc.AwayMS) * time.Millisecond):
	case <-ctx.Done():
		return
	}
	if err := r.c.Reattach(ctx); err != nil {
		r.run.Failures = append(r.run.Failures, eval.Failure{Type: eval.FailError, Detail: "reattach: " + err.Error()})
		return
	}
	_ = r.c.WaitConnected(ctx, 30*time.Second)
	r.convo = append(r.convo, eval.SimLine{AtMS: r.since(), Who: "system", Text: "The phone is connected again."})
	r.hear(ctx, 2500*time.Millisecond, 20*time.Second, false)
}

// collect fills the run's voice-side record from the recording.
func (r *runner) collect(rec voiceclient.Recording, proxy *FaultProxy) {
	run := r.run
	r.meter.Live(rec.Events)
	r.meter.Audio(AudioMS(rec))
	run.Bot = rec.Bot
	// What the voice model heard of each CEO line: the input transcript
	// between it and the next line.
	for i := range r.lines {
		from := r.lines[i].AtMS
		to := int64(1 << 62)
		if i+1 < len(r.lines) {
			to = r.lines[i+1].AtMS
		}
		var b strings.Builder
		for _, f := range rec.In {
			if f.AtMS >= from && f.AtMS < to {
				b.WriteString(f.Text)
			}
		}
		r.lines[i].Heard = strings.Join(strings.Fields(b.String()), " ")
		r.lines[i].WER = ErrorRate(r.lines[i].Said, r.lines[i].Heard)
	}
	run.User = r.lines
	v := &run.Voice
	v.BotUtterances = len(rec.Bot)
	for _, u := range rec.Bot {
		if u.Push {
			v.Pushes++
		}
		if u.End == voiceclient.EndLocalFlush {
			v.LocalFlushes++
		}
	}
	var werSum float64
	for _, l := range r.lines {
		werSum += l.WER
		if l.Heard == "" {
			v.NotHeard++
		}
	}
	if len(r.lines) > 0 {
		v.MeanWER = werSum / float64(len(r.lines))
	}
	for _, ev := range rec.Events {
		switch ev.Type {
		case "web.client.flush":
			v.Flushes++
		case "voice.push_unheard":
			v.Unheard++
		case "voice.push_requeued":
			v.Requeued++
		case "voice.push_deferred":
			v.Deferred++
		case "voice.push_held":
			v.Held++
		case "voice.interrupted":
			v.Interrupted++
		case "voice.stalled":
			v.Stalled++
		case "voice.reconnect", "voice.goaway":
			v.Reconnects++
		case "voice.tool_call":
			v.ToolCalls++
		case "turn.latency":
			run.Latency = append(run.Latency, ev.Payload)
		case "brain.failover":
			run.Failovers = append(run.Failovers, fmt.Sprintf("%v to %v (%v)", ev.Payload["from"], ev.Payload["to"], ev.Payload["reason"]))
		case "brain.fallback":
			e, _ := ev.Payload["error"].(string)
			reason, _ := ev.Payload["reason"].(string)
			run.Fallbacks = append(run.Fallbacks, strings.TrimSpace(reason+": "+eval.FirstLine(e)))
		}
	}
	if proxy != nil {
		req, failed := proxy.Stats()
		run.Proxy = &ProxyStats{Requests: req, Failed: failed}
	}
	run.Excerpt = excerpt(rec)
	r.eventFails = voiceEventChecks(r.sc, rec.Events)
	r.writeEvents(rec)
}

// failures labels a finished run with the shared taxonomy.
func (r *runner) failures() []eval.Failure {
	run, sc := r.run, r.sc
	var fs []eval.Failure
	for _, fb := range run.Fallbacks {
		switch {
		case eval.IsRateLimited(fb):
			fs = append(fs, eval.Failure{Type: eval.FailError, Detail: "brain rate limited: " + fb})
		case eval.IsTimeoutText(fb) || strings.HasPrefix(fb, "timeout"):
			fs = append(fs, eval.Failure{Type: eval.FailTimeout, Detail: "brain turn timed out: " + fb})
		default:
			fs = append(fs, eval.Failure{Type: eval.FailBrainFallback, Detail: fb})
		}
	}
	fs = append(fs, eval.ActionFailures(sc.Expect, run.Actions)...)
	for _, v := range run.Verdicts {
		switch {
		case v.Err != "":
			fs = append(fs, eval.Failure{Type: eval.FailError, Detail: "judge " + v.Name + ": " + v.Err})
		case !v.Passed():
			fs = append(fs, eval.Failure{Type: eval.FailJudgeNo, Detail: v.Name + ": " + orStr(eval.FirstLine(v.Rationale), "no rationale")})
		}
	}
	fs = append(fs, voiceChecks(sc, *run)...)
	fs = append(fs, r.eventFails...)
	if len(fs) > 0 {
		switch run.EndReason {
		case eval.EndMaxTime:
			fs = append(fs, eval.Failure{Type: eval.FailTimeout, Detail: "the conversation hit its time cap"})
		case eval.EndSilence:
			fs = append(fs, eval.Failure{Type: eval.FailSilence, Detail: "the CEO waited in silence past the cap"})
		case eval.EndMaxTurns:
			fs = append(fs, eval.Failure{Type: eval.FailMaxTurns, Detail: "every turn used without the call ending"})
		}
	}
	return fs
}

// voiceChecks are the voice-layer checks: a CEO line the voice model did
// not hear at all, the opening's expected words, and the scenario's
// event expectations.
func voiceChecks(sc Scenario, run Run) []eval.Failure {
	var fs []eval.Failure
	for _, l := range run.User {
		if l.Heard == "" && !l.BargeIn {
			fs = append(fs, eval.Failure{Type: eval.FailCheck, Detail: fmt.Sprintf("voice: the voice model heard nothing of %q", l.Said)})
		}
	}
	if alts := sc.Voice.OpeningHeard; len(alts) > 0 && len(run.User) > 0 {
		ok := false
		for _, a := range alts {
			if fuzzyContains(run.User[0].Heard, a) {
				ok = true
				break
			}
		}
		if !ok {
			fs = append(fs, eval.Failure{Type: eval.FailCheck, Detail: fmt.Sprintf("voice: the opening was heard as %q (want one of %q)", run.User[0].Heard, alts)})
		}
	}
	return fs
}

// voiceEventChecks checks the scenario's event expectations against the
// recorded events.
func voiceEventChecks(sc Scenario, evs []voiceclient.Event) []eval.Failure {
	var fs []eval.Failure
	for _, m := range sc.Voice.Events {
		if !anyEvent(evs, m) {
			fs = append(fs, eval.Failure{Type: eval.FailCheck, Detail: "voice: no " + describe(m)})
		}
	}
	for _, m := range sc.Voice.Absent {
		if anyEvent(evs, m) {
			fs = append(fs, eval.Failure{Type: eval.FailCheck, Detail: "voice: unexpected " + describe(m)})
		}
	}
	return fs
}

func describe(m EventMatch) string {
	s := m.Type
	if len(m.Payload) > 0 {
		b, _ := json.Marshal(m.Payload)
		s += " " + string(b)
	}
	if m.Note != "" {
		s += " (" + m.Note + ")"
	}
	return s
}

func anyEvent(evs []voiceclient.Event, m EventMatch) bool {
	for _, ev := range evs {
		if ev.Type != m.Type {
			continue
		}
		ok := true
		for k, want := range m.Payload {
			if fmt.Sprint(ev.Payload[k]) != fmt.Sprint(want) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// fuzzyContains: every token of want appears in got, in order.
func fuzzyContains(got, want string) bool {
	g, w := Tokens(got), Tokens(want)
	i := 0
	for _, t := range g {
		if i < len(w) && t == w[i] {
			i++
		}
	}
	return i == len(w)
}

// excerpt is a compact, readable event timeline (the report's evidence).
func excerpt(rec voiceclient.Recording) []string {
	keep := map[string]bool{
		"voice.transcript.in": true, "voice.tool_call": true, "voice.push": true, "voice.push_unheard": true, "voice.push_requeued": true,
		"voice.push_deferred": true, "voice.push_held": true, "voice.push_released": true, "voice.push_dropped": true, "voice.interrupted": true,
		"voice.stalled": true, "voice.playback": true, "herdr.send": true, "brain.failover": true, "brain.fallback": true, "brain.tool_call": true,
		"pending.created": true, "pending.confirmed": true, "supervisor.relay": true, "voice.tool_result": true, "brain.rail_blocked": true,
		"web.client.flush": true, "voice.reconnect": true,
	}
	var out []string
	for _, ev := range rec.Events {
		if !keep[ev.Type] {
			continue
		}
		b, _ := json.Marshal(trimPayload(ev.Payload))
		line := fmt.Sprintf("%7.1fs %s %s", float64(ev.AtMS)/1000, ev.Type, b)
		if len(line) > 260 {
			line = line[:257] + "…"
		}
		out = append(out, line)
	}
	return out
}

func trimPayload(p map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range p {
		switch k {
		case "screen", "context", "prompt_text", "messages":
			continue
		}
		if s, ok := v.(string); ok && len(s) > 160 {
			v = s[:157] + "…"
		}
		out[k] = v
	}
	return out
}

func (r *runner) writeEvents(rec voiceclient.Recording) {
	if r.o.Out == "" {
		return
	}
	dir := filepath.Join(r.o.Out, "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%s.r%d.jsonl", r.sc.ID, r.run.Repeat)))
	if err != nil {
		return
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, ev := range rec.Events {
		_ = enc.Encode(ev)
	}
	_ = f.Close()
	if r.o.SaveAudio != "" {
		_ = os.MkdirAll(r.o.SaveAudio, 0o755)
		base := filepath.Join(r.o.SaveAudio, fmt.Sprintf("%s.r%d", r.sc.ID, r.run.Repeat))
		_ = speech.WriteWAV(base+".mic.wav", rec.MicAudio, voiceclient.InRate)
		_ = speech.WriteWAV(base+".bot.wav", rec.BotAudio, voiceclient.OutRate)
	}
}

func (r *runner) save() {
	if r.o.Out == "" {
		return
	}
	dir := filepath.Join(r.o.Out, "runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	b, _ := json.MarshalIndent(r.run, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s.r%d.json", r.sc.ID, r.run.Repeat)), b, 0o644)
}

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
