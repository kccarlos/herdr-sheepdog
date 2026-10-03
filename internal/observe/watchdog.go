package observe

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The daemon watchdog (docs/architecture.md "Watchdog"). Every Tick it
// checks that the daemon's long-lived parts are alive:
//
//	brain       the turn holding the turn lock has not run far past its
//	            budget (a hung provider call the timeout did not end)
//	push_queue  no push waits far longer than normal while a listener is
//	            attached (a push held for want of one is normal)
//	herdr       the watcher heard from herdr (a snapshot or an event)
//	            recently; when not, a socket ping says whether herdr itself
//	            answers
//	voice       the voice session's own stall detection (voice.stalled,
//	            EventStatus{Stalled}) — consumed here, not duplicated
//
// A component that fails its check logs daemon.heartbeat_missed
// {component, age_s, detail} once per incident, and daemon.heartbeat_ok
// {component, down_s} when it recovers. It only observes: nothing is
// restarted or cancelled.

// Watchdog limits (constants: no config knob).
const (
	// brainOverrun is how far past its hard budget a running turn may go.
	brainOverrun = 30 * time.Second
	// pushStall is how long a push may wait with a listener attached:
	// the normal wait is the model's turn (TurnTimeout, 20 s), the
	// shepherd's speech and the push gap.
	pushStall = 90 * time.Second
	// herdrPingTimeout bounds the socket ping.
	herdrPingTimeout = 3 * time.Second
)

// Components.
const (
	ComponentBrain     = "brain"
	ComponentPushQueue = "push_queue"
	ComponentHerdr     = "herdr"
	ComponentVoice     = "voice"
)

// Probes are the read-only accessors the watchdog polls; a nil probe
// skips its component.
type Probes struct {
	// Brain is brain.Brain.RunningTurn.
	Brain func() (started time.Time, budget time.Duration, running bool)
	// PushBacklog is voice.PushBacklogger.PushBacklog.
	PushBacklog func() (n int, oldest time.Time)
	// Listener reports whether a /ws client is attached (nil: always).
	Listener func() bool
	// HerdrBeat is the current herdr.Watcher's LastBeat; running is
	// false while no watcher runs (herdr unavailable, between retries).
	HerdrBeat func() (last time.Time, running bool)
	// HerdrPing pings the herdr socket.
	HerdrPing func(ctx context.Context) error
	// HerdrStale is how long the watcher may go without a sign of life
	// (a few snapshot intervals).
	HerdrStale time.Duration
}

// incident is one component's state.
type incident struct {
	missing bool
	since   time.Time
}

// pingResult is the last herdr socket ping.
type pingResult struct {
	at  time.Time
	err error
}

// Watchdog is the liveness Handler.
type Watchdog struct {
	log    logging.Logger
	probes Probes
	now    func() time.Time // the voice stall's and the ping's clock

	mu           sync.Mutex
	voiceStalled bool
	voiceSince   time.Time
	pinging      bool
	ping         pingResult

	incidents map[string]*incident // observer goroutine only
}

// NewWatchdog returns the watchdog over probes.
func NewWatchdog(log logging.Logger, probes Probes) *Watchdog {
	return &Watchdog{log: log, probes: probes, now: time.Now, incidents: map[string]*incident{}}
}

