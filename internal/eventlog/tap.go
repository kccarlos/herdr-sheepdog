package eventlog

import "sync/atomic"

// Tap is a lossy subscription for observers (internal/observe): every
// event logged from now on is offered to its channel without blocking,
// and one that does not fit is counted as dropped instead. Unlike a
// Subscribe channel, a Tap that falls behind is never closed: an
// observer that misses events loses accuracy, not its feed. The
// observer reads Dropped and logs the loss itself (observe.dropped).
//
// Offering an event costs Log one non-blocking channel send under its
// lock, the same as a subscriber; everything heavier runs on the
// observer's goroutine.
type Tap struct {
	l       *Log
	ch      chan Event
	dropped atomic.Uint64
}

// Tap returns a lossy subscription buffering up to buf events (at least
// 1). It is closed by Tap.Close or when the log is closed.
func (l *Log) Tap(buf int) *Tap {
	t := &Tap{l: l, ch: make(chan Event, max(buf, 1))}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		close(t.ch)
		return t
	}
	if l.taps == nil {
		l.taps = map[*Tap]struct{}{}
	}
	l.taps[t] = struct{}{}
	return t
}

// C is the event channel.
func (t *Tap) C() <-chan Event { return t.ch }

// Dropped is how many events did not fit in the buffer so far.
func (t *Tap) Dropped() uint64 { return t.dropped.Load() }

// Close stops the tap and closes its channel. Safe to call twice and
// after the log closed.
func (t *Tap) Close() {
	t.l.mu.Lock()
	defer t.l.mu.Unlock()
	if _, ok := t.l.taps[t]; ok {
		delete(t.l.taps, t)
		close(t.ch)
	}
}

// offerTapsLocked offers ev to every tap. Caller holds l.mu.
func (l *Log) offerTapsLocked(ev Event) {
	for t := range l.taps {
		select {
		case t.ch <- ev:
		default:
			t.dropped.Add(1)
		}
	}
}

// closeTapsLocked closes every tap (the log is closing). Caller holds
// l.mu.
func (l *Log) closeTapsLocked() {
	for t := range l.taps {
		close(t.ch)
	}
	l.taps = nil
}
