package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

func recordedRunParams(t *testing.T, srv *hctest.Server) []hc.RunParams {
	t.Helper()
	var out []hc.RunParams
	for _, req := range srv.Requests() {
		if req.Method != "terminal.run" {
			continue
		}
		var p hc.RunParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Fatalf("unmarshal terminal.run params: %v", err)
		}
		out = append(out, p)
	}
	return out
}

func countMethodRequests(srv *hctest.Server, method string) int {
	n := 0
	for _, req := range srv.Requests() {
		if req.Method == method {
			n++
		}
	}
	return n
}

func TestProjectRunsShareOneHerdrSpace(t *testing.T) {
	t.Parallel()
	w := newRestartWorld(t, "Group project runs in one Herdr space")
	w.pass() // J7 starts coordinator
	w.applyPlan()
	w.pass() // N11 creates attempt 1
	w.pass() // N23 starts worker
	w.waitIdle(w.workerID)
	w.commitAndReady("package demo\n", greenRollup, true)
	w.pass() // N26 pushes and opens PR
	w.pass() // N29 starts reviewer round 1

	runs := recordedRunParams(t, w.srv)
	if len(runs) != 3 {
		t.Fatalf("expected 3 terminal.run requests (coordinator, worker, reviewer), got %d", len(runs))
	}

	wantNames := map[string]string{
		flow.RoleCoordinator: coordName(w.id),
		flow.RoleWorker:      workerName(w.id, "T1", 1),
		flow.RoleReviewer:    reviewerName(w.id, "T1", 1, 1),
	}
	byRole := map[string]hc.RunParams{}
	seenTabs := map[string]string{}
	for _, p := range runs {
		role := p.Labels["sheepdog.role"]
		if _, dup := byRole[role]; dup {
			t.Fatalf("duplicate terminal.run for role %q", role)
		}
		byRole[role] = p
	}

	for _, role := range []string{flow.RoleCoordinator, flow.RoleWorker, flow.RoleReviewer} {
		p, ok := byRole[role]
		if !ok {
			t.Fatalf("missing terminal.run for role %q", role)
		}
		if got := p.Labels["herdr.space"]; got != w.id {
			t.Errorf("%s: herdr.space = %q, want project id %q", role, got, w.id)
		}
		tab := p.Labels["herdr.tab"]
		if tab == "" {
			t.Errorf("%s: herdr.tab is empty", role)
		} else if prev, dup := seenTabs[tab]; dup {
			t.Errorf("%s: herdr.tab %q duplicates %s", role, tab, prev)
		} else {
			seenTabs[tab] = role
		}
		if got := p.Labels["herdr.seen"]; got != "0" {
			t.Errorf("%s: herdr.seen = %q, want 0", role, got)
		}
		if !p.NoParent {
			t.Errorf("%s: NoParent = false, want true", role)
		}
		if !p.NoNotify {
			t.Errorf("%s: NoNotify = false, want true", role)
		}
		if p.Name != wantNames[role] {
			t.Errorf("%s: Name = %q, want %q", role, p.Name, wantNames[role])
		}
	}
}

func TestTwoProjectsUseTwoHerdrSpaces(t *testing.T) {
	t.Parallel()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	eng := newTestEngine(t, paths, client)
	eng.Log = logging.NewJSONLLogger(&bytes.Buffer{})
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	eng.Now = func() time.Time { return now }

	repoA := initGitRepo(t)
	repoB := initGitRepo(t)
	if err := eng.Start(repoA, "Project A goal", 1); err != nil {
		t.Fatalf("Start project A: %v", err)
	}
	if err := eng.Start(repoB, "Project B goal", 1); err != nil {
		t.Fatalf("Start project B: %v", err)
	}
	dirs := eng.projectDirs()
	if len(dirs) != 2 {
		t.Fatalf("expected 2 project dirs, got %d", len(dirs))
	}
	idA, idB := dirs[0].Name(), dirs[1].Name()
	if idA == idB {
		t.Fatalf("expected distinct project ids, both %q", idA)
	}

	ctx := context.Background()
	eng.passProject(ctx, idA, false)
	eng.passProject(ctx, idB, false)

	runs := recordedRunParams(t, srv)
	if len(runs) != 2 {
		t.Fatalf("expected 2 terminal.run requests, got %d", len(runs))
	}
	spaceByProject := map[string]string{}
	for _, p := range runs {
		pid := p.Labels["sheepdog.project"]
		space := p.Labels["herdr.space"]
		if space != pid {
			t.Errorf("project %s: herdr.space = %q, want %q", pid, space, pid)
		}
		spaceByProject[pid] = space
	}
	if len(spaceByProject) != 2 {
		t.Fatalf("expected terminal.run for both projects, got %v", spaceByProject)
	}
	if spaceByProject[idA] == spaceByProject[idB] {
		t.Errorf("two projects shared herdr.space %q", spaceByProject[idA])
	}
}

