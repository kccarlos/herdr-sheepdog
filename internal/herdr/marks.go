package herdr

import "context"

// Marks is the seen and pinned state of one terminal: what a person has
// looked at, and what they pinned. The marks belong to the terminal, not to
// the agent in it, so they outlive an agent that exits and a new one
// started in the same shell inherits them.
type Marks struct {
	// Turn counts the finished turns of the terminal: hc's own turn
	// counter, or the old herdr's completion_seq. It is not
	// AgentInfo.StateChangeSeq, which goes up on every state change.
	Turn uint64
	// Seen is the last turn a person looked at; nil means no record.
	Seen *uint64
	// Pinned sorts the agent first in its group.
	Pinned bool
	// Word is the backend's own word for the agent. With no Seen record, a
	// done agent is unread and an idle one is read.
	Word State
	// Error is set when the agent reported the status word "error", which
	// counts as needing a person.
	Error bool
	// Shared is true when the marks are hc labels every Herdr client sees,
	// false when they live in sheepdog's own store.
	Shared bool
}

// Unread reports whether a turn finished that nobody has looked at. It does
// not look at the agent's current state: a working or blocked agent is
// never unread (attention.Classify decides that).
func (m Marks) Unread() bool {
	if m.Seen != nil {
		return m.Turn > *m.Seen
	}
	return m.Word == StateDone
}

// SeenWrite is the seen mark that MarkSeen(turn) writes, and whether it
// writes at all. Seen only goes up, and never above the turn the terminal
// is at: a turn that ended while the client was looking is not covered by
// what the client showed. A terminal with no record takes any turn, so it
// joins the others.
func (m Marks) SeenWrite(turn uint64) (seen uint64, write bool) {
	seen = min(turn, m.Turn)
	if m.Seen != nil && seen <= *m.Seen {
		return 0, false
	}
	return seen, true
}

// UnreadWrite is the seen mark that MarkUnread writes, and whether it
// writes at all: one turn short of the current turn, nothing at turn 0 (no
// turn has finished yet) and nothing when the terminal already reads as
// unread.
func (m Marks) UnreadWrite() (seen uint64, write bool) {
	if m.Turn == 0 || m.Unread() {
		return 0, false
	}
	return m.Turn - 1, true
}

// Marker reads and writes the marks of every terminal. hc keeps them in
// terminal labels, which every Herdr client shares; a backend without any
// uses attention.Local.
//
// Panes are addressed by pane id. A pane the marker does not track gives an
// error matching ErrPaneNotFound.
type Marker interface {
	// AllMarks returns the marks of every tracked terminal, by pane id.
	AllMarks(ctx context.Context) (map[string]Marks, error)
	// MarkSeen records that a person looked at the terminal when it was at
	// turn. It only raises the mark, and never above the terminal's current
	// turn; a lower or equal turn changes nothing. The result is the marks
	// afterwards.
	MarkSeen(ctx context.Context, paneID string, turn uint64) (Marks, error)
	// MarkUnread lowers seen to one turn short of the current turn. It is
	// the one write that lowers the mark, and does nothing at turn 0.
	MarkUnread(ctx context.Context, paneID string) (Marks, error)
	// SetPinned pins or unpins the terminal.
	SetPinned(ctx context.Context, paneID string, pinned bool) (Marks, error)
	// MarkChanges reports pane ids whose marks changed outside these calls
	// (another client wrote a label, or a turn ended). A receiver that
	// falls behind loses signals, so it reads AllMarks again.
	MarkChanges() <-chan string
}
