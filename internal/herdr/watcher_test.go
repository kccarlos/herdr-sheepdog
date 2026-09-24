package herdr_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
)

const (
	debounce = 150 * time.Millisecond
	dedupWin = 100 * time.Millisecond
)

// startWatcher runs a Watcher against srv with fast timings and returns it
// once its fleet subscription and the per-pane subscriptions for the panes
// in the initial snapshot are open.
func startWatcher(t *testing.T, srv *herdrtest.Server, snapshotEvery time.Duration, initialPanes int) (*herdr.Watcher, *recLogger, context.CancelFunc) {
	t.Helper()
	c, log := newClient(t, srv)
	w := herdr.NewWatcher(c, herdr.WatcherConfig{Debounce: debounce, SnapshotInterval: snapshotEvery, DedupWindow: dedupWin}, log)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	waitSubs(t, srv, 1+initialPanes)
	return w, log, cancel
}

func recvTransition(t *testing.T, ch <-chan herdr.Transition) herdr.Transition {
	t.Helper()
	select {
	case tr, ok := <-ch:
		if !ok {
			t.Fatal("transitions channel closed")
		}
		return tr
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for transition")
	}
	return herdr.Transition{}
}

func expectNoTransition(t *testing.T, ch <-chan herdr.Transition, d time.Duration) {
	t.Helper()
	select {
	case tr := <-ch:
		t.Fatalf("unexpected transition %+v", tr)
	case <-time.After(d):
	}
}

func expectTransition(t *testing.T, ch <-chan herdr.Transition, from, to herdr.State) herdr.Transition {
	t.Helper()
	tr := recvTransition(t, ch)
	if tr.From != from || tr.To != to {
		t.Fatalf("transition = %s -> %s (%+v), want %s -> %s", tr.From, tr.To, tr, from, to)
	}
	return tr
}

func TestWatcherBootstrapAndImmediateStates(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w5", Label: "sheepdog-recon"})
	srv.SetAgent(codexAgent())
	w, _, _ := startWatcher(t, srv, time.Hour, 1)
	ch := w.Transitions()

	tr := expectTransition(t, ch, "", herdr.StateIdle)
	if tr.PaneID != "w5:p1" || tr.Agent != "codex" || tr.Name != "recon" || tr.WorkspaceLabel != "sheepdog-recon" || tr.Source != "snapshot" {
		t.Errorf("bootstrap transition = %+v", tr)
	}

	srv.EmitStatus("w5:p1", herdr.StateWorking)
	tr = expectTransition(t, ch, herdr.StateIdle, herdr.StateWorking)
	if tr.Source != "socket" || tr.At.IsZero() {
		t.Errorf("working transition = %+v", tr)
	}
	srv.EmitStatus("w5:p1", herdr.StateDone)
	expectTransition(t, ch, herdr.StateWorking, herdr.StateDone)
	// A repeated state beyond the dedup window is not a transition either.
	time.Sleep(dedupWin + 20*time.Millisecond)
	srv.EmitStatus("w5:p1", herdr.StateDone)
	expectNoTransition(t, ch, 2*debounce)
}

func TestWatcherBlockedDebounce(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetAgent(codexAgent())
	w, log, _ := startWatcher(t, srv, time.Hour, 1)
	ch := w.Transitions()
	expectTransition(t, ch, "", herdr.StateIdle)

	srv.EmitStatus("w5:p1", herdr.StateWorking)
	expectTransition(t, ch, herdr.StateIdle, herdr.StateWorking)

	// Flap: blocked for less than the debounce, then working again.
	srv.EmitStatus("w5:p1", herdr.StateBlocked)
	time.Sleep(debounce / 3)
	srv.EmitStatus("w5:p1", herdr.StateWorking)
	expectNoTransition(t, ch, 3*debounce)
	if log.count("herdr.debounce_cancelled") != 1 {
		t.Errorf("debounce_cancelled entries = %d, want 1", log.count("herdr.debounce_cancelled"))
	}

	// A blocked that persists is emitted exactly once, not before the
	// debounce, stamped with the time herdr first reported it.
	start := time.Now()
	srv.EmitStatus("w5:p1", herdr.StateBlocked)
	tr := expectTransition(t, ch, herdr.StateWorking, herdr.StateBlocked)
	if since := time.Since(start); since < debounce {
		t.Errorf("blocked emitted after %v, before the %v debounce", since, debounce)
	}
	if tr.At.Before(start) || tr.At.After(start.Add(debounce)) {
		t.Errorf("transition At %v not within [start, start+debounce]", tr.At)
	}
	expectNoTransition(t, ch, 2*debounce)

	// Blocked → done (Esc declined) is immediate.
	srv.EmitStatus("w5:p1", herdr.StateDone)
	expectTransition(t, ch, herdr.StateBlocked, herdr.StateDone)
}

