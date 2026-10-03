package tier3

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// Run runs one scenario once against real workers. Setup or teardown
// trouble is an errored run, never a pass.
func Run(ctx context.Context, sc Scenario, o Options, repeat int) (rec Record) {
	t0 := time.Now()
	meter := eval.NewMeter()
	var spend *bench.SpendMeter
	if o.Voice != nil {
		spend = bench.NewSpendMeter(o.Voice.LiveModel)
		meter = spend.Meter
	}
	rec.SimRun = eval.SimRun{Scenario: sc.ID, Repeat: repeat}
	rec.Session = fmt.Sprintf("sdbench-%s", randHex(3))
	logf := func(format string, a ...any) {
		if o.Log != nil {
			_, _ = fmt.Fprintf(o.Log, "  [%s r%d] %s\n", sc.ID, repeat, fmt.Sprintf(format, a...))
		}
	}
	fail := func(t eval.FailureType, msg string) {
		rec.Failures = append(rec.Failures, eval.Failure{Type: t, Detail: msg})
	}
	defer func() {
		if p := recover(); p != nil {
			fail(eval.FailError, fmt.Sprint("panic: ", p))
			rec.EndReason = eval.EndError
		}
		rec.DurationMS = time.Since(t0).Milliseconds()
		rec.Usage, rec.CostUSD = meter.Lines(), meter.Total()
		if rec.Voice != nil && spend != nil {
			rec.Voice.Spend = spend.Total()
		}
		rec.Status = eval.StatusOf(rec.Failures)
	}()

	port := 0
	if o.Ports != nil {
		select {
		case port = <-o.Ports:
			defer func() { o.Ports <- port }()
		case <-ctx.Done():
			fail(eval.FailNotRun, "cancelled")
			return rec
		}
	}
	dir := filepath.Join(o.Root, fmt.Sprintf("%s-r%d-%s", sc.ID, repeat, randHex(2)))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(eval.FailError, err.Error())
		return rec
	}
	w := &world{sc: sc, o: o, dir: dir, port: port, logf: logf, ws: map[string]string{}, byName: map[string]*worker{},
		aliasOf: map[string]string{}, pushes: make(chan eval.SimLine, 128), stop: make(chan struct{}), polled: make(chan struct{})}
	defer func() {
		for _, err := range w.teardown() {
			logf("teardown: %v", err)
			rec.Checks = append(rec.Checks, "teardown: "+err.Error())
		}
	}()

	logf("setup (herdr session %s, port %d)", rec.Session, port)
	if err := w.setup(ctx, &rec); err != nil {
		rec.EndReason, rec.EndDetail = eval.EndError, err.Error()
		fail(eval.FailError, "setup: "+firstLine(err.Error()))
		logf("setup failed: %v", err)
		return rec
	}
	rec.SetupMS = time.Since(t0).Milliseconds()
	for _, l := range rec.Isolation {
		logf("isolation: %s", l)
	}

	var user eval.SimUser = eval.ScriptedSimUser{}
	if o.NewSimUser != nil && !sc.Fixed {
		user = o.NewSimUser(meter)
	}
	var judge eval.Judge = &eval.FakeJudge{}
	if o.NewJudge != nil {
		judge = o.NewJudge(meter)
	}
	// From here on the workers go by the daemon's aliases.
	sim := w.sim
	caps := sc.caps()
	settle := o.Settle
	if settle == 0 {
		settle = 4 * time.Second
	}

	// The call starts now: the clock, the event poller, the screens.
	w.start = time.Now()
	initial := w.screens(ctx, 60)
	go w.poll(ctx)
	deadline := w.start.Add(time.Duration(caps.MaxSeconds) * time.Second)
	var cl call = &textCall{w: w, settle: settle, deadline: deadline, logf: logf}
	if o.Voice != nil {
		vc, err := dialVoice(ctx, sc, *o.Voice, w.d.BaseURL(), repeat, user, spend, w.start, logf)
		if err != nil {
			close(w.stop)
			<-w.polled
			rec.EndReason, rec.EndDetail = eval.EndError, err.Error()
			fail(eval.FailError, firstLine(err.Error()))
			logf("voice setup failed: %v", err)
			return rec
		}
		cl = vc
		rec.VoiceMode = true
		defer vc.close()
	}
	// Let the announcements of what the daemon found at start arrive, as
	// on a phone that connects to a flock already running.
	convo := cl.arrive(ctx)
	if vc, ok := cl.(*voiceCall); ok {
		vc.convo = func() []eval.SimLine { return convo }
	}

	silence := time.Duration(caps.SilenceSeconds) * time.Second
	minWait := 10 * time.Second
	silent := time.Duration(0)
	waits := 0
