package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
)

// TestSchedulingDepsAndCap checks PR 4.1 (KB-TSK-16, design.md 4.2, 4.10):
// three tasks with one dependency and max_running 2 plus a fourth queued task:
// two start, the third waits on its dependency by name at N9, nothing queued
// starts at the cap, and a task after a dropped dependency stays at N9.
func TestSchedulingDepsAndCap(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	eng := newGitTestEngine(t, paths, nil)

	// Invalid max_running is refused without saving; 0 keeps the existing/default value.
	for _, bad := range []int{-1, flow.MaxRunningMax + 1} {
		if err := eng.Start(repo, "Goal", bad); err == nil {
			t.Fatalf("Start with max_running=%d succeeded, want error", bad)
		}
	}
	if err := eng.Start(repo, "Goal", 2); err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(repo, "Goal", 0); err != nil {
		t.Fatal(err)
	}
	_, id, err := fixtureGit(t).RepoProject(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if st.Settings.MaxRunning != 2 {
		t.Fatalf("MaxRunning = %d, want 2", st.Settings.MaxRunning)
	}
	st.Project.RunID = "coord-1"
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	// Priority validation in plan apply.
	badPlan := `{"tasks":[{"id":"T1","title":"feat: a","estimate_lines":50,"priority":"high",` + testPlanJSON + `}]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(badPlan)); refusalCode(err) != "plan_invalid" {
		t.Fatalf("unknown priority = %v, want plan_invalid", err)
	}

	plan := `{"tasks":[
		{"id":"T1","title":"feat: first","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T2","title":"feat: second","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T3","title":"feat: third after first","after":["T1"],"estimate_lines":50,` + testPlanJSON + `},
		{"id":"T4","title":"feat: fourth queued","estimate_lines":40,` + testPlanJSON + `}
	]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}

	// Pass 1: T1 and T2 start (max_running 2); T3 waits on T1 (N9); T4 waits at cap (N10).
	eng.passProject(ctx, id, false)
	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	started := func(tasks []flow.Task) []string {
		var ids []string
		for _, task := range tasks {
			if flow.OpenAttempt(task) != nil {
				ids = append(ids, task.ID)
			}
		}
		return ids
	}
	if got := started(st.Tasks); !slices.Equal(got, []string{"T1", "T2"}) {
		t.Fatalf("after pass 1 started = %v, want [T1 T2]", got)
	}
	// Verify T3 lands on N9 naming T1, and T4 lands on N10 for no slot.
	nd3, dr3 := flow.UnmetDeps(st.Tasks[2], st.Tasks)
	r3, d3 := flow.FirstMatch(flow.TaskRows, flow.In{Task: st.Tasks[2], Settings: st.Settings, Facts: flow.Facts{DepsNotDone: nd3, DepsDropped: dr3}})
	if r3.ID != "N9" || !strings.Contains(d3.Why, "T1") {
		t.Fatalf("T3 matched %s (%q), want N9 naming T1", r3.ID, d3.Why)
	}
	r4, d4 := flow.FirstMatch(flow.TaskRows, flow.In{Task: st.Tasks[3], Settings: st.Settings, Facts: flow.Facts{NoSlot: true}})
	if r4.ID != "N10" || d4.Why != "no slot is free" {
		t.Fatalf("T4 matched %s (%q), want N10 no slot is free", r4.ID, d4.Why)
	}

	// Pass 2: still at cap (2 running), nothing queued starts.
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if got := started(st.Tasks); !slices.Equal(got, []string{"T1", "T2"}) {
		t.Fatalf("after pass 2 at cap started = %v, want [T1 T2]", got)
	}

	// Drop T1 and mark T2 done: T4 gets a slot, while T3 stays waiting at N9 on dropped T1.
	st.Tasks[0].Dropped = &flow.Dropped{Why: "no longer needed"}
	st.Tasks[1].Done = &flow.Merged{MergedCommit: "m2"}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if flow.OpenAttempt(st.Tasks[2]) != nil {
		t.Fatalf("T3 started although its dependency T1 was dropped: %+v", st.Tasks[2].Attempts)
	}
	if flow.OpenAttempt(st.Tasks[3]) == nil {
		t.Fatal("T4 did not start after slots freed")
	}
	nd3, dr3 = flow.UnmetDeps(st.Tasks[2], st.Tasks)
	r3, d3 = flow.FirstMatch(flow.TaskRows, flow.In{Task: st.Tasks[2], Settings: st.Settings, Facts: flow.Facts{DepsNotDone: nd3, DepsDropped: dr3}})
	if r3.ID != "N9" || !strings.Contains(d3.Why, "dropped T1") {
		t.Fatalf("T3 after T1 dropped = %s (%q), want N9 naming dropped T1", r3.ID, d3.Why)
	}
}

func TestFailedNewAttemptDoesNotTakeSlot(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	eng := newGitTestEngine(t, paths, nil)
	if err := eng.Start(repo, "Goal", 2); err != nil {
		t.Fatal(err)
	}
	_, id, err := fixtureGit(t).RepoProject(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(paths.State(id))
	st.Project.RunID, st.Project.RunTokenHash = "coord-1", tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	plan := `{"tasks":[
		{"id":"T1","title":"feat: first","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T2","title":"feat: second","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T3","title":"feat: third","estimate_lines":50,` + testPlanJSON + `}
	]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	// Block NewWorktree for T1 and T2 by placing regular files at their worktree paths.
	for _, tid := range []string{"T1", "T2"} {
		wt := paths.Worktree(id, tid, 1)
		if err := os.MkdirAll(filepath.Dir(wt), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wt, []byte("blocker"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Pass 1: T1 and T2 take the 2 slots and fail new_attempt; T3 waits at N10.
	eng.passProject(ctx, id, false)
	// Pass 2: T1 and T2 have failed new_attempt acts and must not starve T3.
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if flow.OpenAttempt(st.Tasks[0]) != nil || flow.OpenAttempt(st.Tasks[1]) != nil {
		t.Fatalf("T1/T2 unexpectedly started: %+v %+v", st.Tasks[0].Attempts, st.Tasks[1].Attempts)
	}
	if flow.OpenAttempt(st.Tasks[2]) == nil {
		t.Fatal("T3 was starved by T1/T2 failed new_attempt acts")
	}
}

func TestDeadWorkersFreeSlots(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	eng := newGitTestEngine(t, paths, client)
	if err := eng.Start(repo, "Goal", 2); err != nil {
		t.Fatal(err)
	}
	_, id, err := fixtureGit(t).RepoProject(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // J7 starts coordinator
	st, _ := LoadState(paths.State(id))
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	plan := `{"tasks":[
		{"id":"T1","title":"feat: first","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T2","title":"feat: second","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T3","title":"feat: third","estimate_lines":50,` + testPlanJSON + `}
	]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	eng.passProject(ctx, id, false) // N11 for T1, T2
	eng.passProject(ctx, id, false) // N23 starts workers for T1, T2
	st, _ = LoadState(paths.State(id))
	if flow.OpenAttempt(st.Tasks[2]) != nil {
		t.Fatal("T3 started while T1 and T2 workers are live")
	}
	// Kill T1 and T2 workers so both are RunGone (S1).
	for _, ti := range []int{0, 1} {
		if err := world.StopRun(ctx, client, st.Tasks[ti].Attempts[0].RunID); err != nil {
			t.Fatal(err)
		}
	}
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if flow.OpenAttempt(st.Tasks[2]) == nil {
		t.Fatal("T3 did not start after T1 and T2 workers died")
	}
}

func TestDroppedDepWakesAndAmendUnblocks(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	eng := newGitTestEngine(t, paths, client)
	if err := eng.Start(repo, "Goal", 2); err != nil {
		t.Fatal(err)
	}
	_, id, err := fixtureGit(t).RepoProject(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // J7 starts coordinator
	st, _ := LoadState(paths.State(id))
	coord := st.Project.RunID
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	plan := `{"tasks":[
		{"id":"T1","title":"feat: first","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T2","title":"feat: second","after":["T1"],"estimate_lines":50,` + testPlanJSON + `},
		{"id":"T3","title":"feat: third","after":["T2"],"estimate_lines":50,` + testPlanJSON + `}
	]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	eng.passProject(ctx, id, false) // T1 starts (N11)
	if err := eng.Report("tok-coord", "task", json.RawMessage(`{"action":"drop","task":"T1","why":"superseded"}`)); err != nil {
		t.Fatalf("task drop: %v", err)
	}
	// Pass with coordinator working: N9 queues wake once for T2's dropped dep T1.
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if len(st.Project.Wakes) != 1 || st.Project.Wakes[0].Task != "T2" || !strings.Contains(st.Project.Wakes[0].Facts, "T1") {
		t.Fatalf("held wakes = %+v, want 1 wake for T2 with facts naming T1", st.Project.Wakes)
	}
	// Coordinator goes idle: J12 delivers the wake once; a follow-up pass does not re-wake.
	setIdle(t, srv, client, coord, "idle")
	eng.passProject(ctx, id, false)
	sent := sendsTo(t, srv.Requests(), coord)
	if len(sent) != 1 || !strings.Contains(sent[0], "T2") || !strings.Contains(sent[0], "T1") {
		t.Fatalf("coordinator wakes sent = %q, want 1 message naming T2 and T1", sent)
	}
	eng.passProject(ctx, id, false)
	if got := sendsTo(t, srv.Requests(), coord); len(got) != 1 {
		t.Fatalf("coordinator woken again: %d messages, want 1", len(got))
	}

	// Amend validation: unknown dep, cycle, bad priority, and after on a started task are refused.
	for name, body := range map[string]string{
		"unknown dep":  `{"why":"fix","tasks":[{"id":"T2","after":["NOPE"]}]}`,
		"cycle":        `{"why":"fix","tasks":[{"id":"T2","after":["T3"]}]}`,
		"bad priority": `{"why":"fix","tasks":[{"id":"T2","priority":"high"}]}`,
	} {
		if err := eng.Report("tok-coord", "plan amend", json.RawMessage(body)); refusalCode(err) != "plan_invalid" {
			t.Fatalf("amend %s = %v, want plan_invalid", name, err)
		}
	}
	// Amend T2 to clear after and set priority=urgent: succeeds and unblocks T2.
	if err := eng.Report("tok-coord", "plan amend", json.RawMessage(`{"why":"T1 was dropped","tasks":[{"id":"T2","after":[],"priority":"urgent"}]}`)); err != nil {
		t.Fatalf("plan amend clear after: %v", err)
	}
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if flow.OpenAttempt(st.Tasks[1]) == nil || st.Tasks[1].Priority != flow.PriorityUrgent {
		t.Fatalf("T2 after amend = attempts:%d priority:%q, want started + urgent", len(st.Tasks[1].Attempts), st.Tasks[1].Priority)
	}
	// Once T2 has an attempt, amending after or priority is refused.
	for name, body := range map[string]string{
		"after on started":    `{"why":"late","tasks":[{"id":"T2","after":["T3"]}]}`,
		"priority on started": `{"why":"late","tasks":[{"id":"T2","priority":""}]}`,
	} {
		if err := eng.Report("tok-coord", "plan amend", json.RawMessage(body)); refusalCode(err) != "plan_invalid" {
			t.Fatalf("amend %s = %v, want plan_invalid", name, err)
		}
	}
}

func TestWaitReasonLoggedOncePerChange(t *testing.T) {
	ctx := context.Background()
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	eng := newGitTestEngine(t, paths, nil)
	if err := eng.Start(repo, "Goal", 1); err != nil {
		t.Fatal(err)
	}
	_, id, err := fixtureGit(t).RepoProject(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(paths.State(id))
	st.Project.RunID, st.Project.RunTokenHash = "coord-1", tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	plan := `{"tasks":[
		{"id":"T1","title":"feat: first","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T2","title":"feat: second","after":["T1"],"estimate_lines":50,` + testPlanJSON + `},
		{"id":"T3","title":"feat: third","estimate_lines":50,` + testPlanJSON + `}
	]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	// Pass 1 and Pass 2: T1 starts; T2 waits at N9 ("waiting on T1"), T3 waits at N10 ("no slot is free").
	eng.passProject(ctx, id, false)
	eng.passProject(ctx, id, false)
	lines, _, err := ProjectLog(ctx, paths, repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := func(task, rule, substr string) int {
		n := 0
		for _, l := range lines {
			if l.Task == task && l.Rule == rule && strings.Contains(l.Why, substr) {
				n++
			}
		}
		return n
	}
	if n := count("T2", "N9", "waiting on T1"); n != 1 {
		t.Fatalf("T2 N9 log count = %d, want 1", n)
	}
	if n := count("T3", "N10", "no slot is free"); n != 1 {
		t.Fatalf("T3 N10 log count = %d, want 1", n)
	}
	// Mark T1 done (and keep T3 taking the single slot next pass): T2 transitions from N9 to N10.
	st, _ = LoadState(paths.State(id))
	st.Tasks[0].Done = &flow.Merged{MergedCommit: "m1"}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // T2 starts (older than T3) or T3 waits; let's check both
	// Wait: T2 is older than T3, so T2 gets the slot and starts; T3 still waits at N10 (already logged once).
	lines, _, err = ProjectLog(ctx, paths, repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := count("T3", "N10", "no slot is free"); n != 1 {
		t.Fatalf("T3 N10 log count after unchanged wait = %d, want 1", n)
	}
}

func TestIdleWorkerHoldsSlot(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	eng := newGitTestEngine(t, paths, client)
	if err := eng.Start(repo, "Goal", 2); err != nil {
		t.Fatal(err)
	}
	_, id, err := fixtureGit(t).RepoProject(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // J7 starts coordinator
	st, _ := LoadState(paths.State(id))
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	plan := `{"tasks":[
		{"id":"T1","title":"feat: first","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T2","title":"feat: second","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T3","title":"feat: third","estimate_lines":50,` + testPlanJSON + `}
	]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	eng.passProject(ctx, id, false) // N11 for T1, T2
	eng.passProject(ctx, id, false) // N23 starts workers for T1, T2
	st, _ = LoadState(paths.State(id))
	for _, state := range []string{"idle", "done", "error"} {
		setIdle(t, srv, client, st.Tasks[0].Attempts[0].RunID, state)
		setIdle(t, srv, client, st.Tasks[1].Attempts[0].RunID, state)
		eng.passProject(ctx, id, false)
		st, _ = LoadState(paths.State(id))
		if flow.OpenAttempt(st.Tasks[2]) != nil {
			t.Fatalf("T3 started while T1 and T2 workers had status %q", state)
		}
	}
}

func TestReviewerGoneKeepsSlotWhileWorkerRuns(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	eng := newGitTestEngine(t, paths, client)
	if err := eng.Start(repo, "Goal", 2); err != nil {
		t.Fatal(err)
	}
	_, id, err := fixtureGit(t).RepoProject(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // J7 starts coordinator
	st, _ := LoadState(paths.State(id))
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	plan := `{"tasks":[
		{"id":"T1","title":"feat: first","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T2","title":"feat: second","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T3","title":"feat: third","estimate_lines":50,` + testPlanJSON + `}
	]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	eng.passProject(ctx, id, false) // N11 for T1, T2
	eng.passProject(ctx, id, false) // N23 starts workers for T1, T2
	st, _ = LoadState(paths.State(id))
	// Give T1 a ready report, an open PR, and a dead reviewer run while its worker keeps running.
	githubOrigin(t, repo)
	gh := installFakeGH(t)
	a1 := &st.Tasks[0].Attempts[0]
	writePRJSON(t, gh, "view.json", a1.BaseCommit, "[]")
	a1.PR = 7
	a1.Ready = []flow.Ready{{Head: a1.BaseCommit}}
	a1.Review = &flow.ReviewRun{Round: 1, Kind: flow.RoundFull, Head: a1.BaseCommit, RunID: "rev-gone", Code: "c1", Reviewer: flow.Reviewer{CLI: "claude", Model: "opus"}}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // 4.8 retries T1's dead reviewer; T1's worker keeps its slot
	st, _ = LoadState(paths.State(id))
	if rev := st.Tasks[0].Attempts[0].Review; rev == nil || rev.Retries != 1 || rev.RunID == "" {
		t.Fatalf("T1 review = %+v, want retried reviewer", rev)
	}
	eng.passProject(ctx, id, false) // T1's worker is still live: T3 must not take T1's slot
	st, _ = LoadState(paths.State(id))
	if flow.OpenAttempt(st.Tasks[2]) != nil {
		t.Fatal("T3 started while T1's worker was still running after reviewer died")
	}
	// Once T1's worker is actually gone, T1 frees its slot and T3 starts.
	if err := world.StopRun(ctx, client, st.Tasks[0].Attempts[0].RunID); err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false)
	st, _ = LoadState(paths.State(id))
	if flow.OpenAttempt(st.Tasks[2]) == nil {
		t.Fatal("T3 did not start after T1's worker died")
	}
}

func TestWorkerRunStateReadOncePerPass(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, _ := initGitRepoWithRemote(t)
	eng := newGitTestEngine(t, paths, client)
	if err := eng.Start(repo, "Goal", 2); err != nil {
		t.Fatal(err)
	}
	_, id, err := fixtureGit(t).RepoProject(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	eng.passProject(ctx, id, false) // J7 starts coordinator
	st, _ := LoadState(paths.State(id))
	st.Project.RunTokenHash = tokenHash("tok-coord")
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	plan := `{"tasks":[
		{"id":"T1","title":"feat: first","estimate_lines":50,` + testPlanJSON + `},
		{"id":"T2","title":"feat: second","estimate_lines":50,` + testPlanJSON + `}
	]}`
	if err := eng.Report("tok-coord", "plan apply", json.RawMessage(plan)); err != nil {
		t.Fatalf("plan apply: %v", err)
	}
	eng.passProject(ctx, id, false) // N11 for T1, T2
	eng.passProject(ctx, id, false) // N23 starts workers for T1, T2
	st, _ = LoadState(paths.State(id))
	w1, w2 := st.Tasks[0].Attempts[0].RunID, st.Tasks[1].Attempts[0].RunID
	before := len(srv.Requests())
	eng.passProject(ctx, id, false)
	counts := map[string]int{}
	for _, req := range srv.Requests()[before:] {
		if req.Method != "terminal.get" {
			continue
		}
		for _, w := range []string{w1, w2} {
			if strings.Contains(string(req.Params), w) {
				counts[w]++
			}
		}
	}
	for _, w := range []string{w1, w2} {
		if counts[w] != 1 {
			t.Errorf("terminal.get count for worker %s = %d, want 1", w, counts[w])
		}
	}
}

func makeSlotWorktree(t *testing.T, ctx context.Context, paths Paths, repo, id, taskID, file string) (wtPath, branch, head string) {
	t.Helper()
	wt, err := fixtureGit(t).NewWorktree(ctx, repo, paths.Worktree(id, taskID, 1), taskID+"-1", "feat: "+taskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, file), []byte("package demo\n// "+taskID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt.Path, "add", file)
	gitIn(t, wt.Path, "commit", "-m", "feat: "+taskID)
	head = gitIn(t, wt.Path, "rev-parse", "HEAD")
	gitIn(t, wt.Path, "push", "origin", head+":refs/heads/"+wt.Branch)
	return wt.Path, wt.Branch, head
}

func writeOpenPRView(t *testing.T, gh string, num int, head, rollup string) {
	t.Helper()
	body := fmt.Sprintf(`{"number":%d,"state":"OPEN","headRefOid":%q,"mergeable":"MERGEABLE","statusCheckRollup":%s,"baseRefName":"main"}`, num, head, rollup)
	writeGHFile(t, gh, fmt.Sprintf("view-%d.json", num), body)
}

// TestCIWaitingTaskTakesNoSlot checks finding 10a (trial seq 65, max_running 1):
// T1 (critical path 760) passed review and waits on CI at N36 with its worker
// ended; T5 (critical path 200) is at N22 with findings and its worker ended.
// T1's matching row asks for no worker, so T1 must not take the slot and T5's
// worker must start in the first pass.
func TestCIWaitingTaskTakesNoSlot(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 22, 20, 34, 0, 0, time.UTC)
	endedAt := now.Add(-5 * time.Minute)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Trial finding 10a", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt1, br1, c1 := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	wt5, br5, c5 := makeSlotWorktree(t, ctx, paths, repo, id, "T5", "t5.go")
	writeOpenPRView(t, gh, 1, c1, "["+ciCheck("ci", "IN_PROGRESS", "", "101", "1001")+"]")
	writeOpenPRView(t, gh, 5, c5, "[]")

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Tasks = []flow.Task{
		{
			ID: "T1", Title: "feat: T1 waiting on CI", EstimateLines: 760,
			Attempts: []flow.Attempt{{
				SizeCap: 1600, BaseCommit: base, Branch: br1, Worktree: wt1, PR: 1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-t1-gone",
					SessionID: "sess-t1", Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: endedAt,
				},
				Ready: []flow.Ready{{Head: c1}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: endedAt.Add(-time.Minute)}},
			}},
		},
		{
			ID: "T5", Title: "feat: T5 fix round 1 findings", EstimateLines: 200,
			Attempts: []flow.Attempt{{
				SizeCap: 400, BaseCommit: base, Branch: br5, Worktree: wt5, PR: 5,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-t5-gone",
					SessionID: "sess-t5", Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: endedAt,
				},
				Ready:   []flow.Ready{{Head: c5}},
				Rounds:  []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c5, Verdict: flow.VerdictChanges, Majors: 1, ReportPath: "/r/t5-r1.md"}},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: endedAt.Add(-time.Minute)}},
			}},
		},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks[0].LastRow != "N36" || st.Tasks[0].Attempts[0].RunID != "" {
		t.Errorf("T1 after pass = row:%q run_id:%q, want N36 with no worker started", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].RunID)
	}
	if st.Tasks[1].Attempts[0].RunID == "" {
		t.Fatalf("T5 worker did not start in the first pass while T1 waited on CI (T5 row=%q hold=%q)", st.Tasks[1].LastRow, st.Tasks[1].HoldWhy)
	}
}

// TestRestartBeforeNewAttempt checks finding 10b (trial seq 109/161, max_running 1):
// an open attempt asking to restart its worker (T5 at N22, critical path 200)
// gets the free slot ahead of new attempts with longer critical paths (T3 300, T4 250).
func TestRestartBeforeNewAttempt(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 22, 21, 38, 0, 0, time.UTC)
	endedAt := now.Add(-5 * time.Minute)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Trial finding 10b", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt5, br5, c5 := makeSlotWorktree(t, ctx, paths, repo, id, "T5", "t5.go")
	writeOpenPRView(t, gh, 5, c5, "[]")

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Tasks = []flow.Task{
		{ID: "T3", Title: "feat: T3 new attempt", EstimateLines: 300},
		{ID: "T4", Title: "feat: T4 new attempt", EstimateLines: 250},
		{
			ID: "T5", Title: "feat: T5 fix round 1 findings", EstimateLines: 200,
			Attempts: []flow.Attempt{{
				SizeCap: 400, BaseCommit: base, Branch: br5, Worktree: wt5, PR: 5,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-t5-gone",
					SessionID: "sess-t5", Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: endedAt,
				},
				Ready:   []flow.Ready{{Head: c5}},
				Rounds:  []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c5, Verdict: flow.VerdictChanges, Majors: 1, ReportPath: "/r/t5-r1.md"}},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: endedAt.Add(-time.Minute)}},
			}},
		},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks[2].Attempts[0].RunID == "" {
		t.Fatalf("T5 open-attempt restart did not beat new attempts T3/T4 (T3 attempts=%d T4 attempts=%d T5 row=%q)",
			len(st.Tasks[0].Attempts), len(st.Tasks[1].Attempts), st.Tasks[2].LastRow)
	}
	if flow.OpenAttempt(st.Tasks[0]) != nil || flow.OpenAttempt(st.Tasks[1]) != nil {
		t.Errorf("new attempts opened while T5 restarted: T3=%d T4=%d", len(st.Tasks[0].Attempts), len(st.Tasks[1].Attempts))
	}
}

