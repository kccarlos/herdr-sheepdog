package scorecard

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/earmetrics"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// duplicateWindow and duplicateOverlapThreshold implement
// docs/dev/iteration-2.md's duplicate-speech check: "a voice.push text and a
// voice.tool_result.spoken within 10 s that share ≥ 60 % of words (the
// 'started on…' then 'Sent —…' pattern)".
const (
	duplicateWindow           = 10 * time.Second
	duplicateOverlapThreshold = 0.6
)

// DuplicateInstance is one push/tool_result pair judged to say the same
// thing twice.
type DuplicateInstance struct {
	PushTS       time.Time
	PushText     string
	ToolResultTS time.Time
	Spoken       string
}

// DuplicateReport is docs/dev/iteration-2.md's duplicate-speech check.
type DuplicateReport struct {
	Instances []DuplicateInstance
	Count     int
}

// computeDuplicateSpeech implements the duplicate-speech section: every
// voice.push is checked against every voice.tool_result within
// duplicateWindow of it (either direction), and a pair whose texts share
// at least duplicateOverlapThreshold of their words is reported once.
func computeDuplicateSpeech(events []eventlog.Event) DuplicateReport {
	type timed struct {
		ts   time.Time
		text string
	}
	var pushes, results []timed
	for _, ev := range events {
		switch {
		case ev.Source == "voice" && ev.Type == "voice.push":
			if t := decode[pushPayload](ev.Payload).Text; t != "" {
				pushes = append(pushes, timed{ev.TS, t})
			}
		case ev.Source == "voice" && ev.Type == "voice.tool_result":
			if s := decode[toolResultSpokenPayload](ev.Payload).Spoken; s != "" {
				results = append(results, timed{ev.TS, s})
			}
		}
	}

	var r DuplicateReport
	for _, p := range pushes {
		for _, res := range results {
			d := p.ts.Sub(res.ts)
			if d < 0 {
				d = -d
			}
			if d > duplicateWindow {
				continue
			}
			if earmetrics.WordOverlap(p.text, res.text) >= duplicateOverlapThreshold {
				r.Instances = append(r.Instances, DuplicateInstance{
					PushTS: p.ts, PushText: p.text, ToolResultTS: res.ts, Spoken: res.text,
				})
				break // one match is enough to flag this push
			}
		}
	}
	r.Count = len(r.Instances)
	return r
}