loop:
	for {
		if time.Now().After(deadline) {
			rec.EndReason = eval.EndMaxTime
			break
		}
		var t eval.SimTurn
		var err error
		if rec.Turns == 0 && sc.Opening != "" {
			t = eval.SimTurn{Kind: eval.TurnSay, Text: sc.Opening}
		} else {
			uctx, cancel := context.WithDeadline(ctx, deadline)
			t, err = user.Next(uctx, sim, convo)
			cancel()
			if err != nil {
				if time.Now().After(deadline) {
					rec.EndReason = eval.EndMaxTime
					break
				}
				rec.EndReason, rec.EndDetail = eval.EndError, err.Error()
				fail(eval.FailError, firstLine(err.Error()))
				break
			}
		}
		switch t.Kind {
		case eval.TurnEnd:
			rec.EndReason, rec.EndDetail = eval.EndCall, t.Reason
			met := t.GoalMet
			rec.GoalMetSelf = &met
			break loop
		case eval.TurnWait:
			waits++
			if waits > caps.MaxTurns*2 {
				rec.EndReason = eval.EndMaxTurns
				break loop
			}
			convo = append(convo, eval.SimLine{AtMS: w.since(), Who: "system", Text: eval.SimWaitMarker})
			d := time.Duration(t.WaitSeconds) * time.Second
			d = min(max(d, minWait), max(silence-silent, minWait))
			got := cl.listen(ctx, d, deadline)
			if len(got) == 0 {
				silent += d
				if silent >= silence {
					rec.EndReason = eval.EndSilence
					break loop
				}
				convo = append(convo, eval.SimLine{AtMS: w.since(), Who: "system", Text: fmt.Sprintf("%d seconds of silence so far.", int(silent.Seconds()))})
				continue
			}
			silent = 0
			convo = append(convo, got...)
		case eval.TurnSay:
			if rec.Turns >= caps.MaxTurns {
				rec.EndReason = eval.EndMaxTurns
				break loop
			}
			rec.Turns++
			logf("CEO: %s", t.Text)
			got, answered, serr := cl.say(ctx, t.Text, rec.Turns == 1 && sc.Opening != "", &rec)
			eval.SortLines(got)
			convo = append(convo, got...)
			if serr != nil {
				rec.EndReason, rec.EndDetail = eval.EndError, serr.Error()
				fail(eval.FailError, firstLine(serr.Error()))
				break loop
			}
			if answered {
				silent = 0
			}
		default:
			rec.EndReason, rec.EndDetail = eval.EndError, "unknown turn kind "+t.Kind
			fail(eval.FailError, rec.EndDetail)
			break loop
		}
	}

	// Hung up while the answer was on its way: listen once more.
	names, criteria := eval.RubricFor(sim)
	if rec.EndReason == eval.EndCall && len(criteria) > 0 && names[0] == "answered" && time.Now().Before(deadline) {
		v, jerr := judge.Judge(ctx, eval.JudgeInput{Criterion: criteria[0], Transcript: eval.RenderTranscript(convo), Facts: "(the call just ended)", Final: false})
		if jerr == nil && v.Answer == eval.AnswerWait {
			rec.Extended = true
			convo = append(convo, cl.listen(ctx, min(silence, time.Until(deadline)), deadline)...)
		}
	}
	// The daemon's last words (an announce turn in flight).
	convo = append(convo, cl.tail(ctx)...)
	voiceFails := cl.finish(&rec)
	close(w.stop)
	<-w.polled
	eval.SortLines(convo)

	final := w.screens(ctx, 80)
	roster, _, _ := w.d.Agents(ctx)
	rec.Roster = roster
	rec.Screens = map[string]string{}
	for _, k := range sortedKeys(final) {
		rec.Screens[k] = tail(final[k], 30)
	}
	evs, err := AllEvents(w.d.DataDir())
	if err != nil {
		logf("events: %v", err)
	}
	if o.EventsDir != "" {
		_ = os.MkdirAll(o.EventsDir, 0o755)
		src := filepath.Join(w.d.DataDir(), "events")
		if files, _ := filepath.Glob(filepath.Join(src, "*.jsonl")); len(files) > 0 {
			var all []byte
			for _, f := range files {
				b, _ := os.ReadFile(f)
				all = append(all, b...)
			}
			_ = os.WriteFile(filepath.Join(o.EventsDir, fmt.Sprintf("%s.r%d.jsonl", sc.ID, repeat)), all, 0o644)
		}
	}
	w.mu.Lock()
	rec.Transcript = convo
	rec.Actions = append([]eval.SimLine(nil), w.actions...)
	rec.Timeline = append([]eval.SimLine(nil), w.timeline...)
	aliasOf := map[string]string{}
	for k, v := range w.aliasOf {
		aliasOf[k] = v
	}
	w.mu.Unlock()
	rec.Excerpt = excerpt(evs, w.start, aliasOf)
	for _, e := range evs {
		switch e.Type {
		case "brain.tool_call":
			rec.ToolCalls = append(rec.ToolCalls, oneLine(string(e.Payload), 200))
		case "brain.failover":
			rec.FailoverEvents = append(rec.FailoverEvents, oneLine(string(e.Payload), 200))
		case "brain.fallback":
			rec.Fallbacks = append(rec.Fallbacks, "brain.fallback: "+oneLine(string(e.Payload), 200))
		}
	}
	rec.Failovers = len(rec.FailoverEvents)

	checks, cfails := w.checks(ctx, evs, final)
	cfails = append(cfails, voiceFails...)
	rec.Checks = append(rec.Checks, checks...)
	facts := w.facts(initial, final, roster)
	if rec.EndReason != eval.EndError {
		for i, v := range eval.JudgeAll(ctx, judge, criteria, eval.RenderTranscript(convo), facts, true) {
			rec.Verdicts = append(rec.Verdicts, eval.NamedVerdict{Name: names[i], Verdict: v})
		}
	}
	rec.Aliases = w.renamed
	rec.Failures = append(rec.Failures, failures(w.expect, rec, cfails)...)
	return rec
}