// TestBackoffRestartIsNoCandidate checks C3 non-starvation: an open attempt
// whose worker restart is in back-off asks nothing, so it is not a slot
// candidate and a queued new attempt B gets the single free slot.
func TestBackoffRestartIsNoCandidate(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 22, 21, 40, 0, 0, time.UTC)
	endedAt := now.Add(-10 * time.Second) // inside 1m ensure_backoff_minutes
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Backoff non-starvation", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wtA, brA, _ := makeSlotWorktree(t, ctx, paths, repo, id, "A", "a.go")

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Tasks = []flow.Task{
		{
			ID: "A", Title: "feat: A in restart backoff", EstimateLines: 300,
			Attempts: []flow.Attempt{{
				SizeCap: 600, BaseCommit: base, Branch: brA, Worktree: wtA,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-a-gone",
					SessionID: "sess-a", Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: endedAt,
				},
				Ready:   []flow.Ready{},
				Rounds:  []flow.Round{},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: endedAt.Add(-time.Minute)}},
			}},
		},
		{ID: "B", Title: "feat: B new attempt", EstimateLines: 50},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if flow.OpenAttempt(st.Tasks[1]) == nil {
		t.Fatalf("B did not start while A was in restart back-off (A row=%q hold=%q)", st.Tasks[0].LastRow, st.Tasks[0].HoldWhy)
	}
	if st.Tasks[0].Attempts[0].RunID != "" {
		t.Errorf("A restarted during back-off: run_id=%q", st.Tasks[0].Attempts[0].RunID)
	}
	if st.Tasks[0].HoldWhy != "waiting for worker restart back-off" {
		t.Errorf("A HoldWhy = %q, want %q", st.Tasks[0].HoldWhy, "waiting for worker restart back-off")
	}
}

