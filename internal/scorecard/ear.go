package scorecard

import (
	"github.com/kccarlos/herdr-sheepdog/internal/earmetrics"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// EarReport is the ear-metrics half of the scorecard (docs/iteration-2.md
// "words, sentences, dashes/semicolons, technical tokens ... and
// unfamiliar terms per spoken line"): the quality of what was actually
// said, and separately of what the brain wrote but never got spoken.
type EarReport struct {
	// Spoken is earmetrics.Aggregate over every line the shepherd
	// actually heard: voice.tool_result.spoken and voice.push.text with
	// urgency != "silent".
	Spoken earmetrics.Summary
	// DroppedBrainLines is earmetrics.Aggregate over brain.push_late
	// lines with added:false (docs/architecture.md: "a brain line that
	// arrived after the code sentence was spoken and was dropped") whose
	// spoken text is non-empty — the words the shepherd never heard.
	DroppedBrainLines earmetrics.Summary
}

type pushPayload struct {
	Text    string `json:"text"`
	Urgency string `json:"urgency"`
}

type toolResultSpokenPayload struct {
	Spoken string `json:"spoken"`
}

type pushLatePayload struct {
	Spoken    string `json:"spoken"`
	NeedsUser bool   `json:"needs_user"`
	Added     bool   `json:"added"`
}

// computeEar implements the ear-metrics section over one ordered event
// slice.
func computeEar(events []eventlog.Event) EarReport {
	var spoken, dropped []earmetrics.Metrics
	for _, ev := range events {
		switch {
		case ev.Source == "voice" && ev.Type == "voice.tool_result":
			if s := decode[toolResultSpokenPayload](ev.Payload).Spoken; s != "" {
				spoken = append(spoken, earmetrics.Measure(s))
			}
		case ev.Source == "voice" && ev.Type == "voice.push":
			p := decode[pushPayload](ev.Payload)
			if p.Text != "" && p.Urgency != "silent" {
				spoken = append(spoken, earmetrics.Measure(p.Text))
			}
		case ev.Source == "supervisor" && ev.Type == "brain.push_late":
			p := decode[pushLatePayload](ev.Payload)
			if !p.Added && p.Spoken != "" {
				dropped = append(dropped, earmetrics.Measure(p.Spoken))
			}
		}
	}
	return EarReport{
		Spoken:            earmetrics.Aggregate(spoken),
		DroppedBrainLines: earmetrics.Aggregate(dropped),
	}
}
