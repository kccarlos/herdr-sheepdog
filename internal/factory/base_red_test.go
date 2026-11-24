package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

const testCIWorkflowPath = ".github/workflows/ci.yml"

func writeBaseRuns(t *testing.T, ghDir, body string) {
	t.Helper()
	writeGHFile(t, ghDir, "base-runs.json", body)
}

// commitBaseHead commits a counted CI workflow (.github/workflows/ci.yml with
// on: [push, pull_request]) on main if not already present (or a distinct
// commit on main carrying it), pushes origin/main, writes base-ref.json, and
// returns the new head SHA.
func commitBaseHead(t *testing.T, repo, ghDir, tag string) string {
	t.Helper()
	var sha string
	if _, err := os.Stat(filepath.Join(repo, testCIWorkflowPath)); err != nil {
		sha = commitFile(t, repo, testCIWorkflowPath, "name: CI\non: [push, pull_request]\n")
	} else {
		sha = commitFile(t, repo, "base-head.txt", tag+"\n")
	}
	gitIn(t, repo, "push", "origin", "main")
	writeGHFile(t, ghDir, "base-ref.json", fmt.Sprintf(`{"object":{"sha":%q}}`, sha))
	return sha
}

func writeCountedBaseRun(t *testing.T, ghDir string, id int, headSHA, conclusion string) {
	t.Helper()
	writeBaseRuns(t, ghDir, fmt.Sprintf(
		`{"workflow_runs":[{"id":%d,"path":%q,"head_sha":%q,"status":"completed","conclusion":%q}]}`,
		id, testCIWorkflowPath, headSHA, conclusion,
	))
}

