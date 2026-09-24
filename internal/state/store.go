package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Options configures Open.
type Options struct {
	// Aliases maps pane_id -> alias from the config file. It wins over a
	// derived alias, both for new agents and for rows loaded from disk.
	Aliases map[string]string
	// Warn receives one line per recoverable problem (corrupt file, alias
	// collision); nil discards.
	Warn io.Writer
	// FullFsync turns on the hardware-durable fsync pass (state.full_fsync
	// in config), debounced to at most one per fsyncDebounce window (plus
	// a final one on Close). Off by default: the plain per-write fsync
	// (see sync_unix.go) is already crash-consistent and orders of
	// magnitude cheaper on macOS, where the durable path is F_FULLFSYNC.
	FullFsync bool
}

// fsyncDebounce is how long a burst of Apply/Update calls coalesces into
// a single hardware-durable fsync pass when Options.FullFsync is set. A
// var (not a const) only so tests can shorten/lengthen it; production
// code never changes it.
var fsyncDebounce = 100 * time.Millisecond

// Store is the state table with atomic JSON persistence: every mutation
// writes a temp file next to path, syncs it and renames it over path —
// synchronously, so the file on disk is always current. Store is safe
// for concurrent use.
type Store struct {
	mu    sync.Mutex
	path  string
	table Table
	opts  Options
	now   func() time.Time

	fsMu    sync.Mutex
	fsTimer *time.Timer
	fsDirty bool
	fsDone  bool

	// onFullSync, when set, is called every time fullSync runs (tests
	// only, to count/observe debounce coalescing without racing on I/O
	// timing).
	onFullSync func()
}

// Open loads the table from path (an absent file starts empty; a corrupt
// one is renamed aside, logged to Warn, and the store starts empty) and
// applies configured aliases to the loaded rows. An empty path keeps the
// table in memory only.
func Open(path string, opts Options) (*Store, error) {
	s := &Store{path: path, table: newTable(), opts: opts, now: time.Now}
	if opts.Aliases == nil {
		s.opts.Aliases = map[string]string{}
	}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("state: read %s: %w", path, err)
	}
	var t Table
	if err := json.Unmarshal(data, &t); err != nil {
		aside := path + ".corrupt"
		_ = os.Rename(path, aside)
		s.warn("state: %s is corrupt (%v); moved to %s and starting empty", path, err, aside)
		return s, nil
	}
	if t.Agents == nil {
		t.Agents = map[string]*Agent{}
	}
	for id, a := range t.Agents {
		if a.PaneID == "" {
			a.PaneID = id
		}
		if alias, ok := s.opts.Aliases[id]; ok && alias != "" && a.Alias != alias {
			a.Alias, a.AliasAuto = s.uniqueFor(id, alias), false
		}
	}
	s.table = t
	return s, nil
}

func (s *Store) warn(format string, args ...any) {
	if s.opts.Warn != nil {
		_, _ = fmt.Fprintf(s.opts.Warn, format+"\n", args...)
	}
}

// Path returns the file the store persists to ("" for in-memory).
func (s *Store) Path() string { return s.path }

