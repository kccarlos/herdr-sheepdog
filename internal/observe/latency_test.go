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

// A slow reply, with ids: every segment from the user's last words
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
	o.Event(e(7, "voice.playback", map[string]any{"state": "started"})) // untagged: not the reply
	o.Event(e(9, "voice.playback", map[string]any{"state": "started", "utterance_id": "utt-1"}))
	evs := l.Tail(0)
	if len(evs) != 1 || evs[0].Type != "turn.latency" || evs[0].UtteranceID != "utt-1" {
		t.Fatalf("events = %+v", evs)
	}
	var b Breakdown
	if err := json.Unmarshal(evs[0].Payload, &b); err != nil || b.Outcome != OutcomePlayed || ms(b.FirstAudioMS) != 4 {
		t.Errorf("turn.latency = %s (%v)", evs[0].Payload, err)
	}
}

// The voice tags the reply's playback start: it wins over an earlier,
// untagged start (the "On it." run), and over a reply waiting longer.
func TestBreakdownTaggedPlayback(t *testing.T) {
	out := Replay([]eventlog.Event{
		e(0, "supervisor.relay", map[string]any{"call_id": "c1", "utterance_id": "utt-1"}),
		e(10, "supervisor.relay", map[string]any{"call_id": "c2", "utterance_id": "utt-2"}),
		e(100, "voice.tool_result", map[string]any{"id": "c1", "spoken": "One."}),
		e(200, "voice.tool_result", map[string]any{"id": "c2", "spoken": "Two."}),
		e(250, "voice.playback", map[string]any{"state": "started"}),
		e(300, "voice.playback", map[string]any{"state": "started", "utterance_id": "utt-2"}),
		e(400, "voice.playback", map[string]any{"state": "started", "utterance_id": "utt-1"}),
	})
	if len(out) != 2 || out[0].UtteranceID != "utt-2" || ms(out[0].FirstAudioMS) != 100 || out[1].UtteranceID != "utt-1" || ms(out[1].FirstAudioMS) != 300 {
		t.Fatalf("breakdowns = %+v", out)
	}
}

// Voice latency decomposes cleanly into uplink_lag_ms, gate_tail_ms and
// provider_eou_ms, summing identically to voice_ms.
func TestBreakdownVoiceDecomposition(t *testing.T) {
	// User finishes speaking on client at t=-340ms (300ms client gate hold +
	// 40ms uplink lag → web.client.silence arrives at t=0). Daemon streams
	// 1000ms of comfort noise and sends AudioStreamEnd at t=1000ms. Gemini's
	// InputTranscription arrives late at t=1180ms right before to_supervisor
	// at t=1250ms (so supervisor's waited_ms is only 80ms, which used to mask
	// the entire 1590ms pre-relay wait).
	tr := NewTracker()
	out := feed(tr,
		e(0, "web.client.silence", map[string]any{"on": true, "speech_end_ms": 300, "seq": 50, "uplink_lag_ms": 40}),
		e(1000, "voice.stream_end", map[string]any{"tail_ms": 1000}),
		e(1180, "voice.transcript.in", map[string]any{"text": "is auth done?"}),
		e(1250, "voice.tool_call", map[string]any{"id": "c1", "name": "to_supervisor"}),
		e(1260, "supervisor.relay", map[string]any{"call_id": "c1", "utterance_id": "utt-1", "waited_ms": 80}),
		e(1500, "voice.tool_result", map[string]any{"id": "c1", "spoken": "Yes."}),
		e(1800, "voice.playback", map[string]any{"state": "started", "utterance_id": "utt-1"}),
	)
	if len(out) != 1 {
		t.Fatalf("breakdowns = %+v", out)
	}
	b := out[0]
	if ms(b.UplinkLagMS) != 40 || ms(b.GateTailMS) != 1300 || ms(b.ProviderEOUMS) != 250 {
		t.Fatalf("sub-segments: uplink=%d gate_tail=%d provider_eou=%d", ms(b.UplinkLagMS), ms(b.GateTailMS), ms(b.ProviderEOUMS))
	}
	if ms(b.VoiceMS) != 40+1300+250 {
		t.Fatalf("voice_ms = %d, want 1590 (sum of sub-segments)", ms(b.VoiceMS))
	}
	// True speech end was -340ms; playback started at 1800ms → total_ms = 2140ms.
	if ms(b.TotalMS) != 2140 {
		t.Fatalf("total_ms = %d, want 2140", ms(b.TotalMS))
	}

	// Mid-tail early EOU: Gemini fires to_supervisor 450ms into the 1000ms
	// comfort-noise tail (before voice.stream_end is logged).
	out = feed(tr,
		e(3000, "web.client.silence", map[string]any{"on": true, "speech_end_ms": 300, "seq": 80, "uplink_lag_ms": 20}),
		e(3450, "voice.tool_call", map[string]any{"id": "c2", "name": "to_supervisor", "gate_tail_elapsed_ms": 450}),
		e(3455, "supervisor.relay", map[string]any{"call_id": "c2", "utterance_id": "utt-2", "waited_ms": 5}),
		e(3600, "voice.tool_result", map[string]any{"id": "c2", "spoken": "Stopped."}),
		e(3700, "voice.playback", map[string]any{"state": "started", "utterance_id": "utt-2"}),
	)
	if len(out) != 1 {
		t.Fatalf("mid-tail breakdowns = %+v", out)
	}
	b2 := out[0]
	if ms(b2.UplinkLagMS) != 20 || ms(b2.GateTailMS) != 750 || ms(b2.ProviderEOUMS) != 0 || ms(b2.VoiceMS) != 770 {
		t.Fatalf("mid-tail = uplink:%d gate:%d eou:%d voice:%d", ms(b2.UplinkLagMS), ms(b2.GateTailMS), ms(b2.ProviderEOUMS), ms(b2.VoiceMS))
	}
}

