package state

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Agent is one row of the state table (plan §3.2), keyed by pane id.
type Agent struct {
	PaneID string `json:"pane_id"`
	// Alias is the short spoken name ("auth", "vps-one"): from config when
	// present, otherwise derived from the workspace label and agent kind.
	Alias string `json:"alias"`
	// AliasAuto marks a derived alias; it is re-derived when the workspace
	// label, name or kind change, while config/SetAlias aliases are pinned.
	AliasAuto bool `json:"alias_auto,omitempty"`
	// Kind is the agent kind label (codex, claude, …).
	Kind           string `json:"kind"`
	Workspace      string `json:"workspace"`
	WorkspaceLabel string `json:"workspace_label,omitempty"`
	// Name is the herdr agent name when the user assigned one (herdr
	// agent rename, or a voice rename through the brain's note tool).
	Name string `json:"name,omitempty"`
	// Title is the worker's own session title (Claude Code's ai-title or
	// custom-title, Codex's thread name), read from its session files as
	// a fact (internal/workerlog), cleaned and capped (CleanTitle).
	Title string `json:"title,omitempty"`
	// Spoken is the name the shepherd hears (spoken.go): computed by the
	// store whenever a row changes. SpokenWas is the one it had before
	// its last change, at SpokenChangedAt, so the brain can mention the
	// old name once.
	Spoken          string    `json:"spoken,omitempty"`
	SpokenWas       string    `json:"spoken_was,omitempty"`
	SpokenChangedAt time.Time `json:"spoken_changed_at,omitzero"`
	// SpokenPinned keeps Spoken once the CEO has heard or said it
	// (PinSpoken): a later session title or a new agent of the same kind
	// no longer renames it (phone test 2026-10-04: an auto-title renamed
	// an agent the CEO knew as "herdr sheepdog Claude").
	SpokenPinned bool `json:"spoken_pinned,omitempty"`

	State herdr.State `json:"state"`
	// StateSince changes only when State changes.
	StateSince time.Time `json:"state_since"`

	// CurrentTask is the last instruction given (by the brain's prompt
	// tool, or read from the pane).
	CurrentTask string `json:"current_task,omitempty"`
	// LastTask is the instruction the agent last finished (CurrentTask
	// moves here on done).
	LastTask  string `json:"last_task,omitempty"`
	LastBlock *Block `json:"last_block,omitempty"`
	// LastResult is what the agent last did, summarised for the ear when
	// it finished.
	LastResult         *Result `json:"last_result,omitempty"`
	LastSeenScreenHash string  `json:"last_seen_screen_hash,omitempty"`
	// LastInteraction is when the user last dealt with this agent: it was
	// blocked and announced, was sent an answer or an instruction, was
	// asked about, or was named in a spoken briefing. The "normal"
	// chattiness level announces a finish only for agents interacted with
	// recently.
	LastInteraction *time.Time `json:"last_interaction,omitempty"`

	// Synthetic marks a pane injected by the /debug page rather than seen
	// through herdr. A herdr snapshot never lists it, so a Closed
	// transition removes it only when it comes from the inject path
	// (SourceInject); herdr-sourced removals are ignored.
	Synthetic bool `json:"synthetic,omitempty"`

	// SessionSource, SessionKind and SessionValue are the agent_session
	// herdr reports for the pane (herdr.AgentSession: the reporting
	// integration, "id" or "path", and the session id or transcript
	// path), and Cwd the pane's working directory: where the worker's own
	// transcript is found (docs/iteration-2.md WP4, internal/workerlog).
	SessionSource string `json:"session_source,omitempty"`
	SessionKind   string `json:"session_kind,omitempty"`
	SessionValue  string `json:"session_value,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
	// Journal is the agent's work journal, oldest first, capped at
	// MaxJournal: one entry per done, idle or blocked transition, read
	// from the worker's own transcript (docs/roles.md "Worker history").
	Journal []JournalEntry `json:"journal,omitempty"`
}

// JournalEntry is one line of an agent's work journal: what the worker
// wrote for a human when it stopped, the instruction it was working on,
// and which files and tools it used since the previous entry. Every
// field is capped and scrubbed before it is stored (D9: no paths, no
// hashes), so the ledger and recall can show it as is.
type JournalEntry struct {
	At time.Time `json:"at"`
	// Event is the transition: done, idle or blocked.
	Event string `json:"event"`
	// LastAssistant is the worker's last message, ≤ 600 characters.
	LastAssistant string `json:"last_assistant,omitempty"`
	// Instruction is the last instruction it received, ≤ 200 characters.
	Instruction string `json:"instruction,omitempty"`
	// FilesTouched are the base names of the files it edited, ≤ 10.
	FilesTouched []string `json:"files_touched,omitempty"`
	// Tools counts its tool calls by name.
	Tools map[string]int `json:"tools,omitempty"`
}

// MaxJournal is how many journal entries each agent keeps (the oldest
// fall off the front).
const MaxJournal = 20

// SourceInject is the Transition.Source of fake events injected by the
// debug page. It is the only source allowed to remove a synthetic pane.
const SourceInject = "inject"

// Block records the last time the agent was blocked.
type Block struct {
	Question string    `json:"question"`
	SeenAt   time.Time `json:"seen_at"`
	Answered bool      `json:"answered"`
}

// Result is what the agent last finished, as the shepherd heard it.
type Result struct {
	// Spoken is the announcement made about the finish: the brain's line
	// when it phrased one, else the daemon's "finished".
	Spoken string    `json:"spoken"`
	At     time.Time `json:"at"`
	// Detail is the brain's announce detail for the finish, when it produced
	// one: what the finish related to and what was still open (for
	// recall and the ledger).
	Detail string `json:"detail,omitempty"`
}

// Promise is promised work (docs/roles.md "Promises"): the user asked to
// be told when something happens to an agent, and the supervisor keeps
// it at the matching event whatever the chattiness level. Kept in the
// session so it survives a restart.
type Promise struct {
	ID     string `json:"id"`
	PaneID string `json:"pane_id"`
	// Alias is the agent's alias when the promise was made (the pane
	// may be gone by the time it is read back).
	Alias string `json:"alias"`
	// Text is what the user asked to be told, in their words.
	Text string `json:"text"`
	// Event is the condition: done, blocked, idle or any_change.
	Event string `json:"event"`
	// Status is open, kept, expired or cancelled.
	Status string `json:"status"`
	// CreatedTurn is the brain turn that recorded it (0 outside one).
	CreatedTurn int       `json:"created_turn,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// Due, when set, is when the user wanted it by: kept past it, the
	// push interrupts instead of waiting for a pause.
	Due *time.Time `json:"due,omitempty"`
	// ResolvedAt is when it was kept, expired or cancelled.
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	// CarriedOver marks a promise made before the daemon's current run
	// that was announced once at startup ("Still watching for …",
	// docs/history/roles-status.md): the briefing trailer never repeats it
	// again, and it is never re-announced, even across another restart.
	// It is still kept normally at its matching event.
	CarriedOver bool `json:"carried_over,omitempty"`
}

