package supervisor

import (
	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/push"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Notifier sends a phone notification (internal/push.Sender). Notify
// must not block: the sender works in the background and dedupes by the
// notification's event id.
type Notifier interface {
	Notify(push.Notification)
}

// notify hands the phone the line an announcement speaks, when the event
// needs the shepherd: a block (with or without an answerable prompt) or a
// promise they asked for. Plain finishes and status chatter are the
// voice's alone. It is called at each place an announcement's line is
// decided (codePath, applyResult, dropLate) — before the chattiness gate
// and the voice session, so a closed app still hears of a blocked agent —
// and the sender's dedupe by event id keeps it to one notification per
// event (dropLate's later line about an event already notified is
// dropped there).
func (s *Supervisor) notify(a announcement, kept []state.Promise, text string) {
	if s.cfg.Notifier == nil || text == "" {
		return
	}
	blocked := a.event == brain.PromiseEventBlocked || a.pending != ""
	if !blocked && len(kept) == 0 {
		return
	}
	n := push.Notification{EventID: a.eventID, PaneID: a.row.PaneID, Agent: a.row.Alias, Text: text, NeedsYou: blocked}
	switch {
	case a.pending != "":
		n.Kind = push.KindApproval
		n.PendingID = a.pending
		if p, ok := s.store.Pending(); ok && p.ID == a.pending {
			for i, o := range p.Options {
				n.Options = append(n.Options, push.Option{Index: i + 1, Label: o.Label})
			}
		}
	case a.event == brain.PromiseEventBlocked:
		n.Kind = push.KindBlocked
	default:
		n.Kind = a.event
	}
	s.cfg.Notifier.Notify(n)
}