func TestWatcherDedupAcrossChannels(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetAgent(codexAgent())
	w, log, _ := startWatcher(t, srv, time.Hour, 1)
	ch := w.Transitions()
	expectTransition(t, ch, "", herdr.StateIdle)

	// Same transition on the socket and on the HTTP channel (POST /api/herdr/event).
	srv.EmitStatus("w5:p1", herdr.StateWorking)
	w.Inject(herdr.StatusEvent{PaneID: "w5:p1", WorkspaceID: "w5", Agent: "codex", State: herdr.StateWorking, Source: "http"})
	expectTransition(t, ch, herdr.StateIdle, herdr.StateWorking)
	expectNoTransition(t, ch, debounce)

	srv.EmitStatus("w5:p1", herdr.StateBlocked)
	w.Inject(herdr.StatusEvent{PaneID: "w5:p1", WorkspaceID: "w5", Agent: "codex", State: herdr.StateBlocked, Source: "http"})
	expectTransition(t, ch, herdr.StateWorking, herdr.StateBlocked)
	expectNoTransition(t, ch, 2*debounce)

	if n := log.count("herdr.dedup"); n < 2 {
		t.Errorf("herdr.dedup entries = %d, want >= 2", n)
	}
	// Only the first of each pair became a herdr.state event.
	if n := log.count("herdr.state"); n != 3 { // idle (bootstrap), working, blocked
		t.Errorf("herdr.state entries = %d, want 3", n)
	}
}

func TestWatcherSnapshotDiffReemitsMissedTransition(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetAgent(codexAgent())
	w, _, _ := startWatcher(t, srv, 100*time.Millisecond, 1)
	ch := w.Transitions()
	expectTransition(t, ch, "", herdr.StateIdle)

	// herdr changes state but neither event channel delivers it.
	srv.SetStatus("w5:p1", herdr.StateDone)
	tr := expectTransition(t, ch, herdr.StateIdle, herdr.StateDone)
	if tr.Source != "snapshot" {
		t.Errorf("source = %q, want snapshot", tr.Source)
	}

	// A missed blocked still goes through the debounce.
	start := time.Now()
	srv.SetStatus("w5:p1", herdr.StateBlocked)
	expectTransition(t, ch, herdr.StateDone, herdr.StateBlocked)
	if since := time.Since(start); since < debounce {
		t.Errorf("snapshot-sourced blocked emitted after %v, before the debounce", since)
	}

	// Agent exits without any event: the pane is still there, so it becomes
	// unknown rather than closed.
	srv.RemoveAgent("w5:p1")
	tr = expectTransition(t, ch, herdr.StateBlocked, herdr.StateUnknown)
	if tr.Closed {
		t.Errorf("pane with a shell left should not be Closed: %+v", tr)
	}
	// And when the pane itself vanishes, it is reported closed.
	srv.RemovePane("w5:p1")
	tr = expectTransition(t, ch, herdr.StateUnknown, herdr.StateUnknown)
	if !tr.Closed {
		t.Errorf("vanished pane should be Closed: %+v", tr)
	}
}

func TestWatcherReconnectsAfterServerDropsSubscriptions(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetAgent(codexAgent())
	w, log, _ := startWatcher(t, srv, time.Hour, 1)
	ch := w.Transitions()
	expectTransition(t, ch, "", herdr.StateIdle)

	srv.CloseSubscriptions()
	waitSubs(t, srv, 2) // fleet + one pane, both back

	srv.EmitStatus("w5:p1", herdr.StateWorking)
	expectTransition(t, ch, herdr.StateIdle, herdr.StateWorking)
	if log.count("herdr.reconnect") < 2 {
		t.Errorf("herdr.reconnect entries = %d, want >= 2 (fleet + pane)", log.count("herdr.reconnect"))
	}
}

func TestWatcherFleetDiscoveryAndClose(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w4", Label: "kvoice"})
	srv.SetPane(herdr.PaneInfo{PaneID: "w4:p1", WorkspaceID: "w4"})
	w, _, _ := startWatcher(t, srv, time.Hour, 0)
	ch := w.Transitions()
	expectNoTransition(t, ch, debounce) // a shell without an agent is not tracked

	// New workspace + agent appear, announced by the fleet stream.
	srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w7", Label: "Auth Service"})
	srv.SetAgent(herdr.AgentInfo{PaneID: "w7:p1", WorkspaceID: "w7", Status: herdr.StateUnknown, Agent: "claude"})
	srv.EmitAgentDetected("w7:p1", "w7", "claude", false, "")
	tr := expectTransition(t, ch, "", herdr.StateUnknown)
	if tr.PaneID != "w7:p1" || tr.Agent != "claude" || tr.WorkspaceLabel != "Auth Service" {
		t.Errorf("discovery transition = %+v", tr)
	}
	waitSubs(t, srv, 2)

	srv.EmitStatus("w7:p1", herdr.StateWorking)
	expectTransition(t, ch, herdr.StateUnknown, herdr.StateWorking)

	// Agent exits: released + unknown, then the workspace is closed.
	srv.EmitAgentDetected("w7:p1", "w7", "claude", true, herdr.StateDone)
	srv.RemoveAgent("w7:p1")
	srv.EmitStatus("w7:p1", herdr.StateUnknown)
	expectTransition(t, ch, herdr.StateWorking, herdr.StateUnknown)

	srv.RemovePane("w7:p1")
	srv.EmitWorkspaceClosed("w7")
	tr = expectTransition(t, ch, herdr.StateUnknown, herdr.StateUnknown)
	if !tr.Closed || tr.Source != "workspace_closed" {
		t.Errorf("close transition = %+v", tr)
	}
	waitSubs(t, srv, 1)

	// pane_closed for a second pane.
	srv.SetAgent(herdr.AgentInfo{PaneID: "w4:p2", WorkspaceID: "w4", Status: herdr.StateIdle, Agent: "codex"})
	srv.EmitAgentDetected("w4:p2", "w4", "codex", false, "")
	expectTransition(t, ch, "", herdr.StateIdle)
	srv.RemovePane("w4:p2")
	srv.EmitPaneClosed("w4:p2", "w4")
	tr = expectTransition(t, ch, herdr.StateIdle, herdr.StateUnknown)
	if !tr.Closed || tr.Source != "pane_closed" {
		t.Errorf("pane_closed transition = %+v", tr)
	}
}

