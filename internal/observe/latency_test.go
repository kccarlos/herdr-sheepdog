package observe

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

var t0 = time.Date(2026, 10, 3, 0, 6, 0, 0, time.UTC)

// e builds an event at t0+ms with ids lifted as the event log does.
func e(ms int, typ string, payload map[string]any) eventlog.Event {
	l, _ := eventlog.Open("", eventlog.Options{Now: func() time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }})
	defer func() { _ = l.Close() }()
	l.Log("test", typ, "", payload)
	return l.Tail(1)[0]
}

func feed(t *Tracker, evs ...eventlog.Event) []Breakdown {
	var out []Breakdown
	for _, ev := range evs {
		out = append(out, t.Add(ev)...)
	}
	return out
}

func ms(p *int64) int64 {
	if p == nil {
		return -1
	}
	return *p
}

// A slow reply, with ids: every segment from the shepherd's last words
// to the push's first audio.
func TestBreakdownPushPathWithIDs(t *testing.T) {
	tr := NewTracker()
	out := feed(tr,
		e(0, "voice.transcript.in", map[string]any{"text": "what did codex change?"}),
		e(600, "voice.tool_call", map[string]any{"id": "c1", "name": "to_supervisor"}),
		e(610, "supervisor.relay", map[string]any{"call_id": "c1", "utterance_id": "utt-1", "waited_ms": 700}),
		e(620, "brain.turn_started", map[string]any{"turn": 4, "turn_id": "turn-r-4", "utterance_id": "utt-1", "queue_ms": 5}),
		e(2620, "voice.tool_result", map[string]any{"id": "c1", "pending": true}),
		e(3620, "brain.step", map[string]any{"turn": 4, "turn_id": "turn-r-4", "utterance_id": "utt-1", "step": 1, "dur_ms": 3000}),
		e(3630, "brain.tool_result", map[string]any{"turn": 4, "turn_id": "turn-r-4", "dur_ms": 10}),
		e(5630, "brain.step", map[string]any{"turn": 4, "turn_id": "turn-r-4", "step": 2, "dur_ms": 2000}),
		e(5640, "brain.turn", map[string]any{"turn": 4, "turn_id": "turn-r-4", "dur_ms": 5025}),
		e(5650, "brain.reply_delivered", map[string]any{"id": "utt-1", "utterance_id": "utt-1"}),
		e(5900, "voice.push", map[string]any{"answers_utterance": "utt-1", "utterance_id": "utt-1", "scheduling_sent": "when_idle"}),
		e(6000, "voice.playback", map[string]any{"state": "drained"}),
		e(6500, "voice.playback", map[string]any{"state": "started"}),
	)
	if len(out) != 1 {
		t.Fatalf("breakdowns = %+v", out)
	}
	b := out[0]
	if b.UtteranceID != "utt-1" || b.TurnID != "turn-r-4" || b.CallID != "c1" || b.Relay != "voice" || b.Reply != "push" || b.Outcome != OutcomePlayed {
		t.Errorf("breakdown = %+v", b)
	}
	// Last words at 610-700 = -90 ms (the input's last fragment trailed
	// nothing here; waited_ms is from it to the relay).
	want := map[string][2]int64{
		"voice": {ms(b.VoiceMS), 690}, "relay": {ms(b.RelayMS), 10}, "queue": {ms(b.QueueMS), 10},
		"brain": {ms(b.BrainMS), 5020}, "model": {ms(b.ModelMS), 5000}, "tool": {ms(b.ToolMS), 10},
		"delivery": {ms(b.DeliveryMS), 10}, "push_queue": {ms(b.PushQueueMS), 250}, "first_audio": {ms(b.FirstAudioMS), 600},
		"total": {ms(b.TotalMS), 6590},
	}
	for name, v := range want {
		if v[0] != v[1] {
			t.Errorf("%s_ms = %d, want %d", name, v[0], v[1])
		}
	}
	if len(b.StepsMS) != 2 || b.StepsMS[0] != 3000 || b.StepsMS[1] != 2000 {
		t.Errorf("steps = %v", b.StepsMS)
	}
	if len(tr.open)+len(tr.byCall)+len(tr.byTurn) != 0 {
		t.Errorf("tracker kept state: %d %d %d", len(tr.open), len(tr.byCall), len(tr.byTurn))
	}
}

