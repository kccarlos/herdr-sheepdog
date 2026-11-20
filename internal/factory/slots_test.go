package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
	eng := New(paths, nil)

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
	_, id, err := world.RepoProject(ctx, repo, false)
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
	eng := New(paths, nil)
	if err := eng.Start(repo, "Goal", 2); err != nil {
		t.Fatal(err)
	}
	_, id, err := world.RepoProject(ctx, repo, false)
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
	eng := New(paths, client)
	if err := eng.Start(repo, "Goal", 2); err != nil {
		t.Fatal(err)
	}
	_, id, err := world.RepoProject(ctx, repo, false)
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
	eng := New(paths, client)
	if err := eng.Start(repo, "Goal", 2); err != nil {
		t.Fatal(err)
	}
	_, id, err := world.RepoProject(ctx, repo, false)
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
	srv.SetStatus(coord, &hc.Status{App: "claude-code", State: "idle"})
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
	eng := New(paths, nil)
	if err := eng.Start(repo, "Goal", 1); err != nil {
		t.Fatal(err)
	}
	_, id, err := world.RepoProject(ctx, repo, false)
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
