// Package eventlog implements the append-only JSONL event log that every
// component of the daemon writes to (plan §3.3, decision D10): one file
// per UTC day under <data_dir>/events/, an in-memory ring of the most
// recent events for late subscribers, and a fan-out that the web server
// turns into an SSE stream.
//
// Log implements logging.Logger. Each Log call encodes the event once
// (payload marshalled immediately, so callers may reuse their maps),
// appends the line to a buffered file writer, stores the event in the
// ring and offers it to every subscriber without blocking: a subscriber
// whose channel is full is dropped and the drop itself is logged as
// eventlog.subscriber_dropped. Write errors are reported to stderr once
// and never panic; a payload that cannot be marshalled is replaced by an
// {"encode_error": …} payload so the line is still written.
//
// The line shape is exactly {ts, source, type, pane_id?, payload, dur_ms?}
// with dur_ms copied from payload.dur_ms when present. The event log is
// the single source of truth for what happened; the /debug page is only a
// viewer, and Replay reads a recorded file back for tests.
package eventlog
