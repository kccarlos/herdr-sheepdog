package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ExeID identifies an executable file on disk.
type ExeID struct {
	// Path is the file with symlinks resolved (for Homebrew, the Cellar
	// directory of one version; for a herdr plugin, the plugin's folder).
	Path  string    `json:"path"`
	Dev   uint64    `json:"dev"`
	Ino   uint64    `json:"ino"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
}

// Identify resolves launch (the path the daemon was started from, for
// example ~/.local/bin/sheepdog) through its symlinks and stats the file.
func Identify(launch string) (ExeID, error) {
	p, err := filepath.EvalSymlinks(launch)
	if err != nil {
		return ExeID{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return ExeID{}, err
	}
	id := ExeID{Path: p, Size: st.Size(), MTime: st.ModTime()}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		id.Dev, id.Ino = uint64(sys.Dev), uint64(sys.Ino) //nolint:unconvert // Dev is int32 on darwin
	}
	return id, nil
}

// Same reports whether a and b are the same file in the same state.
func (a ExeID) Same(b ExeID) bool {
	return a.Path == b.Path && a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size && a.MTime.Equal(b.MTime)
}

// LaunchPath is the user-facing path of the running executable: argv0 made
// absolute (looked up on PATH when it has no slash), else exe. It is the
// path to re-exec and to watch, because an upgrade replaces what it points
// at (a new Cellar directory, a new plugin folder) while the old target
// may be deleted.
func LaunchPath(argv0, exe string, lookPath func(string) (string, error)) string {
	if strings.Contains(argv0, string(filepath.Separator)) {
		if abs, err := filepath.Abs(argv0); err == nil {
			return abs
		}
	} else if argv0 != "" && lookPath != nil {
		if p, err := lookPath(argv0); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
		}
	}
	return exe
}

// Watch notices that the executable on disk is no longer the one that
// started.
type Watch struct {
	Launch string
	start  ExeID
}

// NewWatch snapshots launch now (at startup).
func NewWatch(launch string) (*Watch, error) {
	id, err := Identify(launch)
	if err != nil {
		return nil, err
	}
	return &Watch{Launch: launch, start: id}, nil
}

// Changed reports the current file when it differs from the startup one.
// A path that cannot be resolved right now (an upgrade in progress) is not
// a change.
func (w *Watch) Changed() (ExeID, bool) {
	id, err := Identify(w.Launch)
	if err != nil || id.Same(w.start) {
		return ExeID{}, false
	}
	return id, true
}

// Record is <data_dir>/update-restart.json: the last restart the daemon
// made on its own, so the daemon it starts never restarts into the same
// executable again (no loop).
type Record struct {
	From string    `json:"from"`
	To   string    `json:"to"`
	At   time.Time `json:"at"`
}

// RecordWindow is how long a Record blocks a second restart into To.
const RecordWindow = 30 * time.Minute

func recordPath(dir string) string { return filepath.Join(dir, "update-restart.json") }

// WriteRecord saves r.
func WriteRecord(dir string, r Record) error { return writeJSON(recordPath(dir), r) }

// Attempted reports whether the daemon already restarted into to within
// RecordWindow before now.
func Attempted(dir, to string, now time.Time) bool {
	var r Record
	b, err := os.ReadFile(recordPath(dir))
	if err != nil || json.Unmarshal(b, &r) != nil {
		return false
	}
	return r.To == to && now.Sub(r.At) < RecordWindow
}

// ProbeVersion runs `path version` and returns its first line.
func ProbeVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line, nil
}

// VersionOf is the version word of a `sheepdog version` line ("sheepdog
// v1.2.3 (commit …)" gives "v1.2.3").
func VersionOf(line string) string {
	f := strings.Fields(line)
	if len(f) >= 2 && f[0] == "sheepdog" {
		return f[1]
	}
	return ""
}

// ErrBusy is what Trigger returns when it cannot start the restart right
// now (another restart is in progress): the watcher tries again at the
// next tick instead of giving up.
var ErrBusy = errors.New("another restart is in progress")

// Restarter restarts the daemon into a new executable once it is idle.
type Restarter struct {
	Watch   *Watch
	Tracker *Tracker
	// Auto is config update.auto_restart; with it off the watcher only
	// reports ("update installed, restart to finish").
	Auto bool
	// Running is this binary's `sheepdog version` line.
	Running string
	// Probe returns the new executable's version line (ProbeVersion).
	Probe func(ctx context.Context, path string) (string, error)
	// Idle returns "" when nobody is using the daemon, else why not
	// ("a voice client is connected").
	Idle func() string
	// Attempted reports whether a restart into the version was already
	// made (Attempted over the data dir).
	Attempted func(to string) bool
	// Trigger starts the restart (record, shut down, exec or exit); from
	// and to are the two `sheepdog version` lines. It returns only when it
	// failed; the watcher then stops trying.
	Trigger func(from, to string) error
	// Interval is how often to check (a minute).
	Interval time.Duration
	Logf     func(string, ...any)

	lastSeen ExeID
	probed   int
	line     string // the new executable's version line, once probed
}

func (r *Restarter) logf(f string, a ...any) {
	if r.Logf != nil {
		r.Logf(f, a...)
	}
}

// Tick runs one check; done is true when the watcher has nothing more to
// do (a restart was triggered and failed, or it is blocked for good).
func (r *Restarter) Tick(ctx context.Context) (done bool) {
	id, changed := r.Watch.Changed()
	if !changed {
		return false
	}
	if !id.Same(r.lastSeen) {
		r.lastSeen, r.probed, r.line = id, 0, ""
		r.Tracker.SetRestart(true, "", false)
	}
	line, err := r.line, error(nil)
	if line == "" {
		line, err = r.Probe(ctx, r.Watch.Launch)
	}
	to := VersionOf(line)
	switch {
	case err != nil || to == "":
		// Still being written, or broken: try again a few times, then
		// only show the notice.
		if r.probed++; r.probed >= 5 {
			r.logf("update: the new sheepdog does not report its version (%v); not restarting into it", err)
			r.Tracker.SetRestart(true, "", true)
			return true
		}
		return false
	case line == r.Running:
		r.Tracker.SetRestart(true, to, true)
		return true
	case r.Attempted != nil && r.Attempted(line):
		r.logf("update: already restarted into %s a moment ago; not again", to)
		r.Tracker.SetRestart(true, to, true)
		return true
	}
	r.line = line
	r.Tracker.SetRestart(true, to, false)
	if !r.Auto {
		return true
	}
	if why := r.Idle(); why != "" {
		return false
	}
	if err := r.Trigger(r.Running, line); errors.Is(err, ErrBusy) {
		return false
	} else if err != nil {
		r.logf("update: restart into %s failed: %v", to, err)
		r.Tracker.SetRestart(true, to, true)
		return true
	}
	return true
}

// Run ticks every Interval until ctx ends or Tick is done.
func (r *Restarter) Run(ctx context.Context) {
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if r.Tick(ctx) {
				return
			}
		}
	}
}
