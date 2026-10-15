// Package herdr implements the client for the herdr unix socket protocol
// (docs/dev/herdr-protocol.md) and the Watcher that turns its event streams
// into deduplicated, debounced agent state Transitions.
//
// Client covers the one-shot methods sheepdog needs (ping, agent.list,
// agent.get, session.snapshot, pane.read, agent.prompt, pane.send_keys) —
// one fresh connection per request, because the server closes after each
// reply — plus the long-lived events.subscribe streams: Subscribe for one
// pane's agent_status_changed events and SubscribeFleet for the lifecycle
// events used to discover panes. Both reconnect with backoff.
//
// Watcher composes them per plan §3.1: fleet events plus a periodic
// session.snapshot diff maintain the known pane set; every status event —
// from the socket, from the snapshot diff, or fed in through Inject by the
// future plugin-hook HTTP channel — goes through one dedup path keyed by
// (pane, state) within a short window, and `blocked` must persist for the
// debounce before a Transition is emitted.
//
// This package must not import internal/state or internal/supervisor (plan
// decision D1). Structured events are written through the small Logger
// interface so the event log can be plugged in later.
package herdr
