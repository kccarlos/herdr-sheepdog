package factory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

type projectViewWire struct {
	ID, State, RepoURL string
	Running, Live      bool
}

func viewWire(t *testing.T, v ProjectView) (w projectViewWire) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil || json.Unmarshal(b, &w) != nil {
		t.Fatalf("marshal ProjectView: %v", err)
	}
	return w
}

func lcExpect(t *testing.T, ok bool, f string, a ...any) {
	t.Helper()
	if !ok {
		t.Errorf(f, a...)
	}
}

// TestProjectStateTableEngine verifies every cell of the project state table
// (design.md section 6, plan.md 14.4) on Engine directly, view state precedence
// (finished wins whatever Started is), and the store commit contract.
func TestProjectStateTableEngine(t *testing.T) {
	t.Parallel()
	for _, started := range []bool{true, false} {
		v := viewWire(t, ProjectsView([]flow.State{{Project: flow.Project{ID: "p-fin", Started: started, Finished: true}}})[0])
		lcExpect(t, v.State == "finished", "Started=%v Finished=true -> state=%q, want finished", started, v.State)
	}
	// none / stop -> refused not_found; none / start new goal -> running.
	errNone := newGitTestEngine(t, testPaths(t), nil).Stop("no-such-project")
	lcExpect(t, refusalCode(errNone) == "not_found", "Stop(none) = %v, want Refusal(not_found)", errNone)
	h := newProjectHarness(t, "Goal 1")
	vw := func() projectViewWire { return viewWire(t, h.eng.Projects()[0]) }
	assertLogInvariants(t, h.paths.State(h.id), h.paths.Log(h.id))
	lcExpect(t, vw().State == "running", "first start: state=%q, want running", vw().State)
	// running / start same goal (owner retry clears failed acts), new goal (wakes live coord), stop.
	h.edit(func(s *flow.State) {
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: one", Acts: []flow.Act{{Key: "merge@c1", Result: "failed"}}}}
	})
	lcExpect(t, h.eng.Start(h.repo, "Goal 1", 0) == nil, "running same-goal Start failed")
	st, _ := assertLogInvariants(t, h.paths.State(h.id), h.paths.Log(h.id))
	lcExpect(t, len(st.Tasks[0].Acts) == 0 && vw().State == "running", "running retry: acts=%+v view=%+v", st.Tasks[0].Acts, vw())
	h.srv.Add(hc.Terminal{Address: "coord-live", Name: coordName(h.id), State: "live", Labels: testCoordLabels(h.id, "r1")})
	h.edit(func(s *flow.State) { s.Project.RunID = "coord-live" })
	lcExpect(t, h.eng.Start(h.repo, "Goal 2", 0) == nil, "running new-goal Start failed")
	st = h.state()
	lcExpect(t, st.Project.Goal == "Goal 2" && st.Project.NewGoal && len(st.Project.Wakes) > 0, "running new-goal: goal=%q new=%v wakes=%d", st.Project.Goal, st.Project.NewGoal, len(st.Project.Wakes))
	lcExpect(t, h.eng.Stop(h.id) == nil, "running Stop failed")
	assertLogInvariants(t, h.paths.State(h.id), h.paths.Log(h.id))
	_, getErr := h.client.Get(context.Background(), "coord-live")
	lcExpect(t, errors.Is(getErr, hc.ErrNotFound) && vw().State == "stopped" && !vw().Live, "after running Stop: err=%v view=%+v", getErr, vw())
	// stopped / stop -> stays stopped; start same -> resumes; start new -> new goal.
	lcExpect(t, h.eng.Stop(h.id) == nil && vw().State == "stopped", "stopped Stop: %+v", vw())
	lcExpect(t, h.eng.Start(h.repo, "Goal 2", 0) == nil && vw().State == "running", "stopped resume: %+v", vw())
	_ = h.eng.Stop(h.id)
	lcExpect(t, h.eng.Start(h.repo, "Goal 3", 0) == nil && h.state().Project.Goal == "Goal 3" && vw().State == "running", "stopped new goal: %q", h.state().Project.Goal)
	// finished / stop with live coord -> ends run, stays finished; stop with none live -> no-op, no log.
	h.srv.Add(hc.Terminal{Address: "coord-fin", Name: coordName(h.id), State: "live", Labels: testCoordLabels(h.id, "r-fin")})
	h.edit(func(s *flow.State) {
		s.Project.Started, s.Project.Finished, s.Project.RunID = true, true, "coord-fin"
		s.Project.Run = &flow.RunRecord{ID: "r-fin", Address: "coord-fin", Role: flow.RoleCoordinator, State: flow.PhaseWorking}
		s.Project.Acts, s.Project.Wakes = []flow.Act{{Key: "final_journal@1", Result: "done"}}, nil
		s.Tasks = []flow.Task{{ID: "T1", Title: "feat: one", Done: &flow.Merged{MergedCommit: "m1"}}}
	})
	lcExpect(t, h.eng.Stop(h.id) == nil, "finished live Stop failed")
	assertLogInvariants(t, h.paths.State(h.id), h.paths.Log(h.id))
	_, getErr = h.client.Get(context.Background(), "coord-fin")
	lcExpect(t, errors.Is(getErr, hc.ErrNotFound) && h.state().Project.Finished && vw().State == "finished" && !vw().Live, "after finished live Stop: err=%v fin=%v view=%+v", getErr, h.state().Project.Finished, vw())
	seq0, logs0 := h.state().Seq, len(readRawLogLines(t, h.paths.Log(h.id)))
	lcExpect(t, h.eng.Stop(h.id) == nil, "finished idle Stop failed")
	lcExpect(t, h.state().Seq == seq0 && len(readRawLogLines(t, h.paths.Log(h.id))) == logs0 && h.state().Project.Finished, "finished idle Stop mutated: seq %d->%d logs %d->%d fin=%v", seq0, h.state().Seq, logs0, len(readRawLogLines(t, h.paths.Log(h.id))), h.state().Project.Finished)
	// finished / start same goal (no wake) -> refused already_closed, clears nothing, starts no coord.
	runs0 := len(h.runs)
	errFin := h.eng.Start(h.repo, "Goal 3", 0)
	lcExpect(t, refusalCode(errFin) == flow.RefuseClosed && strings.Contains(errFin.Error(), "This project is finished. Give it a new goal."), "finished same-goal Start = %v", errFin)
	h.pass()
	st = h.state()
	lcExpect(t, st.Project.Finished && len(st.Project.Acts) > 0 && st.Seq == seq0 && len(h.runs) == runs0, "refused finished start mutated/started: fin=%v acts=%d seq=%d/%d runs=%d/%d", st.Project.Finished, len(st.Project.Acts), st.Seq, seq0, len(h.runs), runs0)
	// finished / start same goal with held wake (J4) -> starts coord; finished / start new goal -> reopens.
	h.edit(func(s *flow.State) { s.Project.Wakes = []flow.Wake{{Key: "w1", Why: "pending", At: h.now}} })
	lcExpect(t, h.eng.Start(h.repo, "Goal 3", 0) == nil, "finished same-goal with wake failed")
	h.pass()
	lcExpect(t, len(h.runs) > runs0, "finished same-goal with wake did not start coord (runs=%d)", len(h.runs))
	h.edit(func(s *flow.State) {
		s.Project.Finished, s.Project.Wakes, s.Project.RunID, s.Project.Run = true, nil, "", nil
	})
	lcExpect(t, h.eng.Start(h.repo, "Goal 4", 0) == nil && !h.state().Project.Finished && h.state().Project.Goal == "Goal 4" && vw().State == "running", "reopen: fin=%v goal=%q view=%+v", h.state().Project.Finished, h.state().Project.Goal, vw())
}

