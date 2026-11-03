package attention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// FileName is the store's file inside the data directory.
const FileName = "agent_marks.json"

const (
	fileVersion = 1
	// changeBuffer is how many pane ids MarkChanges holds for a receiver
	// that is busy. Past it a signal is dropped; the receiver reads
	// AllMarks again, so a dropped id costs nothing.
	changeBuffer = 64
)

var _ herdr.Marker = (*Local)(nil)

// Source is where Local learns which panes and agents exist. The old herdr
// client and every herdr.Backend are one.
type Source interface {
	Snapshot(ctx context.Context) (herdr.Snapshot, error)
}

// Options configures Local. The zero value is usable.
type Options struct {
	// Warn receives a note when the store file was unreadable and was moved
	// aside. Nil discards it.
	Warn io.Writer
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// record is what the store keeps of one terminal.
type record struct {
	PaneID string     `json:"pane_id"`
	Turn   uint64     `json:"turn"`
	Seen   *uint64    `json:"seen,omitempty"`
	SeenAt *time.Time `json:"seen_at,omitempty"`
	Pinned bool       `json:"pinned,omitempty"`
}

func (r *record) empty() bool { return r.Turn == 0 && r.Seen == nil && !r.Pinned }

type storeFile struct {
	Version   int                `json:"version"`
	Terminals map[string]*record `json:"terminals"`
}

// paneView is what the last snapshot showed of a terminal.
type paneView struct {
	paneID string
	word   herdr.State // the agent's state; unknown for a shell
}

// Local keeps the marks of a backend that has none of its own: the old
// herdr (which has no labels) and the mock. It is a herdr.Marker over a
// file in the data directory, keyed by terminal id.
//
// The turn is the old herdr's completion_seq, which is null while an agent
// works, so Local keeps the highest it has seen. Refresh reads the fleet; it
// must run after every state change (Run does, and Poke asks for one).
// herdr's own "seen" cannot be written without focusing the pane in the
// user's window, which sheepdog never does: a pane looked at in a TUI client
// shows as done turning idle, and Local follows that.
type Local struct {
	path string
	src  Source
	opts Options

	changes chan string
	poke    chan struct{}

	mu     sync.Mutex
	writes int                 // saves of the store so far
	recs   map[string]*record  // by terminal key
	panes  map[string]paneView // by terminal key, from the last snapshot
	byPane map[string]string   // pane id to terminal key
}

// NewLocal opens the store at path ("" keeps it in memory). A missing file
// starts empty; an unreadable one is moved aside to path+".corrupt" and
// the store starts empty. src may be nil (the mock has no herdr to ask).
func NewLocal(path string, src Source, opts Options) (*Local, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	l := &Local{
		path:    path,
		src:     src,
		opts:    opts,
		changes: make(chan string, changeBuffer),
		poke:    make(chan struct{}, 1),
		recs:    map[string]*record{},
		panes:   map[string]paneView{},
		byPane:  map[string]string{},
	}
	if err := l.load(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Local) load() error {
	if l.path == "" {
		return nil
	}
	data, err := os.ReadFile(l.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("attention: read %s: %w", l.path, err)
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != fileVersion {
		if err == nil {
			err = fmt.Errorf("version %d, want %d", f.Version, fileVersion)
		}
		aside := l.path + ".corrupt"
		_ = os.Rename(l.path, aside)
		if l.opts.Warn != nil {
			_, _ = fmt.Fprintf(l.opts.Warn, "attention: %s is unreadable (%v); moved to %s and starting empty\n", l.path, err, aside)
		}
		return nil
	}
	for key, r := range f.Terminals {
		if r != nil {
			l.recs[key] = r
		}
	}
	return nil
}

// persistLocked writes the store atomically: a temp file in the same
// directory, fsync, rename, mode 0600. The caller holds l.mu.
func (l *Local) persistLocked() error {
	l.writes++
	if l.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(storeFile{Version: fileVersion, Terminals: l.recs}, "", "  ")
	if err != nil {
		return fmt.Errorf("attention: encode: %w", err)
	}
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("attention: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".agent_marks-*.json.tmp")
	if err != nil {
		return fmt.Errorf("attention: temp file: %w", err)
	}
	name := tmp.Name()
	fail := func(step string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("attention: %s: %w", step, err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail("chmod", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return fail("write", err)
	}
	if err := tmp.Sync(); err != nil {
		return fail("fsync", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("attention: close: %w", err)
	}
	if err := os.Rename(name, l.path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("attention: rename: %w", err)
	}
	return nil
}

// terminalKey is the store key of a terminal: its id, which is not reused
// after it closes (a pane id is). A pane with no terminal id, like a mock
// pane, is keyed by its pane id.
func terminalKey(terminalID, paneID string) string {
	if terminalID != "" {
		return terminalID
	}
	return "pane:" + paneID
}

// Writes is how many times the store has been saved. A mark that is not
// raised or lowered saves nothing, which a test can tell by this count.
func (l *Local) Writes() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.writes
}

// Poke asks Run for a refresh soon. It never blocks.
func (l *Local) Poke() {
	select {
	case l.poke <- struct{}{}:
	default:
	}
}

// Run refreshes the store at every Poke and every interval until ctx ends.
// A refresh that fails (herdr is down) is tried again at the next one; the
// Watcher reports that herdr is unavailable.
func (l *Local) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		_ = l.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-l.poke:
		case <-ticker.C:
		}
	}
}

// Refresh reads the fleet from the source and folds it in. It does nothing
// without a source.
func (l *Local) Refresh(ctx context.Context) error {
	if l.src == nil {
		return nil
	}
	snap, err := l.src.Snapshot(ctx)
	if err != nil {
		return err
	}
	return l.Observe(snap)
}

// Observe folds one complete snapshot in: it follows each terminal's turn,
// notices a pane looked at in a TUI client, forgets terminals that are gone
// and tells MarkChanges about what changed.
func (l *Local) Observe(snap herdr.Snapshot) error {
	l.mu.Lock()
	changed, err := l.observeLocked(snap)
	l.mu.Unlock()
	for _, paneID := range changed {
		select {
		case l.changes <- paneID:
		default:
		}
	}
	return err
}
