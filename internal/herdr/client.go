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
	"sync"
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
// (docs/dev/herdr-protocol.md §1). Subscriptions live in events.go.
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

	resyncMu sync.Mutex
	resync   chan struct{}
}

// Resync returns a channel that receives (coalesced, capacity one) after a
// subscription is re-established following a drop or an events_lost end:
// events may have been missed in the gap, so the Watcher re-reads
// session.snapshot at once.
func (c *Client) Resync() <-chan struct{} {
	return c.resyncChan()
}

func (c *Client) resyncChan() chan struct{} {
	c.resyncMu.Lock()
	defer c.resyncMu.Unlock()
	if c.resync == nil {
		c.resync = make(chan struct{}, 1)
	}
	return c.resync
}

func (c *Client) signalResync() {
	select {
	case c.resyncChan() <- struct{}{}:
	default:
	}
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

// ReadANSI is Read with the colours kept (format ansi, strip_ansi off):
// the lines as SGR-coloured text at the pane's width, for the app's
// snapshot window when the live terminal is not available.
func (c *Client) ReadANSI(ctx context.Context, paneID string, source ReadSource, lines int) (ReadResult, error) {
	params := map[string]any{"pane_id": paneID, "source": string(source), "format": "ansi", "strip_ansi": false}
	if lines > 0 {
		params["lines"] = lines
	}
	var res struct {
		Read ReadResult `json:"read"`
	}
	err := c.Call(ctx, "pane.read", params, &res)
	c.log("herdr.read", paneID, map[string]any{"source": string(source), "lines": lines, "ansi": true, "bytes": len(res.Read.Text), "error": errString(err)})
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

// Rename sets the agent's herdr name (`agent.rename`), what herdr shows
// and reports as the agent's name from then on; an empty name clears it.
func (c *Client) Rename(ctx context.Context, paneID, name string) error {
	params := map[string]any{"target": paneID, "name": name}
	if name == "" {
		params["name"] = nil
	}
	err := c.Call(ctx, "agent.rename", params, nil)
	c.log("herdr.rename", paneID, map[string]any{"name": name, "error": errString(err)})
	return err
}

// CreateWorkspace opens a new workspace whose shell starts in cwd
// (`workspace.create`, unfocused) and returns its root pane.
func (c *Client) CreateWorkspace(ctx context.Context, cwd, label string) (PaneInfo, error) {
	var res struct {
		Root PaneInfo `json:"root_pane"`
	}
	params := map[string]any{"cwd": cwd, "focus": false, "env": PaneEnv}
	if label != "" {
		params["label"] = label
	}
	err := c.Call(ctx, "workspace.create", params, &res)
	c.log("herdr.workspace_create", res.Root.PaneID, map[string]any{"cwd": cwd, "label": label, "error": errString(err)})
	return res.Root, err
}

// CreateTab opens a new, unfocused tab in workspaceID whose shell starts
// in cwd (`tab.create`) and returns its root pane.
func (c *Client) CreateTab(ctx context.Context, workspaceID, cwd, label string) (PaneInfo, error) {
	var res struct {
		Root PaneInfo `json:"root_pane"`
	}
	params := map[string]any{"workspace_id": workspaceID, "cwd": cwd, "focus": false, "env": PaneEnv}
	if label != "" {
		params["label"] = label
	}
	err := c.Call(ctx, "tab.create", params, &res)
	c.log("herdr.tab_create", res.Root.PaneID, map[string]any{"workspace_id": workspaceID, "cwd": cwd, "label": label, "error": errString(err)})
	return res.Root, err
}

// CreateWorktree makes a git worktree on a new branch of cwd's repository
// and opens an unfocused workspace in it (`worktree.create`), returning
// the workspace's root pane. herdr picks the checkout path.
func (c *Client) CreateWorktree(ctx context.Context, cwd, branch, label string) (PaneInfo, error) {
	var res struct {
		Root PaneInfo `json:"root_pane"`
	}
	params := map[string]any{"cwd": cwd, "branch": branch, "focus": false}
	if label != "" {
		params["label"] = label
	}
	err := c.Call(ctx, "worktree.create", params, &res)
	c.log("herdr.worktree_create", res.Root.PaneID, map[string]any{"cwd": cwd, "branch": branch, "error": errString(err)})
	return res.Root, err
}

// RenameWorkspace sets a workspace's label (`workspace.rename`).
func (c *Client) RenameWorkspace(ctx context.Context, workspaceID, label string) error {
	err := c.Call(ctx, "workspace.rename", map[string]any{"workspace_id": workspaceID, "label": label}, nil)
	c.log("herdr.workspace_rename", "", map[string]any{"workspace_id": workspaceID, "label": label, "error": errString(err)})
	return err
}

// CloseWorkspace closes a workspace and every pane in it
// (`workspace.close`).
func (c *Client) CloseWorkspace(ctx context.Context, workspaceID string) error {
	err := c.Call(ctx, "workspace.close", map[string]any{"workspace_id": workspaceID}, nil)
	c.log("herdr.workspace_close", "", map[string]any{"workspace_id": workspaceID, "error": errString(err)})
	return err
}

// ListWorktrees lists the checkouts of the repository a workspace works
// in (`worktree.list`).
func (c *Client) ListWorktrees(ctx context.Context, workspaceID string) (WorktreeList, error) {
	var res WorktreeList
	err := c.Call(ctx, "worktree.list", map[string]any{"workspace_id": workspaceID}, &res)
	return res, err
}

// OpenWorktree opens an unfocused workspace in an existing checkout of
// the workspace's repository (`worktree.open`), by path or branch, and
// returns its root pane (the open one when it is already open).
func (c *Client) OpenWorktree(ctx context.Context, workspaceID, path, branch string) (PaneInfo, error) {
	var res struct {
		Root PaneInfo `json:"root_pane"`
	}
	params := map[string]any{"workspace_id": workspaceID, "focus": false}
	if path != "" {
		params["path"] = path
	}
	if branch != "" {
		params["branch"] = branch
	}
	err := c.Call(ctx, "worktree.open", params, &res)
	c.log("herdr.worktree_open", res.Root.PaneID, map[string]any{"workspace_id": workspaceID, "path": path, "branch": branch, "error": errString(err)})
	return res.Root, err
}

// RemoveWorktree removes the checkout a worktree workspace works in and
// closes it (`worktree.remove`); force removes a checkout with changes.
func (c *Client) RemoveWorktree(ctx context.Context, workspaceID string, force bool) error {
	err := c.Call(ctx, "worktree.remove", map[string]any{"workspace_id": workspaceID, "force": force}, nil)
	c.log("herdr.worktree_remove", "", map[string]any{"workspace_id": workspaceID, "force": force, "error": errString(err)})
	return err
}

// PaneEnv is set in the shell of every pane the daemon opens for a new
// agent (workspace.create, pane.split take an env map; agent.start then
// types the agent's executable at the shell prompt). It keeps
// oh-my-zsh from asking to update at shell start: on 2026-10-04 that
// prompt ate the "c" of "claude" on a voice spawn.
var PaneEnv = map[string]string{"DISABLE_AUTO_UPDATE": "true"}

// StartAgent starts a supported agent kind at the shell prompt of paneID
// (`agent.start`). herdr answers at once with the launch pending, types
// the kind's canonical executable plus args, and drops the launch when
// the agent is not detected within timeout; GetAgent's LaunchPending and
// status tell how it went. name must be a herdr agent name (lowercase,
// digits, - and _, 1-32 characters).
func (c *Client) StartAgent(ctx context.Context, paneID, name, kind string, args []string, timeout time.Duration) error {
	params := map[string]any{"name": name, "kind": kind, "pane_id": paneID}
	if len(args) > 0 {
		params["args"] = args
	}
	if timeout > 0 {
		params["timeout_ms"] = timeout.Milliseconds()
	}
	err := c.Call(ctx, "agent.start", params, nil)
	c.log("herdr.agent_start", paneID, map[string]any{"kind": kind, "name": name, "args": args, "error": errString(err)})
	return err
}

// GetAgent returns one agent by name or pane id (`agent.get`);
// ErrAgentNotFound when there is none (a launch herdr dropped).
func (c *Client) GetAgent(ctx context.Context, target string) (AgentInfo, error) {
	var res struct {
		Agent AgentInfo `json:"agent"`
	}
	err := c.Call(ctx, "agent.get", map[string]any{"target": target}, &res)
	return res.Agent, err
}

// AgentKinds lists the agent kinds herdr supports (`server.agent_manifests`):
// the kinds `agent.start` accepts, each its canonical executable.
func (c *Client) AgentKinds(ctx context.Context) ([]string, error) {
	var res struct {
		Manifests []struct {
			Agent string `json:"agent"`
		} `json:"manifests"`
	}
	if err := c.Call(ctx, "server.agent_manifests", map[string]any{}, &res); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res.Manifests))
	for _, m := range res.Manifests {
		if m.Agent != "" {
			out = append(out, m.Agent)
		}
	}
	return out, nil
}

