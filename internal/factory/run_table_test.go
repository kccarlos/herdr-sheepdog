package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// TestWorkerDiedMidTurnSeenAsGone verifies KB-RUN-8 and PR 5a.1's demo: a
// worker killed or exiting mid-turn (state finished, status null) transitions
// through R33 to ended(exited) within one pass, removes the finished terminal,
// logs R33, and feeds S1 as gone rather than working.
func TestWorkerDiedMidTurnSeenAsGone(t *testing.T) {
	for _, reason := range []string{"killed", "exited"} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			srv := hctest.New(t)
			client := hc.NewClient(srv.Path)
			paths := testPaths(t)
			repo, _ := initGitRepoWithRemote(t)
			now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
			eng := newGitTestEngine(t, paths, client)
			eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
			eng.Now = func() time.Time { return now }
			if err := eng.Start(repo, "Goal", 2); err != nil {
				t.Fatal(err)
			}
			id := eng.projectDirs()[0].Name()
			eng.passProject(ctx, id, false) // J7 coordinator
			st, _ := LoadState(paths.State(id))
			st.Project.RunTokenHash = tokenHash("tok-coord")
			if err := SaveState(paths.State(id), st); err != nil {
				t.Fatal(err)
			}
			if err := eng.Report("tok-coord", "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","title":"feat: demo","estimate_lines":50,`+testPlanJSON+`}]}`)); err != nil {
				t.Fatal(err)
			}
			eng.passProject(ctx, id, false) // N11 worktree
			eng.passProject(ctx, id, false) // N23 starts worker (R1 -> starting)
			st, _ = LoadState(paths.State(id))
			worker := st.Tasks[0].Attempts[0].RunID
			if r := st.Tasks[0].Attempts[0].Run; r == nil || r.State != flow.PhaseStarting || r.Address != worker {
				t.Fatalf("launched worker run = %+v, want starting at %s", r, worker)
			}

			// Worker enters working turn (R8).
			srv.SetStatus(worker, &hc.Status{App: "claude-code", State: "working"})
			now = now.Add(5 * time.Second)
			eng.passProject(ctx, id, false)
			st, _ = LoadState(paths.State(id))
			if r := st.Tasks[0].Attempts[0].Run; r == nil || r.State != flow.PhaseWorking {
				t.Fatalf("working worker run = %+v, want working", r)
			}

			// Kill/exit worker mid-turn (hc: state finished, status null).
			if reason == "killed" {
				if _, err := client.Kill(ctx, worker); err != nil {
					t.Fatal(err)
				}
			} else {
				srv.Finish(worker, reason)
			}
			now = now.Add(5 * time.Second)
			eng.passProject(ctx, id, false)

			st, _ = LoadState(paths.State(id))
			run := st.Tasks[0].Attempts[0].Run
			if run == nil || run.State != flow.PhaseEnded || run.Result != flow.EndExited || run.Reason != reason || run.TaskRun().State != flow.RunGone {
				t.Fatalf("after mid-turn %s: run = %+v, taskRun = %+v", reason, run, run.TaskRun())
			}
			if _, err := client.Get(ctx, worker); !errors.Is(err, hc.ErrNotFound) {
				t.Fatalf("finished worker terminal not removed by R33: err = %v", err)
			}
			r33 := logRules(t, paths.Log(id), "R33")
			if len(r33) != 1 || r33[0].Decision != "ended(exited)" || !strings.Contains(r33[0].Why, reason) {
				t.Fatalf("R33 log = %+v, want 1 ended(exited) naming %s", r33, reason)
			}
			if st.Tasks[0].LastRow != "N23" || st.Tasks[0].Attempts[0].RunID != "" || st.Tasks[0].Attempts[0].Restarts != 0 {
				t.Fatalf("after mid-turn %s: row=%s runID=%q restarts=%d, want N23 waiting on backoff", reason, st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].RunID, st.Tasks[0].Attempts[0].Restarts)
			}
		})
	}
}

// TestStartTrustScreenToldApartFromBlank verifies KB-RUN-28, KB-RUN-43 and
// review round 1 item 4 end to end: an unverified Claude trust or bypass-mode
// dialog is recorded on state.json (R6) without sending keys, an Agy permission
// block gets R15 (blocked), an Agy trust dialog gets R3 (enter once), and a
// blank terminal gets ScreenNone; at timeout, dialogs/permission end as stuck
// while blank ends as blank.
func TestStartTrustScreenToldApartFromBlank(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	now := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	eng := newGitTestEngine(t, paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Goal", 4); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false) // coordinator
	st, _ := LoadState(paths.State(id))
	srv.SetStatus(st.Project.RunID, &hc.Status{App: "claude-code", State: "working"})
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	plan := `{"tasks":[
		{"id":"T1","title":"feat: claude-trust","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T2","title":"feat: agy-blocked","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T3","title":"feat: blank","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T4","title":"feat: agy-trust","estimate_lines":50,` + testPlanJSON + `}
	]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11
	eng.passProject(ctx, id, false) // N23 starts T1..T4
	st, _ = LoadState(paths.State(id))
	st.Settings.WorkerRestarts = 0
	st.Tasks[3].Attempts[0].Run.CLI = "agy"
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	wClaude := st.Tasks[0].Attempts[0].RunID
	wAgyBlocked := st.Tasks[1].Attempts[0].RunID
	wBlank := st.Tasks[2].Attempts[0].RunID
	wAgyTrust := st.Tasks[3].Attempts[0].RunID

	srv.SetScreen(wClaude, "Quick safety check: Is this a project you created or one you trust?\n❯ No, exit\n  Yes, I trust this folder\n")
	perm := "permission"
	srv.SetStatus(wAgyBlocked, &hc.Status{App: "agy", State: "blocked", Kind: &perm, Msg: "Trust this folder?"})
	srv.SetScreen(wBlank, "")
	srv.SetScreen(wAgyTrust, readScreen(t, "agy-trust-dialog.screen.txt"))

	reqsBefore := len(srv.Requests())
	now = now.Add(10 * time.Second)
	eng.passProject(ctx, id, false)

	// Only wAgyTrust (verified default "Yes") receives terminal.keys ("enter"); wClaude stays unanswered (R6).
	var keyTargets []string
	for _, r := range srv.Requests()[reqsBefore:] {
		if r.Method == "terminal.keys" {
			keyTargets = append(keyTargets, string(r.Params))
		}
	}
	if len(keyTargets) != 1 || !strings.Contains(keyTargets[0], wAgyTrust) || !strings.Contains(keyTargets[0], `"enter"`) {
		t.Fatalf("terminal.keys calls = %v, want 1 enter to %s only", keyTargets, wAgyTrust)
	}
	st, _ = LoadState(paths.State(id))
	rClaude, rAgyBlocked, rBlank, rAgyTrust := st.Tasks[0].Attempts[0].Run, st.Tasks[1].Attempts[0].Run, st.Tasks[2].Attempts[0].Run, st.Tasks[3].Attempts[0].Run
	if rClaude.State != flow.PhaseStarting || rClaude.Screen != flow.ScreenTrust || len(rClaude.ScreensAnswered) != 0 {
		t.Fatalf("claude trust worker = %+v", rClaude)
	}
	if rAgyBlocked.State != flow.PhaseBlocked || rAgyBlocked.BlockedKind != "permission" {
		t.Fatalf("agy blocked worker = %+v", rAgyBlocked)
	}
	if rBlank.State != flow.PhaseStarting || rBlank.Screen != flow.ScreenNone || len(rBlank.ScreensAnswered) != 0 {
		t.Fatalf("blank worker = %+v", rBlank)
	}
	if rAgyTrust.State != flow.PhaseStarting || rAgyTrust.Screen != flow.ScreenTrust || len(rAgyTrust.ScreensAnswered) != 1 {
		t.Fatalf("agy trust worker = %+v", rAgyTrust)
	}

	// Past start_minutes: stuck dialogs end as stuck, blank ends as blank; past blocked_minutes: agy blocked ends as stuck.
	now = now.Add(time.Duration(st.Settings.StartMinutes) * time.Minute)
	eng.passProject(ctx, id, false)
	now = now.Add(time.Duration(st.Settings.BlockedMinutes) * time.Minute)
	eng.passProject(ctx, id, false)

	st, _ = LoadState(paths.State(id))
	if got := st.Tasks[0].Attempts[0].Run; got.State != flow.PhaseEnded || got.Result != flow.EndStuck {
		t.Fatalf("stuck claude trust at start_minutes = %+v, want ended(stuck)", got)
	}
	if got := st.Tasks[1].Attempts[0].Run; got.State != flow.PhaseEnded || got.Result != flow.EndStuck {
		t.Fatalf("agy blocked at blocked_minutes = %+v, want ended(stuck)", got)
	}
	if got := st.Tasks[2].Attempts[0].Run; got.State != flow.PhaseEnded || got.Result != flow.EndBlank {
		t.Fatalf("blank at start_minutes = %+v, want ended(blank)", got)
	}
	if got := st.Tasks[3].Attempts[0].Run; got.State != flow.PhaseEnded || got.Result != flow.EndStuck {
		t.Fatalf("stuck agy trust at start_minutes = %+v, want ended(stuck)", got)
	}
}

