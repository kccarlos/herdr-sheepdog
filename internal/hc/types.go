package hc

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Terminal is one terminal record from herdr-core.
type Terminal struct {
	Address    string            `json:"address"`
	Name       string            `json:"name"`
	Argv       []string          `json:"argv"`
	Cwd        string            `json:"cwd"`
	State      string            `json:"state"`
	Status     *Status           `json:"status,omitempty"`
	Reported   bool              `json:"reported"`
	Turn       uint64            `json:"turn"`
	Title      string            `json:"title"`
	Labels     map[string]string `json:"labels"`
	Foreground *Foreground       `json:"foreground,omitempty"`
	PID        *int              `json:"pid,omitempty"`
	ExitCode   *int              `json:"exit_code,omitempty"`
	Reason     *string           `json:"reason,omitempty"`
	CreatedMS  uint64            `json:"created_ms"`
	FinishedMS *uint64           `json:"finished_ms,omitempty"`
	StatusMS   *uint64           `json:"status_ms,omitempty"`
	Cols       uint16            `json:"cols"`
	Rows       uint16            `json:"rows"`
	Keep       bool              `json:"keep"`
	Parent     *string           `json:"parent,omitempty"`
}

// Status is the root Program Status Protocol record of a terminal.
type Status struct {
	State    string  `json:"state"`
	App      string  `json:"app"`
	Kind     *string `json:"kind,omitempty"`
	Msg      string  `json:"msg"`
	Title    string  `json:"title"`
	Progress *int    `json:"progress,omitempty"`
}

// Foreground describes the process group currently using a terminal.
type Foreground struct {
	PID  int      `json:"pid"`
	Name string   `json:"name"`
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
}

// CoreInfo identifies the herdr-core instance.
type CoreInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// HelloResult is the protocol handshake result.
type HelloResult struct {
	Version    string   `json:"version"`
	Generation uint32   `json:"generation"`
	Features   []string `json:"features"`
	Methods    []string `json:"methods"`
	Core       CoreInfo `json:"core"`
}

// ListResult is the terminal.list result.
type ListResult struct {
	Terminals      []Terminal `json:"terminals"`
	HiddenFinished int        `json:"hidden_finished"`
}

// ReadResult is the text read from a terminal.
type ReadResult struct {
	Text     string  `json:"text"`
	Complete bool    `json:"complete"`
	How      string  `json:"how"`
	Why      *string `json:"why,omitempty"`
}

// SendResult reports the state observed after text was submitted.
type SendResult struct {
	Started *bool   `json:"started,omitempty"`
	Status  *string `json:"status,omitempty"`
	Turn    uint64  `json:"turn"`
}

// KeysResult reports the terminal state observed after keys were sent.
type KeysResult struct {
	Status *string `json:"status,omitempty"`
	Turn   uint64  `json:"turn"`
}

// InterruptResult reports which key was tried and what happened.
type InterruptResult struct {
	Key     string `json:"key"`
	Outcome string `json:"outcome"`
}

// RunParams selects the program and attributes for terminal.run.
type RunParams struct {
	Argv     []string          `json:"argv,omitempty"`
	Line     string            `json:"line,omitempty"`
	Cwd      string            `json:"cwd,omitempty"`
	Name     string            `json:"name,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Keep     bool              `json:"keep,omitempty"`
	NoParent bool              `json:"no_parent,omitempty"`
}

// Error is a JSON-RPC error returned by herdr-core.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Kind    string `json:"kind,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

// Error returns the server message and appends the hint when present.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Hint != "" {
		return fmt.Sprintf("%s: %s", e.Message, e.Hint)
	}
	return e.Message
}

// Is matches a herdr-core error kind to its sentinel error.
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrNotFound:
		return e.Kind == "not_found"
	case ErrBlocked:
		return e.Kind == "blocked"
	case ErrFinished:
		return e.Kind == "finished"
	case ErrUnavailable:
		return e.Kind == "unavailable"
	case ErrNameTaken:
		return e.Kind == "name_taken"
	}
	return false
}

// Error kinds that callers commonly need to handle.
var (
	// ErrNotFound matches a missing terminal or other missing resource.
	ErrNotFound = errors.New("hc: resource not found")
	// ErrBlocked matches an operation refused because a terminal is blocked.
	ErrBlocked = errors.New("hc: terminal is blocked")
	// ErrFinished matches an operation on a finished terminal.
	ErrFinished = errors.New("hc: terminal is finished")
	// ErrUnavailable matches a temporarily unavailable terminal.
	ErrUnavailable = errors.New("hc: terminal is unavailable")
	// ErrNameTaken matches a terminal name already in use.
	ErrNameTaken = errors.New("hc: terminal name is taken")
)

// SubscribeFilter narrows an event subscription.
type SubscribeFilter struct {
	Terminal string            `json:"terminal,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// SubscribeResult is the snapshot returned by events.subscribe.
type SubscribeResult struct {
	Seq       uint64     `json:"seq"`
	Terminals []Terminal `json:"terminals"`
}

// StatusData is the data of a status event.
type StatusData struct {
	Status   *Status `json:"status"`
	Reported bool    `json:"reported"`
}

// FinishedData is the data of a finished or lost event.
type FinishedData struct {
	Reason   string `json:"reason"`
	ExitCode *int   `json:"exit_code"`
}

// TitleData is the data of a title event.
type TitleData struct {
	Title string `json:"title"`
}

// Notification is one event, heartbeat, or missed notification.
type Notification struct {
	Method    string          `json:"method"`
	Seq       uint64          `json:"seq"`
	Type      string          `json:"type,omitempty"`
	Terminal  string          `json:"terminal,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Terminals []Terminal      `json:"terminals,omitempty"`
}