// SplitPane splits paneID to the right, unfocused, with a new shell in
// cwd (`pane.split`), and returns the new pane. The pane to split goes
// in target_pane_id (PaneSplitParams in `herdr api schema --json`):
// herdr ignores an unknown pane_id and splits the focused pane instead,
// which put two voice-started agents into the wrong workspace on
// 2026-10-03 (the bench agent in kvoice's, the kvoice one in bench's).
func (c *Client) SplitPane(ctx context.Context, paneID, cwd string) (PaneInfo, error) {
	var res struct {
		Pane PaneInfo `json:"pane"`
	}
	err := c.Call(ctx, "pane.split", map[string]any{"target_pane_id": paneID, "direction": "right", "cwd": cwd, "focus": false, "env": PaneEnv}, &res)
	c.log("herdr.pane_split", paneID, map[string]any{"cwd": cwd, "pane": res.Pane.PaneID, "error": errString(err)})
	return res.Pane, err
}

// ClosePane closes the pane (`pane.close`), ending whatever runs in it;
// closing a workspace's last pane closes the workspace.
func (c *Client) ClosePane(ctx context.Context, paneID string) error {
	err := c.Call(ctx, "pane.close", map[string]any{"pane_id": paneID}, nil)
	c.log("herdr.pane_close", paneID, map[string]any{"error": errString(err)})
	return err
}

// SendInput types text into the pane and then presses keys, in one write
// (`pane.send_input`): a command line for the pane's shell.
func (c *Client) SendInput(ctx context.Context, paneID, text string, keys ...string) error {
	params := map[string]any{"pane_id": paneID, "text": text}
	if len(keys) > 0 {
		params["keys"] = keys
	}
	err := c.Call(ctx, "pane.send_input", params, nil)
	c.log("herdr.send", paneID, map[string]any{"kind": "input", "text": text, "keys": keys, "error": errString(err)})
	return err
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

// ProcessInfo returns the pane's shell and foreground processes
// (`pane.process_info`).
func (c *Client) ProcessInfo(ctx context.Context, paneID string) (ProcessInfo, error) {
	var res struct {
		ProcessInfo ProcessInfo `json:"process_info"`
	}
	err := c.Call(ctx, "pane.process_info", map[string]any{"pane_id": paneID}, &res)
	return res.ProcessInfo, err
}
