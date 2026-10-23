package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/bench/speech"
	"github.com/kccarlos/herdr-sheepdog/internal/bench/voiceclient"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// collect fills the run's voice-side record from the recording.
func (r *runner) collect(rec voiceclient.Recording, proxy *FaultProxy) {
	run := r.run
	r.meter.Live(rec.Events)
	r.meter.Audio(AudioMS(rec))
	run.Bot = rec.Bot
	// What the voice model heard of each user line: the input transcript
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
	if r.sc.Explain != nil {
		r.exchanges = explainExchanges(r.sc.Explain, r.lines, rec)
	}
	r.eventFails = voiceEventChecks(r.sc, rec.Events)
	r.writeEvents(rec)
}

// explainExchanges splits the call at the user's lines: for each, the
// orchestrator's text (every to_supervisor result's spoken and detail,
// and the daemon's pushes) and what the voice said, until the next line.
func explainExchanges(spec *eval.ExplainSpec, lines []UserLine, rec voiceclient.Recording) []eval.ExplainExchange {
	var ex []eval.ExplainExchange
	turn := 0
	var bounds []int64
	for _, l := range lines {
		if l.BargeIn || l.Collision {
			continue // not a scripted question
		}
		turn++
		ex = append(ex, eval.ExplainExchange{Turn: turn, AtMS: l.AtMS, Kind: spec.Kind(turn), Question: l.Said})
		bounds = append(bounds, l.AtMS)
	}
	at := func(ms int64) int {
		for i := len(bounds) - 1; i >= 0; i-- {
			if ms >= bounds[i] {
				return i
			}
		}
		return -1
	}
	add := func(dst *string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = strings.TrimSpace(*dst + " " + s)
		}
	}
	// A to_supervisor result belongs to the question its call was made
	// for (a slow answer can arrive after the next question started).
	callAt := map[string]int{}
	for _, ev := range rec.Events {
		if ev.Type == "voice.tool_call" {
			if id, _ := ev.Payload["id"].(string); id != "" {
				callAt[id] = at(ev.AtMS)
			}
		}
	}
	for _, ev := range rec.Events {
		i := at(ev.AtMS)
		if ev.Type == "voice.tool_result" {
			if id, _ := ev.Payload["id"].(string); id != "" {
				if j, ok := callAt[id]; ok {
					i = j
				}
			}
		}
		if i < 0 {
			continue
		}
		switch ev.Type {
		case "voice.tool_result":
			if n, _ := ev.Payload["name"].(string); n != "" && n != "to_supervisor" {
				continue
			}
			spoken, _ := ev.Payload["spoken"].(string)
			detail, _ := ev.Payload["detail"].(string)
			var opts []string
			if os, ok := ev.Payload["options"].([]any); ok {
				for _, o := range os {
					opts = append(opts, fmt.Sprint(o))
				}
			}
			ex[i].Consulted = true
			add(&ex[i].Orchestrator, eval.JoinOrchestrator(spoken, detail, opts))
			add(&ex[i].Spoken, spoken)
		case "voice.push":
			if strings.EqualFold(fmt.Sprint(ev.Payload["focus"]), "true") || ev.Payload["urgency"] == "silent" {
				continue // context for the voice (focus, names), not a line to say
			}
			text, _ := ev.Payload["text"].(string)
			add(&ex[i].Orchestrator, text)
			add(&ex[i].Spoken, text)
		}
	}
	// The voice's speech by fragment time: one utterance can run on into
	// the next question (a stalled answer the user talked over). Only what
	// was heard counts (a flushed utterance is cut at its heard part).
	for _, u := range rec.Bot {
		if len(u.Fragments) == 0 {
			if i := at(u.StartMS); i >= 0 {
				add(&ex[i].Voice, u.Heard)
			}
			continue
		}
		left := len([]rune(u.Heard))
		parts := make([]strings.Builder, len(ex))
		for _, f := range u.Fragments {
			if left <= 0 {
				break
			}
			r := []rune(f.Text)
			if len(r) > left {
				r = r[:left]
			}
			left -= len(r)
			if i := at(f.AtMS); i >= 0 {
				parts[i].WriteString(string(r))
			}
		}
		for i := range parts {
			add(&ex[i].Voice, parts[i].String())
		}
	}
	return ex
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