// TestIdleWorkerWaitingOnCIHoldsSlot pins today's rule for O38 (C4, trial 22:00-22:28):
// T3 passed review and waits on CI at N36 with its worker live and idle; it
// holds the single slot until its worker run is gone, so T5 at N22 waits.
func TestIdleWorkerWaitingOnCIHoldsSlot(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)
	endedAt := now.Add(-5 * time.Minute)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "O38 idle worker holds slot", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt3, br3, c3 := makeSlotWorktree(t, ctx, paths, repo, id, "T3", "t3.go")
	wt5, br5, c5 := makeSlotWorktree(t, ctx, paths, repo, id, "T5", "t5.go")
	writeOpenPRView(t, gh, 3, c3, "["+ciCheck("ci", "IN_PROGRESS", "", "301", "3001")+"]")
	writeOpenPRView(t, gh, 5, c5, "[]")

	w3, err := startRun(ctx, client, world.RunSpec{Name: "worker-t3-idle", Cwd: wt3, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w3, "idle", now.Add(-time.Minute))

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Tasks = []flow.Task{
		{
			ID: "T3", Title: "feat: T3 idle waiting on CI", EstimateLines: 300,
			Attempts: []flow.Attempt{{
				SizeCap: 600, BaseCommit: base, Branch: br3, Worktree: wt3, PR: 3, RunID: w3,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w3,
					State: flow.PhaseIdle, Status: "idle", StatusSince: now.Add(-time.Minute), StatusReported: true,
				},
				Ready: []flow.Ready{{Head: c3}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c3, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c3, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
		{
			ID: "T5", Title: "feat: T5 waiting on slot", EstimateLines: 200,
			Attempts: []flow.Attempt{{
				SizeCap: 400, BaseCommit: base, Branch: br5, Worktree: wt5, PR: 5,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-t5-gone",
					SessionID: "sess-t5", Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: endedAt,
				},
				Ready:   []flow.Ready{{Head: c5}},
				Rounds:  []flow.Round{{N: 1, Kind: flow.RoundFull, Head: c5, Verdict: flow.VerdictChanges, Majors: 1, ReportPath: "/r/t5-r1.md"}},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: endedAt.Add(-time.Minute)}},
			}},
		},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks[0].LastRow != "N36" || st.Tasks[0].Attempts[0].RunID != w3 {
		t.Errorf("T3 after pass = row:%q run_id:%q, want N36 keeping idle worker %q", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].RunID, w3)
	}
	if st.Tasks[1].LastRow != "N22" || st.Tasks[1].Attempts[0].RunID != "" {
		t.Fatalf("T5 started while T3's idle worker held the slot: row=%q run_id=%q", st.Tasks[1].LastRow, st.Tasks[1].Attempts[0].RunID)
	}
}

