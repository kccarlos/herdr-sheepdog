package scorecard

import (
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/observe"
)

// TurnLatencyReport is the per-utterance latency breakdown
// (turn.latency, internal/observe/latency.go): each segment's
// distribution over the CEO utterances of the slice, and how they ended.
type TurnLatencyReport struct {
	// Source is "events" when the log carries turn.latency lines (the
	// daemon emitted them), "replay" when they were rebuilt here from
	// the raw events (an older log: ids absent, matched by call id and
	// time).
	Source   string         `json:"source"`
	N        int            `json:"n"`
	Outcomes map[string]int `json:"outcomes"`
	// Segments, keyed by the turn.latency field (total_ms, voice_ms,
	// relay_ms, queue_ms, brain_ms, step_ms — every brain step pooled —,
	// model_ms, tool_ms, delivery_ms, push_queue_ms, first_audio_ms).
	Segments map[string]Stats `json:"segments"`
}

// TurnLatencySegments lists the segments in timeline order, as the
// report prints them.
var TurnLatencySegments = []string{"total_ms", "voice_ms", "relay_ms", "queue_ms", "brain_ms", "step_ms", "model_ms", "tool_ms", "delivery_ms", "push_queue_ms", "first_audio_ms"}

// TurnLatencies returns the slice's breakdowns: its turn.latency events
// when it has any, else a replay of its raw events.
func TurnLatencies(events []eventlog.Event) (bs []observe.Breakdown, source string) {
	for _, ev := range events {
		if ev.Type == "turn.latency" {
			bs = append(bs, decode[observe.Breakdown](ev.Payload))
		}
	}
	if len(bs) > 0 {
		return bs, "events"
	}
	return observe.Replay(events), "replay"
}

func computeTurnLatency(events []eventlog.Event) TurnLatencyReport {
	bs, source := TurnLatencies(events)
	r := TurnLatencyReport{Source: source, N: len(bs), Outcomes: map[string]int{}, Segments: map[string]Stats{}}
	vals := map[string][]float64{}
	add := func(name string, v *int64) {
		if v != nil {
			vals[name] = append(vals[name], float64(*v))
		}
	}
	for _, b := range bs {
		r.Outcomes[b.Outcome]++
		add("total_ms", b.TotalMS)
		add("voice_ms", b.VoiceMS)
		add("relay_ms", b.RelayMS)
		add("queue_ms", b.QueueMS)
		add("brain_ms", b.BrainMS)
		for i := range b.StepsMS {
			add("step_ms", &b.StepsMS[i])
		}
		add("model_ms", b.ModelMS)
		add("tool_ms", b.ToolMS)
		add("delivery_ms", b.DeliveryMS)
		add("push_queue_ms", b.PushQueueMS)
		add("first_audio_ms", b.FirstAudioMS)
	}
	for name, v := range vals {
		r.Segments[name] = computeStats(v)
	}
	return r
}