// Apply folds a herdr Transition into the table and persists it. It
// returns the resulting row (zero Agent when the pane was removed) and
// whether anything changed. A new pane gets its alias here; state_since is
// only touched when the state actually changes.
func (s *Store) Apply(t herdr.Transition) (Agent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if t.Closed {
		a, ok := s.table.Agents[t.PaneID]
		if !ok {
			return Agent{}, false, nil
		}
		if a.Synthetic && t.Source != SourceInject {
			return a.clone(), false, nil
		}
		delete(s.table.Agents, t.PaneID)
		if s.table.PendingAction != nil && s.table.PendingAction.PaneID == t.PaneID {
			s.table.PendingAction = nil
		}
		return Agent{}, true, s.persist()
	}

	a, ok := s.table.Agents[t.PaneID]
	changed, identity := false, false
	if !ok {
		a = &Agent{PaneID: t.PaneID}
		s.table.Agents[t.PaneID] = a
		changed, identity = true, true
	}
	if t.Agent != "" && a.Kind != t.Agent {
		a.Kind, identity = t.Agent, true
	}
	if t.WorkspaceID != "" && a.Workspace != t.WorkspaceID {
		a.Workspace, changed = t.WorkspaceID, true
	}
	if t.WorkspaceLabel != "" && a.WorkspaceLabel != t.WorkspaceLabel {
		a.WorkspaceLabel, identity = t.WorkspaceLabel, true
	}
	if t.Name != "" && a.Name != t.Name {
		a.Name, identity = t.Name, true
	}
	if sess := t.Session; sess != nil && sess.Value != "" && (a.SessionValue != sess.Value || a.SessionKind != sess.Kind || a.SessionSource != sess.Source) {
		a.SessionSource, a.SessionKind, a.SessionValue, changed = sess.Source, sess.Kind, sess.Value, true
	}
	if t.Cwd != "" && a.Cwd != t.Cwd {
		a.Cwd, changed = t.Cwd, true
	}
	if identity {
		changed = true
		if a.Alias == "" || a.AliasAuto {
			a.Alias, a.AliasAuto = s.defaultAlias(a)
		}
	}
	if t.To != "" && a.State != t.To {
		at := t.At
		if at.IsZero() {
			at = s.now()
		}
		a.State, a.StateSince, changed = t.To, at, true
		if t.To == herdr.StateBlocked {
			a.LastBlock = &Block{SeenAt: at}
		} else if a.LastBlock != nil && !a.LastBlock.Answered && t.From == herdr.StateBlocked {
			// Left the prompt by any route (answered, cancelled, exited).
			a.LastBlock.Answered = true
		}
	}
	if !changed {
		return a.clone(), false, nil
	}
	return a.clone(), true, s.persist()
}

// defaultAlias returns the config alias for the pane (pinned) or a derived
// one (auto).
func (s *Store) defaultAlias(a *Agent) (alias string, auto bool) {
	if alias, ok := s.opts.Aliases[a.PaneID]; ok && alias != "" {
		return s.uniqueFor(a.PaneID, alias), false
	}
	return s.uniqueFor(a.PaneID, deriveAlias(a.WorkspaceLabel, a.Name, a.Kind)), true
}

// uniqueFor returns alias or alias-N so that no other pane uses it.
func (s *Store) uniqueFor(paneID, alias string) string {
	u := uniqueAlias(alias, func(c string) bool {
		for id, other := range s.table.Agents {
			if id != paneID && other.Alias == c {
				return true
			}
		}
		return false
	})
	if u != alias {
		s.warn("state: alias %q for %s already used; using %q", alias, paneID, u)
	}
	return u
}

// SetAlias renames an agent. The alias is slugged; an empty result or a
// collision with another pane is an error.
func (s *Store) SetAlias(paneID, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.table.Agents[paneID]
	if !ok {
		return fmt.Errorf("state: no agent in pane %s", paneID)
	}
	slug := Slug(alias)
	if slug == "" {
		return fmt.Errorf("state: alias %q has no usable characters", alias)
	}
	for id, other := range s.table.Agents {
		if id != paneID && other.Alias == slug {
			return fmt.Errorf("state: alias %q already used by %s", slug, id)
		}
	}
	if a.Alias == slug {
		return nil
	}
	a.Alias, a.AliasAuto = slug, false
	return s.persist()
}

// Update mutates the row for paneID under the lock and persists. It is
// how the supervisor records the block question, the current task and
// the answered flag. A missing pane is an error; fn must not change
// PaneID or Alias.
func (s *Store) Update(paneID string, fn func(*Agent)) (Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.table.Agents[paneID]
	if !ok {
		return Agent{}, fmt.Errorf("state: no agent in pane %s", paneID)
	}
	fn(a)
	return a.clone(), s.persist()
}

// AppendJournal adds e to the agent's work journal, dropping the oldest
// entries past MaxJournal, and persists (docs/iteration-2.md WP4).
func (s *Store) AppendJournal(paneID string, e JournalEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.table.Agents[paneID]
	if !ok {
		return fmt.Errorf("state: no agent in pane %s", paneID)
	}
	a.Journal = append(a.Journal, e.clone())
	if over := len(a.Journal) - MaxJournal; over > 0 {
		a.Journal = append([]JournalEntry(nil), a.Journal[over:]...)
	}
	return s.persist()
}

// MarkSynthetic flags the row for paneID as injected by the debug page
// (see Agent.Synthetic) and persists.
func (s *Store) MarkSynthetic(paneID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.table.Agents[paneID]
	if !ok {
		return fmt.Errorf("state: no agent in pane %s", paneID)
	}
	if a.Synthetic {
		return nil
	}
	a.Synthetic = true
	return s.persist()
}

