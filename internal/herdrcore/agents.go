package herdrcore

import (
	"context"
	"errors"
	"fmt"

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
	if !isPane(t) || !isAgent(t) {
		return herdr.AgentInfo{}, fmt.Errorf("%w: terminal %s is not a live agent", herdr.ErrAgentNotFound, target)
	}
	return agentInfo(t, b.stateOf(ctx, t)), nil
}

// Snapshot lists the live local terminals as panes, and the agents among
// them. There are no workspaces on hc.
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
	for _, t := range list.Terminals {
		if !isPane(t) {
			continue
		}
		state := b.stateOf(ctx, t)
		snap.Panes = append(snap.Panes, paneInfo(t, state))
		if isAgent(t) {
			snap.Agents = append(snap.Agents, agentInfo(t, state))
		}
	}
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
