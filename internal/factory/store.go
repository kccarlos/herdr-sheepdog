package factory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/config"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// Paths says where the engine keeps its files (design.md 14). Tests build
// one with a temporary DataDir; only the daemon calls DefaultPaths.
type Paths struct {
	DataDir      string
	ClaudeConfig string
}

// DefaultPaths uses the daemon's data dir (config file, then
// SHEEPDOG_DATA_DIR). Under `go test` it panics: a test must never reach the
// real home (KB-TST-1).
func DefaultPaths() (Paths, error) {
	if testing.Testing() {
		panic("factory: DefaultPaths called under go test; use Paths{DataDir: t.TempDir()}")
	}
	cfg, err := config.Load("")
	if err != nil {
		return Paths{}, err
	}
	return Paths{DataDir: cfg.DataDir}, nil
}

func (p Paths) project(id string) string {
	return filepath.Join(p.DataDir, "factory", "projects", id)
}

// State is the project's state.json.
func (p Paths) State(id string) string { return filepath.Join(p.project(id), "state.json") }

// Log is the project's log.jsonl.
func (p Paths) Log(id string) string { return filepath.Join(p.project(id), "log.jsonl") }

// Worktree is a task attempt's git worktree folder.
func (p Paths) Worktree(id, task string, attempt int) string {
	return filepath.Join(p.project(id), "worktrees", fmt.Sprintf("%s-%d", task, attempt))
}

// Report is a review round's findings file (design.md 14).
func (p Paths) Report(id, task string, round int) string {
	return filepath.Join(p.project(id), "reports", fmt.Sprintf("%s-r%d.md", task, round))
}

// ReviewDraft is a review round's engine-named scratch findings file outside
// the worktree, passed to --findings <path> (design.md 7.2, spike S-REV-2).
func (p Paths) ReviewDraft(id, task string, round int, code string) string {
	return filepath.Join(p.project(id), "reports", fmt.Sprintf("%s-r%d-%s.draft.md", task, round, code))
}

// Brief is a task's brief file referenced by a worker handoff note (design.md 4.8).
func (p Paths) Brief(id, task string) string {
	return filepath.Join(p.project(id), "briefs", task+".md")
}
func (p Paths) poolCooldowns() string {
	return filepath.Join(p.DataDir, "factory", "pool-cooldowns.json")
}
func (p Paths) proofs() string { return filepath.Join(p.DataDir, "factory", "proofs.json") }

func (p Paths) CodexHome() string { return filepath.Join(p.DataDir, "factory", "codex-home") }

// LogLine is one pass in log.jsonl.
type LogLine struct {
	Seq      int64     `json:"seq"`
	At       time.Time `json:"at"`
	Project  string    `json:"project"`
	By       string    `json:"by"`
	Task     string    `json:"task,omitempty"`
	Input    any       `json:"input,omitempty"`
	Facts    any       `json:"facts,omitempty"`
	Rule     string    `json:"rule"`
	Asked    []string  `json:"asked,omitempty"`
	Decision string    `json:"decision,omitempty"`
	Why      string    `json:"why,omitempty"`
}

// LoadState reads state.json. A missing or unreadable file is an error:
// the engine never guesses (design.md 15).
func LoadState(path string) (flow.State, error) {
	s := flow.State{Settings: flow.DefaultSettings()} // a setting newer than the file keeps its default
	return s, readJSON(path, &s)
}

func readJSON(path string, dst any) error {
	data, err := readGuarded(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("factory: %s: %w", path, err)
	}
	return nil
}

// SaveState writes state.json with a temporary file and a rename, mode 0600.
func SaveState(path string, s flow.State) error { return writeJSON(path, s) }

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

// AppendLog appends one line to log.jsonl.
func AppendLog(path string, l LogLine) error {
	if err := guardTestPath(path); err != nil {
		return err
	}
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// TrimLog drops log lines past seq, a torn last line, and any line that does
// not parse (design.md 14).
func TrimLog(path string, seq int64) error {
	data, err := readGuarded(path)
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var keep bytes.Buffer
	for _, ln := range bytes.SplitAfter(data, []byte("\n")) {
		if len(ln) == 0 {
			continue
		}
		if !bytes.HasSuffix(ln, []byte("\n")) {
			break // torn last line: an append that did not finish
		}
		var l LogLine
		if json.Unmarshal(ln, &l) == nil && l.Seq <= seq {
			keep.Write(ln)
		}
	}
	if keep.Len() == len(data) {
		return nil
	}
	return writeAtomic(path, keep.Bytes())
}

// guardTestPath refuses, under go test, any store path outside the temp
// dir, so no test can read or write the real home (KB-TST-1, KB-TST-3).
func guardTestPath(path string) error {
	if !testing.Testing() {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(os.TempDir(), abs); err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("factory: %s is outside the temp dir under go test", path)
	}
	return nil
}

func readGuarded(path string) ([]byte, error) {
	if err := guardTestPath(path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func writeAtomic(path string, data []byte) error {
	if err := guardTestPath(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
