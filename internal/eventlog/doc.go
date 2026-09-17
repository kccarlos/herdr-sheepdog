// Package eventlog implements the append-only JSONL event log that every
// component of the daemon writes to (one file per day under the daemon's
// data dir), plus an SSE broadcaster so the /debug page can tail it live.
// The event log is the single source of truth for what happened; the debug
// page is only a viewer, and future tests will replay recorded logs from
// testdata/.
package eventlog
