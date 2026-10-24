// Package fsaccess checks that the daemon may list a folder, without
// letting one stuck folder stall the caller.
//
// On macOS the first read of ~/Documents, ~/Desktop, ~/Downloads or iCloud
// Drive by a program that has no permission yet makes the system ask the
// user; the read blocks until they answer (or fails with EPERM once they
// refuse). Listing project folders must not wait for that.
package fsaccess

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Reasons a folder needs access.
const (
	// Denied: the system refused (EPERM or EACCES).
	Denied = "denied"
	// Slow: the read did not answer in time, which on macOS means the
	// permission prompt is waiting for the user.
	Slow = "slow"
)

// Timeout bounds one folder probe. A var so tests can shorten it.
var Timeout = 1500 * time.Millisecond

// Blocked is a folder the daemon cannot read yet.
type Blocked struct {
	// Path is the folder that needs access: the macOS-protected folder
	// (~/Documents) when the path lies inside one, else the path.
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Protected returns the macOS-protected folder under home that path is in
// (Documents, Desktop, Downloads, iCloud Drive), or "" when it is in none.
func Protected(home, path string) string {
	if home == "" {
		return ""
	}
	for _, rel := range []string{"Documents", "Desktop", "Downloads", filepath.Join("Library", "Mobile Documents")} {
		p := filepath.Join(home, rel)
		if path == p || strings.HasPrefix(path, p+string(filepath.Separator)) {
			return p
		}
	}
	return ""
}

// key is the folder to probe for path: its protected folder, else itself.
func key(home, path string) string {
	if p := Protected(home, path); p != "" {
		return p
	}
	return path
}

// Probe opens the folder for path (its protected folder, else path itself)
// and reads one entry, giving up after Timeout. It returns nil when it is
// readable, or missing (not an access problem).
func Probe(path string) *Blocked {
	home, _ := os.UserHomeDir()
	dir := key(home, path)
	select {
	case err := <-join(dir):
		if errors.Is(err, fs.ErrPermission) {
			return &Blocked{Path: dir, Reason: Denied}
		}
		return nil
	case <-time.After(Timeout):
		return &Blocked{Path: dir, Reason: Slow}
	}
}

var (
	mu      sync.Mutex
	waiting = map[string][]chan error{}
)

// join starts the read of dir, or joins the one already running: a
// blocked read keeps its goroutine until the system answers, and
// repeated requests must not pile more of them up.
func join(dir string) <-chan error {
	out := make(chan error, 1)
	mu.Lock()
	defer mu.Unlock()
	if _, running := waiting[dir]; running {
		waiting[dir] = append(waiting[dir], out)
		return out
	}
	waiting[dir] = []chan error{out}
	go func() {
		err := read(dir)
		mu.Lock()
		ws := waiting[dir]
		delete(waiting, dir)
		mu.Unlock()
		for _, w := range ws {
			w <- err
		}
	}()
	return out
}

func read(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// Set collects the folders found blocked during one request and probes
// each protected folder once.
type Set struct {
	home    string
	checked map[string]*Blocked
	order   []string
}

// NewSet starts a request's checks.
func NewSet() *Set {
	home, _ := os.UserHomeDir()
	return &Set{home: home, checked: map[string]*Blocked{}}
}

// OK reports whether path can be read. Later paths in a folder already
// found blocked answer false at once, so the request waits for it once.
func (s *Set) OK(path string) bool {
	k := key(s.home, path)
	b, seen := s.checked[k]
	if !seen {
		b = Probe(path)
		s.checked[k] = b
		if b != nil {
			s.order = append(s.order, k)
		}
	}
	return b == nil
}

// Blocked lists the blocked folders in the order they were found.
func (s *Set) Blocked() []Blocked {
	out := []Blocked{}
	for _, k := range s.order {
		// A folder inside another blocked one is the same problem.
		nested := false
		for _, o := range s.order {
			if o != k && strings.HasPrefix(k, o+string(filepath.Separator)) {
				nested = true
			}
		}
		if !nested {
			out = append(out, *s.checked[k])
		}
	}
	return out
}
