package brain

import (
	"context"
	"errors"
	"time"
)

// Errors callers branch on.
var (
	// ErrFallback is returned by a turn that could not produce a reply
	// (model error, budget exceeded, no model); the supervisor then
	// speaks its own code sentence, or reports the roster.
	ErrFallback = errors.New("brain: fallback")
	// ErrNotPermitted is the rail refusing a send_keys; the Host returns
	// it (wrapped) when the pending action does not match at send time.
	ErrNotPermitted = errors.New("not permitted")
	// ErrNoCLI is returned by Host.RunCLI when no CLI query tool exists.
	ErrNoCLI = errors.New("no CLI query tool is available")
	// ErrNoHost is returned by a turn before SetHost was called.
	ErrNoHost = errors.New("brain: no host")
	// ErrClosed is returned by a turn after Close.
	ErrClosed = errors.New("brain: closed")
	// ErrYielded (wrapped in ErrFallback) is returned by an event turn
	// that stopped before a model call because a CEO turn was waiting
	// for the lock; the supervisor speaks its code sentence instead.
	ErrYielded = errors.New("brain: yielded to a CEO turn")
	// ErrCoalesced (wrapped in ErrFallback) is returned by a queued
	// event turn that a newer event turn about the same agent replaced
	// before it ran.
	ErrCoalesced = errors.New("brain: coalesced into a newer event turn")
	// ErrUnknownProvider is returned by SetModel and Models for a
	// provider id nothing was registered under.
	ErrUnknownProvider = errors.New("brain: unknown provider")
)

// Kind of a turn: what the assistant asked for.
type Kind string

// The turn kinds. Utterance is the CEO turn of D1-revised: the voice
// relays what the CEO said verbatim and the brain decides what it is
// (docs/roles.md "One decision-maker"). (The older classified CEO
// turns, ask and propose, are gone; their request lines still appear in
// transcripts written before, and /debug still reads them.)
// Announce is the event turn the supervisor runs on a worker's block or
// finish (docs/roles.md "Push judgment"). (The stop procedure's closing
// turn, kind stop, is gone too: the daemon acknowledges a stop with its
// own sentence and records it as a context line, Brain.Daemon.)
const (
	// KindUtterance carries the CEO's words verbatim in Question, with
	// no agent hint: the brain resolves intent, target and action.
	KindUtterance Kind = "utterance"
	// KindAnnounce decides what the assistant says about a worker event
	// and how urgently: the one line for that event, which replaces the
	// daemon's own sentence and keeps the promises the event fulfils.
	// The reply's Urgency may be skip or silent; an empty Spoken is a
	// skip. The chattiness ceiling is applied by the supervisor.
	KindAnnounce Kind = "announce"
	// KindInvestigation delivers a finished investigation: the job's
	// question, the shepherd's own words and the sub-agent's answer come
	// back as an event turn that composes the explanation (docs/roles.md
	// "Investigations and explanations"). Read tools only; the shepherd
	// is expecting it, so its line is pushed at the next pause.
	KindInvestigation Kind = "investigation"
)

// IsEvent reports whether k is an event-triggered kind: read-only tools,
// an event budget, and an empty Spoken allowed.
func (k Kind) IsEvent() bool { return k == KindAnnounce || k == KindInvestigation }

// ReadOnly reports whether k's turn is declared (and allowed) the read
// tools only: an event turn.
func (k Kind) ReadOnly() bool { return k.IsEvent() }

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

