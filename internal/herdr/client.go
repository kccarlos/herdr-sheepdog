package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultTimeout bounds every one-shot request (dial + write + read).
const DefaultTimeout = 5 * time.Second

// maxLine is the largest response line the client accepts (matches the
// server's 1 MiB request limit; screen reads are well under it).
const maxLine = 4 << 20

// Client talks to one herdr server over its unix socket. Every method opens
// a fresh connection, sends one NDJSON request and reads one response line,
// because the server closes the connection after each reply
// (docs/herdr-protocol.md §1). Subscriptions live in events.go.
//
// A Client is safe for concurrent use.
type Client struct {
	// Socket is the path of the herdr unix socket.
	Socket string
	// Timeout bounds each one-shot request; DefaultTimeout when zero.
	Timeout time.Duration
	// Log receives herdr.read / herdr.send / herdr.reconnect events; nil
	// disables logging.
	Log Logger
	// ReconnectBackoff controls subscription reconnects; DefaultBackoff when
	// zero.
	ReconnectBackoff Backoff

	seq atomic.Uint64
}

// NewClient returns a client for the socket at path with default timeout.
func NewClient(socket string) *Client {
	return &Client{Socket: socket, Timeout: DefaultTimeout}
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

func (c *Client) nextID(method string) string {
	return "sheepdog-" + method + "-" + strconv.FormatUint(c.seq.Add(1), 10)
}

func (c *Client) log(typ, paneID string, payload any) {
	if c.Log != nil {
		c.Log.Log("herdr", typ, paneID, payload)
	}
}

// Call sends one request and decodes `result` into out (which may be nil).
// A server error is returned as *Error; use errors.Is with the sentinels.
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	conn, err := dial(ctx, c.Socket)
	if err != nil {
		return fmt.Errorf("herdr: dial %s: %w", c.Socket, err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	id := c.nextID(method)
	if params == nil {
		params = struct{}{}
	}
	if err := writeJSONLine(conn, request{ID: id, Method: method, Params: params}); err != nil {
		return fmt.Errorf("herdr: %s: write: %w", method, err)
	}

	var resp response
	if err := readJSONLine(bufio.NewReaderSize(conn, 64<<10), &resp); err != nil {
		return fmt.Errorf("herdr: %s: read: %w", method, err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if resp.ID != id && resp.ID != "" {
		return fmt.Errorf("herdr: %s: response id %q does not match request %q", method, resp.ID, id)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("herdr: %s: decode result: %w", method, err)
		}
	}
	return nil
}

// Ping returns the server version and protocol number.
func (c *Client) Ping(ctx context.Context) (Pong, error) {
	var p Pong
	err := c.Call(ctx, "ping", nil, &p)
	return p, err
}

// ListAgents returns every pane currently hosting a recognised agent.
func (c *Client) ListAgents(ctx context.Context) ([]AgentInfo, error) {
	var res struct {
		Agents []AgentInfo `json:"agents"`
	}
	if err := c.Call(ctx, "agent.list", nil, &res); err != nil {
		return nil, err
	}
	return res.Agents, nil
}

// GetAgent returns the agent hosted by paneID (or by agent name).
func (c *Client) GetAgent(ctx context.Context, paneID string) (AgentInfo, error) {
	var res struct {
		Agent AgentInfo `json:"agent"`
	}
	err := c.Call(ctx, "agent.get", map[string]any{"target": paneID}, &res)
	return res.Agent, err
}

// Snapshot returns the whole session (workspaces, panes, agents); it is the
// `herdr api snapshot` equivalent used by the Watcher's diff loop.
func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	var res struct {
		Snapshot Snapshot `json:"snapshot"`
	}
	err := c.Call(ctx, "session.snapshot", nil, &res)
	return res.Snapshot, err
}

// Read returns the last `lines` lines of the pane's screen from the given
// source as plain text (ANSI stripped). lines <= 0 uses the server default
// (80 for recent*, the whole snapshot for visible/detection). Truncated is
// set when the server dropped lines.
func (c *Client) Read(ctx context.Context, paneID string, source ReadSource, lines int) (ReadResult, error) {
	if source == "" {
		source = SourceRecentUnwrapped
	}
	params := map[string]any{
		"pane_id":    paneID,
		"source":     string(source),
		"format":     "text",
		"strip_ansi": true,
	}
	if lines > 0 {
		params["lines"] = lines
	}
	var res struct {
		Read ReadResult `json:"read"`
	}
	start := time.Now()
	err := c.Call(ctx, "pane.read", params, &res)
	c.log("herdr.read", paneID, map[string]any{
		"source":    string(source),
		"lines":     lines,
		"bytes":     len(res.Read.Text),
		"truncated": res.Read.Truncated,
		"dur_ms":    time.Since(start).Milliseconds(),
		"error":     errString(err),
	})
	return res.Read, err
}

// Prompt submits text + Enter to the agent in paneID as one submission
// (`agent.prompt`). The server refuses with ErrAgentBlocked while an
// approval/question UI is showing; answer those with SendKeys instead.
func (c *Client) Prompt(ctx context.Context, paneID, text string) (AgentInfo, error) {
	var res struct {
		Agent AgentInfo `json:"agent"`
	}
	err := c.Call(ctx, "agent.prompt", map[string]any{"target": paneID, "text": text}, &res)
	c.log("herdr.send", paneID, map[string]any{
		"kind":  "prompt",
		"text":  text,
		"error": errString(err),
	})
	return res.Agent, err
}

// SendKeys sends named keys (`enter`, `esc`, `1`, `ctrl+c`, …) to the pane
// via `pane.send_keys`. All keys are validated by the server before any
// byte is written.
func (c *Client) SendKeys(ctx context.Context, paneID string, keys ...string) error {
	if len(keys) == 0 {
		return fmt.Errorf("herdr: SendKeys: no keys")
	}
	err := c.Call(ctx, "pane.send_keys", map[string]any{"pane_id": paneID, "keys": keys}, nil)
	c.log("herdr.send", paneID, map[string]any{
		"kind":  "keys",
		"keys":  keys,
		"error": errString(err),
	})
	return err
}

// Interrupt sends Ctrl-C to the pane. On a working Codex/Claude turn this
// interrupts the turn and leaves the agent alive at its prompt.
func (c *Client) Interrupt(ctx context.Context, paneID string) error {
	return c.SendKeys(ctx, paneID, "ctrl+c")
}

func dial(ctx context.Context, socket string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", socket)
}

func writeJSONLine(conn net.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(b, '\n'))
	return err
}

func readJSONLine(r *bufio.Reader, v any) error {
	line, err := readLine(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v)
}

// readLine reads one newline-terminated line, tolerating lines longer than
// the bufio buffer, and rejecting lines over maxLine.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		part, err := r.ReadSlice('\n')
		buf = append(buf, part...)
		if len(buf) > maxLine {
			return nil, fmt.Errorf("line exceeds %d bytes", maxLine)
		}
		switch {
		case err == nil:
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
			// Unterminated final line: accept it.
			return buf, nil
		default:
			return nil, err
		}
	}
}

func splitLines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
