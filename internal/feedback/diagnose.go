package feedback

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Verdicts of a diagnosis.
const (
	// VerdictModelGap: the voice's audio reached the daemon later than a
	// real-time player needed it — the provider paused.
	VerdictModelGap = "model_gap"
	// VerdictDeliveryGap: the audio was on time at the daemon but reached
	// the app late (the daemon's send queue, or the network).
	VerdictDeliveryGap = "delivery_gap"
	// VerdictClientPlayback: the audio reached the app in time and its
	// player dropped it or ran dry anyway.
	VerdictClientPlayback = "client_playback"
	// VerdictReplyWait: no voice audio around the mark — the user was
	// waiting for an answer (the orchestrator or the voice model).
	VerdictReplyWait = "reply_wait"
	// VerdictNoGap: nothing in the snapshot explains a pause.
	VerdictNoGap = "no_gap_found"
)

// starveMs is how far behind a real-time player a gap must leave it to
// count as heard (the client's jitter buffer covers less).
const starveMs = 250

// assumedStartMs is when a client is assumed to start playing a run
// with no playback report: the Gemini jitter-buffer target.
const assumedStartMs = 150

// Diagnosis says where a marked pause most likely came from.
type Diagnosis struct {
	Verdict string   `json:"verdict"`
	Summary string   `json:"summary"`
	Facts   []string `json:"facts"`
}

type drain struct {
	ts                   time.Time
	at                   string
	buffer, played, recv float64
	hasRecv              bool
	reason               string
}

