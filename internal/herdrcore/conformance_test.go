package herdrcore_test

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/conformance"
	"github.com/kccarlos/herdr-sheepdog/internal/herdrcore"
)

func TestBackendConformance(t *testing.T) {
	conformance.RunAll(t, newCoreDriver)
}

type coreProcTable struct {
	mu      sync.Mutex
	nextPID int
	shells  map[string]int
}

func newCoreProcTable() *coreProcTable {
	return &coreProcTable{nextPID: 1000, shells: map[string]int{}}
}

func (p *coreProcTable) alloc() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextPID++
	return p.nextPID
}

func (p *coreProcTable) shellPID(paneID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sh, ok := p.shells[paneID]; ok {
		return sh
	}
	p.nextPID++
	sh := p.nextPID
	p.shells[paneID] = sh
	return sh
}

type coreDriver struct {
	backend *herdrcore.Backend
	client  *hc.Client
	srv     *hctest.Server
	procs   *coreProcTable
}

func newCoreDriver(t *testing.T) conformance.Driver {
	t.Helper()
	srv := hctest.New(t)
	return newCoreDriverOver(t, srv, newCoreProcTable())
}

// newCoreDriverOver is a daemon's view of the machine: a backend that keeps
// its marks current from the event stream, up before the scenario starts.
func newCoreDriverOver(t *testing.T, srv *hctest.Server, procs *coreProcTable) *coreDriver {
	t.Helper()
	client := hc.NewClient(srv.Path)
	backend := herdrcore.New(client, nil)
	backend.FallbackPoll = 20 * time.Millisecond
	backend.Backoff = herdr.Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond}

	before := srv.Subscribers()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		backend.RunMarks(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	deadline := time.Now().Add(3 * time.Second)
	for srv.Subscribers() <= before {
		if time.Now().After(deadline) {
			t.Fatal("the marks subscription never came up")
		}
		time.Sleep(time.Millisecond)
	}
	return &coreDriver{backend: backend, client: client, srv: srv, procs: procs}
}

func (d *coreDriver) Marker() herdr.Marker { return d.backend }

func (d *coreDriver) TerminalSize(ctx context.Context, paneID string) (int, int, bool) {
	t, err := d.client.Get(ctx, paneID)
	if err != nil {
		return 0, 0, false
	}
	return int(t.Cols), int(t.Rows), true
}

// Settle: hc follows its event stream, so there is nothing to do.
func (d *coreDriver) Settle(context.Context) error { return nil }

func (d *coreDriver) MarkWrites() int {
	n := 0
	for _, r := range d.srv.Requests() {
		if r.Method == "terminal.label" {
			n++
		}
	}
	return n
}

func (d *coreDriver) RestartDaemon(t *testing.T) conformance.Driver {
	t.Helper()
	return newCoreDriverOver(t, d.srv, d.procs)
}

// StartAgent is sheepdog's spawn path: a shell terminal it opens, the
// agent's command typed into it, then the agent coming up.
func (d *coreDriver) StartAgent(ctx context.Context, cwd, kind string) (herdr.PaneInfo, error) {
	pane, err := d.newPane(ctx, cwd)
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	if err := d.backend.StartAgent(ctx, pane.PaneID, "", kind, nil, 0); err != nil {
		return herdr.PaneInfo{}, err
	}
	return pane, d.runIdle(ctx, pane.PaneID, kind)
}

// ForeignAgent is a terminal another client started: no sheepdog labels,
// and no herdr.seen.
func (d *coreDriver) ForeignAgent(ctx context.Context, cwd, kind string, state herdr.State) (herdr.PaneInfo, error) {
	term, err := d.client.Run(ctx, hc.RunParams{Cwd: cwd, Keep: true, NoParent: true})
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	app := kind
	if kind == "claude" {
		app = "claude-code"
	}
	err = d.Run(ctx, term.Address, conformance.Program{Kind: kind, App: app, Reported: true, State: state})
	return herdr.PaneInfo{PaneID: term.Address, TerminalID: term.Address, Cwd: cwd}, err
}

func (d *coreDriver) EndTurn(ctx context.Context, paneID string) error {
	if err := d.SetState(ctx, paneID, herdr.StateWorking); err != nil {
		return err
	}
	return d.SetState(ctx, paneID, herdr.StateDone)
}

