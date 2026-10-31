package herdr

import (
	"os"
	"path/filepath"
)

// RealDir is path made absolute with every symlink resolved, or "" when it
// is not an existing directory.
func RealDir(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return ""
	}
	return real
}

// PaneDir is the first pane in one existing folder, or the first accepted
// pane when PaneDirs has an access check.
type PaneDir struct {
	Path string
	Pane PaneInfo
}

// PaneDirs returns the first pane in each existing folder, in snapshot
// order. allow, when not nil, is checked first and skips a pane the caller
// cannot access.
func PaneDirs(snap Snapshot, allow func(string) bool) []PaneDir {
	var dirs []PaneDir
	seen := map[string]bool{}
	for _, p := range snap.Panes {
		if allow != nil && !allow(p.Cwd) {
			continue
		}
		path := RealDir(p.Cwd)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		dirs = append(dirs, PaneDir{Path: path, Pane: p})
	}
	return dirs
}

// PaneInDir is the first pane whose folder resolves to dir. Agent spawning
// and phone shells use PaneDirs to find a folder's existing space.
func PaneInDir(snap Snapshot, dir string) (PaneInfo, bool) {
	path := RealDir(dir)
	if path == "" {
		return PaneInfo{}, false
	}
	for _, d := range PaneDirs(snap, nil) {
		if d.Path == path {
			return d.Pane, true
		}
	}
	return PaneInfo{}, false
}
