// Package update tells the user a newer sheepdog exists and notices when
// an upgrade replaced the running daemon's executable (docs/dev/update.md).
//
// Nothing here changes what is installed. The daemon asks GitHub for the
// latest v* release once a day (Run), caches the answer in the data
// directory (<data_dir>/update-check.json), and reports it as a Status in
// /api/state. A second loop (AutoRestart) restarts the daemon into a new
// executable once it is idle.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Repo is the GitHub repository releases are published in.
const Repo = "kccarlos/herdr-sheepdog"

// Every is how often the daemon checks.
const Every = 24 * time.Hour

// Release is the part of GitHub's release JSON the check reads.
type Release struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// ErrNoRelease: the repository is private or has no published v* release.
var ErrNoRelease = errors.New("no published release")

// Semver parses "v1.2.3" (a suffix after - or + is ignored); ok is false
// for anything else ("dev", a commit, a pseudo version without three
// numbers).
func Semver(v string) (out [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// IsRelease reports whether v is a released version: a plain vX.Y.Z (a
// pseudo version such as v0.0.0-20260101-abc, or "dev", is not).
func IsRelease(v string) bool {
	if !strings.HasPrefix(v, "v") || strings.ContainsAny(v, "-+") {
		return false
	}
	_, ok := Semver(v)
	return ok
}

// Newer reports whether version a is newer than b.
func Newer(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// Pick is the newest release among rs that is a daemon release: a plain
// vX.Y.Z tag, not a draft or prerelease. The iPhone app's ios-v* tags
// and anything else are ignored.
func Pick(rs []Release) (Release, bool) {
	var best Release
	var bv [3]int
	found := false
	for _, r := range rs {
		if r.Draft || r.Prerelease || !IsRelease(r.TagName) {
			continue
		}
		v, _ := Semver(r.TagName)
		if !found || Newer(v, bv) {
			best, bv, found = r, v, true
		}
	}
	return best, found
}

// Latest asks the GitHub API at api for the newest daemon release. The
// request carries no identifier beyond what any HTTP request does.
func Latest(ctx context.Context, client *http.Client, api string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+Repo+"/releases?per_page=30", nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Release{}, ErrNoRelease
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return Release{}, fmt.Errorf("GitHub refused the request (%s; its rate limit for anonymous requests is 60 an hour)", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return Release{}, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var rs []Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rs); err != nil {
		return Release{}, err
	}
	r, ok := Pick(rs)
	if !ok {
		return Release{}, ErrNoRelease
	}
	return r, nil
}

// Cache is <data_dir>/update-check.json: the last answer.
type Cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Tag       string    `json:"tag,omitempty"`
	URL       string    `json:"url,omitempty"`
}

func cachePath(dir string) string  { return filepath.Join(dir, "update-check.json") }
func noticePath(dir string) string { return filepath.Join(dir, "update-notice.json") }

// ReadCache reads the cache; ok is false when there is none.
func ReadCache(dir string) (Cache, bool) {
	var c Cache
	b, err := os.ReadFile(cachePath(dir))
	if err != nil || json.Unmarshal(b, &c) != nil {
		return Cache{}, false
	}
	return c, true
}

// WriteCache saves c.
func WriteCache(dir string, c Cache) error { return writeJSON(cachePath(dir), c) }

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Status is what /api/state, `sheepdog status` and the pages show.
type Status struct {
	// Current is the running version; Latest the newest release known
	// ("" before the first check). Available: Latest is newer than
	// Current (never for a development build).
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
	// How is the command that upgrades for the detected install method.
	How string `json:"how,omitempty"`
	// URL is the release page.
	URL string `json:"url,omitempty"`
	// RestartPending: the executable on disk is not the one running;
	// RestartTo is its version when known. Set by AutoRestart's watcher.
	RestartPending bool   `json:"restart_pending,omitempty"`
	RestartTo      string `json:"restart_to,omitempty"`
	// AutoRestart: the daemon restarts itself when idle
	// (config update.auto_restart).
	AutoRestart bool `json:"auto_restart,omitempty"`
}

// Compute is the Status for the running version current and cache c.
func Compute(current string, c Cache, how string) Status {
	s := Status{Current: current, How: how}
	if c.Tag == "" {
		return s
	}
	s.Latest, s.URL = c.Tag, c.URL
	if cv, ok := Semver(current); ok && IsRelease(current) {
		if lv, ok := Semver(c.Tag); ok && Newer(lv, cv) {
			s.Available = true
		}
	}
	return s
}

// Tracker holds the daemon's Status, safe for concurrent use.
type Tracker struct {
	dir, current, how string
	auto              bool

	mu             sync.Mutex
	cache          Cache
	restart        bool
	restartTo      string
	restartBlocked bool
}

// NewTracker loads the cache from dir.
func NewTracker(dir, current, how string, autoRestart bool) *Tracker {
	t := &Tracker{dir: dir, current: current, how: how, auto: autoRestart}
	t.cache, _ = ReadCache(dir)
	return t
}

// Status is the current Status.
func (t *Tracker) Status() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := Compute(t.current, t.cache, t.how)
	s.RestartPending, s.RestartTo, s.AutoRestart = t.restart, t.restartTo, t.auto && !t.restartBlocked
	return s
}

// SetCache records a fresh answer.
func (t *Tracker) SetCache(c Cache) {
	t.mu.Lock()
	t.cache = c
	t.mu.Unlock()
}

// SetRestart records that a new executable is installed (to: its version,
// "" when unknown); blocked: the daemon will not restart itself into it.
func (t *Tracker) SetRestart(pending bool, to string, blocked bool) {
	t.mu.Lock()
	t.restart, t.restartTo, t.restartBlocked = pending, to, blocked
	t.mu.Unlock()
}

// Run checks for a release after delay, then every Every until ctx ends.
// A cache younger than Every is used as it is, so a daemon that restarts
// often does not ask GitHub each time. fetch failures are reported to
// logf and retried at the next tick; the notice simply stays as it was.
func Run(ctx context.Context, t *Tracker, delay time.Duration, fetch func(context.Context) (Release, error), now func() time.Time, logf func(string, ...any)) {
	wait := delay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = Every
		if c, ok := ReadCache(t.dir); ok && now().Sub(c.CheckedAt) < Every && now().After(c.CheckedAt) {
			t.SetCache(c)
			wait = Every - now().Sub(c.CheckedAt)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		r, err := fetch(cctx)
		cancel()
		c := Cache{CheckedAt: now().UTC()}
		switch {
		case errors.Is(err, ErrNoRelease):
		case err != nil:
			if logf != nil {
				logf("update check failed: %v", err)
			}
			continue
		default:
			c.Tag, c.URL = r.TagName, r.HTMLURL
		}
		_ = WriteCache(t.dir, c)
		t.SetCache(c)
	}
}

// NoticeDue reports whether a CLI notice may be printed now: none was in
// the last 24 hours.
func NoticeDue(dir string, now time.Time) bool {
	var v struct {
		At time.Time `json:"at"`
	}
	b, err := os.ReadFile(noticePath(dir))
	if err != nil || json.Unmarshal(b, &v) != nil {
		return true
	}
	return now.Sub(v.At) >= 24*time.Hour || now.Before(v.At)
}

// NoticeShown records that a notice was printed at now.
func NoticeShown(dir string, now time.Time) {
	_ = writeJSON(noticePath(dir), map[string]time.Time{"at": now.UTC()})
}
