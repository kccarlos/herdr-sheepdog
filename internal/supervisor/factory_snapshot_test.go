package supervisor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

type scriptedSnapshotSource struct {
	mu       sync.Mutex
	snapshot func(ctx context.Context) (herdr.Snapshot, error)
	paneCh   chan herdr.StatusEvent
	fleetCh  chan herdr.FleetEvent
	resyncCh chan struct{}
	subs     atomic.Int32
}

func (s *scriptedSnapshotSource) Snapshot(ctx context.Context) (herdr.Snapshot, error) {
	s.mu.Lock()
	fn := s.snapshot
	s.mu.Unlock()
	return fn(ctx)
}

func (s *scriptedSnapshotSource) Subscribe(ctx context.Context, _ string) <-chan herdr.StatusEvent {
	s.subs.Add(1)
	out := make(chan herdr.StatusEvent)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-s.paneCh:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func (s *scriptedSnapshotSource) SubscribeFleet(context.Context) <-chan herdr.FleetEvent {
	return s.fleetCh
}

func (s *scriptedSnapshotSource) Resync() <-chan struct{} {
	return s.resyncCh
}

func TestFactoryQuietSurvivesTransientSnapshotFailure(t *testing.T) {
	workerLabels := map[string]string{"sheepdog.project": "demo", "sheepdog.role": "worker"}

	t.Run("FleetAgentDetected", func(t *testing.T) {
		fn := &fakeNotifier{}
		h := newHarness(t, withNotifier(fn), func(c *Config) { c.Chattiness = string(Chatty) })
		const pane = "w5:p1"
		h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w5", Label: "demo"})
		h.srv.SetAgent(herdr.AgentInfo{
			PaneID: pane, WorkspaceID: "w5", TabID: "w5:t1",
			Agent: "codex", Status: herdr.StateBlocked, Cwd: "/work/demo", Labels: workerLabels,
		})
		h.srv.SetScreen(pane, fixture(t, "codex-approval-prompt.screen.txt"))

		var failSnap atomic.Bool
		var snapCalls atomic.Int32
		src := &scriptedSnapshotSource{
			paneCh:   make(chan herdr.StatusEvent),
			fleetCh:  make(chan herdr.FleetEvent, 4),
			resyncCh: make(chan struct{}, 4),
		}
		src.snapshot = func(context.Context) (herdr.Snapshot, error) {
			if snapCalls.Add(1) == 1 {
				return herdr.Snapshot{
					Workspaces: []herdr.WorkspaceInfo{{WorkspaceID: "w5", Label: "demo"}},
				}, nil
			}
			if failSnap.Load() {
				return herdr.Snapshot{}, errors.New("transient snapshot error")
			}
			return herdr.Snapshot{
				Workspaces: []herdr.WorkspaceInfo{{WorkspaceID: "w5", Label: "demo"}},
				Panes:      []herdr.PaneInfo{{PaneID: pane, WorkspaceID: "w5"}},
				Agents: []herdr.AgentInfo{{
					PaneID: pane, WorkspaceID: "w5", Agent: "codex",
					Status: herdr.StateDone, Cwd: "/work/demo", Labels: workerLabels,
				}},
			}, nil
		}

		w := herdr.NewWatcher(src, herdr.WatcherConfig{
			Debounce:         20 * time.Millisecond,
			SnapshotInterval: 50 * time.Millisecond,
			DedupWindow:      10 * time.Millisecond,
		}, h.log)
		ctx, cancel := context.WithCancel(context.Background())
		runDone := make(chan error, 1)
		go func() { runDone <- w.Run(ctx) }()

		var mu sync.Mutex
		var transitions []herdr.Transition
		consumed := make(chan struct{})
		go func() {
			defer close(consumed)
			for tr := range w.Transitions() {
				mu.Lock()
				transitions = append(transitions, tr)
				mu.Unlock()
				h.sup.HandleTransition(context.Background(), tr)
			}
		}()

		// Wait for initial empty snapshot before triggering FleetAgentDetected.
		waitFor(t, func() bool { return snapCalls.Load() >= 1 }, "initial snapshot")

		failSnap.Store(true)
		src.fleetCh <- herdr.FleetEvent{
			Kind:        herdr.FleetAgentDetected,
			PaneID:      pane,
			WorkspaceID: "w5",
			Agent:       "codex",
			At:          h.clock.Now(),
		}
		waitFor(t, func() bool { return src.subs.Load() >= 1 }, "pane subscription open")

		// While Snapshot is transiently failing, status events arrive for the worker.
		src.paneCh <- herdr.StatusEvent{PaneID: pane, WorkspaceID: "w5", Agent: "codex", State: herdr.StateWorking, Source: "socket"}
		src.paneCh <- herdr.StatusEvent{PaneID: pane, WorkspaceID: "w5", Agent: "codex", State: herdr.StateBlocked, Source: "socket"}
		time.Sleep(60 * time.Millisecond)

		// Snapshot recovers and returns the worker's labels.
		h.srv.SetScreen(pane, "Done.\n")
		failSnap.Store(false)
		src.resyncCh <- struct{}{}
		src.paneCh <- herdr.StatusEvent{PaneID: pane, WorkspaceID: "w5", Agent: "codex", State: herdr.StateDone, Source: "socket"}
		time.Sleep(80 * time.Millisecond)

		cancel()
		if err := <-runDone; err != nil {
			t.Fatalf("Watcher.Run: %v", err)
		}
		<-consumed

		mu.Lock()
		gotTransitions := append([]herdr.Transition(nil), transitions...)
		mu.Unlock()
		for _, tr := range gotTransitions {
			if tr.PaneID == pane && (tr.Labels["sheepdog.project"] != "demo" || tr.Labels["sheepdog.role"] != "worker") {
				t.Errorf("transition emitted without factory worker labels: %+v", tr)
			}
		}
		if got := h.pusher.all(); len(got) != 0 {
			t.Errorf("quiet worker spoke %d voice push(es) on transient snapshot failure: %+v", len(got), got)
		}
		if got := fn.all(); len(got) != 0 {
			t.Errorf("quiet worker sent %d phone push(es) on transient snapshot failure: %+v", len(got), got)
		}
		if got := h.log.count("pending.created"); got != 0 {
			t.Errorf("quiet worker created %d pending prompt(s), want 0", got)
		}
		if row, ok := h.store.Get(pane); ok {
			t.Errorf("quiet worker stored in state table: %+v", row)
		}
	})

	runReleaseTest := func(t *testing.T, withFleetDetect bool) {
		t.Helper()
		fn := &fakeNotifier{}
		h := newHarness(t, withNotifier(fn), func(c *Config) { c.Chattiness = string(Chatty) })
		const pane = "w5:p1"
		h.srv.SetWorkspace(herdr.WorkspaceInfo{WorkspaceID: "w5", Label: "demo"})
		h.srv.SetAgent(herdr.AgentInfo{
			PaneID: pane, WorkspaceID: "w5", TabID: "w5:t1",
			Agent: "codex", Status: herdr.StateBlocked, Cwd: "/work/demo", Labels: workerLabels,
		})
		h.srv.SetScreen(pane, fixture(t, "codex-approval-prompt.screen.txt"))

		var stateMu sync.Mutex
		var snapCalls atomic.Int32
		failSnap := false
		agents := []herdr.AgentInfo{{
			PaneID: pane, WorkspaceID: "w5", Agent: "claude",
			Status: herdr.StateIdle, Cwd: "/work/demo", Labels: workerLabels,
		}}

		src := &scriptedSnapshotSource{
			paneCh:   make(chan herdr.StatusEvent),
			fleetCh:  make(chan herdr.FleetEvent, 4),
			resyncCh: make(chan struct{}, 4),
		}
		src.snapshot = func(context.Context) (herdr.Snapshot, error) {
			snapCalls.Add(1)
			stateMu.Lock()
			defer stateMu.Unlock()
			if failSnap {
				return herdr.Snapshot{}, errors.New("transient snapshot error")
			}
			outAgents := make([]herdr.AgentInfo, len(agents))
			copy(outAgents, agents)
			return herdr.Snapshot{
				Workspaces: []herdr.WorkspaceInfo{{WorkspaceID: "w5", Label: "demo"}},
				Panes:      []herdr.PaneInfo{{PaneID: pane, WorkspaceID: "w5"}},
				Agents:     outAgents,
			}, nil
		}

		w := herdr.NewWatcher(src, herdr.WatcherConfig{
			Debounce:         20 * time.Millisecond,
			SnapshotInterval: 50 * time.Millisecond,
			DedupWindow:      10 * time.Millisecond,
		}, h.log)
		ctx, cancel := context.WithCancel(context.Background())
		runDone := make(chan error, 1)
		go func() { runDone <- w.Run(ctx) }()

		var mu sync.Mutex
		var transitions []herdr.Transition
		releasedCh := make(chan struct{}, 1)
		consumed := make(chan struct{})
		go func() {
			defer close(consumed)
			for tr := range w.Transitions() {
				mu.Lock()
				transitions = append(transitions, tr)
				mu.Unlock()
				h.sup.HandleTransition(context.Background(), tr)
				if tr.Released {
					select {
					case releasedCh <- struct{}{}:
					default:
					}
				}
			}
		}()

		waitFor(t, func() bool { return src.subs.Load() >= 1 }, "initial pane subscription")

		// Release the previous agent so the shell pane enters p.released = true.
		stateMu.Lock()
		agents = nil
		stateMu.Unlock()
		src.fleetCh <- herdr.FleetEvent{
			Kind:        herdr.FleetAgentDetected,
			PaneID:      pane,
			WorkspaceID: "w5",
			Agent:       "claude",
			Released:    true,
			At:          h.clock.Now(),
		}
		select {
		case <-releasedCh:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for Released transition")
		}

		// Start a new factory worker in the recycled pane while Snapshot fails transiently.
		stateMu.Lock()
		failSnap = true
		agents = []herdr.AgentInfo{{
			PaneID: pane, WorkspaceID: "w5", Agent: "codex",
			Status: herdr.StateDone, Cwd: "/work/demo", Labels: workerLabels,
		}}
		stateMu.Unlock()

		if withFleetDetect {
			beforeSnaps := snapCalls.Load()
			src.fleetCh <- herdr.FleetEvent{
				Kind:        herdr.FleetAgentDetected,
				PaneID:      pane,
				WorkspaceID: "w5",
				Agent:       "codex",
				At:          h.clock.Now(),
			}
			waitFor(t, func() bool { return snapCalls.Load() > beforeSnaps }, "FleetAgentDetected snapshot attempt")
		}
		src.paneCh <- herdr.StatusEvent{PaneID: pane, WorkspaceID: "w5", Agent: "codex", State: herdr.StateWorking, Source: "socket"}
		src.paneCh <- herdr.StatusEvent{PaneID: pane, WorkspaceID: "w5", Agent: "codex", State: herdr.StateBlocked, Source: "socket"}
		time.Sleep(60 * time.Millisecond)

		// Snapshot recovers.
		h.srv.SetScreen(pane, "Done.\n")
		stateMu.Lock()
		failSnap = false
		stateMu.Unlock()
		src.resyncCh <- struct{}{}
		src.paneCh <- herdr.StatusEvent{PaneID: pane, WorkspaceID: "w5", Agent: "codex", State: herdr.StateDone, Source: "socket"}
		time.Sleep(80 * time.Millisecond)

		cancel()
		if err := <-runDone; err != nil {
			t.Fatalf("Watcher.Run: %v", err)
		}
		<-consumed

		mu.Lock()
		gotTransitions := append([]herdr.Transition(nil), transitions...)
		mu.Unlock()
		for _, tr := range gotTransitions {
			if tr.PaneID == pane && (tr.Labels["sheepdog.project"] != "demo" || tr.Labels["sheepdog.role"] != "worker") {
				t.Errorf("post-release transition emitted without factory worker labels: %+v", tr)
			}
		}
		if got := h.pusher.all(); len(got) != 0 {
			t.Errorf("quiet worker spoke %d voice push(es) after releasePane/activate: %+v", len(got), got)
		}
		if got := fn.all(); len(got) != 0 {
			t.Errorf("quiet worker sent %d phone push(es) after releasePane/activate: %+v", len(got), got)
		}
		if got := h.log.count("pending.created"); got != 0 {
			t.Errorf("quiet worker created %d pending prompt(s) after releasePane/activate, want 0", got)
		}
		if row, ok := h.store.Get(pane); ok {
			t.Errorf("quiet worker stored in state table after releasePane/activate: %+v", row)
		}
	}

	t.Run("AfterReleaseAndFleetAgentDetected", func(t *testing.T) {
		runReleaseTest(t, true)
	})
	t.Run("AfterReleaseAndStatusActivate", func(t *testing.T) {
		runReleaseTest(t, false)
	})
}
