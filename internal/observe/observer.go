// Package observe holds the daemon's observers (the Pipecat observer
// pattern, docs/architecture.md "Observers"): consumers of the event log
// that derive something from it — the per-utterance latency breakdown
// (turn.latency, latency.go), the daemon watchdog (watchdog.go) — without
// slowing the main path. They read a lossy tap of the log
// (eventlog.Tap): Log only offers each event to a buffered channel, never
// waits, and an event that does not fit is dropped and counted; the
// observer logs the count (observe.dropped) at most once per tick. All
// the work — decoding payloads, matching events, emitting results — runs
// on the observer's own goroutine.
package observe

import (
	"context"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// Constants (no config knobs: docs/architecture.md "Observers").
const (
	// TapBuffer is the observers' event buffer: about a minute of the
	// busiest log seen (herdr.state bursts, web.client.stats every
	// second), far more than one tick's worth.
	TapBuffer = 4096
	// Tick is how often handlers are ticked (timeouts, liveness checks)
	// and the drop count is checked.
	Tick = 5 * time.Second
)

// logSource is the source of every event the observers log.
const logSource = "observe"

// Handler is one observer. Event and Tick are called on the observer's
// goroutine only, never concurrently.
type Handler interface {
	// Event is called for every event of the tap, in log order.
	Event(ev eventlog.Event)
	// Tick is called every Tick.
	Tick(now time.Time)
}

// Observer feeds the handlers from a tap.
type Observer struct {
	log      logging.Logger
	tap      *eventlog.Tap
	handlers []Handler
	tick     time.Duration
	now      func() time.Time

	reported uint64 // drops already logged
}

// New returns an observer over tap. log receives observe.dropped (and is
// what the handlers log to, typically the same event log).
func New(log logging.Logger, tap *eventlog.Tap, handlers ...Handler) *Observer {
	return &Observer{log: log, tap: tap, handlers: handlers, tick: Tick, now: time.Now}
}

// Run feeds the handlers until ctx ends or the tap closes, then closes
// the tap.
func (o *Observer) Run(ctx context.Context) {
	defer o.tap.Close()
	t := time.NewTicker(o.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-o.tap.C():
			if !ok {
				return
			}
			for _, h := range o.handlers {
				h.Event(ev)
			}
		case <-t.C:
			o.Tick(o.now())
		}
	}
}

// Tick logs the drops since the last tick, then ticks every handler.
func (o *Observer) Tick(now time.Time) {
	if d := o.tap.Dropped(); d > o.reported {
		o.log.Log(logSource, "observe.dropped", "", map[string]any{"dropped": d - o.reported, "total": d, "buffer": TapBuffer})
		o.reported = d
	}
	for _, h := range o.handlers {
		h.Tick(now)
	}
}
