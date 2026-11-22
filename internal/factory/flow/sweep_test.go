package flow

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSweepWakesOnceAndSkipsUnknown(t *testing.T) {
	probes := []SweepProbe{
		{Probe: "terminals", Items: []SweepItem{
			{Kind: "stray_terminal", Key: "t3ab", Summary: "a stray agent terminal t3ab with factory labels"},
			{Kind: "stray_terminal", Key: "t3ab", Summary: "duplicate should dedupe"},
		}},
		{Probe: "worktrees", Items: []SweepItem{
			{Kind: "worktree", Key: "/tmp/wt/T2-1", Summary: "a leftover worktree /tmp/wt/T2-1 of done task T2"},
		}},
		{Probe: "branches", Unknown: "git failed", Items: []SweepItem{
			{Kind: "branch", Key: "task/ignored", Summary: "must be ignored when unknown"},
		}},
		{Probe: "local_base", Items: []SweepItem{
			{Kind: "local_base", Key: "abc1234", Summary: "the local main is 2 commits ahead of origin/main"},
		}},
	}
	wakes := SweepWakes(nil, probes)
	if len(wakes) != 3 {
		t.Fatalf("SweepWakes = %+v, want 3 wakes", wakes)
	}
	if wakes[0].WakeKey != "sweep@stray_terminal@t3ab" || wakes[1].WakeKey != "sweep@worktree@/tmp/wt/T2-1" || wakes[2].WakeKey != "sweep@local_base@abc1234" {
		t.Fatalf("unexpected wake keys: %+v", wakes)
	}
	var acts []Act
	for _, w := range wakes {
		acts = append(acts, Act{Key: w.ActKey, Result: "done", At: time.Unix(100, 0)})
	}
	if again := SweepWakes(acts, probes); len(again) != 0 {
		t.Fatalf("second SweepWakes = %+v, want none", again)
	}
	// A new base SHA wakes once again.
	probes[3].Items[0].Key = "def5678"
	if next := SweepWakes(acts, probes); len(next) != 1 || next[0].WakeKey != "sweep@local_base@def5678" {
		t.Fatalf("new base SHA wakes = %+v, want 1 for def5678", next)
	}
}

// Slice 5b row 5b.10 (design.md 7.3, 10.1): sweep_minutes defaults to 5,
// validates 1–60, survives loading legacy state without the key, and appears
// in Settings.Changed() when non-default.
func TestSweepMinutesSetting(t *testing.T) {
	raw, err := json.Marshal(DefaultSettings())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if got, ok := fields["sweep_minutes"]; !ok || got != float64(5) {
		t.Fatalf("DefaultSettings() JSON sweep_minutes = %v (present=%v), want 5", got, ok)
	}

	// Legacy state.json omitting sweep_minutes keeps the default 5.
	legacy := DefaultSettings()
	if err := json.Unmarshal([]byte(`{}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy settings Validate() = %v, want nil", err)
	}
	if slices.ContainsFunc(legacy.Changed(), func(s string) bool { return strings.HasPrefix(s, "sweep_minutes ") }) {
		t.Errorf("legacy DefaultSettings().Changed() unexpectedly included sweep_minutes: %v", legacy.Changed())
	}

	// Range 1–60 (design.md 10.1).
	for _, tc := range []struct {
		val int
		ok  bool
	}{
		{1, true},
		{5, true},
		{60, true},
		{0, false},
		{-1, false},
		{61, false},
	} {
		s := DefaultSettings()
		if err := json.Unmarshal([]byte(fmt.Sprintf(`{"sweep_minutes":%d}`, tc.val)), &s); err != nil {
			t.Fatal(err)
		}
		err := s.Validate()
		if (err == nil) != tc.ok {
			t.Errorf("sweep_minutes=%d: Validate() = %v, want ok=%v", tc.val, err, tc.ok)
		}
		if !tc.ok && err != nil && (!strings.Contains(err.Error(), "sweep_minutes") || !strings.Contains(err.Error(), "1–60")) {
			t.Errorf("sweep_minutes=%d: Validate() error = %q, want sweep_minutes and range 1–60", tc.val, err.Error())
		}
	}

	// Non-default value shows in Changed().
	custom := DefaultSettings()
	if err := json.Unmarshal([]byte(`{"sweep_minutes":12}`), &custom); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(custom.Changed(), "sweep_minutes 12") {
		t.Errorf("Changed() = %q, want sweep_minutes 12", custom.Changed())
	}
}
