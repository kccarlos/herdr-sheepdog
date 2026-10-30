package herdr

import "context"

// EventSource is what a Watcher reads: the fleet as a snapshot, one pane's
// status stream, the lifecycle stream used to discover panes, and a signal
// that a stream was re-established and the snapshot should be re-read.
// *Client is the old herdr's; a backend for another multiplexer
// (docs/dev/design/herdr-core-backend.md) translates its own events into
// these types so the same dedup and debounce path applies.
type EventSource interface {
	Snapshot(ctx context.Context) (Snapshot, error)
	Subscribe(ctx context.Context, paneID string) <-chan StatusEvent
	SubscribeFleet(ctx context.Context) <-chan FleetEvent
	Resync() <-chan struct{}
}

// Backend is everything the daemon needs from the multiplexer the agents
// run in: the Watcher's EventSource, a liveness and version check, and
// the reads and writes the supervisor makes (supervisor.Herdr). The
// optional capabilities (starting agents, closing panes, workspaces,
// renames, coloured reads) stay the supervisor's small interfaces, found
// by type assertion, so a backend without one is refused cleanly.
type Backend interface {
	EventSource
	Ping(ctx context.Context) (Pong, error)
	ListAgents(ctx context.Context) ([]AgentInfo, error)
	Read(ctx context.Context, paneID string, source ReadSource, lines int) (ReadResult, error)
	Prompt(ctx context.Context, paneID, text string) (AgentInfo, error)
	SendKeys(ctx context.Context, paneID string, keys ...string) error
	Interrupt(ctx context.Context, paneID string) error
}

var _ Backend = (*Client)(nil)
