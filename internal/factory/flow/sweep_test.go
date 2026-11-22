package flow

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestSweepEpisodesOnceAndSkipsUnknown(t *testing.T) {
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
	var active, unknown map[string]string
	wakes := []Wake{{Key: "sweep@local_base@abc1234"}}
	notices, changed := SweepEpisodes(&active, &unknown, &wakes, probes)
	if !changed || len(notices) != 4 {
		t.Fatalf("SweepEpisodes = (%+v, %v), want 4 notices and changed=true", notices, changed)
	}
	if notices[0].WakeKey != "sweep@stray_terminal@t3ab" || notices[1].WakeKey != "sweep@worktree@/tmp/wt/T2-1" ||
		notices[2].WakeKey != "" || notices[2].ActKey != "sweep_unknown@branches" ||
		notices[3].WakeKey != "sweep@local_base@abc1234" {
		t.Fatalf("unexpected notices: %+v", notices)
	}
	// Repeating probes (even with a different unknown reason) emits nothing.
	probes[2].Unknown = "deadline exceeded"
	if again, ch := SweepEpisodes(&active, &unknown, &wakes, probes); len(again) != 0 || ch {
		t.Fatalf("second SweepEpisodes = (%+v, %v), want none and changed=false", again, ch)
	}
	// A new base SHA clears the old held wake and wakes once again; branches recovery clears unknown.
	probes[2].Unknown, probes[2].Items = "", nil
	probes[3].Items[0].Key = "def5678"
	if next, ch := SweepEpisodes(&active, &unknown, &wakes, probes); !ch || len(next) != 1 || next[0].WakeKey != "sweep@local_base@def5678" || len(wakes) != 0 || unknown != nil {
		t.Fatalf("new base SHA = (%+v, %v, wakes=%+v, unknown=%+v), want 1 for def5678 and cleared state", next, ch, wakes, unknown)
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
