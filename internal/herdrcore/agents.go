package herdrcore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// ListAgents returns the live local agent terminals.
func (b *Backend) ListAgents(ctx context.Context) ([]herdr.AgentInfo, error) {
	snap, err := b.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Agents, nil
}

// GetAgent returns one agent by address or name. The error matches
// herdr.ErrAgentNotFound when there is no such terminal or it is not a
// live local agent.
func (b *Backend) GetAgent(ctx context.Context, target string) (herdr.AgentInfo, error) {
	t, err := b.client.Get(ctx, target)
	if err != nil {
		if errors.Is(err, hc.ErrNotFound) {
			return herdr.AgentInfo{}, fmt.Errorf("%w: %w", herdr.ErrAgentNotFound, err)
		}
		return herdr.AgentInfo{}, err
	}
	if isPane(t) && !isAgent(t) {
		if kind, ok := b.launching(t.Address); ok {
			// Typed into the shell, not running yet (hc samples the
			// foreground program about twice a second).
			return herdr.AgentInfo{PaneID: t.Address, TerminalID: t.Address, Agent: kind, Cwd: cwdOf(t), LaunchPending: true}, nil
		}
	}
	b.endLaunch(t.Address)
	if !isPane(t) || !isAgent(t) {
		return herdr.AgentInfo{}, fmt.Errorf("%w: terminal %s is not a live agent", herdr.ErrAgentNotFound, target)
	}
	return agentInfo(t, b.stateOf(ctx, t)), nil
}

// launch is one StartAgent launch: the kind typed, and when it is given
// up on if the program never shows (herdr's agent.start drops it then).
type launch struct {
	kind  string
	until time.Time
}

// DefaultLaunchWait is how long a launch stays pending when StartAgent is
// given no timeout.
const DefaultLaunchWait = 30 * time.Second

func (b *Backend) startLaunch(address, kind string, wait time.Duration) {
	if wait <= 0 {
		wait = DefaultLaunchWait
	}
	b.mu.Lock()
	b.launches[address] = launch{kind: kind, until: time.Now().Add(wait)}
	b.mu.Unlock()
}

// launching is the kind of a launch still pending in address.
func (b *Backend) launching(address string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.launches[address]
	if ok && time.Now().After(l.until) {
		delete(b.launches, address)
		return "", false
	}
	return l.kind, ok
}

func (b *Backend) endLaunch(address string) {
	b.mu.Lock()
	delete(b.launches, address)
	b.mu.Unlock()
}

// Snapshot lists the live local terminals as panes, and the agents among
// them. Workspaces are the herdr-app spaces the terminals' labels name
// (spaces.go).
func (b *Backend) Snapshot(ctx context.Context) (herdr.Snapshot, error) {
	list, err := b.client.List(ctx, false)
	if err != nil {
		return herdr.Snapshot{}, err
	}
	pong, err := b.hello(ctx)
	if err != nil {
		return herdr.Snapshot{}, err
	}
	snap := herdr.Snapshot{
		Version:    pong.Version,
		Protocol:   pong.Protocol,
		Workspaces: []herdr.WorkspaceInfo{},
		Panes:      []herdr.PaneInfo{},
		Agents:     []herdr.AgentInfo{},
	}
	spaces := newSpaceSet()
	for _, t := range list.Terminals {
		if !isPane(t) {
			continue
		}
		state := b.stateOf(ctx, t)
		snap.Panes = append(snap.Panes, paneInfo(t, state))
		if isAgent(t) {
			snap.Agents = append(snap.Agents, agentInfo(t, state))
		}
		spaces.add(t, state)
	}
	snap.Workspaces = spaces.workspaces()
	return snap, nil
}

// hello is the core's version and protocol, asking once and then
// remembering.
func (b *Backend) hello(ctx context.Context) (herdr.Pong, error) {
	b.mu.Lock()
	pong := b.pong
	b.mu.Unlock()
	if pong.Version != "" {
		return pong, nil
	}
	return b.Ping(ctx)
}
