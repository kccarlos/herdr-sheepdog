package gemini

import (
	"context"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

type outItem struct {
	data    []byte
	epoch   uint64
	flushes []uint64
}

// outQueue is the model→browser audio queue. The reader goroutine pushes
// chunks without blocking; a pump goroutine hands them to the unbuffered
// out channel one at a time, so Flush can drop everything not yet taken
// by the consumer, including the chunk the pump is currently offering.
type outQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   []outItem
	epoch   uint64
	flushes []uint64
	flushed chan struct{} // closed and replaced on Flush
	closed  bool
}

func newOutQueue() *outQueue {
	q := &outQueue{epoch: voice.NextAudioEpoch(), flushed: make(chan struct{})}
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
	q.items = append(q.items, outItem{data: chunk, epoch: q.epoch, flushes: q.flushes})
	q.cond.Signal()
}

// Flush drops every queued chunk (and any chunk in flight in the pump)
// without recording a daemon flush barrier for the client.
func (q *outQueue) Flush() int {
	n, _ := q.Purge()
	return n
}

// Purge drops every queued chunk (and any chunk in flight in the pump) and
// advances the queue's epoch without recording a client flush barrier.
func (q *outQueue) Purge() (dropped int, purgedEpoch uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.purgeLocked(false)
}

// FlushEpoch drops every queued chunk, advances the queue's epoch, and
// records the flushed epoch as a client flush barrier on subsequent chunks.
func (q *outQueue) FlushEpoch() (dropped int, flushedEpoch uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.purgeLocked(true)
}

func (q *outQueue) purgeLocked(barrier bool) (dropped int, oldEpoch uint64) {
	dropped = len(q.items)
	q.items = nil
	oldEpoch = q.epoch
	if barrier {
		q.flushes = voice.AppendFlushEpoch(q.flushes, oldEpoch)
	}
	q.epoch = voice.NextAudioEpoch()
	close(q.flushed)
	q.flushed = make(chan struct{})
	return dropped, oldEpoch
}

func (q *outQueue) BumpEpoch() {
	q.mu.Lock()
	q.epoch = voice.NextAudioEpoch()
	q.mu.Unlock()
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
	q.pumpBoth(ctx, out, nil)
}

func (q *outQueue) pumpBoth(ctx context.Context, out chan<- []byte, outChunks chan<- voice.AudioChunk) {
	defer close(out)
	if outChunks != nil {
		defer close(outChunks)
	}
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
		item := q.items[0]
		q.items = q.items[1:]
		flushed := q.flushed
		q.mu.Unlock()
		select {
		case out <- item.data:
		case outChunks <- voice.AudioChunk{Data: item.data, Epoch: item.epoch, FlushedEpochs: item.flushes}:
		case <-flushed:
		case <-ctx.Done():
			return
		}
	}
}
