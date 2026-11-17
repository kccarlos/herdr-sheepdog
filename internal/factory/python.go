package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultPythonInstallTimeout = 2 * time.Minute
	pythonProbeExpr             = "import encodings, json, sqlite3, ssl, sys; print('%d.%d.%d' % sys.version_info[:3]); print(sys.base_prefix)"
)

// ManagedPython describes a verified uv-managed CPython installation inside
// <data_dir>/factory/python.
type ManagedPython struct {
	Version    string
	InstallDir string
	HomeDir    string
	BinDir     string
	Executable string
	Repaired   bool
}

// PythonOptions configures InspectPython and EnsurePython.
type PythonOptions struct {
	DataDir        string
	PinnedVersion  string
	Home           string
	TempDir        string
	NoRepair       bool
	LookPath       func(string) (string, error)
	RunCommand     CommandFunc
	ProbeTimeout   time.Duration
	InstallTimeout time.Duration
}

// UVMissingError reports that the managed Python needs installation/repair,
// but uv is not installed.
type UVMissingError struct {
	PinnedVersion string
	InstallDir    string
	InspectErr    error
}

func (e *UVMissingError) Error() string {
	return fmt.Sprintf("managed CPython %s is missing or broken in %s (%v), and uv is not on PATH", e.PinnedVersion, e.InstallDir, e.InspectErr)
}

// PythonInstallDir returns <data_dir>/factory/python, where sheepdog keeps its
// uv-managed CPython runtime (never system python3).
func PythonInstallDir(dataDir string) string {
	return filepath.Join(FactoryDir(dataDir), "python")
}

