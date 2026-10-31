package herdr

import (
	"context"
	"path/filepath"
)

// PhoneShellWorkspace is the workspace label the old herdr uses for phone
// shells. herdr-core uses each folder's existing space instead.
const PhoneShellWorkspace = "phone shell"

// ShellWorkspace returns the space a phone shell in cwd belongs in, or ""
// when one has to be created. The old herdr groups every phone shell in one
// workspace; herdr-core joins the space already working in the folder.
func (c *Client) ShellWorkspace(snap Snapshot, _ string) string {
	for _, w := range snap.Workspaces {
		if w.Label == PhoneShellWorkspace {
			return w.WorkspaceID
		}
	}
	return ""
}

// IsShellPane reports whether p is one of the old herdr's phone shells.
func (c *Client) IsShellPane(p PaneInfo, workspaceID string) bool {
	return workspaceID != "" && p.WorkspaceID == workspaceID
}

// OpenShell opens a phone shell in workspaceID, or in a new workspace when
// workspaceID is empty.
func (c *Client) OpenShell(ctx context.Context, workspaceID, cwd string) (PaneInfo, error) {
	if workspaceID == "" {
		return c.CreateWorkspace(ctx, cwd, PhoneShellWorkspace)
	}
	return c.CreateTab(ctx, workspaceID, cwd, filepath.Base(cwd))
}

// CloseShell closes a phone shell. The web layer has already checked that
// paneID is one it opened.
func (c *Client) CloseShell(ctx context.Context, paneID string) error {
	return c.ClosePane(ctx, paneID)
}
