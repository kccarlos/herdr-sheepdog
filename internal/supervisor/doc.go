// Package supervisor is the daemon's brain: pure Go logic plus the herdr,
// llm, cli, and voice adapters. It maintains the state table from herdr
// events, implements the five voice tools (briefing, ask, propose, confirm/
// cancel, note), manages the single-slot pending action with expiry, and
// runs the push queue (one at a time, FIFO, never while the user is
// speaking) that delivers spoken updates to the voice session.
package supervisor
