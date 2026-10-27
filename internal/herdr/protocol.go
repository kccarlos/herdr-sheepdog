package herdr

import (
	"encoding/json"
	"errors"
	"fmt"
)

// State is herdr's AgentStatus enum, exactly as the server spells it.
type State string

// The five agent states (docs/dev/herdr-protocol.md §2).
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
	// ErrNoReply marks a request that was written to the socket but whose
	// reply never arrived (read timeout, closed connection). The request
	// most likely reached herdr: a caller must not repeat a request that
	// is not idempotent (agent.prompt) as if it had not been sent.
	ErrNoReply = errors.New("herdr: request written, no reply")
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

// AgentInfo is herdr's AgentInfo object (docs/dev/herdr-protocol.md §3).
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
	// LaunchPending is true while an `agent.start` launch has not reached
	// its first ready state (an agent at its first-run trust question is
	// still pending). herdr refuses a rename until it is false.
	LaunchPending bool `json:"launch_pending,omitempty"`
	// Session is the agent's own session as its integration hook reported
	// it (pane.report_agent_session); nil when none was reported.
	Session *AgentSession `json:"agent_session,omitempty"`
}

// AgentSession is herdr's agent_session object (docs/dev/herdr-protocol.md
// §3, §9): the session the pane's agent reported at SessionStart, so the
// daemon can read the worker's own transcript (docs/dev/iteration-2.md WP4).
// Every recorded sample reports Kind "id" — Codex, Claude Code and agy
// alike; "path" (Claude's agent_session_path) is decoded but not seen.
type AgentSession struct {
	// Source is the reporting integration ("herdr:codex", "herdr:claude",
	// "herdr:antigravity_cli").
	Source string `json:"source"`
	// Agent is the kind label the integration reported for.
	Agent string `json:"agent,omitempty"`
	// Kind is "id" (Value is a session id) or "path" (Value is the
	// transcript file).
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// The AgentSession kinds.
const (
	SessionKindID   = "id"
	SessionKindPath = "path"
)

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
	// Session is the pane's agent_session, as on AgentInfo.
	Session *AgentSession `json:"agent_session,omitempty"`
}

// WorkspaceInfo is herdr's WorkspaceInfo.
type WorkspaceInfo struct {
	WorkspaceID string `json:"workspace_id"`
	Number      int    `json:"number"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
	PaneCount   int    `json:"pane_count"`
	Status      State  `json:"agent_status"`
	// Worktree is set when the workspace is a git checkout herdr knows
	// (null otherwise).
	Worktree *WorkspaceWorktree `json:"worktree,omitempty"`
}

// WorkspaceWorktree is herdr's WorkspaceWorktreeInfo.
type WorkspaceWorktree struct {
	RepoName     string `json:"repo_name"`
	RepoRoot     string `json:"repo_root"`
	CheckoutPath string `json:"checkout_path"`
	// Linked is true for a linked worktree (not the repository's main
	// checkout).
	Linked bool `json:"is_linked_worktree"`
}

// Worktree is herdr's WorktreeInfo: one checkout of a repository.
type Worktree struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Label    string `json:"label"`
	Linked   bool   `json:"is_linked_worktree"`
	Detached bool   `json:"is_detached"`
	Prunable bool   `json:"is_prunable"`
	Bare     bool   `json:"is_bare"`
	// OpenWorkspace is the workspace open in it ("" when none).
	OpenWorkspace string `json:"open_workspace_id,omitempty"`
}

// WorktreeList is the `worktree.list` result.
type WorktreeList struct {
	Source struct {
		RepoName string `json:"repo_name"`
		RepoRoot string `json:"repo_root"`
	} `json:"source"`
	Worktrees []Worktree `json:"worktrees"`
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

// Read sources (docs/dev/herdr-protocol.md §5). RecentUnwrapped is the best
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

// ProcessInfo is the `pane.process_info` result.
type ProcessInfo struct {
	PaneID                 string        `json:"pane_id"`
	ShellPID               uint32        `json:"shell_pid,omitempty"`
	ForegroundProcessGroup uint32        `json:"foreground_process_group_id,omitempty"`
	TTY                    string        `json:"tty,omitempty"`
	ForegroundProcesses    []ProcessItem `json:"foreground_processes,omitempty"`
}

// ProcessItem is one foreground process of a pane.
type ProcessItem struct {
	PID  uint32   `json:"pid"`
	Name string   `json:"name"`
	Argv []string `json:"argv,omitempty"`
}

// ShellForeground reports whether the pane's shell itself is the foreground
// process: its process group owns the terminal and it is listed among the
// foreground processes. This is herdr's own CLI predicate
// (src/cli/agent.rs, process_info_shows_shell_initialization); false while
// a child (an rc-file update check, a started program) holds the terminal.
func (p ProcessInfo) ShellForeground() bool {
	if p.ShellPID == 0 || p.ForegroundProcessGroup != p.ShellPID {
		return false
	}
	for _, f := range p.ForegroundProcesses {
		if f.PID == p.ShellPID {
			return true
		}
	}
	return false
}