func TestHerdrSpaceIsTheSameAfterRestart(t *testing.T) {
	t.Parallel()
	w := setupRunningWorker(t, "Preserve herdr.space across restart", 1)
	oldWorkerID := w.workerID
	if oldWorkerID == "" {
		t.Fatal("expected live worker after setupRunningWorker")
	}

	// Simulate a live pre-upgrade worker terminal with an old random herdr.space.
	const oldSpace = "old-random-space"
	if _, ok := w.srv.SetLabels(oldWorkerID, map[string]string{"herdr.space": oldSpace}); !ok {
		t.Fatalf("SetLabels(%s): terminal not found", oldWorkerID)
	}

	runsBefore := len(recordedRunParams(t, w.srv))
	labelsBefore := countMethodRequests(w.srv, "terminal.label")

	// Start a second Engine over the same data dir and run a pass.
	w.restart()
	w.now = w.now.Add(5 * time.Second)
	w.pass()

	if got := len(recordedRunParams(t, w.srv)); got != runsBefore {
		t.Fatalf("restart adopted live worker with %d extra terminal.run request(s), want 0", got-runsBefore)
	}
	if got := countMethodRequests(w.srv, "terminal.label"); got != labelsBefore {
		t.Fatalf("restart sent %d terminal.label request(s), want 0", got-labelsBefore)
	}
	term, err := w.client.Get(w.ctx, oldWorkerID)
	if err != nil {
		t.Fatalf("Get(%s) after restart: %v", oldWorkerID, err)
	}
	if term.State != "live" || term.Labels["herdr.space"] != oldSpace {
		t.Fatalf("adopted worker terminal = (state=%q, herdr.space=%q), want (live, %q)", term.State, term.Labels["herdr.space"], oldSpace)
	}
	if got := w.state().Tasks[0].Attempts[0].RunID; got != oldWorkerID {
		t.Fatalf("adopted attempt RunID = %q, want %q", got, oldWorkerID)
	}

	// Remove the worker's terminal; the replacement must carry the project id as herdr.space.
	w.srv.Remove(oldWorkerID)
	w.now = w.now.Add(5 * time.Second)
	w.pass() // observes EvLost -> worker run ends
	w.now = w.now.Add(2 * time.Minute)
	w.pass() // past restart back-off -> launches replacement worker

	runsAfter := recordedRunParams(t, w.srv)
	if len(runsAfter) != runsBefore+1 {
		t.Fatalf("expected 1 replacement terminal.run request, got %d", len(runsAfter)-runsBefore)
	}
	if got := countMethodRequests(w.srv, "terminal.label"); got != labelsBefore {
		t.Fatalf("replacement sent %d terminal.label request(s), want 0", got-labelsBefore)
	}
	rep := runsAfter[len(runsAfter)-1]
	if rep.Labels["sheepdog.role"] != flow.RoleWorker {
		t.Fatalf("replacement role = %q, want %q", rep.Labels["sheepdog.role"], flow.RoleWorker)
	}
	if got := rep.Labels["herdr.space"]; got != w.id {
		t.Errorf("replacement herdr.space = %q, want project id %q", got, w.id)
	}
	if rep.Labels["herdr.tab"] == "" {
		t.Errorf("replacement herdr.tab is empty")
	}
	if rep.Labels["herdr.seen"] != "0" {
		t.Errorf("replacement herdr.seen = %q, want 0", rep.Labels["herdr.seen"])
	}
	if !rep.NoParent || !rep.NoNotify {
		t.Errorf("replacement NoParent=%v NoNotify=%v, want both true", rep.NoParent, rep.NoNotify)
	}
	if want := workerName(w.id, "T1", 1); rep.Name != want {
		t.Errorf("replacement Name = %q, want %q", rep.Name, want)
	}
}
