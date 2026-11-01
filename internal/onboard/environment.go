package onboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
)

// Probe is the machine access environment detection needs. Tests replace
// every function so detection never reads the real home or dials a socket.
type Probe struct {
	GOOS          string
	Home          string
	Getenv        func(string) string
	LookPath      func(string) (string, error)
	Stat          func(string) (fs.FileInfo, error)
	ReadFile      func(string) ([]byte, error)
	SocketAnswers func(string) bool
}

// SystemProbe returns the real machine's probe.
func SystemProbe() Probe {
	home, _ := os.UserHomeDir()
	return Probe{
		GOOS:          runtime.GOOS,
		Home:          home,
		Getenv:        os.Getenv,
		LookPath:      exec.LookPath,
		Stat:          os.Stat,
		ReadFile:      os.ReadFile,
		SocketAnswers: socketAnswers,
	}
}

// Presence says whether a herdr program is installed and whether its socket
// answers.
type Presence struct {
	Installed bool
	Answers   bool
}

// PanelState is the herdr-app panel compared with the one in this binary.
type PanelState int

const (
	PanelNotApplicable PanelState = iota
	PanelMissing
	PanelOutdated
	PanelCurrent
)

func (s PanelState) String() string {
	switch s {
	case PanelMissing:
		return "missing"
	case PanelOutdated:
		return "outdated"
	case PanelCurrent:
		return "current"
	default:
		return "not applicable"
	}
}

// LayoutState is the Sheepdog layout compared with the one in this binary.
type LayoutState int

const (
	LayoutMissing LayoutState = iota
	LayoutCurrent
	LayoutDifferent
)

func (s LayoutState) String() string {
	switch s {
	case LayoutCurrent:
		return "current"
	case LayoutDifferent:
		return "different"
	default:
		return "missing"
	}
}

// Environment is the herdr setup found on this computer.
type Environment struct {
	OS           string
	OldHerdr     Presence
	HerdrCore    Presence
	HerdrApp     bool
	HerdrAppHome string
	Panel        PanelState
	ShownLayout  string
	PanelDocked  bool
	Layout       LayoutState
	SheepdogApp  bool
}

// Detect inspects the installed herdr tools and compares the panel in panel
// and the layout in layout with the copies installed for herdr-app.
func Detect(cfg config.Config, p Probe, panel fs.FS, layout []byte) Environment {
	env := Environment{
		OS:           p.GOOS,
		OldHerdr:     Presence{Installed: probeLookPath(p, "herdr"), Answers: probeSocket(p, cfg.Herdr.Socket)},
		HerdrCore:    Presence{Installed: probeLookPath(p, "hc") || probeLookPath(p, "herdr-core"), Answers: probeSocket(p, cfg.Herdr.HCSocketPath())},
		HerdrApp:     probeLookPath(p, "herdr-app"),
		HerdrAppHome: probeEnv(p, "HERDR_APP_HOME"),
	}
	if env.HerdrAppHome == "" {
		env.HerdrAppHome = filepath.Join(p.Home, ".config", "herdr-app")
	}
	if p.GOOS == "darwin" {
		env.HerdrApp = env.HerdrApp ||
			probeIsDir(p, "/Applications/Herdr.app") ||
			probeIsDir(p, filepath.Join(p.Home, "Applications", "Herdr.app"))
		env.SheepdogApp = probeIsDir(p, "/Applications/Sheepdog.app") ||
			probeIsDir(p, filepath.Join(p.Home, "Applications", "Sheepdog.app"))
	}
	env.Panel = detectPanel(env.HerdrApp, env.HerdrAppHome, p, panel)
	env.ShownLayout = detectShownLayout(env.HerdrAppHome, p)
	env.PanelDocked = detectPanelDocked(env.HerdrAppHome, env.ShownLayout, p)
	env.Layout = detectLayout(env.HerdrAppHome, p, layout)
	return env
}

func probeEnv(p Probe, key string) string {
	if p.Getenv == nil {
		return ""
	}
	return p.Getenv(key)
}

func probeLookPath(p Probe, name string) bool {
	if p.LookPath == nil {
		return false
	}
	_, err := p.LookPath(name)
	return err == nil
}

func probeSocket(p Probe, socket string) bool {
	return p.SocketAnswers != nil && socket != "" && p.SocketAnswers(socket)
}

func probeIsDir(p Probe, name string) bool {
	if p.Stat == nil {
		return false
	}
	st, err := p.Stat(name)
	return err == nil && st.IsDir()
}

