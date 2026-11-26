package factory

// PR 5b.8 round 2, TDD step 1: failing tests for the carbon and hydrogen
// review findings:
//  1. Owner restart (`Engine.Start`, same or new goal) and owner stop
//     (`Engine.Stop`) preserve active alert episodes without re-logging and
//     keep J5's sign-out hold intact (no launch on unknown sign-in).
//  2. J5 is derived from persisted state (`EndSignedOut` surviving a crash
//     before `ObserveCoordSignIn` writes `agents_down`), and sign-out found
//     by start check or recheck with no live run raises `agents_down` / J5.
//  3. `CheckAgentCLIs` summary (`"agent clis"`) and `StartChecks` (`"start <cli>"`)
//     report unknown sign-in probes (timeout / exec error) as unknown with their
//     reason, never `"not signed in"`, `"no agent CLI is logged in"`, or
//     `"not installed"`.
//  4. `observeNoPoolLocked` counts only tasks needing a worker now (not N29
//     review wait with healthy reviewer pool, or N4 coordinator block), catches
//     unavailable fallback worker CLIs, includes `Passed` tasks needing a
//     worker (N32), and covers `NewGoal` after all tasks ended and `J6` gap on
//     an empty plan.
//  5. Non-J5 `agents_down` triggers (J6 hourly cap, J9 second silent stop, J10
//     second dialog-stuck) raise and clear on a coordinator turn without
//     overwriting J5's sign-out hold.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

// Requirement 1 (Carbon Major 1, Hydrogen Major 1):
// Owner restart (`factory start` again, with same or new goal) and owner stop
// (`factory stop`) keep active alert episodes: no second raise line is logged
// for an alert that is still true, and J5's sign-out hold survives (no launch
// on unknown sign-in).
func TestPR5b8R2_OwnerStartAndStopKeepActiveAlertEpisodes(t *testing.T) {
	t.Run("owner_start_and_stop_preserve_no_pool_episode", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "Owner restart alert test", everyStepRestart)

			t1 := w.now
			w.setPool("claude", &poolWin{2, 4 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w.pass()

			ts1, ok := statusAlert(t, w.paths, w.repo, "no_pool")
			if !ok {
				t.Fatal("expected initial no_pool alert")
			}
			if !ts1.At.IsZero() && !ts1.At.Equal(t1) {
				t.Fatalf("episode 1 At = %v, want %v", ts1.At, t1)
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "no_pool")); got != 1 {
				t.Fatalf("expected 1 raise log for episode 1, got %d", got)
			}

			// Owner runs `factory start` again with the same goal while alert is still true.
			w.advance(time.Minute)
			if err := w.eng.Start(w.repo, "Owner restart alert test", 4); err != nil {
				t.Fatal(err)
			}
			w.pass()
			tsSame, ok := statusAlert(t, w.paths, w.repo, "no_pool")
			if !ok {
				t.Fatal("no_pool disappeared after owner Start with same goal")
			}
			if !tsSame.At.IsZero() && !tsSame.At.Equal(t1) {
				t.Fatalf("no_pool timestamp drifted after Start(same goal): got %v, want %v", tsSame.At, t1)
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "no_pool")); got != 1 {
				t.Fatalf("Start(same goal) logged duplicate raise line: got %d, want 1", got)
			}

			// Owner runs `factory start` with a new goal while alert is still true.
			w.advance(time.Minute)
			if err := w.eng.Start(w.repo, "Updated goal while blocked", 4); err != nil {
				t.Fatal(err)
			}
			w.pass()
			tsNew, ok := statusAlert(t, w.paths, w.repo, "no_pool")
			if !ok {
				t.Fatal("no_pool disappeared after owner Start with new goal")
			}
			if !tsNew.At.IsZero() && !tsNew.At.Equal(t1) {
				t.Fatalf("no_pool timestamp drifted after Start(new goal): got %v, want %v", tsNew.At, t1)
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "no_pool")); got != 1 {
				t.Fatalf("Start(new goal) logged duplicate raise line: got %d, want 1", got)
			}

			// Owner runs `factory stop`: persisted alert stays visible while stopped,
			// and restarting with `factory start` preserves episode 1 without re-logging.
			if err := w.eng.Stop(w.repo); err != nil {
				t.Fatal(err)
			}
			tsStopped, ok := statusAlert(t, w.paths, w.repo, "no_pool")
			if !ok {
				t.Fatal("Stop wiped active no_pool alert episode")
			}
			if !tsStopped.At.IsZero() && !tsStopped.At.Equal(t1) {
				t.Fatalf("no_pool timestamp drifted on Stop: got %v, want %v", tsStopped.At, t1)
			}
			w.advance(time.Minute)
			if err := w.eng.Start(w.repo, "Restart after stop", 4); err != nil {
				t.Fatal(err)
			}
			w.pass()
			tsAfterStop, ok := statusAlert(t, w.paths, w.repo, "no_pool")
			if !ok {
				t.Fatal("no_pool disappeared after Stop -> Start")
			}
			if !tsAfterStop.At.IsZero() && !tsAfterStop.At.Equal(t1) {
				t.Fatalf("no_pool timestamp drifted after Stop -> Start: got %v, want %v", tsAfterStop.At, t1)
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "no_pool")); got != 1 {
				t.Fatalf("Stop -> Start logged duplicate raise line: got %d, want 1", got)
			}

			// Clear evidence removes the preserved alert.
			w.advance(5 * time.Hour)
			w.setPool("claude", &poolWin{80, 4 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w.pass()
			if ts, still := statusAlert(t, w.paths, w.repo, "no_pool"); still {
				t.Fatalf("no_pool did not clear when pool recovered: %+v", ts)
			}
		})
	})

	t.Run("owner_start_and_stop_preserve_j5_signout_hold", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "Goal one", everyStepRestart)
			w.edit(func(s *flow.State) {
				s.Settings.Models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "high"}},
					flow.RoleWorker:      {{CLI: "claude", Model: "sonnet", Effort: "medium"}},
				}
				s.Tasks = []flow.Task{{
					ID: "T1", Title: "feat: parked task", Brief: "b", Parked: true, EstimateLines: 10,
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
				}}
			})

			w.pass() // J7 starts Claude coordinator #1
			coord1 := w.state().Project.RunID
			if coord1 == "" {
				t.Fatal("expected initial coordinator launch")
			}
			w.setClaudeProbe(scriptedProbe{signedIn: false})
			w.srv.SetScreen(coord1, "Not logged in · Please run /login\n")
			w.pass() // R4 -> EndSignedOut -> J5 raises agents_down

			if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
				t.Fatal("expected agents_down alert on coordinator sign-out")
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "agents_down")); got != 1 {
				t.Fatalf("expected 1 agents_down raise log, got %d", got)
			}

			// Sign-in probe is now unknown (timed out): owner Start with a new goal
			// must preserve J5's hold and must NOT launch on an unknown check.
			w.setClaudeProbe(scriptedProbe{timedOut: true})
			w.advance(time.Duration(w.state().Settings.CoordinatorGapMinutes)*time.Minute + time.Minute)
			if err := w.eng.Start(w.repo, "Updated goal during sign-out", 4); err != nil {
				t.Fatal(err)
			}
			w.pass()
			if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
				t.Fatal("owner Start(new goal) wiped J5 agents_down alert")
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
				t.Fatalf("owner Start allowed coordinator launch on unknown sign-in during J5 hold: %d runs", got)
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "agents_down")); got != 1 {
				t.Fatalf("owner Start repeated agents_down log: got %d, want 1", got)
			}

			// Owner Stop -> Start must also keep J5's sign-out hold intact.
			if err := w.eng.Stop(w.repo); err != nil {
				t.Fatal(err)
			}
			if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
				t.Fatal("owner Stop wiped J5 agents_down alert")
			}
			if err := w.eng.Start(w.repo, "Goal after stop", 4); err != nil {
				t.Fatal(err)
			}
			w.advance(time.Duration(w.state().Settings.PoolMinutes)*time.Minute + time.Minute)
			w.pass()
			if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
				t.Fatal("Stop -> Start lost J5 agents_down alert")
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
				t.Fatalf("Stop -> Start launched coordinator on unknown sign-in during J5 hold: %d runs", got)
			}

			// Positive sign-in check clears J5 and starts coordinator #2.
			w.setClaudeProbe(scriptedProbe{signedIn: true})
			w.advance(time.Duration(w.state().Settings.PoolMinutes)*time.Minute + time.Minute)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("positive sign-in check did not clear J5 after Start/Stop: %+v", ts)
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 2 {
				t.Fatalf("coordinator did not start after positive sign-in check: %d runs", got)
			}
		})
	})
}

