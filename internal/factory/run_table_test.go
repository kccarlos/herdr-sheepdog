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
			eng := New(paths, client)
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
			s1 := logRules(t, paths.Log(id), "S1")
			if len(s1) != 1 {
				t.Fatalf("S1 log = %+v, want worker seen as gone in same pass", s1)
			}
		})
	}
}

// TestStartTrustScreenToldApartFromBlank verifies KB-RUN-28 and KB-RUN-43 end
// to end: a worker at a Claude trust dialog gets R3 (enter key), a worker at an
// Agy permission block gets R15 (blocked), and a blank terminal gets neither;
// at timeout, trust/permission end as stuck while blank ends as blank.
func TestStartTrustScreenToldApartFromBlank(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	now := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	eng := New(paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Goal", 3); err != nil {
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
		{"id":"T1","title":"feat: trust","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T2","title":"feat: agy","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T3","title":"feat: blank","estimate_lines":50,` + testPlanJSON + `}
	]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // N11
	eng.passProject(ctx, id, false) // N23 starts T1, T2, T3
	st, _ = LoadState(paths.State(id))
	wTrust := st.Tasks[0].Attempts[0].RunID
	wAgy := st.Tasks[1].Attempts[0].RunID
	wBlank := st.Tasks[2].Attempts[0].RunID

	srv.SetScreen(wTrust, "Is this a project you created or one you trust?\n1. Yes, I trust this folder\n")
	perm := "permission"
	srv.SetStatus(wAgy, &hc.Status{App: "agy", State: "blocked", Kind: &perm, Msg: "Trust this folder?"})
	srv.SetScreen(wBlank, "")

	reqsBefore := len(srv.Requests())
	now = now.Add(10 * time.Second)
	eng.passProject(ctx, id, false)

	// Verify only wTrust received terminal.keys ("enter"), wAgy is blocked, wBlank is starting with ScreenNone.
	var keyTargets []string
	for _, r := range srv.Requests()[reqsBefore:] {
		if r.Method == "terminal.keys" {
			keyTargets = append(keyTargets, string(r.Params))
		}
	}
	if len(keyTargets) != 1 || !strings.Contains(keyTargets[0], wTrust) || !strings.Contains(keyTargets[0], `"enter"`) {
		t.Fatalf("terminal.keys calls = %v, want 1 enter to %s", keyTargets, wTrust)
	}
	st, _ = LoadState(paths.State(id))
	rTrust, rAgy, rBlank := st.Tasks[0].Attempts[0].Run, st.Tasks[1].Attempts[0].Run, st.Tasks[2].Attempts[0].Run
	if rTrust.State != flow.PhaseStarting || rTrust.Screen != flow.ScreenTrust || !rTrust.ScreenAnswered {
		t.Fatalf("trust worker = %+v", rTrust)
	}
	if rAgy.State != flow.PhaseBlocked || rAgy.BlockedKind != "permission" {
		t.Fatalf("agy worker = %+v", rAgy)
	}
	if rBlank.State != flow.PhaseStarting || rBlank.Screen != flow.ScreenNone || rBlank.ScreenAnswered {
		t.Fatalf("blank worker = %+v", rBlank)
	}

	// Past start_minutes: stuck trust ends as stuck, blank ends as blank; past blocked_minutes: agy ends as stuck.
	now = now.Add(time.Duration(st.Settings.StartMinutes) * time.Minute)
	eng.passProject(ctx, id, false)
	now = now.Add(time.Duration(st.Settings.BlockedMinutes) * time.Minute)
	eng.passProject(ctx, id, false)

	st, _ = LoadState(paths.State(id))
	if got := st.Tasks[0].Attempts[0].Run; got.State != flow.PhaseEnded || got.Result != flow.EndStuck {
		t.Fatalf("stuck trust at start_minutes = %+v, want ended(stuck)", got)
	}
	if got := st.Tasks[1].Attempts[0].Run; got.State != flow.PhaseEnded || got.Result != flow.EndStuck {
		t.Fatalf("agy blocked at blocked_minutes = %+v, want ended(stuck)", got)
	}
	if got := st.Tasks[2].Attempts[0].Run; got.State != flow.PhaseEnded || got.Result != flow.EndBlank {
		t.Fatalf("blank at start_minutes = %+v, want ended(blank)", got)
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

	eng1 := New(paths, client)
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
	eng2 := New(paths, client)
	eng2.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	eng2.Now = func() time.Time { return now }
	eng2.passProject(ctx, id, true)

	stAfter, _ := LoadState(paths.State(id))
	got := stAfter.Tasks[0].Attempts[0].Run
	if got == nil || got.ID != runID || !got.StartedAt.Equal(startedAt) || got.State != flow.PhaseIdle || !got.LastTurnEnd.Equal(turnEnd) {
		t.Fatalf("after restart run = %+v, want preserved ID=%s StartedAt=%v State=idle LastTurnEnd=%v", got, runID, startedAt, turnEnd)
	}
}
