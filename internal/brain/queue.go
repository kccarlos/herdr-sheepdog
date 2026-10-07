package brain

import (
	"context"
	"sync"
)

// waiter is one turn queued for the lock.
type waiter struct {
	ch chan struct{}
	// key names what an event turn is about (the agent); a newer event
	// turn with the same key replaces a queued one ("" never coalesces).
	key string
	// coalesced is set (under turnLock.mu, before ch is closed) when the
	// waiter was dropped for a newer event turn instead of handed the
	// lock.
	coalesced bool
}

// turnLock serialises turns with two priorities: when the lock is
// released, a waiting user turn is handed it before any waiting event turn,
// in arrival order within a priority. At most one event turn waits per
// key: a newer one replaces the queued one, which fails with
// ErrCoalesced.
type turnLock struct {
	mu    sync.Mutex
	busy  bool
	ceo   []*waiter
	event []*waiter
}

// acquire blocks until the lock is handed over or ctx ends. key is the
// coalescing key of an event turn (ignored for a user turn). replaced
// reports that a queued event turn with the same key was dropped for
// this one; ErrCoalesced means this turn was dropped the same way.
func (l *turnLock) acquire(ctx context.Context, ceo bool, key string) (replaced bool, err error) {
	l.mu.Lock()
	if !l.busy {
		l.busy = true
		l.mu.Unlock()
		turnStarted(ctx)
		return false, nil
	}
	w := &waiter{ch: make(chan struct{}), key: key}
	if ceo {
		l.ceo = append(l.ceo, w)
	} else {
		if key != "" {
			for i, old := range l.event {
				if old.key == key {
					l.event = append(l.event[:i], l.event[i+1:]...)
					old.coalesced = true
					close(old.ch)
					replaced = true
					break
				}
			}
		}
		l.event = append(l.event, w)
	}
	l.mu.Unlock()
	select {
	case <-w.ch:
		// coalesced was written under the lock before the close.
		if w.coalesced {
			return replaced, ErrCoalesced
		}
		turnStarted(ctx)
		return replaced, nil
	case <-ctx.Done():
		l.mu.Lock()
		if l.remove(w) {
			l.mu.Unlock()
			return replaced, ctx.Err()
		}
		coalesced := w.coalesced
		l.mu.Unlock()
		if coalesced {
			return replaced, ctx.Err()
		}
		// Handed the lock in the meantime: pass it on.
		l.release()
		return replaced, ctx.Err()
	}
}

// remove drops w from the waiting lists; false when it is not there
// (it was already signalled). Caller holds l.mu.
func (l *turnLock) remove(w *waiter) bool {
	for i, c := range l.ceo {
		if c == w {
			l.ceo = append(l.ceo[:i], l.ceo[i+1:]...)
			return true
		}
	}
	for i, c := range l.event {
		if c == w {
			l.event = append(l.event[:i], l.event[i+1:]...)
			return true
		}
	}
	return false
}

// release hands the lock to the next waiter (user first) or frees it.
func (l *turnLock) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch {
	case len(l.ceo) > 0:
		w := l.ceo[0]
		l.ceo = l.ceo[1:]
		close(w.ch)
	case len(l.event) > 0:
		w := l.event[0]
		l.event = l.event[1:]
		close(w.ch)
	default:
		l.busy = false
	}
}

// ceoWaiting reports how many user turns are queued: an event turn checks
// it before every model call and yields when it is not zero.
func (l *turnLock) ceoWaiting() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ceo)
}

// turnStartKey carries WithTurnStart's callback.
type turnStartKey struct{}

// WithTurnStart returns ctx carrying f: a turn run under ctx calls f
// once, the moment it is handed the turn lock — never while it waits.
// The supervisor coalesces back-to-back user utterances only while
// the earlier one's turn is still queued (docs/roles.md "One
// decision-maker"); a turn in progress is never cut, so it must know
// exactly when the turn started.
func WithTurnStart(ctx context.Context, f func()) context.Context {
	return context.WithValue(ctx, turnStartKey{}, f)
}

// turnStarted calls ctx's WithTurnStart callback, if any.
func turnStarted(ctx context.Context) {
	if f, ok := ctx.Value(turnStartKey{}).(func()); ok && f != nil {
		f()
	}
}
