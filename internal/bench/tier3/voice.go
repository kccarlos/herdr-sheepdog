package tier3

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// VoiceOptions turn tier 3's simulated user from a typist into a caller:
// the lines are rendered to speech and played into the daemon's /ws
// through the tier-2 phone emulator (internal/bench/voiceclient), and the
// user hears the bot through the voice model's output transcription. The
// scratch daemon then runs with the real voice model (Gemini Live on
// Vertex) instead of --voice=off.
type VoiceOptions struct {
	// Synth renders the user's lines.
	Synth speech.Synth
	// LiveModel prices the Live session (default gemini-3.8-live).
	LiveModel string
	// Dial connects the phone (default voiceclient.Dial; tests fake it).
	Dial func(ctx context.Context, o voiceclient.Options) (*voiceclient.Client, error)
}

// VoiceRun is what only a voice call has: what the user said and what the
// voice model made of it, what the bot said, the audio-side counters.
type VoiceRun struct {
	TTS          string                     `json:"tts"`
	User         []bench.UserLine           `json:"user_lines"`
	Bot          []voiceclient.BotUtterance `json:"bot_utterances"`
	Pushes       int                        `json:"pushes"`
	Flushes      int                        `json:"flushes"`
	LocalFlushes int                        `json:"local_flushes"`
	MeanWER      float64                    `json:"mean_wer"`
	NotHeard     int                        `json:"lines_not_heard"`
	Spend        bench.Spend                `json:"spend"`
}

// call is the user's side of the line: the text router or the phone. Run
// drives either through it.
type call interface {
	// arrive lets the announcements of what the daemon found at start
	// come in, as on a phone that connects to a workers already running.
	arrive(ctx context.Context) []eval.SimLine
	// say speaks (or types) a line and returns the user's own line and
	// what came back, and whether the daemon answered at all. first is
	// the opening.
	say(ctx context.Context, text string, first bool, rec *Record) (lines []eval.SimLine, answered bool, err error)
	// listen waits up to d for the daemon to speak.
	listen(ctx context.Context, d time.Duration, deadline time.Time) []eval.SimLine
	// tail collects the daemon's last words.
	tail(ctx context.Context) []eval.SimLine
	// finish ends the call and fills the record's call-side fields; it
	// returns the call's own failures.
	finish(rec *Record) []eval.Failure
}

// textCall is the text router (POST /api/inject/utterance): the daemon's
// pushes arrive as voice.push events.
type textCall struct {
	w      *world
	settle time.Duration
	// deadline is the call's end.
	deadline time.Time
	logf     func(string, ...any)
}

func (t *textCall) arrive(context.Context) []eval.SimLine {
	return t.w.waitPushes(8*time.Second, t.settle, time.Now().Add(20*time.Second))
}

func (t *textCall) say(ctx context.Context, text string, _ bool, rec *Record) ([]eval.SimLine, bool, error) {
	w := t.w
	lines := []eval.SimLine{{AtMS: w.since(), Who: "ceo", Text: text}}
	res, serr := w.d.Say(ctx, text)
	reply := strings.TrimSpace(res.Spoken)
	if len(res.Options) > 0 {
		reply = strings.TrimSpace(reply + " Options: " + strings.Join(res.Options, "; ") + ".")
	}
	if serr != nil {
		rec.Fallbacks = append(rec.Fallbacks, "utterance: "+firstLine(serr.Error()))
	}
	t.logf("sheepdog: %s", oneLine(reply, 160))
	got := append(w.drain(), eval.SimLine{AtMS: w.since(), Who: "sheepdog", Text: reply})
	got = append(got, w.waitPushes(t.settle, t.settle, t.deadline)...)
	eval.SortLines(got)
	return append(lines, got...), reply != "", nil
}

func (t *textCall) listen(_ context.Context, d time.Duration, deadline time.Time) []eval.SimLine {
	return t.w.waitPushes(d, t.settle, deadline)
}

func (t *textCall) tail(context.Context) []eval.SimLine {
	return t.w.waitPushes(t.settle, t.settle, time.Now().Add(15*time.Second))
}

