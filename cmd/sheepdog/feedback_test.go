package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/feedback"
)

// TestFeedbackCommand lists a logged mark with its diagnosis and words.
func TestFeedbackCommand(t *testing.T) {
	t0 := time.Date(2026, 10, 8, 0, 48, 47, 0, time.UTC)
	ms := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }
	evs := []eventlog.Event{
		{TS: ms(-2000), Type: "voice.transcript.in", Payload: json.RawMessage(`{"text":"is auth done"}`)},
		{TS: ms(10), Type: "voice.transcript.out", Payload: json.RawMessage(`{"text":"Auth finished."}`)},
	}
	chunks := []feedback.Chunk{
		{Run: 1, At: ms(0), Bytes: 48000, SentAt: ms(1)},
		{Run: 1, At: ms(3000), Bytes: 48000, SentAt: ms(3001)},
	}
	p := feedback.Payload{ID: "fb-1", Tag: "paused", Note: "that paused", ServerTS: ms(5000)}
	p.Snapshot = feedback.Build(ms(5000), "gemini", "gemini-live", chunks, evs)
	b, _ := json.Marshal(p)
	line, _ := json.Marshal(eventlog.Event{TS: ms(5000), Source: "web", Type: feedback.EventType, Payload: b})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-10-08.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"feedback", "--events-dir", dir}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"1 marks: model_gap 1", "fb-1  paused", `"that paused"`, "model_gap: the voice's audio stopped for 3000 ms", "heard: is auth done", "said:  Auth finished."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if code := run(context.Background(), []string{"feedback", "--events-dir", dir, "--format", "json"}, &out, &errb); code != 0 {
		t.Fatalf("json exit %d", code)
	}
	var marks []feedback.Mark
	if err := json.Unmarshal(out.Bytes(), &marks); err != nil || len(marks) != 1 || marks[0].Diagnosis.Verdict != feedback.VerdictModelGap {
		t.Fatalf("json = %v %s", err, out.String())
	}
}
