package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/hc/hctest"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
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

// TestIdleWorkerWaitingOnCIYieldsSlotToRestart checks O38 (trial 22:13-22:20):
// T3 passed review and waits on CI at N36 with its worker live and idle while
// T5 wants to restart at N22 on max_running 1. Pass 1 stops T3's idle worker
// (EndStopped, rule slot) while T5 waits at N22; pass 2 restarts T5 at N22.
func TestIdleWorkerWaitingOnCIYieldsSlotToRestart(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 22, 22, 13, 51, 0, time.UTC)
	endedAt := now.Add(-5 * time.Minute)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "O38 idle worker yields slot to restart", 1); err != nil {
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
					SessionID: "sess-t3", Resumable: true,
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

	// Pass 1: T3 stays N36, its idle worker is stopped (EndStopped, rule slot); T5 waits at N22.
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	a3 := st.Tasks[0].Attempts[0]
	if st.Tasks[0].LastRow != "N36" || a3.RunID != "" || a3.Run == nil || a3.Run.State != flow.PhaseEnded || a3.Run.Result != flow.EndStopped || !a3.Run.Resumable {
		t.Fatalf("T3 after pass 1 = row:%q run_id:%q run:%+v, want N36 ended(stopped) resumable", st.Tasks[0].LastRow, a3.RunID, a3.Run)
	}
	if _, err := client.Get(ctx, w3); !errors.Is(err, hc.ErrNotFound) {
		t.Errorf("T3 worker terminal %s still present after yield: err=%v", w3, err)
	}
	if slotLines := logRules(t, paths.Log(id), "slot"); len(slotLines) != 1 || slotLines[0].Task != "T3" || slotLines[0].Decision != "stopped idle worker" {
		t.Errorf("slot log lines after pass 1 = %+v, want 1 stopped idle worker line for T3", slotLines)
	}
	if st.Tasks[1].LastRow != "N22" || st.Tasks[1].Attempts[0].RunID != "" || st.Tasks[1].HoldWhy != "no slot is free" {
		t.Fatalf("T5 after pass 1 = row:%q run_id:%q hold:%q, want N22 waiting no slot is free", st.Tasks[1].LastRow, st.Tasks[1].Attempts[0].RunID, st.Tasks[1].HoldWhy)
	}

	// Pass 2: T5 restarts at N22 on the freed slot; T3 stays N36 with no worker.
	now = now.Add(flow.PassInterval)
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks[1].LastRow != "N22" || st.Tasks[1].Attempts[0].RunID == "" || !flow.Done(st.Tasks[1].Attempts[0].Acts, flow.AskStartWorker+"@"+flow.ContinueResume+"@1") {
		t.Fatalf("T5 after pass 2 = row:%q run_id:%q acts:%+v, want N22 restarted via start_worker@resume@1", st.Tasks[1].LastRow, st.Tasks[1].Attempts[0].RunID, st.Tasks[1].Attempts[0].Acts)
	}
	if st.Tasks[0].LastRow != "N36" || st.Tasks[0].Attempts[0].RunID != "" {
		t.Errorf("T3 after pass 2 = row:%q run_id:%q, want N36 with no worker", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].RunID)
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

// TestCIWaitYieldsSlotToNewAttempt replays c-live.txt (O38, max_running 1):
// T1 has two passed rounds, checks IN_PROGRESS, and an idle worker; T2 has no
// attempt. Pass 1 keeps T1 at N36, stops its idle worker (EndStopped, one log
// line rule "slot"), and holds T2 at N10 "no slot is free". Pass 2 starts T2
// at N11 (new_attempt) with no start_worker for T1. When T1's checks turn
// green, T1 merges at N42 with no worker.
func TestCIWaitYieldsSlotToNewAttempt(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 24, 4, 48, 3, 0, time.UTC)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "PR C live check (c-live replay)", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt1, br1, c1 := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	writeOpenPRView(t, gh, 139, c1, "["+ciCheck("ci", "IN_PROGRESS", "", "1391", "13901")+"]")

	w1, err := startRun(ctx, client, world.RunSpec{Name: "worker-t1-clive", Cwd: wt1, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w1, "idle", now.Add(-time.Minute))

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Tasks = []flow.Task{
		{
			ID: "T1", Title: "chore(ci): add slow marker and c-live note", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: base, Branch: br1, Worktree: wt1, PR: 139, RunID: w1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w1,
					SessionID: "sess-t1-clive", Resumable: true,
					State: flow.PhaseIdle, Status: "idle", StatusSince: now.Add(-time.Minute), StatusReported: true,
				},
				Ready: []flow.Ready{{Head: c1}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-5 * time.Minute)}},
			}},
		},
		{
			ID: "T2", Title: "chore(notes): append T2 to c-live", EstimateLines: 50,
			Outcomes: []flow.Outcome{{Text: "T2 note appended", ProvedBy: "cmd: true"}},
		},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	// Pass 1: T1 stays N36, its idle worker is stopped (EndStopped, one rule "slot" line); T2 waits at N10.
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	a1 := st.Tasks[0].Attempts[0]
	if st.Tasks[0].LastRow != "N36" || a1.RunID != "" || a1.Run == nil || a1.Run.State != flow.PhaseEnded || a1.Run.Result != flow.EndStopped || !a1.Run.Resumable {
		t.Fatalf("T1 after pass 1 = row:%q run_id:%q run:%+v, want N36 ended(stopped) resumable", st.Tasks[0].LastRow, a1.RunID, a1.Run)
	}
	if _, err := client.Get(ctx, w1); !errors.Is(err, hc.ErrNotFound) {
		t.Errorf("T1 worker %s still present in hc after yield: err=%v", w1, err)
	}
	slotLines := logRules(t, paths.Log(id), "slot")
	if len(slotLines) != 1 || slotLines[0].Task != "T1" || slotLines[0].Decision != "stopped idle worker" || !strings.Contains(slotLines[0].Why, "T2") {
		t.Errorf("slot log lines after pass 1 = %+v, want 1 stopped idle worker line for T1 naming T2", slotLines)
	}
	if st.Tasks[1].LastRow != "N10" || st.Tasks[1].HoldWhy != "no slot is free" || flow.OpenAttempt(st.Tasks[1]) != nil {
		t.Fatalf("T2 after pass 1 = row:%q hold:%q attempts:%d, want N10 no slot is free with 0 attempts", st.Tasks[1].LastRow, st.Tasks[1].HoldWhy, len(st.Tasks[1].Attempts))
	}
	if n10 := logRules(t, paths.Log(id), "N10"); len(n10) != 1 || n10[0].Task != "T2" || n10[0].Why != "no slot is free" {
		t.Errorf("N10 log lines after pass 1 = %+v, want 1 line for T2 no slot is free", n10)
	}

	// Pass 2: T2 opens attempt 1 at N11 (new_attempt); no start_worker for T1.
	now = now.Add(flow.PassInterval)
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	a2 := flow.OpenAttempt(st.Tasks[1])
	if st.Tasks[1].LastRow != "N11" || a2 == nil || !flow.Done(a2.Acts, flow.AskNewAttempt) {
		t.Fatalf("T2 after pass 2 = row:%q attempt:%+v, want N11 with new_attempt done", st.Tasks[1].LastRow, a2)
	}
	if n11 := logRules(t, paths.Log(id), "N11"); len(n11) != 1 || n11[0].Task != "T2" || !slices.Equal(n11[0].Asked, []string{flow.AskNewAttempt}) {
		t.Errorf("N11 log lines after pass 2 = %+v, want 1 new_attempt line for T2", n11)
	}
	if st.Tasks[0].LastRow != "N36" || st.Tasks[0].Attempts[0].RunID != "" || flow.Count(st.Tasks[0].Attempts[0].Acts, flow.AskStartWorker+"@"+flow.ContinueResume) != 0 {
		t.Errorf("T1 after pass 2 = row:%q run_id:%q acts:%+v, want N36 with no start_worker", st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].RunID, st.Tasks[0].Attempts[0].Acts)
	}

	// Checks turn green: T1 merges at N42 with no worker started.
	writeOpenPRView(t, gh, 139, c1, greenRollup)
	now = now.Add(flow.PassInterval)
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	a1 = st.Tasks[0].Attempts[0]
	if st.Tasks[0].LastRow != "N42" || a1.RunID != "" || !flow.Done(a1.Acts, flow.AskAccept+"@"+c1) || !flow.Done(a1.Acts, flow.AskMerge+"@"+c1) {
		t.Fatalf("T1 after green checks = row:%q run_id:%q acts:%+v, want N42 accept+merge with no worker", st.Tasks[0].LastRow, a1.RunID, a1.Acts)
	}
	if merges := ghCalls(t, gh, "pr merge"); len(merges) != 1 {
		t.Errorf("pr merge calls = %q, want 1", merges)
	}
	if n42 := logRules(t, paths.Log(id), "N42"); len(n42) != 1 || n42[0].Task != "T1" {
		t.Errorf("N42 log lines = %+v, want 1 line for T1", n42)
	}
}