// Request is one turn's request: the shepherd's relayed words or a
// worker event.
type Request struct {
	// Kind defaults to KindUtterance.
	Kind Kind
	// Trigger defaults to TriggerCEO.
	Trigger Trigger
	// Question is the CEO's words verbatim (utterance).
	Question string
	// Agent is the alias hint, set only when the CEO said a name (never
	// for an utterance: the brain resolves the target itself); for an
	// event turn, the alias the event is about.
	Agent string
	// Briefing is the supervisor's roster sentence (composeBriefing) for
	// a CEO turn, so a status question needs no tool call.
	Briefing string
	// RosterUnchangedFor is set (a KindUtterance turn only) when Briefing
	// is byte-identical to the one last delivered, within
	// supervisor.repeat_window_s: requestText adds a note so the brain
	// can say "no change since I last checked" itself rather than
	// repeat the roster (docs/history/issues-2026-09-18.md "Smaller items").
	RosterUnchangedFor time.Duration
	// Event is the worker event of an event turn (done, blocked).
	Event string
	// Template is the sentence the daemon speaks for the event when the
	// brain does not answer in time: what the brain's Spoken replaces.
	Template string
	// Result is a finished investigation's answer, put in the window
	// wrapped as worker output by a KindInvestigation turn.
	Result string
	// Screen is the pane read the supervisor already took for the event;
	// it is put in the window (wrapped as worker output, pruned like a
	// tool result) so the turn need not read again.
	Screen string
	// Promises are the promises this event keeps (already marked kept by
	// the supervisor): the CEO asked to be told, so Spoken says so.
	Promises []PromiseInfo
	// Owed marks an announce turn the shepherd is waiting on: the event
	// keeps a promise or finishes an instruction the shepherd gave this
	// run. It gets Config.AnnounceTimeout instead of AnnounceTurnTimeout:
	// the finish waits for the brain's line, so that line (not a code
	// sentence first) is what the shepherd hears.
	Owed bool
	// AlsoAsking is set on a finish whose screen ends at a prompt the
	// agent showed after its turn (Claude Code's "teach auto mode…?"):
	// the question as parsed, so the one line covers the result and the
	// question both.
	AlsoAsking string
	// Investigation is the finished job a KindInvestigation turn
	// delivers: its question, the CEO's own words, the sub-agent's
	// answer and what it follows up on.
	Investigation *InvestigationInfo
	// UtteranceID names the CEO utterance an utterance turn answers
	// (the supervisor mints it at the relay): every event of the turn
	// carries it (trace.go).
	UtteranceID string
	// Link is the request message's link to the voice context (the
	// Context tab): "call:<id>" for the voice tool call that relayed an
	// utterance, "evt:<id>" for the worker event an announce is about.
	Link string
	// Facts, when set, is what the daemon knows now and the brain cannot
	// see: about the event of an announce turn (docs/bench-2026-10-03.md
	// #15b), or, on an utterance turn, what the CEO had heard when they
	// began speaking (Heard): called when the turn starts, after the turn lock — the
	// template line may have gone out while the turn was queued — and
	// again before each later step, when a change is put in the window.
	Facts func() EventFacts

	// facts is Facts as the request was rendered.
	facts EventFacts
}

// EventFacts is what has happened to an announced event outside the
// brain's turn.
type EventFacts struct {
	// Spoken are the lines already said about this event, word for word
	// and in order (the daemon's template sentence spoken at
	// brain.announce_wait_s while the brain was still writing, say).
	Spoken []string
	// Prompt is the state of the approval prompt a block created:
	// PromptWaiting, PromptAnswered or PromptGone; "" when none.
	Prompt string
	// Heard is set on an utterance turn: when the CEO began saying the
	// words and the spoken lines the voice was given around then
	// (docs/eval-campaign-2026-10-03.md H6: a "yes" to one question was
	// taken as the answer to a question pushed after the CEO spoke).
	Heard *HeardFacts
}

// HeardFacts is what the CEO had and had not heard when they began
// speaking.
type HeardFacts struct {
	// SpokeAt is when their first words of this utterance reached the
	// daemon.
	SpokeAt time.Time
	// Before is the last spoken line the voice was given before SpokeAt
	// (nil when none is remembered); After are the lines it was given
	// since, which the CEO had not heard when they began.
	Before *PushedLine
	After  []PushedLine
}

// PushedLine is one spoken line handed to the voice.
type PushedLine struct {
	Text string
	// At is when the voice was given it (sent, not queued).
	At time.Time
	// Unheard is set when the voice layer judged it did not reach the
	// CEO (voice.push_unheard).
	Unheard bool
}

// The states of EventFacts.Prompt.
const (
	PromptWaiting  = "waiting"
	PromptAnswered = "answered"
	PromptGone     = "gone"
)