var rateLimited = regexp.MustCompile(`(?i)429|rate.?limit|resource.?exhausted`)

// failures labels a finished run as eval sim does: the brain's
// fallbacks, the action expectations, the code checks, the verdicts and
// — when it failed anyway — the cap that ended it.
func failures(exp eval.SimExpect, rec Record, checks []eval.Failure) []eval.Failure {
	var fs []eval.Failure
	for _, fb := range rec.Fallbacks {
		if rateLimited.MatchString(fb) {
			fs = append(fs, eval.Failure{Type: eval.FailError, Detail: "rate limited: " + fb})
			continue
		}
		fs = append(fs, eval.Failure{Type: eval.FailBrainFallback, Detail: fb})
	}
	fs = append(fs, eval.ActionFailures(exp, rec.Actions)...)
	fs = append(fs, checks...)
	for _, v := range rec.Verdicts {
		switch {
		case v.Err != "":
			fs = append(fs, eval.Failure{Type: eval.FailError, Detail: "judge " + v.Name + ": " + v.Err})
		case !v.Passed():
			fs = append(fs, eval.Failure{Type: eval.FailJudgeNo, Detail: v.Name + ": " + orDefault(firstLine(v.Rationale), "no rationale")})
		}
	}
	if len(fs) > 0 {
		switch rec.EndReason {
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

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Campaign runs every scenario repeat times, parallel at once (each run
// takes a port from o.Ports, so parallel is bounded by the pool too).
func Campaign(ctx context.Context, scs []Scenario, o Options, repeat, parallel int, done func(Record)) ([]Record, *eval.Meter) {
	type job struct {
		sc Scenario
		r  int
		i  int
	}
	var jobs []job
	for r := 1; r <= max(repeat, 1); r++ {
		for _, sc := range scs {
			jobs = append(jobs, job{sc, r, len(jobs)})
		}
	}
	out := make([]Record, len(jobs))
	meter := eval.NewMeter()
	var mu sync.Mutex
	sem := make(chan struct{}, max(parallel, 1))
	var wg sync.WaitGroup
	for _, j := range jobs {
		if ctx.Err() != nil {
			out[j.i] = Record{SimRun: eval.SimRun{Scenario: j.sc.ID, Repeat: j.r, Status: eval.RunNotRun, Failures: []eval.Failure{{Type: eval.FailNotRun, Detail: "cancelled"}}}}
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			defer func() { <-sem }()
			rec := Run(ctx, j.sc, o, j.r)
			mu.Lock()
			out[j.i] = rec
			for _, l := range rec.Usage {
				meter.Add(l.Role, l.Model, l.Usage)
			}
			mu.Unlock()
			if done != nil {
				done(rec)
			}
		}(j)
		// Stagger starts: two Claude Code launches at once race on its
		// config file.
		time.Sleep(5 * time.Second)
	}
	wg.Wait()
	return out, meter
}
