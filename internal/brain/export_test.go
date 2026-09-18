package brain

// QueuedTurns exposes the turn-lock waiter count to the external test
// package so concurrency tests can synchronise without sleeping.
func (b *Brain) QueuedTurns() int { return b.lock.waiting() }
