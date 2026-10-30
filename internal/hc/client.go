package hc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

const defaultTimeout = 5 * time.Second

// Client calls a herdr-core JSON-RPC endpoint.
type Client struct {
	// Socket is the Unix socket path.
	Socket string
	// Timeout limits each call or subscription handshake. Zero uses 5 seconds.
	Timeout time.Duration
}

// NewClient returns a client for socket.
func NewClient(socket string) *Client {
	return &Client{Socket: socket}
}

// Call invokes a JSON-RPC method and decodes its result into out.
//
// Each call uses a new connection. Call is not safe to use with a nil out; pass
// a pointer to the expected result type.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(callCtx, "unix", c.Socket)
	if err != nil {
		return fmt.Errorf("hc: %s: dial %s: %w", method, c.Socket, err)
	}
	defer func() { _ = conn.Close() }()

	stop := context.AfterFunc(callCtx, func() { _ = conn.Close() })
	defer stop()

	const requestID uint64 = 1
	request := wireRequest{
		JSONRPC: "2.0",
		ID:      requestID,
		Method:  method,
		Params:  params,
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		if callCtx.Err() != nil {
			return fmt.Errorf("hc: %s: %w", method, callCtx.Err())
		}
		return fmt.Errorf("hc: %s: write request: %w", method, err)
	}

	dec := json.NewDecoder(conn)
	for {
		var response wireResponse
		if err := dec.Decode(&response); err != nil {
			if callCtx.Err() != nil {
				return fmt.Errorf("hc: %s: %w", method, callCtx.Err())
			}
			return fmt.Errorf("hc: %s: read reply: %w", method, err)
		}
		if response.ID == nil || *response.ID != requestID {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("hc: %s: %w", method, response.Error.toError())
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(response.Result, out); err != nil {
			return fmt.Errorf("hc: %s: decode result: %w", method, err)
		}
		return nil
	}
}

// Hello performs the protocol handshake for generation 1.
func (c *Client) Hello(ctx context.Context) (HelloResult, error) {
	var result HelloResult
	err := c.Call(ctx, "hello", struct {
		Generation int `json:"generation"`
	}{Generation: 1}, &result)
	return result, err
}

// List returns terminals, including finished terminals when all is true.
func (c *Client) List(ctx context.Context, all bool) (ListResult, error) {
	var result ListResult
	err := c.Call(ctx, "terminal.list", struct {
		All bool `json:"all"`
	}{All: all}, &result)
	return result, err
}

// Get returns one terminal by address or name.
func (c *Client) Get(ctx context.Context, terminal string) (Terminal, error) {
	var result Terminal
	err := c.Call(ctx, "terminal.get", struct {
		Terminal string `json:"terminal"`
	}{Terminal: terminal}, &result)
	return result, err
}

// Read returns a terminal's screen plus a number of history lines.
func (c *Client) Read(ctx context.Context, terminal string, lines int) (ReadResult, error) {
	var result ReadResult
	err := c.Call(ctx, "terminal.read", struct {
		Terminal string `json:"terminal"`
		Lines    int    `json:"lines"`
	}{Terminal: terminal, Lines: lines}, &result)
	return result, err
}

// Send pastes text and submits it with Enter.
func (c *Client) Send(ctx context.Context, terminal, text string, force bool) (SendResult, error) {
	var result SendResult
	err := c.Call(ctx, "terminal.send", struct {
		Terminal string `json:"terminal"`
		Text     string `json:"text"`
		Force    bool   `json:"force"`
	}{Terminal: terminal, Text: text, Force: force}, &result)
	return result, err
}

// Keys presses named keys in a terminal.
func (c *Client) Keys(ctx context.Context, terminal string, keys ...string) (KeysResult, error) {
	var result KeysResult
	err := c.Call(ctx, "terminal.keys", struct {
		Terminal string   `json:"terminal"`
		Keys     []string `json:"keys"`
	}{Terminal: terminal, Keys: keys}, &result)
	return result, err
}

// Interrupt asks herdr-core to press the terminal's stop key.
func (c *Client) Interrupt(ctx context.Context, terminal string) (InterruptResult, error) {
	var result InterruptResult
	err := c.Call(ctx, "terminal.interrupt", struct {
		Terminal string `json:"terminal"`
	}{Terminal: terminal}, &result)
	return result, err
}

// Run starts a program in a new terminal.
func (c *Client) Run(ctx context.Context, p RunParams) (Terminal, error) {
	var result Terminal
	err := c.Call(ctx, "terminal.run", p, &result)
	return result, err
}

// Kill ends a terminal's program.
func (c *Client) Kill(ctx context.Context, terminal string) (Terminal, error) {
	var result Terminal
	err := c.Call(ctx, "terminal.kill", struct {
		Terminal string `json:"terminal"`
	}{Terminal: terminal}, &result)
	return result, err
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultTimeout
}

type wireRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type wireResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *uint64         `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *wireError      `json:"error"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Kind string `json:"kind"`
		Hint string `json:"hint"`
	} `json:"data"`
}

func (e *wireError) toError() *Error {
	return &Error{
		Code:    e.Code,
		Message: e.Message,
		Kind:    e.Data.Kind,
		Hint:    e.Data.Hint,
	}
}
