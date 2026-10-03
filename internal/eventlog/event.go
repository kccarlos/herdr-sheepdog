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
//
// TurnID and UtteranceID tie the events of one brain turn and of one CEO
// utterance together (voice relay → brain turn → reply push → playback),
// so a reader groups them by id instead of guessing by time. Like DurMS
// they are lifted from the payload (payload.turn_id, payload.utterance_id,
// when strings), so an emitter sets them by adding the key to the payload
// or to a push's tags; both are absent on lines written before they
// existed, and readers fall back to time then.
type Event struct {
	ID          int64           `json:"id,omitempty"`
	TS          time.Time       `json:"ts"`
	Source      string          `json:"source"`
	Type        string          `json:"type"`
	PaneID      string          `json:"pane_id,omitempty"`
	TurnID      string          `json:"turn_id,omitempty"`
	UtteranceID string          `json:"utterance_id,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	DurMS       *int64          `json:"dur_ms,omitempty"`

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
// dur_ms, turn_id and utterance_id are lifted out of it, and the whole
// line is encoded once.
func newEvent(seq uint64, id int64, ts time.Time, source, typ, paneID string, payload any) Event {
	ev := Event{ID: id, TS: ts.UTC(), Source: source, Type: typ, PaneID: paneID, Seq: seq}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			raw, _ = json.Marshal(map[string]string{"encode_error": err.Error()})
		}
		ev.Payload = raw
		ev.DurMS, ev.TurnID, ev.UtteranceID = liftFromPayload(raw)
	}
	ev.encoded, _ = json.Marshal(ev)
	return ev
}

// liftFromPayload returns payload.dur_ms when it is a number and
// payload.turn_id and payload.utterance_id when they are strings, in one
// decode of the payload. A field of another type is ignored on its own:
// it never costs the others.
func liftFromPayload(raw json.RawMessage) (dur *int64, turnID, utteranceID string) {
	var probe struct {
		DurMS       json.RawMessage `json:"dur_ms"`
		TurnID      any             `json:"turn_id"`
		UtteranceID any             `json:"utterance_id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, "", ""
	}
	turnID, _ = probe.TurnID.(string)
	utteranceID, _ = probe.UtteranceID.(string)
	return durOf(probe.DurMS), turnID, utteranceID
}

// durOf converts a raw dur_ms to milliseconds (nil when absent or not a
// number).
func durOf(raw json.RawMessage) *int64 {
	if len(raw) == 0 {
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err != nil {
		return nil
	}
	n, err := num.Int64()
	if err != nil {
		f, ferr := num.Float64()
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
