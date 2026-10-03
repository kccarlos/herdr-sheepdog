package tier3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Herdr drives one named herdr session through the herdr CLI
// (`herdr --session <name> …`). A named session is its own server with
// its own socket under ~/.config/herdr/sessions/<name>/, so nothing here
// can see or touch the default session's panes.
type Herdr struct {
	// Bin is the herdr executable (default "herdr").
	Bin string
	// Session is the session name; it must not be "default".
	Session string
	server  *exec.Cmd
}

// errDefaultSession guards the one rule the bench never breaks.
var errDefaultSession = errors.New("tier3: refusing to use herdr's default session")

func (h *Herdr) bin() string {
	if h.Bin == "" {
		return "herdr"
	}
	return h.Bin
}

// run runs `herdr --session S args…` and returns stdout.
func (h *Herdr) run(ctx context.Context, args ...string) ([]byte, error) {
	if h.Session == "" || h.Session == "default" {
		return nil, errDefaultSession
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.bin(), append([]string{"--session", h.Session}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return out.Bytes(), fmt.Errorf("herdr %s: %w: %s%s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(errb.String()), strings.TrimSpace(out.String()))
	}
	return out.Bytes(), nil
}

// call runs a JSON-answering command and decodes .result into v.
func (h *Herdr) call(ctx context.Context, v any, args ...string) error {
	out, err := h.run(ctx, args...)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil // pane run, send-keys: nothing to say on success
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("herdr %s: bad JSON: %w", args[0], err)
	}
	if env.Error != nil {
		return &CallError{Code: env.Error.Code, Message: env.Error.Message}
	}
	if v != nil {
		return json.Unmarshal(env.Result, v)
	}
	return nil
}

// CallError is an error answer of the herdr API.
type CallError struct{ Code, Message string }

func (e *CallError) Error() string { return "herdr: " + e.Code + ": " + e.Message }

// Start runs the session's headless server and waits for its socket.
// log receives the server's output.
func (h *Herdr) Start(ctx context.Context, log io.Writer) error {
	if h.Session == "" || h.Session == "default" {
		return errDefaultSession
	}
	if _, err := h.SocketPath(ctx); err == nil {
		return fmt.Errorf("tier3: herdr session %q already exists; stop and delete it first", h.Session)
	}
	cmd := exec.Command(h.bin(), "--session", h.Session, "server")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tier3: start herdr server: %w", err)
	}
	h.server = cmd
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if p, err := h.SocketPath(ctx); err == nil {
			if _, err := os.Stat(p); err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("tier3: herdr session %q did not come up", h.Session)
}

var sessionLine = regexp.MustCompile(`^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s*$`)

// SocketPath is the session's API socket, from `herdr session list`.
func (h *Herdr) SocketPath(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, h.bin(), "session", "list").Output()
	if err != nil {
		return "", err
	}
	return parseSessionSocket(string(out), h.Session)
}

func parseSessionSocket(list, name string) (string, error) {
	for _, line := range strings.Split(list, "\n") {
		m := sessionLine.FindStringSubmatch(strings.TrimSpace(line))
		if m != nil && m[1] == name && m[2] == "running" {
			return m[4], nil
		}
	}
	return "", fmt.Errorf("tier3: herdr session %q is not running", name)
}

// Stop stops and deletes the session, and reaps the server process.
func (h *Herdr) Stop() error {
	if h.Session == "" || h.Session == "default" {
		return errDefaultSession
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var errs []error
	if out, err := exec.CommandContext(ctx, h.bin(), "session", "stop", h.Session).CombinedOutput(); err != nil {
		errs = append(errs, fmt.Errorf("session stop: %w: %s", err, strings.TrimSpace(string(out))))
	}
	if h.server != nil {
		done := make(chan struct{})
		go func() { _ = h.server.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = h.server.Process.Kill()
			<-done
		}
	}
	if out, err := exec.CommandContext(ctx, h.bin(), "session", "delete", h.Session).CombinedOutput(); err != nil {
		errs = append(errs, fmt.Errorf("session delete: %w: %s", err, strings.TrimSpace(string(out))))
	}
	return errors.Join(errs...)
}

// Pane is a pane as herdr reports it.
type Pane struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	Cwd         string `json:"cwd"`
	AgentStatus string `json:"agent_status"`
	Agent       string `json:"agent"`
	Name        string `json:"name"`
}

