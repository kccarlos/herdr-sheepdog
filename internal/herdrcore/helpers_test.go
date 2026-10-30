package herdrcore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
)

// trustScreen is a Codex-style first-run trust question with numbered
// options.
const trustScreen = "Do you trust the contents of this directory?\n\n" +
	"› 1. Yes, continue\n  2. No, quit\n\n  Press enter to continue\n"

// newBackend returns a backend over a fake server, with fast reconnects and
// a fast screen-fallback poll.
func newBackend(t *testing.T) (*herdrcore.Backend, *hctest.Server) {
	t.Helper()
	srv := hctest.New(t)
	b := herdrcore.New(hc.NewClient(srv.Path), nil)
	b.FallbackPoll = 20 * time.Millisecond
	b.Backoff = herdr.Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond}
	return b, srv
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// liveTerminal is a live terminal of the given program.
func liveTerminal(address string, argv ...string) hc.Terminal {
	return hc.Terminal{Address: address, State: "live", Argv: argv}
}

// reported is a live terminal whose program reported the status word.
func reported(address, app, word string) hc.Terminal {
	t := liveTerminal(address, app)
	t.Reported = true
	t.Status = &hc.Status{State: word, App: app}
	return t
}

// requestParams decodes the params of every request of method, in order.
func requestParams[T any](t *testing.T, srv *hctest.Server, method string) []T {
	t.Helper()
	var out []T
	for _, r := range srv.Requests() {
		if r.Method != method {
			continue
		}
		var p T
		if err := json.Unmarshal(r.Params, &p); err != nil {
			t.Fatalf("decode %s params: %v", method, err)
		}
		out = append(out, p)
	}
	return out
}

func countRequests(srv *hctest.Server, method string) int {
	n := 0
	for _, r := range srv.Requests() {
		if r.Method == method {
			n++
		}
	}
	return n
}

// awaitSubscribers waits until the server has seen n events.subscribe
// requests in total, then gives it a moment to register the last one before
// the test emits events.
func awaitSubscribers(t *testing.T, srv *hctest.Server, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for countRequests(srv, "events.subscribe") < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d subscriptions", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
}

// receive returns the next value of ch or fails the test.
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a value")
	}
	var zero T
	return zero
}

// expectNone fails if ch delivers anything within d.
func expectNone[T any](t *testing.T, ch <-chan T, d time.Duration) {
	t.Helper()
	select {
	case v, ok := <-ch:
		if ok {
			t.Fatalf("unexpected value %+v", v)
		}
		t.Fatal("channel closed")
	case <-time.After(d):
	}
}

// expectClosed waits for ch to close, discarding what it still delivers.
func expectClosed[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("channel did not close")
		}
	}
}
