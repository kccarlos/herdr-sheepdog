package gemini

import (
	"context"
	"sync"
)

// outQueue is the model→browser audio queue. The reader goroutine pushes
// chunks without blocking; a pump goroutine hands them to the unbuffered
// out channel one at a time, so Flush can drop everything not yet taken
// by the consumer, including the chunk the pump is currently offering.
type outQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   [][]byte
	flushed chan struct{} // closed and replaced on Flush
	closed  bool
}

func newOutQueue() *outQueue {
	q := &outQueue{flushed: make(chan struct{})}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Push appends a chunk. Chunks pushed after Close are dropped.
func (q *outQueue) Push(chunk []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, chunk)
	q.cond.Signal()
}

// Flush drops every queued chunk and returns how many were dropped.
func (q *outQueue) Flush() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.items)
	q.items = nil
	close(q.flushed)
	q.flushed = make(chan struct{})
	return n
}

// Len returns the number of queued chunks (for tests).
func (q *outQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Close stops the pump after the queue drains; the pump closes out.
func (q *outQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// pump delivers chunks to out until Close (and the queue is empty) or ctx
// ends, then closes out.
func (q *outQueue) pump(ctx context.Context, out chan<- []byte) {
	defer close(out)
	go func() {
		<-ctx.Done()
		q.Close()
	}()
	for {
		q.mu.Lock()
		for len(q.items) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.items) == 0 && q.closed {
			q.mu.Unlock()
			return
		}
		chunk := q.items[0]
		q.items = q.items[1:]
		flushed := q.flushed
		q.mu.Unlock()
		select {
		case out <- chunk:
		case <-flushed:
		case <-ctx.Done():
			return
		}
	}
}
