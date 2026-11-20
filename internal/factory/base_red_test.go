package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

func writeBaseRuns(t *testing.T, ghDir, body string) {
	t.Helper()
	writeGHFile(t, ghDir, "base-runs.json", body)
}

// TestBaseRedHoldsBehindAndGreenResumesKBMRG47 verifies KB-MRG-47, N38, the
// base observer's red/green/red wakes, status+/factory stage agreement, and
// that N40 resumes once the base turns green.
func TestBaseRedHoldsBehindAndGreenResumesKBMRG47(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":101,"head_sha":"deadbeef1234567890","conclusion":"failure"}]}`)

	// Pass 1 & 2: base is red + behind -> N38 holds (no update-branch, no merge),
	// coordinator woken once for run 101.
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 0 {
		t.Fatalf("update-branch called while base red: %q", got)
	}
	if got := ghCalls(t, l.gh, "pr merge"); len(got) != 0 {
		t.Fatalf("merge called while base red: %q", got)
	}
	wakes := l.state().Project.Wakes
	if len(wakes) != 1 || wakes[0].Key != "base_red@101" {
		t.Fatalf("wakes on red = %+v, want 1 base_red@101", wakes)
	}
	// Shared stage function: status and /factory both name the base branch and agree on stage.
	st, logs := l.state(), l.logLines()
	sv, gv := StatusViewOf(st, logs), GraphViewOf(st, logs)
	if len(sv) != 1 || len(gv.Tasks) != 1 || sv[0].Stage != gv.Tasks[0].Stage || sv[0].Rule != "N38" || sv[0].Stale {
		t.Fatalf("status=%+v graph=%+v, want matching stage with non-stale N38", sv, gv.Tasks)
	}
	if !strings.Contains(strings.ToLower(sv[0].Why), "base") || !strings.Contains(strings.ToLower(gv.Tasks[0].WhyWaiting), "base") {
		t.Fatalf("status why=%q graph why=%q, want both naming the base branch", sv[0].Why, gv.Tasks[0].WhyWaiting)
	}

	// Base red + BehindUnknown: N38 still holds (no update, no merge, no N36 behind_unknown wake).
	writeGHFile(t, l.gh, "compare_fail", "")
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 0 {
		t.Fatalf("update-branch called on base red + BehindUnknown: %q", got)
	}
	if got := ghCalls(t, l.gh, "pr merge"); len(got) != 0 {
		t.Fatalf("merge called on base red + BehindUnknown: %q", got)
	}
	_ = os.Remove(filepath.Join(l.gh, "compare_fail"))
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)

	// Base turns green (run 102): coordinator woken once for 102, and N40 asks update-branch.
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":102,"head_sha":"cafebabe1234567890","conclusion":"success"}]}`)
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 1 {
		t.Fatalf("update-branch calls after green = %q, want 1", got)
	}
	wakes = l.state().Project.Wakes
	if len(wakes) != 2 || wakes[1].Key != "base_green@102" {
		t.Fatalf("wakes after green = %+v, want base_red@101 then base_green@102", wakes)
	}

	// Base turns red again (run 103): red -> green -> red wakes again.
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":103,"head_sha":"badc0ffe1234567890","conclusion":"timed_out"}]}`)
	l.pass()
	wakes = l.state().Project.Wakes
	if len(wakes) != 3 || wakes[2].Key != "base_red@103" {
		t.Fatalf("wakes after red->green->red = %+v, want base_red@103", wakes)
	}
}

