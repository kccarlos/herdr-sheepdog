// Package herdr implements the client for the herdr unix socket protocol.
// It will subscribe to agent state events (blocked / working / done / idle),
// read pane screen content, and send text and keys to a pane, wrapping the
// same surface as `herdr agent …` / `herdr wait …`. Reliability is expected
// to come from two deduplicated event channels (the socket subscription and
// a plugin hook shim) plus a periodic snapshot diff that re-emits any
// transition both channels missed, with a per-pane debounce before a
// transition becomes a supervisor event.
package herdr
