package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

type fakeDemoSession struct {
	mu     sync.Mutex
	closed bool
	dir    string
}

func (s *fakeDemoSession) BaseURL() string { return "http://127.0.0.1:1" }
func (s *fakeDemoSession) Arm()            {}
func (s *fakeDemoSession) Close()          { s.mu.Lock(); s.closed = true; s.mu.Unlock() }
func (s *fakeDemoSession) isClosed() bool  { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }

func demoWithFake(t *testing.T) (*setupDemo, *[]*fakeDemoSession) {
	t.Helper()
	var made []*fakeDemoSession
	m := newSetupDemo(io.Discard)
	m.ports = []int{freePort(t)}
	m.start = func(_ context.Context, _ config.Config, _ int, dir string, _ io.Writer) (demoSession, error) {
		s := &fakeDemoSession{dir: dir}
		made = append(made, s)
		return s, nil
	}
	return m, &made
}

func keyed() config.Config {
	c := config.Default()
	c.Gemini.APIKeys = []string{"k"}
	return c
}

// TestSetupDemoLifecycle: start (twice is one demo), stop closes it and
// removes its data dir; no key, no demo.
func TestSetupDemoLifecycle(t *testing.T) {
	m, made := demoWithFake(t)
	if _, err := m.Start(config.Default()); err == nil || !strings.Contains(err.Error(), "needs the voice key") {
		t.Fatalf("no key: %v", err)
	}
	st, err := m.Start(keyed())
	if err != nil || !st.Running || st.EndsAt == nil {
		t.Fatalf("start: %+v %v", st, err)
	}
	if _, err := m.Start(keyed()); err != nil || len(*made) != 1 {
		t.Fatalf("second start made %d demos (%v)", len(*made), err)
	}
	s := (*made)[0]
	m.Stop("stopped")
	if !s.isClosed() {
		t.Error("stop did not close the demo")
	}
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) {
		t.Errorf("the demo's data dir is left: %v", err)
	}
	if st := m.State(); st.Running || st.Stopped != "stopped" {
		t.Errorf("state %+v", st)
	}
	m.Stop("again") // no demo: nothing happens
}

// TestSetupDemoStopsWithoutThePage: no ping for the timeout stops it;
// the time cap stops it too.
func TestSetupDemoStopsWithoutThePage(t *testing.T) {
	oldPing, oldMax, oldWatch := demoPingTimeout, demoMaxRun, demoWatchEvery
	t.Cleanup(func() { demoPingTimeout, demoMaxRun, demoWatchEvery = oldPing, oldMax, oldWatch })
	demoWatchEvery = 20 * time.Millisecond

	demoPingTimeout, demoMaxRun = 100*time.Millisecond, time.Hour
	m, made := demoWithFake(t)
	if _, err := m.Start(keyed()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return (*made)[0].isClosed() })
	if st := m.State(); st.Stopped != "the setup page closed" {
		t.Errorf("stopped = %q", st.Stopped)
	}

	demoPingTimeout, demoMaxRun = time.Hour, 100*time.Millisecond
	m, made = demoWithFake(t)
	if _, err := m.Start(keyed()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return (*made)[0].isClosed() })
	if st := m.State(); st.Stopped != "time limit" {
		t.Errorf("stopped = %q", st.Stopped)
	}

	// Pings keep it alive.
	demoPingTimeout, demoMaxRun = 150*time.Millisecond, time.Hour
	m, made = demoWithFake(t)
	if _, err := m.Start(keyed()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		time.Sleep(50 * time.Millisecond)
		m.Ping()
	}
	if (*made)[0].isClosed() {
		t.Error("stopped while the page pinged")
	}
	m.Stop("done")
}

// TestSetupDemoStartFailure: a demo that does not start leaves no dir.
func TestSetupDemoStartFailure(t *testing.T) {
	m := newSetupDemo(io.Discard)
	m.ports = []int{freePort(t)}
	var dir string
	m.start = func(_ context.Context, _ config.Config, _ int, d string, _ io.Writer) (demoSession, error) {
		dir = d
		return nil, errors.New("boom")
	}
	if _, err := m.Start(keyed()); err == nil || !strings.Contains(err.Error(), "the demo did not start: boom") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir left: %v", err)
	}
}
