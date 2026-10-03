package eventlog

import (
	"testing"
	"time"
)

// A tap that falls behind loses events (counted), never blocks Log and is
// never closed for it; it closes with the log.
func TestTapDropsWithoutBlocking(t *testing.T) {
	l, err := Open("", Options{})
	if err != nil {
		t.Fatal(err)
	}
	tap := l.Tap(4)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			l.Log("x", "x.event", "", map[string]any{"i": i})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Log blocked on a full tap")
	}
	if got := tap.Dropped(); got != 6 {
		t.Errorf("dropped = %d, want 6", got)
	}
	for i := 0; i < 4; i++ {
		if ev := <-tap.C(); ev.Type != "x.event" {
			t.Errorf("event %d = %+v", i, ev)
		}
	}
	l.Log("x", "x.after", "", nil)
	if ev := <-tap.C(); ev.Type != "x.after" {
		t.Errorf("the tap stopped after overflowing: %+v", ev)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-tap.C(); ok {
		t.Error("tap not closed with the log")
	}
	tap.Close() // after the log closed: no panic
	if late := l.Tap(1); late != nil {
		if _, ok := <-late.C(); ok {
			t.Error("a tap on a closed log is open")
		}
	}
}

func TestTapClose(t *testing.T) {
	l, err := Open("", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	tap := l.Tap(1)
	tap.Close()
	tap.Close()
	l.Log("x", "x.event", "", nil) // no send on a closed channel
	if _, ok := <-tap.C(); ok {
		t.Error("closed tap delivered")
	}
}