// Requirement 2 (Carbon Major 2, Hydrogen Minor 7):
// J5 is derived from persisted state, not an in-memory edge flag:
//   - Crash and restart right after `EndSignedOut` was persisted (before
//     `agents_down` was written) still holds the coordinator role until a
//     positive check.
//   - A sign-out discovered by the start check (`Run == nil` or after a normal
//     `EndExited`) or a recheck with no live run also raises `agents_down` / J5.
func TestPR5b8R2_J5DerivedFromPersistedStateAndSignOutDiscovery(t *testing.T) {
	t.Run("crash_and_restart_after_end_signed_out_persisted", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "Crash right after EndSignedOut", everyStepRestart)
			w.edit(func(s *flow.State) {
				s.Settings.Models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "high"}},
				}
			})

			w.pass() // J7 starts coordinator #1
			coord1 := w.state().Project.RunID
			if coord1 == "" {
				t.Fatal("expected initial coordinator")
			}
			// Simulate the crash window after observeCoordLocked persisted
			// Run={PhaseEnded, EndSignedOut} to state.json and before
			// observeCoordSignInLocked persisted Alerts[agents_down].
			w.srv.FinishExit(coord1, "exited", 0)
			w.edit(func(s *flow.State) {
				s.Project.RunID = ""
				s.Project.RunTokenHash = ""
				s.Project.Run = &flow.RunRecord{
					ID:      "run-coord-1",
					Role:    flow.RoleCoordinator,
					CLI:     "claude",
					Model:   "opus",
					State:   flow.PhaseEnded,
					Result:  flow.EndSignedOut,
					EndedAt: w.now,
				}
				s.Project.Alerts = nil
			})
			w.restart()

			// On restart, claude sign-in probe times out (unknown): J5 must still
			// hold from persisted EndSignedOut and raise agents_down without launching.
			w.setClaudeProbe(scriptedProbe{timedOut: true})
			w.advance(time.Duration(w.state().Settings.CoordinatorGapMinutes)*time.Minute + time.Minute)
			w.pass()
			if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
				t.Fatal("crash/restart after persisted EndSignedOut failed to raise agents_down / J5")
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
				t.Fatalf("launched coordinator on unknown probe after EndSignedOut crash: %d runs", got)
			}

			// Scheduled recheck while still unknown keeps holding.
			w.advance(time.Duration(w.state().Settings.PoolMinutes)*time.Minute + time.Minute)
			w.pass()
			if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
				t.Fatalf("recheck launched coordinator while sign-in remained unknown: %d runs", got)
			}

			// Positive sign-in check clears J5 and launches coordinator #2.
			w.setClaudeProbe(scriptedProbe{signedIn: true})
			w.advance(time.Duration(w.state().Settings.PoolMinutes)*time.Minute + time.Minute)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("positive sign-in check did not clear agents_down: %+v", ts)
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 2 {
				t.Fatalf("coordinator did not launch after positive sign-in check: %d runs", got)
			}
		})
	})

	t.Run("start_check_signed_out_single_candidate_raises_j5", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "Signed out before first start", everyStepRestart)
			w.edit(func(s *flow.State) {
				s.Settings.Models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "high"}},
				}
			})
			w.setClaudeProbe(scriptedProbe{signedIn: false})

			w.pass() // start check finds claude signed out (no live run ever started)
			if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
				t.Fatal("start check discovering signed-out coordinator failed to raise agents_down / J5")
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 0 {
				t.Fatalf("signed-out coordinator launched: %d runs", got)
			}

			// Subsequent unknown (timed out) probe must stay held by J5, never launch.
			w.setClaudeProbe(scriptedProbe{timedOut: true})
			w.advance(time.Duration(w.state().Settings.PoolMinutes)*time.Minute + time.Minute)
			w.pass()
			if got := w.roleRunCount(flow.RoleCoordinator); got != 0 {
				t.Fatalf("J5 failed to hold across unknown probe after start-check sign-out: %d runs", got)
			}

			// Positive sign-in check clears J5 and launches coordinator #1.
			w.setClaudeProbe(scriptedProbe{signedIn: true})
			w.advance(time.Duration(w.state().Settings.PoolMinutes)*time.Minute + time.Minute)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("positive check did not clear agents_down: %+v", ts)
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
				t.Fatalf("coordinator did not launch after positive sign-in check: %d runs", got)
			}
		})
	})

	t.Run("start_check_signed_out_with_unknown_fallback_holds_j5", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "Signed out primary with unknown fallback", everyStepRestart)
			w.edit(func(s *flow.State) {
				s.Settings.Models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {
						{CLI: "claude", Model: "opus", Effort: "high"},
						{CLI: "codex", Model: codexModel, Effort: "high"},
					},
				}
			})
			w.setClaudeProbe(scriptedProbe{signedIn: false})
			w.setCodexProbe(scriptedProbe{timedOut: true})

			w.pass()
			if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
				t.Fatal("expected agents_down when primary is signed out and fallback sign-in is unknown")
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 0 {
				t.Fatalf("launched fallback coordinator on an unknown sign-in check: %d runs", got)
			}

			// Positive check on configured fallback candidate launches Codex coordinator.
			w.setCodexProbe(scriptedProbe{signedIn: true})
			w.advance(time.Duration(w.state().Settings.PoolMinutes)*time.Minute + time.Minute)
			w.pass()
			if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
				t.Fatalf("configured fallback with positive check did not launch: %d runs", got)
			}
		})
	})

	t.Run("recheck_after_normal_exit_when_signed_out_raises_j5", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "Signed out after EndExited", everyStepRestart)
			w.edit(func(s *flow.State) {
				s.Settings.Models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "high"}},
				}
			})

			w.pass() // starts coordinator #1
			coord1 := w.makeCoordinatorIdle()
			w.srv.FinishExit(coord1, "exited", 0)
			w.pass() // EndExited (not EndSignedOut)

			// Sign out while waiting with no live run.
			w.setClaudeProbe(scriptedProbe{signedIn: false})
			w.advance(time.Duration(w.state().Settings.CoordinatorGapMinutes)*time.Minute + time.Minute)
			w.pass() // start check / recheck with no live run sees signed out
			if _, ok := statusAlert(t, w.paths, w.repo, "agents_down"); !ok {
				t.Fatal("recheck / start check after EndExited failed to raise agents_down when signed out")
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
				t.Fatalf("coordinator launched while signed out: %d runs", got)
			}

			// Unknown probe stays held by J5.
			w.setClaudeProbe(scriptedProbe{timedOut: true})
			w.advance(time.Duration(w.state().Settings.PoolMinutes)*time.Minute + time.Minute)
			w.pass()
			if got := w.roleRunCount(flow.RoleCoordinator); got != 1 {
				t.Fatalf("coordinator launched on unknown probe after non-EndSignedOut sign-out: %d runs", got)
			}
		})
	})
}

