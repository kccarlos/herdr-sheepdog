package factory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	herdrsheepdog "github.com/kccarlos/herdr-sheepdog"
)

// EngineMarkerName records the digest and file manifest inside each unpacked
// engine directory so damaged or stale copies self-heal atomically.
const EngineMarkerName = ".sheepdog-engine.json"

const embeddedRoot = "factory"

var pinnedPyVerRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// EngineSource is the embedded factory filesystem and build version to unpack.
// Zero values use herdrsheepdog.Factory and "dev".
type EngineSource struct {
	FS      fs.FS
	Version string
}

// Engine describes an unpacked copy of factory/ under <data_dir>/factory/engine/.
type Engine struct {
	Dir             string
	Version         string
	VersionKey      string
	Digest          string
	PythonVersion   string
	HerdrTask       string
	HerdrSecret     string
	TemplatesDir    string
	ReviewerSandbox string
	Unpacked        bool
}

type engineMarker struct {
	Version       string                      `json:"version"`
	Digest        string                      `json:"digest"`
	PythonVersion string                      `json:"python_version"`
	Files         map[string]engineMarkerFile `json:"files"`
}

type engineMarkerFile struct {
	SHA256 string `json:"sha256"`
	Exec   bool   `json:"exec,omitempty"`
}

type embeddedFile struct {
	data []byte
	hash string
	exec bool
}

// FactoryDir returns <data_dir>/factory, where unpacked engine versions and the
// uv-managed Python interpreter live.
func FactoryDir(dataDir string) string {
	return filepath.Join(dataDir, "factory")
}

// EnginesDir returns <data_dir>/factory/engine.
func EnginesDir(dataDir string) string {
	return filepath.Join(FactoryDir(dataDir), "engine")
}

// PinnedPythonVersion returns the CPython version pinned in the binary's
// embedded factory/.python-version.
func PinnedPythonVersion() string {
	return herdrsheepdog.FactoryPythonVersion()
}

// PinnedPythonVersionFor reads and validates factory/.python-version from fsys
// (defaulting to herdrsheepdog.Factory when fsys is nil).
func PinnedPythonVersionFor(fsys fs.FS) (string, error) {
	if fsys == nil {
		fsys = herdrsheepdog.Factory
	}
	raw, err := fs.ReadFile(fsys, embeddedRoot+"/.python-version")
	if err != nil {
		return "", fmt.Errorf("read embedded .python-version: %w", err)
	}
	v := strings.TrimSpace(string(raw))
	if !pinnedPyVerRe.MatchString(v) {
		return "", fmt.Errorf("invalid embedded .python-version %q (want X.Y.Z)", v)
	}
	return v, nil
}

