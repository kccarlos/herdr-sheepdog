package eventlog

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// Defaults.
const (
	DefaultRingSize      = 2000
	DefaultFlushInterval = 500 * time.Millisecond
	DefaultSubscriberBuf = 256
	fileBufferBytes      = 64 << 10
)

// Options tune a Log; zero values take the defaults above.
type Options struct {
	// RingSize is how many recent events Tail and late subscribers can
	// see (default 2000).
	RingSize int
	// FlushInterval is the period of the background flush (default 500 ms).
	FlushInterval time.Duration
	// SubscriberBuffer is the channel capacity of each subscriber; a
	// subscriber that falls this far behind is dropped (default 256).
	SubscriberBuffer int
	// Now overrides the clock (tests: rotation).
	Now func() time.Time
	// Stderr receives the one-line report of a write failure; nil means
	// os.Stderr.
	Stderr io.Writer
}

func (o Options) withDefaults() Options {
	if o.RingSize <= 0 {
		o.RingSize = DefaultRingSize
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = DefaultFlushInterval
	}
	if o.SubscriberBuffer <= 0 {
		o.SubscriberBuffer = DefaultSubscriberBuf
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	return o
}

// Log is the event log. It is safe for concurrent use; Log never blocks
// on subscribers or on disk beyond the buffered write itself.
type Log struct {
	dir  string // "" keeps the log in memory only
	opts Options

	mu       sync.Mutex
	seq      uint64
	lastID   int64 // the last event id handed out (or found on disk at Open)
	file     *os.File
	w        *bufio.Writer
	fileDay  string
	ring     []Event
	ringHead int // index of the oldest entry when the ring is full
	ringLen  int
	subs     map[*subscriber]struct{}
	taps     map[*Tap]struct{} // lossy observer feeds (tap.go)
	closed   bool
	reported bool // write error already reported to stderr

	stop chan struct{}
	done chan struct{}
}

type subscriber struct {
	ch     chan Event
	cancel context.CancelFunc
}

var _ logging.Logger = (*Log)(nil)

// Open creates <dataDir>/events and starts the flush ticker. An empty
// dataDir keeps the log in memory (ring + subscribers only).
func Open(dataDir string, opts Options) (*Log, error) {
	l := &Log{opts: opts.withDefaults(), subs: map[*subscriber]struct{}{}, stop: make(chan struct{}), done: make(chan struct{})}
	l.ring = make([]Event, l.opts.RingSize)
	if dataDir != "" {
		l.dir = filepath.Join(dataDir, "events")
		if err := os.MkdirAll(l.dir, 0o700); err != nil {
			return nil, fmt.Errorf("eventlog: mkdir %s: %w", l.dir, err)
		}
		// Ids keep increasing across restarts, even when the clock went
		// back: the next one is after the last line on disk.
		l.lastID = lastIDOnDisk(l.dir)
	}
	go l.flushLoop()
	return l, nil
}

// Dir returns the directory the daily files are written to ("" when in
// memory).
func (l *Log) Dir() string { return l.dir }

// Path returns the file events with timestamp ts go to.
func (l *Log) Path(ts time.Time) string {
	if l.dir == "" {
		return ""
	}
	return filepath.Join(l.dir, dayOf(ts)+".jsonl")
}

func dayOf(ts time.Time) string { return ts.UTC().Format("2006-01-02") }

// Log implements logging.Logger.
func (l *Log) Log(source, typ, paneID string, payload any) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.seq++
	now := l.opts.Now()
	l.lastID = nextID(l.lastID, now)
	ev := newEvent(l.seq, l.lastID, now, source, typ, paneID, payload)
	l.writeLocked(ev)
	l.pushRingLocked(ev)
	dropped := l.fanOutLocked(ev)
	l.offerTapsLocked(ev)
	l.mu.Unlock()

	for _, s := range dropped {
		s.cancel()
		l.Log("eventlog", "eventlog.subscriber_dropped", "", map[string]any{"seq": ev.Seq, "buffer": l.opts.SubscriberBuffer})
	}
}

// writeLocked appends the line to the current day's file, rotating when
// the day changed. Caller holds l.mu.
func (l *Log) writeLocked(ev Event) {
	if l.dir == "" {
		return
	}
	day := dayOf(ev.TS)
	if l.file == nil || day != l.fileDay {
		l.rotateLocked(day)
	}
	if l.w == nil {
		return
	}
	if _, err := l.w.Write(append(ev.encoded, '\n')); err != nil {
		l.reportLocked(err)
	}
}

