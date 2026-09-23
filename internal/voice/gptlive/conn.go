package gptlive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// Conn is one primary Live WebSocket. Send writes one JSON client event;
// Receive returns the next server event's raw JSON. Implementations must
// allow Send concurrently with Receive.
type Conn interface {
	Send(ctx context.Context, ev any) error
	Receive(ctx context.Context) ([]byte, error)
	Close() error
}

// Dialer opens a Conn. The WebSocket implementation is the default; tests
// inject a fake.
type Dialer interface {
	Dial(ctx context.Context, url, apiKey string) (Conn, error)
}

// wsDialer dials with github.com/coder/websocket. The only header the
// endpoint needs is the bearer token (docs: "no query parameters",
// "Authorization: Bearer $OPENAI_API_KEY"; verified live 2026-09-23).
type wsDialer struct{}

// HTTPError is a failed WebSocket handshake with its HTTP status.
type HTTPError struct {
	Status int
	Err    error
}

func (e *HTTPError) Error() string { return fmt.Sprintf("gptlive: handshake: HTTP %d: %v", e.Status, e.Err) }
func (e *HTTPError) Unwrap() error { return e.Err }

func (wsDialer) Dial(ctx context.Context, url, apiKey string) (Conn, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+apiKey)
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		if resp != nil {
			return nil, &HTTPError{Status: resp.StatusCode, Err: err}
		}
		return nil, fmt.Errorf("gptlive: dial: %w", err)
	}
	// Output audio arrives as base64 in JSON; a large chunk exceeds the
	// library's 32 KiB default.
	c.SetReadLimit(8 << 20)
	return &wsConn{c: c}, nil
}

type wsConn struct {
	c  *websocket.Conn
	mu sync.Mutex
}

func (w *wsConn) Send(ctx context.Context, ev any) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.c.Write(ctx, websocket.MessageText, b)
}

func (w *wsConn) Receive(ctx context.Context) ([]byte, error) {
	for {
		typ, data, err := w.c.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ == websocket.MessageText {
			return data, nil
		}
	}
}

func (w *wsConn) Close() error { return w.c.CloseNow() }

// isRateLimited reports a 429 at the handshake or in an error event.
func isRateLimited(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests
	}
	return false
}