// Reply is the answer contract (docs/roles.md): what the assistant says,
// what it may use for follow-ups, and whether the CEO must act.
//
// The ear contract: Spoken is the headline (short by default, longer when
// the CEO asked for detail, no dashes; the supervisor cuts only a runaway,
// at prose.EarRunaway, and moves the overflow into Detail); Ask is the one question when
// there is a decision only the CEO can make, read as the last sentence;
// Detail is the plain explanation of every term the headline used, in
// everyday words, which the voice reads when the CEO asks "what does
// that mean". An old-shape reply without Ask still parses.
type Reply struct {
	Spoken    string   `json:"spoken"`
	Ask       string   `json:"ask,omitempty"`
	Detail    string   `json:"detail"`
	NeedsUser bool     `json:"needs_user"`
	Options   []string `json:"options,omitempty"`
	Clarify   bool     `json:"clarify"`
	// Pending marks an answer that is not the final one: the brain said
	// how long it will take and the result follows on its own (the
	// investigations work of docs/roles.md). Plumbed through to the
	// voice's tool result; nothing produces it yet.
	Pending bool `json:"pending,omitempty"`
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
	// Turn is the turn that produced the reply, filled in by the brain
	// and never by the model: the supervisor names it when it drops a
	// line that came too late (brain.push_late).
	Turn int `json:"-"`
	// Acted is set by the brain, never the model, when the turn sent
	// something to a worker or recorded something (prompt, send_keys,
	// interrupt, investigate, promise, note): the supervisor keeps a late
	// utterance reply that acted as silent context rather than dropping it
	// (internal/supervisor/utterance.go).
	Acted bool `json:"-"`
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
	// CarriedOver marks a promise made before the daemon's current run,
	// announced once at startup: the ledger section still lists it
	// (renderLedger notes "from an earlier run"), but the briefing
	// trailer does not repeat it.
	CarriedOver bool `json:"carried_over,omitempty"`
}

// Investigation statuses (state.Investigation mirrors them; the brain
// sees jobs only through the Host).
const (
	InvestigationRunning   = "running"
	InvestigationDone      = "done"
	InvestigationCancelled = "cancelled"
	InvestigationError     = "error"
)

// The sources an investigation can run through.
const (
	// InvestigationSourceCLI is the read-only CLI sub-agent in the
	// worker's repository.
	InvestigationSourceCLI = "cli"
	// InvestigationSourceWorker is the worker itself: its own session,
	// asked through a read-only fork that never writes into the session
	// or touches the pane (docs/dive-deep.md §3).
	InvestigationSourceWorker = "worker"
)

// InvestigationRequest is what the investigate tool asks the Host to
// start: a question about an agent, optionally continuing an earlier
// job's session.
type InvestigationRequest struct {
	// Question is what the sub-agent is asked, self-contained (it has
	// not heard this conversation).
	Question string
	// FollowUpOf is the id of the job to continue, when the CEO is
	// digging deeper into an answer they already had.
	FollowUpOf string
	// Source is who is asked: InvestigationSourceCLI ("" too) or
	// InvestigationSourceWorker.
	Source string
	// Asked is what the CEO said that started it, in their words (the
	// delivery turn says what they wanted, not what the tool asked).
	Asked string
	// Turn is the brain turn that started it.
	Turn int
}

// InvestigationStart is what the investigate tool returns at once: the
// job is running in the background and its answer arrives later as an
// event turn.
type InvestigationStart struct {
	// Job is the job's id, which a follow-up names.
	Job string `json:"job"`
	// ETASeconds is a rough estimate from the backend's recorded
	// durations, for the acknowledgement ("about half a minute").
	ETASeconds int `json:"eta_s"`
}

