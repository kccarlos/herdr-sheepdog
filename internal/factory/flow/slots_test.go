package flow

import (
	"slices"
	"testing"
)

// TestGrantSlots checks design.md 4.10 and KB-TSK-16: ordering by urgent,
// critical path (longest remaining chain of estimate_lines through after),
// then oldest, with up to max_running - running slots plus one extra slot for
// an urgent task.
func TestGrantSlots(t *testing.T) {
	cases := []struct {
		name       string
		tasks      []Task
		candidates []string
		running    int
		maxRunning int
		want       []string
	}{
		{
			name: "cap_limits_normal_tasks_oldest_first",
			tasks: []Task{
				{ID: "T1", EstimateLines: 50},
				{ID: "T2", EstimateLines: 50},
				{ID: "T3", EstimateLines: 50},
			},
			candidates: []string{"T3", "T2", "T1"},
			running:    0, maxRunning: 2,
			want: []string{"T1", "T2"},
		},
		{
			name: "running_tasks_subtract_from_cap",
			tasks: []Task{
				{ID: "T1", EstimateLines: 50},
				{ID: "T2", EstimateLines: 50},
			},
			candidates: []string{"T1", "T2"},
			running:    1, maxRunning: 2,
			want: []string{"T1"},
		},
		{
			name: "at_cap_no_normal_task_starts",
			tasks: []Task{
				{ID: "T1", EstimateLines: 50},
			},
			candidates: []string{"T1"},
			running:    2, maxRunning: 2,
			want: nil,
		},
		{
			name: "urgent_beats_longer_critical_path_and_older",
			tasks: []Task{
				{ID: "T1", EstimateLines: 300},
				{ID: "T2", EstimateLines: 10, Priority: PriorityUrgent},
			},
			candidates: []string{"T1", "T2"},
			running:    1, maxRunning: 2,
			want: []string{"T2"},
		},
		{
			name: "critical_path_through_after_beats_older_and_larger_solo",
			tasks: []Task{
				{ID: "T1", EstimateLines: 100},
				{ID: "T2", EstimateLines: 50},
				{ID: "T3", EstimateLines: 200, After: []string{"T2"}},
			},
			candidates: []string{"T1", "T2"},
			running:    1, maxRunning: 2,
			want: []string{"T2"}, // T2->T3 = 250 > T1 = 100
		},
		{
			name: "critical_path_ignores_ended_downstream",
			tasks: []Task{
				{ID: "T1", EstimateLines: 100},
				{ID: "T2", EstimateLines: 50},
				{ID: "T3", EstimateLines: 300, After: []string{"T2"}, Done: &Merged{MergedCommit: "m1"}},
			},
			candidates: []string{"T1", "T2"},
			running:    1, maxRunning: 2,
			want: []string{"T1"}, // T3 is done, so T2 is 50 < T1 100
		},
		{
			name: "critical_path_diamond_takes_longest_branch",
			tasks: []Task{
				{ID: "A", EstimateLines: 10},
				{ID: "B", EstimateLines: 50, After: []string{"A"}},
				{ID: "C", EstimateLines: 150, After: []string{"A"}},
				{ID: "D", EstimateLines: 20, After: []string{"B", "C"}},
				{ID: "E", EstimateLines: 170},
			},
			candidates: []string{"E", "A"},
			running:    1, maxRunning: 2,
			want: []string{"A"}, // A->C->D = 180 > E = 170
		},
		{
			name: "extra_urgent_slot_at_cap",
			tasks: []Task{
				{ID: "N1", EstimateLines: 200},
				{ID: "U1", EstimateLines: 10, Priority: PriorityUrgent},
				{ID: "U2", EstimateLines: 10, Priority: PriorityUrgent},
			},
			candidates: []string{"N1", "U1", "U2"},
			running:    2, maxRunning: 2,
			want: []string{"U1"}, // only one slot beyond max_running
		},
		{
			name: "no_extra_slot_when_already_above_cap",
			tasks: []Task{
				{ID: "U1", EstimateLines: 10, Priority: PriorityUrgent},
			},
			candidates: []string{"U1"},
			running:    3, maxRunning: 2,
			want: nil,
		},
		{
			name: "urgent_with_free_slot_does_not_push_normal_over_cap",
			tasks: []Task{
				{ID: "U1", EstimateLines: 10, Priority: PriorityUrgent},
				{ID: "N1", EstimateLines: 50},
				{ID: "N2", EstimateLines: 50},
			},
			candidates: []string{"U1", "N1", "N2"},
			running:    0, maxRunning: 2,
			want: []string{"U1", "N1"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GrantSlots(c.candidates, c.tasks, c.running, c.maxRunning)
			var ids []string
			for id := range got {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			want := slices.Clone(c.want)
			slices.Sort(want)
			if !slices.Equal(ids, want) {
				t.Errorf("GrantSlots = %v, want %v", ids, want)
			}
		})
	}
}

func TestUnmetDepsAndRunningTasks(t *testing.T) {
	tasks := []Task{
		{ID: "T1", Done: &Merged{MergedCommit: "m1"}},
		{ID: "T2", Dropped: &Dropped{Why: "no longer needed"}},
		{ID: "T3", Attempts: []Attempt{{BaseCommit: "b0"}}},
		{ID: "T4", Parked: true, Attempts: []Attempt{{BaseCommit: "b0"}}},
		{ID: "T5", After: []string{"T1", "T2", "T3", "MISSING"}},
		{ID: "T6", Attempts: []Attempt{{BaseCommit: "b0", RunID: "w6"}}},
		{ID: "T7", Attempts: []Attempt{{BaseCommit: "b0", RunID: "w7"}}},
		{ID: "T8", Attempts: []Attempt{{BaseCommit: "b0", RunID: "w8"}}},
		{ID: "T9", Attempts: []Attempt{{BaseCommit: "b0", Acts: []Act{{Key: AskStopTask, Result: "not_built_yet"}}}}},
	}
	notDone, dropped := UnmetDeps(tasks[4], tasks)
	if !slices.Equal(notDone, []string{"T3", "MISSING"}) || !slices.Equal(dropped, []string{"T2"}) {
		t.Errorf("UnmetDeps = (%v, %v), want ([T3 MISSING], [T2])", notDone, dropped)
	}
	ws := map[string]RunState{"T6": RunWorking, "T7": RunIdle, "T8": RunGone}
	if n := RunningTasks(tasks, ws); n != 3 {
		t.Errorf("RunningTasks = %d, want 3 (T3 starting + T6 working + T7 idle; not T8 gone, T9 stop_task)", n)
	}
}

func TestReviewerGoneS1KeepsSlotWhileWorkerRuns(t *testing.T) {
	tasks := []Task{
		{ID: "T1", Attempts: []Attempt{{BaseCommit: "b0", RunID: "w1", Acts: []Act{{Key: AskStopTask, Result: "not_built_yet"}}}}},
		{ID: "T2", Attempts: []Attempt{{BaseCommit: "b0", RunID: "w2", Acts: []Act{{Key: AskStopTask, Result: "not_built_yet"}}}}},
		{ID: "T3", Attempts: []Attempt{{BaseCommit: "b0", RunID: "w3", Acts: []Act{{Key: AskStopTask, Result: "not_built_yet"}}}}},
	}
	ws := map[string]RunState{"T1": RunWorking, "T2": RunIdle, "T3": RunGone}
	if n := RunningTasks(tasks, ws); n != 2 {
		t.Errorf("RunningTasks = %d, want 2 (T1 working + T2 idle keep slots despite stop_task; T3 gone frees slot)", n)
	}
}

func TestClosedDepCountsAsSatisfied(t *testing.T) {
	tasks := []Task{
		{ID: "T1", Closed: &Closed{Why: "already done upstream"}},
		{ID: "T2", After: []string{"T1"}},
	}
	notDone, dropped := UnmetDeps(tasks[1], tasks)
	if len(notDone) != 0 || len(dropped) != 0 {
		t.Fatalf("UnmetDeps for closed dep = (%v, %v), want ([], [])", notDone, dropped)
	}
	r, d := FirstMatch(TaskRows, In{Task: tasks[1], Settings: DefaultSettings(), Facts: Facts{DepsNotDone: notDone, DepsDropped: dropped}})
	if r.ID != "N11" || d.Why == "waiting on T1" {
		t.Fatalf("T2 matched %s (%q), want N11", r.ID, d.Why)
	}
}
