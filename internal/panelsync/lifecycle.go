package panelsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const (
	layoutSidecarFile = "sheepdog.json.new"
	keysFile          = "keys.json"
	keysBackupFile    = "keys.json.bak"
	sharedFile        = "shared.json"

	// FocusKeySection is the keys.json section used for the panel shortcut.
	FocusKeySection = "global"
	// FocusCommand is the herdr-app mod action that focuses the Sheepdog panel.
	FocusCommand = "sheepdog.focus"
)

// KeybindingRecord remembers an opt-in keys.json binding setup wrote so
// uninstall can restore or remove it cleanly.
type KeybindingRecord struct {
	Section    string `json:"section"`
	Key        string `json:"key"`
	Command    string `json:"command"`
	HadFile    bool   `json:"had_file"`
	HadBinding bool   `json:"had_binding,omitempty"`
	Previous   string `json:"previous,omitempty"`
}

// KeyState describes the current state of key in <home>/keys.json.
type KeyState struct {
	Key         string
	FileExists  bool
	Bound       bool
	AlreadyOurs bool
	Current     string
}

// UninstallPlan describes what Uninstall did (or would do when dryRun is true).
type UninstallPlan struct {
	Actions        []string
	Kept           []string
	RestoredLayout string
}

// DefaultFocusKey returns the default opt-in panel focus chord for goos.
func DefaultFocusKey(goos string) string {
	if goos == "darwin" {
		return "cmd-shift-s"
	}
	return "ctrl-shift-s"
}

// NormalizeLayout parses a herdr-app layout JSON document and strips runtime
// click/resize properties (size, hidden, active) from layout tree nodes,
// returning canonical compact JSON. Non-JSON inputs return (nil, false).
func NormalizeLayout(raw []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil || root == nil {
		return nil, false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, false
	}
	norm := stripLayoutRuntime(root)
	out, err := json.Marshal(norm)
	if err != nil {
		return nil, false
	}
	return out, true
}

func stripLayoutRuntime(node map[string]any) map[string]any {
	clean := make(map[string]any, len(node))
	for k, v := range node {
		switch k {
		case "size", "hidden", "active":
			continue
		case "children":
			if items, ok := v.([]any); ok {
				next := make([]any, len(items))
				for i, item := range items {
					if child, ok := item.(map[string]any); ok {
						next[i] = stripLayoutRuntime(child)
					} else {
						next[i] = item
					}
				}
				clean[k] = next
				continue
			}
		}
		clean[k] = v
	}
	return clean
}

// LayoutHash returns the SHA-256 of NormalizeLayout(raw), falling back to raw
// bytes when raw is not a JSON object.
func LayoutHash(raw []byte) string {
	if norm, ok := NormalizeLayout(raw); ok {
		return sum(norm)
	}
	return sum(raw)
}

// LayoutsEqual reports whether a and b have the same normalized layout
// structure (or identical raw bytes when not JSON objects).
func LayoutsEqual(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	na, oka := NormalizeLayout(a)
	nb, okb := NormalizeLayout(b)
	return oka && okb && bytes.Equal(na, nb)
}

func layoutMatchesMarker(disk []byte, markerHash string, shipped []byte) bool {
	if markerHash == "" {
		return false
	}
	if LayoutHash(disk) == markerHash || sum(disk) == markerHash {
		return true
	}
	// Upgrade bridge: an older sheepdog stored sum(raw) in the marker, and
	// herdr-app resized panes before this upgrade. When the structure matches
	// the shipped layout, it is still unmodified.
	return len(shipped) > 0 && LayoutsEqual(disk, shipped)
}

func isSwapUnsupported(err error) bool {
	return errors.Is(err, os.ErrInvalid) ||
		errors.Is(err, syscall.ENOSYS) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EXDEV)
}

// swapOrReplace atomically swaps stage into dir using the OS single-syscall
// directory exchange when dir exists, falling back to a two-rename swap if
// the filesystem rejects atomic exchange.
func swapOrReplace(stage, dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		return os.Rename(stage, dir)
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return errors.New("panelsync: refusing to overwrite symlinked panel folder")
	}
	if err := swapDirsFn(stage, dir); err == nil {
		_ = os.RemoveAll(stage)
		return nil
	} else if !isSwapUnsupported(err) {
		return err
	}
	old := stage + ".old"
	if err := os.Rename(dir, old); err != nil {
		return err
	}
	if err := os.Rename(stage, dir); err != nil {
		_ = os.Rename(old, dir)
		return err
	}
	_ = os.RemoveAll(old)
	return nil
}