// OnVoiceEvent takes the voice session's stall signal (its own
// detection: EventStatus{Stalled}); any later status clears it. Safe to
// call from the voice event pump.
func (w *Watchdog) OnVoiceEvent(ev voice.Event) {
	st, ok := ev.(voice.EventStatus)
	if !ok {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if st.Stalled && !w.voiceStalled {
		w.voiceSince = w.now()
	}
	w.voiceStalled = st.Stalled
}

// Event implements Handler (the watchdog polls; events are not needed).
func (w *Watchdog) Event(eventlog.Event) {}

// Tick implements Handler: one check of every component.
func (w *Watchdog) Tick(now time.Time) {
	w.report(ComponentBrain, now, w.checkBrain(now))
	w.report(ComponentPushQueue, now, w.checkPushes(now))
	w.report(ComponentHerdr, now, w.checkHerdr(now))
	w.report(ComponentVoice, now, w.checkVoice(now))
}

// miss is a failed check: since when, and what is wrong. A zero miss
// passed.
type miss struct {
	since  time.Time
	detail string
}

func (w *Watchdog) checkBrain(now time.Time) miss {
	if w.probes.Brain == nil {
		return miss{}
	}
	started, budget, running := w.probes.Brain()
	if !running || now.Sub(started) <= budget+brainOverrun {
		return miss{}
	}
	return miss{since: started, detail: fmt.Sprintf("turn running %ds, budget %ds", int(now.Sub(started).Seconds()), int(budget.Seconds()))}
}

func (w *Watchdog) checkPushes(now time.Time) miss {
	if w.probes.PushBacklog == nil {
		return miss{}
	}
	if w.probes.Listener != nil && !w.probes.Listener() {
		return miss{} // held for a listener: normal (voice.push_held)
	}
	n, oldest := w.probes.PushBacklog()
	if n == 0 || now.Sub(oldest) <= pushStall {
		return miss{}
	}
	return miss{since: oldest, detail: fmt.Sprintf("%d push(es) queued, oldest %ds, listener attached", n, int(now.Sub(oldest).Seconds()))}
}

// checkHerdr fails when the watcher has been silent past HerdrStale; it
// pings the socket then (asynchronously: the result is read on a later
// tick, so a hung socket never holds the observer) to say why.
func (w *Watchdog) checkHerdr(now time.Time) miss {
	if w.probes.HerdrBeat == nil {
		return miss{}
	}
	last, running := w.probes.HerdrBeat()
	if running && now.Sub(last) <= w.probes.HerdrStale {
		return miss{}
	}
	w.mu.Lock()
	ping := w.ping
	if w.probes.HerdrPing != nil && !w.pinging {
		w.pinging = true
		go w.pingHerdr()
	}
	w.mu.Unlock()
	socket := "socket not checked yet"
	if !ping.at.IsZero() {
		if ping.err != nil {
			socket = "socket unresponsive: " + ping.err.Error()
		} else {
			socket = "socket answers"
		}
	}
	if !running {
		return miss{since: last, detail: "no watcher running (herdr unavailable); " + socket}
	}
	return miss{since: last, detail: fmt.Sprintf("no snapshot or event for %ds; %s", int(now.Sub(last).Seconds()), socket)}
}

func (w *Watchdog) pingHerdr() {
	ctx, cancel := context.WithTimeout(context.Background(), herdrPingTimeout)
	defer cancel()
	err := w.probes.HerdrPing(ctx)
	w.mu.Lock()
	w.ping = pingResult{at: w.now(), err: err}
	w.pinging = false
	w.mu.Unlock()
}

func (w *Watchdog) checkVoice(time.Time) miss {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.voiceStalled {
		return miss{}
	}
	return miss{since: w.voiceSince, detail: "voice.stalled: the model has not responded to the shepherd's turn"}
}

// report logs the first miss of an incident and its recovery.
func (w *Watchdog) report(component string, now time.Time, m miss) {
	inc := w.incidents[component]
	if inc == nil {
		inc = &incident{}
		w.incidents[component] = inc
	}
	switch {
	case m.detail != "" && !inc.missing:
		inc.missing, inc.since = true, m.since
		if inc.since.IsZero() || inc.since.After(now) {
			inc.since = now
		}
		w.log.Log(logSource, "daemon.heartbeat_missed", "", map[string]any{
			"component": component, "age_s": int(now.Sub(inc.since).Seconds()), "detail": m.detail,
		})
	case m.detail == "" && inc.missing:
		w.log.Log(logSource, "daemon.heartbeat_ok", "", map[string]any{"component": component, "down_s": int(now.Sub(inc.since).Seconds())})
		inc.missing = false
	}
}