// Edge cases: silent-room gate close (seq present, speech_end_ms absent),
// mute-on-connect vs mute-after-speech, legacy client without stamps, and
// typed utterance clearing stale silence.
func TestBreakdownVoiceDecompositionEdges(t *testing.T) {
	tr := NewTracker()
	// 1. Connect into a silent room: client sends mute on connect, then
	// unmutes, then gate closes with seq=15 and no speech_end_ms (0 voiced
	// frames), followed later by a ?gate=off / non-gated utterance. Neither
	// mute-on-connect nor silent-room gate-close should invent gate_tail_ms.
	out := feed(tr,
		e(10, "web.client.mute", map[string]any{"muted": true}),
		e(20, "web.client.mute", map[string]any{"muted": false}),
		e(320, "web.client.silence", map[string]any{"on": true, "seq": 15}),
		e(2000, "voice.transcript.in", map[string]any{"text": "hello"}),
		e(2400, "voice.tool_call", map[string]any{"id": "c1", "name": "to_supervisor"}),
		e(2410, "supervisor.relay", map[string]any{"call_id": "c1", "utterance_id": "utt-1", "waited_ms": 410}),
		e(2500, "voice.tool_result", map[string]any{"id": "c1", "spoken": "Hi."}),
		e(2600, "voice.playback", map[string]any{"state": "started", "utterance_id": "utt-1"}),
	)
	if len(out) != 1 || out[0].UplinkLagMS != nil || out[0].GateTailMS != nil || out[0].ProviderEOUMS != nil || ms(out[0].VoiceMS) != 400 {
		t.Fatalf("silent-room breakdown = %+v", out)
	}

	// 2. Mute-after-speech: user speaks, then taps Mute to finish.
	out = feed(tr,
		e(3000, "web.client.speaking", map[string]any{"on": true}),
		e(3800, "web.client.mute", map[string]any{"muted": true}),
		e(4800, "voice.stream_end", map[string]any{"tail_ms": 1000}),
		e(5000, "voice.tool_call", map[string]any{"id": "c2", "name": "to_supervisor"}),
		e(5010, "supervisor.relay", map[string]any{"call_id": "c2", "utterance_id": "utt-2", "waited_ms": 50}),
		e(5100, "voice.tool_result", map[string]any{"id": "c2", "spoken": "Got it."}),
		e(5200, "voice.playback", map[string]any{"state": "started", "utterance_id": "utt-2"}),
	)
	if len(out) != 1 || out[0].UplinkLagMS != nil || ms(out[0].GateTailMS) != 1000 || ms(out[0].ProviderEOUMS) != 200 || ms(out[0].VoiceMS) != 1200 {
		t.Fatalf("mute-after-speech = %+v (gate:%d eou:%d voice:%d)", out, ms(out[0].GateTailMS), ms(out[0].ProviderEOUMS), ms(out[0].VoiceMS))
	}

	// 3. Stale silence followed by typed utterance (web.utterance) clears the mark.
	out = feed(tr,
		e(6000, "web.client.silence", map[string]any{"on": true, "speech_end_ms": 300, "seq": 99, "uplink_lag_ms": 15}),
		e(7000, "web.utterance", map[string]any{"text": "status", "via": "voice"}),
		e(7200, "voice.tool_call", map[string]any{"id": "c3", "name": "to_supervisor"}),
		e(7205, "supervisor.relay", map[string]any{"call_id": "c3", "utterance_id": "utt-3", "waited_ms": 205}),
		e(7300, "voice.tool_result", map[string]any{"id": "c3", "spoken": "All idle."}),
		e(7400, "voice.playback", map[string]any{"state": "started", "utterance_id": "utt-3"}),
	)
	if len(out) != 1 || out[0].GateTailMS != nil || out[0].ProviderEOUMS != nil || out[0].UplinkLagMS != nil {
		t.Fatalf("typed utterance inherited silence mark: %+v", out)
	}
}
