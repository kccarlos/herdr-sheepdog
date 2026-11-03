package attention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// MutesFileName is the space mute store's file inside the data directory.
const MutesFileName = "space_mutes.json"

const mutesVersion = 1

// The durations the phone offers for a space mute. The API takes the
// resulting instant as "until" (RFC 3339), so a client of another kind can
// pick its own.
//
// These are product defaults, not settled decisions: see
// docs/dev/design/unread-pinned-mute.md "Open questions for KC" (Q4). They
// are the one place to change a duration.
const (
	// MuteForHour is "For 1 hour".
	MuteForHour = time.Hour
	// MuteUntilMorningHour is the local hour "Until tomorrow morning" ends.
	MuteUntilMorningHour = 8
)

// UntilTomorrowMorning is the next MuteUntilMorningHour o'clock strictly
// after now, in now's own location ("until tomorrow 08:00"): asked at 07:00
// it ends in an hour, asked at 09:00 it ends the next day.
func UntilTomorrowMorning(now time.Time) time.Time {
	y, m, d := now.Date()
	t := time.Date(y, m, d, MuteUntilMorningHour, 0, 0, 0, now.Location())
	if !t.After(now) {
		t = time.Date(y, m, d+1, MuteUntilMorningHour, 0, 0, 0, now.Location())
	}
	return t
}

// Mute is one muted space folder.
type Mute struct {
	// Path is the space's folder (GET /api/spaces path).
	Path string `json:"-"`
	// MutedAt is when the mute was set.
	MutedAt time.Time `json:"muted_at"`
	// Until is when it ends; nil means until the user unmutes.
	Until *time.Time `json:"until,omitempty"`
	// Via is who asked: "phone" or "panel".
	Via string `json:"via,omitempty"`
}

type mutesFile struct {
	Version int              `json:"version"`
	Spaces  map[string]*Mute `json:"spaces"`
}

// MutesOptions configures Mutes. The zero value is usable.
type MutesOptions struct {
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
	// Exists says whether a folder still exists; nil uses os.Stat. An entry
	// whose folder is gone is removed at start.
	Exists func(path string) bool
}

// Mutes is the set of muted space folders, kept in
// <data_dir>/space_mutes.json (mode 0600, written atomically). The key is the
// space's folder, not its herdr workspace id: ids are positional and come
// back after a close or a restart, while the folder is stable on both
// backends (the same reason wrap-up commands are stored by folder).
//
// An entry with an end time stops counting at that time, whether or not
// Run has removed it yet.
type Mutes struct {
	path string
	opts MutesOptions

	mu      sync.Mutex
	spaces  map[string]*Mute
	wake    chan struct{}
	expired func(Mute)
}

