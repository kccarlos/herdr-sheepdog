package brain

// QueuedTurns exposes the turn-lock waiter count to the external test
// package so concurrency tests can synchronise without sleeping.
func (b *Brain) QueuedTurns() int { return b.lock.waiting() }

// QueuedCEOTurns returns how many CEO turns wait for the lock.
func (b *Brain) QueuedCEOTurns() int { return b.lock.ceoWaiting() }
