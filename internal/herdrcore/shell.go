package herdrcore

import (
	"context"
	"fmt"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// ShellWorkspace returns the existing space working in cwd, or "" when the
// phone shell needs a new one. PaneInDir is the same folder match agent
// spawning uses.
func (b *Backend) ShellWorkspace(snap herdr.Snapshot, cwd string) string {
	if p, ok := herdr.PaneInDir(snap, cwd); ok {
		return p.WorkspaceID
	}
	return ""
}

// IsShellPane reports whether p carries sheepdog=shell.
func (b *Backend) IsShellPane(p herdr.PaneInfo, _ string) bool {
	return p.PhoneShell
}

// OpenShell opens a phone shell in workspaceID, or in a new space when
// workspaceID is empty. It uses the agent spawn path but labels the kept
// terminal as a shell, so it is not an agent and only it can be closed here.
func (b *Backend) OpenShell(ctx context.Context, workspaceID, cwd string) (herdr.PaneInfo, error) {
	if workspaceID == "" {
		workspaceID = newID()
	}
	return b.openShell(ctx, cwd, workspaceID, shellValue)
}

// CloseShell kills paneID only when it carries sheepdog=shell.
func (b *Backend) CloseShell(ctx context.Context, paneID string) error {
	t, err := b.client.Get(ctx, paneID)
	if err != nil {
		return translate(err)
	}
	if t.Labels[spawnLabel] != shellValue {
		return fmt.Errorf("herdrcore: terminal %s is not a phone shell", paneID)
	}
	_, err = b.client.Kill(ctx, paneID)
	b.logEvent("herdr.close_shell", paneID, map[string]any{"error": errString(err)})
	return translate(err)
}
