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
	// Name is the herdr agent name when the user assigned one.
	Name string `json:"name,omitempty"`

	State herdr.State `json:"state"`
	// StateSince changes only when State changes.
	StateSince time.Time `json:"state_since"`

	// CurrentTask is the last instruction given (by the user via propose,
	// or read from the pane).
	CurrentTask string `json:"current_task,omitempty"`
	// LastTask is the instruction the agent last finished (CurrentTask
	// moves here on done).
	LastTask  string `json:"last_task,omitempty"`
	LastBlock *Block `json:"last_block,omitempty"`
	// LastResult is what the agent last did, summarised for the ear when
	// it finished (the summarize_done pass).
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
}

// SourceInject is the Transition.Source of fake events injected by the
// debug page. It is the only source allowed to remove a synthetic pane.
const SourceInject = "inject"

// Block records the last time the agent was blocked.
type Block struct {
	Question string    `json:"question"`
	SeenAt   time.Time `json:"seen_at"`
	Answered bool      `json:"answered"`
}

// Result is the summary of an agent's finished turn.
type Result struct {
	// Spoken is the past-tense phrase completing "<alias> finished: …".
	Spoken string `json:"spoken"`
	// Outcome is success, failed, partial or unknown.
	Outcome string    `json:"outcome"`
	At      time.Time `json:"at"`
}

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
}

// Note is one thing the user asked to be remembered (the note tool).
type Note struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Session is the voice-session bookkeeping.
type Session struct {
	VoiceConnected bool       `json:"voice_connected"`
	LastBriefing   string     `json:"last_briefing,omitempty"`
	LastBriefingAt *time.Time `json:"last_briefing_at,omitempty"`
	// Notes are the note-tool entries of this session, newest last.
	Notes []Note `json:"notes,omitempty"`
	// Chattiness is the push policy chosen at runtime (quiet | normal |
	// chatty); empty means "use the configured initial value".
	Chattiness string `json:"chattiness,omitempty"`
}

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
	return a
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
	return out
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
