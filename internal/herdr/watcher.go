package herdr

import (
	"context"
	"time"
)

// Transition is one confirmed agent state change, after dedup and debounce.
type Transition struct {
	PaneID string `json:"pane_id"`
	// From is the previous confirmed state; empty when the pane was just
	// discovered.
	From State     `json:"from,omitempty"`
	To   State     `json:"to"`
	At   time.Time `json:"at"`
	// Agent is the kind label (codex, claude, …); Name the herdr agent name
	// when one was assigned.
	Agent          string `json:"agent,omitempty"`
	Name           string `json:"name,omitempty"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
	WorkspaceLabel string `json:"workspace_label,omitempty"`
	// Closed is set when the pane (or its workspace) went away; To is then
	// StateUnknown and the pane will not be reported again.
	Closed bool `json:"closed,omitempty"`
	// Source is the channel that delivered the triggering event.
	Source string `json:"source,omitempty"`
}

// WatcherConfig tunes the Watcher; zero values take the defaults below.
type WatcherConfig struct {
	// Debounce is how long `blocked` must persist before it is emitted
	// (default 3 s). Other states are emitted immediately.
	Debounce time.Duration
	// SnapshotInterval is the period of the session.snapshot diff (default
	// 30 s).
	SnapshotInterval time.Duration
	// DedupWindow is how long a repeated (pane, state) from another channel
	// is considered the same event (default 2 s).
	DedupWindow time.Duration
	// Known seeds the pane set with panes the caller already has rows for
	// (pane id -> last known state), typically the persisted state table.
	// The first snapshot then reports any of them that no longer exist as
	// closed, instead of leaving a stale row behind after a restart, and
	// does not re-emit the ones whose state is unchanged.
	Known map[string]State
}

func (c WatcherConfig) withDefaults() WatcherConfig {
	if c.Debounce == 0 {
		c.Debounce = 3 * time.Second
	}
	if c.SnapshotInterval == 0 {
		c.SnapshotInterval = 30 * time.Second
	}
	if c.DedupWindow == 0 {
		c.DedupWindow = 2 * time.Second
	}
	return c
}

// Watcher turns herdr's raw event streams into deduplicated, debounced
// Transitions (plan §3.1). It discovers panes through the fleet subscription
// and a periodic session.snapshot diff, keeps one status subscription per
// known pane, accepts a second input channel through Inject (POST
// /api/herdr/event), and funnels every channel through the same
// dedup + debounce path. All bookkeeping is owned by the Run goroutine; no
// locks are needed.
type Watcher struct {
	client *Client
	cfg    WatcherConfig
	log    Logger

	inject chan StatusEvent // Inject()
	status chan StatusEvent // fan-in of per-pane subscriptions
	fired  chan uint64      // debounce timers
	out    chan Transition
	done   chan struct{}

	// Owned by Run.
	panes    map[string]*paneState
	wsLabels map[string]string
	seq      uint64
}

type paneState struct {
	id, workspace, agent, name string

	state State // confirmed state ("" until first emit)

	lastEv   State // dedup: last accepted raw event
	lastEvAt time.Time

	pending *pendingBlock // debounce in flight
	cancel  context.CancelFunc

	// seeded marks a pane from WatcherConfig.Known that has not been seen
	// alive yet (no subscription open).
	seeded bool
}

type pendingBlock struct {
	seq    uint64
	at     time.Time
	source string
	timer  *time.Timer
}

// NewWatcher builds a Watcher over client. log may be nil.
func NewWatcher(client *Client, cfg WatcherConfig, log Logger) *Watcher {
	if log == nil {
		log = NopLogger{}
	}
	return &Watcher{
		client:   client,
		cfg:      cfg.withDefaults(),
		log:      log,
		inject:   make(chan StatusEvent, 64),
		status:   make(chan StatusEvent, 64),
		fired:    make(chan uint64, 64),
		out:      make(chan Transition, 64),
		done:     make(chan struct{}),
		panes:    map[string]*paneState{},
		wsLabels: map[string]string{},
	}
}

// Transitions is the output channel; it is closed when Run returns.
func (w *Watcher) Transitions() <-chan Transition { return w.out }

// Inject feeds a status event from another channel (POST
// /api/herdr/event) into the same dedup/debounce path as the socket subscription. It
// stamps At and Source when they are empty. It never blocks once Run has
// returned.
func (w *Watcher) Inject(ev StatusEvent) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	if ev.Source == "" {
		ev.Source = "inject"
	}
	select {
	case w.inject <- ev:
	case <-w.done:
	}
}

// Run drives the watcher until ctx is cancelled, then closes Transitions().
// The initial snapshot failure is returned; later failures are logged and
// retried on the next tick.
func (w *Watcher) Run(ctx context.Context) error {
	defer close(w.done)
	defer close(w.out)
	// Everything Run starts (subscriptions, debounce timers) hangs off this
	// context so an early error return does not leak goroutines.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fleet := w.client.SubscribeFleet(ctx)

	// Seed persisted panes without a subscription: refreshSnapshot removes
	// the ones that are gone and ensurePane subscribes the rest on sight.
	for id, st := range w.cfg.Known {
		if _, ok := w.panes[id]; !ok && id != "" {
			w.panes[id] = &paneState{id: id, state: st, seeded: true}
		}
	}
	if err := w.refreshSnapshot(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(w.cfg.SnapshotInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			for _, p := range w.panes {
				w.dropPane(p)
			}
			return nil
		case ev, ok := <-fleet:
			if !ok {
				fleet = nil
				continue
			}
			w.handleFleet(ctx, ev)
		case ev := <-w.status:
			w.handleStatus(ctx, ev)
		case ev := <-w.inject:
			w.handleStatus(ctx, ev)
		case seq := <-w.fired:
			w.handleFired(ctx, seq)
		case <-ticker.C:
			if err := w.refreshSnapshot(ctx); err != nil {
				w.log.Log("herdr", "herdr.snapshot_error", "", map[string]any{"error": err.Error()})
			}
		}
	}
}

// refreshSnapshot reconciles the known pane set with session.snapshot and
// feeds every agent's current state through the normal path, so anything
// both event channels missed is re-emitted.
func (w *Watcher) refreshSnapshot(ctx context.Context) error {
	snap, err := w.client.Snapshot(ctx)
	if err != nil {
		return err
	}
	at := time.Now()
	for _, ws := range snap.Workspaces {
		w.wsLabels[ws.WorkspaceID] = ws.Label
	}
	seen := map[string]bool{}
	for _, a := range snap.Agents {
		seen[a.PaneID] = true
		p := w.ensurePane(ctx, a.PaneID, a.WorkspaceID, a.Agent)
		if a.Name != "" {
			p.name = a.Name
		}
		w.applyEvent(ctx, p, StatusEvent{PaneID: a.PaneID, WorkspaceID: a.WorkspaceID, Agent: a.Agent, State: a.Status, At: at, Source: "snapshot"})
	}
	livePanes := map[string]bool{}
	for _, p := range snap.Panes {
		livePanes[p.PaneID] = true
	}
	for id, p := range w.panes {
		if seen[id] {
			continue
		}
		if !livePanes[id] {
			w.removePane(ctx, p, at, "snapshot")
			continue
		}
		// Pane still exists but hosts no agent any more. Panes that never
		// reported a state are left alone (their first status event is
		// probably in flight).
		if p.state == "" {
			continue
		}
		w.applyEvent(ctx, p, StatusEvent{PaneID: id, WorkspaceID: p.workspace, State: StateUnknown, At: at, Source: "snapshot"})
	}
	return nil
}

func (w *Watcher) handleFleet(ctx context.Context, ev FleetEvent) {
	w.log.Log("herdr", "herdr.fleet", ev.PaneID, ev)
	switch ev.Kind {
	case FleetAgentDetected:
		if ev.Released {
			return // the status subscription delivers the following `unknown`
		}
		p := w.ensurePane(ctx, ev.PaneID, ev.WorkspaceID, ev.Agent)
		p.agent = ev.Agent
		// Bootstrap the new pane's state and workspace label right away
		// rather than waiting for the next tick.
		if err := w.refreshSnapshot(ctx); err != nil {
			w.log.Log("herdr", "herdr.snapshot_error", ev.PaneID, map[string]any{"error": err.Error()})
		}
	case FleetPaneClosed, FleetPaneExited:
		if p, ok := w.panes[ev.PaneID]; ok {
			w.removePane(ctx, p, ev.At, string(ev.Kind))
		}
	case FleetWorkspaceClosed:
		for _, p := range w.panes {
			if p.workspace == ev.WorkspaceID {
				w.removePane(ctx, p, ev.At, string(ev.Kind))
			}
		}
		delete(w.wsLabels, ev.WorkspaceID)
	case FleetPaneCreated:
		// No agent yet; pane_agent_detected will follow if one starts.
	}
}

func (w *Watcher) handleStatus(ctx context.Context, ev StatusEvent) {
	if ev.PaneID == "" || !ev.State.Valid() {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	p := w.ensurePane(ctx, ev.PaneID, ev.WorkspaceID, ev.Agent)
	w.applyEvent(ctx, p, ev)
}

// applyEvent is the single dedup + debounce path for every channel.
func (w *Watcher) applyEvent(ctx context.Context, p *paneState, ev StatusEvent) {
	if ev.Agent != "" {
		p.agent = ev.Agent
	}
	if ev.WorkspaceID != "" {
		p.workspace = ev.WorkspaceID
	}
	if ev.State == p.lastEv && ev.At.Sub(p.lastEvAt) < w.cfg.DedupWindow {
		w.log.Log("herdr", "herdr.dedup", p.id, map[string]any{"state": ev.State, "source": ev.Source})
		return
	}
	p.lastEv, p.lastEvAt = ev.State, ev.At
	w.log.Log("herdr", "herdr.state", p.id, map[string]any{
		"state":  ev.State,
		"source": ev.Source,
		"agent":  p.agent,
	})

	if p.pending != nil {
		if ev.State == StateBlocked {
			return // already waiting for this state to settle
		}
		p.pending.timer.Stop()
		w.log.Log("herdr", "herdr.debounce_cancelled", p.id, map[string]any{
			"pending_ms": ev.At.Sub(p.pending.at).Milliseconds(),
			"state":      ev.State,
		})
		p.pending = nil
	}

	if ev.State == StateBlocked && p.state != StateBlocked && w.cfg.Debounce > 0 {
		w.seq++
		seq := w.seq
		p.pending = &pendingBlock{seq: seq, at: ev.At, source: ev.Source}
		p.pending.timer = time.AfterFunc(w.cfg.Debounce, func() {
			select {
			case w.fired <- seq:
			case <-ctx.Done():
			}
		})
		return
	}
	w.emit(ctx, p, ev.State, ev.At, ev.Source, false)
}

func (w *Watcher) handleFired(ctx context.Context, seq uint64) {
	for _, p := range w.panes {
		if p.pending != nil && p.pending.seq == seq {
			pb := p.pending
			p.pending = nil
			w.emit(ctx, p, StateBlocked, pb.at, pb.source, false)
			return
		}
	}
}

func (w *Watcher) emit(ctx context.Context, p *paneState, to State, at time.Time, source string, closed bool) {
	if p.state == to && !closed {
		return
	}
	t := Transition{
		PaneID:         p.id,
		From:           p.state,
		To:             to,
		At:             at,
		Agent:          p.agent,
		Name:           p.name,
		WorkspaceID:    p.workspace,
		WorkspaceLabel: w.wsLabels[p.workspace],
		Closed:         closed,
		Source:         source,
	}
	p.state = to
	w.log.Log("herdr", "herdr.transition", p.id, t)
	select {
	case w.out <- t:
	case <-ctx.Done():
	}
}

// ensurePane returns the pane record, creating it and opening its status
// subscription on first sight.
func (w *Watcher) ensurePane(ctx context.Context, id, workspace, agent string) *paneState {
	p, ok := w.panes[id]
	if ok && !p.seeded {
		return p
	}
	subCtx, cancel := context.WithCancel(ctx)
	if ok {
		// A seeded (persisted) pane seen alive for the first time: keep its
		// last known state so an unchanged state is not re-emitted, and
		// open its subscription now.
		p.seeded, p.cancel = false, cancel
		if p.workspace == "" {
			p.workspace = workspace
		}
		if p.agent == "" {
			p.agent = agent
		}
	} else {
		p = &paneState{id: id, workspace: workspace, agent: agent, cancel: cancel}
		w.panes[id] = p
	}
	ch := w.client.Subscribe(subCtx, id)
	go func() {
		for ev := range ch {
			select {
			case w.status <- ev:
			case <-subCtx.Done():
				return
			}
		}
	}()
	return p
}

// removePane forgets a pane and reports it as closed.
func (w *Watcher) removePane(ctx context.Context, p *paneState, at time.Time, source string) {
	w.dropPane(p)
	delete(w.panes, p.id)
	if p.state != "" {
		w.emit(ctx, p, StateUnknown, at, source, true)
	}
}

func (w *Watcher) dropPane(p *paneState) {
	if p.pending != nil {
		p.pending.timer.Stop()
		p.pending = nil
	}
	if p.cancel != nil {
		p.cancel()
	}
}