func (o PythonOptions) withDefaults() (PythonOptions, bool) {
	customLookPath := o.LookPath != nil
	if o.PinnedVersion == "" {
		o.PinnedVersion = PinnedPythonVersion()
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.DataDir == "" {
		o.DataDir = filepath.Join(o.Home, ".local", "share", "sheepdog")
	}
	if o.TempDir == "" {
		o.TempDir = os.TempDir()
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.RunCommand == nil {
		o.RunCommand = SystemRunCommand
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = defaultAuthTimeout
	}
	if o.InstallTimeout <= 0 {
		o.InstallTimeout = defaultPythonInstallTimeout
	}
	return o, customLookPath
}

// InspectPython verifies that <data_dir>/factory/python holds an executable,
// working CPython matching opts.PinnedVersion whose binary and stdlib live
// strictly inside <data_dir>/factory/python (never system python3).
func InspectPython(ctx context.Context, opts PythonOptions) (ManagedPython, error) {
	o, _ := opts.withDefaults()
	return inspectPython(ctx, o)
}

func inspectPython(ctx context.Context, o PythonOptions) (ManagedPython, error) {
	if strings.TrimSpace(o.DataDir) == "" {
		return ManagedPython{}, errors.New("data_dir is empty")
	}
	if !pinnedPyVerRe.MatchString(o.PinnedVersion) {
		return ManagedPython{}, fmt.Errorf("invalid pinned Python version %q", o.PinnedVersion)
	}
	installDir := PythonInstallDir(o.DataDir)
	st, err := os.Lstat(installDir)
	if err != nil {
		if os.IsNotExist(err) {
			return ManagedPython{}, fmt.Errorf("not installed in %s", installDir)
		}
		return ManagedPython{}, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return ManagedPython{}, fmt.Errorf("%s is a symlink; refusing external Python directory", installDir)
	}
	if !st.IsDir() {
		return ManagedPython{}, fmt.Errorf("%s is not a directory", installDir)
	}
	realInstallDir, err := filepath.EvalSymlinks(installDir)
	if err != nil {
		return ManagedPython{}, err
	}

	entries, err := os.ReadDir(realInstallDir)
	if err != nil {
		return ManagedPython{}, err
	}
	var candNames []string
	wantExact := "cpython-" + o.PinnedVersion
	wantPrefix := wantExact + "-"
	for _, e := range entries {
		name := e.Name()
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if e.IsDir() && (name == wantExact || strings.HasPrefix(name, wantPrefix)) {
			candNames = append(candNames, name)
		}
	}
	sort.Strings(candNames)
	if len(candNames) == 0 {
		return ManagedPython{}, fmt.Errorf("CPython %s is not installed in %s", o.PinnedVersion, installDir)
	}

	probeCwd := existingDir(o.TempDir, ".")
	var lastReason string
	for _, name := range candNames {
		candHome := filepath.Join(installDir, name)
		realCandHome := filepath.Join(realInstallDir, name)
		candExec := filepath.Join(candHome, "bin", "python3")
		realBin, err := filepath.EvalSymlinks(candExec)
		if err != nil {
			lastReason = fmt.Sprintf("%s: %v", candExec, err)
			continue
		}
		// Hard rail: the resolved binary must stay inside <data_dir>/factory/python,
		// so a symlink to /usr/bin/python3 or Homebrew is rejected before execution.
		if !isWithinDir(realBin, realInstallDir) {
			lastReason = fmt.Sprintf("%s resolves outside %s (%s)", candExec, installDir, realBin)
			continue
		}
		info, err := os.Stat(realBin)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			lastReason = fmt.Sprintf("%s is not an executable file", candExec)
			continue
		}

		pCtx, cancel := context.WithTimeout(ctx, o.ProbeTimeout)
		out := o.RunCommand(pCtx, probeCwd, []string{
			"PYTHONNOUSERSITE=1",
			"PYTHONDONTWRITEBYTECODE=1",
		}, candExec, "-I", "-B", "-c", pythonProbeExpr)
		cancel()

		if out.TimedOut {
			lastReason = fmt.Sprintf("%s timed out after %s", candExec, o.ProbeTimeout)
			continue
		}
		if out.Err != nil || out.Exit != 0 {
			why := cleanReason(firstNonEmpty(errText(out.Err), out.Stderr, out.Stdout, fmt.Sprintf("exit %d", out.Exit)))
			lastReason = fmt.Sprintf("%s failed probe: %s", candExec, why)
			continue
		}
		lines := nonEmptyLines(out.Stdout)
		if len(lines) < 2 {
			lastReason = fmt.Sprintf("%s returned unexpected probe output %q", candExec, cleanReason(out.Stdout))
			continue
		}
		gotVer, gotPrefix := lines[0], lines[1]
		if gotVer != o.PinnedVersion {
			lastReason = fmt.Sprintf("found Python %s, want pinned %s", gotVer, o.PinnedVersion)
			continue
		}
		if !prefixWithinManagedDir(gotPrefix, installDir, realInstallDir) {
			lastReason = fmt.Sprintf("sys.base_prefix %q is outside %s", gotPrefix, installDir)
			continue
		}
		_ = realCandHome
		return ManagedPython{
			Version:    gotVer,
			InstallDir: installDir,
			HomeDir:    candHome,
			BinDir:     filepath.Join(candHome, "bin"),
			Executable: candExec,
		}, nil
	}
	return ManagedPython{}, errors.New(lastReason)
}

// EnsurePython checks for the pinned uv-managed CPython in <data_dir>/factory/python
// and, when missing or broken (unless opts.NoRepair is true), installs/repairs
// it with `uv python install --reinstall` and re-verifies the result.
func EnsurePython(ctx context.Context, opts PythonOptions) (ManagedPython, error) {
	o, customLookPath := opts.withDefaults()
	if py, err := inspectPython(ctx, o); err == nil {
		return py, nil
	} else if o.NoRepair {
		return ManagedPython{}, err
	} else {
		uvBin, ok := resolveUVBinary(o.LookPath, customLookPath, o.Home)
		if !ok {
			return ManagedPython{}, &UVMissingError{
				PinnedVersion: o.PinnedVersion,
				InstallDir:    PythonInstallDir(o.DataDir),
				InspectErr:    err,
			}
		}
		installDir := PythonInstallDir(o.DataDir)
		if st, lerr := os.Lstat(installDir); lerr == nil && st.Mode()&os.ModeSymlink != 0 {
			return ManagedPython{}, fmt.Errorf("%s is a symlink; refusing external Python directory", installDir)
		}
		if mkErr := os.MkdirAll(installDir, 0o755); mkErr != nil {
			return ManagedPython{}, fmt.Errorf("create %s: %w", installDir, mkErr)
		}

		probeCwd := existingDir(o.TempDir, ".")
		iCtx, cancel := context.WithTimeout(ctx, o.InstallTimeout)
		res := o.RunCommand(iCtx, probeCwd, []string{"UV_NO_PROGRESS=1"},
			uvBin, "python", "install",
			"--reinstall",
			"--install-dir", installDir,
			"--no-bin",
			"--managed-python",
			"--no-config",
			o.PinnedVersion,
		)
		cancel()

		if res.TimedOut && ctx.Err() == nil {
			return ManagedPython{}, fmt.Errorf("uv python install %s timed out after %s", o.PinnedVersion, o.InstallTimeout)
		}
		if res.Err != nil || res.Exit != 0 {
			why := cleanReason(firstNonEmpty(errText(res.Err), res.Stderr, res.Stdout, fmt.Sprintf("exit %d", res.Exit)))
			return ManagedPython{}, fmt.Errorf("uv python install %s failed: %s", o.PinnedVersion, why)
		}

		py, verifyErr := inspectPython(ctx, o)
		if verifyErr != nil {
			return ManagedPython{}, fmt.Errorf("uv python install %s exited 0, but verification failed: %w", o.PinnedVersion, verifyErr)
		}
		py.Repaired = true
		return py, nil
	}
}

// EngineEnv returns environ with the managed Python's bin directory and the
// unpacked engine's CLI directories prepended to PATH (plus bytecode/user-site
// isolation), so every `python3`, `herdr-task` and `herdr-secret` invocation
// resolves to the pinned runtime instead of system python3.
func EngineEnv(environ []string, py ManagedPython, eng Engine) []string {
	var prepend []string
	for _, d := range []string{
		py.BinDir,
		filepath.Dir(eng.HerdrTask),
		filepath.Dir(eng.HerdrSecret),
	} {
		if d = strings.TrimSpace(d); d != "" && d != "." {
			prepend = append(prepend, d)
		}
	}
	out := make([]string, 0, len(environ)+3)
	var oldPath string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "PATH":
			oldPath = v
		case "PYTHONDONTWRITEBYTECODE", "PYTHONNOUSERSITE":
			continue
		default:
			out = append(out, kv)
		}
	}
	parts := append([]string(nil), prepend...)
	if oldPath != "" {
		parts = append(parts, oldPath)
	}
	out = append(out,
		"PATH="+strings.Join(parts, string(os.PathListSeparator)),
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONNOUSERSITE=1",
	)
	return out
}

func resolveUVBinary(lookPath func(string) (string, error), customLookPath bool, home string) (string, bool) {
	if p, err := lookPath("uv"); err == nil && p != "" {
		return p, true
	}
	if !customLookPath && home != "" {
		cand := filepath.Join(home, ".local", "bin", "uv")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return cand, true
		}
	}
	return "", false
}

func prefixWithinManagedDir(prefix, installDir, realInstallDir string) bool {
	if prefix == "" {
		return false
	}
	if realPfx, err := filepath.EvalSymlinks(prefix); err == nil {
		return isWithinDir(realPfx, realInstallDir)
	}
	return isWithinDir(filepath.Clean(prefix), filepath.Clean(installDir)) ||
		isWithinDir(filepath.Clean(prefix), realInstallDir)
}

func isWithinDir(target, root string) bool {
	target = filepath.Clean(target)
	root = filepath.Clean(root)
	if target == root {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func nonEmptyLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}
