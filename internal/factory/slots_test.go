package factory

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
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

	// Invalid max_running is refused without saving.
	for _, bad := range []int{0, -1, flow.MaxRunningMax + 1} {
		if err := eng.Start(repo, "Goal", bad); err == nil {
			t.Fatalf("Start with max_running=%d succeeded, want error", bad)
		}
	}
	if err := eng.Start(repo, "Goal", 2); err != nil {
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
