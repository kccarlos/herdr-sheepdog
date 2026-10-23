package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSemverAndNewer(t *testing.T) {
	for v, ok := range map[string]bool{"v1.2.3": true, "1.2.3": true, "v1.2.3-rc1": true, "dev": false, "v1.2": false, "abc1234": false, "v1.x.3": false} {
		if _, got := Semver(v); got != ok {
			t.Errorf("Semver(%q) ok=%v, want %v", v, got, ok)
		}
	}
	a, _ := Semver("v0.10.0")
	b, _ := Semver("v0.9.9")
	if !Newer(a, b) || Newer(b, a) || Newer(a, a) {
		t.Error("Newer compares numerically")
	}
	for v, want := range map[string]bool{"v1.2.3": true, "dev": false, "v0.0.0-20260101120000-abcdef": false, "v1.2.3-dirty": false, "1.2.3": false} {
		if IsRelease(v) != want {
			t.Errorf("IsRelease(%q) = %v", v, !want)
		}
	}
}

func TestPickIgnoresIOSDraftsAndPrereleases(t *testing.T) {
	r, ok := Pick([]Release{
		{TagName: "ios-v9.0.0"},
		{TagName: "v0.4.0", Draft: true},
		{TagName: "v0.5.0-rc1", Prerelease: true},
		{TagName: "v0.3.1"},
		{TagName: "v0.3.10"},
		{TagName: "v0.3.2"},
		{TagName: "nightly"},
	})
	if !ok || r.TagName != "v0.3.10" {
		t.Errorf("picked %+v %v", r, ok)
	}
	if _, ok := Pick([]Release{{TagName: "ios-v1.0.0"}}); ok {
		t.Error("only an ios release: nothing to pick")
	}
}

