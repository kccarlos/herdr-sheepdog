package herdrcore_test

import (
	"context"
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

type coreDriver struct {
	backend *herdrcore.Backend
	client  *hc.Client
	srv     *hctest.Server
}

func newCoreDriver(t *testing.T) conformance.Driver {
	t.Helper()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	backend := herdrcore.New(client, nil)
	backend.FallbackPoll = 20 * time.Millisecond
	backend.Backoff = herdr.Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond}
	return &coreDriver{backend: backend, client: client, srv: srv}
}

func (d *coreDriver) Backend() herdr.Backend { return d.backend }

func (d *coreDriver) ShellHost() conformance.ShellHost { return d.backend }

func (d *coreDriver) StartPlain(ctx context.Context, cwd string) (herdr.PaneInfo, error) {
	return d.backend.CreateWorkspace(ctx, cwd, "")
}

func (d *coreDriver) Run(ctx context.Context, paneID string, p conformance.Program) error {
	term, err := d.client.Get(ctx, paneID)
	if err != nil {
		return err
	}
	argv := append([]string{p.Kind}, p.Args...)
	term.Argv = argv
	term.Foreground = &hc.Foreground{PID: 200, Name: p.Kind, Argv: argv, Cwd: term.Cwd}
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
	term.Argv = []string{"zsh"}
	term.Foreground = &hc.Foreground{PID: 100, Name: "zsh", Argv: []string{"zsh"}, Cwd: term.Cwd}
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
	snap, err := d.backend.Snapshot(ctx)
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	var pane herdr.PaneInfo
	if anchor, ok := herdr.PaneInDir(snap, cwd); ok {
		pane, err = d.backend.SplitPane(ctx, anchor.PaneID, cwd)
	} else {
		pane, err = d.backend.CreateWorkspace(ctx, cwd, "")
	}
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	app := kind
	if kind == "claude" {
		app = "claude-code"
	}
	if err := d.Run(ctx, pane.PaneID, conformance.Program{
		Kind: kind, App: app, Reported: true, State: herdr.StateIdle,
	}); err != nil {
		return herdr.PaneInfo{}, err
	}
	return pane, nil
}
