package attention

import (
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Group is the section an agent shows under in the agent list.
type Group string

// The groups, in the order the list shows them.
const (
	GroupNeedsYou   Group = "needs_you"
	GroupWorking    Group = "working"
	GroupDone       Group = "done"
	GroupNotRunning Group = "not_running"
)

// rank is a group's place in the list.
func (g Group) rank() int {
	switch g {
	case GroupNeedsYou:
		return 0
	case GroupWorking:
		return 1
	case GroupDone:
		return 2
	}
	return 3
}

// Attention is what the list needs to know about one agent beyond its
// state: which group it is in, whether a finished turn is waiting for a
// person, and whether it is pinned.
type Attention struct {
	Group  Group
	Unread bool
	Pinned bool
}

// Classify places an agent from its state and its terminal's marks. It is
// pure: the phone, the panel and the voice must agree on its answers, and a
// shared fixture keeps their implementations in step.
//
// Only an idle or done agent can be unread. A blocked or working one is
// never unread, whatever the marks say, and is unread again once it is idle.
func Classify(state herdr.State, m herdr.Marks) Attention {
	a := Attention{Pinned: m.Pinned}
	switch {
	case state == herdr.StateBlocked || m.Error:
		a.Group = GroupNeedsYou
	case state == herdr.StateWorking:
		a.Group = GroupWorking
	case state == herdr.StateIdle || state == herdr.StateDone:
		a.Group = GroupDone
		a.Unread = m.Unread()
	default:
		a.Group = GroupNotRunning
	}
	return a
}

// Item is one row to order: an agent's spoken name and its attention.
type Item struct {
	Name string
	Attention
}

// Compare orders two rows for the agent list: by group, then pinned first,
// then unread first within Done, then by name without regard to case. There
// is no time sort, so rows never jump while a person looks at them. It
// returns zero for rows that tie on all of these.
func Compare(a, b Item) int {
	if c := a.Group.rank() - b.Group.rank(); c != 0 {
		return c
	}
	if a.Pinned != b.Pinned {
		return flag(b.Pinned) - flag(a.Pinned)
	}
	if a.Group == GroupDone && a.Unread != b.Unread {
		return flag(b.Unread) - flag(a.Unread)
	}
	return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
}

// Sort orders items by Compare, keeping the given order among ties.
func Sort(items []Item) {
	slices.SortStableFunc(items, Compare)
}

func flag(b bool) int {
	if b {
		return 1
	}
	return 0
}