// InvestigationInfo is one job of the ledger as the Host records it.
type InvestigationInfo struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	// Question is what the sub-agent was asked; Asked what the CEO said.
	Question string `json:"question"`
	Asked    string `json:"asked,omitempty"`
	// FollowUpOf is the earlier job this one continues.
	FollowUpOf string `json:"follow_up_of,omitempty"`
	// Source is cli or worker; Backend the CLI backend that ran it.
	Source  string `json:"source"`
	Backend string `json:"backend,omitempty"`
	// Status is running, done, cancelled or error.
	Status   string     `json:"status"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	// Result is the sub-agent's answer verbatim (wrapped in provenance
	// delimiters wherever it enters the window); Summary the one line
	// the ledger and recall keep.
	Result  string `json:"result,omitempty"`
	Summary string `json:"summary,omitempty"`
	Err     string `json:"err,omitempty"`
	// Resumable reports that a follow-up can continue this job's
	// sub-agent session (it reported one and the TTL has not passed).
	Resumable bool `json:"resumable,omitempty"`
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
	// LastDetail is the announce detail stored with the last result
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
	// Journal is the agent's work journal, oldest first: what the worker
	// wrote at each finish or block, read from its own transcript
	// (docs/roles.md "Worker history").
	Journal []JournalEntry
}

// SessionRead is what Host.ReadSession found at the end of a worker's
// own session transcript (internal/workerlog, docs/dive-deep.md WP8a):
// the worker's last message for a human, whole; the last instruction it
// received; and what it did for that instruction. The executor caps and
// wraps it as worker output.
type SessionRead struct {
	LastAssistant string
	Instruction   string
	// Files are the base names of the files it edited; Tools counts its
	// tool calls by name.
	Files []string
	Tools map[string]int
}

// JournalEntry is one work journal entry (state.JournalEntry): capped
// and D9-scrubbed by the supervisor before the brain sees it.
type JournalEntry struct {
	At time.Time
	// Event is done, idle or blocked.
	Event string
	// LastAssistant is what the worker last wrote for a human.
	LastAssistant string
	// Instruction is the last instruction it received.
	Instruction string
	// Files are the base names of files it edited; Tools its tool calls
	// by name.
	Files []string
	Tools map[string]int
}

// PendingPrompt is the recorded approval prompt of a pane.
type PendingPrompt struct {
	ID       string
	Question string
	// AskedAt is when the prompt was announced (the pending action's
	// creation): the roster says how long it has waited, a fact the
	// brain weighs when the CEO's words may or may not answer it
	// (docs/bench-2026-10-03.md #8).
	AskedAt time.Time
	Options []Option
	YesKeys []string
	NoKeys  []string
}

// Option is one selectable choice of a PendingPrompt.
type Option struct {
	Label string
	Keys  []string
	// Meta marks an entry that is not an answer (Claude Code's "Type
	// something." free-text box): the roster says so, so the brain knows
	// it exists for a free-text answer and never offers it as a choice
	// (docs/eval-campaign-2026-10-03.md H2).
	Meta bool
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

// SelectedBy reports whether keys, as a whole sequence, is exactly one
// of the prompt's options (or its yes/no shorthand). AllowedKeys alone
// is not enough to validate a call: "y" and "enter" can each belong to
// a different option, and sending both could answer this prompt and
// then confirm whatever the pane asks next (docs/roles.md "the one
// rail").
func (p PendingPrompt) SelectedBy(keys []string) bool {
	same := func(a []string) bool {
		if len(a) != len(keys) {
			return false
		}
		for i := range a {
			if a[i] != keys[i] {
				return false
			}
		}
		return true
	}
	if same(p.YesKeys) || same(p.NoKeys) {
		return true
	}
	for _, o := range p.Options {
		if same(o.Keys) {
			return true
		}
	}
	return false
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
	// Note records something the CEO wants remembered ("don't touch the
	// migrations today"); it returns the spoken echo.
	Note(text string) (string, error)
	// ReadScreen returns the last lines of the pane.
	ReadScreen(ctx context.Context, paneID string, lines int) (string, error)
	// ReadSession reads the end of the pane's worker session transcript
	// without moving the journal's offset. A session that cannot be read
	// returns an error naming the worker.session_missing reason.
	ReadSession(ctx context.Context, paneID string) (SessionRead, error)
	// RunCLI runs the read-only CLI query tool in the agent's workspace;
	// ErrNoCLI when there is none.
	RunCLI(ctx context.Context, paneID, question string) (string, error)
	// Investigate starts a background job about the pane and returns at
	// once; its answer comes back later as a KindInvestigation turn.
	// ErrNoCLI when there is no query tool to run it with.
	Investigate(ctx context.Context, paneID string, req InvestigationRequest) (InvestigationStart, error)
	// Investigations returns the job ledger, oldest first, running jobs
	// included.
	Investigations() []InvestigationInfo
	// Prompt sends an instruction as typed text, creating the
	// send_instruction pending action (echo, send, cancellable); it returns
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
