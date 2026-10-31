package herdr_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/conformance"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr/herdrtest"
)

func TestBackendConformance(t *testing.T) {
	conformance.RunAll(t, newOldDriver)
}

type oldDriver struct {
	client *herdr.Client
	srv    *herdrtest.Server
}

func newOldDriver(t *testing.T) conformance.Driver {
	t.Helper()
	srv := herdrtest.New(t)
	client := herdr.NewClient(srv.Path)
	client.ReconnectBackoff = herdr.Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond}
	return &oldDriver{client: client, srv: srv}
}

func (d *oldDriver) Backend() herdr.Backend { return d.client }

func (d *oldDriver) ShellHost() conformance.ShellHost { return d.client }

func (d *oldDriver) StartPlain(ctx context.Context, cwd string) (herdr.PaneInfo, error) {
	return d.client.CreateWorkspace(ctx, cwd, "conformance")
}

func (d *oldDriver) Run(ctx context.Context, paneID string, p conformance.Program) error {
	pane, err := d.pane(ctx, paneID)
	if err != nil {
		return err
	}
	agent := herdr.AgentInfo{
		PaneID:      pane.PaneID,
		WorkspaceID: pane.WorkspaceID,
		TabID:       pane.TabID,
		TerminalID:  pane.PaneID,
		Status:      p.State,
		Agent:       p.Kind,
		Cwd:         pane.Cwd,
	}
	d.srv.SetAgent(agent)
	d.srv.EmitAgentDetected(pane.PaneID, pane.WorkspaceID, p.Kind, false, "")
	d.srv.EmitStatus(pane.PaneID, p.State)
	return nil
}

func (d *oldDriver) Exit(ctx context.Context, paneID string) error {
	pane, err := d.pane(ctx, paneID)
	if err != nil {
		return err
	}
	d.srv.EmitAgentDetected(paneID, pane.WorkspaceID, pane.Agent, true, herdr.StateDone)
	d.srv.RemoveAgent(paneID)
	d.srv.EmitStatus(paneID, herdr.StateUnknown)
	return nil
}

func (d *oldDriver) SetScreen(_ context.Context, paneID, screen string) error {
	d.srv.SetScreen(paneID, screen)
	return nil
}

func (d *oldDriver) NewAgent(ctx context.Context, cwd, kind string) (herdr.PaneInfo, error) {
	snap, err := d.client.Snapshot(ctx)
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	var pane herdr.PaneInfo
	if anchor, ok := herdr.PaneInDir(snap, cwd); ok {
		pane, err = d.client.SplitPane(ctx, anchor.PaneID, cwd)
	} else {
		pane, err = d.client.CreateWorkspace(ctx, cwd, "conformance")
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

func (d *oldDriver) pane(ctx context.Context, paneID string) (herdr.PaneInfo, error) {
	snap, err := d.client.Snapshot(ctx)
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	for _, p := range snap.Panes {
		if p.PaneID == paneID {
			return p, nil
		}
	}
	return herdr.PaneInfo{}, os.ErrNotExist
}
