package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/demo"
	"github.com/kccarlos/herdr-sheepdog/internal/web"
)

// Demo limits for the setup page's "Try it with demo agents": it stops
// when the page has not pinged for demoPingTimeout (the page sends one
// every 10 s and a stop when it closes) or after demoMaxRun.
var (
	demoPingTimeout = 45 * time.Second
	demoMaxRun      = 20 * time.Minute
	demoWatchEvery  = 5 * time.Second
)

// demoStarter starts a demo (tests replace it).
type demoStarter func(ctx context.Context, cfg config.Config, port int, dir string, log io.Writer) (demoSession, error)

// demoSession is a running demo.
type demoSession interface {
	BaseURL() string
	Arm()
	Close()
}

func (r *demoRun) BaseURL() string { return r.URL }
func (r *demoRun) Arm()            { r.arm() }

// setupDemo is the daemon's one demo (web.DemoControl): in this
// process, on its own port and temp data dir, so stopping it (or the
// daemon exiting) leaves nothing behind.
type setupDemo struct {
	start demoStarter
	log   io.Writer
	// ports are tried in order for the demo's web server.
	ports []int

	mu       sync.Mutex
	cur      demoSession
	cancel   context.CancelFunc
	dir      string
	lastPing time.Time
	endsAt   time.Time
	stopped  string
	done     chan struct{}
}

func newSetupDemo(log io.Writer) *setupDemo {
	ports := make([]int, 0, 10)
	for p := demo.DefaultPort; len(ports) < 10; p++ {
		if p != 7777 {
			ports = append(ports, p)
		}
	}
	return &setupDemo{
		log: log, ports: ports,
		start: func(ctx context.Context, cfg config.Config, port int, dir string, log io.Writer) (demoSession, error) {
			return startDemo(ctx, cfg, port, dir, log)
		},
	}
}

// Start implements web.DemoControl.
func (m *setupDemo) Start(cfg config.Config) (web.DemoState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil {
		return m.stateLocked(), nil
	}
	if msg := demoKeyProblem(cfg); msg != "" {
		return m.stateLocked(), errors.New("the demo needs the voice key first: save it in step 1")
	}
	port := 0
	for _, p := range m.ports {
		if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), 300*time.Millisecond); err == nil {
			_ = c.Close()
			continue
		}
		port = p
		break
	}
	if port == 0 {
		return m.stateLocked(), fmt.Errorf("no free port for the demo (tried %d to %d)", m.ports[0], m.ports[len(m.ports)-1])
	}
	dir, err := os.MkdirTemp("", "sheepdog-demo-")
	if err != nil {
		return m.stateLocked(), err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := m.start(ctx, cfg, port, dir, m.log)
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return m.stateLocked(), fmt.Errorf("the demo did not start: %v", err)
	}
	now := time.Now()
	m.cur, m.cancel, m.dir, m.lastPing, m.endsAt, m.stopped = s, cancel, dir, now, now.Add(demoMaxRun), ""
	m.done = make(chan struct{})
	go m.watch(ctx, s, m.done, demoWatchEvery, demoPingTimeout)
	fprintf(m.log, "demo started on %s (stops with the setup page, or in %s)\n", s.BaseURL(), demoMaxRun)
	return m.stateLocked(), nil
}

// watch arms the workers when a client connects and stops the demo when
// the page went away or the time is up.
func (m *setupDemo) watch(ctx context.Context, s demoSession, done chan struct{}, every, pingTimeout time.Duration) {
	defer close(done)
	go func() {
		if waitForClient(ctx, s.BaseURL()) {
			s.Arm()
		}
	}()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.mu.Lock()
			ping, ends := m.lastPing, m.endsAt
			m.mu.Unlock()
			switch {
			case now.Sub(ping) > pingTimeout:
				go m.Stop("the setup page closed")
				return
			case now.After(ends):
				go m.Stop("time limit")
				return
			}
		}
	}
}

// Stop implements web.DemoControl.
func (m *setupDemo) Stop(reason string) {
	m.mu.Lock()
	s, cancel, dir := m.cur, m.cancel, m.dir
	if s == nil {
		m.mu.Unlock()
		return
	}
	m.cur, m.cancel, m.dir, m.stopped = nil, nil, "", reason
	m.mu.Unlock()
	cancel()
	s.Close()
	_ = os.RemoveAll(dir)
	fprintf(m.log, "demo stopped (%s)\n", reason)
}

// Ping implements web.DemoControl: the page is still open.
func (m *setupDemo) Ping() {
	m.mu.Lock()
	m.lastPing = time.Now()
	m.mu.Unlock()
}

// State implements web.DemoControl.
func (m *setupDemo) State() web.DemoState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stateLocked()
}

func (m *setupDemo) stateLocked() web.DemoState {
	if m.cur == nil {
		return web.DemoState{Stopped: m.stopped}
	}
	t := m.endsAt
	return web.DemoState{Running: true, URL: m.cur.BaseURL(), EndsAt: &t}
}
