package herdrcore

import (
	"context"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// SubscribeFleet streams the lifecycle events used to discover agents from
// one unfiltered hc subscription until ctx is cancelled, reconnecting with
// backoff.
func (b *Backend) SubscribeFleet(ctx context.Context) <-chan herdr.FleetEvent {
	out := make(chan herdr.FleetEvent, 16)
	go func() {
		defer close(out)
		b.stream(ctx, "fleet", "", hc.SubscribeFilter{}, func(ctx context.Context, sub *hc.Subscription) bool {
			_, terminals := sub.Snapshot()
			feed := &fleetFeed{b: b, out: out, agents: agentSet(terminals)}
			feed.serve(ctx, sub)
			return false
		})
	}()
	return out
}

// fleetFeed turns hc events into FleetEvents. agents holds the terminals
// already known to be agents, so the first sign of a new one is reported
// once.
type fleetFeed struct {
	b      *Backend
	out    chan<- herdr.FleetEvent
	agents map[string]bool
}

// agentSet is the addresses of the live local agent terminals.
func agentSet(terminals []hc.Terminal) map[string]bool {
	set := make(map[string]bool)
	for _, t := range terminals {
		if isPane(t) && isAgent(t) {
			set[t.Address] = true
		}
	}
	return set
}

func (f *fleetFeed) serve(ctx context.Context, sub *hc.Subscription) {
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-sub.Events():
			if !ok || !f.handle(ctx, n) {
				return
			}
		}
	}
}

// handle processes one notification. It reports false when ctx ended while
// emitting.
func (f *fleetFeed) handle(ctx context.Context, n hc.Notification) bool {
	if n.Method == "missed" {
		f.agents = agentSet(n.Terminals)
		f.b.signalResync()
		return true
	}
	if n.Method != "event" || !isLocal(n.Terminal) {
		return true
	}
	switch n.Type {
	case "created", "restarted":
		return f.detect(ctx, n.Terminal, false)
	case "status":
		var data hc.StatusData
		if err := n.DecodeData(&data); err != nil || !data.Reported {
			return true
		}
		return f.detect(ctx, n.Terminal, true)
	case "finished", "lost":
		delete(f.agents, n.Terminal)
		return f.emit(ctx, herdr.FleetEvent{Kind: herdr.FleetPaneExited, PaneID: n.Terminal})
	case "removed":
		delete(f.agents, n.Terminal)
		return f.emit(ctx, herdr.FleetEvent{Kind: herdr.FleetPaneClosed, PaneID: n.Terminal})
	}
	return true
}

// detect emits FleetAgentDetected for the terminal when it is a live agent.
// A terminal already known is reported again only when fresh (it was just
// created or restarted).
func (f *fleetFeed) detect(ctx context.Context, address string, fromStatus bool) bool {
	if fromStatus && f.agents[address] {
		return true
	}
	t, err := f.b.client.Get(ctx, address)
	if err != nil || !isPane(t) || !isAgent(t) {
		return true
	}
	f.agents[address] = true
	pane := paneInfo(t, f.b.stateOf(ctx, t))
	return f.emit(ctx, herdr.FleetEvent{
		Kind:   herdr.FleetAgentDetected,
		PaneID: address,
		Agent:  kindOf(t),
		Pane:   &pane,
	})
}

func (f *fleetFeed) emit(ctx context.Context, ev herdr.FleetEvent) bool {
	ev.At = time.Now()
	select {
	case f.out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
