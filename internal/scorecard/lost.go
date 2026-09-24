package scorecard

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// lostWindow is how long a final utterance can go unanswered before it
// counts as lost (docs/iteration-2.md "lost-utterance rate
// (voice.transcript.in final with no tool call and no spoken output
// within 5 s)").
const lostWindow = 5 * time.Second

// LostReport is docs/iteration-2.md's lost-utterance rate, plus the two
// supervisor guard counters that are closely related: an utterance the
// daemon deliberately did not act on (voice_initiative_ignored,
// echo_ignored) rather than one that fell through the cracks.
type LostReport struct {
	Total                  int // utterance boundaries considered
	Lost                   int
	Rate                   float64
	VoiceInitiativeIgnored int
	EchoIgnored            int
}

// computeLost implements the lost-utterance section. An utterance
// boundary is a voice.transcript.in with payload.final:true, or (the
// practical case: real logs essentially never set final:true) the last
// voice.transcript.in fragment seen before a voice.turn event.
func computeLost(events []eventlog.Event) LostReport {
	var r LostReport
	var boundaries []eventlog.Event

	var lastFragment *eventlog.Event
	fragmentUsed := false
	for i := range events {
		ev := events[i]
		switch {
		case ev.Source == "voice" && ev.Type == "voice.transcript.in":
			if decode[transcriptPayload](ev.Payload).Final {
				boundaries = append(boundaries, ev)
				lastFragment = nil
			} else {
				lastFragment = &events[i]
				fragmentUsed = false
			}
		case ev.Source == "voice" && ev.Type == "voice.turn":
			if lastFragment != nil && !fragmentUsed {
				boundaries = append(boundaries, *lastFragment)
				fragmentUsed = true
			}
		case ev.Source == "supervisor" && ev.Type == "supervisor.voice_initiative_ignored":
			r.VoiceInitiativeIgnored++
		case ev.Source == "supervisor" && ev.Type == "supervisor.echo_ignored":
			r.EchoIgnored++
		}
	}

	r.Total = len(boundaries)
	for _, b := range boundaries {
		if isLost(events, b) {
			r.Lost++
		}
	}
	if r.Total > 0 {
		r.Rate = float64(r.Lost) / float64(r.Total)
	}
	return r
}

// isLost reports whether boundary is followed within lostWindow by
// neither a voice.tool_call nor a voice.transcript.out.
func isLost(events []eventlog.Event, boundary eventlog.Event) bool {
	deadline := boundary.TS.Add(lostWindow)
	for _, ev := range events {
		if !ev.TS.After(boundary.TS) {
			continue
		}
		if ev.TS.After(deadline) {
			break
		}
		if ev.Source == "voice" && (ev.Type == "voice.tool_call" || ev.Type == "voice.transcript.out") {
			return false
		}
	}
	return true
}