// InstallPanel stages the panel files under home and swaps mods/sheepdog into
// place atomically, preserving non-Sheepdog files and dropping files listed in
// an earlier install marker that are no longer shipped.
func InstallPanel(home string, panel fs.FS) error {
	src := Source{Panel: panel}
	emb, err := src.embedded()
	if err != nil {
		return err
	}
	dir := modDir(home)
	if isSymlink(dir) {
		return errors.New("panelsync: refusing to overwrite symlinked panel folder")
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	var prev Marker
	hasPrev := false
	if m, err := ReadMarker(dir); err == nil {
		prev, hasPrev = m, true
	}
	stage, err := buildStage(home, dir, emb, prev, hasPrev, nil)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if hasPrev {
		updated := newMarker(Source{Version: prev.Version}, emb, prev.Layout)
		updated.PreviousLayout = prev.PreviousLayout
		updated.Keybinding = prev.Keybinding
		updated.KeyDeclined = prev.KeyDeclined
		if err := writeMarker(stage, updated); err != nil {
			return err
		}
	}
	return swapOrReplace(stage, dir)
}

// RecordPreviousLayout saves the layout herdr-app was showing before setup
// switched it to sheepdog, so uninstall can switch back.
func RecordPreviousLayout(home, layout string) error {
	if layout == "" || layout == "sheepdog" {
		return nil
	}
	dir := modDir(home)
	m, err := ReadMarker(dir)
	if err != nil {
		return err
	}
	if m.PreviousLayout != "" {
		return nil
	}
	m.PreviousLayout = layout
	return writeMarker(dir, m)
}

// RecordKeyDeclined remembers that the user declined the focus shortcut.
func RecordKeyDeclined(home string) error {
	dir := modDir(home)
	m, err := ReadMarker(dir)
	if err != nil {
		return err
	}
	m.KeyDeclined = true
	return writeMarker(dir, m)
}

// InspectKeybinding reports whether key is already bound in <home>/keys.json's
// global section.
func InspectKeybinding(home, key string) (KeyState, error) {
	ks := KeyState{Key: key}
	raw, err := os.ReadFile(filepath.Join(home, keysFile))
	if err != nil {
		if os.IsNotExist(err) {
			return ks, nil
		}
		return ks, err
	}
	ks.FileExists = true
	sections, global, err := parseKeysJSON(raw)
	if err != nil {
		return ks, fmt.Errorf("parse %s: %w", filepath.Join(home, keysFile), err)
	}
	_ = sections
	if cur, ok := global[key]; ok && cur != "" {
		ks.Bound = true
		ks.Current = cur
		ks.AlreadyOurs = cur == FocusCommand
	}
	return ks, nil
}

// InstallKeybinding sets global[key] = "sheepdog.focus" in <home>/keys.json,
// backing up an existing keys.json to keys.json.bak and recording the previous
// state in mods/sheepdog/.sheepdog-install.json.
func InstallKeybinding(home, key string) error {
	if key == "" {
		return errors.New("panelsync: key is empty")
	}
	keysPath := filepath.Join(home, keysFile)
	raw, err := os.ReadFile(keysPath)
	hadFile := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	sections := map[string]json.RawMessage{}
	global := map[string]string{}
	if hadFile {
		sections, global, err = parseKeysJSON(raw)
		if err != nil {
			return fmt.Errorf("parse %s: %w", keysPath, err)
		}
	}
	dir := modDir(home)
	if isSymlink(dir) {
		return errors.New("panelsync: panel folder is a symlink")
	}
	mk, err := ReadMarker(dir)
	if err != nil {
		return fmt.Errorf("read install marker: %w", err)
	}
	cur, hadBinding := global[key]
	if cur == FocusCommand && mk.Keybinding != nil && mk.Keybinding.Key == key {
		return nil
	}
	if hadFile {
		if err := writeAtomic(home, keysBackupFile, raw); err != nil {
			return fmt.Errorf("back up keys.json: %w", err)
		}
	}
	rec := &KeybindingRecord{
		Section:    FocusKeySection,
		Key:        key,
		Command:    FocusCommand,
		HadFile:    hadFile,
		HadBinding: hadBinding && cur != FocusCommand,
	}
	if rec.HadBinding {
		rec.Previous = cur
	} else if mk.Keybinding != nil && mk.Keybinding.Key == key {
		rec = mk.Keybinding
	}
	global[key] = FocusCommand
	gb, err := json.Marshal(global)
	if err != nil {
		return err
	}
	sections[FocusKeySection] = gb
	out, err := json.MarshalIndent(sections, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(home, keysFile, append(out, '\n')); err != nil {
		return err
	}
	mk.Keybinding = rec
	return writeMarker(dir, mk)
}

func parseKeysJSON(raw []byte) (map[string]json.RawMessage, map[string]string, error) {
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil || sections == nil {
		if err == nil {
			err = errors.New("not a JSON object")
		}
		return nil, nil, err
	}
	global := map[string]string{}
	if graw, ok := sections[FocusKeySection]; ok && len(bytes.TrimSpace(graw)) > 0 && !bytes.Equal(bytes.TrimSpace(graw), []byte("null")) {
		if err := json.Unmarshal(graw, &global); err != nil {
			return nil, nil, fmt.Errorf("%s section: %w", FocusKeySection, err)
		}
	}
	return sections, global, nil
}

func isHerdrGeneratedExtra(rel string) bool {
	switch rel {
	case "gpui-kit.d.ts", "herdr.d.ts", "jsconfig.json":
		return true
	default:
		return false
	}
}

func panelSafeToRemove(dir string, mk Marker, hasMk bool) bool {
	if !hasMk || isSymlink(dir) {
		return false
	}
	for rel, want := range mk.Files {
		if h, ok := hashOf(filepath.Join(dir, filepath.FromSlash(rel))); !ok || h != want {
			return false
		}
	}
	clean := true
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			clean = false
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			clean = false
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == MarkerName || isHerdrGeneratedExtra(rel) {
			return nil
		}
		if _, ok := mk.Files[rel]; !ok {
			clean = false
		}
		return nil
	})
	return clean
}