// Requirement 3 (Carbon Major 3, Hydrogen Major 2):
// `factory doctor` summary (`"agent clis"`) and `factory start` checks
// (`StartChecks`) report an unknown sign-in probe (timeout or failed exec) as
// unknown with its reason, never `"not signed in"`, `"no agent CLI is logged in"`,
// or `"not installed"`.
func TestPR5b8R2_DoctorSummaryAndStartChecksReportUnknownProbe(t *testing.T) {
	ctx := context.Background()
	virtualHome := "/Users/alice"
	tmp := t.TempDir()

	modes := []struct {
		name       string
		out        CommandOutput
		wantSubstr string
	}{
		{name: "timeout", out: CommandOutput{Exit: -1, TimedOut: true}, wantSubstr: "time"},
		{name: "exec_error", out: CommandOutput{Exit: -1, Err: errors.New("hc exec transport closed")}, wantSubstr: "hc exec transport closed"},
	}

	assertUnknownNotNegative := func(t *testing.T, label, modeName, wantSubstr string, r onboard.Result) {
		t.Helper()
		if r.Status == onboard.OK || r.Status == onboard.Skip {
			t.Errorf("%s (%s): unexpected status %v (%+v)", label, modeName, r.Status, r)
		}
		low := strings.ToLower(r.Detail)
		if !strings.Contains(low, "unknown") || !strings.Contains(low, strings.ToLower(wantSubstr)) {
			t.Errorf("%s (%s): detail = %q, want unknown with reason %q", label, modeName, r.Detail, wantSubstr)
		}
		for _, bad := range []string{"not signed in", "not logged in", "no agent cli is logged in", "not installed"} {
			if strings.Contains(low, bad) {
				t.Errorf("%s (%s): reported unknown probe as %q: %+v", label, modeName, bad, r)
			}
		}
	}

	for _, mode := range modes {
		t.Run("doctor_agent_clis_summary_all_unknown_"+mode.name, func(t *testing.T) {
			env := newFakeEnv("claude", "codex", "agy", "gh")
			env.handlers["claude auth"] = func(string, []string, []string) CommandOutput { return mode.out }
			env.handlers["codex login"] = func(string, []string, []string) CommandOutput { return mode.out }
			env.handlers["agy models"] = func(string, []string, []string) CommandOutput { return mode.out }

			opts := DoctorOptions{
				Folder:      filepath.Join(virtualHome, "Documents", "demo"),
				GOOS:        "darwin",
				Home:        virtualHome,
				TempDir:     tmp,
				LookPath:    env.lookPath,
				RunCommand:  env.run,
				AuthTimeout: time.Second,
			}
			cliResults := CheckAgentCLIs(ctx, opts, false)
			gate := findResult(t, cliResults, "agent clis")
			assertUnknownNotNegative(t, "agent clis", mode.name, mode.wantSubstr, gate)
		})

		t.Run("doctor_agent_clis_summary_mixed_signed_out_and_unknown_"+mode.name, func(t *testing.T) {
			env := newFakeEnv("claude", "codex")
			env.handlers["claude auth"] = func(string, []string, []string) CommandOutput {
				return CommandOutput{Stdout: `{"loggedIn":false}`, Exit: 1}
			}
			env.handlers["codex login"] = func(string, []string, []string) CommandOutput { return mode.out }

			opts := DoctorOptions{
				Folder:      filepath.Join(virtualHome, "Documents", "demo"),
				GOOS:        "darwin",
				Home:        virtualHome,
				TempDir:     tmp,
				LookPath:    env.lookPath,
				RunCommand:  env.run,
				AuthTimeout: time.Second,
			}
			cliResults := CheckAgentCLIs(ctx, opts, false)
			gate := findResult(t, cliResults, "agent clis")
			assertUnknownNotNegative(t, "agent clis (mixed)", mode.name, mode.wantSubstr, gate)
		})

		t.Run("start_checks_cli_unknown_"+mode.name, func(t *testing.T) {
			dataDir := t.TempDir()
			root := t.TempDir()
			st := flow.State{
				Project:  flow.Project{ID: "p1", Folder: root},
				Settings: flow.DefaultSettings(),
			}
			st.Settings.SecondReviewer = "codex"
			if err := SaveState(Paths{DataDir: dataDir}.State("p1"), st); err != nil {
				t.Fatal(err)
			}

			f := startFake{
				"git --version":                                             out("git version 2.45.0\n"),
				"rev-parse --show-toplevel":                                 out(root),
				"rev-parse --verify HEAD":                                   out("abc1234\n"),
				"remote get-url --all origin":                               out("https://github.com/o/r.git"),
				"remote get-url --push --all origin":                        out("https://github.com/o/r.git"),
				"refs/remotes/origin/HEAD":                                  out("origin/main"),
				"rev-parse --verify --quiet origin/main":                    out("abc1234\n"),
				"claude auth status --json":                                 mode.out,
				"codex login status":                                        mode.out,
				"ls-tree --name-only origin/main .github/workflows/":        out(".github/workflows/ci.yml"),
				"show origin/main:.github/workflows/ci.yml":                 out(goodWorkflow),
				"gh api repos/o/r/branches/main":                            out(`{"protected":false}`),
				"gh repo view o/r --json defaultBranchRef,viewerPermission": out(repoView),
				"gh api repos/o/r/branches/main/protection":                 {Exit: 1, Stderr: "HTTP 404: Branch not protected"},
				"gh api repos/o/r/rules/branches/main":                      out("[]"),
				"fetch --prune origin":                                      out(""),
			}
			rs := StartChecks(ctx, DoctorOptions{Folder: root, DataDir: dataDir, RunCommand: f.run})
			assertUnknownNotNegative(t, "start claude", mode.name, mode.wantSubstr, findResult(t, rs, "start claude"))
			assertUnknownNotNegative(t, "start codex", mode.name, mode.wantSubstr, findResult(t, rs, "start codex"))
		})
	}
}