// OpenMutes loads the store at path ("" keeps it in memory). A missing file
// starts empty; an unreadable one is moved aside to path+".corrupt". Entries
// that ended while the daemon was down, or whose folder no longer exists, are
// dropped (and the file saved without them).
func OpenMutes(path string, opts MutesOptions) (*Mutes, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Exists == nil {
		opts.Exists = func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }
	}
	m := &Mutes{path: path, opts: opts, spaces: map[string]*Mute{}, wake: make(chan struct{}, 1)}
	if path == "" {
		return m, nil
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return m, nil
	case err != nil:
		return nil, fmt.Errorf("attention: read %s: %w", path, err)
	}
	var f mutesFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != mutesVersion {
		_ = os.Rename(path, path+".corrupt")
		return m, nil
	}
	now := opts.Now()
	dropped := false
	for p, e := range f.Spaces {
		if e == nil {
			continue
		}
		e.Path = p
		if e.ended(now) || !opts.Exists(p) {
			dropped = true
			continue
		}
		m.spaces[p] = e
	}
	if dropped {
		if err := m.saveLocked(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (e *Mute) ended(now time.Time) bool { return e.Until != nil && !now.Before(*e.Until) }

// OnExpire sets the function Expire calls with each mute that ended. Set it
// before Run.
func (m *Mutes) OnExpire(f func(Mute)) {
	m.mu.Lock()
	m.expired = f
	m.mu.Unlock()
}

// Empty reports that no space is muted right now. It is the cheap test the
// hot paths make first, so that with nothing muted they do no more work than
// before mutes existed.
func (m *Mutes) Empty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.opts.Now()
	for _, e := range m.spaces {
		if !e.ended(now) {
			return false
		}
	}
	return true
}

// Muted reports whether the space folder is muted now.
func (m *Mutes) Muted(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.spaces[path]
	return ok && !e.ended(m.opts.Now())
}

// Get returns the active mute of a folder.
func (m *Mutes) Get(path string) (Mute, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.spaces[path]
	if !ok || e.ended(m.opts.Now()) {
		return Mute{}, false
	}
	return *e, true
}

// List returns the active mutes, sorted by path.
func (m *Mutes) List() []Mute {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.opts.Now()
	out := make([]Mute, 0, len(m.spaces))
	for _, e := range m.spaces {
		if !e.ended(now) {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ErrMuteInPast is a mute that would already have ended.
var ErrMuteInPast = errors.New("until is in the past")

// Set mutes a space folder until the given time (nil: until unmuted),
// replacing an earlier mute of the same folder. It returns the entry.
func (m *Mutes) Set(path string, until *time.Time, via string) (Mute, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.opts.Now()
	if until != nil && !until.After(now) {
		return Mute{}, ErrMuteInPast
	}
	e := &Mute{Path: path, MutedAt: now.UTC(), Via: via}
	if until != nil {
		u := until.UTC()
		e.Until = &u
	}
	prev, had := m.spaces[path]
	m.spaces[path] = e
	if err := m.saveLocked(); err != nil {
		if had {
			m.spaces[path] = prev
		} else {
			delete(m.spaces, path)
		}
		return Mute{}, err
	}
	m.pokeLocked()
	return *e, nil
}

// Clear unmutes a space folder. It reports whether it was muted.
func (m *Mutes) Clear(path string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, had := m.spaces[path]
	if !had {
		return false, nil
	}
	delete(m.spaces, path)
	if err := m.saveLocked(); err != nil {
		m.spaces[path] = prev
		return false, err
	}
	m.pokeLocked()
	return !prev.ended(m.opts.Now()), nil
}

func (m *Mutes) pokeLocked() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Expire removes the mutes that ended and calls the OnExpire function for
// each. Run calls it at the right moments; a test calls it directly.
func (m *Mutes) Expire() {
	m.mu.Lock()
	now := m.opts.Now()
	var gone []Mute
	for p, e := range m.spaces {
		if e.ended(now) {
			gone = append(gone, *e)
			delete(m.spaces, p)
		}
	}
	if len(gone) > 0 {
		_ = m.saveLocked()
	}
	f := m.expired
	m.mu.Unlock()
	sort.Slice(gone, func(i, j int) bool { return gone[i].Path < gone[j].Path })
	if f != nil {
		for _, e := range gone {
			f(e)
		}
	}
}

// next is when the first mute ends; ok is false when none ends.
func (m *Mutes) next() (t time.Time, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.spaces {
		if e.Until != nil && (!ok || e.Until.Before(t)) {
			t, ok = *e.Until, true
		}
	}
	return t, ok
}

// Run removes each mute when it ends until ctx ends, telling OnExpire.
func (m *Mutes) Run(ctx context.Context) {
	for {
		var timer <-chan time.Time
		var tm *time.Timer
		if t, ok := m.next(); ok {
			tm = time.NewTimer(max(t.Sub(m.opts.Now()), 0) + time.Millisecond)
			timer = tm.C
		}
		select {
		case <-ctx.Done():
			if tm != nil {
				tm.Stop()
			}
			return
		case <-m.wake:
		case <-timer:
			m.Expire()
		}
		if tm != nil {
			tm.Stop()
		}
	}
}

// saveLocked writes the file atomically: a temp file beside it, fsync,
// rename, mode 0600. The caller holds m.mu.
func (m *Mutes) saveLocked() error {
	if m.path == "" {
		return nil
	}
	f := mutesFile{Version: mutesVersion, Spaces: m.spaces}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("attention: encode mutes: %w", err)
	}
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("attention: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".space_mutes-*.json.tmp")
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
	if err := os.Rename(name, m.path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("attention: rename: %w", err)
	}
	return nil
}