// Get returns the row for paneID.
func (s *Store) Get(paneID string) (Agent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.table.Agents[paneID]
	if !ok {
		return Agent{}, false
	}
	return a.clone(), true
}

// List returns every row sorted by pane id.
func (s *Store) List() []Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Agent, 0, len(s.table.Agents))
	for _, a := range s.table.Agents {
		out = append(out, a.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PaneID < out[j].PaneID })
	return out
}

// Pending returns the current pending action, if any.
func (s *Store) Pending() (PendingAction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.table.PendingAction == nil {
		return PendingAction{}, false
	}
	return s.table.PendingAction.clone(), true
}

// SetPending replaces the pending action (one at a time) and persists.
func (s *Store) SetPending(p PendingAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p = p.clone()
	s.table.PendingAction = &p
	return s.persist()
}

// ClearPending removes the pending action and persists.
func (s *Store) ClearPending() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.table.PendingAction == nil {
		return nil
	}
	s.table.PendingAction = nil
	return s.persist()
}

// UpdateSession mutates the session bookkeeping under the lock and
// persists.
func (s *Store) UpdateSession(fn func(*Session)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.table.Session)
	return s.persist()
}

// Snapshot returns a deep copy of the whole table.
func (s *Store) Snapshot() Table {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.table.clone()
}

// persist writes the table atomically: temp file in the same directory,
// (plain, cheap) fsync, rename — always synchronous, so the file on disk
// reflects this call by the time it returns. Caller holds s.mu. When
// Options.FullFsync is set, it also schedules the debounced
// hardware-durable pass (scheduleFullSync); that part, and only that
// part, is coalesced across a burst of calls.
func (s *Store) persist() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.table, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encode: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json.tmp")
	if err != nil {
		return fmt.Errorf("state: temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("state: write: %w", err)
	}
	if err := syncFile(tmp); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("state: fsync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("state: close: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return fmt.Errorf("state: chmod: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		cleanup()
		return fmt.Errorf("state: rename: %w", err)
	}
	if s.opts.FullFsync {
		s.scheduleFullSync()
	}
	return nil
}

// scheduleFullSync coalesces the hardware-durable fsync pass into at
// most one per fsyncDebounce window: a burst of persist() calls pays for
// it once, not once per call. It never blocks persist(); the actual sync
// runs later on its own goroutine (fireFullSync), or synchronously from
// Close.
func (s *Store) scheduleFullSync() {
	s.fsMu.Lock()
	defer s.fsMu.Unlock()
	if s.fsDone {
		return
	}
	s.fsDirty = true
	if s.fsTimer == nil {
		s.fsTimer = time.AfterFunc(fsyncDebounce, s.fireFullSync)
	}
}

// fireFullSync is the debounce timer's callback: it performs the durable
// sync (fullSync) once, for whatever is on disk at that moment — always
// the latest table, since persist()'s write+rename is never itself
// debounced.
func (s *Store) fireFullSync() {
	s.fsMu.Lock()
	if !s.fsDirty {
		s.fsMu.Unlock()
		return
	}
	s.fsDirty = false
	s.fsTimer = nil
	s.fsMu.Unlock()
	s.fullSync()
}

// fullSync fsyncs the state file and its directory for hardware
// durability (F_FULLFSYNC on Darwin). Best-effort: errors are not
// actionable here (the file itself is already correct; this only affects
// how durable it is against a power loss).
func (s *Store) fullSync() {
	if s.onFullSync != nil {
		s.onFullSync()
	}
	if f, err := os.Open(s.path); err == nil {
		_ = fullSyncFile(f)
		_ = f.Close()
	}
	if dir, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = fullSyncFile(dir)
		_ = dir.Close()
	}
}

// Close flushes any pending debounced hardware-durable fsync
// (Options.FullFsync) and stops the debounce timer. A no-op when
// FullFsync is off or nothing is pending. Safe to call more than once
// and from any goroutine; the daemon calls it during shutdown so a
// full_fsync=true state file is durable even if the process is about to
// exit right after the last mutation.
func (s *Store) Close() error {
	s.fsMu.Lock()
	if s.fsDone {
		s.fsMu.Unlock()
		return nil
	}
	s.fsDone = true
	timer, dirty := s.fsTimer, s.fsDirty
	s.fsTimer, s.fsDirty = nil, false
	s.fsMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if dirty {
		s.fullSync()
	}
	return nil
}
