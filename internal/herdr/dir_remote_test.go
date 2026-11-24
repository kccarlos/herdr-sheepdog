package herdr

import (
	"os"
	"path/filepath"
	"testing"
)

// Blocking probe (NeonH §3.4, Carbon §3.3): PaneDirs and PaneInDir must never
// resolve a remote pane's Cwd on the local filesystem or invoke allow probes
// on it, even when that path is a real local directory with files.
func TestPaneDirsAndPaneInDirIgnoreRemotePanes(t *testing.T) {
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "secret.txt"), []byte("local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snap := Snapshot{
		Panes: []PaneInfo{
			{PaneID: "box~t4", WorkspaceID: "s-remote", Cwd: localDir, Agent: "claude"},
		},
	}

	probed := false
	dirs := PaneDirs(snap, func(string) bool {
		probed = true
		return true
	})
	if probed {
		t.Error("PaneDirs invoked allow callback on remote pane cwd")
	}
	if len(dirs) != 0 {
		t.Fatalf("PaneDirs returned local dir for remote pane: %+v", dirs)
	}
	if p, ok := PaneInDir(snap, localDir); ok {
		t.Fatalf("PaneInDir matched remote pane %+v for local dir %q", p, localDir)
	}
}
