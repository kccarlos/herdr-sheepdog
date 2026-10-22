package bench

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/pcm"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// converse is the call: the opening, then the simulated user's turns.
func (r *runner) converse(ctx context.Context) {
	run, sc := r.run, r.sc
	caps := capsOf(sc)
	deadline := time.Now().Add(time.Duration(caps.MaxSeconds) * time.Second)
	silence := time.Duration(caps.SilenceSeconds) * time.Second
	// The phone connects to a workers already running: hear what it says
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
			t = eval.AgendaOpening(sc.SimScenario)
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
			r.convo = append(r.convo, eval.SimLine{AtMS: r.since(), Who: "system", Text: eval.SimWaitMarker, Beat: t.Beat, Decl: t.Decl, Items: t.Items})
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
			r.tagLast(t)
			// The answer: the first words within 25 s (an orchestrator turn can
			// take that long), then until quiet.
			r.answering = true
			if got := r.hear(ctx, 1500*time.Millisecond, 25*time.Second, true); got > 0 {
				silent = 0
				r.hear(ctx, 1500*time.Millisecond, 20*time.Second, false)
			}
			r.answering = false
			cue := t.CueBeats()
			if len(cue) == 0 {
				r.w.AfterTurnBeat(run.Turns, 0)
			}
			for i, b := range cue {
				n := run.Turns
				if i > 0 {
					n = -1 // the turn's own cues fire once
				}
				r.w.AfterTurnBeat(n, b)
			}
			if rc := sc.Behavior.Reconnect; rc != nil && rc.AfterTurn == run.Turns {
				r.reconnect(ctx, rc)
			}
			if sc.Behavior.Resume.At(run.Turns) || sc.Behavior.Resume.AtBeat(t.Beat) {
				r.resume(ctx)
			}
		default:
			run.EndReason, run.EndDetail = eval.EndError, "unknown turn kind "+t.Kind
			run.Failures = append(run.Failures, eval.Failure{Type: eval.FailError, Detail: run.EndDetail})
			return
		}
	}
}

// tagLast marks the user's latest line with its agenda beat, tag and
// declaration.
func (r *runner) tagLast(t eval.SimTurn) {
	for i := len(r.convo) - 1; i >= 0; i-- {
		if r.convo[i].Who == "ceo" {
			r.convo[i].Beat, r.convo[i].Tag = t.Beat, t.Tag
			r.convo[i].Decl, r.convo[i].Items = t.Decl, t.Items
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
// needAny), or max passes; meanwhile it plays the user's barge-in and
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

// addBot appends a bot utterance as the user heard it.
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
	r.convo = append(r.convo, eval.SimLine{AtMS: r.since(), Who: "system", Text: fmt.Sprintf("The user's phone lost the connection for %d seconds.", rc.AwayMS/1000)})
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

// resume forces one Gemini resume and lets it settle: the voice model's
// connection is swapped for one resumed from its latest handle.
func (r *runner) resume(ctx context.Context) {
	ok := r.d.RecycleVoice("bench_resume")
	r.convo = append(r.convo, eval.SimLine{AtMS: r.since(), Who: "system", Text: "The voice connection was recycled (a session resume)."})
	if !ok {
		r.run.Failures = append(r.run.Failures, eval.Failure{Type: eval.FailError, Detail: "resume: the voice provider cannot recycle"})
		return
	}
	select {
	case <-time.After(4 * time.Second):
	case <-ctx.Done():
	}
	_ = r.c.WaitConnected(ctx, 30*time.Second)
}
