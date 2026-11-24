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
)

func writeBaseRuns(t *testing.T, ghDir, body string) {
	t.Helper()
	writeGHFile(t, ghDir, "base-runs.json", body)
}

// TestBaseRedHoldsBehindAndGreenResumesKBMRG47 verifies KB-MRG-47, N38, the
// base observer's red/green/red wakes, status+/factory stage agreement, and
// that N40 resumes once the base turns green.
func TestBaseRedHoldsBehindAndGreenResumesKBMRG47(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":1}`)
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"deadbeef1234567890"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":101,"head_sha":"deadbeef1234567890","status":"completed","conclusion":"failure"}]}`)

	// Pass 1 & 2: base is red + behind -> N38 holds (no update-branch, no merge),
	// coordinator woken once for head sha deadbeef1234567890.
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 0 {
		t.Fatalf("update-branch called while base red: %q", got)
	}
	if got := ghCalls(t, l.gh, "pr merge"); len(got) != 0 {
		t.Fatalf("merge called while base red: %q", got)
	}
	wakes := l.state().Project.Wakes
	if len(wakes) != 1 || wakes[0].Key != "base_red@deadbeef1234567890@1" {
		t.Fatalf("wakes on red = %+v, want 1 base_red@deadbeef1234567890@1", wakes)
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

	// Base turns green (cafebabe1234567890): coordinator woken once, and N40 asks update-branch.
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"cafebabe1234567890"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":102,"head_sha":"cafebabe1234567890","status":"completed","conclusion":"success"}]}`)
	l.pass()
	l.pass()
	if got := ghCalls(t, l.gh, "api -X PUT"); len(got) != 1 {
		t.Fatalf("update-branch calls after green = %q, want 1", got)
	}
	wakes = l.state().Project.Wakes
	if len(wakes) != 2 || wakes[1].Key != "base_green@cafebabe1234567890@1" {
		t.Fatalf("wakes after green = %+v, want base_red then base_green@cafebabe1234567890@1", wakes)
	}

	// Base turns red again (badc0ffe1234567890): red -> green -> red wakes again.
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"badc0ffe1234567890"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":103,"head_sha":"badc0ffe1234567890","status":"completed","conclusion":"timed_out"}]}`)
	l.pass()
	wakes = l.state().Project.Wakes
	if len(wakes) != 3 || wakes[2].Key != "base_red@badc0ffe1234567890@1" {
		t.Fatalf("wakes after red->green->red = %+v, want base_red@badc0ffe1234567890@1", wakes)
	}
}

// TestBaseRedNotBehindStillMerges verifies that a PR not behind the base with
// green checks still merges even while the base is red (it may repair the base).
func TestBaseRedNotBehindStillMerges(t *testing.T) {
	t.Parallel()
	l := newLanding(t)
	writePRJSON(t, l.gh, "view.json", l.c1, greenRollup)
	writeGHFile(t, l.gh, "compare.json", `{"behind_by":0}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":201,"head_sha":"deadbeef1234567890","status":"completed","conclusion":"failure"}]}`)

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
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"deadbeef1234567890"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":301,"head_sha":"deadbeef1234567890","status":"completed","conclusion":"failure"}]}`)

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
		if w.Key == "base_red_held@deadbeef1234567890@1" {
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
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"deadbeef1234567890"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":401,"head_sha":"deadbeef1234567890","status":"completed","conclusion":"failure"}]}`)
	l.pass()
	writeGHFile(t, l.gh, "base_runs_fail", "")
	l.pass()
	wakes := l.state().Project.Wakes
	if len(wakes) != 1 || wakes[0].Key != "base_red@deadbeef1234567890@1" {
		t.Fatalf("wakes after red then read error = %+v, want only base_red@deadbeef1234567890@1", wakes)
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
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"aaa1111"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":501,"head_sha":"aaa1111","status":"completed","conclusion":"failure"}]}`)
	l.pass()

	// Re-run of 501 is now in progress while a second workflow already passed: must stay red.
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":501,"head_sha":"aaa1111","status":"in_progress","conclusion":""},{"id":502,"head_sha":"aaa1111","status":"completed","conclusion":"success"}]}`)
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
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"redsha1"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":601,"head_sha":"redsha1","status":"completed","conclusion":"failure"}]}`)
	l.pass()

	// Daemon restarts with a brand-new Engine; a second failing push landed on main while down.
	eng2 := newGitTestEngine(t, l.paths, l.client)
	eng2.Bin = l.eng.Bin
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"redsha2"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":602,"head_sha":"redsha2","status":"completed","conclusion":"failure"}]}`)
	eng2.passProject(context.Background(), l.id, false)

	// Must NOT wake a second time in the same red spell, and keeps original spell ID and Since.
	st := l.state()
	if len(st.Project.Wakes) != 1 || st.Project.Base == nil || st.Project.Base.SpellID != "redsha1@1" {
		t.Fatalf("after restart: wakes=%+v base=%+v, want 1 wake and spell redsha1@1", st.Project.Wakes, st.Project.Base)
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
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"redsha1"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":701,"head_sha":"redsha1","status":"completed","conclusion":"failure"}]}`)
	l.pass()

	// Base turns green while compare fails (N36 holds without logging a new row).
	writeGHFile(t, l.gh, "compare_fail", "")
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"grnsha2"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":702,"head_sha":"grnsha2","status":"completed","conclusion":"success"}]}`)
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
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"failsha1"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":801,"head_sha":"failsha1","status":"completed","conclusion":"failure"}]}`)
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
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"samesha1234567"}}`)

	// Spell 1: red at samesha1234567 -> base_red wake, escalate past main_red_minutes.
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":901,"head_sha":"samesha1234567","status":"completed","conclusion":"failure"}]}`)
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
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":901,"head_sha":"samesha1234567","status":"completed","conclusion":"success"}]}`)
	l.pass()

	// Spell 2: a later re-run fails at the same sha -> new base_red wake, and tells again past main_red_minutes.
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":901,"head_sha":"samesha1234567","status":"completed","conclusion":"failure"}]}`)
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
	writeGHFile(t, l.gh, "base-ref.json", `{"object":{"sha":"cascadesha1"}}`)
	writeBaseRuns(t, l.gh, `{"workflow_runs":[{"id":902,"head_sha":"cascadesha1","status":"completed","conclusion":"failure"}]}`)
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