// TestSamePassDepCompletionRespectsMaxRunning checks that a task whose
// dependency finishes in the same pass (non-candidate during slot selection)
// still respects max_running instead of opening an attempt without a grant.
func TestSamePassDepCompletionRespectsMaxRunning(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	endedAt := now.Add(-5 * time.Minute)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Same-pass dep completion respects max_running", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt0, br0, c0 := makeSlotWorktree(t, ctx, paths, repo, id, "T0", "t0.go")
	wt1, br1, _ := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	writeGHFile(t, gh, "view-1.json", fmt.Sprintf(
		`{"number":1,"state":"MERGED","headRefOid":%q,"mergeable":"UNKNOWN","statusCheckRollup":[],"baseRefName":"main","mergeCommit":{"oid":"m0"}}`,
		c0,
	))

	w1, err := startRun(ctx, client, world.RunSpec{Name: "worker-t1-live", Cwd: wt1, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w1, "working", now.Add(-time.Minute))

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Tasks = []flow.Task{
		{
			ID: "T0", Title: "feat: T0 merged PR", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 200, BaseCommit: base, Branch: br0, Worktree: wt0, PR: 1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-t0-gone",
					SessionID: "sess-t0", Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: endedAt,
				},
				Ready: []flow.Ready{{Head: c0}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c0, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c0, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{{Head: c0}},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: endedAt.Add(-time.Minute)}},
			}},
		},
		{
			ID: "T1", Title: "feat: T1 holding single slot", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 200, BaseCommit: base, Branch: br1, Worktree: wt1, RunID: w1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w1,
					State: flow.PhaseWorking, Status: "working", StatusSince: now.Add(-time.Minute), StatusReported: true,
				},
				Ready:   []flow.Ready{},
				Rounds:  []flow.Round{},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
		{ID: "T2", Title: "feat: T2 after T0", After: []string{"T0"}, EstimateLines: 50},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks[0].Done == nil {
		t.Fatalf("T0 was not marked done (row=%q)", st.Tasks[0].LastRow)
	}
	if flow.OpenAttempt(st.Tasks[2]) != nil {
		t.Fatalf("T2 opened an attempt in the same pass while T1 held the only slot (max_running=1): %+v", st.Tasks[2].Attempts)
	}
	if st.Tasks[2].LastRow != "N10" || st.Tasks[2].HoldWhy != "no slot is free" {
		t.Errorf("T2 after pass = row:%q hold:%q, want N10 / %q", st.Tasks[2].LastRow, st.Tasks[2].HoldWhy, "no slot is free")
	}
}