func (t *textCall) finish(*Record) []eval.Failure { return nil }

// voiceCall is the phone: speech in, speech out.
type voiceCall struct {
	sc     Scenario
	o      VoiceOptions
	c      *voiceclient.Client
	rng    *rand.Rand
	meter  *bench.SpendMeter
	user   eval.SimUser
	sim    eval.SimScenario
	convo  func() []eval.SimLine // the call so far, for a barge-in line
	voice  string
	lines  []bench.UserLine
	bargeN int
	// answering: the user's barge-in habit applies only while listening
	// for the answer to a line.
	answering bool
	// off maps the phone's clock to the run's (the call's start).
	off  int64
	logf func(string, ...any)
	// turns is the lines spoken so far.
	turns int
	rec   voiceclient.Recording
	done  bool
}

// bscenario is the scenario as the tier-2 helpers know it.
func (s Scenario) bscenario() bench.Scenario {
	return bench.Scenario{SimScenario: eval.SimScenario{ID: s.ID, Language: s.Language, Opening: s.Opening}, Audio: s.Audio, Behavior: s.Behavior}
}

// dialVoice connects the phone to the daemon at baseURL and waits for the
// voice model to be up. start is the call's start (the run's clock).
func dialVoice(ctx context.Context, sc Scenario, vo VoiceOptions, baseURL string, repeat int, user eval.SimUser, meter *bench.SpendMeter, start time.Time, logf func(string, ...any)) (*voiceCall, error) {
	seed := int64(0)
	for _, r := range sc.ID {
		seed = seed*31 + int64(r)
	}
	v := &voiceCall{sc: sc, o: vo, meter: meter, user: user, sim: sc.Sim(), logf: logf,
		rng: rand.New(rand.NewSource(seed + int64(repeat)*7919 + 1)), voice: bench.VoiceFor(sc.bscenario())}
	noise := ""
	if n := sc.Audio.Noise; n != nil {
		noise = n.Kind
	}
	bed, err := bench.NewBed(noise, seed+int64(repeat))
	if err != nil {
		return nil, fmt.Errorf("noise: %w", err)
	}
	floor := sc.Audio.FloorDBFS
	if floor == 0 {
		floor = -75
	}
	bed.SetFloor(floor, seed)
	copts := voiceclient.Options{BaseURL: baseURL, Bed: bed.Next, Echo: sc.Audio.Echo, Gate: sc.Audio.Gate}
	if b := sc.Behavior.BargeIn; b != nil && b.Local {
		copts.LocalBargeIn = true
	}
	dial := vo.Dial
	if dial == nil {
		dial = voiceclient.Dial
	}
	c, err := dial(ctx, copts)
	if err != nil {
		return nil, fmt.Errorf("phone: %w", err)
	}
	v.c = c
	v.off = time.Since(start).Milliseconds() - c.SinceMS()
	if err := c.WaitConnected(ctx, 45*time.Second); err != nil {
		v.close()
		return nil, fmt.Errorf("voice: %w", err)
	}
	return v, nil
}

func (v *voiceCall) close() {
	if !v.done {
		v.rec = v.c.Close()
		v.done = true
	}
}

func (v *voiceCall) at() int64 { return v.c.SinceMS() + v.off }

func (v *voiceCall) arrive(ctx context.Context) []eval.SimLine {
	return v.hear(ctx, 2500*time.Millisecond, 25*time.Second, false)
}

func (v *voiceCall) say(ctx context.Context, text string, first bool, _ *Record) ([]eval.SimLine, bool, error) {
	if !first {
		v.think()
	}
	var lines []eval.SimLine
	if err := v.speak(ctx, text, false, &lines); err != nil {
		return lines, false, fmt.Errorf("speech: %s", firstLine(err.Error()))
	}
	// The answer: the first words within 45 s (an orchestrator turn with real
	// workers can take that long), then until quiet.
	v.answering = true
	defer func() { v.answering = false }()
	got := v.hear(ctx, 1500*time.Millisecond, 45*time.Second, true)
	answered := len(got) > 0
	if answered {
		got = append(got, v.hear(ctx, 1500*time.Millisecond, 20*time.Second, false)...)
	}
	return append(lines, got...), answered, nil
}

