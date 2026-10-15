// Package gemini implements voice.Session for the Gemini Live API (plan
// §3.7, §6; field names verified in docs/dev/gemini-live-notes.md).
//
// One Session owns one WebSocket connection at a time for the lifetime of
// the daemon. Connections are ~10 minutes; the *session* outlives them
// through resumption handles, so GoAway, unexpected drops and rate limits
// all go through the same reconnect path. Concurrency follows the SDK's
// rules: exactly one goroutine calls Receive on a connection and every
// Send* is serialised by a mutex.
//
// Goroutines: run (connection lifecycle), one reader per connection, the
// push queue, the audio output pump, and one short-lived goroutine per
// tool call. Everything stops on Close or when the Start context ends.
//
// Tool calls are declared NON_BLOCKING: the model ends its turn as soon as
// it emits the call and speaks the result as a new generation after the
// function response arrives with the scheduling the ToolHandler chose.
package gemini
