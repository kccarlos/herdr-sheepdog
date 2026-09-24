package eventlog

import (
	"encoding/json"
	"time"
)

// Event is one logged line. ID is its stable id, written to disk:
// strictly increasing across day files and daemon restarts, and close to
// the event's time in Unix microseconds (the log takes max(now, last+1)),
// so it sorts, pages and resumes an SSE stream (Last-Event-ID). A line
// written before ids existed gets one derived the same way from its ts
// when it is read back (readEvents). Seq is a process-local monotonic counter
// (not written to disk).
type Event struct {
	ID      int64           `json:"id,omitempty"`
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
func newEvent(seq uint64, id int64, ts time.Time, source, typ, paneID string, payload any) Event {
	ev := Event{ID: id, TS: ts.UTC(), Source: source, Type: typ, PaneID: paneID, Seq: seq}
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

// nextID is the id after last for an event at ts: its time in Unix
// microseconds, or last+1 when that would not be greater.
func nextID(last int64, ts time.Time) int64 {
	return max(ts.UnixMicro(), last+1)
}
