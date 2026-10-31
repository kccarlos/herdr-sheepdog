package herdrcore_test

import (
	"context"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

func TestSubscribeStatusEvents(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(reported("t1", "claude-code", "idle"))
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	ch := b.Subscribe(ctx, "t1")
	awaitSubscribers(t, srv, 1)

	srv.SetStatus("t1", &hc.Status{State: "error", App: "claude-code"})
	ev := receive(t, ch)
	if ev.PaneID != "t1" || ev.Agent != "claude" || ev.State != herdr.StateDone ||
		ev.Source != "socket" || ev.At.IsZero() {
		t.Fatalf("event = %+v", ev)
	}
	srv.SetStatus("t1", &hc.Status{State: "blocked", App: "claude-code"})
	if ev := receive(t, ch); ev.State != herdr.StateBlocked {
		t.Fatalf("event = %+v", ev)
	}

	cancel()
	expectClosed(t, ch)
}

func TestSubscribeStatusEventCarriesTheSpace(t *testing.T) {
	b, srv := newBackend(t)
	agent := reported("t1", "claude-code", "idle")
	agent.Labels = map[string]string{"herdr.space": "s1", "herdr.tab": "a"}
	srv.Add(agent)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	ch := b.Subscribe(ctx, "t1")
	awaitSubscribers(t, srv, 1)
	srv.SetStatus("t1", &hc.Status{State: "working", App: "claude-code"})
	if ev := receive(t, ch); ev.WorkspaceID != "s1" || ev.State != herdr.StateWorking {
		t.Fatalf("event = %+v", ev)
	}
}

func TestSubscribeEndsForMissingTerminals(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(reported("t1", "claude-code", "idle"))
	ctx := testContext(t)

	expectClosed(t, b.Subscribe(ctx, "t99"))
	expectClosed(t, b.Subscribe(ctx, "m/t1"))

	// A terminal that is removed ends its stream too.
	ch := b.Subscribe(ctx, "t1")
	awaitSubscribers(t, srv, 2)
	srv.Remove("t1")
	expectClosed(t, ch)
}

func TestSubscribeReconnectsAndSignalsResync(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(reported("t1", "claude-code", "idle"))
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	ch := b.Subscribe(ctx, "t1")
	awaitSubscribers(t, srv, 1)
	srv.DropSubscribers()
	receive(t, b.Resync())
	awaitSubscribers(t, srv, 2)

	srv.SetStatus("t1", &hc.Status{State: "working", App: "claude-code"})
	if ev := receive(t, ch); ev.State != herdr.StateWorking {
		t.Fatalf("event after reconnect = %+v", ev)
	}
}

func TestSubscribeScreenFallback(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(liveTerminal("t1", "codex"))
	srv.SetScreen("t1", "OpenAI Codex\n")
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	ch := b.Subscribe(ctx, "t1")
	awaitSubscribers(t, srv, 1)
	expectNone(t, ch, 100*time.Millisecond)

	srv.SetScreen("t1", trustScreen)
	ev := receive(t, ch)
	if ev.State != herdr.StateBlocked || ev.Agent != "codex" {
		t.Fatalf("fallback event = %+v", ev)
	}
	expectNone(t, ch, 100*time.Millisecond) // unchanged screen, no event

	srv.SetScreen("t1", "OpenAI Codex\n")
	if ev := receive(t, ch); ev.State != herdr.StateUnknown {
		t.Fatalf("fallback event = %+v", ev)
	}

	// Once the agent reports, its screen is no longer polled.
	srv.SetStatus("t1", &hc.Status{State: "working", App: "codex"})
	if ev := receive(t, ch); ev.State != herdr.StateWorking {
		t.Fatalf("status event = %+v", ev)
	}
	srv.SetScreen("t1", trustScreen)
	expectNone(t, ch, 150*time.Millisecond)
}

func TestSubscribeFleetEvents(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(liveTerminal("t1", "zsh"))
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	ch := b.SubscribeFleet(ctx)
	awaitSubscribers(t, srv, 1)

	// A shell is not an agent; a known program is, as soon as it is created.
	if _, err := hc.NewClient(srv.Path).Run(ctx, hc.RunParams{Argv: []string{"zsh"}}); err != nil {
		t.Fatal(err)
	}
	started, err := hc.NewClient(srv.Path).Run(ctx, hc.RunParams{Argv: []string{"claude"}})
	if err != nil {
		t.Fatal(err)
	}
	ev := receive(t, ch)
	if ev.Kind != herdr.FleetAgentDetected || ev.PaneID != started.Address || ev.Agent != "claude" ||
		ev.Pane == nil || ev.Pane.PaneID != started.Address {
		t.Fatalf("created event = %+v", ev)
	}

	// The first status of a shell turns it into a detected agent, once.
	srv.SetStatus("t1", &hc.Status{State: "idle", App: "codex"})
	ev = receive(t, ch)
	if ev.Kind != herdr.FleetAgentDetected || ev.PaneID != "t1" || ev.Agent != "codex" {
		t.Fatalf("status event = %+v", ev)
	}
	srv.SetStatus("t1", &hc.Status{State: "working", App: "codex"})
	expectNone(t, ch, 100*time.Millisecond)

	srv.Finish("t1", "exited")
	if ev := receive(t, ch); ev.Kind != herdr.FleetPaneExited || ev.PaneID != "t1" {
		t.Fatalf("finished event = %+v", ev)
	}
	srv.Remove("t1")
	if ev := receive(t, ch); ev.Kind != herdr.FleetPaneClosed || ev.PaneID != "t1" {
		t.Fatalf("removed event = %+v", ev)
	}

	srv.Missed()
	receive(t, b.Resync())

	cancel()
	expectClosed(t, ch)
}

func TestSubscribeFleetSkipsRemoteTerminals(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(reported("m/t1", "claude-code", "idle"))
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	ch := b.SubscribeFleet(ctx)
	awaitSubscribers(t, srv, 1)
	srv.SetStatus("m/t1", &hc.Status{State: "working", App: "claude-code"})
	srv.Finish("m/t1", "exited")
	expectNone(t, ch, 150*time.Millisecond)
}
