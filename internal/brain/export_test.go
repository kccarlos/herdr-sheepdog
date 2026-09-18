package brain

// QueuedTurns exposes the turn-lock waiter count to the external test
// package so concurrency tests can synchronise without sleeping.
func (b *Brain) QueuedTurns() int { return b.lock.waiting() }

// QueuedCEOTurns returns how many CEO turns wait for the lock.
func (b *Brain) QueuedCEOTurns() int { return b.lock.ceoWaiting() }

// ToolDeclarations exposes the brain's tool surface to the external test
// package (the adapter's golden tools file is rendered from it).
var ToolDeclarations = toolDeclarations