// Requirement 4 & Carbon Major 4:
// `no_pool` counts only tasks that need a worker now:
//   - A task waiting at N29 for a review with a usable reviewer pool does NOT
//     raise `no_pool` even when the worker pool is at the floor, and N29 launches.
//   - A task blocked at N4 (PR closed) with an ended worker does NOT raise `no_pool`.
//   - A `Passed` task that needs a worker now (N32 merge conflict) DOES raise `no_pool`
//     when no worker candidate is usable.
//   - An unavailable fallback worker CLI (signed out) does NOT suppress `no_pool`.
//   - A `NewGoal` after all prior tasks ended, and an empty plan during J6 gap,
//     still raise `no_pool` when no coordinator candidate is usable.
func TestPR5b8R2_NoPoolCountsOnlyTasksNeedingWorkerNow(t *testing.T) {
	t.Run("n29_review_wait_with_usable_reviewer_does_not_raise_no_pool", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "N29 review with worker floor", everyStepRestart)
			w.pass() // start coordinator
			w.makeCoordinatorIdle()

			wt, err := fixtureGit(t).NewWorktree(w.ctx, w.repo, w.paths.Worktree(w.id, "T1", 1), "T1-1", "feat: T1")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wt.Path, "t1.go"), []byte("package demo\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitIn(t, wt.Path, "add", "t1.go")
			gitIn(t, wt.Path, "commit", "-m", "feat: T1")
			head := gitIn(t, wt.Path, "rev-parse", "HEAD")
			gitIn(t, wt.Path, "push", "origin", head+":refs/heads/"+wt.Branch)
			writePRJSON(t, w.gh, "view.json", head, greenRollup)

			w.edit(func(s *flow.State) {
				s.Settings.SecondReviewer = "codex"
				s.Settings.Models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "codex", Model: codexModel, Effort: "high"}},
					flow.RoleWorker:      {{CLI: "claude", Model: "sonnet", Effort: "medium"}},
				}
				s.Tasks = []flow.Task{{
					ID: "T1", Title: "feat: review waiting", Brief: "b", EstimateLines: 20,
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
					Attempts: []flow.Attempt{{
						SizeCap: 100, BaseCommit: w.baseSHA, Branch: wt.Branch, Worktree: wt.Path, PR: 7,
						Run:   &flow.RunRecord{Role: flow.RoleWorker, CLI: "claude", Model: "sonnet", State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: w.now.Add(-5 * time.Minute)},
						Ready: []flow.Ready{{Head: head}},
						Rounds: []flow.Round{
							{N: 1, Kind: flow.RoundFull, Head: head, Reviewer: flow.Reviewer{CLI: "claude", Model: "opus", Effort: "high"}, Verdict: flow.VerdictPass},
						},
					}},
				}}
			})

			w.setPool("claude", &poolWin{2, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w.setPool("codex", &poolWin{80, 3 * time.Hour}, &poolWin{80, 72 * time.Hour})

			w.pass()
			w.pass()

			if ts, active := statusAlert(t, w.paths, w.repo, "no_pool"); active {
				t.Fatalf("N29 task waiting on review with healthy Codex reviewer falsely raised no_pool: %+v", ts)
			}
			st := w.state()
			if w.roleRunCount(flow.RoleReviewer) != 1 || st.Tasks[0].Attempts[0].Review == nil || st.Tasks[0].Attempts[0].Review.RunID == "" {
				t.Fatalf("N29 reviewer did not launch while worker pool was at floor: reviewers=%d review=%+v", w.roleRunCount(flow.RoleReviewer), st.Tasks[0].Attempts[0].Review)
			}
		})
	})

	t.Run("n4_closed_pr_with_ended_worker_does_not_raise_no_pool", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "N4 blocked task", everyStepRestart)
			w.pass() // start coordinator
			w.makeCoordinatorIdle()

			wt, err := fixtureGit(t).NewWorktree(w.ctx, w.repo, w.paths.Worktree(w.id, "T1", 1), "T1-1", "feat: T1")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wt.Path, "t1.go"), []byte("package demo\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitIn(t, wt.Path, "add", "t1.go")
			gitIn(t, wt.Path, "commit", "-m", "feat: T1")
			head := gitIn(t, wt.Path, "rev-parse", "HEAD")
			gitIn(t, wt.Path, "push", "origin", head+":refs/heads/"+wt.Branch)
			writeGHFile(t, w.gh, "view.json", fmt.Sprintf(`{"number":7,"state":"CLOSED","headRefOid":%q,"mergeable":"MERGEABLE","statusCheckRollup":%s,"baseRefName":"main"}`, head, greenRollup))

			w.edit(func(s *flow.State) {
				s.Settings.Models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "codex", Model: codexModel, Effort: "high"}},
					flow.RoleWorker:      {{CLI: "claude", Model: "sonnet", Effort: "medium"}},
				}
				s.Tasks = []flow.Task{{
					ID: "T1", Title: "feat: closed PR", Brief: "b", EstimateLines: 20,
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
					Attempts: []flow.Attempt{{
						SizeCap: 100, BaseCommit: w.baseSHA, Branch: wt.Branch, Worktree: wt.Path, PR: 7,
						Run:   &flow.RunRecord{Role: flow.RoleWorker, CLI: "claude", Model: "sonnet", State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: w.now.Add(-5 * time.Minute)},
						Ready: []flow.Ready{{Head: head}},
					}},
				}}
			})

			w.setPool("claude", &poolWin{2, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w.pass()
			w.pass()

			if ts, active := statusAlert(t, w.paths, w.repo, "no_pool"); active {
				t.Fatalf("N4 closed PR task not needing a worker falsely raised no_pool: %+v", ts)
			}
		})
	})

	t.Run("passed_task_needing_worker_at_n32_raises_no_pool", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "N32 Passed task needing worker", everyStepRestart)
			w.pass() // start coordinator
			w.makeCoordinatorIdle()

			wt, err := fixtureGit(t).NewWorktree(w.ctx, w.repo, w.paths.Worktree(w.id, "T1", 1), "T1-1", "feat: T1")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wt.Path, "t1.go"), []byte("package demo\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			gitIn(t, wt.Path, "add", "t1.go")
			gitIn(t, wt.Path, "commit", "-m", "feat: T1")
			head := gitIn(t, wt.Path, "rev-parse", "HEAD")
			gitIn(t, wt.Path, "push", "origin", head+":refs/heads/"+wt.Branch)
			writeGHFile(t, w.gh, "view.json", fmt.Sprintf(`{"number":7,"state":"OPEN","headRefOid":%q,"mergeable":"CONFLICTING","statusCheckRollup":%s,"baseRefName":"main"}`, head, greenRollup))

			w.edit(func(s *flow.State) {
				s.Settings.Models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "high"}},
					flow.RoleWorker:      {{CLI: "claude", Model: "sonnet", Effort: "medium"}},
				}
				s.Tasks = []flow.Task{{
					ID: "T1", Title: "feat: conflict after pass", Brief: "b", EstimateLines: 20,
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
					Attempts: []flow.Attempt{{
						SizeCap: 100, BaseCommit: w.baseSHA, Branch: wt.Branch, Worktree: wt.Path, PR: 7,
						Run:   &flow.RunRecord{Role: flow.RoleWorker, CLI: "claude", Model: "sonnet", State: flow.PhaseEnded, Result: flow.EndQuota, EndedAt: w.now.Add(-5 * time.Minute)},
						Ready: []flow.Ready{{Head: head}},
						Rounds: []flow.Round{
							{N: 1, Kind: flow.RoundFull, Head: head, Verdict: flow.VerdictPass},
							{N: 2, Kind: flow.RoundFull, Head: head, Verdict: flow.VerdictPass},
						},
					}},
				}}
			})

			w.setPool("claude", &poolWin{2, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w.pass()
			w.pass()

			if _, active := statusAlert(t, w.paths, w.repo, "no_pool"); !active {
				t.Fatal("expected no_pool when Passed task at N32 needs a worker and worker pool is at floor")
			}

			// Recover pool -> no_pool clears and N32 starts worker.
			w.advance(3 * time.Hour)
			w.setPool("claude", &poolWin{80, 4 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w.pass()
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "no_pool"); active {
				t.Fatalf("no_pool did not clear after pool recovered: %+v", ts)
			}
			if w.roleRunCount(flow.RoleWorker) != 1 {
				t.Fatalf("N32 worker did not launch after pool recovery: %d", w.roleRunCount(flow.RoleWorker))
			}
		})
	})

	t.Run("fallback_worker_cli_signed_out_raises_no_pool", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "Signed out fallback worker", everyStepRestart)
			w.pass() // start coordinator
			w.makeCoordinatorIdle()

			w.edit(func(s *flow.State) {
				s.Settings.Models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {{CLI: "claude", Model: "opus", Effort: "high"}},
					flow.RoleWorker: {
						{CLI: "claude", Model: "sonnet", Effort: "medium"},
						{CLI: "codex", Model: codexModel, Effort: "high"},
					},
					flow.RoleReviewer: {
						{CLI: "claude", Model: "opus", Effort: "high"},
						{CLI: "codex", Model: codexModel, Effort: "high"},
					},
				}
				s.Tasks = []flow.Task{{
					ID: "T1", Title: "feat: open task", Brief: "b", EstimateLines: 20,
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
				}}
			})

			w.setPool("claude", &poolWin{2, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w.setCodexProbe(scriptedProbe{signedIn: false})
			w.pass()
			w.pass()

			if _, active := statusAlert(t, w.paths, w.repo, "no_pool"); !active {
				t.Fatal("expected no_pool when primary worker is at floor and only fallback CLI is signed out")
			}

			// Sign in fallback CLI -> clears no_pool and starts worker on codex.
			w.setCodexProbe(scriptedProbe{signedIn: true})
			w.advance(3 * time.Minute)
			w.pass()
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "no_pool"); active {
				t.Fatalf("no_pool did not clear once fallback worker CLI signed in: %+v", ts)
			}
			if w.roleRunCount(flow.RoleWorker) != 1 {
				t.Fatalf("worker did not launch on signed-in fallback CLI: %d", w.roleRunCount(flow.RoleWorker))
			}
		})
	})

	t.Run("new_goal_after_all_tasks_ended_and_j6_gap_on_empty_plan_raise_no_pool", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			// Part 1: All prior tasks ended, owner sets NewGoal, coordinator is gone and pool is at floor.
			w1 := newAlertWorld(t, "Plan one", everyStepRestart)
			w1.pass() // start coord #1
			coord1 := w1.state().Project.RunID
			w1.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{{
					ID: "T1", Title: "feat: done", Brief: "b", EstimateLines: 20,
					Done:     &flow.Merged{MergedCommit: "abc1234"},
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
				}}
			})
			w1.srv.FinishExit(coord1, "exited", 0)
			w1.pass()
			if err := w1.eng.Start(w1.repo, "Plan two goal", 4); err != nil {
				t.Fatal(err)
			}
			w1.setPool("claude", &poolWin{2, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w1.advance(time.Duration(w1.state().Settings.CoordinatorGapMinutes)*time.Minute + time.Minute)
			w1.pass()
			if _, active := statusAlert(t, w1.paths, w1.repo, "no_pool"); !active {
				t.Fatal("expected no_pool when NewGoal awaits coordinator with coordinator pool at floor")
			}

			// Part 2: Empty plan (0 tasks) inside J6 coordinator_gap_minutes with pool at floor.
			w2 := newAlertWorld(t, "Empty plan J6 gap", everyStepRestart)
			w2.pass() // start coord #1 at t=0 (records AskStartCoordinator)
			c1 := w2.state().Project.RunID
			w2.advance(time.Minute)
			w2.srv.FinishExit(c1, "exited", 0)
			w2.setPool("claude", &poolWin{2, 2 * time.Hour}, &poolWin{60, 72 * time.Hour})
			w2.pass() // still inside J6 coordinator_gap_minutes
			if _, active := statusAlert(t, w2.paths, w2.repo, "no_pool"); !active {
				t.Fatal("expected no_pool on 0-task project during J6 gap when coordinator pool is at floor")
			}
		})
	})
}

