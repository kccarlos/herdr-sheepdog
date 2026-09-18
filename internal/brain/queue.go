package brain

import (
	"context"
	"sync"
)

// turnLock serialises turns with two priorities: when the lock is
// released, a waiting CEO turn is handed it before any waiting event turn,
// in arrival order within a priority.
type turnLock struct {
	mu    sync.Mutex
	busy  bool
	ceo   []chan struct{}
	event []chan struct{}
}

// acquire blocks until the lock is handed over or ctx ends.
func (l *turnLock) acquire(ctx context.Context, ceo bool) error {
	l.mu.Lock()
	if !l.busy {
		l.busy = true
		l.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	if ceo {
		l.ceo = append(l.ceo, ch)
	} else {
		l.event = append(l.event, ch)
	}
	l.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		l.mu.Lock()
		if l.remove(ch) {
			l.mu.Unlock()
			return ctx.Err()
		}
		// Handed the lock in the meantime: pass it on.
		l.mu.Unlock()
		l.release()
		return ctx.Err()
	}
}

// remove drops ch from the waiting lists; false when it is not there
// (it was already signalled). Caller holds l.mu.
func (l *turnLock) remove(ch chan struct{}) bool {
	for i, c := range l.ceo {
		if c == ch {
			l.ceo = append(l.ceo[:i], l.ceo[i+1:]...)
			return true
		}
	}
	for i, c := range l.event {
		if c == ch {
			l.event = append(l.event[:i], l.event[i+1:]...)
			return true
		}
	}
	return false
}

// release hands the lock to the next waiter (CEO first) or frees it.
func (l *turnLock) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case len(l.ceo) > 0:
		ch := l.ceo[0]
		l.ceo = l.ceo[1:]
		close(ch)
	case len(l.event) > 0:
		ch := l.event[0]
		l.event = l.event[1:]
		close(ch)
	default:
		l.busy = false
	}
}

// waiting reports how many turns are queued behind the current one
// (both priorities). Used by tests to synchronise without sleeping.
func (l *turnLock) waiting() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ceo) + len(l.event)
}