// EnsureEngine unpacks src into <data_dir>/factory/engine/<version_key> if not
// already present and intact, repairing any missing, modified or non-executable
// runtime file via atomic staging. It starts no process: the factory stays off
// until `factory start`.
func EnsureEngine(dataDir string, src EngineSource) (Engine, error) {
	if strings.TrimSpace(dataDir) == "" {
		return Engine{}, errors.New("factory: data_dir is empty")
	}
	if src.FS == nil {
		src.FS = herdrsheepdog.Factory
	}
	pyVer, err := PinnedPythonVersionFor(src.FS)
	if err != nil {
		return Engine{}, err
	}
	emb, digest, err := readEmbeddedEngine(src.FS)
	if err != nil {
		return Engine{}, err
	}
	ver := strings.TrimSpace(src.Version)
	if ver == "" {
		ver = "dev"
	}
	vKey := engineVersionKey(ver, digest)
	enginesRoot := EnginesDir(dataDir)
	targetDir := filepath.Join(enginesRoot, vKey)

	out := Engine{
		Dir:             targetDir,
		Version:         ver,
		VersionKey:      vKey,
		Digest:          digest,
		PythonVersion:   pyVer,
		HerdrTask:       filepath.Join(targetDir, "mods", "tasks", "bin", "herdr-task"),
		HerdrSecret:     filepath.Join(targetDir, "bin", "herdr-secret"),
		TemplatesDir:    filepath.Join(targetDir, "templates"),
		ReviewerSandbox: filepath.Join(targetDir, "mods", "tasks", "bin", "herdr_task", "reviewer.sb"),
	}

	if verifyEngineDir(targetDir, emb, digest) {
		return out, nil
	}
	if st, lerr := os.Lstat(targetDir); lerr == nil && st.Mode()&os.ModeSymlink != 0 {
		return Engine{}, fmt.Errorf("factory: refusing to overwrite symlinked engine folder %s", targetDir)
	}

	if err := os.MkdirAll(enginesRoot, 0o755); err != nil {
		return Engine{}, fmt.Errorf("create %s: %w", enginesRoot, err)
	}
	stage, err := stageEngineDir(enginesRoot, ver, pyVer, digest, emb)
	if err != nil {
		return Engine{}, err
	}
	defer func() { _ = os.RemoveAll(stage) }()

	if _, lerr := os.Lstat(targetDir); os.IsNotExist(lerr) {
		if rerr := os.Rename(stage, targetDir); rerr != nil {
			if verifyEngineDir(targetDir, emb, digest) {
				return out, nil
			}
			return Engine{}, fmt.Errorf("activate unpacked engine %s: %w", targetDir, rerr)
		}
		out.Unpacked = true
		return out, nil
	}

	old := stage + ".old"
	if err := os.Rename(targetDir, old); err != nil {
		return Engine{}, fmt.Errorf("replace unpacked engine %s: %w", targetDir, err)
	}
	if err := os.Rename(stage, targetDir); err != nil {
		_ = os.Rename(old, targetDir)
		return Engine{}, fmt.Errorf("activate unpacked engine %s: %w", targetDir, err)
	}
	_ = os.RemoveAll(old)
	out.Unpacked = true
	return out, nil
}

// InspectEngine checks whether <data_dir>/factory/engine/<version_key> is
// already unpacked and intact without writing anything to disk.
func InspectEngine(dataDir string, src EngineSource) (Engine, error) {
	if strings.TrimSpace(dataDir) == "" {
		return Engine{}, errors.New("factory: data_dir is empty")
	}
	if src.FS == nil {
		src.FS = herdrsheepdog.Factory
	}
	pyVer, err := PinnedPythonVersionFor(src.FS)
	if err != nil {
		return Engine{}, err
	}
	emb, digest, err := readEmbeddedEngine(src.FS)
	if err != nil {
		return Engine{}, err
	}
	ver := strings.TrimSpace(src.Version)
	if ver == "" {
		ver = "dev"
	}
	vKey := engineVersionKey(ver, digest)
	targetDir := filepath.Join(EnginesDir(dataDir), vKey)
	if !verifyEngineDir(targetDir, emb, digest) {
		return Engine{}, fmt.Errorf("engine %s is not unpacked or differs from embedded build in %s", vKey, targetDir)
	}
	return Engine{
		Dir:             targetDir,
		Version:         ver,
		VersionKey:      vKey,
		Digest:          digest,
		PythonVersion:   pyVer,
		HerdrTask:       filepath.Join(targetDir, "mods", "tasks", "bin", "herdr-task"),
		HerdrSecret:     filepath.Join(targetDir, "bin", "herdr-secret"),
		TemplatesDir:    filepath.Join(targetDir, "templates"),
		ReviewerSandbox: filepath.Join(targetDir, "mods", "tasks", "bin", "herdr_task", "reviewer.sb"),
	}, nil
}