// Diagnose reads a user.feedback payload's snapshot.
func Diagnose(p Payload) Diagnosis {
	s := p.Snapshot
	mark := p.ServerTS
	d := Diagnosis{Facts: []string{}}
	if s.Provider != "" {
		d.Facts = append(d.Facts, fmt.Sprintf("voice %s %s", s.Provider, s.Model))
	}
	var started []time.Time
	var drains []drain
	var outTS []time.Time
	var stats []map[string]any
	var lastIn time.Time
	for _, ev := range s.Events {
		var m map[string]any
		_ = json.Unmarshal(ev.Payload, &m)
		switch ev.Type {
		case "voice.playback":
			if m["state"] == "started" {
				started = append(started, ev.TS)
			}
		case "web.client.drain":
			dr := drain{ts: ev.TS, at: str(m["at"]), buffer: num(m["buffer_ms"]), played: num(m["played_ms"]), reason: str(m["reason"])}
			if v, ok := m["received_ms"]; ok {
				dr.recv, dr.hasRecv = num(v), true
			}
			drains = append(drains, dr)
		case "voice.transcript.out":
			outTS = append(outTS, ev.TS)
		case "voice.transcript.in":
			lastIn = ev.TS
		case "web.client.stats":
			stats = append(stats, m)
		}
	}
	if gap, at := longestPause(outTS); gap >= 1000 {
		d.Facts = append(d.Facts, fmt.Sprintf("the output transcript paused %.1f s at %s", gap/1000, clock(at)))
	}
	for _, f := range s.BrainInFlight {
		d.Facts = append(d.Facts, fmt.Sprintf("orchestrator turn %s (%s) in flight for %.1f s", f.TurnID, f.Kind, float64(f.AgeMs)/1000))
	}

	var run *Run
	for i := range s.Runs {
		if !s.Runs[i].Start.After(mark) {
			run = &s.Runs[i]
		}
	}
	if run == nil {
		if len(s.BrainInFlight) > 0 || !lastIn.IsZero() {
			d.Verdict = VerdictReplyWait
			d.Summary = "no voice audio in the window before the mark: the user was waiting for an answer"
			if !lastIn.IsZero() {
				d.Summary += fmt.Sprintf(" (last heard words %.1f s before)", mark.Sub(lastIn).Seconds())
			}
			return d
		}
		d.Verdict = VerdictNoGap
		d.Summary = "no voice audio and no user words in the window before the mark"
		return d
	}

	// When the app began playing the run: its playback "started", or the
	// jitter-buffer target after the first chunk.
	ps := float64(assumedStartMs)
	psNote := "assumed"
	end := run.Start.Add(time.Duration(runAudioMs(run)+2000) * time.Millisecond)
	for _, t := range started {
		if !t.Before(run.Start.Add(-50*time.Millisecond)) && t.Before(end) {
			ps, psNote = ms(t.Sub(run.Start)), "reported"
			break
		}
	}
	t := run.Timing
	d.Facts = append(d.Facts, fmt.Sprintf("run %d: %.0f ms of audio in %d chunks over %.0f ms (×%.2f), longest gap %.0f ms with %.0f ms banked; playback started +%.0f ms (%s)",
		run.Run, num(t["audio_ms"]), len(run.Chunks), num(t["wall_ms"]), num(t["realtime_ratio"]), num(t["max_gap_ms"]), num(t["lead_at_max_gap_ms"]), ps, psNote))
	if !lastIn.IsZero() && lastIn.Before(run.Start) {
		if w := run.Start.Sub(lastIn); w > 3*time.Second {
			d.Facts = append(d.Facts, fmt.Sprintf("the answer's first audio came %.1f s after the user's last words", w.Seconds()))
		}
	}

	// Replay the run against a real-time player that started at ps.
	var audio, worstArr, worstSent, arrAt, sentAt, arrGap float64
	for i, c := range run.Chunks {
		if i > 0 {
			if def := c[0] - ps - audio; def > worstArr {
				worstArr, arrAt, arrGap = def, c[0], c[0]-run.Chunks[i-1][0]
			}
			if c[2] >= 0 {
				if def := c[2] - ps - audio; def > worstSent {
					worstSent, sentAt = def, c[2]
				}
			}
		}
		audio += c[1]
	}
	if run.MaxSendWaitMs >= starveMs {
		d.Facts = append(d.Facts, fmt.Sprintf("a chunk waited %.0f ms in the daemon's send queue", run.MaxSendWaitMs))
	}
	if run.Unsent > 0 {
		d.Facts = append(d.Facts, fmt.Sprintf("%d chunks were never written to the app", run.Unsent))
	}

	// The app's own reports.
	var clientFinding, clientVerdict string
	for _, dr := range drains {
		if dr.ts.Before(run.Start) || dr.ts.After(end.Add(Window)) {
			continue
		}
		desc := fmt.Sprintf("the app's player ran dry at %s (%s) after %.0f ms played", clock(dr.ts), orDefault(dr.reason, "dry"), dr.played)
		if dr.hasRecv {
			desc += fmt.Sprintf(", %.0f ms received", dr.recv)
		}
		d.Facts = append(d.Facts, desc)
		sentBy := sentAudioBy(run, dr.ts)
		switch {
		case clientFinding != "":
		case dr.reason == "early" || dr.buffer >= starveMs:
			clientVerdict = VerdictClientPlayback
			clientFinding = fmt.Sprintf("the app's player dropped %.0f ms of audio it had buffered at %s", dr.buffer, clock(dr.ts))
		case dr.hasRecv && sentBy-dr.recv >= 500:
			clientVerdict = VerdictDeliveryGap
			clientFinding = fmt.Sprintf("at %s the daemon had written %.0f ms of audio and the app had received %.0f ms: %.0f ms was in flight on the network", clock(dr.ts), sentBy, dr.recv, sentBy-dr.recv)
		case dr.played+300 < runAudioMs(run):
			clientVerdict = VerdictClientPlayback
			clientFinding = fmt.Sprintf("the app's player ran dry at %s with the run on time at the daemon", clock(dr.ts))
		}
	}
	if clientFinding == "" && len(drains) == 0 && len(stats) >= 2 {
		first, last := stats[0], stats[len(stats)-1]
		if du := num(last["underruns"]) - num(first["underruns"]); du > 0 {
			clientVerdict = VerdictClientPlayback
			clientFinding = fmt.Sprintf("the app counted %.0f underrun(s) in the window (no drain reports: an older app)", du)
		}
	}

	switch {
	case worstArr >= starveMs:
		d.Verdict = VerdictModelGap
		d.Summary = fmt.Sprintf("the voice's audio stopped for %.0f ms at +%.1f s into run %d; a real-time player had been dry for %.0f ms when it resumed", arrGap, arrAt/1000, run.Run, worstArr)
	case worstSent >= starveMs:
		d.Verdict = VerdictDeliveryGap
		d.Summary = fmt.Sprintf("the audio was on time at the daemon but was written to the app %.0f ms late at +%.1f s (send queue)", worstSent, sentAt/1000)
	case clientFinding != "":
		d.Verdict = clientVerdict
		d.Summary = clientFinding
	default:
		d.Verdict = VerdictNoGap
		d.Summary = fmt.Sprintf("audio arrived ahead of a real-time player (least lead %.0f ms), was written at once, and the app reported no drain; a heard pause would be silence inside the audio itself", num(t["min_lead_ms"]))
	}
	if d.Verdict != clientVerdict && clientFinding != "" {
		d.Facts = append(d.Facts, "also: "+clientFinding)
	}
	return d
}

// sentAudioBy is the audio of run written to the app by t.
func sentAudioBy(run *Run, t time.Time) float64 {
	cut := ms(t.Sub(run.Start))
	var sum float64
	for _, c := range run.Chunks {
		if c[2] >= 0 && c[2] <= cut {
			sum += c[1]
		}
	}
	return sum
}

func runAudioMs(run *Run) float64 {
	var sum float64
	for _, c := range run.Chunks {
		sum += c[1]
	}
	return sum
}

// longestPause is the longest gap between consecutive times (ms) and
// when it began.
func longestPause(ts []time.Time) (float64, time.Time) {
	var best float64
	var at time.Time
	for i := 1; i < len(ts); i++ {
		if g := ms(ts[i].Sub(ts[i-1])); g > best {
			best, at = g, ts[i-1]
		}
	}
	return best, at
}

func clock(t time.Time) string { return t.UTC().Format("15:04:05.000") }

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
