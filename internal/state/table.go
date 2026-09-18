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
	CurrentTask        string `json:"current_task,omitempty"`
	LastBlock          *Block `json:"last_block,omitempty"`
	LastSeenScreenHash string `json:"last_seen_screen_hash,omitempty"`
}

// Block records the last time the agent was blocked.
type Block struct {
	Question string    `json:"question"`
	SeenAt   time.Time `json:"seen_at"`
	Answered bool      `json:"answered"`
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
}

// Session is the voice-session bookkeeping.
type Session struct {
	VoiceConnected bool       `json:"voice_connected"`
	LastBriefing   string     `json:"last_briefing,omitempty"`
	LastBriefingAt *time.Time `json:"last_briefing_at,omitempty"`
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
func (t Table) clone() Table {
	out := Table{Agents: make(map[string]*Agent, len(t.Agents)), Session: t.Session}
	for k, a := range t.Agents {
		c := *a
		if a.LastBlock != nil {
			b := *a.LastBlock
			c.LastBlock = &b
		}
		out.Agents[k] = &c
	}
	if t.PendingAction != nil {
		p := *t.PendingAction
		out.PendingAction = &p
	}
	return out
}