// rotateLocked flushes and closes the current file and opens the one for
// day. Caller holds l.mu.
func (l *Log) rotateLocked(day string) {
	l.closeFileLocked()
	path := filepath.Join(l.dir, day+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		l.reportLocked(err)
		l.fileDay = day // do not retry on every line
		return
	}
	l.file, l.w, l.fileDay = f, bufio.NewWriterSize(f, fileBufferBytes), day
}

func (l *Log) closeFileLocked() {
	if l.w != nil {
		if err := l.w.Flush(); err != nil {
			l.reportLocked(err)
		}
	}
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			l.reportLocked(err)
		}
	}
	l.file, l.w = nil, nil
}

// reportLocked prints the first write error; later ones are silent so a
// full disk does not flood stderr. Caller holds l.mu.
func (l *Log) reportLocked(err error) {
	if l.reported {
		return
	}
	l.reported = true
	_, _ = fmt.Fprintf(l.opts.Stderr, "eventlog: write error (further errors suppressed): %v\n", err)
}

func (l *Log) pushRingLocked(ev Event) {
	n := len(l.ring)
	if l.ringLen < n {
		l.ring[(l.ringHead+l.ringLen)%n] = ev
		l.ringLen++
		return
	}
	l.ring[l.ringHead] = ev
	l.ringHead = (l.ringHead + 1) % n
}

// fanOutLocked offers ev to every subscriber and returns those that were
// full (already removed from the set). Caller holds l.mu.
func (l *Log) fanOutLocked(ev Event) []*subscriber {
	var dropped []*subscriber
	for s := range l.subs {
		select {
		case s.ch <- ev:
		default:
			delete(l.subs, s)
			dropped = append(dropped, s)
		}
	}
	return dropped
}

// Tail returns the last n events (all of the ring when n <= 0 or larger
// than it), oldest first.
func (l *Log) Tail(n int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > l.ringLen {
		n = l.ringLen
	}
	out := make([]Event, 0, n)
	size := len(l.ring)
	start := (l.ringHead + l.ringLen - n) % size
	for i := 0; i < n; i++ {
		out = append(out, l.ring[(start+i)%size])
	}
	return out
}

// Seq returns the sequence number of the most recent event (0 when none).
func (l *Log) Seq() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// Subscribe returns a channel that receives every event logged from now
// on. It is closed when ctx ends, when the log is closed, or when the
// subscriber falls SubscriberBuffer events behind (the drop is logged).
// Callers that want history first should call Tail and skip events whose
// Seq is not greater than the last one they replayed.
func (l *Log) Subscribe(ctx context.Context) <-chan Event {
	ctx, cancel := context.WithCancel(ctx)
	s := &subscriber{ch: make(chan Event, l.opts.SubscriberBuffer), cancel: cancel}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		cancel()
		close(s.ch)
		return s.ch
	}
	l.subs[s] = struct{}{}
	l.mu.Unlock()
	go func() {
		<-ctx.Done()
		l.mu.Lock()
		delete(l.subs, s)
		l.mu.Unlock()
		// Drain so a concurrent fan-out that already picked s cannot block
		// (it never can: sends are non-blocking), then close.
		close(s.ch)
	}()
	return s.ch
}

// Flush writes buffered lines to disk.
func (l *Log) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w == nil {
		return nil
	}
	if err := l.w.Flush(); err != nil {
		l.reportLocked(err)
		return err
	}
	return nil
}

func (l *Log) flushLoop() {
	defer close(l.done)
	t := time.NewTicker(l.opts.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			_ = l.Flush()
		}
	}
}

// Close flushes and closes the file and every subscriber. Later Log calls
// are ignored.
func (l *Log) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	close(l.stop)
	subs := make([]*subscriber, 0, len(l.subs))
	for s := range l.subs {
		subs = append(subs, s)
	}
	l.subs = map[*subscriber]struct{}{}
	l.closeTapsLocked()
	l.closeFileLocked()
	reported := l.reported
	l.mu.Unlock()
	<-l.done
	for _, s := range subs {
		s.cancel()
	}
	if reported {
		return fmt.Errorf("eventlog: a write error occurred (see stderr)")
	}
	return nil
}
