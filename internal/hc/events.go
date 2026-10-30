package hc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
)

// Subscribe establishes a dedicated event connection and returns its initial
// snapshot. The caller must call Close when finished with the subscription.
func (c *Client) Subscribe(ctx context.Context, filter SubscribeFilter) (*Subscription, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(callCtx, "unix", c.Socket)
	if err != nil {
		return nil, fmt.Errorf("hc: events.subscribe: dial %s: %w", c.Socket, err)
	}

	stopHandshake := context.AfterFunc(callCtx, func() { _ = conn.Close() })
	const requestID uint64 = 1
	request := wireRequest{
		JSONRPC: "2.0",
		ID:      requestID,
		Method:  "events.subscribe",
		Params:  filter,
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		stopHandshake()
		_ = conn.Close()
		if callCtx.Err() != nil {
			return nil, fmt.Errorf("hc: events.subscribe: %w", callCtx.Err())
		}
		return nil, fmt.Errorf("hc: events.subscribe: write request: %w", err)
	}

	dec := json.NewDecoder(conn)
	var snapshot SubscribeResult
	for {
		var response wireResponse
		if err := dec.Decode(&response); err != nil {
			stopHandshake()
			_ = conn.Close()
			if callCtx.Err() != nil {
				return nil, fmt.Errorf("hc: events.subscribe: %w", callCtx.Err())
			}
			return nil, fmt.Errorf("hc: events.subscribe: read reply: %w", err)
		}
		if response.ID == nil || *response.ID != requestID {
			continue
		}
		if response.Error != nil {
			stopHandshake()
			_ = conn.Close()
			return nil, fmt.Errorf("hc: events.subscribe: %w", response.Error.toError())
		}
		if err := json.Unmarshal(response.Result, &snapshot); err != nil {
			stopHandshake()
			_ = conn.Close()
			return nil, fmt.Errorf("hc: events.subscribe: decode result: %w", err)
		}
		break
	}

	stopHandshake()
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("hc: events.subscribe: %w", err)
	}

	s := &Subscription{
		conn:      conn,
		ctx:       ctx,
		events:    make(chan Notification, 32),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		seq:       snapshot.Seq,
		terminals: snapshot.Terminals,
	}
	s.stopContext = context.AfterFunc(ctx, s.stopForContext)
	go s.readLoop(dec)
	return s, nil
}

// Subscription is a live events.subscribe connection.
type Subscription struct {
	conn net.Conn
	ctx  context.Context

	events chan Notification
	stop   chan struct{}
	done   chan struct{}

	stopContext func() bool
	shutdown    sync.Once
	closing     atomic.Bool
	contextEnd  atomic.Bool
	closeErr    error

	snapshotMu sync.RWMutex
	seq        uint64
	terminals  []Terminal

	errMu sync.Mutex
	err   error
}

// Snapshot returns the subscription's initial sequence and matching terminals.
func (s *Subscription) Snapshot() (seq uint64, terminals []Terminal) {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return s.seq, append([]Terminal(nil), s.terminals...)
}

// Events returns the notification stream. It closes when the connection ends
// or the subscription is closed.
func (s *Subscription) Events() <-chan Notification {
	return s.events
}

// Err reports why the subscription ended. It is nil after Close or context
// cancellation.
func (s *Subscription) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

// Close ends the subscription and waits for its reader to finish.
func (s *Subscription) Close() error {
	s.shutdownOnce(false)
	<-s.done
	return s.closeErr
}

func (s *Subscription) shutdownOnce(contextEnd bool) {
	s.shutdown.Do(func() {
		s.closing.Store(true)
		s.contextEnd.Store(contextEnd)
		close(s.stop)
		s.closeErr = s.conn.Close()
	})
}

func (s *Subscription) stopForContext() {
	s.shutdownOnce(true)
}

func (s *Subscription) readLoop(dec *json.Decoder) {
	defer func() {
		if s.stopContext != nil {
			s.stopContext()
		}
		close(s.events)
		close(s.done)
	}()

	for {
		var message incomingMessage
		if err := dec.Decode(&message); err != nil {
			if !s.contextEnd.Load() && !s.closing.Load() {
				if errors.Is(err, io.EOF) {
					err = io.EOF
				}
				s.setErr(fmt.Errorf("hc: subscription: %w", err))
			}
			return
		}
		if len(message.ID) != 0 {
			continue
		}
		notification, ok, err := decodeNotification(message)
		if err != nil {
			s.setErr(err)
			return
		}
		if !ok {
			continue
		}
		select {
		case s.events <- notification:
		case <-s.stop:
			return
		}
	}
}

func (s *Subscription) setErr(err error) {
	s.errMu.Lock()
	s.err = err
	s.errMu.Unlock()
}

type incomingMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func decodeNotification(message incomingMessage) (Notification, bool, error) {
	switch message.Method {
	case "event":
		var params struct {
			Seq      uint64          `json:"seq"`
			Type     string          `json:"type"`
			Terminal string          `json:"terminal"`
			Data     json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return Notification{}, false, fmt.Errorf("hc: decode event notification: %w", err)
		}
		return Notification{
			Method:   message.Method,
			Seq:      params.Seq,
			Type:     params.Type,
			Terminal: params.Terminal,
			Data:     params.Data,
		}, true, nil
	case "heartbeat":
		var params struct {
			Seq uint64 `json:"seq"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return Notification{}, false, fmt.Errorf("hc: decode heartbeat notification: %w", err)
		}
		return Notification{Method: message.Method, Seq: params.Seq}, true, nil
	case "missed":
		var params struct {
			Seq       uint64     `json:"seq"`
			Terminals []Terminal `json:"terminals"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return Notification{}, false, fmt.Errorf("hc: decode missed notification: %w", err)
		}
		return Notification{
			Method:    message.Method,
			Seq:       params.Seq,
			Terminals: params.Terminals,
		}, true, nil
	default:
		return Notification{}, false, nil
	}
}

// DecodeData decodes the event-specific data into v.
func (n Notification) DecodeData(v any) error {
	if err := json.Unmarshal(n.Data, v); err != nil {
		return fmt.Errorf("hc: decode %s event data: %w", n.Type, err)
	}
	return nil
}
