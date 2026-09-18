package eventlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Event is one logged line. Seq is a process-local monotonic counter (not
// written to disk) that subscribers use to resume without duplicates.
type Event struct {
	TS      time.Time       `json:"ts"`
	Source  string          `json:"source"`
	Type    string          `json:"type"`
	PaneID  string          `json:"pane_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	DurMS   *int64          `json:"dur_ms,omitempty"`

	Seq uint64 `json:"-"`

	encoded []byte
}

// Encoded returns the JSON line (without the trailing newline) exactly as
// it was, or would be, written to disk.
func (e Event) Encoded() []byte {
	if e.encoded != nil {
		return e.encoded
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil
	}
	return b
}

// newEvent builds an Event from a Log call: the payload is marshalled now,
// dur_ms is lifted out of it, and the whole line is encoded once.
func newEvent(seq uint64, ts time.Time, source, typ, paneID string, payload any) Event {
	ev := Event{TS: ts.UTC(), Source: source, Type: typ, PaneID: paneID, Seq: seq}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			raw, _ = json.Marshal(map[string]string{"encode_error": err.Error()})
		}
		ev.Payload = raw
		ev.DurMS = durFromPayload(raw)
	}
	ev.encoded, _ = json.Marshal(ev)
	return ev
}

// durFromPayload returns payload.dur_ms when it is a number.
func durFromPayload(raw json.RawMessage) *int64 {
	var probe struct {
		DurMS *json.Number `json:"dur_ms"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.DurMS == nil {
		return nil
	}
	n, err := probe.DurMS.Int64()
	if err != nil {
		f, ferr := probe.DurMS.Float64()
		if ferr != nil {
			return nil
		}
		n = int64(f)
	}
	return &n
}

// Replay reads a JSONL event file back. Blank lines are skipped; a
// malformed line is an error naming its number. Seq is assigned in file
// order starting at 1.
func Replay(r io.Reader) ([]Event, error) {
	var out []Event
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			return out, fmt.Errorf("eventlog: line %d: %w", n, err)
		}
		ev.Seq = uint64(len(out) + 1)
		ev.encoded = append([]byte(nil), line...)
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("eventlog: read: %w", err)
	}
	return out, nil
}