func detectPanel(applicable bool, home string, p Probe, panel fs.FS) PanelState {
	if !applicable {
		return PanelNotApplicable
	}
	root := filepath.Join(home, "mods", "sheepdog")
	if !probeIsDir(p, root) {
		return PanelMissing
	}
	if panel == nil || p.ReadFile == nil {
		return PanelOutdated
	}
	err := fs.WalkDir(panel, "mods/sheepdog", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		want, err := fs.ReadFile(panel, name)
		if err != nil {
			return err
		}
		got, err := p.ReadFile(filepath.Join(home, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(got, want) {
			return fs.ErrInvalid
		}
		return nil
	})
	if errors.Is(err, fs.ErrInvalid) {
		return PanelOutdated
	}
	if err != nil {
		return PanelOutdated
	}
	return PanelCurrent
}

func detectShownLayout(home string, p Probe) string {
	const main = "main"
	if p.ReadFile == nil {
		return main
	}
	data, err := p.ReadFile(filepath.Join(home, "shared.json"))
	if err != nil {
		return main
	}
	var shared map[string]json.RawMessage
	if json.Unmarshal(data, &shared) != nil {
		return main
	}
	var name string
	if raw, ok := shared["layout"]; !ok || json.Unmarshal(raw, &name) != nil {
		return main
	}
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return main
	}
	return name
}

func detectPanelDocked(home, name string, p Probe) bool {
	if p.ReadFile == nil {
		return false
	}
	data, err := p.ReadFile(filepath.Join(home, "layouts", name+".json"))
	if err != nil {
		return false
	}
	var layout any
	if json.Unmarshal(data, &layout) != nil {
		return false
	}
	return jsonReferencesMod(layout, "sheepdog")
}

func jsonReferencesMod(value any, mod string) bool {
	switch v := value.(type) {
	case map[string]any:
		if content, ok := v["content"].(map[string]any); ok && content["mod"] == mod {
			return true
		}
		for _, child := range v {
			if jsonReferencesMod(child, mod) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if jsonReferencesMod(child, mod) {
				return true
			}
		}
	}
	return false
}

func detectLayout(home string, p Probe, want []byte) LayoutState {
	if p.ReadFile == nil {
		return LayoutMissing
	}
	got, err := p.ReadFile(filepath.Join(home, "layouts", "sheepdog.json"))
	if err != nil {
		return LayoutMissing
	}
	if bytes.Equal(got, want) {
		return LayoutCurrent
	}
	return LayoutDifferent
}

// InstallLayout writes the embedded Sheepdog layout atomically with mode
// 0644. Setup calls it only when the layout file is missing.
func InstallLayout(home string, layout []byte) error {
	dir := filepath.Join(home, "layouts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".sheepdog-layout-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(layout); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, "sheepdog.json")); err != nil {
		return err
	}
	return os.Chmod(filepath.Join(dir, "sheepdog.json"), 0o644)
}

// InstallPanel replaces every file in panel under home/mods/sheepdog. Other
// files are left alone, and each file is replaced atomically in its directory.
func InstallPanel(home string, panel fs.FS) error {
	if panel == nil {
		return errors.New("onboard: panel filesystem is nil")
	}
	return fs.WalkDir(panel, "mods/sheepdog", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(panel, name)
		if err != nil {
			return err
		}
		dst := filepath.Join(home, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(dst), ".sheepdog-panel-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		defer func() { _ = os.Remove(tmpName) }()
		if err := tmp.Chmod(0o644); err != nil {
			_ = tmp.Close()
			return err
		}
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		if err := os.Rename(tmpName, dst); err != nil {
			return err
		}
		return os.Chmod(dst, 0o644)
	})
}

// BackendAdvice is how setup should resolve herdr.backend = auto.
type BackendAdvice struct {
	Set     string
	Ask     bool
	Default string
	Why     string
}

// AdviseBackend chooses whether to pin a backend or ask, using what is
// installed and what is running.
func AdviseBackend(cfg config.Config, env Environment) BackendAdvice {
	if cfg.Herdr.Backend == config.HerdrBackendHerdr || cfg.Herdr.Backend == config.HerdrBackendHC {
		return BackendAdvice{}
	}
	const (
		whyBothRunning = "Both are running, and auto would pick herdr-tui."
		whyCoreRunning = "Only herdr-core is running, so pinning it avoids waiting for herdr-tui after a restart."
		whyApp         = "herdr-app uses herdr-core, but only herdr-tui is running."
		whyBoth        = "Both are installed, so choose the one Sheepdog should start with."
		whyCore        = "Only herdr-core is installed, so Sheepdog should wait for herdr-core."
	)
	switch {
	case env.OldHerdr.Answers && env.HerdrCore.Answers:
		def := config.HerdrBackendHerdr
		if env.HerdrApp {
			def = config.HerdrBackendHC
		}
		return BackendAdvice{Ask: true, Default: def, Why: whyBothRunning}
	case env.HerdrCore.Answers:
		return BackendAdvice{Set: config.HerdrBackendHC, Why: whyCoreRunning}
	case env.OldHerdr.Answers:
		if env.HerdrApp && env.HerdrCore.Installed {
			return BackendAdvice{Ask: true, Default: config.HerdrBackendHC, Why: whyApp}
		}
	case env.HerdrCore.Installed && env.OldHerdr.Installed:
		def := config.HerdrBackendHerdr
		if env.HerdrApp {
			def = config.HerdrBackendHC
		}
		return BackendAdvice{Ask: true, Default: def, Why: whyBoth}
	case env.HerdrCore.Installed:
		return BackendAdvice{Set: config.HerdrBackendHC, Why: whyCore}
	}
	return BackendAdvice{}
}
