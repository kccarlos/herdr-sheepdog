package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// StatusEvent is a `pane.agent_status_changed` event stamped on receipt
// (herdr events carry no timestamps).
type StatusEvent struct {
	PaneID      string    `json:"pane_id"`
	WorkspaceID string    `json:"workspace_id"`
	Agent       string    `json:"agent,omitempty"`
	State       State     `json:"agent_status"`
	At          time.Time `json:"at"`
	// Source names the channel that delivered the event: "socket",
	// "snapshot", "bootstrap", or whatever Inject callers pass ("http").
	Source string `json:"source,omitempty"`
}

// FleetKind is the lifecycle event kind, in herdr's snake_case spelling.
type FleetKind string

// Lifecycle events used for fleet discovery (docs/dev/herdr-protocol.md §4).
const (
	FleetAgentDetected   FleetKind = "pane_agent_detected"
	FleetPaneCreated     FleetKind = "pane_created"
	FleetPaneClosed      FleetKind = "pane_closed"
	FleetPaneExited      FleetKind = "pane_exited"
	FleetWorkspaceClosed FleetKind = "workspace_closed"
)

// dotted maps a FleetKind to the dotted subscription type name.
var dotted = map[FleetKind]string{
	FleetAgentDetected:   "pane.agent_detected",
	FleetPaneCreated:     "pane.created",
	FleetPaneClosed:      "pane.closed",
	FleetPaneExited:      "pane.exited",
	FleetWorkspaceClosed: "workspace.closed",
}

