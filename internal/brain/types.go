package brain

import (
	"context"
	"errors"
	"time"
)

// Errors callers branch on.
var (
	// ErrFallback is returned by a turn that could not produce a reply
	// (model error, budget exceeded, no model); the supervisor answers
	// from its template path.
	ErrFallback = errors.New("brain: fallback")
	// ErrNotPermitted is the rail refusing a send_keys; the Host returns
	// it (wrapped) when the pending action does not match at send time.
	ErrNotPermitted = errors.New("not permitted")
	// ErrNoCLI is returned by Host.RunCLI when no CLI query tool exists.
	ErrNoCLI = errors.New("no CLI query tool is available")
	// ErrNoHost is returned by a turn before SetHost was called.
	ErrNoHost = errors.New("brain: no host")
)

// Kind of a turn: what the assistant asked for.
type Kind string

// The turn kinds.
const (
	KindAsk     Kind = "ask"
	KindPropose Kind = "propose"
)

// Trigger of a turn: a CEO utterance (a voice tool call) or an event.
type Trigger string

// The triggers. Only CEO-triggered turns may pass the send_keys rail.
const (
	TriggerCEO   Trigger = "ceo"
	TriggerEvent Trigger = "event"
)

// Request is one ask or propose from the assistant.
type Request struct {
	// Kind defaults to KindAsk.
	Kind Kind
	// Trigger defaults to TriggerCEO.
	Trigger Trigger
	// Question is the assistant's question (ask) or instruction (propose)
	// as it put it in the tool call; the CEO's verbatim words are in the
	// transcript.
	Question string
	// Agent is the alias hint, set only when the CEO said a name.
	Agent string
}

// Reply is the answer contract (docs/roles.md): what the assistant says,
// what it may use for follow-ups, and whether the CEO must act.
type Reply struct {
	Spoken    string   `json:"spoken"`
	Detail    string   `json:"detail"`
	NeedsUser bool     `json:"needs_user"`
	Options   []string `json:"options,omitempty"`
	Clarify   bool     `json:"clarify"`
}

// AgentInfo is one row of the roster the Host serves: the state table's
// view of a pane, for the ear (alias, never pane id, in prompts).
type AgentInfo struct {
	PaneID string
	Alias  string
	Kind   string
	State  string
	// StateSince is when State was entered (zero when unknown).
	StateSince time.Time
	// Task is the current instruction; LastTask the one last finished.
	Task     string
	LastTask string
	// LastResult is what the agent last did, spoken form with outcome.
	LastResult string
	// LastBlock is the question the agent last asked (with whether it
	// was answered).
	LastBlock string
	// LastInteraction is when the CEO last dealt with the agent.
	LastInteraction time.Time
	// Pending is the supervisor's answer_prompt for this pane, when one is
	// waiting: the only prompt send_keys may answer.
	Pending *PendingPrompt
}

// PendingPrompt is the recorded approval prompt of a pane.
type PendingPrompt struct {
	ID       string
	Question string
	Options  []Option
	YesKeys  []string
	NoKeys   []string
}

// Option is one selectable choice of a PendingPrompt.
type Option struct {
	Label string
	Keys  []string
}

// AllowedKeys returns every key the prompt's option set permits.
func (p PendingPrompt) AllowedKeys() []string {
	var out []string
	for _, o := range p.Options {
		out = append(out, o.Keys...)
	}
	out = append(out, p.YesKeys...)
	out = append(out, p.NoKeys...)
	return out
}

// Host is what the brain needs from the supervisor: the roster and the
// ledger, the read tools, and the actions (which go through the
// supervisor's existing herdr paths and pending-action bookkeeping).
// Agents are addressed by pane id here; the brain resolves aliases.
type Host interface {
	// Agents returns the roster.
	Agents() []AgentInfo
	// Resolve maps an alias (exact, slug, or unambiguous prefix) to a row.
	Resolve(name string) (AgentInfo, bool)
	// Notes returns the CEO's notes, oldest first.
	Notes() []string
	// ReadScreen returns the last lines of the pane.
	ReadScreen(ctx context.Context, paneID string, lines int) (string, error)
	// RunCLI runs the read-only CLI query tool in the agent's workspace;
	// ErrNoCLI when there is none.
	RunCLI(ctx context.Context, paneID, question string) (string, error)
	// Prompt sends an instruction as typed text, creating the
	// send_instruction pending action exactly as propose does; it returns
	// the spoken echo.
	Prompt(ctx context.Context, paneID, text string) (string, error)
	// AnswerPrompt sends keys to the pane's pending answer_prompt, after
	// re-checking under its own lock that the action is still pending and
	// the keys are in its option set (ErrNotPermitted otherwise), and
	// clears it. It returns the spoken echo.
	AnswerPrompt(ctx context.Context, paneID string, keys []string) (string, error)
	// Interrupt sends Ctrl-C and drops the pane's pending action; it
	// returns the spoken echo.
	Interrupt(ctx context.Context, paneID string) (string, error)
}
