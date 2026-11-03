package attention

import (
	"context"
	"fmt"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// marksLocked is the marks of a terminal from its record and what the last
// snapshot showed.
func (l *Local) marksLocked(key string, view paneView) herdr.Marks {
	m := herdr.Marks{Word: view.word}
	if r := l.recs[key]; r != nil {
		m.Turn, m.Pinned = r.Turn, r.Pinned
		if r.Seen != nil {
			seen := *r.Seen
			m.Seen = &seen
		}
	}
	return m
}

// AllMarks returns the marks of every terminal in the last snapshot, by
// pane id. It reads only what Refresh gathered.
func (l *Local) AllMarks(context.Context) (map[string]herdr.Marks, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]herdr.Marks, len(l.panes))
	for key, view := range l.panes {
		out[view.paneID] = l.marksLocked(key, view)
	}
	return out, nil
}

// MarkChanges reports panes whose turn or seen mark moved in a refresh: a
// turn ended, or someone looked at the pane in a TUI client.
func (l *Local) MarkChanges() <-chan string { return l.changes }

// MarkSeen raises seen to min(turn, the pane's turn) (herdr.Marks.SeenWrite).
func (l *Local) MarkSeen(ctx context.Context, paneID string, turn uint64) (herdr.Marks, error) {
	return l.write(ctx, paneID, func(m herdr.Marks, r *record) bool {
		seen, write := m.SeenWrite(turn)
		if write {
			at := l.opts.Now()
			r.Seen, r.SeenAt = &seen, &at
		}
		return write
	})
}

// MarkUnread lowers seen to one turn short of the pane's turn
// (herdr.Marks.UnreadWrite).
func (l *Local) MarkUnread(ctx context.Context, paneID string) (herdr.Marks, error) {
	return l.write(ctx, paneID, func(m herdr.Marks, r *record) bool {
		seen, write := m.UnreadWrite()
		if write {
			r.Seen, r.SeenAt = &seen, nil
		}
		return write
	})
}

// SetPinned pins or unpins the terminal. The old herdr's own window shows no
// pins: they exist only here.
func (l *Local) SetPinned(ctx context.Context, paneID string, pinned bool) (herdr.Marks, error) {
	return l.write(ctx, paneID, func(m herdr.Marks, r *record) bool {
		r.Pinned = pinned
		return m.Pinned != pinned
	})
}

// AgentStarted marks a pane sheepdog started an agent in as seen up to now,
// so the agent is not unread before its first turn. It is the old herdr's
// counterpart of herdr.seen=0, which hc keeps in a label. A pane that has a
// seen mark keeps it.
func (l *Local) AgentStarted(ctx context.Context, paneID string) error {
	l.mu.Lock()
	key, tracked := l.byPane[paneID]
	var m herdr.Marks
	if tracked {
		m = l.marksLocked(key, l.panes[key])
	}
	l.mu.Unlock()
	if !tracked {
		all, err := l.refreshedMarks(ctx)
		if err != nil {
			return err
		}
		var ok bool
		if m, ok = all[paneID]; !ok {
			return fmt.Errorf("%w: %s", herdr.ErrPaneNotFound, paneID)
		}
	}
	if m.Seen != nil {
		return nil
	}
	_, err := l.MarkSeen(ctx, paneID, m.Turn)
	return err
}

// refreshedMarks reads the fleet again and returns the marks it shows.
func (l *Local) refreshedMarks(ctx context.Context) (map[string]herdr.Marks, error) {
	if err := l.Refresh(ctx); err != nil {
		return nil, err
	}
	return l.AllMarks(ctx)
}

// write runs one change to a pane's record and saves the store. change
// edits r and reports whether anything changed; the pane's marks before the
// change are m. A pane the store has not seen yet is looked for in a fresh
// snapshot first. When saving fails the record is put back.
func (l *Local) write(ctx context.Context, paneID string, change func(m herdr.Marks, r *record) bool) (herdr.Marks, error) {
	l.mu.Lock()
	_, tracked := l.byPane[paneID]
	l.mu.Unlock()
	if !tracked {
		if err := l.Refresh(ctx); err != nil {
			return herdr.Marks{}, err
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	key, ok := l.byPane[paneID]
	if !ok {
		return herdr.Marks{}, fmt.Errorf("%w: %s", herdr.ErrPaneNotFound, paneID)
	}
	view := l.panes[key]
	m := l.marksLocked(key, view)

	prev, had := l.recs[key]
	var backup record
	r := &record{}
	if had {
		backup = *prev
		r = prev
	}
	if !change(m, r) {
		return m, nil
	}
	r.PaneID = paneID
	if had || !r.empty() {
		l.recs[key] = r
	}
	if had && r.empty() {
		delete(l.recs, key)
	}
	if err := l.persistLocked(); err != nil {
		if had {
			l.recs[key] = &backup
		} else {
			delete(l.recs, key)
		}
		return herdr.Marks{}, err
	}
	return l.marksLocked(key, view), nil
}