// An inline reply: the tool result carries it; no push segment.
func TestBreakdownInline(t *testing.T) {
	out := feed(NewTracker(),
		e(0, "voice.tool_call", map[string]any{"id": "c1", "name": "to_supervisor"}),
		e(5, "supervisor.relay", map[string]any{"call_id": "c1", "utterance_id": "utt-1", "waited_ms": 400}),
		e(10, "brain.turn_started", map[string]any{"turn": 1, "turn_id": "t-1", "utterance_id": "utt-1"}),
		e(1010, "brain.step", map[string]any{"turn": 1, "turn_id": "t-1", "step": 1, "dur_ms": 1000}),
		e(1011, "brain.turn", map[string]any{"turn": 1, "turn_id": "t-1"}),
		e(1012, "voice.tool_result", map[string]any{"id": "c1", "spoken": "Auth is fine."}),
		e(1500, "voice.playback", map[string]any{"state": "started"}),
	)
	if len(out) != 1 || out[0].Reply != "tool_result" || out[0].PushQueueMS != nil || ms(out[0].FirstAudioMS) != 488 || ms(out[0].TotalMS) != 1895 {
		t.Fatalf("breakdowns = %+v", out)
	}
}

// The daemon's own relay; a coalesced utterance; a reply that never
// plays; an unanswered one; a restart.
func TestBreakdownOutcomes(t *testing.T) {
	tr := NewTracker()
	out := feed(tr,
		e(0, "supervisor.relay_by_daemon", map[string]any{"utterance_id": "utt-d", "waited_ms": 4000}),
		e(10, "supervisor.relay", map[string]any{"call_id": "c2", "utterance_id": "utt-2"}),
		e(20, "supervisor.relay", map[string]any{"call_id": "c3", "utterance_id": "utt-3"}),
		e(30, "supervisor.utterance_coalesced", map[string]any{"id": "utt-3", "replaces": "utt-2", "utterance_id": "utt-3"}),
		e(40, "voice.tool_result", map[string]any{"id": "c3", "spoken": "Done."}),
	)
	if len(out) != 1 || out[0].UtteranceID != "utt-2" || out[0].Outcome != OutcomeCoalesced {
		t.Fatalf("coalesced = %+v", out)
	}
	out = tr.Expire(t0.Add(40*time.Millisecond + audioWait + time.Second))
	if len(out) != 1 || out[0].UtteranceID != "utt-3" || out[0].Outcome != OutcomeNoAudio {
		t.Fatalf("no audio = %+v", out)
	}
	out = feed(tr, e(50, "daemon.start", nil))
	if len(out) != 1 || out[0].UtteranceID != "utt-d" || out[0].Relay != "daemon" || ms(out[0].RelayMS) != 4000 || out[0].Outcome != OutcomeRestart {
		t.Fatalf("restart = %+v", out)
	}
	out = feed(tr,
		e(100, "supervisor.relay", map[string]any{"call_id": "c4", "utterance_id": "utt-4"}),
		e(110, "voice.push", map[string]any{"utterance_id": "utt-4", "skipped": true}),
		e(120, "supervisor.relay", map[string]any{"call_id": "c5", "utterance_id": "utt-5"}),
		e(130, "voice.push", map[string]any{"utterance_id": "utt-5", "scheduling_sent": "silent"}),
		e(140, "supervisor.relay", map[string]any{"call_id": "c6", "utterance_id": "utt-6"}),
		e(150, "voice.push_dropped", map[string]any{"utterance_id": "utt-6", "reason": "no_listener"}),
	)
	if len(out) != 3 || out[0].Outcome != OutcomeNoVoice || out[1].Outcome != OutcomeSilent || out[2].Outcome != OutcomeDropped {
		t.Fatalf("outcomes = %+v", out)
	}
	feed(tr, e(200, "supervisor.relay", map[string]any{"call_id": "c7", "utterance_id": "utt-7"}))
	out = tr.Expire(t0.Add(200*time.Millisecond + recordTTL + time.Second))
	if len(out) != 1 || out[0].Outcome != OutcomeTimeout {
		t.Fatalf("timeout = %+v", out)
	}
}

