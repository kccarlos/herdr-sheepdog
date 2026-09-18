package gemini

import (
	"context"
	"testing"
	"time"
)

func TestOutQueuePumpAndFlush(t *testing.T) {
	q := newOutQueue()
	out := make(chan []byte)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); q.pump(ctx, out) }()

	q.Push([]byte("a"))
	if got := <-out; string(got) != "a" {
		t.Fatalf("got %q", got)
	}
	q.Push([]byte("b"))
	q.Push([]byte("c"))
	time.Sleep(10 * time.Millisecond) // pump is now offering "b"
	if n := q.Flush(); n != 1 {
		t.Errorf("Flush dropped %d queued, want 1 (b was in flight)", n)
	}
	q.Push([]byte("d"))
	if got := <-out; string(got) != "d" {
		t.Fatalf("after flush got %q, want d", got)
	}
	q.Close()
	if _, ok := <-out; ok {
		t.Fatal("out not closed")
	}
	<-done
	q.Push([]byte("late")) // must not panic or block
	if q.Len() != 0 {
		t.Error("push after close was queued")
	}
}
