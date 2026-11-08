// Package panelsync keeps the herdr-app panel (and the Sheepdog layout) that
// `sheepdog setup` installed in step with the sheepdog binary. The binary
// carries the panel it was built with (the root package's embed); setup writes
// a marker beside the installed files saying what it wrote; after an upgrade
// the daemon replaces the files when the marker says they are still exactly
// what setup wrote. Anything else is left alone and reported as a notice.
// See docs/dev/update.md.
package panelsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MarkerName is the file in the panel folder that says what setup installed.
const MarkerName = ".sheepdog-install.json"

const (
	modRoot    = "mods/sheepdog"
	layoutFile = "sheepdog.json"
)

// swapDirsFn is the OS single-syscall directory swap; tests replace it to
// exercise the two-rename fallback.
var swapDirsFn = swapDirs

// Source is the panel and layout built into this binary.
type Source struct {
	// Panel holds the mod under mods/sheepdog (the root package's Panel).
	Panel fs.FS
	// Layout is layouts/sheepdog.json.
	Layout []byte
	// Version is this binary's version, recorded in the marker.
	Version string
}

// Marker is the contents of MarkerName.
type Marker struct {
	// Version is the sheepdog version that wrote the files.
	Version string `json:"version"`
	// Files maps each file (slash path inside the panel folder) to the
	// SHA-256 of what was written.
	Files map[string]string `json:"files"`
	// Layout is the normalized SHA-256 of layouts/sheepdog.json as written;
	// empty when setup did not write that file.
	Layout string `json:"layout,omitempty"`
	// PreviousLayout is the layout herdr-app showed before setup switched to
	// sheepdog, restored by uninstall when sheepdog is still shown.
	PreviousLayout string `json:"previous_layout,omitempty"`
	// Keybinding records an opt-in keys.json shortcut setup added.
	Keybinding *KeybindingRecord `json:"keybinding,omitempty"`
	// KeyDeclined: the user said no to that shortcut, so setup does not ask again.
	KeyDeclined bool `json:"key_declined,omitempty"`
}

// State is what /api/state reports as `panel`.
type State struct {
	// Installed: the panel folder exists.
	Installed bool `json:"installed"`
	// Version is the sheepdog version that installed it (empty without a marker).
	Version string `json:"version,omitempty"`
	// Modified: the files are not exactly what setup wrote (or there is no
	// marker, or the folder is a symlink), so the daemon never replaces them.
	Modified bool `json:"modified"`
	// UpdateAvailable: the installed files differ from this binary's.
	UpdateAvailable bool `json:"update_available"`
	// AutoUpdate: panel.auto_update is on (set by the daemon).
	AutoUpdate bool `json:"auto_update"`
}

// Home is herdr-app's home: $HERDR_APP_HOME, else ~/.config/herdr-app.
func Home(getenv func(string) string, userHome string) string {
	if v := getenv("HERDR_APP_HOME"); v != "" {
		return v
	}
	return filepath.Join(userHome, ".config", "herdr-app")
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type file struct {
	data []byte
	hash string
}

// embedded returns the panel files by path inside the panel folder.
func (s Source) embedded() (map[string]file, error) {
	out := map[string]file{}
	if s.Panel == nil {
		return out, errors.New("panelsync: no embedded panel")
	}
	err := fs.WalkDir(s.Panel, modRoot, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(s.Panel, name)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(name, modRoot+"/")] = file{b, sum(b)}
		return nil
	})
	return out, err
}

func modDir(home string) string { return filepath.Join(home, "mods", "sheepdog") }