// Carbon Major 5 (design.md section 6, J6, J9, J10, agents_down):
// Non-J5 coordinator `agents_down` triggers (J6 hourly cap, J9 second silent
// stop, J10 second R29 dialog-stuck) raise `agents_down` once per episode,
// clear when a coordinator run is alive and has taken a turn, and do not
// overwrite an active J5 sign-out hold.
func TestPR5b8R2_NonJ5AgentsDownAndMultiSlotIsolation(t *testing.T) {
	t.Run("j6_hourly_cap_raises_agents_down_and_clears_on_coord_turn", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "J6 hourly cap alert", everyStepRestart)
			w.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{{
					ID: "T1", Title: "feat: parked", Brief: "b", Parked: true, EstimateLines: 10,
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
				}}
			})

			// Start #1 at t=0 -> exit at t=2m (in gap, NOT hourly cap -> no agents_down).
			w.pass()
			w.advance(2 * time.Minute)
			w.srv.FinishExit(w.state().Project.RunID, "exited", 0)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("J6 gap alone falsely raised agents_down: %+v", ts)
			}

			// Start #2 at t=12m -> exit at t=14m.
			w.advance(10 * time.Minute)
			w.pass()
			w.advance(2 * time.Minute)
			w.srv.FinishExit(w.state().Project.RunID, "exited", 0)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("2 starts within hour falsely raised agents_down: %+v", ts)
			}

			// Start #3 at t=24m -> exit at t=26m -> advance past gap (t=36m) so hourly cap holds.
			w.advance(10 * time.Minute)
			w.pass()
			w.advance(2 * time.Minute)
			w.srv.FinishExit(w.state().Project.RunID, "exited", 0)
			w.pass()
			w.advance(10 * time.Minute) // t=36m: gap passed, 3 starts in last hour
			w.pass()

			if _, active := statusAlert(t, w.paths, w.repo, "agents_down"); !active {
				t.Fatal("expected agents_down when J6 coordinator_starts_per_hour cap holds coordinator")
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "agents_down")); got != 1 {
				t.Fatalf("expected 1 agents_down log on J6 hourly cap, got %d", got)
			}

			// Subsequent pass while still capped must not duplicate log line.
			w.advance(10 * time.Minute) // t=46m
			w.pass()
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "agents_down")); got != 1 {
				t.Fatalf("J6 agents_down repeated across passes: got %d, want 1", got)
			}

			// At t=61m, start #1 leaves the 1h window -> J7 starts #4; once #4 completes a turn, agents_down clears.
			w.advance(15 * time.Minute)
			w.pass()
			if w.roleRunCount(flow.RoleCoordinator) != 4 {
				t.Fatalf("expected coordinator #4 to start after hourly cap expired, got %d", w.roleRunCount(flow.RoleCoordinator))
			}
			w.makeCoordinatorIdle()
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("agents_down did not clear after coordinator took a turn: %+v", ts)
			}
		})
	})

	t.Run("j9_second_silent_stop_raises_agents_down_and_clears_on_coord_turn", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "J9 silent streak alert", everyStepRestart)
			w.pass() // start #1
			c1 := w.makeCoordinatorIdle()
			w.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{{
					ID: "T1", Title: "feat: parked", Brief: "b", Parked: true, EstimateLines: 10,
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
				}}
				s.Project.Wakes = []flow.Wake{{Key: "worker_failed", Task: "T1", Why: "N23", Facts: "title x", At: w.now}}
			})
			w.pass() // J12 delivers wake to c1, arms CoordWakeAt

			silentDur := time.Duration(w.state().Settings.CoordinatorSilentMinutes) * time.Minute
			w.advance(silentDur / 2)
			w.srv.SetStatus(c1, &hc.Status{App: "claude-code", State: "idle"})
			w.pass()
			w.advance(silentDur/2 + time.Minute)
			w.pass() // J9 stops c1 (1st silent stop)
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("1st J9 silent stop falsely raised agents_down: %+v", ts)
			}

			// J7 starts c2 (silentDur >= CoordinatorGapMinutes), c2 goes idle -> gets re-queued wake -> stays silent -> 2nd J9 stop.
			w.pass()
			if w.roleRunCount(flow.RoleCoordinator) != 2 {
				t.Fatalf("expected J7 to start coordinator #2 after 1st J9 stop, got %d", w.roleRunCount(flow.RoleCoordinator))
			}
			c2 := w.makeCoordinatorIdle()
			w.pass() // J12 delivers wake to c2
			w.advance(silentDur / 2)
			w.srv.SetStatus(c2, &hc.Status{App: "claude-code", State: "idle"})
			w.pass()
			w.advance(silentDur/2 + time.Minute)
			w.pass() // J9 stops c2 (2nd silent stop in a row)

			if _, active := statusAlert(t, w.paths, w.repo, "agents_down"); !active {
				t.Fatal("expected agents_down on 2nd consecutive J9 silent stop")
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "agents_down")); got != 1 {
				t.Fatalf("expected 1 agents_down log on 2nd J9 stop, got %d", got)
			}

			// J7 starts c3; J12 delivers the wake. While the batch is still in flight, agents_down@j9 stays active.
			w.pass()
			c3 := w.makeCoordinatorIdle()
			sent := lastSendTo(t, w.srv.Requests(), c3)
			w.srv.SetStatus(c3, &hc.Status{App: "claude-code", State: "working"})
			w.advance(5 * time.Second)
			w.pass()
			if _, active := statusAlert(t, w.paths, w.repo, "agents_down"); !active {
				t.Fatal("agents_down@j9 cleared while c3's wake batch was still in flight")
			}
			// Once c3 completes the wake turn with matching evidence, agents_down clears.
			w.srv.SetScreen(c3, completedWakeScreen("claude", sent, "Retried T1."))
			w.srv.SetStatus(c3, &hc.Status{App: "claude-code", State: "idle"})
			w.advance(5 * time.Second)
			w.pass()
			w.advance(25 * time.Second)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("agents_down did not clear after c3 completed its wake turn: %+v", ts)
			}
		})
	})

	t.Run("j10_second_dialog_stuck_raises_agents_down_and_clears_on_coord_turn", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "J10 dialog stuck alert", everyStepRestart)
			w.edit(func(s *flow.State) {
				s.Tasks = []flow.Task{{
					ID: "T1", Title: "feat: parked", Brief: "b", Parked: true, EstimateLines: 10,
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
				}}
			})

			w.pass() // starts c1
			perm := "permission"
			blockedDur := time.Duration(w.state().Settings.BlockedMinutes)*time.Minute + time.Minute

			// 1st R29 dialog stuck -> J10 (1st) + J7 starts c2, no agents_down yet.
			c1 := w.state().Project.RunID
			w.srv.SetStatus(c1, &hc.Status{App: "claude-code", State: "blocked", Kind: &perm, Msg: "Allow tool?"})
			w.advance(5 * time.Second)
			w.pass()
			w.advance(blockedDur)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("1st J10 dialog-stuck falsely raised agents_down: %+v", ts)
			}
			if w.roleRunCount(flow.RoleCoordinator) != 2 {
				t.Fatalf("expected c2 to start after 1st R29, got %d", w.roleRunCount(flow.RoleCoordinator))
			}

			// 2nd R29 dialog stuck in a row -> raises agents_down, J7 starts c3.
			c2 := w.state().Project.RunID
			w.srv.SetStatus(c2, &hc.Status{App: "claude-code", State: "blocked", Kind: &perm, Msg: "Allow tool again?"})
			w.advance(5 * time.Second)
			w.pass()
			w.advance(blockedDur)
			w.pass()
			if _, active := statusAlert(t, w.paths, w.repo, "agents_down"); !active {
				t.Fatal("expected agents_down on 2nd consecutive J10 R29 dialog-stuck end")
			}
			if got := len(alertEpisodeLogs(t, w.paths, w.repo, "agents_down")); got != 1 {
				t.Fatalf("expected 1 agents_down log on 2nd J10, got %d", got)
			}

			// Once c3 is alive and takes a turn, agents_down clears.
			w.makeCoordinatorIdle()
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("agents_down did not clear after c3 completed a turn: %+v", ts)
			}
		})
	})

	t.Run("j6_hourly_cap_does_not_overwrite_j5_signout_hold", func(t *testing.T) {
		runBothModes(t, func(t *testing.T, everyStepRestart bool) {
			w := newAlertWorld(t, "J5 and J6 isolation", everyStepRestart)
			w.edit(func(s *flow.State) {
				s.Settings.Models = map[string][]flow.Candidate{
					flow.RoleCoordinator: {
						{CLI: "claude", Model: "opus", Effort: "high"},
						{CLI: "codex", Model: codexModel, Effort: "high"},
					},
				}
				s.Tasks = []flow.Task{{
					ID: "T1", Title: "feat: parked", Brief: "b", Parked: true, EstimateLines: 10,
					Outcomes: []flow.Outcome{{Text: "ok", ProvedBy: "manual: t1"}},
				}}
			})

			// 3 coordinator starts within 1 hour, and start #3 ends signed_out while
			// codex fallback is positively signed in so ProjectRows reaches J6.
			w.pass() // start #1 at t=0
			w.advance(2 * time.Minute)
			w.srv.FinishExit(w.state().Project.RunID, "exited", 0)
			w.pass()

			w.advance(10 * time.Minute) // start #2 at t=12m
			w.pass()
			w.advance(2 * time.Minute)
			w.srv.FinishExit(w.state().Project.RunID, "exited", 0)
			w.pass()

			w.advance(10 * time.Minute) // start #3 at t=24m
			w.pass()
			c3 := w.state().Project.RunID
			w.advance(2 * time.Minute) // t=26m
			w.setClaudeProbe(scriptedProbe{signedIn: false})
			w.setCodexProbe(scriptedProbe{signedIn: true})
			w.srv.SetScreen(c3, "Not logged in · Please run /login\n")
			w.pass() // R4 EndSignedOut -> J5 hold active; CoordFallbackReady=true -> J6 hourly cap active

			// Now both probes become unknown; advance past J6's 1h cap expiry (t=66m).
			w.setClaudeProbe(scriptedProbe{timedOut: true})
			w.setCodexProbe(scriptedProbe{timedOut: true})
			w.advance(10 * time.Minute) // t=36m (J6 hourly cap active)
			w.pass()
			w.advance(30 * time.Minute) // t=66m (J6 hourly cap expires)
			w.pass()

			if _, active := statusAlert(t, w.paths, w.repo, "agents_down"); !active {
				t.Fatal("J5 sign-out hold was overwritten by J6 hourly cap and lost on cap expiry")
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 3 {
				t.Fatalf("coordinator launched on unknown sign-in after J6 cap expired (J5 hold lost): %d runs", got)
			}

			// Positive check finally clears J5 and starts coordinator #4.
			w.setClaudeProbe(scriptedProbe{signedIn: true})
			w.advance(time.Duration(w.state().Settings.PoolMinutes)*time.Minute + time.Minute)
			w.pass()
			if ts, active := statusAlert(t, w.paths, w.repo, "agents_down"); active {
				t.Fatalf("positive sign-in check did not clear J5: %+v", ts)
			}
			if got := w.roleRunCount(flow.RoleCoordinator); got != 4 {
				t.Fatalf("expected coordinator #4 after positive sign-in check, got %d", got)
			}
		})
	})
}