func TestLatestOverHTTP(t *testing.T) {
	var status = 200
	var body = `[{"tag_name":"ios-v5.0.0","html_url":"x"},{"tag_name":"v0.2.0","html_url":"https://example/v0.2.0"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/"+Repo+"/releases" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	r, err := Latest(context.Background(), srv.Client(), srv.URL)
	if err != nil || r.TagName != "v0.2.0" || r.HTMLURL != "https://example/v0.2.0" {
		t.Fatalf("got %+v %v", r, err)
	}
	status, body = 404, `{}`
	if _, err := Latest(context.Background(), srv.Client(), srv.URL); !errors.Is(err, ErrNoRelease) {
		t.Errorf("404: %v", err)
	}
	status, body = 403, `{}`
	if _, err := Latest(context.Background(), srv.Client(), srv.URL); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("403: %v", err)
	}
}

func TestComputeSkipsDevAndPseudoVersions(t *testing.T) {
	c := Cache{Tag: "v1.0.0", URL: "u"}
	if s := Compute("v0.9.0", c, "brew upgrade sheepdog"); !s.Available || s.Latest != "v1.0.0" || s.How != "brew upgrade sheepdog" {
		t.Errorf("older: %+v", s)
	}
	for _, cur := range []string{"v1.0.0", "v1.1.0", "dev", "v0.0.0-20260101-abc", "abc1234-dirty"} {
		if Compute(cur, c, "").Available {
			t.Errorf("%q must not report an update", cur)
		}
	}
	if s := Compute("v0.9.0", Cache{}, ""); s.Available || s.Latest != "" {
		t.Errorf("no cache: %+v", s)
	}
}

func TestCacheAndNoticeOncePerDay(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadCache(dir); ok {
		t.Fatal("empty dir has no cache")
	}
	now := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	if err := WriteCache(dir, Cache{CheckedAt: now, Tag: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if c, ok := ReadCache(dir); !ok || c.Tag != "v1.0.0" || !c.CheckedAt.Equal(now) {
		t.Errorf("cache %+v", c)
	}
	if !NoticeDue(dir, now) {
		t.Error("first notice is due")
	}
	NoticeShown(dir, now)
	if NoticeDue(dir, now.Add(23*time.Hour)) {
		t.Error("not due within a day")
	}
	if !NoticeDue(dir, now.Add(25*time.Hour)) {
		t.Error("due after a day")
	}
}

func TestRunChecksOnceThenUsesFreshCache(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir, "v0.1.0", "brew upgrade sheepdog", true)
	var calls atomic.Int32
	fetch := func(context.Context) (Release, error) { //nolint:unparam // a stub that always succeeds
		calls.Add(1)
		return Release{TagName: "v0.2.0", HTMLURL: "u"}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Run(ctx, tr, time.Millisecond, fetch, time.Now, nil); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for !tr.Status().Available && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if !tr.Status().Available || calls.Load() != 1 {
		t.Fatalf("status %+v calls %d", tr.Status(), calls.Load())
	}
	// A second daemon start within a day reads the cache and asks nothing.
	tr2 := NewTracker(dir, "v0.1.0", "", true)
	if !tr2.Status().Available {
		t.Error("cache not loaded at start")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	Run(ctx2, tr2, time.Millisecond, fetch, time.Now, nil)
	if calls.Load() != 1 {
		t.Errorf("fresh cache must not refetch: %d calls", calls.Load())
	}
}

func TestRunFailureKeepsOldAnswer(t *testing.T) {
	dir := t.TempDir()
	tr := NewTracker(dir, "v0.1.0", "", true)
	var logged atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	Run(ctx, tr, time.Millisecond, func(context.Context) (Release, error) { return Release{}, errors.New("offline") }, time.Now,
		func(string, ...any) { logged.Add(1) })
	if logged.Load() == 0 || tr.Status().Available {
		t.Errorf("logged %d status %+v", logged.Load(), tr.Status())
	}
	if _, ok := ReadCache(dir); ok {
		t.Error("a failed check must not write the cache")
	}
}

// writeExe writes a fake executable.
func writeExe(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestWatchDetectsSymlinkRetargetAndInPlaceReplace(t *testing.T) {
	dir := t.TempDir()
	v1 := filepath.Join(dir, "Cellar", "1", "sheepdog")
	v2 := filepath.Join(dir, "Cellar", "2", "sheepdog")
	writeExe(t, v1, "one")
	link := filepath.Join(dir, "bin", "sheepdog")
	_ = os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(v1, link); err != nil {
		t.Fatal(err)
	}
	w, err := NewWatch(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed := w.Changed(); changed {
		t.Fatal("unchanged at start")
	}
	// brew upgrade: new keg, link retargeted, old keg removed.
	writeExe(t, v2, "two!")
	_ = os.Remove(link)
	if _, changed := w.Changed(); changed {
		t.Fatal("a link that does not resolve right now is not a change")
	}
	if err := os.Symlink(v2, link); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Dir(v1))
	if id, changed := w.Changed(); !changed || id.Path == "" {
		t.Fatal("retargeted link not noticed")
	}

	// In-place replacement of a plain file (new inode, new mtime).
	plain := filepath.Join(dir, "plain", "sheepdog")
	writeExe(t, plain, "a")
	w2, _ := NewWatch(plain)
	tmp := plain + ".new"
	writeExe(t, tmp, "bb")
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(tmp, future, future)
	if err := os.Rename(tmp, plain); err != nil {
		t.Fatal(err)
	}
	if _, changed := w2.Changed(); !changed {
		t.Fatal("replaced file not noticed")
	}
}

func TestLaunchPath(t *testing.T) {
	look := func(string) (string, error) { return "/opt/homebrew/bin/sheepdog", nil }
	if got := LaunchPath("sheepdog", "/real/exe", look); got != "/opt/homebrew/bin/sheepdog" {
		t.Errorf("PATH lookup: %q", got)
	}
	if got := LaunchPath("/home/u/.local/bin/sheepdog", "/real/exe", look); got != "/home/u/.local/bin/sheepdog" {
		t.Errorf("absolute: %q", got)
	}
	if got := LaunchPath("sheepdog", "/real/exe", func(string) (string, error) { return "", errors.New("no") }); got != "/real/exe" {
		t.Errorf("fallback: %q", got)
	}
}

func TestVersionOfAndRecord(t *testing.T) {
	if VersionOf("sheepdog v1.2.3 (commit abc, built now)") != "v1.2.3" || VersionOf("garbage") != "" {
		t.Error("VersionOf")
	}
	dir := t.TempDir()
	now := time.Now()
	if Attempted(dir, "x", now) {
		t.Error("no record yet")
	}
	_ = WriteRecord(dir, Record{From: "a", To: "x", At: now})
	if !Attempted(dir, "x", now.Add(time.Minute)) || Attempted(dir, "y", now) || Attempted(dir, "x", now.Add(time.Hour)) {
		t.Error("record window")
	}
}

// restarterFixture is a Restarter over a temp executable that "gets
// upgraded" by calling upgrade().
type restarterFixture struct {
	r        *Restarter
	upgrade  func()
	idle     *string
	triggers *[]string
	probeLn  *string
}

func newRestarterFixture(t *testing.T, auto bool) restarterFixture {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "sheepdog")
	writeExe(t, exe, "old")
	w, err := NewWatch(exe)
	if err != nil {
		t.Fatal(err)
	}
	idle, probe := "", "sheepdog v0.2.0 (commit b, built d)"
	var triggers []string
	f := restarterFixture{idle: &idle, triggers: &triggers, probeLn: &probe}
	f.r = &Restarter{
		Watch: w, Tracker: NewTracker(t.TempDir(), "v0.1.0", "", auto), Auto: auto,
		Running:   "sheepdog v0.1.0 (commit a, built d)",
		Probe:     func(context.Context, string) (string, error) { return probe, nil },
		Idle:      func() string { return idle },
		Attempted: func(string) bool { return false },
		Trigger:   func(from, to string) error { triggers = append(triggers, from+" -> "+to); return nil },
		Interval:  time.Minute,
	}
	f.upgrade = func() {
		tmp := exe + ".new"
		writeExe(t, tmp, "new, longer")
		_ = os.Chtimes(tmp, time.Now().Add(time.Hour), time.Now().Add(time.Hour))
		_ = os.Rename(tmp, exe)
	}
	return f
}

func TestRestarterWaitsForIdle(t *testing.T) {
	f := newRestarterFixture(t, true)
	ctx := context.Background()
	if f.r.Tick(ctx) || len(*f.triggers) != 0 || f.r.Tracker.Status().RestartPending {
		t.Fatal("nothing changed yet")
	}
	f.upgrade()
	for _, busy := range []string{"a voice client is connected", "the orchestrator is working", "an approval is waiting", "the setup demo is running"} {
		*f.idle = busy
		if f.r.Tick(ctx) || len(*f.triggers) != 0 {
			t.Fatalf("busy (%s) must not restart", busy)
		}
		if s := f.r.Tracker.Status(); !s.RestartPending || s.RestartTo != "v0.2.0" {
			t.Fatalf("pages must say restart pending: %+v", s)
		}
	}
	*f.idle = ""
	if !f.r.Tick(ctx) || len(*f.triggers) != 1 || !strings.Contains((*f.triggers)[0], "v0.1.0") {
		t.Fatalf("idle: triggers %v", *f.triggers)
	}
}

func TestRestarterAutoOffOnlyShowsNotice(t *testing.T) {
	f := newRestarterFixture(t, false)
	f.upgrade()
	f.r.Tick(context.Background())
	if len(*f.triggers) != 0 || !f.r.Tracker.Status().RestartPending || f.r.Tracker.Status().AutoRestart {
		t.Errorf("%v %+v", *f.triggers, f.r.Tracker.Status())
	}
}

func TestRestarterNoLoop(t *testing.T) {
	ctx := context.Background()
	// The new binary reports the same version: notice only.
	f := newRestarterFixture(t, true)
	*f.probeLn = f.r.Running
	f.upgrade()
	if !f.r.Tick(ctx) || len(*f.triggers) != 0 || !f.r.Tracker.Status().RestartPending {
		t.Errorf("same version: %v %+v", *f.triggers, f.r.Tracker.Status())
	}
	// Already restarted into it a moment ago.
	f = newRestarterFixture(t, true)
	f.r.Attempted = func(string) bool { return true }
	f.upgrade()
	if !f.r.Tick(ctx) || len(*f.triggers) != 0 {
		t.Errorf("attempted: %v", *f.triggers)
	}
	// The restart itself fails: stop trying.
	f = newRestarterFixture(t, true)
	f.r.Trigger = func(string, string) error { return errors.New("exec failed") }
	f.upgrade()
	if !f.r.Tick(ctx) || f.r.Tracker.Status().AutoRestart {
		t.Errorf("failed trigger must stop and show the notice: %+v", f.r.Tracker.Status())
	}
	// A new binary that cannot report a version is tried a few times only.
	f = newRestarterFixture(t, true)
	f.r.Probe = func(context.Context, string) (string, error) { return "", errors.New("bad") }
	f.upgrade()
	var done bool
	for i := 0; i < 6 && !done; i++ {
		done = f.r.Tick(ctx)
	}
	if !done || len(*f.triggers) != 0 {
		t.Errorf("broken binary: done=%v %v", done, *f.triggers)
	}
}