// Uninstall removes unchanged herdr-app panel and layout artifacts, restores
// any opt-in keys.json shortcut setup wrote, and switches shared.json away
// from the sheepdog layout only if sheepdog is still the active layout.
// When dryRun is true, nothing on disk is modified.
func Uninstall(home string, src Source, dryRun bool) (UninstallPlan, error) {
	var plan UninstallPlan
	if home == "" {
		return plan, nil
	}
	if _, err := os.Stat(home); err != nil {
		if os.IsNotExist(err) {
			return plan, nil
		}
		return plan, err
	}
	dir := modDir(home)
	mk, err := ReadMarker(dir)
	hasMk := err == nil

	// 1. Opt-in keys.json binding.
	if hasMk && mk.Keybinding != nil {
		if err := uninstallKeybinding(home, mk.Keybinding, dryRun, &plan); err != nil {
			return plan, err
		}
	}

	// 2. Layout sidecar and layouts/sheepdog.json.
	sidecarPath := filepath.Join(home, "layouts", layoutSidecarFile)
	if _, err := os.Lstat(sidecarPath); err == nil {
		if dryRun {
			plan.Actions = append(plan.Actions, "would remove "+sidecarPath)
		} else {
			if err := os.Remove(sidecarPath); err != nil && !os.IsNotExist(err) {
				return plan, err
			}
			plan.Actions = append(plan.Actions, "removed "+sidecarPath)
		}
	}
	layoutPath := filepath.Join(home, "layouts", layoutFile)
	if disk, err := os.ReadFile(layoutPath); err == nil {
		unmod := (hasMk && layoutMatchesMarker(disk, mk.Layout, src.Layout)) ||
			(len(src.Layout) > 0 && LayoutsEqual(disk, src.Layout))
		if unmod && !isSymlink(layoutPath) {
			if dryRun {
				plan.Actions = append(plan.Actions, "would remove "+layoutPath)
			} else {
				if err := os.Remove(layoutPath); err != nil && !os.IsNotExist(err) {
					return plan, err
				}
				plan.Actions = append(plan.Actions, "removed "+layoutPath)
			}
		} else {
			plan.Kept = append(plan.Kept, "kept "+layoutPath+" (modified)")
		}
	}

	// 3. shared.json active layout (only if still "sheepdog").
	sharedPath := filepath.Join(home, sharedFile)
	if raw, err := os.ReadFile(sharedPath); err == nil {
		var shared map[string]json.RawMessage
		var shown string
		if json.Unmarshal(raw, &shared) == nil && shared != nil && json.Unmarshal(shared["layout"], &shown) == nil && shown == "sheepdog" {
			target := "main"
			if hasMk && mk.PreviousLayout != "" && mk.PreviousLayout != "sheepdog" {
				target = mk.PreviousLayout
			}
			plan.RestoredLayout = target
			if dryRun {
				plan.Actions = append(plan.Actions, fmt.Sprintf("would switch %s layout to %s", sharedPath, target))
			} else {
				tb, _ := json.Marshal(target)
				shared["layout"] = tb
				out, err := json.MarshalIndent(shared, "", "  ")
				if err != nil {
					return plan, err
				}
				if err := writeAtomic(home, sharedFile, append(out, '\n')); err != nil {
					return plan, err
				}
				plan.Actions = append(plan.Actions, fmt.Sprintf("switched %s layout back to %s", sharedPath, target))
			}
		}
	}

	// 4. mods/sheepdog folder.
	if _, err := os.Lstat(dir); err == nil {
		if panelSafeToRemove(dir, mk, hasMk) {
			if dryRun {
				plan.Actions = append(plan.Actions, "would remove "+dir)
			} else {
				if err := os.RemoveAll(dir); err != nil {
					return plan, err
				}
				plan.Actions = append(plan.Actions, "removed "+dir)
			}
		} else {
			plan.Kept = append(plan.Kept, "kept "+dir+" (modified)")
		}
	}
	return plan, nil
}

