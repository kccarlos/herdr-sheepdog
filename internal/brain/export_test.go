package brain

import "time"

// QueuedTurns exposes the turn-lock waiter count to the external test
// package so concurrency tests can synchronise without sleeping.
func (b *Brain) QueuedTurns() int { return b.lock.waiting() }

// QueuedCEOTurns returns how many CEO turns wait for the lock.
func (b *Brain) QueuedCEOTurns() int { return b.lock.ceoWaiting() }

// ToolDeclarations exposes the brain's tool surface to the external test
// package (the adapter's golden tools file is rendered from it).
var ToolDeclarations = toolDeclarations

// waiting reports how many turns are queued behind the current one
// (both priorities), so tests can synchronise without sleeping.
func (l *turnLock) waiting() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ceo) + len(l.event)
}

// KeyIndex returns the index of the key in use (never the key).
func (m *GenaiModel) KeyIndex() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idx
}

// NewVertexModelAt is NewVertexModel against baseURL (a fake server).
var NewVertexModelAt = newVertexModel

// SetFailoverReserve shrinks the failover reserve and minimum first
// attempt for a test; the returned func restores them.
func SetFailoverReserve(reserve, minAttempt time.Duration) func() {
	r, m := failoverReserve, failoverMinAttempt
	failoverReserve, failoverMinAttempt = reserve, minAttempt
	return func() { failoverReserve, failoverMinAttempt = r, m }
}
