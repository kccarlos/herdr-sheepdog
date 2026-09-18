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
	// ErrYielded (wrapped in ErrFallback) is returned by an event turn
	// that stopped before a model call because a CEO turn was waiting
	// for the lock; the supervisor speaks its template sentence instead.
	ErrYielded = errors.New("brain: yielded to a CEO turn")
	// ErrCoalesced (wrapped in ErrFallback) is returned by a queued
	// event turn that a newer event turn about the same agent replaced
	// before it ran.
	ErrCoalesced = errors.New("brain: coalesced into a newer event turn")
)

// Kind of a turn: what the assistant asked for.
type Kind string

// The turn kinds. Ask and propose are CEO turns; announce is the event
// turn the supervisor runs on a worker's block or finish (docs/roles.md
// "Push judgment").
const (
	KindAsk     Kind = "ask"
	KindPropose Kind = "propose"
	// KindAnnounce decides what the assistant says about a worker event
	// and how urgently: one line that replaces the template sentence
	// (and keeps the promises the event fulfils), or an addition when
	// the template was already spoken (a question is never delayed).
	// The reply's Urgency may be skip or silent; an empty Spoken is a
	// skip. The chattiness ceiling is applied by the supervisor.
	KindAnnounce Kind = "announce"
)

// IsEvent reports whether k is an event-triggered kind: read-only tools,
// the enrich timeout, and an empty Spoken allowed.
func (k Kind) IsEvent() bool { return k == KindAnnounce }

// The urgencies an announce reply may ask for (Reply.Urgency); the
// supervisor caps them under the chattiness ceiling.
const (
	UrgencyInterrupt = "interrupt"
	UrgencyIdle      = "idle"
	UrgencySilent    = "silent"
	UrgencySkip      = "skip"
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
	// Agent is the alias hint, set only when the CEO said a name; for an
	// event turn, the alias the event is about.
	Agent string
	// Event is the worker event of an event turn (done, blocked).
	Event string
	// Template is the template path's sentence for the event: what the
	// brain's Spoken replaces, or (TemplateSpoken) what was already said.
	Template string
	// TemplateSpoken reports that the supervisor already pushed Template
	// before the turn (a block: a question is never delayed), so Spoken
	// is an addition, not a replacement.
	TemplateSpoken bool
	// Screen is the pane read the supervisor already took for the event;
	// it is put in the window (wrapped as worker output, pruned like a
	// tool result) so the turn need not read again.
	Screen string
	// Promises are the promises this event keeps (already marked kept by
	// the supervisor): the CEO asked to be told, so Spoken says so.
	Promises []PromiseInfo
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
	// announce turn found one ("the rerun you asked for").
	RelatesTo string `json:"relates_to,omitempty"`
	// Urgency is what an announce turn asks for: interrupt, idle, silent
	// or skip (see the Urgency constants); "" means idle when Spoken is
	// set and skip when it is not.
	Urgency string `json:"urgency,omitempty"`
	// KeepsPromiseIDs lists the ids of the request's promises the
	// announce turn's Spoken keeps (informational: the supervisor keeps
	// every matching promise regardless).
	KeepsPromiseIDs []string `json:"keeps_promise_ids,omitempty"`
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