// CreateWorkspace creates a workspace and returns its root pane.
func (h *Herdr) CreateWorkspace(ctx context.Context, cwd, label string, env []string) (Pane, error) {
	args := []string{"workspace", "create", "--cwd", cwd, "--label", label, "--no-focus"}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	var res struct {
		RootPane Pane `json:"root_pane"`
	}
	err := h.call(ctx, &res, args...)
	return res.RootPane, err
}

// Split splits pane and returns the new one.
func (h *Herdr) Split(ctx context.Context, paneID, cwd string, env []string) (Pane, error) {
	args := []string{"pane", "split", paneID, "--direction", "right", "--cwd", cwd, "--no-focus"}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	var res struct {
		Pane Pane `json:"pane"`
	}
	err := h.call(ctx, &res, args...)
	return res.Pane, err
}

// Run types command into the pane's shell and presses Enter.
func (h *Herdr) Run(ctx context.Context, paneID, command string) error {
	return h.call(ctx, nil, "pane", "run", paneID, command)
}

// Read returns the pane's recent text.
func (h *Herdr) Read(ctx context.Context, paneID string, lines int) (string, error) {
	out, err := h.run(ctx, "pane", "read", paneID, "--source", "recent", "--lines", fmt.Sprint(lines))
	return string(out), err
}

// SendKeys sends keys to the pane.
func (h *Herdr) SendKeys(ctx context.Context, paneID string, keys ...string) error {
	return h.call(ctx, nil, append([]string{"pane", "send-keys", paneID}, keys...)...)
}

// Prompt submits text to the agent in the pane (no wait).
func (h *Herdr) Prompt(ctx context.Context, paneID, text string) error {
	return h.call(ctx, nil, "agent", "prompt", paneID, text)
}

// AgentStatus is the pane's agent status ("" when no agent is seen).
func (h *Herdr) AgentStatus(ctx context.Context, paneID string) (string, error) {
	var res struct {
		Pane Pane `json:"pane"`
	}
	if err := h.call(ctx, &res, "pane", "get", paneID); err != nil {
		return "", err
	}
	if res.Pane.Agent == "" && res.Pane.AgentStatus == "unknown" {
		return "", nil
	}
	return res.Pane.AgentStatus, nil
}

// Panes lists every pane of the session.
func (h *Herdr) Panes(ctx context.Context) ([]Pane, error) {
	var res struct {
		Panes []Pane `json:"panes"`
	}
	err := h.call(ctx, &res, "pane", "list")
	return res.Panes, err
}

// CloseWorkspace closes a workspace (its panes' processes end).
func (h *Herdr) CloseWorkspace(ctx context.Context, id string) error {
	return h.call(ctx, nil, "workspace", "close", id)
}

// DefaultAgentPanes lists the pane ids with an agent in herdr's default
// session (read-only), for the isolation proof.
func DefaultAgentPanes(ctx context.Context, bin string) ([]string, error) {
	if bin == "" {
		bin = "herdr"
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "agent", "list").Output()
	if err != nil {
		return nil, err
	}
	var env struct {
		Result struct {
			Agents []struct {
				PaneID string `json:"pane_id"`
				Cwd    string `json:"cwd"`
			} `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, err
	}
	var ids []string
	for _, a := range env.Result.Agents {
		ids = append(ids, a.PaneID+" "+filepath.Base(a.Cwd))
	}
	return ids, nil
}