func (d *coreDriver) SetState(ctx context.Context, paneID string, state herdr.State) error {
	term, err := d.client.Get(ctx, paneID)
	if err != nil {
		return err
	}
	app := ""
	if term.Status != nil {
		app = term.Status.App
	}
	d.srv.SetStatus(paneID, &hc.Status{State: string(state), App: app})
	return nil
}

// OtherClientSeen is another Herdr window looking at the agent: it writes
// herdr.seen = the turn.
func (d *coreDriver) OtherClientSeen(ctx context.Context, paneID string) error {
	term, err := d.client.Get(ctx, paneID)
	if err != nil {
		return err
	}
	d.srv.SetLabels(paneID, map[string]string{"herdr.seen": strconv.FormatUint(term.Turn, 10)})
	return nil
}

func (d *coreDriver) OtherClientPin(_ context.Context, paneID string, on bool) error {
	value := ""
	if on {
		value = "1"
	}
	d.srv.SetLabels(paneID, map[string]string{"herdr.pinned": value})
	return nil
}

func (d *coreDriver) Backend() herdr.Backend { return d.backend }

func (d *coreDriver) ShellHost() conformance.ShellHost { return d.backend }

func (d *coreDriver) StartPlain(ctx context.Context, cwd string) (herdr.PaneInfo, error) {
	return d.backend.CreateWorkspace(ctx, cwd, "")
}

func (d *coreDriver) ensureShellPID(term *hc.Terminal) int {
	if term.PID != nil && *term.PID > 0 {
		return *term.PID
	}
	sh := d.procs.shellPID(term.Address)
	term.PID = &sh
	return sh
}

func (d *coreDriver) Run(ctx context.Context, paneID string, p conformance.Program) error {
	term, err := d.client.Get(ctx, paneID)
	if err != nil {
		return err
	}
	d.ensureShellPID(&term)
	fg := p.PID
	if fg <= 0 {
		fg = d.procs.alloc()
	}
	argv := append(append([]string(nil), p.Wrapper...), p.Kind)
	argv = append(argv, p.Args...)
	name := p.Kind
	if len(p.Wrapper) > 0 {
		name = filepath.Base(p.Wrapper[0])
	}
	term.Argv = argv
	term.Foreground = &hc.Foreground{PID: fg, Name: name, Argv: argv, Cwd: term.Cwd}
	if p.Reported {
		app := p.App
		if app == "" {
			app = p.Kind
		}
		term.Reported = true
		term.Status = &hc.Status{State: string(p.State), App: app}
		d.srv.Add(term)
		d.srv.SetStatus(paneID, term.Status)
		return nil
	}
	term.Reported = false
	term.Status = nil
	d.srv.Add(term)
	return nil
}

func (d *coreDriver) Exit(ctx context.Context, paneID string) error {
	term, err := d.client.Get(ctx, paneID)
	if err != nil {
		return err
	}
	sh := d.ensureShellPID(&term)
	term.Argv = []string{"zsh"}
	term.Foreground = &hc.Foreground{PID: sh, Name: "zsh", Argv: []string{"zsh"}, Cwd: term.Cwd}
	term.Status = nil
	// Keep Reported true: a program that once reported must still leave the
	// agent list when its status is cleared and the shell is in front.
	term.Reported = true
	d.srv.Add(term)
	d.srv.SetStatus(paneID, nil)
	return nil
}

func (d *coreDriver) SetScreen(_ context.Context, paneID, screen string) error {
	d.srv.SetScreen(paneID, screen)
	return nil
}

func (d *coreDriver) NewAgent(ctx context.Context, cwd, kind string) (herdr.PaneInfo, error) {
	pane, err := d.newPane(ctx, cwd)
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	return pane, d.runIdle(ctx, pane.PaneID, kind)
}

// newPane opens a terminal in cwd, joining the folder's space.
func (d *coreDriver) newPane(ctx context.Context, cwd string) (herdr.PaneInfo, error) {
	snap, err := d.backend.Snapshot(ctx)
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	if anchor, ok := herdr.PaneInDir(snap, cwd); ok {
		return d.backend.SplitPane(ctx, anchor.PaneID, cwd)
	}
	return d.backend.CreateWorkspace(ctx, cwd, "")
}

func (d *coreDriver) runIdle(ctx context.Context, paneID, kind string) error {
	app := kind
	if kind == "claude" {
		app = "claude-code"
	}
	return d.Run(ctx, paneID, conformance.Program{Kind: kind, App: app, Reported: true, State: herdr.StateIdle})
}
