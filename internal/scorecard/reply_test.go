package scorecard

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

var t0 = time.Date(2026, 10, 3, 0, 6, 0, 0, time.UTC)

// ev builds an event at t0+ms; utt sets the top-level utterance id.
func ev(ms int, source, typ, utt string, payload map[string]any) eventlog.Event {
	raw, _ := json.Marshal(payload)
	return eventlog.Event{TS: t0.Add(time.Duration(ms) * time.Millisecond), Source: source, Type: typ, UtteranceID: utt, Payload: raw}
}

func eventsOf(evs ...eventlog.Event) []eventlog.Event { return evs }

func TestReplyLatencyByIDs(t *testing.T) {
	events := []eventlog.Event{
		// Inline: the tool result carries the answer at 1.5 s.
		ev(0, "voice", "voice.tool_call", "", map[string]any{"id": "c1", "name": "to_supervisor"}),
		ev(10, "supervisor", "supervisor.relay", "utt-1", map[string]any{"call_id": "c1"}),
		ev(1500, "voice", "voice.tool_result", "", map[string]any{"id": "c1", "spoken": "Auth is fine."}),
		// Pending: answered by a push at 10 s; an announce push in between
		// (no utterance) is not it.
		ev(20000, "voice", "voice.tool_call", "", map[string]any{"id": "c2", "name": "to_supervisor"}),
		ev(20010, "supervisor", "supervisor.relay", "utt-2", map[string]any{"call_id": "c2"}),
		ev(22000, "voice", "voice.tool_result", "", map[string]any{"id": "c2", "pending": true}),
		ev(25000, "voice", "voice.push", "", map[string]any{"text": "docs finished", "event_id": "evt-1"}),
		ev(30000, "voice", "voice.push", "utt-2", map[string]any{"text": "the answer"}),
	}
	s := computeReplyLatency(events)
	if s.N != 2 || !approxEqual(s.Mean, (1500+10000)/2.0) {
		t.Fatalf("reply latency = %+v, want 1500 and 10000 ms", s)
	}
}

// A log from before the ids: the legacy links, then order.
func TestReplyLatencyFallsBack(t *testing.T) {
	events := []eventlog.Event{
		ev(0, "voice", "voice.tool_call", "", map[string]any{"id": "c1", "name": "to_supervisor"}),
		ev(2000, "supervisor", "brain.reply_pending", "", map[string]any{"id": "utt-a", "call_id": "c1"}),
		ev(2000, "voice", "voice.tool_result", "", map[string]any{"id": "c1", "pending": true}),
		ev(6000, "voice", "voice.push", "", map[string]any{"answers_utterance": "utt-a"}),
		// No reply_pending link at all: matched by order.
		ev(10000, "voice", "voice.tool_call", "", map[string]any{"id": "c2", "name": "to_supervisor"}),
		ev(12000, "voice", "voice.tool_result", "", map[string]any{"id": "c2", "pending": true}),
		ev(13000, "voice", "voice.push", "", map[string]any{"text": "an announce"}),
		ev(18000, "voice", "voice.push", "", map[string]any{"answers_utterance": "utt-b"}),
	}
	s := computeReplyLatency(events)
	if s.N != 2 || !approxEqual(s.Mean, (6000+8000)/2.0) {
		t.Fatalf("reply latency = %+v, want 6000 and 8000 ms", s)
	}
}
