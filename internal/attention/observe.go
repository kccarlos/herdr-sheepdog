package attention

import "github.com/kccarlos/herdr-sheepdog/internal/herdr"

// target is one terminal in a snapshot.
type target struct{ key, paneID string }

// observeLocked folds a snapshot into the store and the pane table. It
// returns the pane ids whose marks changed, and the error of saving them.
func (l *Local) observeLocked(snap herdr.Snapshot) (changed []string, err error) {
	agents := make(map[string]herdr.AgentInfo, len(snap.Agents))
	for _, a := range snap.Agents {
		agents[a.PaneID] = a
	}
	var targets []target
	inPanes := make(map[string]bool, len(snap.Panes))
	for _, p := range snap.Panes {
		inPanes[p.PaneID] = true
		targets = append(targets, target{terminalKey(p.TerminalID, p.PaneID), p.PaneID})
	}
	for _, a := range snap.Agents {
		if !inPanes[a.PaneID] {
			targets = append(targets, target{terminalKey(a.TerminalID, a.PaneID), a.PaneID})
		}
	}

	panes := make(map[string]paneView, len(targets))
	byPane := make(map[string]string, len(targets))
	dirty := false
	for _, t := range targets {
		view := paneView{paneID: t.paneID, word: herdr.StateUnknown}
		if a, ok := agents[t.paneID]; ok {
			view.word = a.Status
			marksMoved, stored := l.followLocked(t.key, t.paneID, a)
			dirty = dirty || stored
			if marksMoved {
				changed = append(changed, t.paneID)
			}
		}
		panes[t.key] = view
		byPane[t.paneID] = t.key
	}
	l.panes, l.byPane = panes, byPane

	// A terminal that is not in a complete snapshot has closed, and takes
	// its marks with it.
	for key := range l.recs {
		if _, ok := panes[key]; !ok {
			delete(l.recs, key)
			dirty = true
		}
	}
	if dirty {
		err = l.persistLocked()
	}
	return changed, err
}

// followLocked updates a terminal's record from its agent in the snapshot.
// It reports whether the terminal's marks moved (a reader should look
// again) and whether the record changed in any way (it must be saved).
//
// The turn is completion_seq: null while the agent works, so the record keeps
// the highest value seen. A value below the record means herdr restarted and
// counts again from the start, so the turn and the seen mark are dropped (the
// backend's own word decides again). An agent that went from done to idle
// without a new turn is a pane someone focused in a TUI client: that is
// "seen".
func (l *Local) followLocked(key, paneID string, a herdr.AgentInfo) (marksMoved, stored bool) {
	r := l.recs[key]
	turnMoved := false
	if cs := a.CompletionSeq; cs != nil {
		switch {
		case r != nil && *cs < r.Turn:
			r.Turn, r.Seen, r.SeenAt = *cs, nil, nil
			marksMoved, turnMoved = true, true
		case *cs > recordTurn(r):
			if r == nil {
				r = &record{}
				l.recs[key] = r
			}
			r.Turn = *cs
			marksMoved, turnMoved = true, true
		}
	}
	prev, known := l.panes[key]
	if known && prev.word == herdr.StateDone && a.Status == herdr.StateIdle && !turnMoved {
		if r == nil {
			r = &record{}
			l.recs[key] = r
		}
		if r.Seen == nil || *r.Seen < r.Turn {
			seen, at := r.Turn, l.opts.Now()
			r.Seen, r.SeenAt = &seen, &at
			marksMoved = true
		}
	}
	if r == nil {
		return marksMoved, false
	}
	if r.PaneID != paneID {
		r.PaneID = paneID
		stored = true
	}
	return marksMoved, stored || marksMoved
}

func recordTurn(r *record) uint64 {
	if r == nil {
		return 0
	}
	return r.Turn
}