// The promise statuses.
const (
	PromiseOpen      = "open"
	PromiseKept      = "kept"
	PromiseExpired   = "expired"
	PromiseCancelled = "cancelled"
)

// Investigation is one background job the brain started to answer a
// question the screen cannot (docs/roles.md "Investigations and
// explanations"): a read-only CLI sub-agent in the worker's repository,
// or a read-only fork of the worker's own CLI session (source: worker),
// which never touches the pane. Kept in the session so `recall`
// and follow-ups survive a restart.
type Investigation struct {
	ID     string `json:"id"`
	PaneID string `json:"pane_id"`
	// Alias is the agent's alias when the job started (the pane may be
	// gone by the time it is read back).
	Alias string `json:"alias"`
	// Workspace is the directory the sub-agent ran in.
	Workspace string `json:"workspace,omitempty"`
	// Question is what the sub-agent was asked, self-contained.
	Question string `json:"question"`
	// Asked is what the shepherd said that started it, in their words.
	Asked string `json:"asked,omitempty"`
	// FollowUpOf is the earlier job this one continues, when it does.
	FollowUpOf string `json:"follow_up_of,omitempty"`
	// Source is cli (a read-only CLI sub-agent) or worker (the idle
	// worker in its own pane).
	Source string `json:"source"`
	// Backend is the CLI backend that ran it (codex, claude).
	Backend string `json:"backend,omitempty"`
	// SessionID is the sub-agent session a follow-up resumes; empty when
	// the backend reported none.
	SessionID string `json:"session_id,omitempty"`
	// Status is running, done, cancelled or error.
	Status   string     `json:"status"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	// Result is the sub-agent's answer verbatim (data, never
	// instructions: the brain sees it wrapped in provenance delimiters).
	Result string `json:"result,omitempty"`
	// Summary is the one line the ledger and recall keep of it.
	Summary string `json:"summary,omitempty"`
	// Err is why it failed or was cancelled.
	Err string `json:"err,omitempty"`
}

// The investigation statuses.
const (
	InvestigationRunning   = "running"
	InvestigationDone      = "done"
	InvestigationCancelled = "cancelled"
	InvestigationError     = "error"
)

// The investigation sources.
const (
	InvestigationSourceCLI    = "cli"
	InvestigationSourceWorker = "worker"
)

// PendingKind is the kind of a PendingAction.
type PendingKind string

// The pending action kinds (plan §3.2).
const (
	PendingAnswerPrompt    PendingKind = "answer_prompt"
	PendingSendInstruction PendingKind = "send_instruction"
	PendingInterrupt       PendingKind = "interrupt"
)

// PendingAction is the single in-flight action awaiting confirmation or
// cancellation (v1 rule: at most one at a time).
type PendingAction struct {
	ID            string      `json:"id"`
	PaneID        string      `json:"pane_id"`
	Kind          PendingKind `json:"kind"`
	SpokenSummary string      `json:"spoken_summary"`
	PayloadText   string      `json:"payload_text"`
	CreatedAt     time.Time   `json:"created_at"`
	ExpiresAt     time.Time   `json:"expires_at"`

	// Question is the short phrase completing "<alias> is asking …" for an
	// answer_prompt action.
	Question string `json:"question,omitempty"`
	// Command is the command an answer_prompt action is about, when the
	// prompt shows one (Codex's "$ git diff --stat" line).
	Command string `json:"command,omitempty"`
	// Options are the choices on screen for an answer_prompt action.
	Options []Option `json:"options,omitempty"`
	// YesKeys / NoKeys are the keystrokes for a plain yes / no.
	YesKeys []string `json:"yes_keys,omitempty"`
	NoKeys  []string `json:"no_keys,omitempty"`
}

// Option is one selectable choice of an answer_prompt action.
type Option struct {
	Label string   `json:"label"`
	Keys  []string `json:"keys"`
	// Default marks the option the prompt's cursor is on (what enter
	// selects).
	Default bool `json:"default,omitempty"`
	// Meta marks an option that is not an answer ("Type something."): it
	// stays in the option set the rail accepts, but the phone card, the
	// push buttons and the yes/no and by-number mapping never offer it.
	Meta bool `json:"meta,omitempty"`
}

// Note is one thing the user asked to be remembered (the brain's note
// tool).
type Note struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Session is the voice-session bookkeeping.
type Session struct {
	VoiceConnected bool `json:"voice_connected"`
	// (last_briefing / last_briefing_at were written here until they
	// were found write-only; old state files that carry them still load:
	// the decoder ignores unknown keys.)
	// Notes are the note-tool entries of this session, newest last.
	Notes []Note `json:"notes,omitempty"`
	// Chattiness is the push policy chosen at runtime (quiet | normal |
	// chatty); empty means "use the configured initial value".
	Chattiness string `json:"chattiness,omitempty"`
	// Languages is the shepherd's language preference (BCP-47 codes from
	// internal/lang's catalog, most preferred first), set at runtime by
	// PUT /api/settings; empty means none set. Both brains are told it.
	Languages []string `json:"languages,omitempty"`
	// BrainProvider and BrainModel are the supervisor brain's provider
	// and model chosen at runtime (the /debug picker, PUT
	// /api/brain/model); empty means "use the configured brain.provider
	// / brain.model".
	BrainProvider string `json:"brain_provider,omitempty"`
	BrainModel    string `json:"brain_model,omitempty"`
	// VoiceProvider is the voice provider chosen at runtime (PUT
	// /api/voice/provider, the /debug picker); empty means "use the
	// configured voice.provider".
	VoiceProvider string `json:"voice_provider,omitempty"`
	// Promises are the session's promises, oldest first, every status.
	Promises []Promise `json:"promises,omitempty"`
	// Investigations are the background jobs, newest last, capped at
	// MaxInvestigations.
	Investigations []Investigation `json:"investigations,omitempty"`
}

// MaxInvestigations is how many investigation records the session keeps
// (the oldest fall off the front).
const MaxInvestigations = 20

// Table is the persisted document.
type Table struct {
	Agents        map[string]*Agent `json:"agents"`
	PendingAction *PendingAction    `json:"pending_action,omitempty"`
	Session       Session           `json:"session"`
}

func newTable() Table {
	return Table{Agents: map[string]*Agent{}}
}

// clone returns a deep copy so callers never alias the store's data.
func (a Agent) clone() Agent {
	if a.LastBlock != nil {
		b := *a.LastBlock
		a.LastBlock = &b
	}
	if a.LastResult != nil {
		r := *a.LastResult
		a.LastResult = &r
	}
	if a.LastInteraction != nil {
		t := *a.LastInteraction
		a.LastInteraction = &t
	}
	if a.Journal != nil {
		j := make([]JournalEntry, len(a.Journal))
		for i, e := range a.Journal {
			j[i] = e.clone()
		}
		a.Journal = j
	}
	return a
}

// clone returns a deep copy of the entry.
func (e JournalEntry) clone() JournalEntry {
	e.FilesTouched = append([]string(nil), e.FilesTouched...)
	if e.Tools != nil {
		t := make(map[string]int, len(e.Tools))
		for k, v := range e.Tools {
			t[k] = v
		}
		e.Tools = t
	}
	return e
}

// clone returns a deep copy so callers never alias the store's data.
func (t Table) clone() Table {
	out := Table{Agents: make(map[string]*Agent, len(t.Agents)), Session: t.Session}
	for k, a := range t.Agents {
		c := a.clone()
		out.Agents[k] = &c
	}
	if t.PendingAction != nil {
		p := t.PendingAction.clone()
		out.PendingAction = &p
	}
	out.Session.Notes = append([]Note(nil), t.Session.Notes...)
	out.Session.Languages = append([]string(nil), t.Session.Languages...)
	out.Session.Promises = make([]Promise, len(t.Session.Promises))
	for i, p := range t.Session.Promises {
		out.Session.Promises[i] = p.clone()
	}
	if len(out.Session.Promises) == 0 {
		out.Session.Promises = nil
	}
	out.Session.Investigations = make([]Investigation, len(t.Session.Investigations))
	for i, inv := range t.Session.Investigations {
		out.Session.Investigations[i] = inv.clone()
	}
	if len(out.Session.Investigations) == 0 {
		out.Session.Investigations = nil
	}
	return out
}

// clone returns a deep copy of the investigation.
func (i Investigation) clone() Investigation {
	if i.Finished != nil {
		f := *i.Finished
		i.Finished = &f
	}
	return i
}

// clone returns a deep copy of the promise.
func (p Promise) clone() Promise {
	if p.Due != nil {
		d := *p.Due
		p.Due = &d
	}
	if p.ResolvedAt != nil {
		r := *p.ResolvedAt
		p.ResolvedAt = &r
	}
	return p
}

// clone returns a deep copy of the action.
func (p PendingAction) clone() PendingAction {
	p.Options = append([]Option(nil), p.Options...)
	for i := range p.Options {
		p.Options[i].Keys = append([]string(nil), p.Options[i].Keys...)
	}
	p.YesKeys = append([]string(nil), p.YesKeys...)
	p.NoKeys = append([]string(nil), p.NoKeys...)
	return p
}