// ReadMarker reads the marker in the panel folder at dir.
func ReadMarker(dir string) (Marker, error) {
	var m Marker
	b, err := os.ReadFile(filepath.Join(dir, MarkerName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return Marker{}, err
	}
	if len(m.Files) == 0 {
		return Marker{}, errors.New("panelsync: marker lists no files")
	}
	return m, nil
}

func hashOf(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return sum(b), true
}

// isSymlink: a panel folder that is a link (a development checkout) is
// never replaced.
func isSymlink(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode()&os.ModeSymlink != 0
}

type evaluation struct {
	state  State
	marker Marker
	hasMk  bool
	emb    map[string]file
	// layoutUnmodified: the layout file exists and structurally matches what setup wrote.
	layoutUnmodified bool
}

func evaluate(home string, src Source) (evaluation, error) {
	var ev evaluation
	emb, err := src.embedded()
	if err != nil {
		return ev, err
	}
	ev.emb = emb
	dir := modDir(home)
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return ev, nil
	}
	ev.state.Installed = true
	current := true
	for rel, f := range emb {
		if h, ok := hashOf(filepath.Join(dir, filepath.FromSlash(rel))); !ok || h != f.hash {
			current = false
			break
		}
	}
	if m, err := ReadMarker(dir); err == nil {
		ev.marker, ev.hasMk = m, true
		ev.state.Version = m.Version
	}
	unmodified := ev.hasMk && !isSymlink(dir)
	if unmodified {
		for rel, want := range ev.marker.Files {
			if h, ok := hashOf(filepath.Join(dir, filepath.FromSlash(rel))); !ok || h != want {
				unmodified = false
				break
			}
		}
	}
	if !current {
		ev.state.UpdateAvailable = true
		ev.state.Modified = !unmodified
	}
	if ev.hasMk && ev.marker.Layout != "" && !isSymlink(dir) {
		if disk, err := os.ReadFile(filepath.Join(home, "layouts", layoutFile)); err == nil {
			ev.layoutUnmodified = layoutMatchesMarker(disk, ev.marker.Layout, src.Layout)
		}
	}
	return ev, nil
}

// Evaluate reports the installed panel against this binary's. It writes nothing.
func Evaluate(home string, src Source) State {
	ev, _ := evaluate(home, src)
	return ev.state
}

// Result is what Sync did.
type Result struct {
	State State
	// Updated: the panel files were replaced; From and To are the marker
	// versions before and after.
	Updated  bool
	From, To string
	Files    int
	// LayoutUpdated: layouts/sheepdog.json was replaced.
	LayoutUpdated bool
	// LayoutSidecar: layouts/sheepdog.json had structural user edits, so the
	// new shipped layout was written beside it as layouts/sheepdog.json.new.
	LayoutSidecar bool
}

// Sync replaces the installed panel and layout with this binary's when
// setup installed them and they are unchanged. It never creates the panel
// folder and never touches a file the user changed.
func Sync(home string, src Source) (Result, error) {
	ev, err := evaluate(home, src)
	res := Result{State: ev.state}
	if err != nil || !ev.state.Installed {
		return res, err
	}
	dir := modDir(home)
	layoutsDir := filepath.Join(home, "layouts")
	if ev.hasMk && ev.marker.Layout != "" && !isSymlink(dir) && len(src.Layout) > 0 {
		wantHash := LayoutHash(src.Layout)
		disk, diskErr := os.ReadFile(filepath.Join(layoutsDir, layoutFile))
		if ev.layoutUnmodified {
			_ = os.Remove(filepath.Join(layoutsDir, layoutSidecarFile))
			if diskErr == nil && LayoutsEqual(disk, src.Layout) {
				if ev.marker.Layout != wantHash {
					ev.marker.Layout = wantHash
					if !ev.state.UpdateAvailable || ev.state.Modified {
						if err := writeMarker(dir, ev.marker); err != nil {
							return res, err
						}
					}
				}
			} else if wantHash != ev.marker.Layout || diskErr != nil {
				// The layout first, and its new hash into the marker at once, so a
				// failure later cannot leave a layout the marker does not know.
				if err := writeAtomic(layoutsDir, layoutFile, src.Layout); err != nil {
					return res, fmt.Errorf("replace the layout: %w", err)
				}
				ev.marker.Layout = wantHash
				if err := writeMarker(dir, ev.marker); err != nil {
					return res, err
				}
				res.LayoutUpdated = true
			}
		} else if diskErr == nil && !LayoutsEqual(disk, src.Layout) && wantHash != ev.marker.Layout {
			if existing, err := os.ReadFile(filepath.Join(layoutsDir, layoutSidecarFile)); err != nil || string(existing) != string(src.Layout) {
				if err := writeAtomic(layoutsDir, layoutSidecarFile, src.Layout); err != nil {
					return res, fmt.Errorf("write layout sidecar: %w", err)
				}
				res.LayoutSidecar = true
			}
			ev.marker.Layout = wantHash
			if !ev.state.UpdateAvailable || ev.state.Modified {
				if err := writeMarker(dir, ev.marker); err != nil {
					return res, err
				}
			}
		}
	}
	if ev.state.UpdateAvailable && !ev.state.Modified {
		n, err := replacePanel(home, ev, src)
		if err != nil {
			return res, fmt.Errorf("replace the panel: %w", err)
		}
		res.Updated, res.From, res.To, res.Files = true, ev.state.Version, src.Version, n
		res.State.UpdateAvailable, res.State.Version = false, src.Version
	}
	return res, nil
}

