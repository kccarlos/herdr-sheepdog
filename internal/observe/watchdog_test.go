package observe

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

type lineLog struct {
	mu    sync.Mutex
	lines []logging.Line
}

func (l *lineLog) Log(source, typ, paneID string, payload any) {
	l.mu.Lock()
	l.lines = append(l.lines, logging.Line{Source: source, Type: typ, Payload: payload})
	l.mu.Unlock()
}

// take returns and clears the lines as "type component detail".
func (l *lineLog) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, ln := range l.lines {
		p := ln.Payload.(map[string]any)
		s := ln.Type + " " + p["component"].(string)
		if d, ok := p["detail"].(string); ok {
			s += " " + d
		}
		out = append(out, s)
	}
	l.lines = nil
	return out
}

// Each component: a miss is logged once per incident, its recovery once.
func TestWatchdogIncidents(t *testing.T) {
	now := time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC)
	var (
		turnStart   time.Time
		turnRunning bool
		queued      int
		oldest      time.Time
		listener    = true
		beat        = now
		watcherUp   = true
		pingErr     = errors.New("connection refused")
		pinged      = make(chan struct{}, 8)
	)
	log := &lineLog{}
	w := NewWatchdog(log, Probes{
		Brain:       func() (time.Time, time.Duration, bool) { return turnStart, 60 * time.Second, turnRunning },
		PushBacklog: func() (int, time.Time) { return queued, oldest },
		Listener:    func() bool { return listener },
		HerdrBeat:   func() (time.Time, bool) { return beat, watcherUp },
		HerdrPing: func(context.Context) error {
			defer func() { pinged <- struct{}{} }()
			return pingErr
		},
		HerdrStale: 100 * time.Second,
	})
	var clockMu sync.Mutex // the ping goroutine reads the clock
	w.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	frozen := false // the herdr beat stops
	tick := func(d time.Duration) []string {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
		if !frozen {
			beat = now
		}
		w.Tick(now)
		return log.take()
	}

	if got := tick(0); len(got) != 0 {
		t.Fatalf("healthy daemon logged %v", got)
	}

	// Brain: a turn past budget + overrun.
	turnStart, turnRunning = now, true
	if got := tick(80 * time.Second); len(got) != 0 {
		t.Fatalf("turn within budget+overrun: %v", got)
	}
	if got := tick(15 * time.Second); len(got) != 1 || got[0] != "daemon.heartbeat_missed brain turn running 95s, budget 60s" {
		t.Fatalf("brain miss = %v", got)
	}
	if got := tick(5 * time.Second); len(got) != 0 {
		t.Fatalf("second miss logged: %v", got)
	}
	turnRunning = false
	if got := tick(5 * time.Second); len(got) != 1 || got[0] != "daemon.heartbeat_ok brain" {
		t.Fatalf("brain recovery = %v", got)
	}

	// Push queue: stuck with a listener; held without one is fine.
	queued, oldest = 2, now
	listener = false
	if got := tick(100 * time.Second); len(got) != 0 {
		t.Fatalf("held without a listener: %v", got)
	}
	listener = true
	if got := tick(5 * time.Second); len(got) != 1 || got[0] != "daemon.heartbeat_missed push_queue 2 push(es) queued, oldest 105s, listener attached" {
		t.Fatalf("push miss = %v", got)
	}
	queued = 0
	if got := tick(5 * time.Second); len(got) != 1 || got[0] != "daemon.heartbeat_ok push_queue" {
		t.Fatalf("push recovery = %v", got)
	}

	// Herdr: silent past HerdrStale. The first miss cannot say yet
	// whether the socket answers (the ping runs off the tick).
	frozen = true
	if got := tick(101 * time.Second); len(got) != 1 || got[0] != "daemon.heartbeat_missed herdr no snapshot or event for 101s; socket not checked yet" {
		t.Fatalf("herdr miss = %v", got)
	}
	<-pinged
	frozen = false
	if got := tick(time.Second); len(got) != 1 || got[0] != "daemon.heartbeat_ok herdr" {
		t.Fatalf("herdr recovery = %v", got)
	}
	watcherUp = false
	got := tick(time.Second)
	<-pinged
	if len(got) != 1 || !strings.Contains(got[0], "no watcher running") || !strings.Contains(got[0], "socket unresponsive: connection refused") {
		t.Fatalf("herdr down = %v", got)
	}
	watcherUp = true
	if got := tick(time.Second); len(got) != 1 || got[0] != "daemon.heartbeat_ok herdr" {
		t.Fatalf("herdr recovery = %v", got)
	}

	// Voice: its own stall signal.
	w.OnVoiceEvent(voice.EventStatus{Status: voice.StatusConnected, Stalled: true})
	if got := tick(time.Second); len(got) != 1 || !strings.HasPrefix(got[0], "daemon.heartbeat_missed voice voice.stalled") {
		t.Fatalf("voice miss = %v", got)
	}
	w.OnVoiceEvent(voice.EventTranscript{Text: "x"}) // not a status: no change
	if got := tick(time.Second); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	w.OnVoiceEvent(voice.EventStatus{Status: voice.StatusConnected})
	if got := tick(time.Second); len(got) != 1 || got[0] != "daemon.heartbeat_ok voice" {
		t.Fatalf("voice recovery = %v", got)
	}
}

// Nil probes skip their components.
func TestWatchdogNoProbes(t *testing.T) {
	log := &lineLog{}
	NewWatchdog(log, Probes{}).Tick(time.Now())
	if got := log.take(); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