func (v *voiceCall) listen(ctx context.Context, d time.Duration, deadline time.Time) []eval.SimLine {
	if until := time.Until(deadline); d > until {
		d = until
	}
	if d <= 0 {
		return nil
	}
	v.answering = true
	defer func() { v.answering = false }()
	return v.hear(ctx, 2*time.Second, d, true)
}

func (v *voiceCall) tail(ctx context.Context) []eval.SimLine {
	return v.hear(ctx, 2500*time.Millisecond, 12*time.Second, false)
}

func (v *voiceCall) think() {
	lo, hi := 500, 2000
	if t := v.sc.Behavior.ThinkMS; len(t) == 2 && t[1] >= t[0] {
		lo, hi = t[0], t[1]
	}
	time.Sleep(time.Duration(lo+v.rng.Intn(hi-lo+1)) * time.Millisecond)
}

// speak renders text and plays it (async for a barge-in: the bot may
// still be talking). The user's line, as spoken, is added to lines.
func (v *voiceCall) speak(ctx context.Context, text string, barge bool, lines *[]eval.SimLine) error {
	lang := v.sc.Language
	han := strings.ContainsFunc(text, func(r rune) bool { return r >= 0x4e00 && r <= 0x9fff })
	switch {
	case han:
		lang = "zh"
	case lang == "zh":
		lang = "en"
	case lang == "":
		lang = "en"
	}
	style := v.sc.Audio.Style
	if style == "mixed" {
		style = speech.Styles[v.rng.Intn(len(speech.Styles))]
	}
	spoken := text
	if v.sc.Audio.Disfluency > 0 && (v.turns != 0 || text != v.sc.Opening) {
		spoken = speech.Disfluent(text, lang, v.sc.Audio.Disfluency, v.rng)
	}
	clip, err := v.o.Synth.Synthesize(ctx, speech.Request{Text: spoken, Lang: lang, Voice: v.voice, Style: style})
	if err != nil {
		return err
	}
	v.meter.TTS(clip)
	samples := speech.Trim(clip.Samples, 60, 150)
	v.turns++
	at := v.c.SinceMS()
	src := clip.Source
	if clip.Model != "" {
		src += "/" + clip.Model
	}
	v.lines = append(v.lines, bench.UserLine{AtMS: at, Said: spoken, Voice: clip.Voice, Style: style, Source: src, DurMS: pcm.DurationMS(len(samples), speech.Rate), BargeIn: barge})
	*lines = append(*lines, eval.SimLine{AtMS: at + v.off, Who: "ceo", Text: spoken})
	done, err := v.c.SayAsync(spoken, samples)
	if err != nil {
		return err
	}
	if barge {
		return nil
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// hear collects the bot's finished utterances until it has been quiet
// for quiet (with at least one when needAny) or max passes, playing the
// user's barge-in habit meanwhile.
func (v *voiceCall) hear(ctx context.Context, quiet, max time.Duration, needAny bool) []eval.SimLine {
	end := time.Now().Add(max)
	hard := end.Add(30 * time.Second) // never walk away mid-sentence, within reason
	var out []eval.SimLine
	lastHeard := time.Now()
	barge := v.armBarge()
	for ctx.Err() == nil {
		now := time.Now()
		if now.After(hard) || (now.After(end) && !v.c.BotActive() && !v.c.UserSpeaking()) {
			break
		}
		if u, ok := v.c.NextUtterance(ctx, 100*time.Millisecond); ok {
			lastHeard = time.Now()
			if l, ok := v.botLine(u); ok {
				out = append(out, l)
			}
		}
		if barge != nil && v.c.PlayingFor() >= barge.after {
			out = append(out, v.bargeIn(ctx, barge)...)
			barge = nil
		}
		if (len(out) > 0 || !needAny) && !v.c.BotActive() && !v.c.UserSpeaking() && v.c.QuietFor() >= quiet && time.Since(lastHeard) >= quiet {
			break
		}
	}
	return out
}

type bargePlan struct {
	after time.Duration
	b     *bench.BargeIn
}

func (v *voiceCall) armBarge() *bargePlan {
	b := v.sc.Behavior.BargeIn
	if b == nil || !v.answering || v.bargeN >= max(b.Max, 1) || v.rng.Float64() >= b.Prob {
		return nil
	}
	lo, hi := 1500, 4000
	if len(b.AfterMS) == 2 && b.AfterMS[1] >= b.AfterMS[0] {
		lo, hi = b.AfterMS[0], b.AfterMS[1]
	}
	return &bargePlan{after: time.Duration(lo+v.rng.Intn(hi-lo+1)) * time.Millisecond, b: b}
}

// bargeIn talks over the answer in progress.
func (v *voiceCall) bargeIn(ctx context.Context, p *bargePlan) []eval.SimLine {
	v.bargeN++
	text := p.b.Line
	if text == "" {
		var seen []eval.SimLine
		if v.convo != nil {
			seen = append(seen, v.convo()...)
		}
		seen = append(seen, eval.SimLine{AtMS: v.at(), Who: "sheepdog", Text: v.c.CurrentText() + " … (still talking)"})
		t, err := v.user.Next(ctx, v.sim, seen)
		if err != nil || t.Kind != eval.TurnSay {
			return nil
		}
		text = t.Text
	}
	var lines []eval.SimLine
	_ = v.speak(ctx, text, true, &lines)
	return lines
}

// botLine is a bot utterance as the user heard it.
func (v *voiceCall) botLine(u voiceclient.BotUtterance) (eval.SimLine, bool) {
	text := strings.TrimSpace(u.Heard)
	if text == "" {
		return eval.SimLine{}, false
	}
	if (u.End == voiceclient.EndFlushed || u.End == voiceclient.EndLocalFlush) && u.PlayedMS < u.AudioMS {
		text += " — (cut off)"
	}
	who := "sheepdog"
	if u.Push {
		who = "push"
	}
	v.logf("%s: %s", who, oneLine(text, 160))
	return eval.SimLine{AtMS: u.StartMS + v.off, Who: who, Text: text}, true
}

// finish hangs up and records what the voice model heard of each line.
func (v *voiceCall) finish(rec *Record) []eval.Failure {
	v.close()
	r := v.rec
	v.meter.Live(r.Events)
	v.meter.Audio(bench.AudioMS(r))
	vr := &VoiceRun{TTS: v.o.Synth.ID(), Bot: r.Bot}
	for i := range v.lines {
		from := v.lines[i].AtMS
		to := int64(1 << 62)
		if i+1 < len(v.lines) {
			to = v.lines[i+1].AtMS
		}
		var b strings.Builder
		for _, f := range r.In {
			if f.AtMS >= from && f.AtMS < to {
				b.WriteString(f.Text)
			}
		}
		v.lines[i].Heard = strings.Join(strings.Fields(b.String()), " ")
		v.lines[i].WER = bench.ErrorRate(v.lines[i].Said, v.lines[i].Heard)
	}
	vr.User = v.lines
	for _, u := range r.Bot {
		if u.Push {
			vr.Pushes++
		}
		if u.End == voiceclient.EndLocalFlush {
			vr.LocalFlushes++
		}
	}
	for _, ev := range r.Events {
		if ev.Type == "web.client.flush" {
			vr.Flushes++
		}
	}
	var fs []eval.Failure
	var sum float64
	for _, l := range v.lines {
		sum += l.WER
		if l.Heard == "" {
			vr.NotHeard++
			if !l.BargeIn {
				fs = append(fs, eval.Failure{Type: eval.FailCheck, Detail: fmt.Sprintf("voice: the voice model heard nothing of %q", l.Said)})
			}
		}
	}
	if len(v.lines) > 0 {
		vr.MeanWER = sum / float64(len(v.lines))
	}
	vr.Spend = v.meter.Total()
	rec.Voice = vr
	return fs
}
