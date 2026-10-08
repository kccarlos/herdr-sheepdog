package feedback

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

var t0 = time.Date(2026, 10, 8, 0, 48, 47, 900_000_000, time.UTC)

func at(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

func ev(ms int, typ string, p map[string]any) eventlog.Event {
	b, _ := json.Marshal(p)
	return eventlog.Event{TS: at(ms), Type: typ, Payload: b}
}

// chunk builds an audio chunk of run 1, audioMs arriving at ms, sent sendMs later.
func chunk(ms, audioMs, sendMs int) Chunk {
	return Chunk{Run: 1, At: at(ms), Bytes: audioMs * 48, SentAt: at(ms + sendMs)}
}

func diagnose(t *testing.T, markMs int, chunks []Chunk, events []eventlog.Event) Diagnosis {
	t.Helper()
	p := Payload{Tag: TagPaused, ServerTS: at(markMs)}
	p.Snapshot = Build(at(markMs), "gemini", "gemini-live", chunks, events)
	// The diagnosis runs on the payload as it is read back from the log.
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back Payload
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	return Diagnose(back)
}

// The provider stopped mid-reply with nothing banked.
func TestDiagnoseModelGap(t *testing.T) {
	chunks := []Chunk{chunk(0, 1000, 1), chunk(300, 500, 1), chunk(3000, 1000, 1)}
	d := diagnose(t, 6000, chunks, []eventlog.Event{ev(200, "voice.playback", map[string]any{"state": "started"})})
	if d.Verdict != VerdictModelGap || !strings.Contains(d.Summary, "2700 ms") {
		t.Fatalf("diagnosis = %+v", d)
	}
}

// The turn of 00:48:47Z: Gemini front-loaded 7.8 s, paused 5.1 s, then
// sent the rest; the app's player lost what it had buffered at about
// +4 s. The daemon side is clean, so the pause is the app's.
func TestDiagnoseIncidentIsClientPlayback(t *testing.T) {
	var chunks []Chunk
	for i := 0; i < 26; i++ {
		chunks = append(chunks, chunk(i*60, 300, 1))
	}
	chunks = append(chunks, chunk(6600, 3700, 1))
	events := []eventlog.Event{
		ev(100, "voice.transcript.out", map[string]any{"text": "herdr sheepdog "}),
		ev(194, "voice.playback", map[string]any{"state": "started", "played_ms": 0}),
		ev(1593, "voice.transcript.out", map[string]any{"text": "确切原因。 您"}),
		ev(4050, "web.client.drain", map[string]any{"at": "2026-10-08T00:48:51.950Z", "buffer_ms": 3900, "played_ms": 3850, "received_ms": 7800, "reason": "early"}),
		ev(6770, "voice.transcript.out", map[string]any{"text": "觉得十美元"}),
	}
	d := diagnose(t, 9000, chunks, events)
	if d.Verdict != VerdictClientPlayback || !strings.Contains(d.Summary, "dropped 3900 ms") {
		t.Fatalf("diagnosis = %+v", d)
	}
	facts := strings.Join(d.Facts, "\n")
	if !strings.Contains(facts, "output transcript paused 5.2 s") || !strings.Contains(facts, "longest gap 5100 ms") {
		t.Fatalf("facts = %s", facts)
	}
}

// The daemon wrote the audio on time; the app had received much less
// when it ran dry: the network held it.
func TestDiagnoseDeliveryGapOnTheNetwork(t *testing.T) {
	chunks := []Chunk{chunk(0, 2000, 1), chunk(100, 2000, 1), chunk(200, 2000, 1)}
	events := []eventlog.Event{
		ev(150, "voice.playback", map[string]any{"state": "started"}),
		ev(2300, "web.client.drain", map[string]any{"at": "2026-10-08T00:48:50.200Z", "buffer_ms": 0, "played_ms": 2000, "received_ms": 2000, "reason": "dry"}),
	}
	d := diagnose(t, 5000, chunks, events)
	if d.Verdict != VerdictDeliveryGap || !strings.Contains(d.Summary, "in flight") {
		t.Fatalf("diagnosis = %+v", d)
	}
}

// The audio was on time from the provider but sat in the send queue.
func TestDiagnoseDeliveryGapInTheSendQueue(t *testing.T) {
	chunks := []Chunk{chunk(0, 500, 1), chunk(100, 500, 1500)}
	d := diagnose(t, 4000, chunks, nil)
	if d.Verdict != VerdictDeliveryGap || !strings.Contains(d.Summary, "send queue") {
		t.Fatalf("diagnosis = %+v", d)
	}
}

// No audio, an orchestrator turn running: the user waited for an answer.
func TestDiagnoseReplyWait(t *testing.T) {
	events := []eventlog.Event{
		ev(0, "voice.transcript.in", map[string]any{"text": "what is auth doing"}),
		{TS: at(100), Type: "brain.turn_started", TurnID: "turn-9", Payload: json.RawMessage(`{"kind":"utterance"}`)},
	}
	d := diagnose(t, 8000, nil, events)
	if d.Verdict != VerdictReplyWait || !strings.Contains(strings.Join(d.Facts, " "), "turn-9") {
		t.Fatalf("diagnosis = %+v", d)
	}
}

// Smooth audio and no drain: nothing to blame.
func TestDiagnoseNoGap(t *testing.T) {
	chunks := []Chunk{chunk(0, 1000, 1), chunk(200, 1000, 1), chunk(400, 1000, 1)}
	d := diagnose(t, 5000, chunks, []eventlog.Event{ev(3500, "voice.playback", map[string]any{"state": "drained", "played_ms": 3000})})
	if d.Verdict != VerdictNoGap {
		t.Fatalf("diagnosis = %+v", d)
	}
}

func TestAudioLogRunsAndWindow(t *testing.T) {
	a := NewAudioLog()
	s1 := a.Arrived(at(0), 480)
	a.Sent(s1, at(5))
	a.Arrived(at(100), 480)
	a.EndRun()
	a.Arrived(at(40_000), 480) // run 2
	got := a.Since(at(30_000))
	if len(got) != 1 || got[0].Run != 2 {
		t.Fatalf("since 30 s = %+v", got)
	}
	got = a.Since(at(50))
	if len(got) != 3 || got[0].Run != 1 || got[0].SentAt != at(5) || !got[1].SentAt.IsZero() {
		t.Fatalf("since 50 ms = %+v", got)
	}
	a.Arrived(at(200_000), 480) // trims everything older than 60 s
	if got := a.Since(time.Time{}); len(got) != 1 {
		t.Fatalf("after trim = %d chunks", len(got))
	}
}
