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
}

// Store is the state table with atomic JSON persistence: every mutation
// writes a temp file next to path, fsyncs it and renames it over path.
// Store is safe for concurrent use.
type Store struct {
	mu    sync.Mutex
	path  string
	table Table
	opts  Options
	now   func() time.Time
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
		if _, ok := s.table.Agents[t.PaneID]; !ok {
			return Agent{}, false, nil
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
		return *a, false, nil
	}
	return *a, true, s.persist()
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

// Get returns the row for paneID.
func (s *Store) Get(paneID string) (Agent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.table.Agents[paneID]
	if !ok {
		return Agent{}, false
	}
	return *a, true
}

// ByAlias returns the row whose alias matches.
func (s *Store) ByAlias(alias string) (Agent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.table.Agents {
		if a.Alias == alias {
			return *a, true
		}
	}
	return Agent{}, false
}

// List returns every row sorted by pane id.
func (s *Store) List() []Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Agent, 0, len(s.table.Agents))
	for _, a := range s.table.Agents {
		out = append(out, *a)
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
	return *s.table.PendingAction, true
}

// SetPending replaces the pending action (one at a time) and persists.
func (s *Store) SetPending(p PendingAction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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
// fsync, rename. Caller holds s.mu.
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
	if err := tmp.Sync(); err != nil {
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
	return nil
}
