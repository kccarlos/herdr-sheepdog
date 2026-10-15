package inject

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// TransitionHandler is the supervisor entry point fake transitions go
// through; *supervisor.Supervisor satisfies it.
type TransitionHandler interface {
	HandleTransition(ctx context.Context, t herdr.Transition)
}

// Injector turns debug requests into synthetic panes and supervisor
// transitions.
type Injector struct {
	store *state.Store
	herdr *Herdr
	sup   TransitionHandler
	log   logging.Logger
	now   func() time.Time
}

// NewInjector wires an Injector. log and now may be nil.
func NewInjector(store *state.Store, h *Herdr, sup TransitionHandler, log logging.Logger, now func() time.Time) *Injector {
	if log == nil {
		log = logging.NopLogger{}
	}
	if now == nil {
		now = time.Now
	}
	return &Injector{store: store, herdr: h, sup: sup, log: log, now: now}
}

// BlockedRequest describes a fake blocked pane.
type BlockedRequest struct {
	PaneID string `json:"pane_id"`
	// Alias pins the spoken name (optional; derived from Kind otherwise).
	Alias string `json:"alias,omitempty"`
	// Kind is the agent kind label (default "codex").
	Kind string `json:"kind,omitempty"`
	// Screen is the pane text the supervisor will read.
	Screen string `json:"screen"`
}

// ErrNotSynthetic is returned when a fake state change targets a pane
// that herdr owns.
var ErrNotSynthetic = errors.New("inject: pane is not synthetic")

// Blocked registers (or updates) the synthetic pane, stores its screen and
// runs the supervisor's blocked path synchronously: the pane read, the
// prompt parse, the pending action and the push all happen before
// it returns. The returned row is the state after the chain.
func (i *Injector) Blocked(ctx context.Context, req BlockedRequest) (state.Agent, error) {
	req.PaneID = strings.TrimSpace(req.PaneID)
	if req.PaneID == "" {
		return state.Agent{}, errors.New("inject: pane_id is required")
	}
	if strings.TrimSpace(req.Screen) == "" {
		return state.Agent{}, errors.New("inject: screen is required")
	}
	if req.Kind == "" {
		req.Kind = "codex"
	}
	if row, ok := i.store.Get(req.PaneID); ok && !row.Synthetic {
		return state.Agent{}, fmt.Errorf("%w: %s", ErrNotSynthetic, req.PaneID)
	}
	i.herdr.SetScreen(req.PaneID, req.Kind, req.Screen, herdr.StateBlocked)
	from, err := i.ensureRow(req.PaneID, req.Kind, req.Alias)
	if err != nil {
		return state.Agent{}, err
	}
	i.log.Log("web", "web.inject", req.PaneID, map[string]any{"kind": "blocked", "alias": req.Alias, "agent": req.Kind, "screen_bytes": len(req.Screen)})
	i.transition(ctx, req.PaneID, req.Kind, from, herdr.StateBlocked)
	row, _ := i.store.Get(req.PaneID)
	return row, nil
}

// PaneRequest describes a synthetic pane registered in any state (the
// voice bench's scripted workers start working, idle or done, not only
// blocked).
type PaneRequest struct {
	PaneID string `json:"pane_id"`
	// Alias pins the spoken name (optional).
	Alias string `json:"alias,omitempty"`
	// Kind is the agent kind label (default "codex").
	Kind string `json:"kind,omitempty"`
	// State is working, idle, done or blocked (default idle).
	State string `json:"state,omitempty"`
	// Screen is the pane text the supervisor will read.
	Screen string `json:"screen"`
}

// Pane registers a synthetic pane in req.State, or updates one, and runs
// its transition through the supervisor. A new pane is reported as coming
// from unknown (as the herdr watcher's first snapshot reports a pane it
// had not seen), so it is never mistaken for a finish; blocked takes the
// Blocked path.
func (i *Injector) Pane(ctx context.Context, req PaneRequest) (state.Agent, error) {
	req.PaneID = strings.TrimSpace(req.PaneID)
	to := herdr.State(strings.ToLower(strings.TrimSpace(req.State)))
	if to == "" {
		to = herdr.StateIdle
	}
	if to == herdr.StateBlocked {
		return i.Blocked(ctx, BlockedRequest{PaneID: req.PaneID, Alias: req.Alias, Kind: req.Kind, Screen: req.Screen})
	}
	switch {
	case req.PaneID == "":
		return state.Agent{}, errors.New("inject: pane_id is required")
	case strings.TrimSpace(req.Screen) == "":
		return state.Agent{}, errors.New("inject: screen is required")
	case !to.Valid() || to == herdr.StateUnknown:
		return state.Agent{}, fmt.Errorf("inject: invalid state %q", to)
	}
	if req.Kind == "" {
		req.Kind = "codex"
	}
	row, ok := i.store.Get(req.PaneID)
	if ok && !row.Synthetic {
		return state.Agent{}, fmt.Errorf("%w: %s", ErrNotSynthetic, req.PaneID)
	}
	from := herdr.StateUnknown
	if ok {
		from = row.State
	} else {
		var err error
		row, _, err = i.store.Apply(herdr.Transition{PaneID: req.PaneID, To: to, At: i.now(), Agent: req.Kind, WorkspaceID: "synthetic", Source: state.SourceInject})
		if err != nil {
			return state.Agent{}, err
		}
	}
	if err := i.store.MarkSynthetic(req.PaneID); err != nil {
		return state.Agent{}, err
	}
	if alias := strings.TrimSpace(req.Alias); alias != "" && alias != row.Alias {
		if err := i.store.SetAlias(req.PaneID, alias); err != nil {
			return state.Agent{}, err
		}
	}
	i.herdr.SetScreen(req.PaneID, req.Kind, req.Screen, to)
	i.log.Log("web", "web.inject", req.PaneID, map[string]any{"kind": "pane", "state": string(to), "alias": req.Alias, "agent": req.Kind, "screen_bytes": len(req.Screen)})
	i.transition(ctx, req.PaneID, req.Kind, from, to)
	row, _ = i.store.Get(req.PaneID)
	return row, nil
}

