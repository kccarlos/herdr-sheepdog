package web

import (
	"context"
	"errors"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/wsproto"
)

// The agent window over /ws (docs/ws-contract.md "Agent window over /ws"):
// a client subscribes to one tracked agent's screen and the daemon pushes
// a screen frame whenever the window changed, instead of the app polling
// GET /api/agents/{pane_id}/screen.
//
// herdr offers no plain output-changed event to wait on (events.wait
// refuses output matches; a catch-all pane.output_matched subscription was
// not evaluated), so the daemon reads the one subscribed window itself: every
// Deps.ScreenInterval, and sooner (never closer than Deps.ScreenMinGap to
// the last read) when the event log shows the watcher's debounced
// herdr.transition or a herdr.send for that pane. Only a changed hash is
// sent. There is one /ws client at a time and at most one subscription
// per client, so the daemon reads at most one window this way.

// screenKickTypes are the event-log types that ask for an early read of
// the subscribed pane: the watcher's debounced state change and anything
// sent to the agent (the window's own input among them).
var screenKickTypes = map[string]bool{"herdr.transition": true, "herdr.send": true}

// screenReadTimeout bounds one window read.
const screenReadTimeout = 5 * time.Second

// screenSub is a client's one screen subscription.
type screenSub struct {
	pane   string
	lines  int
	cancel context.CancelFunc
	// reason is why it ended, set before cancel (under client.mu).
	reason string
}

// subscribeScreen starts (or replaces) the client's screen subscription.
func (c *client) subscribeScreen(pane string, lines int) {
	if c.h.s.deps.Supervisor == nil {
		c.enqueue(controlFrame(wsproto.ScreenError(pane, "no supervisor", true)))
		return
	}
	c.stopScreen("resubscribe")
	ctx, cancel := context.WithCancel(context.Background())
	sub := &screenSub{pane: pane, lines: lines, cancel: cancel}
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		cancel()
		return
	}
	c.screen = sub
	c.mu.Unlock()
	c.h.s.log.Log(logSource, "web.screen.subscribe", pane, map[string]any{"client_id": c.id, "lines": lines})
	go c.screenLoop(ctx, sub)
}

// stopScreen ends the client's screen subscription, if any.
func (c *client) stopScreen(reason string) {
	c.mu.Lock()
	sub := c.screen
	c.screen = nil
	if sub != nil && sub.reason == "" {
		sub.reason = reason
	}
	c.mu.Unlock()
	if sub != nil {
		sub.cancel()
	}
}

// endScreen clears sub if it is still the client's subscription (the
// loop ended on its own: the agent is gone).
func (c *client) endScreen(sub *screenSub, reason string) {
	c.mu.Lock()
	if c.screen == sub {
		c.screen = nil
	}
	if sub.reason == "" {
		sub.reason = reason
	}
	c.mu.Unlock()
	sub.cancel()
}

// screenKicks forwards the event-log lines that ask for an early read of
// pane, until ctx ends.
func (c *client) screenKicks(ctx context.Context, pane string) <-chan struct{} {
	kick := make(chan struct{}, 1)
	if c.h.s.deps.Log == nil {
		return kick
	}
	events := c.h.s.deps.Log.Subscribe(ctx)
	go func() {
		for ev := range events {
			if ev.PaneID != pane || !screenKickTypes[ev.Type] {
				continue
			}
			select {
			case kick <- struct{}{}:
			default:
			}
		}
	}()
	return kick
}

// screenLoop reads the subscribed window and sends a screen frame when it
// changed, until the subscription or the connection ends.
func (c *client) screenLoop(ctx context.Context, sub *screenSub) {
	deps := c.h.s.deps
	frames, reads, failures := 0, 0, 0
	defer func() {
		c.mu.Lock()
		reason := sub.reason
		c.mu.Unlock()
		if reason == "" {
			reason = "disconnect"
		}
		c.h.s.log.Log(logSource, "web.screen.end", sub.pane, map[string]any{
			"client_id": c.id, "reason": reason, "frames": frames, "reads": reads, "failures": failures,
		})
	}()
	kicks := c.screenKicks(ctx, sub.pane)
	timer := time.NewTimer(0)
	defer timer.Stop()
	due := time.Now()
	var last time.Time
	lastHash := ""
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			c.endScreen(sub, "disconnect")
			return
		case <-kicks:
			at := time.Now().Add(deps.ScreenMinGap)
			if min := last.Add(deps.ScreenMinGap); min.After(at) {
				at = min
			}
			if at.Before(due) {
				due = at
				timer.Reset(time.Until(at))
			}
			continue
		case <-timer.C:
		}
		rctx, cancel := context.WithTimeout(ctx, screenReadTimeout)
		scr, err := deps.Supervisor.Screen(rctx, sub.pane, sub.lines)
		cancel()
		last = time.Now()
		due = last.Add(deps.ScreenInterval)
		timer.Reset(deps.ScreenInterval)
		reads++
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, supervisor.ErrNoSuchAgent):
			c.enqueue(controlFrame(wsproto.ScreenError(sub.pane, err.Error(), true)))
			c.endScreen(sub, "gone")
			return
		case err != nil:
			failures++
			if !failing {
				c.enqueue(controlFrame(wsproto.ScreenError(sub.pane, err.Error(), false)))
			}
			failing, lastHash = true, ""
			continue
		}
		failing = false
		if scr.Hash == lastHash {
			continue
		}
		lastHash = scr.Hash
		frames++
		c.enqueue(controlFrame(wsproto.ScreenFrame(screenFrame(scr))))
	}
}

// screenFrame is the wire form of one window read.
func screenFrame(s supervisor.Screen) wsproto.Screen {
	f := wsproto.Screen{
		PaneID: s.PaneID, Alias: s.Alias, Spoken: s.Spoken, Kind: s.Kind, State: string(s.State),
		Text: s.Text, Lines: s.Lines, Truncated: s.Truncated, Hash: s.Hash,
	}
	if !s.StateSince.IsZero() {
		f.StateSince = s.StateSince.Format(time.RFC3339Nano)
	}
	return f
}