// TestProjectViewLiveMatchesStopRuns pins: for every state file where Stop ends
// a run, ProjectView.Live is true; where none runs, false.
func TestProjectViewLiveMatchesStopRuns(t *testing.T) {
	t.Parallel()
	ended := &flow.RunRecord{Role: flow.RoleWorker, State: flow.PhaseEnded, Result: flow.EndStopped}
	cases := []struct {
		name, addr string
		wantLive   bool
		st         flow.State
	}{
		{"coordinator", "c-1", true, flow.State{Project: flow.Project{Finished: true, RunID: "c-1"}}},
		{"worker", "w-1", true, flow.State{Project: flow.Project{Started: true}, Tasks: []flow.Task{{ID: "T1", Attempts: []flow.Attempt{{RunID: "w-1"}}}}}},
		{"reviewer", "r-1", true, flow.State{Project: flow.Project{Started: true}, Tasks: []flow.Task{{ID: "T1", Attempts: []flow.Attempt{{Review: &flow.ReviewRun{Round: 1, RunID: "r-1"}}}}}}},
		{"plan_reviewer", "pr-1", true, flow.State{Project: flow.Project{Started: true, PlanReviews: []flow.PlanReview{{N: 1, Run: &flow.ReviewRun{Round: 1, RunID: "pr-1"}}}}}},
		{"none_live", "", false, flow.State{Project: flow.Project{Finished: true}, Tasks: []flow.Task{{ID: "T1", Done: &flow.Merged{MergedCommit: "m1"}, Attempts: []flow.Attempt{{BaseCommit: "000", Run: ended}}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, paths, id := hctest.New(t), testPaths(t), "proj-"+tc.name
			if tc.addr != "" {
				srv.Add(hc.Terminal{Address: tc.addr, Name: tc.addr, State: "live", Labels: map[bool]map[string]string{true: testCoordLabels(id, tc.addr)}[tc.name == "coordinator"]})
			}
			st := tc.st
			st.Project.ID, st.Settings = id, flow.DefaultSettings()
			if err := SaveState(paths.State(id), st); err != nil {
				t.Fatal(err)
			}
			v := viewWire(t, ProjectsView([]flow.State{st})[0])
			lcExpect(t, newGitTestEngine(t, paths, hc.NewClient(srv.Path)).Stop(id) == nil, "Stop failed")
			killed := slices.ContainsFunc(srv.Requests(), func(r hctest.Request) bool { return r.Method == "terminal.kill" })
			lcExpect(t, (!killed || v.Live) && v.Live == tc.wantLive, "view.Live=%v want=%v killed=%v", v.Live, tc.wantLive, killed)
		})
	}
}

// TestProjectRepoURLStoredAndViewsNeverCallGit verifies SE18: repo_url stored at
// start for https/ssh GitHub origins, empty for non-GitHub/unreadable origins
// without failing start, and no view function calls git.
func TestProjectRepoURLStoredAndViewsNeverCallGit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, origin, want string }{
		{"https", "https://github.com/o/r.git", "https://github.com/o/r"},
		{"ssh", "git@github.com:o/r.git", "https://github.com/o/r"},
		{"non_github", "https://gitlab.com/o/r.git", ""},
		{"no_origin", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, paths := initGitRepo(t), testPaths(t)
			if tc.origin != "" {
				if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", tc.origin).CombinedOutput(); err != nil {
					t.Fatalf("git remote add: %v: %s", err, out)
				}
			}
			eng := newGitTestEngine(t, paths, nil)
			if err := eng.Start(repo, "ship se18", 0); err != nil {
				t.Fatalf("Start(%s) = %v, want nil", tc.name, err)
			}
			id := eng.projectDirs()[0].Name()
			var raw map[string]map[string]any
			b, err := os.ReadFile(paths.State(id))
			_ = json.Unmarshal(b, &raw)
			gotRaw, _ := raw["project"]["repo_url"].(string)
			lcExpect(t, err == nil && gotRaw == tc.want, "state.json repo_url=%q want=%q", gotRaw, tc.want)
			noGit := hctest.New(t)
			noGit.OnExec = func(p hc.ExecParams) (hc.ExecResult, error) {
				t.Fatalf("view called git: %v", p.Argv)
				return hc.ExecResult{}, nil
			}
			ve := New(paths, nil)
			ve.git = world.NewGit(hc.NewClient(noGit.Path), nil, nil)
			_, gErr := ve.Graph(id)
			_, lErr := ve.LogLines(id, 10)
			lcExpect(t, viewWire(t, ve.Projects()[0]).RepoURL == tc.want && gErr == nil && lErr == nil, "view RepoURL=%q want=%q errs=%v/%v", viewWire(t, ve.Projects()[0]).RepoURL, tc.want, gErr, lErr)
		})
	}
}

