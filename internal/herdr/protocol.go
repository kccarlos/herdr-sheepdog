package herdr

import (
	"encoding/json"
	"errors"
	"fmt"
)

// State is herdr's AgentStatus enum, exactly as the server spells it.
type State string

// The five agent states (docs/herdr-protocol.md §2).
const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateBlocked State = "blocked"
	StateDone    State = "done"
	StateUnknown State = "unknown"
)

// Valid reports whether s is one of the five documented states.
func (s State) Valid() bool {
	switch s {
	case StateIdle, StateWorking, StateBlocked, StateDone, StateUnknown:
		return true
	}
	return false
}

// request is the NDJSON request envelope.
type request struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

// response is the NDJSON response envelope: exactly one of Result / Error is
// set.
type response struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error is a herdr server error (`{"error":{"code","message"}}`).
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("herdr: %s: %s", e.Code, e.Message)
}

// Sentinel errors for the codes callers need to branch on. Match them with
// errors.Is; the returned error is always the *Error carrying the message.
var (
	ErrAgentBlocked  = errors.New("herdr: agent is blocked at a prompt")
	ErrAgentNotReady = errors.New("herdr: agent is not ready")
	ErrPaneNotFound  = errors.New("herdr: pane not found")
	ErrAgentNotFound = errors.New("herdr: agent not found")
	ErrTimeout       = errors.New("herdr: server-side timeout")
)

// Is maps well-known error codes onto the sentinel errors above.
func (e *Error) Is(target error) bool {
	switch target {
	case ErrAgentBlocked:
		return e.Code == "agent_blocked"
	case ErrAgentNotReady:
		return e.Code == "agent_not_ready"
	case ErrPaneNotFound:
		return e.Code == "pane_not_found"
	case ErrAgentNotFound:
		return e.Code == "agent_not_found"
	case ErrTimeout:
		return e.Code == "timeout"
	}
	return false
}

// Pong is the result of `ping`.
type Pong struct {
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// AgentInfo is herdr's AgentInfo object (docs/herdr-protocol.md §3).
// Fields sheepdog does not use are left out; unknown fields are ignored.
type AgentInfo struct {
	PaneID        string `json:"pane_id"`
	WorkspaceID   string `json:"workspace_id"`
	TabID         string `json:"tab_id"`
	TerminalID    string `json:"terminal_id"`
	Status        State  `json:"agent_status"`
	Agent         string `json:"agent,omitempty"`
	Name          string `json:"name,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
	TerminalTitle string `json:"terminal_title_stripped,omitempty"`
	Focused       bool   `json:"focused"`
	Revision      uint64 `json:"revision"`
	// StateChangeSeq increases on every state change; absent from PaneInfo.
	StateChangeSeq uint64 `json:"state_change_seq,omitempty"`
}

// PaneInfo is herdr's PaneInfo: AgentInfo minus the agent-only fields, plus
// the user pane label.
type PaneInfo struct {
	PaneID        string `json:"pane_id"`
	WorkspaceID   string `json:"workspace_id"`
	TabID         string `json:"tab_id"`
	Status        State  `json:"agent_status"`
	Agent         string `json:"agent,omitempty"`
	Label         string `json:"label,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
	TerminalTitle string `json:"terminal_title_stripped,omitempty"`
}

// WorkspaceInfo is herdr's WorkspaceInfo.
type WorkspaceInfo struct {
	WorkspaceID string `json:"workspace_id"`
	Number      int    `json:"number"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	PaneCount   int    `json:"pane_count"`
	Status      State  `json:"agent_status"`
}

// Snapshot is the `session.snapshot` result (== `herdr api snapshot`).
type Snapshot struct {
	Version    string          `json:"version"`
	Protocol   int             `json:"protocol"`
	Workspaces []WorkspaceInfo `json:"workspaces"`
	Panes      []PaneInfo      `json:"panes"`
	Agents     []AgentInfo     `json:"agents"`
}

// WorkspaceLabel returns the label of the workspace with the given id, or ""
// when it is not in the snapshot.
func (s *Snapshot) WorkspaceLabel(id string) string {
	for _, w := range s.Workspaces {
		if w.WorkspaceID == id {
			return w.Label
		}
	}
	return ""
}

// ReadSource selects which buffer `pane.read` returns.
type ReadSource string

// Read sources (docs/herdr-protocol.md §5). RecentUnwrapped is the best
// choice for feeding an LLM; Visible is the rendered viewport.
const (
	SourceVisible         ReadSource = "visible"
	SourceRecent          ReadSource = "recent"
	SourceRecentUnwrapped ReadSource = "recent_unwrapped"
	SourceDetection       ReadSource = "detection"
)

// ReadResult is a pane screen read.
type ReadResult struct {
	PaneID    string     `json:"pane_id"`
	Source    ReadSource `json:"source"`
	Text      string     `json:"text"`
	Revision  uint64     `json:"revision"`
	Truncated bool       `json:"truncated"`
}

// Lines splits Text into lines, dropping a single trailing newline.
func (r ReadResult) Lines() []string {
	return splitLines(r.Text)
}