// TestSamePassStaleGateCancelRespectsMaxRunning checks that a task whose stale
// gate ask is cancelled in actTaskLocked (making it a non-candidate during slot
// selection at N31) still respects max_running when falling through to N32.
func TestSamePassStaleGateCancelRespectsMaxRunning(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 24, 10, 5, 0, 0, time.UTC)
	endedAt := now.Add(-5 * time.Minute)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Same-pass stale gate cancel respects max_running", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt1, br1, _ := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	wt2, br2, c1 := makeSlotWorktree(t, ctx, paths, repo, id, "T2", "t2.go")
	if err := os.WriteFile(filepath.Join(wt2, "t2.go"), []byte("package demo\n// T2 v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, wt2, "add", "t2.go")
	gitIn(t, wt2, "commit", "-m", "feat: T2 second commit")
	c2 := gitIn(t, wt2, "rev-parse", "HEAD")
	gitIn(t, wt2, "push", "origin", c2+":refs/heads/"+br2)

	writeGHFile(t, gh, "view-2.json", fmt.Sprintf(
		`{"number":2,"state":"OPEN","headRefOid":%q,"mergeable":"CONFLICTING","statusCheckRollup":[],"baseRefName":"main"}`,
		c2,
	))

	w1, err := startRun(ctx, client, world.RunSpec{Name: "worker-t1-live", Cwd: wt1, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w1, "working", now.Add(-time.Minute))

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Asks = []flow.Ask{{
		ID: "ask-stale", Task: "T2", Kind: flow.KindGate, Gated: true,
		Status: flow.AskStatusOpen, Head: c1,
	}}
	st.Tasks = []flow.Task{
		{
			ID: "T1", Title: "feat: T1 holding single slot", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 200, BaseCommit: base, Branch: br1, Worktree: wt1, RunID: w1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w1,
					State: flow.PhaseWorking, Status: "working", StatusSince: now.Add(-time.Minute), StatusReported: true,
				},
				Ready:   []flow.Ready{},
				Rounds:  []flow.Round{},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
		{
			ID: "T2", Title: "feat: T2 stale gate ask with conflict", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 200, BaseCommit: base, Branch: br2, Worktree: wt2, PR: 2,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-t2-gone",
					SessionID: "sess-t2", Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: endedAt,
				},
				Ready: []flow.Ready{{Head: c1}, {Head: c2}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: endedAt.Add(-time.Minute)}},
			}},
		},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Asks) != 1 || st.Asks[0].Status != flow.AskStatusCancelled {
		t.Fatalf("stale gate ask = %+v, want cancelled", st.Asks)
	}
	if st.Tasks[1].Attempts[0].RunID != "" {
		t.Fatalf("T2 launched a second live worker in the same pass at max_running=1: run_id=%q", st.Tasks[1].Attempts[0].RunID)
	}
	if st.Tasks[1].LastRow != "N32" || st.Tasks[1].HoldWhy != "no slot is free" {
		t.Errorf("T2 after pass = row:%q hold:%q, want N32 / %q", st.Tasks[1].LastRow, st.Tasks[1].HoldWhy, "no slot is free")
	}
}