// TestStartSetCheckStartAndMaxRunningRefusal_KB_LCH_23_KB_SET_2 verifies
// DoctorOptions.Settings with models.worker, Engine.CheckStart, Engine.StartSet,
// bad pair refusal, failed check leaving no state, and refusing a different
// --max-running / set on an existing project while accepting the same value.
func TestStartSetCheckStartAndMaxRunningRefusal_KB_LCH_23_KB_SET_2(t *testing.T) {
	t.Parallel()
	const wantFix = "Change max running in the settings panel on the factory page."
	repo, paths := initGitRepo(t), testPaths(t)
	chosen := flow.DefaultSettings()
	chosen.Models = map[string][]flow.Candidate{flow.RoleWorker: {{CLI: "codex", Model: "gpt-6.2-sol", Effort: "medium"}}}
	fake := makeStartCheckFake(repo, startFake{"codex login status": {Exit: 1, Stdout: "Not logged in\n"}})
	opts := DoctorOptions{Folder: repo, DataDir: paths.DataDir, RunCommand: fake.run}
	if f := reflect.ValueOf(&opts).Elem().FieldByName("Settings"); !f.IsValid() {
		t.Error("DoctorOptions.Settings field is missing (plan.md 14.4)")
	} else {
		v := reflect.ValueOf(chosen)
		if f.Kind() == reflect.Pointer {
			v = reflect.ValueOf(&chosen)
		}
		f.Set(v)
		r := findResult(t, StartChecks(context.Background(), opts), "start codex")
		lcExpect(t, r.Status == onboard.Fail && strings.Contains(strings.ToLower(r.Detail), "worker"), "StartChecks(models.worker=codex) = %+v", r)
	}
	eng := newGitTestEngine(t, paths, nil)
	wireStartCheck(eng, makeStartCheckFake(repo, startFake{"claude auth status --json": out(`{"loggedIn":false}`)}))
	type chkIface interface {
		CheckStart(string, map[string]string) ([]onboard.Result, error)
	}
	if ck, ok := any(eng).(chkIface); !ok {
		t.Error("Engine.CheckStart(folder, set) method is missing (plan.md 14.4)")
	} else if rs, err := ck.CheckStart(repo, map[string]string{"max_running": "5"}); err != nil || len(rs) == 0 || !onboard.Failed(rs) || len(eng.Projects()) != 0 {
		t.Errorf("CheckStart(failing) = len:%d err:%v projs:%d", len(rs), err, len(eng.Projects()))
	}
	type setIface interface {
		StartSet(string, string, map[string]string) error
	}
	if ss, ok := any(eng).(setIface); !ok {
		t.Error("Engine.StartSet(folder, goal, set) method is missing (plan.md 14.4)")
	} else {
		eng.StartCheck = noopStartCheck
		errBad := ss.StartSet(repo, "goal", map[string]string{"max_running": "0"})
		lcExpect(t, errBad != nil && strings.Contains(errBad.Error(), "1–20") && len(eng.Projects()) == 0, "StartSet(0) = %v projs=%d", errBad, len(eng.Projects()))
		if err := ss.StartSet(repo, "goal", map[string]string{"max_running": "5"}); err != nil {
			t.Fatalf("StartSet(first start): %v", err)
		}
		id := eng.projectDirs()[0].Name()
		e1, e2 := ss.StartSet(repo, "goal", map[string]string{"max_running": "2"}), eng.Start(repo, "goal", 2)
		st, _ := LoadState(paths.State(id))
		lcExpect(t, e1 != nil && strings.Contains(e1.Error(), wantFix) && e2 != nil && strings.Contains(e2.Error(), wantFix) && st.Settings.MaxRunning == 5, "existing diff max_running: e1=%v e2=%v stored=%d", e1, e2, st.Settings.MaxRunning)
		lcExpect(t, ss.StartSet(repo, "goal", map[string]string{"max_running": "5"}) == nil && eng.Start(repo, "goal", 5) == nil, "same max_running refused")
	}
	h2 := newProjectHarness(t, "Existing max_running")
	errFlag := h2.eng.Start(h2.repo, "Existing max_running", 5)
	lcExpect(t, errFlag != nil && strings.Contains(errFlag.Error(), wantFix), "Start(existing, --max-running=5 when 2) = %v", errFlag)
}