// TestWatcherReplayFixture pushes the verbatim capture through the watcher
// at 20 ms spacing. Every `blocked` is therefore a flap shorter than the
// debounce: no blocked transition may come out, while the working/done/idle
// edges are reported as they happened.
func TestWatcherReplayFixture(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w5", Label: "sheepdog-recon"})
	srv.SetAgent(herdr.AgentInfo{PaneID: "w5:p1", WorkspaceID: "w5", Status: herdr.StateUnknown})
	w, log, _ := startWatcher(t, srv, time.Hour, 1)
	ch := w.Transitions()
	expectTransition(t, ch, "", herdr.StateUnknown)

	n := srv.Replay(t, filepath.Join(fixtureDir, "events-agent-status.jsonl"), 20*time.Millisecond)
	if n != 24 {
		t.Fatalf("replayed %d events, want 24", n)
	}

	var got []herdr.State
	deadline := time.After(3 * debounce)
collect:
	for {
		select {
		case tr := <-ch:
			if tr.To == herdr.StateBlocked {
				t.Errorf("flapping blocked leaked through: %+v", tr)
			}
			got = append(got, tr.To)
		case <-deadline:
			break collect
		}
	}
	// Codex: idle working done working [blocked] working done working
	// [blocked] done working done unknown; Claude: [blocked] idle working
	// [blocked] working done unknown. Dropping the bracketed flaps and
	// collapsing the resulting same-state edges (working→working) gives:
	want := []herdr.State{
		"idle", "working", "done", "working", "done", "working", "done", "working", "done", "unknown", // codex
		"idle", "working", "done", "unknown", // claude
	}
	if len(got) != len(want) {
		t.Fatalf("got %d transitions %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("transition %d = %s, want %s (all: %v)", i, got[i], want[i], got)
		}
	}
	if log.count("herdr.debounce_cancelled") != 4 {
		t.Errorf("debounce_cancelled = %d, want 4 (one per fixture blocked)", log.count("herdr.debounce_cancelled"))
	}
}

func TestWatcherInjectAfterStopDoesNotBlock(t *testing.T) {
	srv := herdrtest.New(t)
	w, _, cancel := startWatcher(t, srv, time.Hour, 0)
	cancel()
	for range 100 {
		w.Inject(herdr.StatusEvent{PaneID: "w1:p1", State: herdr.StateWorking})
	}
}

// Known (persisted) panes: one that is gone is reported closed by the
// first snapshot; one still alive with an unchanged state is not re-emitted
// but does get its subscription; a changed state is a normal transition.
func TestWatcherKnownPanesReconciledBySnapshot(t *testing.T) {
	srv := herdrtest.New(t)
	srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w5", Label: "sheepdog-recon"})
	srv.SetAgent(codexAgent()) // w5:p1 idle
	c, log := newClient(t, srv)
	w := herdr.NewWatcher(c, herdr.WatcherConfig{
		Debounce: debounce, SnapshotInterval: time.Hour, DedupWindow: dedupWin,
		Known: map[string]herdr.State{"w5:p1": herdr.StateIdle, "w9:p1": herdr.StateWorking},
	}, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	ch := w.Transitions()

	tr := recvTransition(t, ch)
	if tr.PaneID != "w9:p1" || !tr.Closed || tr.From != herdr.StateWorking || tr.Source != "snapshot" {
		t.Fatalf("stale pane transition = %+v, want w9:p1 closed from working", tr)
	}
	// w5:p1 is unchanged (idle -> idle): no transition, but it is subscribed.
	expectNoTransition(t, ch, 2*debounce)
	waitSubs(t, srv, 2)
	srv.EmitStatus("w5:p1", herdr.StateWorking)
	tr = expectTransition(t, ch, herdr.StateIdle, herdr.StateWorking)
	if tr.Source != "socket" {
		t.Errorf("transition = %+v", tr)
	}
}
