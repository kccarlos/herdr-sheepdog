package attention

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// clock is a settable test clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)}
}

func openMutes(t *testing.T, c *clock) (*Mutes, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), MutesFileName)
	m, err := OpenMutes(path, MutesOptions{Now: c.now, Exists: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	return m, path
}

func at(t time.Time) *time.Time { return &t }

func TestUntilTomorrowMorning(t *testing.T) {
	loc := time.FixedZone("local", -8*3600)
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"evening is tomorrow 08:00", time.Date(2026, 11, 3, 22, 30, 0, 0, loc), time.Date(2026, 11, 4, 8, 0, 0, 0, loc)},
		{"after 08:00 is tomorrow", time.Date(2026, 11, 3, 9, 0, 0, 0, loc), time.Date(2026, 11, 4, 8, 0, 0, 0, loc)},
		{"exactly 08:00 is tomorrow", time.Date(2026, 11, 3, 8, 0, 0, 0, loc), time.Date(2026, 11, 4, 8, 0, 0, 0, loc)},
		{"small hours is today 08:00", time.Date(2026, 11, 3, 1, 15, 0, 0, loc), time.Date(2026, 11, 3, 8, 0, 0, 0, loc)},
		{"month end rolls over", time.Date(2026, 11, 30, 23, 0, 0, 0, loc), time.Date(2026, 12, 1, 8, 0, 0, 0, loc)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := UntilTomorrowMorning(tc.now); !got.Equal(tc.want) {
				t.Errorf("UntilTomorrowMorning(%v) = %v, want %v", tc.now, got, tc.want)
			}
		})
	}
	if MuteForHour != time.Hour {
		t.Errorf("MuteForHour = %v, want 1h", MuteForHour)
	}
}

func TestMutesSetClearAndExpiry(t *testing.T) {
	c := newClock()
	m, _ := openMutes(t, c)
	if !m.Empty() || m.Muted("/code/auth") {
		t.Fatal("a new store is not empty")
	}

	if _, err := m.Set("/code/auth", at(c.now().Add(-time.Minute)), "phone"); !errors.Is(err, ErrMuteInPast) {
		t.Errorf("Set in the past = %v, want ErrMuteInPast", err)
	}
	if _, err := m.Set("/code/auth", at(c.now()), "phone"); !errors.Is(err, ErrMuteInPast) {
		t.Errorf("Set until now = %v, want ErrMuteInPast", err)
	}

	hour := c.now().Add(time.Hour)
	if _, err := m.Set("/code/auth", &hour, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Set("/code/docs", nil, "panel"); err != nil {
		t.Fatal(err)
	}
	if !m.Muted("/code/auth") || !m.Muted("/code/docs") || m.Muted("/code/other") || m.Empty() {
		t.Fatal("both spaces should be muted and nothing else")
	}
	if got := m.List(); len(got) != 2 || got[0].Path != "/code/auth" || got[0].Via != "phone" || got[0].Until == nil || got[1].Until != nil {
		t.Errorf("List = %+v", got)
	}

	// An entry stops counting at its end time even before Expire runs.
	c.add(time.Hour)
	if m.Muted("/code/auth") {
		t.Error("an ended mute still counts")
	}
	if !m.Muted("/code/docs") {
		t.Error("an until-unmuted mute ended")
	}
	if _, ok := m.Get("/code/auth"); ok {
		t.Error("Get returns an ended mute")
	}

	var told []Mute
	m.OnExpire(func(e Mute) { told = append(told, e) })
	m.Expire()
	if len(told) != 1 || told[0].Path != "/code/auth" {
		t.Errorf("OnExpire got %+v, want only /code/auth", told)
	}
	m.Expire()
	if len(told) != 1 {
		t.Errorf("Expire told twice: %+v", told)
	}

	was, err := m.Clear("/code/docs")
	if err != nil || !was {
		t.Errorf("Clear = %v, %v; want true, nil", was, err)
	}
	if was, _ := m.Clear("/code/docs"); was {
		t.Error("Clear of an unmuted space says it was muted")
	}
	if !m.Empty() {
		t.Error("store should be empty")
	}
}

func TestMutesPersistAndFileMode(t *testing.T) {
	c := newClock()
	m, path := openMutes(t, c)
	until := c.now().Add(time.Hour)
	if _, err := m.Set("/code/auth", &until, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Set("/code/docs", nil, "phone"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}

	// A restart sees the same mutes.
	again, err := OpenMutes(path, MutesOptions{Now: c.now, Exists: func(string) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Muted("/code/auth") || !again.Muted("/code/docs") {
		t.Errorf("after a restart: %+v", again.List())
	}

	// A mute that ended while the daemon was down, and one whose folder is
	// gone, are dropped at start and the file is saved without them.
	c.add(2 * time.Hour)
	later, err := OpenMutes(path, MutesOptions{Now: c.now, Exists: func(p string) bool { return p != "/code/docs" }})
	if err != nil {
		t.Fatal(err)
	}
	if !later.Empty() {
		t.Errorf("stale entries kept: %+v", later.List())
	}
	third, _ := OpenMutes(path, MutesOptions{Now: c.now, Exists: func(string) bool { return true }})
	if !third.Empty() {
		t.Errorf("the dropped entries came back from the file: %+v", third.List())
	}
}

func TestMutesCorruptFileIsMovedAside(t *testing.T) {
	path := filepath.Join(t.TempDir(), MutesFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := OpenMutes(path, MutesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Empty() {
		t.Error("a corrupt file should start empty")
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Errorf("corrupt file not kept aside: %v", err)
	}
}

func TestMutesSaveFailureKeepsOldState(t *testing.T) {
	c := newClock()
	dir := t.TempDir()
	m, err := OpenMutes(filepath.Join(dir, "sub", MutesFileName), MutesOptions{Now: c.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Set("/code/auth", nil, "phone"); err != nil {
		t.Fatal(err)
	}
	// Make the directory unwritable by replacing it with a file.
	if err := os.RemoveAll(filepath.Join(dir, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Set("/code/docs", nil, "phone"); err == nil {
		t.Fatal("Set should fail when the file cannot be saved")
	}
	if m.Muted("/code/docs") {
		t.Error("a mute that could not be saved still counts")
	}
	if _, err := m.Clear("/code/auth"); err == nil {
		t.Fatal("Clear should fail when the file cannot be saved")
	}
	if !m.Muted("/code/auth") {
		t.Error("a clear that could not be saved took effect")
	}
}

// Run removes an ended mute by itself and tells OnExpire.
func TestMutesRunExpiresByTimer(t *testing.T) {
	m, err := OpenMutes("", MutesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan Mute, 1)
	m.OnExpire(func(e Mute) { got <- e })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	if _, err := m.Set("/code/auth", at(time.Now().Add(60*time.Millisecond)), "phone"); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		if e.Path != "/code/auth" {
			t.Errorf("expired %+v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the mute never expired")
	}
	if !m.Empty() {
		t.Error("expired mute still listed")
	}
}
