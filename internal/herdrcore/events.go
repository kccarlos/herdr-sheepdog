package herdrcore

import (
	"context"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

var _ herdr.EventSource = (*Backend)(nil)

// Subscribe streams the status changes of one terminal until ctx is
// cancelled, reconnecting with backoff. It also polls the terminal's screen
// while it is an agent that has not reported status (FallbackPoll). The
// channel closes when the terminal does not exist or is removed.
func (b *Backend) Subscribe(ctx context.Context, paneID string) <-chan herdr.StatusEvent {
	out := make(chan herdr.StatusEvent, 16)
	go func() {
		defer close(out)
		if !isLocal(paneID) {
			return
		}
		feed := &paneFeed{b: b, paneID: paneID, out: out}
		b.stream(ctx, "status:"+paneID, paneID, hc.SubscribeFilter{Terminal: paneID}, feed.serve)
	}()
	return out
}

// paneFeed turns one terminal's hc events into StatusEvents.
type paneFeed struct {
	b      *Backend
	paneID string
	out    chan<- herdr.StatusEvent

	kind     string
	fallback bool        // the terminal is an agent that has not reported
	last     herdr.State // the last state emitted or seeded
}

func (f *paneFeed) serve(ctx context.Context, sub *hc.Subscription) bool {
	f.seed(ctx, sub)
	ticker := time.NewTicker(f.b.fallbackPoll())
	defer ticker.Stop()
	for {
		var tick <-chan time.Time
		if f.fallback {
			tick = ticker.C
		}
		select {
		case <-ctx.Done():
			return false
		case <-tick:
			if !f.poll(ctx) {
				return false
			}
		case n, ok := <-sub.Events():
			if !ok {
				return false
			}
			done, alive := f.handle(ctx, n)
			if done || !alive {
				return done
			}
		}
	}
}

// seed takes the terminal's kind and fallback need from the subscription's
// snapshot, and its current screen state as the baseline of the poll.
func (f *paneFeed) seed(ctx context.Context, sub *hc.Subscription) {
	_, terminals := sub.Snapshot()
	for _, t := range terminals {
		if t.Address != f.paneID {
			continue
		}
		f.kind = kindOf(t)
		f.fallback = isPane(t) && needsFallback(t)
		if f.fallback {
			f.last, _ = f.b.screenState(ctx, f.paneID)
		}
	}
}

// poll reads the screen and emits a status event when the fallback state
// changed. It reports false when ctx ended while emitting.
func (f *paneFeed) poll(ctx context.Context) bool {
	state, ok := f.b.screenState(ctx, f.paneID)
	if !ok || state == f.last {
		return true
	}
	f.last = state
	return f.emit(ctx, state)
}

// handle processes one notification. done is true when the terminal is
// gone; alive is false when ctx ended while emitting.
func (f *paneFeed) handle(ctx context.Context, n hc.Notification) (done, alive bool) {
	switch {
	case n.Method == "missed":
		f.b.signalResync()
	case n.Method != "event":
		// A heartbeat: nothing to report.
	case n.Type == "removed":
		return true, true
	case n.Type == "finished" || n.Type == "lost":
		f.fallback = false
	case n.Type == "status":
		var data hc.StatusData
		if err := n.DecodeData(&data); err != nil {
			return false, true
		}
		if data.Status != nil && data.Status.App != "" {
			f.kind = appKind(data.Status.App)
		}
		f.fallback = wantsFallback(data.Status, data.Reported, f.kind)
		state := mapState(data.Status)
		if f.fallback {
			state, _ = f.b.screenState(ctx, f.paneID)
		}
		f.last = state
		return false, f.emit(ctx, state)
	}
	return false, true
}

func (f *paneFeed) emit(ctx context.Context, state herdr.State) bool {
	ev := herdr.StatusEvent{PaneID: f.paneID, Agent: f.kind, State: state, At: time.Now(), Source: "socket"}
	select {
	case f.out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func (b *Backend) fallbackPoll() time.Duration {
	if b.FallbackPoll > 0 {
		return b.FallbackPoll
	}
	return DefaultFallbackPoll
}
