package herdrcore

import (
	"context"
	"errors"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// serveFunc handles one established subscription until its events end or
// ctx is done. It returns true when the stream is over for good (the
// terminal it follows is gone) and must not be re-established.
type serveFunc func(ctx context.Context, sub *hc.Subscription) (done bool)

// stream keeps one hc subscription alive until ctx ends, re-establishing
// it with backoff when the connection drops (logged as herdr.reconnect)
// and signalling Resync after every reconnect. A filter naming a terminal
// that does not exist ends the stream.
func (b *Backend) stream(ctx context.Context, name, paneID string, filter hc.SubscribeFilter, serve serveFunc) {
	var (
		delay    time.Duration
		attempts int
	)
	for {
		sub, err := b.client.Subscribe(ctx, filter)
		if err == nil {
			if attempts > 0 {
				b.signalResync()
			}
			delay = 0
			done := serve(ctx, sub)
			_ = sub.Close()
			if done {
				return
			}
			err = sub.Err()
		}
		if ctx.Err() != nil {
			return
		}
		if filter.Terminal != "" && errors.Is(err, hc.ErrNotFound) {
			b.logEvent("herdr.subscribe_end", paneID, map[string]any{"subscription": name, "error": err.Error()})
			return
		}
		attempts++
		delay = nextDelay(b.backoff(), delay)
		b.logEvent("herdr.reconnect", paneID, map[string]any{
			"subscription": name,
			"attempt":      attempts,
			"delay_ms":     delay.Milliseconds(),
			"error":        errString(err),
		})
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
}

// nextDelay doubles cur within the bounds of bo, starting at bo.Min.
func nextDelay(bo herdr.Backoff, cur time.Duration) time.Duration {
	if cur == 0 {
		return bo.Min
	}
	return min(cur*2, bo.Max)
}

func (b *Backend) backoff() herdr.Backoff {
	if b.Backoff.Min > 0 {
		return b.Backoff
	}
	return herdr.DefaultBackoff
}
