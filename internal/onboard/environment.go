package onboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/panelsync"
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
	OS                 string
	OldHerdr           Presence
	HerdrCore          Presence
	HerdrApp           bool
	HerdrAppHome       string
	Panel              PanelState
	ShownLayout        string
	PanelDocked        bool
	Layout             LayoutState
	SheepdogApp        bool
	Claude             bool
	ClaudeHook         string
	ClaudeHookNeedsFix bool
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
	env.Claude = probeLookPath(p, "claude")
	if p.ReadFile != nil && p.Home != "" {
		if raw, err := p.ReadFile(filepath.Join(p.Home, ".claude", "settings.json")); err == nil {
			var current bool
			_, env.ClaudeHook, current, _ = editClaudeSettings(raw, "")
			env.ClaudeHookNeedsFix = env.ClaudeHook != "" && !current
		}
	}
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

// ShownLayout is the layout herdr-app shows, from <home>/shared.json
// ("main" when it cannot tell).
func ShownLayout(home string, p Probe) string { return detectShownLayout(home, p) }

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
	if panelsync.LayoutsEqual(got, want) {
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

// InstallPanel stages the panel files under home and swaps home/mods/sheepdog
// into place atomically, preserving non-Sheepdog files and removing dropped
// files from an earlier marker.
func InstallPanel(home string, panel fs.FS) error {
	if panel == nil {
		return errors.New("onboard: panel filesystem is nil")
	}
	return panelsync.InstallPanel(home, panel)
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

const claudeHookArgs = " hook claude-permission"

var (
	claudeSettingsMu sync.Mutex
	shellSafe        = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)
)

func shellQuote(s string) string {
	if s != "" && shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func unquoteShell(s string) (bin string, quotedOK bool) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		inner := strings.ReplaceAll(s[1:len(s)-1], `'\''`, "\x00")
		if strings.ContainsRune(inner, '\'') {
			return "", false
		}
		bin = strings.ReplaceAll(inner, "\x00", "'")
		return bin, bin != ""
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		inner := strings.ReplaceAll(strings.ReplaceAll(s[1:len(s)-1], `\"`, `"`), `\\`, `\`)
		return inner, inner != "" && !strings.ContainsAny(inner, "$`")
	}
	return s, s != "" && shellSafe.MatchString(s)
}

// UpdateClaudeHook sets (wantBin != "") or removes (wantBin == "") Sheepdog's
// PermissionRequest entry in <home>/.claude/settings.json.
func UpdateClaudeHook(home, wantBin string, dryRun bool) (string, error) {
	if home == "" {
		return "", nil
	}
	claudeSettingsMu.Lock()
	defer claudeSettingsMu.Unlock()

	p := filepath.Join(home, ".claude", "settings.json")
	for _, check := range []string{filepath.Dir(p), p, p + ".bak"} {
		if err := rejectSymlink(check); err != nil {
			return "", err
		}
	}
	for range 3 {
		raw, err := os.ReadFile(p)
		existed := err == nil
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
		if err != nil || (wantBin == "" && !existed) {
			return "", err
		}
		next, prev, current, err := editClaudeSettings(raw, wantBin)
		if err != nil || (wantBin == "" && prev == "") || (wantBin != "" && prev == wantBin && current) {
			return "", err
		}
		if dryRun {
			return "would remove Sheepdog hook from " + p, nil
		}
		curRaw, curErr := os.ReadFile(p)
		if (existed && (curErr != nil || !bytes.Equal(curRaw, raw))) || (!existed && !errors.Is(curErr, os.ErrNotExist)) {
			continue
		}
		bak := ""
		if wantBin != "" && existed {
			if err := writeFileAtomic(p+".bak", raw); err != nil {
				return "", err
			}
			bak = " (backup kept at " + p + ".bak)"
		}
		if err := writeFileAtomic(p, next); err != nil {
			return "", err
		}
		if wantBin == "" {
			return "removed Sheepdog hook from " + p, nil
		}
		return "Added Sheepdog's PermissionRequest hook to " + p + bak + ".", nil
	}
	return "", errors.New("onboard: settings.json changed during update; try again")
}

func claudeHookCheck(enabled bool, env Environment) Result {
	switch _, missing := os.Stat(env.ClaudeHook); {
	case !enabled:
		return Result{Name: "claude hook", Status: Skip, Detail: "disabled by hooks.claude_permission"}
	case !env.Claude && env.ClaudeHook == "":
		return Result{Name: "claude hook", Status: Skip, Detail: "claude not found"}
	case env.ClaudeHook == "":
		return Result{Name: "claude hook", Status: Warn, Detail: "not installed in ~/.claude/settings.json", Fix: "sheepdog setup"}
	case missing != nil:
		return Result{Name: "claude hook", Status: Warn, Detail: "hook binary missing: " + env.ClaudeHook, Fix: "sheepdog setup"}
	case env.ClaudeHookNeedsFix:
		return Result{Name: "claude hook", Status: Warn, Detail: "outdated entry in ~/.claude/settings.json", Fix: "sheepdog setup"}
	default:
		return Result{Name: "claude hook", Status: OK, Detail: "installed in ~/.claude/settings.json"}
	}
}

func editClaudeSettings(raw []byte, wantBin string) ([]byte, string, bool, error) {
	root, hooks := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	var groups []map[string]json.RawMessage
	if len(bytes.TrimSpace(raw)) > 0 && (json.Unmarshal(raw, &root) != nil || root == nil) ||
		len(root["hooks"]) > 0 && string(root["hooks"]) != "null" && json.Unmarshal(root["hooks"], &hooks) != nil ||
		len(hooks["PermissionRequest"]) > 0 && json.Unmarshal(hooks["PermissionRequest"], &groups) != nil {
		return nil, "", false, errors.New("invalid settings.json")
	}
	var found string
	count, allValid := 0, true
	groups = slices.DeleteFunc(groups, func(g map[string]json.RawMessage) bool {
		var hs []map[string]any
		if json.Unmarshal(g["hooks"], &hs) != nil {
			return false
		}
		hs = slices.DeleteFunc(hs, func(h map[string]any) bool {
			s, _ := h["command"].(string)
			prefix, ok := strings.CutSuffix(strings.TrimSpace(s), claudeHookArgs)
			if !ok {
				return false
			}
			count++
			bin, quotedOK := unquoteShell(prefix)
			found = bin
			asyncOK, _ := h["async"].(bool)
			if !quotedOK || !asyncOK || (wantBin != "" && strings.TrimSpace(s) != shellQuote(wantBin)+claudeHookArgs) {
				allValid = false
			}
			return true
		})
		g["hooks"], _ = json.Marshal(hs)
		return len(hs) == 0
	})
	if wantBin != "" {
		b, _ := json.Marshal([]map[string]any{{"type": "command", "command": shellQuote(wantBin) + claudeHookArgs, "async": true}})
		groups = append(groups, map[string]json.RawMessage{"hooks": b})
	}
	delete(hooks, "PermissionRequest")
	if len(groups) > 0 {
		hooks["PermissionRequest"], _ = json.Marshal(groups)
	}
	delete(root, "hooks")
	if len(hooks) > 0 {
		root["hooks"], _ = json.Marshal(hooks)
	}
	out, err := json.MarshalIndent(root, "", "  ")
	return append(out, '\n'), found, count == 1 && allValid, err
}
