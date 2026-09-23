package gptlive

import (
	"context"
	"encoding/binary"
	"math"
	"sync"
)

// upsampler converts 16 kHz PCM16 LE mono to 24 kHz (ratio 2:3) by linear
// interpolation, keeping state across frames so frame boundaries do not
// click. Every two input samples x0, x1 (with x2 the next) yield three
// outputs at input positions 0, 2/3 and 4/3.
type upsampler struct {
	carry []int16 // input samples not yet consumed (at most 2)
}

func (u *upsampler) process(in []byte) []byte {
	samples := make([]int16, 0, len(u.carry)+len(in)/2)
	samples = append(samples, u.carry...)
	for i := 0; i+1 < len(in); i += 2 {
		samples = append(samples, int16(binary.LittleEndian.Uint16(in[i:])))
	}
	out := make([]byte, 0, len(samples)*3)
	i := 0
	for ; i+2 < len(samples); i += 2 {
		x0, x1, x2 := float64(samples[i]), float64(samples[i+1]), float64(samples[i+2])
		out = appendSample(out, x0)
		out = appendSample(out, x0+(x1-x0)*2/3)
		out = appendSample(out, x1+(x2-x1)/3)
	}
	u.carry = append(u.carry[:0], samples[i:]...)
	return out
}

func appendSample(b []byte, v float64) []byte {
	v = math.Round(v)
	if v > math.MaxInt16 {
		v = math.MaxInt16
	} else if v < math.MinInt16 {
		v = math.MinInt16
	}
	return binary.LittleEndian.AppendUint16(b, uint16(int16(v)))
}

// rms is the root mean square of a PCM16 LE buffer.
func rms(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	var sum float64
	for i := 0; i+1 < len(pcm); i += 2 {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		sum += v * v
	}
	return math.Sqrt(sum / float64(n))
}

// peak is the largest absolute sample of a PCM16 LE buffer.
func peak(pcm []byte) int {
	m := 0
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int(int16(binary.LittleEndian.Uint16(pcm[i:])))
		if v < 0 {
			v = -v
		}
		if v > m {
			m = v
		}
	}
	return m
}

// outQueue is the model→client audio queue (the gemini package's design):
// the reader pushes without blocking; a pump hands chunks to the
// unbuffered out channel one at a time, so Flush drops everything the
// consumer has not taken yet.
type outQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	items   [][]byte
	flushed chan struct{}
	closed  bool
}

func newOutQueue() *outQueue {
	q := &outQueue{flushed: make(chan struct{})}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *outQueue) Push(chunk []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, chunk)
	q.cond.Signal()
}

func (q *outQueue) Flush() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.items)
	q.items = nil
	close(q.flushed)
	q.flushed = make(chan struct{})
	return n
}

func (q *outQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

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
