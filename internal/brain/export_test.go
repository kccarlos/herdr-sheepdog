package brain

import "time"

// QueuedTurns exposes the turn-lock waiter count to the external test
// package so concurrency tests can synchronise without sleeping.
func (b *Brain) QueuedTurns() int { return b.lock.waiting() }

// QueuedCEOTurns returns how many user turns wait for the lock.
func (b *Brain) QueuedCEOTurns() int { return b.lock.ceoWaiting() }

// ToolDeclarations exposes the orchestrator's tool surface to the external test
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

// SetFailoverReserve shrinks the failover reserve and the minimum first
// attempt (on a failing streak, and healthy) for a test; the returned
// func restores them.
func SetFailoverReserve(reserve, minAttempt, healthyAttempt time.Duration) func() {
	r, m, h := failoverReserve, failoverMinAttempt, failoverHealthyAttempt
	failoverReserve, failoverMinAttempt, failoverHealthyAttempt = reserve, minAttempt, healthyAttempt
	return func() { failoverReserve, failoverMinAttempt, failoverHealthyAttempt = r, m, h }
}

// IsTransportError exposes isTransportError to the external tests.
var IsTransportError = isTransportError