func newMarker(src Source, emb map[string]file, layoutHash string) Marker {
	m := Marker{Version: src.Version, Files: map[string]string{}, Layout: layoutHash}
	for rel, f := range emb {
		m.Files[rel] = f.hash
	}
	return m
}

func writeMarker(dir string, m Marker) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return writeAtomic(dir, MarkerName, append(b, '\n'))
}

// writeAtomic writes dir/name through a temporary file in dir.
func writeAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sheepdog-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}

func buildStage(home, dir string, emb map[string]file, prev Marker, hasPrev bool, mk *Marker) (string, error) {
	stage, err := os.MkdirTemp(home, ".sheepdog-panel-new-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	for rel, f := range emb {
		dst := filepath.Join(stage, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			_ = os.RemoveAll(stage)
			return "", err
		}
		if err := os.WriteFile(dst, f.data, 0o644); err != nil {
			_ = os.RemoveAll(stage)
			return "", err
		}
	}
	if mk != nil {
		if err := writeMarker(stage, *mk); err != nil {
			_ = os.RemoveAll(stage)
			return "", err
		}
	}
	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		// Files we did not write stay; files of the old version that the new one
		// dropped go.
		err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			_, isNew := emb[rel]
			_, wasOurs := prev.Files[rel]
			if isNew || (hasPrev && wasOurs) || rel == MarkerName {
				return nil
			}
			return copyFile(p, filepath.Join(stage, filepath.FromSlash(rel)))
		})
		if err != nil {
			_ = os.RemoveAll(stage)
			return "", err
		}
	}
	return stage, nil
}

// replacePanel builds the new panel folder beside the old one (in home, the
// same file system), carries over files that are not ours (herdr-app's own
// gpui-kit.d.ts and jsconfig.json, .gitignore), and swaps it in atomically; a
// failure leaves the old folder in place.
func replacePanel(home string, ev evaluation, src Source) (int, error) {
	dir := modDir(home)
	mk := newMarker(src, ev.emb, ev.marker.Layout)
	mk.PreviousLayout = ev.marker.PreviousLayout
	mk.Keybinding = ev.marker.Keybinding
	mk.KeyDeclined = ev.marker.KeyDeclined
	stage, err := buildStage(home, dir, ev.emb, ev.marker, ev.hasMk, &mk)
	if err != nil {
		return 0, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := swapOrReplace(stage, dir); err != nil {
		return 0, err
	}
	return len(ev.emb), nil
}

func copyFile(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Record writes the marker for a panel folder that now holds this binary's
// files, as `sheepdog setup` leaves it. A file that differs from the embedded
// one means no marker: the daemon then treats the panel as changed.
// A panel folder that is a symlink (a development checkout) gets no marker.
func Record(home string, src Source) error {
	emb, err := src.embedded()
	if err != nil {
		return err
	}
	dir := modDir(home)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() || isSymlink(dir) {
		return nil
	}
	var prev Marker
	if existing, err := ReadMarker(dir); err == nil {
		prev = existing
	}
	m := Marker{
		Version:        src.Version,
		Files:          map[string]string{},
		PreviousLayout: prev.PreviousLayout,
		Keybinding:     prev.Keybinding,
		KeyDeclined:    prev.KeyDeclined,
	}
	rels := make([]string, 0, len(emb))
	for rel := range emb {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if h, ok := hashOf(filepath.Join(dir, filepath.FromSlash(rel))); ok && h == emb[rel].hash {
			m.Files[rel] = h
		}
	}
	if got, err := os.ReadFile(filepath.Join(home, "layouts", layoutFile)); err == nil && len(src.Layout) > 0 && LayoutsEqual(got, src.Layout) {
		m.Layout = LayoutHash(src.Layout)
	}
	if len(m.Files) != len(emb) {
		// Not what this binary ships: no claim that it is ours.
		_ = os.Remove(filepath.Join(dir, MarkerName))
		return nil
	}
	return writeMarker(dir, m)
}
