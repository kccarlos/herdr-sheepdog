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
	agents map[string]string
}

// agentSet maps the addresses of live local agent terminals to their kinds.
func agentSet(terminals []hc.Terminal) map[string]string {
	set := make(map[string]string)
	for _, t := range terminals {
		if isPane(t) && isAgent(t) {
			set[t.Address] = kindOf(t)
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
	case "foreground":
		// Re-read the terminal: a known agent may have been typed into an
		// existing shell, or the shell may have replaced an exited one.
		return f.detect(ctx, n.Terminal, true)
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

// detect emits FleetAgentDetected for the terminal when it is a live agent,
// or Released when it stopped being one while the terminal stayed alive. A
// terminal already known is reported again only when fresh (it was just
// created or restarted).
func (f *fleetFeed) detect(ctx context.Context, address string, fromStatus bool) bool {
	t, err := f.b.client.Get(ctx, address)
	if err != nil || !isPane(t) {
		return true
	}
	kind, known := f.agents[address]
	if !isAgent(t) {
		if !known {
			return true
		}
		delete(f.agents, address)
		return f.emit(ctx, herdr.FleetEvent{
			Kind: herdr.FleetAgentDetected, PaneID: address, Agent: kind, Released: true,
		})
	}
	if fromStatus && known {
		return true
	}
	kind = kindOf(t)
	f.agents[address] = kind
	pane := paneInfo(t, f.b.stateOf(ctx, t))
	return f.emit(ctx, herdr.FleetEvent{
		Kind:   herdr.FleetAgentDetected,
		PaneID: address,
		Agent:  kind,
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