// FleetEvent is a lifecycle event stamped on receipt.
type FleetEvent struct {
	Kind        FleetKind `json:"kind"`
	PaneID      string    `json:"pane_id,omitempty"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	Agent       string    `json:"agent,omitempty"`
	// Released is set on pane_agent_detected when the agent process exited;
	// FinalStatus is its last state.
	Released    bool      `json:"released,omitempty"`
	FinalStatus State     `json:"final_status,omitempty"`
	Pane        *PaneInfo `json:"pane,omitempty"`
	At          time.Time `json:"at"`
}

// rawEvent is the wire shape shared by both envelope kinds: `event` is a
// dotted name for per-pane subscription events and a snake_case kind for
// lifecycle events.
type rawEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type rawData struct {
	Type        string    `json:"type"`
	PaneID      string    `json:"pane_id"`
	WorkspaceID string    `json:"workspace_id"`
	Agent       string    `json:"agent"`
	Status      State     `json:"agent_status"`
	Released    bool      `json:"released"`
	FinalStatus State     `json:"final_status"`
	Pane        *PaneInfo `json:"pane"`
}

// ParseEvent decodes one event line into a StatusEvent or FleetEvent.
// Exactly one of the two results is non-nil for a known event; both are nil
// for events sheepdog does not use (pane_updated, workspace_created, …).
func ParseEvent(line []byte, at time.Time) (*StatusEvent, *FleetEvent, error) {
	var raw rawEvent
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, nil, fmt.Errorf("herdr: decode event: %w", err)
	}
	var d rawData
	if len(raw.Data) > 0 {
		if err := json.Unmarshal(raw.Data, &d); err != nil {
			return nil, nil, fmt.Errorf("herdr: decode event data: %w", err)
		}
	}
	switch raw.Event {
	case "pane.agent_status_changed", "pane_agent_status_changed":
		if !d.Status.Valid() {
			return nil, nil, fmt.Errorf("herdr: status event with invalid state %q", d.Status)
		}
		return &StatusEvent{PaneID: d.PaneID, WorkspaceID: d.WorkspaceID, Agent: d.Agent, State: d.Status, At: at, Source: "socket"}, nil, nil
	case string(FleetAgentDetected), string(FleetPaneCreated), string(FleetPaneClosed), string(FleetPaneExited), string(FleetWorkspaceClosed):
		ev := &FleetEvent{Kind: FleetKind(raw.Event), PaneID: d.PaneID, WorkspaceID: d.WorkspaceID, Agent: d.Agent,
			Released: d.Released, FinalStatus: d.FinalStatus, Pane: d.Pane, At: at}
		if ev.Pane != nil && ev.PaneID == "" {
			ev.PaneID = ev.Pane.PaneID
			ev.WorkspaceID = ev.Pane.WorkspaceID
		}
		return nil, ev, nil
	}
	return nil, nil, nil
}

// subscription is one entry of events.subscribe params.subscriptions.
type subscription struct {
	Type   string `json:"type"`
	PaneID string `json:"pane_id,omitempty"`
}

// Backoff controls subscription reconnects.
type Backoff struct {
	Min time.Duration
	Max time.Duration
}

// DefaultBackoff is used when a Client's ReconnectBackoff is zero.
var DefaultBackoff = Backoff{Min: 200 * time.Millisecond, Max: 5 * time.Second}

func (b Backoff) next(cur time.Duration) time.Duration {
	if cur == 0 {
		return b.Min
	}
	cur *= 2
	if cur > b.Max {
		cur = b.Max
	}
	return cur
}

// Subscribe streams pane.agent_status_changed events for one pane until ctx
// is cancelled. The connection is re-established with backoff when it
// drops (logged as herdr.reconnect). If herdr reports the pane does not
// exist the stream ends and the channel is closed.
func (c *Client) Subscribe(ctx context.Context, paneID string) <-chan StatusEvent {
	out := make(chan StatusEvent, 16)
	subs := []subscription{{Type: "pane.agent_status_changed", PaneID: paneID}}
	go func() {
		defer close(out)
		c.subscribeLoop(ctx, "status:"+paneID, paneID, subs, func(line []byte, at time.Time) {
			ev, _, err := ParseEvent(line, at)
			if err != nil || ev == nil {
				return
			}
			select {
			case out <- *ev:
			case <-ctx.Done():
			}
		})
	}()
	return out
}

// SubscribeFleet streams the lifecycle events needed for fleet discovery
// (pane.agent_detected, pane.created, pane.closed, pane.exited,
// workspace.closed) until ctx is cancelled, reconnecting with backoff.
func (c *Client) SubscribeFleet(ctx context.Context) <-chan FleetEvent {
	out := make(chan FleetEvent, 16)
	subs := make([]subscription, 0, len(dotted))
	for _, k := range []FleetKind{FleetAgentDetected, FleetPaneCreated, FleetPaneClosed, FleetPaneExited, FleetWorkspaceClosed} {
		subs = append(subs, subscription{Type: dotted[k]})
	}
	go func() {
		defer close(out)
		c.subscribeLoop(ctx, "fleet", "", subs, func(line []byte, at time.Time) {
			_, ev, err := ParseEvent(line, at)
			if err != nil || ev == nil {
				return
			}
			select {
			case out <- *ev:
			case <-ctx.Done():
			}
		})
	}()
	return out
}

// subscribeLoop keeps one subscription connection alive until ctx ends or
// the server answers with a terminal error (pane_not_found).
func (c *Client) subscribeLoop(ctx context.Context, name, paneID string, subs []subscription, handle func([]byte, time.Time)) {
	var (
		delay    time.Duration
		attempts int
	)
	for {
		err := c.subscribeOnce(ctx, subs, handle, attempts > 0)
		if ctx.Err() != nil {
			return
		}
		var he *Error
		if errors.As(err, &he) && he.Code == "events_lost" {
			c.log("herdr.events_lost", paneID, map[string]any{"subscription": name, "message": he.Message})
		}
		if errors.Is(err, ErrPaneNotFound) {
			c.log("herdr.subscribe_end", paneID, map[string]any{"subscription": name, "error": err.Error()})
			return
		}
		attempts++
		delay = c.backoff().next(delay)
		c.log("herdr.reconnect", paneID, map[string]any{
			"subscription": name,
			"attempt":      attempts,
			"delay_ms":     delay.Milliseconds(),
			"error":        errString(err),
		})
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) backoff() Backoff {
	if c.ReconnectBackoff.Min > 0 {
		return c.ReconnectBackoff
	}
	return DefaultBackoff
}

// subscribeOnce opens one connection, sends events.subscribe, waits for the
// ack and then hands every event line to handle. It returns nil only when
// ctx is cancelled; any other return is a dropped connection or an error.
func (c *Client) subscribeOnce(ctx context.Context, subs []subscription, handle func([]byte, time.Time), resubscribe bool) error {
	dialCtx, cancel := context.WithTimeout(ctx, c.timeout())
	conn, err := dial(dialCtx, c.Socket)
	cancel()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Unblock the reader when ctx ends.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	id := c.nextID("events.subscribe")
	params := map[string]any{"subscriptions": subs}
	_ = conn.SetWriteDeadline(time.Now().Add(c.timeout()))
	if err := writeJSONLine(conn, request{ID: id, Method: "events.subscribe", Params: params}); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	r := bufio.NewReaderSize(conn, 64<<10)
	_ = conn.SetReadDeadline(time.Now().Add(c.timeout()))
	var ack response
	if err := readJSONLine(r, &ack); err != nil {
		return fmt.Errorf("ack: %w", err)
	}
	if ack.Error != nil {
		return ack.Error
	}
	_ = conn.SetReadDeadline(time.Time{})
	if resubscribe {
		c.signalResync()
	}

	for {
		line, err := readLine(r)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return fmt.Errorf("read: %w", err)
			}
			return fmt.Errorf("connection closed: %w", err)
		}
		// Since herdr 0.9.2 a subscriber that fell behind gets an error
		// envelope and the connection is closed.
		if bytes.Contains(line, []byte(`"error"`)) {
			var env response
			if json.Unmarshal(line, &env) == nil && env.Error != nil {
				return env.Error
			}
		}
		handle(line, time.Now())
	}
}