// TestClockAdvancePastBackoffRespectsMaxRunning checks that a task still in
// worker restart back-off during candidate selection (t0+59s) cannot launch
// over max_running when the clock crosses the 1m back-off deadline before
// actTaskLocked runs (t0+61s).
func TestClockAdvancePastBackoffRespectsMaxRunning(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	bindRepoFakeGH(t, repo)

	t0 := time.Date(2026, 11, 24, 10, 10, 0, 0, time.UTC)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return t0.Add(59 * time.Second) }
	if err := eng.Start(repo, "Clock advance past backoff respects max_running", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt1, br1, _ := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	wt2, br2, _ := makeSlotWorktree(t, ctx, paths, repo, id, "T2", "t2.go")

	w1, err := startRun(ctx, client, world.RunSpec{Name: "worker-t1-live", Cwd: wt1, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w1, "working", t0)

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Settings.EnsureBackoffMinutes = 1
	st.Tasks = []flow.Task{
		{
			ID: "T1", Title: "feat: T1 holding single slot", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 200, BaseCommit: base, Branch: br1, Worktree: wt1, RunID: w1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w1,
					State: flow.PhaseWorking, Status: "working", StatusSince: t0, StatusReported: true,
				},
				Ready:   []flow.Ready{},
				Rounds:  []flow.Round{},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: t0.Add(-5 * time.Minute)}},
			}},
		},
		{
			ID: "T2", Title: "feat: T2 crossing restart backoff", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 200, BaseCommit: base, Branch: br2, Worktree: wt2,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-t2-gone",
					SessionID: "sess-t2", Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndExited, EndedAt: t0,
				},
				Ready:   []flow.Ready{},
				Rounds:  []flow.Round{},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: t0.Add(-5 * time.Minute)}},
			}},
		},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	// Candidate selection sees t0+59s (inside 1m back-off); actTaskLocked sees
	// t0+61s (past 1m back-off).
	clock := t0.Add(59 * time.Second)
	eng.Now = func() time.Time {
		var pcs [8]uintptr
		frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
		for {
			f, more := frames.Next()
			if strings.HasSuffix(f.Function, ".actTaskLocked") {
				clock = t0.Add(61 * time.Second)
				break
			}
			if !more {
				break
			}
		}
		return clock
	}

	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks[1].Attempts[0].RunID != "" {
		t.Fatalf("T2 launched a second live worker after clock crossed back-off at max_running=1: run_id=%q", st.Tasks[1].Attempts[0].RunID)
	}
	if st.Tasks[1].HoldWhy != "no slot is free" {
		t.Errorf("T2 HoldWhy = %q, want %q (row=%q)", st.Tasks[1].HoldWhy, "no slot is free", st.Tasks[1].LastRow)
	}
}