func readEmbeddedEngine(fsys fs.FS) (map[string]embeddedFile, string, error) {
	out := map[string]embeddedFile{}
	err := fs.WalkDir(fsys, embeddedRoot, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(name, embeddedRoot+"/")
		sum := sha256Hex(b)
		out[rel] = embeddedFile{
			data: b,
			hash: sum,
			exec: bytes.HasPrefix(b, []byte("#!")),
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("walk embedded factory: %w", err)
	}
	for _, must := range []string{
		".python-version",
		"NOTICE",
		"bin/herdr-secret",
		"mods/tasks/bin/herdr-task",
		"mods/tasks/bin/herdr_task/__init__.py",
		"mods/tasks/bin/herdr_task/reviewer.sb",
		"templates/assistant.md",
		"templates/reviewer.md",
		"templates/worker.md",
	} {
		if _, ok := out[must]; !ok {
			return nil, "", fmt.Errorf("embedded factory is missing %s", must)
		}
	}

	rels := make([]string, 0, len(out))
	for rel := range out {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	h := sha256.New()
	for _, rel := range rels {
		f := out[rel]
		modeTag := "644"
		if f.exec {
			modeTag = "755"
		}
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\n", rel, modeTag, f.hash)
	}
	return out, hex.EncodeToString(h.Sum(nil)), nil
}

func engineVersionKey(version, digest string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(version) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '+', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	clean := strings.Trim(b.String(), ".-")
	if clean == "" {
		clean = "dev"
	}
	short := digest
	if len(short) > 12 {
		short = short[:12]
	}
	if clean == "dev" || strings.Contains(clean, "dirty") {
		return clean + "-" + short
	}
	return clean
}

func stageEngineDir(enginesRoot, version, pyVer, digest string, emb map[string]embeddedFile) (string, error) {
	stage, err := os.MkdirTemp(enginesRoot, ".engine-stage-*")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}

	rels := make([]string, 0, len(emb))
	for rel := range emb {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	mk := engineMarker{
		Version:       version,
		Digest:        digest,
		PythonVersion: pyVer,
		Files:         make(map[string]engineMarkerFile, len(emb)),
	}
	for _, rel := range rels {
		f := emb[rel]
		dst := filepath.Join(stage, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			_ = os.RemoveAll(stage)
			return "", err
		}
		perm := fs.FileMode(0o644)
		if f.exec {
			perm = 0o755
		}
		if err := os.WriteFile(dst, f.data, perm); err != nil {
			_ = os.RemoveAll(stage)
			return "", err
		}
		if err := os.Chmod(dst, perm); err != nil {
			_ = os.RemoveAll(stage)
			return "", err
		}
		mk.Files[rel] = engineMarkerFile{SHA256: f.hash, Exec: f.exec}
	}

	raw, err := json.MarshalIndent(mk, "", "  ")
	if err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, EngineMarkerName), append(raw, '\n'), 0o644); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	return stage, nil
}

func verifyEngineDir(dir string, emb map[string]embeddedFile, wantDigest string) bool {
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(dir, EngineMarkerName))
	if err != nil {
		return false
	}
	var mk engineMarker
	if json.Unmarshal(raw, &mk) != nil || mk.Digest != wantDigest || len(mk.Files) != len(emb) {
		return false
	}

	seen := 0
	ok := true
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			ok = false
			return err
		}
		if p == dir {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			ok = false
			return fs.SkipAll
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			ok = false
			return fs.SkipAll
		}
		rel = filepath.ToSlash(rel)
		if rel == EngineMarkerName || strings.HasSuffix(rel, ".pyc") {
			return nil
		}
		want, exists := emb[rel]
		if !exists {
			ok = false
			return fs.SkipAll
		}
		info, err := d.Info()
		if err != nil || (info.Mode()&0o111 != 0) != want.exec {
			ok = false
			return fs.SkipAll
		}
		data, err := os.ReadFile(p)
		if err != nil || sha256Hex(data) != want.hash {
			ok = false
			return fs.SkipAll
		}
		seen++
		return nil
	})
	return walkErr == nil && ok && seen == len(emb)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
