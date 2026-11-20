package flow

import (
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
