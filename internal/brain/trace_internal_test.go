package brain

import "testing"

// The decorator stamps any event that carries the turn number: the
// failover and per-tool timeout events of #92 included; the compaction's
// failover, which belongs to no turn, is left alone.
func TestTraceStampsFailoverAndToolTimeout(t *testing.T) {
	rec := &recLog{}
	l := newTraceLog(rec)
	l.recent[3%traceKeep] = turnTrace{n: 3, id: "turn-x-3", utterance: "utt-1"}
	l.Log(logSource, "brain.failover", "", map[string]any{"turn": 3, "call": "turn"})
	l.Log(logSource, "brain.tool_timeout", "", map[string]any{"turn": 3, "name": "read_screen"})
	l.Log(logSource, "brain.failover", "", map[string]any{"call": "compaction"})
	for i, typ := range []string{"brain.failover", "brain.tool_timeout"} {
		p := payload(rec.events[i])
		if p["turn_id"] != "turn-x-3" || p["utterance_id"] != "utt-1" {
			t.Errorf("%s = %v", typ, p)
		}
	}
	if p := payload(rec.events[2]); p["turn_id"] != nil {
		t.Errorf("compaction failover stamped: %v", p)
	}
}