// TestBaseRedHoldsBehindAndGreenResumesKBMRG47 verifies KB-MRG-47, N38, the
// base observer's red/green/red wakes, status+/factory stage agreement, and
// that N40 resumes once the base turns green.
func TestBaseRedHoldsBehindAndGreenResumesKBMRG47(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	red1 := commitBaseHead(t, l.repo, l.gh, "red1")
	writeCountedBaseRun(t, l.gh, 101, red1, "failure")

	// Pass 1 & 2: base is red + behind -> N38 holds (no update-branch, no merge),
	// coordinator woken once for head sha red1.
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 0 {
		t.Fatalf("update-branch called while base red: %q", got)
	}
	if got := ghCalls(t, l.gh, "pr merge"); len(got) != 0 {
		t.Fatalf("merge called while base red: %q", got)
	}
	wakes := l.state().Project.Wakes
	if len(wakes) != 1 || wakes[0].Key != "base_red@"+red1+"@1" {
		t.Fatalf("wakes on red = %+v, want 1 base_red@%s@1", wakes, red1)
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

	// Base turns green (green1): coordinator woken once, and N40 asks update-branch.
	green1 := commitBaseHead(t, l.repo, l.gh, "green1")
	writeCountedBaseRun(t, l.gh, 102, green1, "success")
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 1 {
		t.Fatalf("update-branch calls after green = %q, want 1", got)
	}
	wakes = l.state().Project.Wakes
	if len(wakes) != 2 || wakes[1].Key != "base_green@"+green1+"@1" {
		t.Fatalf("wakes after green = %+v, want base_red then base_green@%s@1", wakes, green1)
	}

	// Base turns red again (red2): red -> green -> red wakes again.
	red2 := commitBaseHead(t, l.repo, l.gh, "red2")
	writeCountedBaseRun(t, l.gh, 103, red2, "timed_out")
	l.pass()
	wakes = l.state().Project.Wakes
	if len(wakes) != 3 || wakes[2].Key != "base_red@"+red2+"@1" {
		t.Fatalf("wakes after red->green->red = %+v, want base_red@%s@1", wakes, red2)
	}
}

// TestBaseRedNotBehindStillMerges verifies that a PR not behind the base with
// green checks still merges even while the base is red (it may repair the base).
func TestBaseRedNotBehindStillMerges(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":0}`)
	redHead := commitBaseHead(t, l.repo, l.gh, "red")
	writeCountedBaseRun(t, l.gh, 201, redHead, "failure")

	l.pass()
	if merges := ghCalls(t, l.gh, "pr merge"); len(merges) != 1 {
		t.Fatalf("not-behind PR merges while base red = %q, want 1", merges)
	}
}

// TestBaseRedPastMainRedMinutesOldestTaskOnce verifies N38 past main_red_minutes:
// once per red spell, only the oldest held task's worker is told to merge the
// base and say ready, and the coordinator is woken once naming that way out.
func TestBaseRedPastMainRedMinutesOldestTaskOnce(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	ctx := context.Background()
	// Add a second (younger) task T2 also passed and behind.
	wt2, err := fixtureGit(t).NewWorktree(ctx, l.repo, l.paths.Worktree(l.id, "T2", 1), "T2-1", "feat: y")
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
	// Keep T2 in its own worktree (I3) at l.c1 so both T1 and T2 have tip == H == l.c1 and are held by N38.
	gitIn(t, wt2.Path, "reset", "--hard", l.c1)
	w2, err := startRun(ctx, l.client, world.RunSpec{Name: "worker-t2", Cwd: wt2.Path, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setIdle(t, l.srv, l.client, w2, "idle")
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
	})
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	redHead := commitBaseHead(t, l.repo, l.gh, "red")
	writeCountedBaseRun(t, l.gh, 301, redHead, "failure")

	// Before main_red_minutes: held, no worker told, only base_red wake.
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 0 {
		t.Fatalf("T1 worker told before main_red_minutes: %q", sends)
	}
	if sends := sendsTo(t, l.srv.Requests(), w2); len(sends) != 0 {
		t.Fatalf("T2 worker told before main_red_minutes: %q", sends)
	}

	// Age base red spell past main_red_minutes in state.json.
	l.edit(func(s *flow.State) {
		s.Project.Base.Since = time.Now().Add(-time.Duration(s.Settings.MainRedMinutes+1) * time.Minute)
	})

	// Two passes: only T1 (oldest) is told once, T2 never told, coordinator woken once with base_red_held@<spell>.
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
		if w.Key == "base_red_held@"+redHead+"@1" {
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
		if strings.Contains(c, "event=push&head_sha=") {
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
	t.Parallel()
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
	redHead := commitBaseHead(t, l.repo, l.gh, "red")
	writeCountedBaseRun(t, l.gh, 401, redHead, "failure")
	l.pass()
	writeGHFile(t, l.gh, "base_runs_fail", "")
	l.pass()
	wakes := l.state().Project.Wakes
	if len(wakes) != 1 || wakes[0].Key != "base_red@"+redHead+"@1" {
		t.Fatalf("wakes after red then read error = %+v, want only base_red@%s@1", wakes, redHead)
	}
	if b := l.state().Project.Base; b == nil || !b.Red {
		t.Fatal("base flipped to green on read error")
	}
}

// TestBaseRedRerunStaysRed verifies that a re-run in progress on a red head
// commit keeps the base red (no base_green wake, no update-branch).
func TestBaseRedRerunStaysRed(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	redHead := commitBaseHead(t, l.repo, l.gh, "red")
	writeCountedBaseRun(t, l.gh, 501, redHead, "failure")
	l.pass()

	// Re-run of 501 is now in progress while a second workflow already passed: must stay red.
	writeBaseRuns(t, l.gh, fmt.Sprintf(
		`{"workflow_runs":[{"id":501,"path":%q,"head_sha":%q,"status":"in_progress","conclusion":""},{"id":502,"path":%q,"head_sha":%q,"status":"completed","conclusion":"success"}]}`,
		testCIWorkflowPath, redHead, testCIWorkflowPath, redHead,
	))
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 0 {
		t.Fatalf("update-branch called during rerun of red base: %q", got)
	}
	if wakes := l.state().Project.Wakes; len(wakes) != 1 {
		t.Fatalf("wakes during rerun = %+v, want only the initial red wake", wakes)
	}
}

// TestBaseRedSurvivesRestartAndStopStart verifies that the red spell (red, since,
// spell id = first red head sha) persists in state.json across engine restarts,
// and that Stop/Start resets it consistently with Project.Acts.
func TestBaseRedSurvivesRestartAndStopStart(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	red1 := commitBaseHead(t, l.repo, l.gh, "red1")
	writeCountedBaseRun(t, l.gh, 601, red1, "failure")
	l.pass()

	// Daemon restarts with a brand-new Engine; a second failing push landed on main while down.
	eng2 := newGitTestEngine(t, l.paths, l.client)
	eng2.Bin = l.eng.Bin
	red2 := commitBaseHead(t, l.repo, l.gh, "red2")
	writeCountedBaseRun(t, l.gh, 602, red2, "failure")
	eng2.passProject(context.Background(), l.id, false)

	// Must NOT wake a second time in the same red spell, and keeps original spell ID and Since.
	st := l.state()
	if len(st.Project.Wakes) != 1 || st.Project.Base == nil || st.Project.Base.SpellID != red1+"@1" {
		t.Fatalf("after restart: wakes=%+v base=%+v, want 1 wake and spell %s@1", st.Project.Wakes, st.Project.Base, red1)
	}

	// Age past main_red_minutes, escalate once, then restart again: must NOT tell or wake twice.
	l.edit(func(s *flow.State) {
		s.Project.Base.Since = time.Now().Add(-time.Duration(s.Settings.MainRedMinutes+1) * time.Minute)
	})
	eng2.passProject(context.Background(), l.id, false)
	eng3 := newGitTestEngine(t, l.paths, l.client)
	eng3.Bin = l.eng.Bin
	eng3.passProject(context.Background(), l.id, false)
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 1 {
		t.Fatalf("sends after escalation + restart = %d, want 1", len(sends))
	}

	// Stop + Start clears Project.Base consistently with Project.Acts.
	if err := eng3.Stop(l.id); err != nil {
		t.Fatal(err)
	}
	if b := l.state().Project.Base; b != nil {
		t.Fatalf("Project.Base after Stop = %+v, want nil", b)
	}
	if err := eng3.Start(l.repo, "goal", 0); err != nil {
		t.Fatal(err)
	}
	if b := l.state().Project.Base; b != nil {
		t.Fatalf("Project.Base after Start = %+v, want nil", b)
	}
}

// TestBaseRedWaitTextClearsOnGreen verifies that GraphViewOf derives N38's wait
// text from the row's WaitsOn and clears it once the base is no longer red.
func TestBaseRedWaitTextClearsOnGreen(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	red1 := commitBaseHead(t, l.repo, l.gh, "red1")
	writeCountedBaseRun(t, l.gh, 701, red1, "failure")
	l.pass()

	// Base turns green while compare fails (N36 holds without logging a new row).
	writeGHFile(t, l.gh, "compare_fail", "")
	grn2 := commitBaseHead(t, l.repo, l.gh, "grn2")
	writeCountedBaseRun(t, l.gh, 702, grn2, "success")
	l.pass()
	gv := GraphViewOf(l.state(), l.logLines())
	if strings.Contains(strings.ToLower(gv.Tasks[0].WhyWaiting), "base") {
		t.Fatalf("graph WhyWaiting after base turned green = %q, want base hold cleared", gv.Tasks[0].WhyWaiting)
	}
}

// TestBaseRedFailedTellRetriesOnceNextPass verifies that a failed worker tell
// does not mark the red spell escalated, and retries after ensure_backoff_minutes.
func TestBaseRedFailedTellRetriesOnceNextPass(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	now := time.Now()
	l.eng.Now = func() time.Time { return now }
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	redHead := commitBaseHead(t, l.repo, l.gh, "red")
	writeCountedBaseRun(t, l.gh, 801, redHead, "failure")
	l.pass()

	// Age past main_red_minutes and clear RunID (with start_worker already recorded) so tell fails on pass 1.
	l.edit(func(s *flow.State) {
		s.Project.Base.Since = now.Add(-time.Duration(s.Settings.MainRedMinutes+1) * time.Minute)
		s.Tasks[0].Attempts[0].RunID = ""
		s.Tasks[0].Attempts[0].Acts = append(s.Tasks[0].Attempts[0].Acts, flow.Act{Key: flow.AskStartWorker, Result: "done"})
	})
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 0 {
		t.Fatalf("sends when RunID empty = %q, want 0", sends)
	}

	// Restore RunID: before back-off it waits; past back-off it retries once and succeeds.
	l.edit(func(s *flow.State) {
		s.Tasks[0].Attempts[0].RunID = l.worker
	})
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 0 {
		t.Fatalf("sends before backoff = %q, want 0", sends)
	}
	now = now.Add(time.Minute)
	l.pass()
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 1 {
		t.Fatalf("sends after failed tell + retry = %d (%q), want 1", len(sends), sends)
	}
}

// TestBaseRedGreenRedSameSHAWakesAndTellsAgain verifies item 1: red -> green ->
// red on the same base commit sha opens a new spell, wakes the coordinator
// again, and may tell a held worker once more after main_red_minutes.
func TestBaseRedGreenRedSameSHAWakesAndTellsAgain(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	now := time.Now()
	l.eng.Now = func() time.Time { return now }
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	sameHead := commitBaseHead(t, l.repo, l.gh, "same")

	// Spell 1: red at sameHead -> base_red wake, escalate past main_red_minutes.
	writeCountedBaseRun(t, l.gh, 901, sameHead, "failure")
	l.pass()
	l.edit(func(s *flow.State) {
		s.Project.Base.Since = now.Add(-time.Duration(s.Settings.MainRedMinutes+1) * time.Minute)
	})
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 1 {
		t.Fatalf("spell 1 sends = %d, want 1", len(sends))
	}

	// Re-run passes at the same sha -> base_green wake.
	setStatusAt(t, l, l.worker, "idle", now.Add(time.Second))
	now = now.Add(30 * time.Second)
	writeCountedBaseRun(t, l.gh, 901, sameHead, "success")
	l.pass()

	// Spell 2: a later re-run fails at the same sha -> new base_red wake, and tells again past main_red_minutes.
	writeCountedBaseRun(t, l.gh, 901, sameHead, "failure")
	l.pass()
	redWakes := 0
	for _, w := range l.state().Project.Wakes {
		if strings.HasPrefix(w.Key, "base_red@") {
			redWakes++
		}
	}
	if redWakes != 2 {
		t.Fatalf("base_red wakes after red->green->red on same sha = %d, want 2 (all: %+v)", redWakes, l.state().Project.Wakes)
	}
	l.edit(func(s *flow.State) {
		s.Project.Base.Since = now.Add(-time.Duration(s.Settings.MainRedMinutes+1) * time.Minute)
	})
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 2 {
		t.Fatalf("sends after second red spell on same sha = %d, want 2", len(sends))
	}
}

// TestBaseRedFailedTellFallbackWakesOncePerSpell verifies item 2: when the
// first held task's N38 tell fails out and a second held task is told in the
// same red spell, the coordinator is woken once per spell for base_red_held.
func TestBaseRedFailedTellFallbackWakesOncePerSpell(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	now := time.Now()
	l.eng.Now = func() time.Time { return now }
	ctx := context.Background()
	w2, err := startRun(ctx, l.client, world.RunSpec{Name: "worker-t2-n38", Cwd: l.wt, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setIdle(t, l.srv, l.client, w2, "idle")
	l.edit(func(s *flow.State) {
		s.Settings.EnsureFailures = 2
		s.Tasks[0].Attempts[0].RunID = ""
		s.Tasks[0].Attempts[0].Acts = append(s.Tasks[0].Attempts[0].Acts, flow.Act{Key: flow.AskStartWorker, Result: "done"})
		s.Tasks = append(s.Tasks, flow.Task{
			ID: "T2", Title: "feat: y", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: s.Tasks[0].Attempts[0].BaseCommit, Branch: l.branch, Worktree: l.wt, PR: 7, RunID: w2,
				Ready: []flow.Ready{{Head: l.c1}},
				Rounds: []flow.Round{
					{N: 1, Kind: "full", Head: l.c1, Verdict: flow.VerdictPass},
					{N: 2, Kind: "full", Head: l.c1, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{}, Acts: []flow.Act{},
			}},
		})
	})
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	redHead := commitBaseHead(t, l.repo, l.gh, "red")
	writeCountedBaseRun(t, l.gh, 902, redHead, "failure")
	l.pass()
	l.edit(func(s *flow.State) {
		s.Project.Base.Since = now.Add(-time.Duration(s.Settings.MainRedMinutes+1) * time.Minute)
	})
	// Pass 1 & 2: T1 fails tell_worker_base_red twice (hits EnsureFailures=2); pass 3: T1 is at N5, T2 is told.
	l.pass()
	now = now.Add(time.Minute)
	l.pass()
	l.pass()
	if sends2 := sendsTo(t, l.srv.Requests(), w2); len(sends2) != 1 {
		t.Fatalf("T2 worker sends = %d, want 1 fallback tell", len(sends2))
	}
	heldWakes := 0
	for _, w := range l.state().Project.Wakes {
		if strings.HasPrefix(w.Key, "base_red_held@") {
			heldWakes++
		}
	}
	if heldWakes != 1 {
		t.Fatalf("base_red_held wakes = %d, want 1 (all: %+v)", heldWakes, l.state().Project.Wakes)
	}
}

// TestBaseStatusPullRequestTargetCounts verifies PR A rule 1 through
// observeBaseLocked with the real parsePRWorkflow parser (design review round 2):
// a push-only workflow (pages.yml) is ignored and must not redden the base,
// while a workflow with on: [push, pull_request_target] is counted and reddens
// the base when its push run fails.
func TestBaseStatusPullRequestTargetCounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("push_only_ignored", func(t *testing.T) {
		t.Parallel()
		repo, _ := initGitRepoWithRemote(t)
		githubOrigin(t, repo)
		gh := bindRepoFakeGH(t, repo)
		head := commitFile(t, repo, ".github/workflows/pages.yml", "name: Pages\non: push\n")
		gitIn(t, repo, "push", "origin", "main")
		writeGHFile(t, gh, "base-ref.json", `{"object":{"sha":"`+head+`"}}`)
		writeBaseRuns(t, gh, `{"workflow_runs":[{"id":45325825569,"path":".github/workflows/pages.yml","head_sha":"`+head+`","status":"completed","conclusion":"failure"}]}`)

		eng := newGitTestEngine(t, testPaths(t), nil)
		s := flow.State{Settings: flow.DefaultSettings()}
		s.Project.Folder = repo
		eng.observeBaseLocked(ctx, "project", &s)
		if s.Project.Base != nil && s.Project.Base.Red {
			t.Fatalf("push-only pages.yml failure reddened base: %+v", s.Project.Base)
		}
		if len(s.Project.Wakes) != 0 {
			t.Fatalf("wakes on push-only failure = %+v, want none", s.Project.Wakes)
		}
	})

	t.Run("pull_request_target_counts", func(t *testing.T) {
		t.Parallel()
		repo, _ := initGitRepoWithRemote(t)
		githubOrigin(t, repo)
		gh := bindRepoFakeGH(t, repo)
		head := commitFile(t, repo, ".github/workflows/ci.yml", "name: CI\non: [push, pull_request_target]\n")
		gitIn(t, repo, "push", "origin", "main")
		writeGHFile(t, gh, "base-ref.json", `{"object":{"sha":"`+head+`"}}`)
		writeBaseRuns(t, gh, `{"workflow_runs":[{"id":45340000001,"path":".github/workflows/ci.yml","head_sha":"`+head+`","status":"completed","conclusion":"failure"}]}`)

		eng := newGitTestEngine(t, testPaths(t), nil)
		s := flow.State{Settings: flow.DefaultSettings()}
		s.Project.Folder = repo
		eng.observeBaseLocked(ctx, "project", &s)
		if s.Project.Base == nil || !s.Project.Base.Red {
			t.Fatalf("pull_request_target failure did not redden base: %+v", s.Project.Base)
		}
		if len(s.Project.Wakes) != 1 || s.Project.Wakes[0].Key != "base_red@"+head+"@1" {
			t.Fatalf("wakes on pull_request_target failure = %+v, want 1 base_red@%s@1", s.Project.Wakes, head)
		}
	})
}

// TestBaseRedCleanCancelStaysRed verifies PR A (finding 8, rule 4): once the
// base is red, a later head whose counted CI run ends with a clean cancel (no
// failed or timed-out job) returns unknown, keeps the base red, emits no
// base_green wake, and keeps N38 holding behind PRs.
func TestBaseRedCleanCancelStaysRed(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)

	// Head 1 (trial 291cd69 shape): counted CI fails -> base turns red.
	redHead := commitFile(t, l.repo, ".github/workflows/ci.yml", "name: CI\non: [push, pull_request]\n")
	gitIn(t, l.repo, "push", "origin", "main")
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"`+redHead+`"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":45335000001,"path":".github/workflows/ci.yml","head_sha":"`+redHead+`","status":"completed","conclusion":"failure"}]}`)
	l.pass()
	if b := l.state().Project.Base; b == nil || !b.Red {
		t.Fatalf("after red head: base = %+v, want red", b)
	}

	// Head 2: clean cancel of counted CI (no failed job, no timeout annotation) ->
	// unknown, so the base stays red with no base_green wake and no update-branch.
	cancelHead := commitFile(t, l.repo, "note.txt", "next commit\n")
	gitIn(t, l.repo, "push", "origin", "main")
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"`+cancelHead+`"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":45335000002,"path":".github/workflows/ci.yml","head_sha":"`+cancelHead+`","status":"completed","conclusion":"cancelled"}]}`)
	writeGHFile(t, l.gh, "run-jobs.json", `{"jobs":[{"id":901,"name":"Test","conclusion":"cancelled"}]}`)
	writeGHFile(t, l.gh, "annotations.json", `[{"message":"The run was canceled by @someone."}]`)
	l.pass()

	if b := l.state().Project.Base; b == nil || !b.Red {
		t.Fatalf("after clean cancel: base = %+v, want still red", b)
	}
	if wakes := l.state().Project.Wakes; len(wakes) != 1 || wakes[0].Key != "base_red@"+redHead+"@1" {
		t.Fatalf("wakes after clean cancel = %+v, want only base_red@%s@1 (no base_green)", wakes, redHead)
	}
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 0 {
		t.Fatalf("update-branch called after clean cancel on red base: %q", got)
	}
}

// TestFinishedProjectClearsMainRed verifies finding 11 (round 2): when main_red
// is active and the last task is dropped so the project finishes, a later green
// base still clears main_red via observeBaseLocked without holding a base_green
// wake, recording a base_green act, or starting a second coordinator.
func TestFinishedProjectClearsMainRed(t *testing.T) {
	t.Parallel()
	h := newGitProjectHarness(t, "Main red then finish")
	h.pass() // J7 starts coord 1
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open", Parked: true}}
	})
	h.setCoordWorking(coord1)

	// Base turns red and stays red past main_red_minutes -> main_red alert active.
	redHead := commitBaseHead(t, h.repo, h.gh, "red1")
	writeCountedBaseRun(t, h.gh, 101, redHead, "failure")
	h.now = h.now.Add(15 * time.Second)
	h.pass()
	h.now = h.now.Add(time.Duration(h.state().Settings.MainRedMinutes+1) * time.Minute)
	h.setCoordWorking(coord1)
	if !h.state().Project.HasAlert(flow.AlertMainRed) {
		t.Fatalf("expected main_red alert before finish: %+v", h.state().Project.Alerts)
	}

	// Drop the last task, deliver the pending base_red wake, and finish through J4.
	h.edit(func(s *flow.State) {
		s.Tasks[0].Dropped = &flow.Dropped{Why: "no longer needed"}
	})
	h.coordIdleWithStatus(coord1, "idle") // J12 delivers base_red wake
	h.setCoordWorking(coord1)
	h.finishThroughJ4(coord1)

	if st := h.state(); len(h.runs) != 1 || st.Project.RunID != "" || !st.Project.HasAlert(flow.AlertMainRed) || len(st.Project.Wakes) != 0 {
		t.Fatalf("after finish with main_red: runs=%d runID=%q alerts=%+v wakes=%+v", len(h.runs), st.Project.RunID, st.Project.Alerts, st.Project.Wakes)
	}

	// Base turns green on a new head -> main_red clears, no wake held, one run ever.
	greenHead := commitBaseHead(t, h.repo, h.gh, "green1")
	writeCountedBaseRun(t, h.gh, 102, greenHead, "success")
	h.now = h.now.Add(time.Duration(h.state().Settings.CoordinatorGapMinutes+1) * time.Minute)
	h.pass()
	h.pass()

	st := h.state()
	if st.Project.HasAlert(flow.AlertMainRed) {
		t.Fatalf("main_red did not clear after base turned green on finished project: %+v", st.Project.Alerts)
	}
	if len(st.Project.Wakes) != 0 {
		t.Fatalf("base_green on finished project held wakes: %+v", st.Project.Wakes)
	}
	for _, a := range st.Project.Acts {
		if strings.HasPrefix(a.Key, flow.AskWake+"@base_green@") {
			t.Fatalf("base_green on finished project recorded wake act: %+v", a)
		}
	}
	if len(h.runs) != 1 || st.Project.RunID != "" {
		t.Fatalf("base_green on finished project started a second coordinator: runs=%d runID=%q", len(h.runs), st.Project.RunID)
	}
}

// TestFinishedProjectClearsGitHubUnavailable pins finding 11 (round 2): when
// github_unavailable is raised and the project finishes, observeBaseLocked
// keeps running so the first good GitHub response clears github_unavailable.
func TestFinishedProjectClearsGitHubUnavailable(t *testing.T) {
	t.Parallel()
	h := newGitProjectHarness(t, "GitHub outage then finish")
	h.pass() // J7 starts coord 1
	coord1 := h.state().Project.RunID
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: open", Parked: true}}
	})
	h.setCoordWorking(coord1)

	writeGHFile(t, h.gh, "fail", "")
	h.now = h.now.Add(15 * time.Second)
	h.pass()
	if !h.state().Project.HasAlert(flow.AlertGitHubUnavailable) {
		t.Fatalf("expected github_unavailable before finish: %+v", h.state().Project.Alerts)
	}

	h.edit(func(s *flow.State) {
		s.Tasks[0].Dropped = &flow.Dropped{Why: "no longer needed"}
	})
	h.finishThroughJ4(coord1)
	if st := h.state(); len(h.runs) != 1 || st.Project.RunID != "" || !st.Project.HasAlert(flow.AlertGitHubUnavailable) {
		t.Fatalf("after finish with github_unavailable: runs=%d runID=%q alerts=%+v", len(h.runs), st.Project.RunID, st.Project.Alerts)
	}

	// GitHub answers again -> github_unavailable clears, no wake held, one run ever.
	_ = os.Remove(filepath.Join(h.gh, "fail"))
	greenHead := commitBaseHead(t, h.repo, h.gh, "green1")
	writeCountedBaseRun(t, h.gh, 201, greenHead, "success")
	h.now = h.now.Add(time.Minute)
	h.pass()

	st := h.state()
	if st.Project.HasAlert(flow.AlertGitHubUnavailable) {
		t.Fatalf("github_unavailable did not clear on finished project when GitHub answered again: %+v", st.Project.Alerts)
	}
	if len(h.runs) != 1 || st.Project.RunID != "" || len(st.Project.Wakes) != 0 {
		t.Fatalf("after github_unavailable cleared on finished project: runs=%d runID=%q wakes=%+v", len(h.runs), st.Project.RunID, st.Project.Wakes)
	}
}
