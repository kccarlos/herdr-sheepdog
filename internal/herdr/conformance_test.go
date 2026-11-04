package herdr_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/attention"
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
	local  *attention.Local
	store  string
}

func newOldDriver(t *testing.T) conformance.Driver {
	t.Helper()
	srv := herdrtest.New(t)
	client := herdr.NewClient(srv.Path)
	client.ReconnectBackoff = herdr.Backoff{Min: 10 * time.Millisecond, Max: 50 * time.Millisecond}
	return newOldDriverOver(t, client, srv, filepath.Join(t.TempDir(), attention.FileName))
}

// newOldDriverOver is a daemon's view of the machine: the old herdr's marks
// live in a store file, which a restarted daemon opens again.
func newOldDriverOver(t *testing.T, client *herdr.Client, srv *herdrtest.Server, store string) *oldDriver {
	t.Helper()
	local, err := attention.NewLocal(store, client, attention.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &oldDriver{client: client, srv: srv, local: local, store: store}
}

func (d *oldDriver) Marker() herdr.Marker { return d.local }

func (d *oldDriver) Settle(ctx context.Context) error { return d.local.Refresh(ctx) }

func (d *oldDriver) MarkWrites() int { return d.local.Writes() }

func (d *oldDriver) RestartDaemon(t *testing.T) conformance.Driver {
	t.Helper()
	return newOldDriverOver(t, d.client, d.srv, d.store)
}

func (d *oldDriver) EndTurn(_ context.Context, paneID string) error {
	d.srv.StartTurn(paneID)
	d.srv.EndTurn(paneID, herdr.StateDone)
	return nil
}

func (d *oldDriver) SetState(_ context.Context, paneID string, state herdr.State) error {
	d.srv.EmitStatus(paneID, state)
	return nil
}

// OtherClientSeen is a person focusing the pane in a TUI client: herdr turns
// the done agent idle, with no new turn.
func (d *oldDriver) OtherClientSeen(_ context.Context, paneID string) error {
	d.srv.Focus(paneID)
	return nil
}

// OtherClientPin: the TUI has no pins, so this is the marker's own write.
func (d *oldDriver) OtherClientPin(ctx context.Context, paneID string, on bool) error {
	_, err := d.local.SetPinned(ctx, paneID, on)
	return err
}

// StartAgent is sheepdog's spawn path: a pane, agent.start, and the hook
// the daemon runs after it (inject.Herdr.AfterStart), then the agent coming
// up.
func (d *oldDriver) StartAgent(ctx context.Context, cwd, kind string) (herdr.PaneInfo, error) {
	pane, err := d.newPane(ctx, cwd)
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	if err := d.client.StartAgent(ctx, pane.PaneID, "conformance", kind, nil, 0); err != nil {
		return herdr.PaneInfo{}, err
	}
	if err := d.local.AgentStarted(ctx, pane.PaneID); err != nil {
		return herdr.PaneInfo{}, err
	}
	return pane, d.runIdle(ctx, pane.PaneID, kind)
}

func (d *oldDriver) ForeignAgent(ctx context.Context, cwd, kind string, state herdr.State) (herdr.PaneInfo, error) {
	pane, err := d.client.CreateWorkspace(ctx, cwd, "foreign")
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	return pane, d.Run(ctx, pane.PaneID, conformance.Program{Kind: kind, App: kind, Reported: true, State: state})
}

func (d *oldDriver) Backend() herdr.Backend { return d.client }

func (d *oldDriver) ShellHost() conformance.ShellHost { return d.client }

func (d *oldDriver) TerminalSize(_ context.Context, _ string) (int, int, bool) {
	// The old herdr backend does not support setting or querying initial terminal size.
	return 0, 0, false
}

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
		TerminalID:  pane.TerminalID,
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
	pane, err := d.newPane(ctx, cwd)
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	return pane, d.runIdle(ctx, pane.PaneID, kind)
}

// newPane opens a terminal in cwd, joining the folder's workspace.
func (d *oldDriver) newPane(ctx context.Context, cwd string) (herdr.PaneInfo, error) {
	snap, err := d.client.Snapshot(ctx)
	if err != nil {
		return herdr.PaneInfo{}, err
	}
	if anchor, ok := herdr.PaneInDir(snap, cwd); ok {
		return d.client.SplitPane(ctx, anchor.PaneID, cwd)
	}
	return d.client.CreateWorkspace(ctx, cwd, "conformance")
}

func (d *oldDriver) runIdle(ctx context.Context, paneID, kind string) error {
	app := kind
	if kind == "claude" {
		app = "claude-code"
	}
	return d.Run(ctx, paneID, conformance.Program{Kind: kind, App: app, Reported: true, State: herdr.StateIdle})
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