// TestRedCIAfterYieldWaitsForSlot checks O38's red-after-yield path: T1 is
// stopped, T2 is working on the single slot, and T1's CI turns red with no
// re-runs left -> N33 holds the CI tell ("held for the next worker: …"), stores
// "no slot is free", and leaves only one live worker. When T2 goes idle at N36,
// T2 is stopped and T1 resumes on the next pass via start_worker@resume with
// Restarts 0 and the held CI text in its prompt.
func TestRedCIAfterYieldWaitsForSlot(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	var launched []hc.RunParams
	srv.OnRun = func(_ hc.Terminal, p hc.RunParams) {
		if p.Labels["sheepdog.role"] == flow.RoleWorker {
			launched = append(launched, p)
		}
	}

	now := time.Date(2026, 11, 24, 5, 0, 0, 0, time.UTC)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Red CI after yield waits for slot", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt1, br1, c1 := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	wt2, br2, c2 := makeSlotWorktree(t, ctx, paths, repo, id, "T2", "t2.go")
	writeOpenPRView(t, gh, 1, c1, checkRollup("FAILURE", "44714", "1001"))
	writeGHFile(t, gh, "workflow-runs.json", `{"total_count":1,"workflow_runs":[{"id":44714,"status":"completed","conclusion":"failure"}]}`)
	writeOpenPRView(t, gh, 2, c2, "["+ciCheck("ci", "IN_PROGRESS", "", "202", "2002")+"]")

	w2, err := startRun(ctx, client, world.RunSpec{Name: "worker-t2-working", Cwd: wt2, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w2, "working", now.Add(-time.Minute))

	const t1Session = "11111111-2222-4333-8444-555555555555"
	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Settings.FlakeReruns = 0
	st.Tasks = []flow.Task{
		{
			ID: "T1", Title: "feat: T1 red after yield", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: base, Branch: br1, Worktree: wt1, PR: 1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-t1-stopped",
					SessionID: t1Session, Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndStopped, EndedAt: now.Add(-2 * time.Minute),
				},
				Ready: []flow.Ready{{Head: c1}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
		{
			ID: "T2", Title: "feat: T2 working on slot", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: base, Branch: br2, Worktree: wt2, PR: 2, RunID: w2,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w2,
					SessionID: "22222222-2222-4333-8444-555555555555", Resumable: true,
					State: flow.PhaseWorking, Status: "working", StatusSince: now.Add(-time.Minute), StatusReported: true,
				},
				Ready: []flow.Ready{{Head: c2}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	// Pass 1: T2 is working -> T1 holds CI tell via R36, stores "no slot is free", still 1 live worker.
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks[0].LastRow != "N33" || st.Tasks[0].HoldWhy != "no slot is free" || st.Tasks[0].Attempts[0].RunID != "" {
		t.Fatalf("T1 after pass 1 = row:%q hold:%q run_id:%q, want N33 / no slot is free / no worker", st.Tasks[0].LastRow, st.Tasks[0].HoldWhy, st.Tasks[0].Attempts[0].RunID)
	}
	if held := st.Tasks[0].Attempts[0].Held; len(held) != 1 || !strings.Contains(held[0], "44714") {
		t.Fatalf("T1 Held after pass 1 = %q, want 1 CI failure message naming run 44714", held)
	}
	if n33 := logRules(t, paths.Log(id), "N33"); len(n33) != 1 || !strings.HasPrefix(n33[0].Decision, "held for the next worker:") {
		t.Errorf("N33 log after pass 1 = %+v, want Decision starting with %q", n33, "held for the next worker:")
	}
	if st.Tasks[1].Attempts[0].RunID != w2 || len(launched) != 0 {
		t.Fatalf("working T2 was disrupted or second worker launched: T2 run_id=%q launched=%d", st.Tasks[1].Attempts[0].RunID, len(launched))
	}
	if slotLines := logRules(t, paths.Log(id), "slot"); len(slotLines) != 0 {
		t.Errorf("slot log lines while T2 working = %+v, want 0", slotLines)
	}

	// T2 goes idle at N36 -> pass 2 stops T2's idle worker.
	setTerminalStatusAt(t, srv, client, w2, "idle", now)
	now = now.Add(time.Minute)
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	a2 := st.Tasks[1].Attempts[0]
	if st.Tasks[1].LastRow != "N36" || a2.RunID != "" || a2.Run == nil || a2.Run.State != flow.PhaseEnded || a2.Run.Result != flow.EndStopped {
		t.Fatalf("T2 after going idle at N36 = row:%q run_id:%q run:%+v, want N36 ended(stopped)", st.Tasks[1].LastRow, a2.RunID, a2.Run)
	}
	if slotLines := logRules(t, paths.Log(id), "slot"); len(slotLines) != 1 || slotLines[0].Task != "T2" {
		t.Errorf("slot log lines after T2 idle = %+v, want 1 line for T2", slotLines)
	}

	// Pass 3: T1 resumes via start_worker@resume with Restarts 0 and the held CI text in its prompt.
	now = now.Add(flow.PassInterval)
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	a1 := st.Tasks[0].Attempts[0]
	if a1.RunID == "" || a1.Restarts != 0 || !flow.Done(a1.Acts, flow.AskStartWorker+"@"+flow.ContinueResume) || len(a1.Held) != 0 {
		t.Fatalf("T1 after pass 3 = run_id:%q restarts:%d held:%q acts:%+v, want resumed worker with Restarts=0 and cleared Held", a1.RunID, a1.Restarts, a1.Held, a1.Acts)
	}
	if len(launched) != 1 {
		t.Fatalf("launched workers = %d, want 1", len(launched))
	}
	prompt := launched[0].Argv[len(launched[0].Argv)-1]
	if !slices.Contains(launched[0].Argv, "--resume") || !slices.Contains(launched[0].Argv, t1Session) || !strings.Contains(prompt, "CI failed on PR #1") || !strings.Contains(prompt, "44714") {
		t.Errorf("resumed worker argv = %q, want --resume %s and held CI prompt naming PR #1 / 44714", launched[0].Argv, t1Session)
	}
}

// TestRedCIAfterYieldBehindOwnerGate checks O38 hole 3 and round-2 correction 3:
// T1 is stopped, T2 is idle at N31 with its gate ask open, and T1 turns red.
// Pass 1 yields T2's worker; pass 2 resumes T1. Later the owner answers T2's
// N31 gate ask with the real no option (flow.GateOptionHold; a Merge yes needs
// no worker) -> N30 opens an owner_no recap, coordinator amends and decides
// new, and T2 starts on the extra slot while T1 is still working.
func TestRedCIAfterYieldBehindOwnerGate(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 24, 6, 0, 0, 0, time.UTC)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Red CI behind owner gate", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt1, br1, c1 := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	wt2, br2, c2 := makeSlotWorktree(t, ctx, paths, repo, id, "T2", "t2.go")
	writeOpenPRView(t, gh, 1, c1, checkRollup("FAILURE", "44714", "1001"))
	writeGHFile(t, gh, "workflow-runs.json", `{"total_count":1,"workflow_runs":[{"id":44714,"status":"completed","conclusion":"failure"}]}`)
	writeOpenPRView(t, gh, 2, c2, greenRollup)

	w2, err := startRun(ctx, client, world.RunSpec{Name: "worker-t2-gate", Cwd: wt2, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w2, "idle", now.Add(-time.Minute))

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Project.RunTokenHash = tokenHash("tok-coord")
	st.Settings.FlakeReruns = 0
	st.Asks = []flow.Ask{{
		ID: "ask-t2-gate", Project: id, Kind: flow.KindGate, Gated: true,
		Task: "T2", Head: c2, AskedBy: flow.AskedByN31,
		Question: "Approve T2 for merge?", Options: []string{flow.GateOptionMerge, flow.GateOptionHold},
		Recommend: flow.GateOptionMerge, Status: flow.AskStatusOpen, At: now.Add(-2 * time.Minute),
	}}
	st.Tasks = []flow.Task{
		{
			ID: "T1", Title: "feat: T1 red after yield", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: base, Branch: br1, Worktree: wt1, PR: 1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-t1-stopped",
					SessionID: "11111111-2222-4333-8444-555555555555", Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndStopped, EndedAt: now.Add(-3 * time.Minute),
				},
				Ready: []flow.Ready{{Head: c1}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
		{
			ID: "T2", Title: "feat: T2 at owner gate", Gate: flow.GateOwner, EstimateLines: 50,
			Brief:    "Initial gated implementation.",
			Outcomes: []flow.Outcome{{Text: "T2 works", ProvedBy: "cmd: true"}},
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: base, Branch: br2, Worktree: wt2, PR: 2, RunID: w2,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w2,
					SessionID: "22222222-2222-4333-8444-555555555555", Resumable: true,
					State: flow.PhaseIdle, Status: "idle", StatusSince: now.Add(-time.Minute), StatusReported: true,
				},
				Ready: []flow.Ready{{Head: c2}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c2, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	// Pass 1: T2 idle at N31 (owner_gate) yields its worker so red T1 can restart next pass.
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	a2 := st.Tasks[1].Attempts[0]
	if st.Tasks[1].LastRow != "N31" || a2.RunID != "" || a2.Run == nil || a2.Run.State != flow.PhaseEnded || a2.Run.Result != flow.EndStopped {
		t.Fatalf("T2 after pass 1 = row:%q run_id:%q run:%+v, want N31 ended(stopped)", st.Tasks[1].LastRow, a2.RunID, a2.Run)
	}
	if slotLines := logRules(t, paths.Log(id), "slot"); len(slotLines) != 1 || slotLines[0].Task != "T2" {
		t.Errorf("slot log lines after pass 1 = %+v, want 1 line for T2", slotLines)
	}

	// Pass 2: T1 resumes on the freed slot and works.
	now = now.Add(flow.PassInterval)
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	t1Run := st.Tasks[0].Attempts[0].RunID
	if t1Run == "" || st.Tasks[0].Attempts[0].Restarts != 0 {
		t.Fatalf("T1 after pass 2 = run_id:%q restarts:%d, want resumed worker with Restarts=0", t1Run, st.Tasks[0].Attempts[0].Restarts)
	}
	setTerminalStatusAt(t, srv, client, t1Run, "working", now)

	// Owner answers T2's gate ask with the real no option (Hold; Merge needs no worker).
	now = now.Add(time.Minute)
	answerOwnerAsk(t, eng, "ask-t2-gate", flow.GateOptionHold)

	// Pass 3: N30 opens owner_no recap for T2 while T1 keeps working.
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if rec := flow.OpenRecap(st.Tasks[1]); st.Tasks[1].LastRow != "N30" || rec == nil || rec.Trigger != flow.TriggerOwnerNo {
		t.Fatalf("T2 after owner Hold = row:%q recap:%+v, want N30 owner_no recap", st.Tasks[1].LastRow, rec)
	}

	// Coordinator amends T2 and decides new; T2 then takes the owner-answered extra slot alongside live T1.
	if err := eng.Report("tok-coord", "plan amend", json.RawMessage(`{"why":"owner chose Hold on gate","tasks":[{"id":"T2","brief":"Revised brief after owner Hold."}]}`)); err != nil {
		t.Fatalf("plan amend: %v", err)
	}
	if err := eng.Report("tok-coord", "recap decide", json.RawMessage(`{"task":"T2","choice":"new","why":"rework after owner Hold"}`)); err != nil {
		t.Fatalf("recap decide new: %v", err)
	}

	now = now.Add(flow.PassInterval)
	eng.passProject(ctx, id, false) // N11 opens attempt 2 on the extra slot
	now = now.Add(flow.PassInterval)
	eng.passProject(ctx, id, false) // N23 starts worker on attempt 2

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Tasks[1].Attempts) != 2 || st.Tasks[1].Attempts[1].RunID == "" {
		t.Fatalf("T2 after owner Hold + recap new = attempts:%d latest:%+v, want attempt 2 with live worker on extra slot", len(st.Tasks[1].Attempts), flow.OpenAttempt(st.Tasks[1]))
	}
	if st.Tasks[0].Attempts[0].RunID != t1Run {
		t.Errorf("T1 worker changed while T2 used extra slot: got %q, want %q", st.Tasks[0].Attempts[0].RunID, t1Run)
	}
}

// TestRedCIBehindBlockedHolderWaits pins O38's accepted unbounded wait
// (design.md 4.10): T2 idle at N5 (stage blocked_or_failed) keeps its slot, so
// red T1 gets no stop and waits with "no slot is free".
func TestRedCIBehindBlockedHolderWaits(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 24, 7, 0, 0, 0, time.UTC)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Red CI behind blocked holder waits", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt1, br1, c1 := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	wt2, br2, c2 := makeSlotWorktree(t, ctx, paths, repo, id, "T2", "t2.go")
	writeOpenPRView(t, gh, 1, c1, checkRollup("FAILURE", "44714", "1001"))
	writeGHFile(t, gh, "workflow-runs.json", `{"total_count":1,"workflow_runs":[{"id":44714,"status":"completed","conclusion":"failure"}]}`)

	w2, err := startRun(ctx, client, world.RunSpec{Name: "worker-t2-blocked", Cwd: wt2, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w2, "idle", now.Add(-time.Minute))

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Settings.FlakeReruns = 0
	st.Tasks = []flow.Task{
		{
			ID: "T1", Title: "feat: T1 red waiting on slot", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: base, Branch: br1, Worktree: wt1, PR: 1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: "worker-t1-stopped",
					SessionID: "11111111-2222-4333-8444-555555555555", Resumable: true,
					State: flow.PhaseEnded, Result: flow.EndStopped, EndedAt: now.Add(-3 * time.Minute),
				},
				Ready: []flow.Ready{{Head: c1}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
		{
			ID: "T2", Title: "feat: T2 idle at N5", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: base, Branch: br2, Worktree: wt2, RunID: w2,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w2,
					SessionID: "22222222-2222-4333-8444-555555555555", Resumable: true,
					State: flow.PhaseIdle, Status: "idle", StatusSince: now.Add(-time.Minute), StatusReported: true,
				},
				Ready:   []flow.Ready{{Head: c2}},
				Rounds:  []flow.Round{},
				Accepts: []flow.Accept{},
				Acts: []flow.Act{
					{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)},
					{Key: flow.AskPushPR + "@" + c2, Result: "failed", Class: flow.ClassSettled, Err: "push rejected", At: now.Add(-2 * time.Minute)},
					{Key: flow.AskWake + "@failed@" + flow.AskPushPR + "@" + c2, Result: "done", At: now.Add(-time.Minute)},
				},
			}},
		},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	eng.passProject(ctx, id, false)
	now = now.Add(flow.PassInterval)
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if st.Tasks[1].LastRow != "N5" || st.Tasks[1].Attempts[0].RunID != w2 {
		t.Fatalf("T2 at N5 did not keep its worker: row=%q run_id=%q (want N5 / %q)", st.Tasks[1].LastRow, st.Tasks[1].Attempts[0].RunID, w2)
	}
	if countRPC(srv, "terminal.kill") != 0 {
		t.Errorf("terminal.kill called %d times for N5 holder, want 0", countRPC(srv, "terminal.kill"))
	}
	if slotLines := logRules(t, paths.Log(id), "slot"); len(slotLines) != 0 {
		t.Errorf("slot log lines for N5 holder = %+v, want 0", slotLines)
	}
	if st.Tasks[0].LastRow != "N33" || st.Tasks[0].HoldWhy != "no slot is free" || st.Tasks[0].Attempts[0].RunID != "" {
		t.Fatalf("T1 behind N5 holder = row:%q hold:%q run_id:%q, want N33 / no slot is free / no worker", st.Tasks[0].LastRow, st.Tasks[0].HoldWhy, st.Tasks[0].Attempts[0].RunID)
	}
}

// TestYieldSkipsWorkerNoLongerIdle checks O38's pre-stop re-read: if an idle
// CI-waiting worker turns working between the pass's first worker read and the
// yield stop after the act loop, the re-read observes PhaseWorking and skips
// the kill (0 terminal.kill, 0 slot lines).
func TestYieldSkipsWorkerNoLongerIdle(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Yield skips worker no longer idle", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt1, br1, c1 := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	writeOpenPRView(t, gh, 1, c1, "["+ciCheck("ci", "IN_PROGRESS", "", "101", "1001")+"]")

	w1, err := startRun(ctx, client, world.RunSpec{Name: "worker-t1-flip", Cwd: wt1, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w1, "idle", now.Add(-time.Minute))

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Tasks = []flow.Task{
		{
			ID: "T1", Title: "feat: T1 turns working before yield", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: base, Branch: br1, Worktree: wt1, PR: 1, RunID: w1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w1,
					SessionID: "sess-t1-flip", Resumable: true,
					State: flow.PhaseIdle, Status: "idle", StatusSince: now.Add(-time.Minute), StatusReported: true,
				},
				Ready: []flow.Ready{{Head: c1}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
		{ID: "T2", Title: "feat: T2 queued", EstimateLines: 50},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	// Flip w1 from idle to working during actTaskLocked (after the pass's first
	// worker read and before the post-act-loop yield re-read).
	var flipped atomic.Bool
	eng.writeLog = func(path string, l LogLine) error {
		if l.Task == "T2" && l.Rule == "N10" && flipped.CompareAndSwap(false, true) {
			setTerminalStatusAt(t, srv, client, w1, "working", now)
		}
		return AppendLog(path, l)
	}

	eng.passProject(ctx, id, false)

	if !flipped.Load() {
		t.Fatal("test setup bug: N10 log hook did not fire to flip w1 to working")
	}
	if kills := countRPC(srv, "terminal.kill"); kills != 0 {
		t.Fatalf("terminal.kill called %d times after w1 turned working, want 0", kills)
	}
	if _, err := client.Get(ctx, w1); err != nil {
		t.Fatalf("w1 not live in hc after turning working: %v", err)
	}
	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	a1 := st.Tasks[0].Attempts[0]
	if a1.RunID != w1 || a1.Run == nil || a1.Run.State != flow.PhaseWorking {
		t.Fatalf("T1 after re-read = run_id:%q run:%+v, want live %s in PhaseWorking", a1.RunID, a1.Run, w1)
	}
	if slotLines := logRules(t, paths.Log(id), "slot"); len(slotLines) != 0 {
		t.Errorf("slot log lines = %+v, want 0", slotLines)
	}
}

// TestYieldStoreDirtyStopsNothing checks O38 hole 2 and round-2 correction 4:
// when writeState fails in the worker loop, OR during the yield loop's
// advanceRunLocked re-read just before the kill, cleanLocked stops nothing (0
// HC kills, 0 slot lines); once healthy, the next pass stops the idle worker.
func TestYieldStoreDirtyStopsNothing(t *testing.T) {
	setup := func(t *testing.T, initialPhase string) (context.Context, *hctest.Server, *hc.Client, Paths, *Engine, string, string, *time.Time) {
		t.Helper()
		ctx := context.Background()
		srv := hctest.New(t)
		client := hc.NewClient(srv.Path)
		paths := testPaths(t)
		repo, base := initGitRepoWithRemote(t)
		githubOrigin(t, repo)
		gh := bindRepoFakeGH(t, repo)

		now := time.Date(2026, 11, 24, 8, 30, 0, 0, time.UTC)
		eng := newTestEngine(t, paths, client)
		eng.Now = func() time.Time { return now }
		if err := eng.Start(repo, "Yield dirty store stops nothing", 1); err != nil {
			t.Fatal(err)
		}
		id := eng.projectDirs()[0].Name()
		srv.Add(hc.Terminal{
			Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
			Labels: testCoordLabels(id, "run-elsewhere"),
			Status: &hc.Status{App: "claude-code", State: "working"},
		})

		wt1, br1, c1 := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
		writeOpenPRView(t, gh, 1, c1, "["+ciCheck("ci", "IN_PROGRESS", "", "101", "1001")+"]")
		w1, err := startRun(ctx, client, world.RunSpec{Name: "worker-t1-dirty", Cwd: wt1, Argv: []string{"sh"}})
		if err != nil {
			t.Fatal(err)
		}
		srv.SetScreen(w1, "screen-1")
		setTerminalStatusAt(t, srv, client, w1, "idle", now.Add(-time.Minute))

		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		st.Project.RunID = "coord-elsewhere"
		st.Tasks = []flow.Task{
			{
				ID: "T1", Title: "feat: T1 waiting on CI", EstimateLines: 50,
				Attempts: []flow.Attempt{{
					SizeCap: 100, BaseCommit: base, Branch: br1, Worktree: wt1, PR: 1, RunID: w1,
					Run: &flow.RunRecord{
						Role: flow.RoleWorker, CLI: "claude", Address: w1,
						SessionID: "sess-t1-dirty", Resumable: true,
						State: initialPhase, Status: "idle", StatusSince: now.Add(-time.Minute), StatusReported: true,
					},
					Ready: []flow.Ready{{Head: c1}},
					Rounds: []flow.Round{
						{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
						{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					},
					Accepts: []flow.Accept{},
					Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
				}},
			},
			{ID: "T2", Title: "feat: T2 queued", EstimateLines: 50},
		}
		if err := SaveState(paths.State(id), st); err != nil {
			t.Fatal(err)
		}
		return ctx, srv, client, paths, eng, id, w1, &now
	}

	t.Run("dirty_in_worker_loop", func(t *testing.T) {
		ctx, srv, client, paths, eng, id, w1, now := setup(t, flow.PhaseWorking)

		var failedWorkerSave atomic.Bool
		eng.writeState = func(path string, s flow.State) error {
			if len(s.Tasks) > 0 && len(s.Tasks[0].Attempts) > 0 && s.Tasks[0].Attempts[0].Run != nil &&
				s.Tasks[0].Attempts[0].Run.State == flow.PhaseIdle && failedWorkerSave.CompareAndSwap(false, true) {
				return errors.New("state write failed in worker loop")
			}
			return SaveState(path, s)
		}

		eng.passProject(ctx, id, false)

		if !failedWorkerSave.Load() {
			t.Fatal("test setup bug: worker-loop writeState failure did not trigger")
		}
		if kills := countRPC(srv, "terminal.kill"); kills != 0 {
			t.Fatalf("terminal.kill called %d times with dirty store from worker loop, want 0", kills)
		}
		if _, err := client.Get(ctx, w1); err != nil {
			t.Fatalf("w1 not live after dirty worker-loop pass: %v", err)
		}
		if slotLines := logRules(t, paths.Log(id), "slot"); len(slotLines) != 0 {
			t.Errorf("slot log lines after dirty pass = %+v, want 0", slotLines)
		}

		// Recovery pass succeeds and stops the idle worker.
		*now = now.Add(flow.PassInterval)
		eng.passProject(ctx, id, false)
		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		if a1 := st.Tasks[0].Attempts[0]; a1.RunID != "" || a1.Run == nil || a1.Run.Result != flow.EndStopped {
			t.Fatalf("T1 after healthy recovery pass = run_id:%q run:%+v, want ended(stopped)", a1.RunID, a1.Run)
		}
	})

	t.Run("dirty_in_yield_reread", func(t *testing.T) {
		ctx, srv, client, paths, eng, id, w1, now := setup(t, flow.PhaseIdle)

		var armed, failedRereadSave atomic.Bool
		eng.writeState = func(path string, s flow.State) error {
			if armed.CompareAndSwap(true, false) {
				failedRereadSave.Store(true)
				return errors.New("state write failed in yield re-read")
			}
			err := SaveState(path, s)
			if err == nil && len(s.Tasks) > 1 && s.Tasks[1].LastRow == "N10" && !failedRereadSave.Load() {
				srv.SetScreen(w1, "screen-2")
				armed.Store(true)
			}
			return err
		}

		eng.passProject(ctx, id, false)

		if !failedRereadSave.Load() {
			t.Fatal("test setup bug: yield re-read writeState failure did not trigger")
		}
		if kills := countRPC(srv, "terminal.kill"); kills != 0 {
			t.Fatalf("terminal.kill called %d times after yield re-read dirtied store, want 0", kills)
		}
		if _, err := client.Get(ctx, w1); err != nil {
			t.Fatalf("w1 not live after dirty yield re-read pass: %v", err)
		}
		if slotLines := logRules(t, paths.Log(id), "slot"); len(slotLines) != 0 {
			t.Errorf("slot log lines after dirty yield re-read = %+v, want 0", slotLines)
		}

		// Recovery pass succeeds and stops the idle worker.
		*now = now.Add(flow.PassInterval)
		eng.passProject(ctx, id, false)
		st, err := LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		if a1 := st.Tasks[0].Attempts[0]; a1.RunID != "" || a1.Run == nil || a1.Run.Result != flow.EndStopped {
			t.Fatalf("T1 after healthy recovery pass = run_id:%q run:%+v, want ended(stopped)", a1.RunID, a1.Run)
		}
	})
}

// TestYieldCommitFails checks O38 hole 2 (design.md 4.10): when the kill
// succeeds and writeLog fails on the "slot" line, state.json still holds the
// live worker record. On the next healthy pass, R33 records the gone terminal
// as exited/lost (not stopped), so a later restart obeys back-off and is
// counted (Restarts 1).
func TestYieldCommitFails(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	if err := eng.Start(repo, "Yield commit fails is counted exit", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt1, br1, c1 := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	writeOpenPRView(t, gh, 1, c1, "["+ciCheck("ci", "IN_PROGRESS", "", "101", "1001")+"]")
	w1, err := startRun(ctx, client, world.RunSpec{Name: "worker-t1-commitfail", Cwd: wt1, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w1, "idle", now.Add(-time.Minute))

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Settings.FlakeReruns = 0
	st.Settings.EnsureBackoffMinutes = 1
	st.Tasks = []flow.Task{
		{
			ID: "T1", Title: "feat: T1 yield commit fails", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: base, Branch: br1, Worktree: wt1, PR: 1, RunID: w1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w1,
					SessionID: "11111111-2222-4333-8444-555555555555", Resumable: true,
					State: flow.PhaseIdle, Status: "idle", StatusSince: now.Add(-time.Minute), StatusReported: true,
				},
				Ready: []flow.Ready{{Head: c1}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
		{ID: "T2", Title: "feat: T2 queued", EstimateLines: 50},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	var failedSlot atomic.Bool
	eng.writeLog = func(path string, l LogLine) error {
		if l.Rule == "slot" && failedSlot.CompareAndSwap(false, true) {
			return errors.New("log write failed on slot line")
		}
		return AppendLog(path, l)
	}

	// Pass 1: kill succeeds in hc, commit fails on the slot log line -> state.json keeps w1 live.
	eng.passProject(ctx, id, false)

	if !failedSlot.Load() {
		t.Fatal("yield stop did not attempt to write the slot log line")
	}
	if _, err := client.Get(ctx, w1); !errors.Is(err, hc.ErrNotFound) {
		t.Fatalf("w1 still present in hc after kill: err=%v", err)
	}
	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if a1 := st.Tasks[0].Attempts[0]; a1.RunID != w1 || a1.Run == nil || a1.Run.State != flow.PhaseIdle {
		t.Fatalf("state.json mutated despite failed slot commit: run_id=%q run=%+v", a1.RunID, a1.Run)
	}

	// Pass 2 (healthy): R33 sees the gone terminal on the live record -> ended(lost/exited), not stopped.
	now = now.Add(flow.PassInterval)
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	a1 := st.Tasks[0].Attempts[0]
	if a1.RunID != "" || a1.Run == nil || a1.Run.State != flow.PhaseEnded ||
		(a1.Run.Result != flow.EndExited && a1.Run.Result != flow.EndLost) {
		t.Fatalf("T1 after pass 2 = run_id:%q run:%+v, want ended(exited|lost) via R33", a1.RunID, a1.Run)
	}
	if r33 := logRules(t, paths.Log(id), "R33"); len(r33) != 1 || r33[0].Task != "T1" {
		t.Errorf("R33 log lines after pass 2 = %+v, want 1 line for T1", r33)
	}

	// Free T2's slot and turn T1 red: T1 is in back-off inside 1m, then restarts counted (Restarts == 1).
	st.Tasks[1].Done = &flow.Merged{MergedCommit: "m2"}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}
	writeOpenPRView(t, gh, 1, c1, checkRollup("FAILURE", "44714", "1001"))
	writeGHFile(t, gh, "workflow-runs.json", `{"total_count":1,"workflow_runs":[{"id":44714,"status":"completed","conclusion":"failure"}]}`)

	// Inside 1m EnsureBackoffMinutes: held in back-off (no worker launched yet).
	now = now.Add(15 * time.Second)
	eng.passProject(ctx, id, false)
	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if a1 := st.Tasks[0].Attempts[0]; a1.RunID != "" || a1.Restarts != 0 {
		t.Fatalf("T1 restarted during exit back-off: run_id=%q restarts=%d", a1.RunID, a1.Restarts)
	}

	// Past 1m back-off: restarts via counted start_worker@resume@1 with Restarts == 1.
	now = now.Add(time.Minute)
	eng.passProject(ctx, id, false)
	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	a1 = st.Tasks[0].Attempts[0]
	if a1.RunID == "" || a1.Restarts != 1 || !flow.Done(a1.Acts, flow.AskStartWorker+"@"+flow.ContinueResume+"@1") {
		t.Fatalf("T1 after exit back-off = run_id:%q restarts:%d acts:%+v, want counted restart (Restarts=1)", a1.RunID, a1.Restarts, a1.Acts)
	}
}

// TestYieldStopFailureIsNotN5 checks O38 hole 2 (design.md 4.10): when the
// yield stop fails before the kill for four passes, it is logged via logError
// and retried without recording any counted act or moving T1 to N5; T1 stays
// at N36 and yields once HC recovers.
func TestYieldStopFailureIsNotN5(t *testing.T) {
	ctx := context.Background()
	srv := hctest.New(t)
	client := hc.NewClient(srv.Path)
	paths := testPaths(t)
	repo, base := initGitRepoWithRemote(t)
	githubOrigin(t, repo)
	gh := bindRepoFakeGH(t, repo)

	var logBuf bytes.Buffer
	now := time.Date(2026, 11, 24, 9, 30, 0, 0, time.UTC)
	eng := newTestEngine(t, paths, client)
	eng.Now = func() time.Time { return now }
	eng.Log = logging.NewJSONLLogger(&logBuf)
	if err := eng.Start(repo, "Yield stop failure is not N5", 1); err != nil {
		t.Fatal(err)
	}
	id := eng.projectDirs()[0].Name()
	srv.Add(hc.Terminal{
		Address: "coord-elsewhere", Name: "coord-elsewhere", State: "live",
		Labels: testCoordLabels(id, "run-elsewhere"),
		Status: &hc.Status{App: "claude-code", State: "working"},
	})

	wt1, br1, c1 := makeSlotWorktree(t, ctx, paths, repo, id, "T1", "t1.go")
	writeOpenPRView(t, gh, 1, c1, "["+ciCheck("ci", "IN_PROGRESS", "", "101", "1001")+"]")
	w1, err := startRun(ctx, client, world.RunSpec{Name: "worker-t1-stoperr", Cwd: wt1, Argv: []string{"sh"}})
	if err != nil {
		t.Fatal(err)
	}
	setTerminalStatusAt(t, srv, client, w1, "idle", now.Add(-time.Minute))

	st, err := LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	st.Project.RunID = "coord-elsewhere"
	st.Settings.EnsureFailures = 3
	st.Tasks = []flow.Task{
		{
			ID: "T1", Title: "feat: T1 stop errors", EstimateLines: 50,
			Attempts: []flow.Attempt{{
				SizeCap: 100, BaseCommit: base, Branch: br1, Worktree: wt1, PR: 1, RunID: w1,
				Run: &flow.RunRecord{
					Role: flow.RoleWorker, CLI: "claude", Address: w1,
					SessionID: "sess-t1-stoperr", Resumable: true,
					State: flow.PhaseIdle, Status: "idle", StatusSince: now.Add(-time.Minute), StatusReported: true,
				},
				Ready: []flow.Ready{{Head: c1}},
				Rounds: []flow.Round{
					{N: 1, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
					{N: 2, Kind: flow.RoundFull, Head: c1, Verdict: flow.VerdictPass},
				},
				Accepts: []flow.Accept{},
				Acts:    []flow.Act{{Key: flow.AskStartWorker, Result: "done", At: now.Add(-10 * time.Minute)}},
			}},
		},
		{ID: "T2", Title: "feat: T2 queued", EstimateLines: 50},
	}
	if err := SaveState(paths.State(id), st); err != nil {
		t.Fatal(err)
	}

	var failKill atomic.Bool
	failKill.Store(true)
	eng.HC = hc.NewClient(methodFailProxy(t, srv.Path, "terminal.kill", &failKill))

	// Four failing passes (exceeding EnsureFailures=3): T1 stays N36, never N5, no stop_runs act.
	for i := range 4 {
		eng.passProject(ctx, id, false)
		now = now.Add(2 * time.Minute)
		st, err = LoadState(paths.State(id))
		if err != nil {
			t.Fatal(err)
		}
		if st.Tasks[0].LastRow != "N36" || st.Tasks[0].Attempts[0].RunID != w1 {
			t.Fatalf("pass %d: T1 = row:%q run_id:%q, want N36 keeping %q", i+1, st.Tasks[0].LastRow, st.Tasks[0].Attempts[0].RunID, w1)
		}
		if flow.Count(st.Tasks[0].Attempts[0].Acts, flow.AskStopRuns) != 0 || flow.HasExhausted(st.Tasks[0].Attempts[0].Acts, st.Settings) {
			t.Fatalf("pass %d: yield stop failure polluted acts: %+v", i+1, st.Tasks[0].Attempts[0].Acts)
		}
		if len(st.Project.Wakes) != 0 {
			t.Fatalf("pass %d: yield stop failure queued wake: %+v", i+1, st.Project.Wakes)
		}
		if flow.OpenAttempt(st.Tasks[1]) != nil {
			t.Fatalf("pass %d: T2 started while w1 stop failed", i+1)
		}
	}
	if got := strings.Count(logBuf.String(), "terminal.kill failed"); got < 4 {
		t.Fatalf("daemon log recorded %d terminal.kill failures across 4 passes, want >= 4:\n%s", got, logBuf.String())
	}

	// Recovery: pass 5 stops T1's idle worker; pass 6 opens T2's attempt.
	failKill.Store(false)
	eng.passProject(ctx, id, false)
	now = now.Add(flow.PassInterval)
	eng.passProject(ctx, id, false)

	st, err = LoadState(paths.State(id))
	if err != nil {
		t.Fatal(err)
	}
	if a1 := st.Tasks[0].Attempts[0]; a1.RunID != "" || a1.Run == nil || a1.Run.Result != flow.EndStopped {
		t.Fatalf("T1 after recovery = run_id:%q run:%+v, want ended(stopped)", a1.RunID, a1.Run)
	}
	if flow.OpenAttempt(st.Tasks[1]) == nil {
		t.Fatalf("T2 did not start after yield stop recovered: row=%q", st.Tasks[1].LastRow)
	}
}