// TestBaseRedNotBehindStillMerges verifies that a PR not behind the base with
// green checks still merges even while the base is red (it may repair the base).
func TestBaseRedNotBehindStillMerges(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":0}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":201,"head_sha":"deadbeef1234567890","conclusion":"failure"}]}`)

	l.pass()
	if merges := ghCalls(t, l.gh, "pr merge"); len(merges) != 1 {
		t.Fatalf("not-behind PR merges while base red = %q, want 1", merges)
	}
}

// TestBaseRedPastMainRedMinutesOldestTaskOnce verifies N38 past main_red_minutes:
// once per red spell, only the oldest held task's worker is told to merge the
// base and say ready, and the coordinator is woken once naming that way out.
func TestBaseRedPastMainRedMinutesOldestTaskOnce(t *testing.T) {
	l := newLanding(t)
	ctx := context.Background()
	// Add a second (younger) task T2 also passed and behind.
	wt2, err := world.NewWorktree(ctx, l.repo, l.paths.Worktree(l.id, "T2", 1), "T2-1", "feat: y")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt2.Path, "y.go"), []byte("package demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt2.Path, "add", "y.go")
	gitIn(t, wt2.Path, "commit", "-m", "feat: y")
	c2 := gitIn(t, wt2.Path, "rev-parse", "HEAD")
	gitIn(t, wt2.Path, "push", "origin", c2+":refs/heads/"+wt2.Branch)
	w2, err := world.StartRun(ctx, l.client, world.RunSpec{Name: "worker-t2", Cwd: wt2.Path, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	l.srv.SetStatus(w2, &hc.Status{App: "claude-code", State: "idle"})
	l.edit(func(s *flow.State) {
		s.Tasks = append(s.Tasks, flow.Task{
			ID: "T2", Title: "feat: y", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: wt2.BaseCommit, Branch: wt2.Branch, Worktree: wt2.Path, PR: 7, RunID: w2,
				Ready: []flow.Ready{{Head: l.c1}}, // matches view.json's head so both T1 and T2 are held at N38
				Rounds: []flow.Round{
					{N: 1, Kind: "full", Head: l.c1, Verdict: flow.VerdictPass},
					{N: 2, Kind: "full", Head: l.c1, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{}, Acts: []flow.Act{},
			}},
		})
		// Align T2's Ready/tip by using T1's worktree head on T2? No: inspect checks tip == H;
		// point T2's worktree to l.wt so both have tip == H == l.c1 and are held by N38.
		s.Tasks[1].Attempts[0].Worktree = l.wt
	})
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":301,"head_sha":"deadbeef1234567890","conclusion":"failure"}]}`)

	// Before main_red_minutes: held, no worker told, only base_red@301 wake.
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 0 {
		t.Fatalf("T1 worker told before main_red_minutes: %q", sends)
	}
	if sends := sendsTo(t, l.srv.Requests(), w2); len(sends) != 0 {
		t.Fatalf("T2 worker told before main_red_minutes: %q", sends)
	}

	// Age base red spell past main_red_minutes.
	l.eng.mu.Lock()
	b := l.eng.base[l.id]
	b.since = time.Now().Add(-time.Duration(l.state().Settings.MainRedMinutes+1) * time.Minute)
	l.eng.base[l.id] = b
	l.eng.mu.Unlock()

	// Two passes: only T1 (oldest) is told once, T2 never told, coordinator woken once with base_red_held@301.
	l.pass()
	l.pass()
	sends1 := sendsTo(t, l.srv.Requests(), l.worker)
	if len(sends1) != 1 || !strings.Contains(sends1[0], "merge the base") || !strings.Contains(sends1[0], "report ready") {
		t.Fatalf("T1 worker sends = %q, want 1 telling it to merge the base and say ready", sends1)
	}
	if sends2 := sendsTo(t, l.srv.Requests(), w2); len(sends2) != 0 {
		t.Fatalf("younger T2 worker told = %q, want none", sends2)
	}
	heldWakes := 0
	for _, w := range l.state().Project.Wakes {
		if w.Key == "base_red_held@301" {
			heldWakes++
			if w.Task != "T1" || !strings.Contains(w.Why, "merge the base") {
				t.Errorf("base_red_held wake = %+v, want task T1 naming merge the base", w)
			}
		}
	}
	if heldWakes != 1 {
		t.Fatalf("base_red_held wakes = %d, want 1 (all: %+v)", heldWakes, l.state().Project.Wakes)
	}
	// Base observer ran once per pass (project level), not once per task: 3 passes -> 3 base-runs calls.
	baseCalls := 0
	for _, c := range ghCalls(t, l.gh, "api repos/acme/demo/actions/runs?branch=") {
		if strings.Contains(c, "event=push&status=completed") {
			baseCalls++
		}
	}
	if baseCalls != 3 {
		t.Fatalf("base observer calls across 3 passes with 2 tasks = %d, want 3 (once per pass)", baseCalls)
	}
}

// TestBaseRedReadErrorKeepsLastState verifies that a runs-list read error never
// flips the base red and keeps the last known state when already red.
func TestBaseRedReadErrorKeepsLastState(t *testing.T) {
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	writeGHFile(t, l.gh, "base_runs_fail", "")

	// Read error when initially green: does NOT flip red, so N40 asks update-branch.
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 1 {
		t.Fatalf("update-branch calls on base read error = %q, want 1 (not flipped red)", got)
	}
	if wakes := l.state().Project.Wakes; len(wakes) != 0 {
		t.Fatalf("wakes on base read error = %+v, want none", wakes)
	}

	// Now turn red, then fail reads: must stay red (no green wake).
	_ = os.Remove(filepath.Join(l.gh, "base_runs_fail"))
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":401,"head_sha":"deadbeef1234567890","conclusion":"failure"}]}`)
	l.pass()
	writeGHFile(t, l.gh, "base_runs_fail", "")
	l.pass()
	wakes := l.state().Project.Wakes
	if len(wakes) != 1 || wakes[0].Key != "base_red@401" {
		t.Fatalf("wakes after red then read error = %+v, want only base_red@401", wakes)
	}
	l.eng.mu.Lock()
	stillRed := l.eng.base[l.id].red
	l.eng.mu.Unlock()
	if !stillRed {
		t.Fatal("base flipped to green on read error")
	}
}