// A log from before the ids: the call id, reply_pending's link, the turn
// number and time.
func TestBreakdownLegacyLog(t *testing.T) {
	out := Replay([]eventlog.Event{
		e(0, "voice.transcript.in", map[string]any{"text": "What exactly did Codex change?"}),
		e(650, "voice.tool_call", map[string]any{"id": "call_1", "name": "to_supervisor"}),
		e(651, "supervisor.relay", map[string]any{"call_id": "call_1"}),
		e(2652, "brain.reply_pending", map[string]any{"id": "utt-x", "call_id": "call_1"}),
		e(2653, "voice.tool_result", map[string]any{"id": "call_1", "pending": true}),
		e(3537, "brain.step", map[string]any{"turn": 47, "step": 1, "template": "utterance", "dur_ms": 2881}),
		e(3540, "brain.step", map[string]any{"turn": 48, "step": 1, "template": "announce", "dur_ms": 100}),
		e(3545, "brain.tool_result", map[string]any{"turn": 47, "dur_ms": 7}),
		e(6636, "brain.step", map[string]any{"turn": 47, "step": 2, "template": "utterance", "dur_ms": 3086}),
		e(6637, "brain.turn", map[string]any{"turn": 47, "dur_ms": 5984}),
		e(6638, "brain.reply_delivered", map[string]any{"id": "utt-x"}),
		e(6639, "voice.push", map[string]any{"answers_utterance": "utt-x", "scheduling_sent": "interrupt"}),
		e(7231, "voice.playback", map[string]any{"state": "started"}),
	})
	if len(out) != 1 {
		t.Fatalf("breakdowns = %+v", out)
	}
	b := out[0]
	if b.Outcome != OutcomePlayed || b.UtteranceID != "utt-x" || ms(b.VoiceMS) != 650 || ms(b.QueueMS) != 5 || ms(b.ModelMS) != 5967 || ms(b.ToolMS) != 7 || ms(b.FirstAudioMS) != 592 || ms(b.TotalMS) != 7231 {
		t.Errorf("legacy breakdown = %+v (voice %d queue %d model %d)", b, ms(b.VoiceMS), ms(b.QueueMS), ms(b.ModelMS))
	}
}

// The live handler logs turn.latency with the ids on top.
func TestLatencyObserverLogs(t *testing.T) {
	l, err := eventlog.Open("", eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	var log logging.Logger = l
	o := NewLatencyObserver(log)
	o.Event(e(0, "supervisor.relay", map[string]any{"call_id": "c1", "utterance_id": "utt-1"}))
	o.Event(e(5, "voice.tool_result", map[string]any{"id": "c1", "spoken": "Hi."}))
	o.Event(e(9, "voice.playback", map[string]any{"state": "started"}))
	evs := l.Tail(0)
	if len(evs) != 1 || evs[0].Type != "turn.latency" || evs[0].UtteranceID != "utt-1" {
		t.Fatalf("events = %+v", evs)
	}
	var b Breakdown
	if err := json.Unmarshal(evs[0].Payload, &b); err != nil || b.Outcome != OutcomePlayed || ms(b.FirstAudioMS) != 4 {
		t.Errorf("turn.latency = %s (%v)", evs[0].Payload, err)
	}
}