// SetState moves a synthetic pane to another state (done, working, idle,
// unknown) through the supervisor. A non-empty screen replaces the pane
// text first, so a "done" can show what the agent finished with.
func (i *Injector) SetState(ctx context.Context, paneID string, to herdr.State, screen string) (state.Agent, error) {
	paneID = strings.TrimSpace(paneID)
	if !to.Valid() {
		return state.Agent{}, fmt.Errorf("inject: invalid state %q", to)
	}
	row, ok := i.store.Get(paneID)
	if !ok || !row.Synthetic || !i.herdr.IsSynthetic(paneID) {
		return state.Agent{}, fmt.Errorf("%w: %s", ErrNotSynthetic, paneID)
	}
	if strings.TrimSpace(screen) != "" {
		i.herdr.SetScreen(paneID, "", screen, to)
	} else {
		i.herdr.SetStatus(paneID, to)
	}
	i.log.Log("web", "web.inject", paneID, map[string]any{"kind": string(to), "screen_bytes": len(screen)})
	i.transition(ctx, paneID, row.Kind, row.State, to)
	row, _ = i.store.Get(paneID)
	return row, nil
}

// Screen replaces a synthetic pane's screen text without moving its
// state or touching the supervisor: the mock's counterpart to a worker
// showing a new prompt while herdr's own status report stays unchanged
// (docs/dev/roles.md "blocked -> blocked is a block") — the post-answer
// poll or the reconciler is what is meant to notice it, not this call.
func (i *Injector) Screen(paneID, screen string) (state.Agent, error) {
	paneID = strings.TrimSpace(paneID)
	if strings.TrimSpace(screen) == "" {
		return state.Agent{}, errors.New("inject: screen is required")
	}
	row, ok := i.store.Get(paneID)
	if !ok || !row.Synthetic || !i.herdr.IsSynthetic(paneID) {
		return state.Agent{}, fmt.Errorf("%w: %s", ErrNotSynthetic, paneID)
	}
	i.herdr.SetScreen(paneID, "", screen, row.State)
	i.log.Log("web", "web.inject", paneID, map[string]any{"kind": "screen", "screen_bytes": len(screen)})
	return row, nil
}

// Remove closes a synthetic pane: the supervisor sees a Closed transition
// and the row and screen are dropped.
func (i *Injector) Remove(ctx context.Context, paneID string) error {
	paneID = strings.TrimSpace(paneID)
	row, ok := i.store.Get(paneID)
	if !ok || !row.Synthetic {
		return fmt.Errorf("%w: %s", ErrNotSynthetic, paneID)
	}
	i.log.Log("web", "web.inject", paneID, map[string]any{"kind": "remove"})
	t := herdr.Transition{PaneID: paneID, From: row.State, To: herdr.StateUnknown, At: i.now(), Agent: row.Kind, Closed: true, Source: state.SourceInject}
	i.log.Log("herdr", "herdr.transition", paneID, t)
	i.sup.HandleTransition(ctx, t)
	i.herdr.Remove(paneID)
	return nil
}

// ensureRow creates the state row for a new synthetic pane (seeded as
// working, so the blocked transition looks like a real one), applies the
// alias and the synthetic flag, and returns the state the pane was in.
func (i *Injector) ensureRow(paneID, kind, alias string) (herdr.State, error) {
	row, ok := i.store.Get(paneID)
	if !ok {
		var err error
		row, _, err = i.store.Apply(herdr.Transition{PaneID: paneID, To: herdr.StateWorking, At: i.now(), Agent: kind, WorkspaceID: "synthetic", Source: state.SourceInject})
		if err != nil {
			return "", err
		}
	}
	if err := i.store.MarkSynthetic(paneID); err != nil {
		return "", err
	}
	if alias = strings.TrimSpace(alias); alias != "" && alias != row.Alias {
		if err := i.store.SetAlias(paneID, alias); err != nil {
			return "", err
		}
	}
	return row.State, nil
}

// transition logs the same herdr.state / herdr.transition pair the Watcher
// would and hands the transition to the supervisor.
func (i *Injector) transition(ctx context.Context, paneID, kind string, from, to herdr.State) {
	at := i.now()
	i.log.Log("herdr", "herdr.state", paneID, map[string]any{"state": to, "source": state.SourceInject, "agent": kind})
	t := herdr.Transition{PaneID: paneID, From: from, To: to, At: at, Agent: kind, WorkspaceID: "synthetic", Source: state.SourceInject}
	i.log.Log("herdr", "herdr.transition", paneID, t)
	i.sup.HandleTransition(ctx, t)
}
