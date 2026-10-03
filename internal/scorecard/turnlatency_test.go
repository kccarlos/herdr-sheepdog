package scorecard

import (
	"strings"
	"testing"
)

// The daemon's own turn.latency lines win over a replay; without them
// the raw events are replayed.
func TestTurnLatencyPrefersLoggedBreakdowns(t *testing.T) {
	raw := ev(0, "supervisor", "supervisor.relay", "utt-1", map[string]any{"call_id": "c1"})
	result := ev(1000, "voice", "voice.tool_result", "", map[string]any{"id": "c1", "spoken": "Hi."})
	play := ev(1500, "voice", "voice.playback", "", map[string]any{"state": "started"})

	r := computeTurnLatency(eventsOf(raw, result, play))
	if r.Source != "replay" || r.N != 1 || r.Outcomes["played"] != 1 || r.Segments["first_audio_ms"].Median != 500 {
		t.Fatalf("replayed = %+v", r)
	}
	logged := ev(1600, "observe", "turn.latency", "utt-1", map[string]any{"utterance_id": "utt-1", "relay": "voice", "outcome": "played", "total_ms": 4000, "steps_ms": []int{100, 200}})
	r = computeTurnLatency(eventsOf(raw, result, play, logged))
	if r.Source != "events" || r.N != 1 || r.Segments["total_ms"].Median != 4000 || r.Segments["step_ms"].N != 2 {
		t.Fatalf("logged = %+v", r)
	}
	var b strings.Builder
	writeTurnLatency(&b, r)
	if !strings.Contains(b.String(), "| total_ms | 1 | 4000 | 4000 | 4000 |") {
		t.Errorf("markdown = %s", b.String())
	}
}
