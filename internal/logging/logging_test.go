package logging

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestJSONLLogger(t *testing.T) {
	var buf bytes.Buffer
	l := NewJSONLLogger(&buf)
	l.now = func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) }
	l.Log("voice", "voice.push", "", map[string]any{"text": "hi"})
	l.Log("herdr", "herdr.state", "w1:p1", nil)
	l.Log("x", "y", "", make(chan int)) // unencodable: swallowed

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %s", len(lines), buf.String())
	}
	var first Line
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.Source != "voice" || first.Type != "voice.push" || first.PaneID != "" || first.TS.Year() != 2026 {
		t.Errorf("line = %+v", first)
	}
	if !bytes.Contains(lines[1], []byte(`"pane_id":"w1:p1"`)) {
		t.Errorf("pane id missing: %s", lines[1])
	}
}
