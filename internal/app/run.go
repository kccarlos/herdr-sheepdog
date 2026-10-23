package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/observe"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Run starts every goroutine and blocks until ctx ends, then shuts down
// in order. It calls Listen when it has not been called yet. The event
// log is closed last.
func (a *App) Run(ctx context.Context) (err error) {
	if a.web.Addr() == "" {
		if err := a.Listen(); err != nil {
			return err
		}
	}
	brainMode := a.BrainMode()
	a.log.Log("daemon", "daemon.start", "", map[string]any{
		"version": a.opts.Version, "voice": a.opts.Voice, "herdr": a.opts.Herdr, "cli": a.opts.CLI, "brain": brainMode, "url": a.URL(), "urls": a.URLs(),
		"data_dir": a.cfg.DataDir, "brain_provider": a.BrainProvider(), "brain_model": a.BrainModel(), "voice_provider": a.VoiceProvider(),
	})
	a.restoreBrainModel()
	a.restoreVoiceProvider()
	if a.pushErr != nil {
		a.log.Log("push", "push.disabled", "", map[string]any{"reason": a.pushErr.Error()})
	} else {
		a.push.LogStartup()
	}
	// Logged here, not in wire(), so it never precedes daemon.start (the
	// Loader itself is built in wire(), since the orchestrator and the voice
	// session need it before Run).
	a.log.Log("daemon", "prompts.loaded", "", map[string]any{"dir": a.cfg.Prompts.Dir, "files": a.prompts.Files()})
	if a.queryLazy != nil {
		if runner, err := a.buildQuerier(); err != nil {
			a.log.Log("daemon", "daemon.cli_error", "", map[string]any{"error": err.Error()})
		} else if runner != nil {
			a.queryLazy.set(runner)
		}
	}
	start := time.Now()
	defer func() {
		// A notification in flight finishes (its push.sent/failed line
		// logged) before daemon.stop; each is bounded by its own timeout.
		a.push.Wait()
		a.log.Log("daemon", "daemon.stop", "", map[string]any{"version": a.opts.Version, "uptime_s": int(time.Since(start).Seconds()), "error": errString(err)})
		// Flush any debounced hardware-durable fsync (state.full_fsync)
		// before the event log closes; harmless no-op otherwise.
		_ = a.store.Close()
		if a.brain != nil {
			_ = a.brain.Close()
		}
		if cerr := a.elog.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	inner, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { defer wg.Done(); a.observer.Run(inner) }()

	if a.wref != nil {
		wg.Add(1)
		go func() { defer wg.Done(); a.runWatcher(inner) }()
	}
	if a.voice != nil {
		// The voice starts in the background and never stops the
		// daemon: without a key it stays off (setup mode); a failed
		// connect (a bad key, no network) is retried with a backoff
		// while the web server, herdr and the supervisor run, so a
		// login service's KeepAlive / Restart never loops on it.
		wg.Add(3)
		go func() { defer wg.Done(); a.startVoice(inner) }()
		go func() {
			defer wg.Done()
			for ev := range a.voice.Events() {
				a.watchdog.OnVoiceEvent(ev)
				a.sup.OnVoiceEvent(ev)
				a.web.OnVoiceEvent(ev)
			}
		}()
		go func() { defer wg.Done(); a.web.Run(inner) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); a.sup.Run(inner) }()

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.web.Serve(inner, a.opts.Drain) }()

	restart := false
	select {
	case <-ctx.Done():
	case <-a.restart:
		restart = true
	case err = <-serveErr:
		cancel()
		if a.voice != nil {
			_ = a.voice.Close()
		}
		wg.Wait()
		return err
	}

	// Shutdown: stop accepting, drain HTTP, then the session and the rest.
	cancel()
	if serr := <-serveErr; serr != nil && !errors.Is(serr, context.Canceled) {
		a.log.Log("daemon", "daemon.web_error", "", map[string]any{"error": serr.Error()})
	}
	if a.voice != nil {
		_ = a.voice.Close()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(a.opts.Drain):
		a.log.Log("daemon", "daemon.drain_timeout", "", nil)
	}
	if restart {
		return ErrRestart
	}
	return nil
}