func uninstallKeybinding(home string, rec *KeybindingRecord, dryRun bool, plan *UninstallPlan) error {
	keysPath := filepath.Join(home, keysFile)
	raw, err := os.ReadFile(keysPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var sections map[string]json.RawMessage
	if json.Unmarshal(raw, &sections) != nil || sections == nil {
		plan.Kept = append(plan.Kept, "kept "+keysPath+" (modified)")
		return nil
	}
	secName := rec.Section
	if secName == "" {
		secName = FocusKeySection
	}
	var sec map[string]string
	if sraw, ok := sections[secName]; !ok || json.Unmarshal(sraw, &sec) != nil {
		return nil
	}
	if sec[rec.Key] != rec.Command {
		return nil
	}
	if rec.HadBinding {
		if dryRun {
			plan.Actions = append(plan.Actions, fmt.Sprintf("would restore %s in %s", rec.Key, keysPath))
			return nil
		}
		sec[rec.Key] = rec.Previous
		sb, err := json.Marshal(sec)
		if err != nil {
			return err
		}
		sections[secName] = sb
		out, err := json.MarshalIndent(sections, "", "  ")
		if err != nil {
			return err
		}
		if err := writeAtomic(home, keysFile, append(out, '\n')); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(home, keysBackupFile))
		plan.Actions = append(plan.Actions, fmt.Sprintf("restored %s in %s", rec.Key, keysPath))
		return nil
	}
	delete(sec, rec.Key)
	if len(sec) == 0 {
		delete(sections, secName)
	} else {
		sb, err := json.Marshal(sec)
		if err != nil {
			return err
		}
		sections[secName] = sb
	}
	if len(sections) == 0 && !rec.HadFile {
		if dryRun {
			plan.Actions = append(plan.Actions, "would remove "+keysPath)
			return nil
		}
		if err := os.Remove(keysPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = os.Remove(filepath.Join(home, keysBackupFile))
		plan.Actions = append(plan.Actions, "removed "+keysPath)
		return nil
	}
	if dryRun {
		plan.Actions = append(plan.Actions, fmt.Sprintf("would remove %s from %s", rec.Key, keysPath))
		return nil
	}
	out, err := json.MarshalIndent(sections, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(home, keysFile, append(out, '\n')); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(home, keysBackupFile))
	plan.Actions = append(plan.Actions, fmt.Sprintf("removed %s from %s", rec.Key, keysPath))
	return nil
}
