package observe

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// recHandler records what the observer fed it.
type recHandler struct {
	mu     sync.Mutex
	events []string
	ticks  int
}

func (h *recHandler) Event(ev eventlog.Event) {
	h.mu.Lock()
	h.events = append(h.events, ev.Type)
	h.mu.Unlock()
}

func (h *recHandler) Tick(time.Time) {
	h.mu.Lock()
	h.ticks++
	h.mu.Unlock()
}

// Overflow is logged once per tick with the count since the last report,
// and the observer keeps reading.
func TestObserverLogsDrops(t *testing.T) {
	l, err := eventlog.Open("", eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	tap := l.Tap(2)
	h := &recHandler{}
	o := New(l, tap, h)
	for i := 0; i < 5; i++ {
		l.Log("x", "x.event", "", nil)
	}
	o.Tick(time.Now())
	// The tap was full, so that report itself did not fit: the next tick
	// counts it; the one after has nothing new.
	<-tap.C()
	<-tap.C()
	o.Tick(time.Now())
	o.Tick(time.Now())
	var drops []string
	for _, ev := range l.Tail(0) {
		if ev.Type == "observe.dropped" {
			drops = append(drops, string(ev.Payload))
		}
	}
	if len(drops) != 2 || drops[0] != `{"buffer":4096,"dropped":3,"total":3}` || drops[1] != `{"buffer":4096,"dropped":1,"total":4}` {
		t.Fatalf("observe.dropped = %v", drops)
	}
	if h.ticks != 3 {
		t.Errorf("ticks = %d", h.ticks)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { o.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.mu.Lock()
		n := len(h.events)
		h.mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("handler got %d events", n)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}