// observers are the handlers the observer feeds (internal/observe).
func (a *App) observers() []observe.Handler {
	a.watchdog = observe.NewWatchdog(a.log, a.watchdogProbes())
	return []observe.Handler{observe.NewLatencyObserver(a.log), a.watchdog}
}

// watchdogProbes are the read-only accessors the watchdog polls; a
// component the daemon runs without (no orchestrator, --voice=off,
// --herdr=mock) has none.
func (a *App) watchdogProbes() observe.Probes {
	var p observe.Probes
	if a.brain != nil {
		p.Brain = a.brain.RunningTurn
	}
	if pb, ok := a.voice.(voice.PushBacklogger); ok {
		p.PushBacklog = pb.PushBacklog
	}
	if li, ok := a.voice.(supervisor.Listener); ok {
		p.Listener = li.HasListener
	}
	if a.wref != nil && a.hc != nil {
		p.HerdrBeat = a.wref.beat
		p.HerdrPing = func(ctx context.Context) error {
			_, err := a.hc.Ping(ctx)
			return err
		}
		// Three missed snapshots (and no event meanwhile).
		iv := time.Duration(a.cfg.SnapshotIntervalS) * time.Second
		if iv <= 0 {
			iv = 30 * time.Second
		}
		p.HerdrStale = 3*iv + 10*time.Second
	}
	return p
}

// runWatcher keeps a Watcher running: transitions are handled serialised
// per pane; a Watcher whose initial snapshot fails (herdr not running) is
// retried after WatcherRetry.
func (a *App) runWatcher(ctx context.Context) {
	var panes paneLocks
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		// Seed the watcher with the persisted rows so the first snapshot
		// closes panes that went away while the daemon was down (WP7b: a
		// closed workspace's agent survived a restart and was offered in
		// clarify questions).
		known := map[string]herdr.State{}
		for _, row := range a.store.List() {
			if !row.Synthetic {
				known[row.PaneID] = row.State
			}
		}
		w := herdr.NewWatcher(a.hc, herdr.WatcherConfig{
			Debounce:         time.Duration(a.cfg.DebounceMS) * time.Millisecond,
			SnapshotInterval: time.Duration(a.cfg.SnapshotIntervalS) * time.Second,
			Known:            known,
		}, a.log)
		a.wref.set(w)
		errc := make(chan error, 1)
		go func() { errc <- w.Run(ctx) }()
		for t := range w.Transitions() {
			wg.Add(1)
			go func(t herdr.Transition) {
				defer wg.Done()
				unlock := panes.lock(t.PaneID)
				defer unlock()
				a.sup.HandleTransition(ctx, t)
			}(t)
		}
		err := <-errc
		a.wref.set(nil)
		if ctx.Err() != nil {
			return
		}
		a.log.Log("daemon", "daemon.herdr_unavailable", "", map[string]any{"error": errString(err), "retry_s": int(a.opts.WatcherRetry.Seconds())})
		select {
		case <-ctx.Done():
			return
		case <-time.After(a.opts.WatcherRetry):
		}
	}
}

// watcherRef forwards Inject to the current Watcher (there is a new one
// after every retry); events arriving while none runs are dropped.
type watcherRef struct {
	mu sync.Mutex
	w  *herdr.Watcher
}

func (r *watcherRef) set(w *herdr.Watcher) {
	r.mu.Lock()
	r.w = w
	r.mu.Unlock()
}

// beat is the current Watcher's LastBeat; running is false while none
// runs (the watchdog's herdr probe).
func (r *watcherRef) beat() (last time.Time, running bool) {
	r.mu.Lock()
	w := r.w
	r.mu.Unlock()
	if w == nil {
		return time.Time{}, false
	}
	return w.LastBeat(), true
}

// Inject implements web.EventInjector.
func (r *watcherRef) Inject(ev herdr.StatusEvent) {
	r.mu.Lock()
	w := r.w
	r.mu.Unlock()
	if w != nil {
		w.Inject(ev)
	}
}

// paneLocks serialises transition handling per pane while letting
// different panes proceed in parallel.
type paneLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (p *paneLocks) lock(pane string) func() {
	p.mu.Lock()
	if p.locks == nil {
		p.locks = map[string]*sync.Mutex{}
	}
	m, ok := p.locks[pane]
	if !ok {
		m = &sync.Mutex{}
		p.locks[pane] = m
	}
	p.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