// TestRunStateSurvivesRestart checks that a live run's persisted RunRecord in
// state.json survives an Engine restart without resetting its state or clocks.
func TestRunStateSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

	eng1 := newGitTestEngine(t, paths, client)
	eng1.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng1.Now = func() time.Time { return now }
	if err := eng1.Start(repo, "Goal", 1); err != nil {
		t.Fatal(err)
	}
	id := eng1.projectDirs()[0].Name()
	eng1.passProject(ctx, id, false)
	st, _ := LoadState(paths.State(id))
	srv.SetStatus(st.Project.RunID, &hc.Status{App: "claude-code", State: "working"})
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	if err := eng1.Report("tok-coord", "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","title":"feat: persist","estimate_lines":50,`+testPlanJSON+`}]}`)); err != nil {
		t.Fatal(err)
	}
	eng1.passProject(ctx, id, false) // N11
	eng1.passProject(ctx, id, false) // N23 starts worker at t=0
	st, _ = LoadState(paths.State(id))
	worker := st.Tasks[0].Attempts[0].RunID
	startedAt := st.Tasks[0].Attempts[0].Run.StartedAt
	runID := st.Tasks[0].Attempts[0].Run.ID

	// Worker works, then goes idle past grace (R20).
	srv.SetStatus(worker, &hc.Status{App: "claude-code", State: "working"})
	now = now.Add(10 * time.Second)
	eng1.passProject(ctx, id, false)
	srv.SetStatus(worker, &hc.Status{App: "claude-code", State: "idle"})
	now = now.Add(5 * time.Second)
	eng1.passProject(ctx, id, false) // R17 records StatusSince
	now = now.Add(25 * time.Second)
	eng1.passProject(ctx, id, false) // R20 -> idle
	stBefore, _ := LoadState(paths.State(id))
	turnEnd := stBefore.Tasks[0].Attempts[0].Run.LastTurnEnd
	if stBefore.Tasks[0].Attempts[0].Run.State != flow.PhaseIdle || turnEnd.IsZero() {
		t.Fatalf("before restart run = %+v, want idle with LastTurnEnd", stBefore.Tasks[0].Attempts[0].Run)
	}

	// Fresh Engine instance after daemon restart: live idle run stays idle with same ID, StartedAt and LastTurnEnd.
	now = now.Add(3 * time.Minute)
	eng2 := newGitTestEngine(t, paths, client)
	eng2.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng2.Now = func() time.Time { return now }
	eng2.passProject(ctx, id, true)

	stAfter, _ := LoadState(paths.State(id))
	got := stAfter.Tasks[0].Attempts[0].Run
	if got == nil || got.ID != runID || !got.StartedAt.Equal(startedAt) || got.State != flow.PhaseIdle || !got.LastTurnEnd.Equal(turnEnd) {
		t.Fatalf("after restart run = %+v, want preserved ID=%s StartedAt=%v State=idle LastTurnEnd=%v", got, runID, startedAt, turnEnd)
	}
}

// TestIdleRunWithLimitProseStaysIdle checks review round 1 item 1: an idle run
// whose output above the bottom UI region contains "usage limit" and "quota
// exceeded" transitions to idle (R20) and is not stopped as ended(quota) (R19).
func TestIdleRunWithLimitProseStaysIdle(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	now := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	eng := newGitTestEngine(t, paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Goal", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false)
	st, _ := LoadState(paths.State(id))
	srv.SetStatus(st.Project.RunID, &hc.Status{App: "claude-code", State: "working"})
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","title":"feat: prose","estimate_lines":50,`+testPlanJSON+`}]}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11
	eng.passProject(ctx, id, false) // N23 launches worker
	st, _ = LoadState(paths.State(id))
	worker := st.Tasks[0].Attempts[0].RunID

	screen := "⏺ Fixed usage limit and quota exceeded handling; tests for You've hit your limit pass.\n" +
		strings.Repeat("  ok  internal/workerlog 0.01s\n", 15) +
		"────────────────────────────────────────\n❯ \n────────────────────────────────────────\n  ⏵⏵ bypass permissions on\n"
	srv.SetScreen(worker, screen)
	srv.SetStatus(worker, &hc.Status{App: "claude-code", State: "done"})
	now = now.Add(5 * time.Second)
	eng.passProject(ctx, id, false) // R9 -> working
	now = now.Add(25 * time.Second)
	eng.passProject(ctx, id, false) // past idle_grace_seconds -> R20 idle, NOT R19 ended(quota)

	st, _ = LoadState(paths.State(id))
	got := st.Tasks[0].Attempts[0].Run
	if got == nil || got.State != flow.PhaseIdle || got.Result != "" {
		t.Fatalf("idle worker with limit prose = %+v, want idle (not stopped by R19)", got)
	}
	if _, err := client.Get(ctx, worker); err != nil {
		t.Fatalf("worker terminal was stopped: %v", err)
	}
}

// TestQuietRunInterruptedOnceThenEndedStuck checks review round 1 item 3:
// ticking timers/spinners do not count as activity, R21 interrupts once at
// quiet_minutes, R21's own interrupt screen change does not reset activity, and
// R22 ends the run as ended(stuck) after interrupt_seconds.
func TestQuietRunInterruptedOnceThenEndedStuck(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	eng := newGitTestEngine(t, paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Goal", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	eng.passProject(ctx, id, false)
	st, _ := LoadState(paths.State(id))
	srv.SetStatus(st.Project.RunID, &hc.Status{App: "claude-code", State: "idle"})
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(`{"tasks":[{"id":"T1","title":"feat: quiet","estimate_lines":50,`+testPlanJSON+`}]}`)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11
	eng.passProject(ctx, id, false) // N23 launches worker at t=0
	st, _ = LoadState(paths.State(id))
	worker := st.Tasks[0].Attempts[0].RunID

	srv.SetStatus(worker, &hc.Status{App: "claude-code", State: "working"})
	srv.SetScreen(worker, "✢ Thinking… (12s · esc to interrupt)\n")
	now = now.Add(5 * time.Second)
	eng.passProject(ctx, id, false) // R8 -> working, LastActivity = t+5s

	// Spinner and timer tick across 59s -> 1m 05s -> 19m 50s without real output.
	srv.SetScreen(worker, "✻ Thinking… (59s · esc to interrupt)\n")
	now = now.Add(10 * time.Minute)
	eng.passProject(ctx, id, false)
	srv.SetScreen(worker, "✽ Thinking… (19m 50s · esc to interrupt)\n")
	now = now.Add(10 * time.Minute) // 20m since R8 -> R21 interrupts once
	reqsBefore := len(srv.Requests())
	eng.passProject(ctx, id, false)

	interrupts := 0
	for _, r := range srv.Requests()[reqsBefore:] {
		if r.Method == "terminal.interrupt" && strings.Contains(string(r.Params), worker) {
			interrupts++
		}
	}
	if interrupts != 1 {
		t.Fatalf("interrupts at quiet_minutes = %d, want 1", interrupts)
	}

	// The CLI redraws its screen in response to R21's interrupt.
	srv.SetScreen(worker, "■ Conversation interrupted - tell the model what to do differently.\n")
	now = now.Add(15 * time.Second)
	eng.passProject(ctx, id, false)
	now = now.Add(75 * time.Second) // 90s = interrupt_seconds after R21 -> R22 ended(stuck)
	eng.passProject(ctx, id, false)

	st, _ = LoadState(paths.State(id))
	got := st.Tasks[0].Attempts[0].Run
	if got == nil || got.State != flow.PhaseEnded || got.Result != flow.EndStuck {
		t.Fatalf("after interrupt_seconds run = %+v, want ended(stuck) via R22", got)
	}
	if r22 := logRules(t, paths.Log(id), "R22"); len(r22) != 1 {
		t.Fatalf("R22 log lines = %+v, want 1", r22)
	}
}
