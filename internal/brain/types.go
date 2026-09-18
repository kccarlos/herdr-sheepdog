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

// The turn kinds. Ask and propose are CEO turns; summarize and
// keep_promise are event turns the supervisor runs after a worker's
// blocked/done announcement (docs/roles.md "Delivery" 2).
const (
	KindAsk     Kind = "ask"
	KindPropose Kind = "propose"
	// KindSummarize enriches a template announcement with context: what
	// the CEO asked for, what is still open. An empty Spoken means the
	// template sentence was sufficient.
	KindSummarize Kind = "summarize"
	// KindKeepPromise turns a kept promise plus the fresh template
	// sentence into one spoken line.
	KindKeepPromise Kind = "keep_promise"
)

// IsEvent reports whether k is an event-triggered kind: read-only tools,
// the enrich timeout, and an empty Spoken allowed.
func (k Kind) IsEvent() bool { return k == KindSummarize || k == KindKeepPromise }

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
	// Agent is the alias hint, set only when the CEO said a name; for an
	// event turn, the alias the event is about.
	Agent string
	// Event is the worker event of an event turn (done, blocked).
	Event string
	// Template is the sentence the template path already pushed for the
	// event (the brain adds to it, never repeats it).
	Template string
	// Screen is the pane read the supervisor already took for the event;
	// it is put in the window (wrapped as worker output, pruned like a
	// tool result) so the turn need not read again.
	Screen string
	// Promise is the promise a keep_promise turn keeps.
	Promise *PromiseInfo
}

// Reply is the answer contract (docs/roles.md): what the assistant says,
// what it may use for follow-ups, and whether the CEO must act.
type Reply struct {
	Spoken    string   `json:"spoken"`
	Detail    string   `json:"detail"`
	NeedsUser bool     `json:"needs_user"`
	Options   []string `json:"options,omitempty"`
	Clarify   bool     `json:"clarify"`
	// RelatesTo names the CEO's request the event fulfils, when an
	// enrichment found one ("the rerun you asked for").
	RelatesTo string `json:"relates_to,omitempty"`
}

// Promise events and statuses (state.Promise mirrors them; the brain
// sees promises only through the Host).
const (
	PromiseEventDone      = "done"
	PromiseEventBlocked   = "blocked"
	PromiseEventIdle      = "idle"
	PromiseEventAnyChange = "any_change"

	PromiseOpen      = "open"
	PromiseKept      = "kept"
	PromiseExpired   = "expired"
	PromiseCancelled = "cancelled"
)

// PromiseEvents lists the accepted promise conditions.
var PromiseEvents = []string{PromiseEventDone, PromiseEventBlocked, PromiseEventIdle, PromiseEventAnyChange}

// PromiseRequest is what the promise tool asks the Host to record.
type PromiseRequest struct {
	// Text is what the CEO asked to be told, in their words.
	Text string
	// Event is the condition (one of PromiseEvents).
	Event string
	// Due, when set, is when the CEO wants it by (now when they asked to
	// be interrupted at once); kept past it, the push interrupts.
	Due *time.Time
	// Turn is the brain turn making the promise.
	Turn int
}

// PromiseInfo is one promised piece of work as the Host records it: the
// CEO's words, the agent and worker event it waits for, and its status.
type PromiseInfo struct {
	ID string `json:"id"`
	// Text is what the CEO asked to be told, in their words.
	Text string `json:"text"`
	// Agent is the alias the promise is about.
	Agent string `json:"agent"`
	// Event is the condition: done, blocked, idle or any_change.
	Event string `json:"event"`
	// Status is open, kept, expired or cancelled.
	Status      string    `json:"status"`
	CreatedTurn int       `json:"created_turn,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// Due, when set, is when the CEO wanted it by: a promise kept past
	// it interrupts.
	Due *time.Time `json:"due,omitempty"`
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
	// LastDetail is the enrichment detail stored with the last result
	// (what it related to, what was still open), when there is one.
	LastDetail string
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
	// Promise records promised work: tell the CEO req.Text when the
	// pane's next event of kind req.Event happens (docs/roles.md
	// "Promises"). It returns the record (with its id) and is only
	// called in CEO turns.
	Promise(ctx context.Context, paneID string, req PromiseRequest) (PromiseInfo, error)
	// Promises returns every promise of the session, open ones first.
	Promises() []PromiseInfo
}
