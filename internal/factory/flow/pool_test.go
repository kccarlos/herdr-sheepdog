package flow

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestPoolSettings (PR 5b.6, design.md 10.1): pool_floor_percent is 0–50 and
// pool_minutes 1–60, both default 5; each names itself and its range when
// refused, shows in Changed() only off its default, and round-trips by its
// settings name.
func TestPoolSettings(t *testing.T) {
	def := DefaultSettings()
	if def.PoolFloorPercent != 5 || def.PoolMinutes != 5 {
		t.Fatalf("defaults = %d, %d, want 5, 5", def.PoolFloorPercent, def.PoolMinutes)
	}
	if err := def.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		set      func(*Settings, int)
		low, top int
	}{
		{"pool_floor_percent", func(s *Settings, n int) { s.PoolFloorPercent = n }, 0, 50},
		{"pool_minutes", func(s *Settings, n int) { s.PoolMinutes = n }, 1, 60},
	} {
		for _, n := range []int{c.low, c.top, c.low - 1, c.top + 1, -100, 1000} {
			s := DefaultSettings()
			c.set(&s, n)
			err, ok := s.Validate(), n >= c.low && n <= c.top
			if (err == nil) != ok {
				t.Errorf("%s %d: err %v, want ok=%v", c.name, n, err, ok)
			}
			if err != nil && !strings.Contains(err.Error(), c.name+" ") {
				t.Errorf("%s %d: error %q does not name the setting", c.name, n, err)
			}
		}
	}
	s := DefaultSettings()
	s.PoolFloorPercent = 51
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "pool_floor_percent 51: allowed 0–50") {
		t.Errorf("floor 51: %v", err)
	}
	s = DefaultSettings()
	s.PoolMinutes = 0
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "pool_minutes 0: allowed 1–60") {
		t.Errorf("minutes 0: %v", err)
	}

	// Changed() lists a setting only when it is off its default; 0 is a
	// real floor, not "unset".
	if got := def.Changed(); slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, "pool_") }) {
		t.Errorf("defaults listed as changed: %q", got)
	}
	s = DefaultSettings()
	s.PoolFloorPercent, s.PoolMinutes = 0, 15
	if got := s.Changed(); !slices.Contains(got, "pool_floor_percent 0") || !slices.Contains(got, "pool_minutes 15") {
		t.Errorf("Changed() = %q, want pool_floor_percent 0 and pool_minutes 15", got)
	}

	raw, err := json.Marshal(DefaultSettings())
	if err != nil || !strings.Contains(string(raw), `"pool_floor_percent":5`) || !strings.Contains(string(raw), `"pool_minutes":5`) {
		t.Errorf("settings JSON lacks the pool settings: %s (%v)", raw, err)
	}
	var back Settings
	if err := json.Unmarshal([]byte(`{"pool_floor_percent":0,"pool_minutes":30}`), &back); err != nil || back.PoolFloorPercent != 0 || back.PoolMinutes != 30 {
		t.Errorf("unmarshal = %+v (%v)", back, err)
	}
}
