package herdrcore_test

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
)

// runWatcher starts a Watcher over the backend and returns its transitions.
// The watcher stops, and its channel closes, when the test ends.
func runWatcher(t *testing.T, b *herdrcore.Backend) <-chan herdr.Transition {
	t.Helper()
	w := herdr.NewWatcher(b, herdr.WatcherConfig{
		Debounce:         50 * time.Millisecond,
		SnapshotInterval: time.Hour,
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("watcher did not stop")
		}
	})
	return w.Transitions()
}

// receiveUntil returns the first transition satisfying match.
func receiveUntil(t *testing.T, ch <-chan herdr.Transition, match func(herdr.Transition) bool) herdr.Transition {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case tr, ok := <-ch:
			if !ok {
				t.Fatal("transitions closed")
			}
			if match(tr) {
				return tr
			}
		case <-deadline:
			t.Fatal("timed out waiting for the transition")
		}
	}
}

// startFleet seeds one idle Claude agent, starts the watcher and waits until
// it holds both its subscriptions (fleet and the agent's).
func startFleet(t *testing.T) (*hctest.Server, <-chan herdr.Transition) {
	t.Helper()
	b, srv := newBackend(t)
	srv.Add(reported("t1", "claude-code", "idle"))
	ch := runWatcher(t, b)

	first := receive(t, ch)
	if first.PaneID != "t1" || first.To != herdr.StateIdle || first.Agent != "claude" {
		t.Fatalf("first transition = %+v", first)
	}
	awaitSubscribers(t, srv, 2)
	return srv, ch
}

func TestWatcherTransitionsInOrder(t *testing.T) {
	srv, ch := startFleet(t)

	srv.SetStatus("t1", &hc.Status{State: "working", App: "claude-code"})
	if tr := receive(t, ch); tr.To != herdr.StateWorking || tr.From != herdr.StateIdle {
		t.Fatalf("transition = %+v", tr)
	}

	blockedAt := time.Now()
	srv.SetStatus("t1", &hc.Status{State: "blocked", App: "claude-code", Kind: ptr("permission")})
	tr := receive(t, ch)
	if tr.To != herdr.StateBlocked {
		t.Fatalf("transition = %+v", tr)
	}
	if waited := time.Since(blockedAt); waited < 40*time.Millisecond {
		t.Fatalf("blocked emitted after %v, want the debounce first", waited)
	}

	srv.SetStatus("t1", &hc.Status{State: "done", App: "claude-code"})
	if tr := receive(t, ch); tr.To != herdr.StateDone || tr.From != herdr.StateBlocked {
		t.Fatalf("transition = %+v", tr)
	}
}

func TestWatcherClosesFinishedTerminals(t *testing.T) {
	srv, ch := startFleet(t)

	srv.Finish("t1", "exited")
	tr := receiveUntil(t, ch, func(tr herdr.Transition) bool { return tr.Closed })
	if tr.PaneID != "t1" {
		t.Fatalf("closed transition = %+v", tr)
	}
}

func TestWatcherResyncsOnMissed(t *testing.T) {
	srv, _ := startFleet(t)

	before := countRequests(srv, "terminal.list")
	srv.Missed()
	deadline := time.Now().Add(3 * time.Second)
	for countRequests(srv, "terminal.list") <= before {
		if time.Now().After(deadline) {
			t.Fatal("no snapshot after a missed notification")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatcherSurvivesDroppedSubscriptions(t *testing.T) {
	srv, ch := startFleet(t)

	srv.DropSubscribers()
	awaitSubscribers(t, srv, 4) // fleet and pane, again

	srv.SetStatus("t1", &hc.Status{State: "working", App: "claude-code"})
	receiveUntil(t, ch, func(tr herdr.Transition) bool { return tr.To == herdr.StateWorking })
}

func ptr[T any](v T) *T { return &v }
