package flow

import (
	"slices"
	"testing"
	"time"
)

// TestGrantSlots checks design.md 4.10 and KB-TSK-16: ordering by owner-answered,
// urgent, open attempt, critical path (longest remaining chain of estimate_lines
// through after), then oldest, with up to max_running - running slots plus one
// extra slot for an owner-answered or urgent task.
func TestGrantSlots(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 21, 38, 0, 0, time.UTC)
	cases := []struct {
		name       string
		tasks      []Task
		candidates []string
		asks       []Ask
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
		{
			name: "open_attempt_beats_new_attempt_with_longer_critical_path",
			tasks: []Task{
				{ID: "T3", EstimateLines: 300},
				{ID: "T5", EstimateLines: 200, Attempts: []Attempt{{BaseCommit: "b0"}}},
			},
			candidates: []string{"T3", "T5"},
			running:    0, maxRunning: 1,
			want: []string{"T5"},
		},
		{
			name: "owner_answered_beats_urgent_and_open_attempt",
			tasks: []Task{
				{ID: "O1", EstimateLines: 10},
				{ID: "U1", EstimateLines: 10, Priority: PriorityUrgent},
				{ID: "T5", EstimateLines: 200, Attempts: []Attempt{{BaseCommit: "b0"}}},
				{ID: "T3", EstimateLines: 300},
			},
			candidates: []string{"T3", "T5", "U1", "O1"},
			asks: []Ask{{
				ID: "ask-1", Task: "O1", Gated: true,
				Status: AskStatusAnswered, AnsweredBy: "owner", AnsweredAt: t0,
			}},
			running: 0, maxRunning: 1,
			want: []string{"O1"},
		},
		{
			name: "urgent_beats_open_attempt_and_new_attempt",
			tasks: []Task{
				{ID: "U1", EstimateLines: 10, Priority: PriorityUrgent},
				{ID: "T5", EstimateLines: 200, Attempts: []Attempt{{BaseCommit: "b0"}}},
				{ID: "T3", EstimateLines: 300},
			},
			candidates: []string{"T3", "T5", "U1"},
			running:    0, maxRunning: 1,
			want: []string{"U1"},
		},
		{
			name: "owner_answered_and_urgent_beat_open_attempt_which_beats_new",
			tasks: []Task{
				{ID: "O1", EstimateLines: 10},
				{ID: "U1", EstimateLines: 10, Priority: PriorityUrgent},
				{ID: "T5", EstimateLines: 200, Attempts: []Attempt{{BaseCommit: "b0"}}},
				{ID: "T3", EstimateLines: 300},
			},
			candidates: []string{"T3", "T5", "U1", "O1"},
			asks: []Ask{{
				ID: "ask-1", Task: "O1", Gated: true,
				Status: AskStatusAnswered, AnsweredBy: "owner", AnsweredAt: t0,
			}},
			running: 0, maxRunning: 3,
			want: []string{"O1", "U1", "T5"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := GrantSlots(c.candidates, c.tasks, c.running, c.maxRunning, c.asks...)
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
		{ID: "T9", Attempts: []Attempt{{BaseCommit: "b0", Run: &RunRecord{State: PhaseEnded, Result: EndExited}}}},
	}
	notDone, dropped := UnmetDeps(tasks[4], tasks)
	if !slices.Equal(notDone, []string{"T3", "MISSING"}) || !slices.Equal(dropped, []string{"T2"}) {
		t.Errorf("UnmetDeps = (%v, %v), want ([T3 MISSING], [T2])", notDone, dropped)
	}
	ws := map[string]RunState{"T6": RunWorking, "T7": RunIdle, "T8": RunGone}
	if n := RunningTasks(tasks, ws); n != 3 {
		t.Errorf("RunningTasks = %d, want 3 (T3 starting + T6 working + T7 idle; not T8/T9 ended)", n)
	}
}

func TestReviewerGoneKeepsSlotWhileWorkerRuns(t *testing.T) {
	tasks := []Task{
		{ID: "T1", Attempts: []Attempt{{BaseCommit: "b0", RunID: "w1", Review: &ReviewRun{Run: &RunRecord{State: PhaseEnded, Result: EndExited}}}}},
		{ID: "T2", Attempts: []Attempt{{BaseCommit: "b0", RunID: "w2", Review: &ReviewRun{Run: &RunRecord{State: PhaseEnded, Result: EndExited}}}}},
		{ID: "T3", Attempts: []Attempt{{BaseCommit: "b0", Run: &RunRecord{State: PhaseEnded, Result: EndExited}}}},
	}
	ws := map[string]RunState{"T1": RunWorking, "T2": RunIdle, "T3": RunGone}
	if n := RunningTasks(tasks, ws); n != 2 {
		t.Errorf("RunningTasks = %d, want 2 (T1 working + T2 idle keep slots despite ended reviewer; T3 gone frees slot)", n)
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

func TestCriticalPathsCycle(t *testing.T) {
	tasks := []Task{
		{ID: "A", EstimateLines: 100, After: []string{"B"}},
		{ID: "B", EstimateLines: 50, After: []string{"A"}},
		{ID: "C", EstimateLines: 80},
	}
	crit := criticalPaths(tasks)
	if crit["A"] != 150 || crit["B"] != 50 || crit["C"] != 80 {
		t.Fatalf("criticalPaths(cycle) = %v, want A=150 B=50 C=80", crit)
	}
	if got := GrantSlots([]string{"B", "C"}, tasks, 0, 1); !got["C"] || got["B"] {
		t.Fatalf("GrantSlots(cycle) = %v, want C", got)
	}
}

// TestExhaustedNewAttemptReachesN5WhenNoSlot checks that when Facts.NoSlot is
// true, N5's probe pass skips N10 to inspect N11 and surface an exhausted
// new_attempt failure, while a non-exhausted task still lands on N10.
func TestExhaustedNewAttemptReachesN5WhenNoSlot(t *testing.T) {
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	settings := DefaultSettings()

	t.Run("settled_failure_reaches_N5", func(t *testing.T) {
		task := Task{
			ID: "T1",
			Acts: []Act{{
				Key: AskNewAttempt, Result: "failed", Class: ClassSettled,
				Err: "worktree path blocked", At: t0,
			}},
		}
		r, d := FirstMatch(TaskRows, In{
			Task: task, Settings: settings, Facts: Facts{NoSlot: true}, Now: t0.Add(time.Minute),
		})
		if r.ID != "N5" || !slices.Equal(d.Asked, []string{"wake_coordinator@failed@new_attempt"}) {
			t.Fatalf("exhausted settled new_attempt with NoSlot=true = %s %v (%q), want N5 wake_coordinator@failed@new_attempt", r.ID, d.Asked, d.Why)
		}
	})

	t.Run("transient_failures_at_limit_reach_N5", func(t *testing.T) {
		acts := make([]Act, settings.EnsureFailures)
		for i := range acts {
			acts[i] = Act{
				Key: AskNewAttempt, Result: "failed", Class: ClassTransient,
				Err: "git worktree failed", At: t0.Add(time.Duration(i) * time.Minute),
			}
		}
		task := Task{ID: "T2", Acts: acts}
		r, d := FirstMatch(TaskRows, In{
			Task: task, Settings: settings, Facts: Facts{NoSlot: true}, Now: t0.Add(time.Hour),
		})
		if r.ID != "N5" || !slices.Equal(d.Asked, []string{"wake_coordinator@failed@new_attempt"}) {
			t.Fatalf("exhausted transient new_attempt with NoSlot=true = %s %v (%q), want N5 wake_coordinator@failed@new_attempt", r.ID, d.Asked, d.Why)
		}
	})

	t.Run("non_exhausted_stays_at_N10", func(t *testing.T) {
		task := Task{
			ID: "T3",
			Acts: []Act{{
				Key: AskNewAttempt, Result: "failed", Class: ClassTransient,
				Err: "temporary error", At: t0,
			}},
		}
		r, d := FirstMatch(TaskRows, In{
			Task: task, Settings: settings, Facts: Facts{NoSlot: true}, Now: t0.Add(time.Hour),
		})
		if r.ID != "N10" || len(d.Asked) != 0 || d.Why != "no slot is free" {
			t.Fatalf("non-exhausted new_attempt with NoSlot=true = %s %v (%q), want N10 no slot is free", r.ID, d.Asked, d.Why)
		}
	})
}

// TestYieldSlots checks O38 (design.md 4.10): an idle worker whose task,
// matched on a copy as the stop would leave it (RunGone, EndStopped, NoSlot
// false, in.probe false), lands on a ci or owner_gate row that asks nothing
// and has no open ask from its worker yields its slot when starts are refused,
// oldest first, counting used = running + granted against max_running, without
// mutating any input task's Run record.
func TestYieldSlots(t *testing.T) {
	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	ciPassedTask := func(id, head string, restarts int, extraActs ...Act) Task {
		acts := append([]Act{{Key: AskStartWorker, Result: "done", At: t0.Add(-10 * time.Minute)}}, extraActs...)
		return Task{
			ID: id, Title: "feat: " + id, EstimateLines: 50,
			Attempts: []Attempt{{
				SizeCap: 100, BaseCommit: "b0", Branch: "br-" + id, Worktree: "/wt/" + id, PR: 1,
				RunID: "w-" + id, Restarts: restarts,
				Run: &RunRecord{
					Role: RoleWorker, CLI: "claude", Address: "w-" + id,
					SessionID: "sess-" + id, Resumable: true,
					State: PhaseIdle, Status: "idle", StatusSince: t0.Add(-time.Minute), StatusReported: true,
				},
				Ready: []Ready{{Head: head}},
				Rounds: []Round{
					{N: 1, Kind: RoundFull, Head: head, Verdict: VerdictPass},
					{N: 2, Kind: RoundFull, Head: head, Verdict: VerdictPass},
				},
				Acts: acts,
			}},
		}
	}
	ciPendingFacts := func(head string) Facts {
		return Facts{
			Tip: head, TipRel: RelSame, Worker: Run{State: RunIdle},
			PR: PR{
				Answered: true, Number: 1, State: PROpen, Head: head, HeadRel: RelSame,
				HeadSince: t0.Add(-2 * time.Minute), Mergeable: MergeableClean, Checks: ChecksPending,
			},
		}
	}
	ciRedFacts := func(head string) Facts {
		return Facts{
			Tip: head, TipRel: RelSame, Worker: Run{State: RunIdle},
			PR: PR{
				Answered: true, Number: 1, State: PROpen, Head: head, HeadRel: RelSame,
				HeadSince: t0.Add(-2 * time.Minute), Mergeable: MergeableClean, Checks: ChecksRed,
				Failed: []Failure{{Run: "44714", Job: "100", Name: "check"}},
			},
		}
	}

	workingTask := ciPassedTask("T1", "c1", 0)
	workingTask.Attempts[0].Run.State = PhaseWorking
	workingTask.Attempts[0].Run.Status = "working"
	workingFacts := ciPendingFacts("c1")
	workingFacts.Worker.State = RunWorking

	n29Task := ciPassedTask("T1", "c1", 0)
	n29Task.Attempts[0].Rounds = []Round{{N: 1, Kind: RoundFull, Head: "c1", Verdict: VerdictPass}}

	n31Task := ciPassedTask("T1", "c1", 0)
	n31Task.Gate = GateOwner

	n5Task := Task{
		ID: "T1", Title: "feat: T1 blocked at N5", EstimateLines: 50,
		Attempts: []Attempt{{
			SizeCap: 100, BaseCommit: "b0", Branch: "br-T1", Worktree: "/wt/T1", RunID: "w-T1",
			Run: &RunRecord{
				Role: RoleWorker, CLI: "claude", Address: "w-T1",
				SessionID: "sess-T1", Resumable: true,
				State: PhaseIdle, Status: "idle", StatusSince: t0.Add(-time.Minute), StatusReported: true,
			},
			Ready: []Ready{{Head: "c1"}},
			Acts: []Act{
				{Key: AskStartWorker, Result: "done", At: t0.Add(-10 * time.Minute)},
				{Key: AskPushPR + "@c1", Result: "failed", Class: ClassSettled, Err: "push rejected", At: t0.Add(-2 * time.Minute)},
				{Key: AskWake + "@failed@" + AskPushPR + "@c1", Result: "done", At: t0.Add(-time.Minute)},
			},
		}},
	}

	workingHolder := ciPassedTask("T3", "c3", 0)
	workingHolder.Attempts[0].Run.State = PhaseWorking
	workingHolder.Attempts[0].Run.Status = "working"
	workingHolderFacts := ciPendingFacts("c3")
	workingHolderFacts.Worker.State = RunWorking

	noReruns := DefaultSettings()
	noReruns.FlakeReruns = 0

	cases := []struct {
		name        string
		refused     int
		running     int
		granted     int
		maxRunning  int
		tasks       []Task
		facts       map[string]Facts
		workerState map[string]RunState
		asks        []Ask
		settings    Settings
		want        []string
	}{
		{
			name:    "no_stop_worker_working_at_N36",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks:       []Task{workingTask},
			facts:       map[string]Facts{"T1": workingFacts},
			workerState: map[string]RunState{"T1": RunWorking},
			settings:    DefaultSettings(),
			want:        nil,
		},
		{
			name:    "no_stop_N33_tell_due",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks:       []Task{ciPassedTask("T1", "c1", 0)},
			facts:       map[string]Facts{"T1": ciRedFacts("c1")},
			workerState: map[string]RunState{"T1": RunIdle},
			settings:    noReruns,
			want:        nil,
		},
		{
			name:    "no_stop_N33_tell_in_backoff",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks: []Task{ciPassedTask("T1", "c1", 0, Act{
				Key: AskTellCI + "@44714@c1", Result: "failed", Class: ClassTransient,
				Err: "hc send timed out", At: t0.Add(-10 * time.Second),
			})},
			facts:       map[string]Facts{"T1": ciRedFacts("c1")},
			workerState: map[string]RunState{"T1": RunIdle},
			settings:    noReruns,
			want:        nil,
		},
		{
			name:    "no_stop_N33_tell_done_at_worker_restarts_probed_as_EndStopped",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks: []Task{ciPassedTask("T1", "c1", DefaultSettings().WorkerRestarts,
				Act{Key: AskTellCI + "@44714@c1", Result: "done", At: t0.Add(-2 * time.Minute)},
				Act{Key: AskWake + "@worker_failed", Result: "done", At: t0.Add(-time.Minute)},
			)},
			facts:       map[string]Facts{"T1": ciRedFacts("c1")},
			workerState: map[string]RunState{"T1": RunIdle},
			settings:    noReruns,
			want:        nil,
		},
		{
			name:    "no_stop_N38_past_main_red_minutes",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks: []Task{ciPassedTask("T1", "c1", 0)},
			facts: map[string]Facts{"T1": {
				Tip: "c1", TipRel: RelSame, Worker: Run{State: RunIdle},
				PR: PR{
					Answered: true, Number: 1, State: PROpen, Head: "c1", HeadRel: RelSame,
					Mergeable: MergeableClean, Checks: ChecksGreen, Behind: true,
				},
				BaseRed: true, BaseRedSince: t0.Add(-2 * time.Hour), BaseRedSpell: "deadbeef@1",
			}},
			workerState: map[string]RunState{"T1": RunIdle},
			settings:    DefaultSettings(),
			want:        nil,
		},
		{
			name:    "no_stop_N29_review_stage",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks: []Task{n29Task},
			facts: map[string]Facts{"T1": {
				Tip: "c1", TipRel: RelSame, Worker: Run{State: RunIdle}, Reviewer: Run{State: RunWorking},
				PR: PR{
					Answered: true, Number: 1, State: PROpen, Head: "c1", HeadRel: RelSame,
					Mergeable: MergeableClean, Checks: ChecksPending,
				},
			}},
			workerState: map[string]RunState{"T1": RunIdle},
			settings:    DefaultSettings(),
			want:        nil,
		},
		{
			name:    "no_stop_N42_green",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks: []Task{ciPassedTask("T1", "c1", 0)},
			facts: map[string]Facts{"T1": {
				Tip: "c1", TipRel: RelSame, Worker: Run{State: RunIdle},
				PR: PR{
					Answered: true, Number: 1, State: PROpen, Head: "c1", HeadRel: RelSame,
					Mergeable: MergeableClean, Checks: ChecksGreen,
				},
			}},
			workerState: map[string]RunState{"T1": RunIdle},
			settings:    DefaultSettings(),
			want:        nil,
		},
		{
			name:    "no_stop_N36_open_worker_ask",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks:       []Task{ciPassedTask("T1", "c1", 0)},
			facts:       map[string]Facts{"T1": ciPendingFacts("c1")},
			workerState: map[string]RunState{"T1": RunIdle},
			asks: []Ask{{
				ID: "ask-w1", Task: "T1", Kind: KindDecision, AskedBy: RoleWorker,
				RunID: "w-T1", Status: AskStatusOpen, At: t0.Add(-time.Minute),
			}},
			settings: DefaultSettings(),
			want:     nil,
		},
		{
			name:    "no_stop_N5_blocked_stage",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks: []Task{n5Task},
			facts: map[string]Facts{"T1": {
				Tip: "c1", TipRel: RelSame, Worker: Run{State: RunIdle},
				PR: PR{Answered: true, State: PRNone},
			}},
			workerState: map[string]RunState{"T1": RunIdle},
			settings:    DefaultSettings(),
			want:        nil,
		},
		{
			name:    "no_stop_nothing_refused_max_running_2",
			refused: 0, running: 1, granted: 0, maxRunning: 2,
			tasks:       []Task{ciPassedTask("T1", "c1", 0)},
			facts:       map[string]Facts{"T1": ciPendingFacts("c1")},
			workerState: map[string]RunState{"T1": RunIdle},
			settings:    DefaultSettings(),
			want:        nil,
		},
		{
			name:    "no_stop_overage_running_max_plus_1_one_eligible",
			refused: 1, running: 3, granted: 0, maxRunning: 2,
			tasks:       []Task{ciPassedTask("T1", "c1", 0)},
			facts:       map[string]Facts{"T1": ciPendingFacts("c1")},
			workerState: map[string]RunState{"T1": RunIdle},
			settings:    DefaultSettings(),
			want:        nil,
		},
		{
			name:    "stop_three_eligible_one_refused_oldest_only",
			refused: 1, running: 3, granted: 0, maxRunning: 3,
			tasks: []Task{
				ciPassedTask("T1", "c1", 0),
				ciPassedTask("T2", "c2", 0),
				ciPassedTask("T3", "c3", 0),
			},
			facts: map[string]Facts{
				"T1": ciPendingFacts("c1"),
				"T2": ciPendingFacts("c2"),
				"T3": ciPendingFacts("c3"),
			},
			workerState: map[string]RunState{"T1": RunIdle, "T2": RunIdle, "T3": RunIdle},
			settings:    DefaultSettings(),
			want:        []string{"T1"},
		},
		{
			name:    "stop_overage_running_max_plus_1_two_eligible_both",
			refused: 1, running: 3, granted: 0, maxRunning: 2,
			tasks: []Task{
				ciPassedTask("T1", "c1", 0),
				ciPassedTask("T2", "c2", 0),
			},
			facts: map[string]Facts{
				"T1": ciPendingFacts("c1"),
				"T2": ciPendingFacts("c2"),
			},
			workerState: map[string]RunState{"T1": RunIdle, "T2": RunIdle},
			settings:    DefaultSettings(),
			want:        []string{"T1", "T2"},
		},
		{
			name:    "stop_N31_open_owner_gate_ask",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks: []Task{n31Task},
			facts: map[string]Facts{"T1": {
				Tip: "c1", TipRel: RelSame, Worker: Run{State: RunIdle},
				PR: PR{
					Answered: true, Number: 1, State: PROpen, Head: "c1", HeadRel: RelSame,
					Mergeable: MergeableClean, Checks: ChecksGreen,
				},
			}},
			workerState: map[string]RunState{"T1": RunIdle},
			asks: []Ask{{
				ID: "ask-g1", Task: "T1", Kind: KindGate, Gated: true, AskedBy: AskedByN31,
				Head: "c1", Status: AskStatusOpen, At: t0.Add(-time.Minute),
			}},
			settings: DefaultSettings(),
			want:     []string{"T1"},
		},
		{
			name:    "stop_at_worker_restarts_probed_as_EndStopped",
			refused: 1, running: 1, granted: 0, maxRunning: 1,
			tasks:       []Task{ciPassedTask("T1", "c1", DefaultSettings().WorkerRestarts)},
			facts:       map[string]Facts{"T1": ciPendingFacts("c1")},
			workerState: map[string]RunState{"T1": RunIdle},
			settings:    DefaultSettings(),
			want:        []string{"T1"},
		},
		{
			// Round 2 correction cell 1: max_running 3, two idle CI-waiters
			// (running 2), three new candidates -> pass 1 grants 1 and refuses 2.
			// used = 2 + 1 = 3, so both idle CI-waiters must be stopped.
			name:    "stop_used_count_pass1_stops_two",
			refused: 2, running: 2, granted: 1, maxRunning: 3,
			tasks: []Task{
				ciPassedTask("T1", "c1", 0),
				ciPassedTask("T2", "c2", 0),
				{ID: "T3", Title: "feat: T3"},
				{ID: "T4", Title: "feat: T4"},
				{ID: "T5", Title: "feat: T5"},
			},
			facts: map[string]Facts{
				"T1": ciPendingFacts("c1"),
				"T2": ciPendingFacts("c2"),
			},
			workerState: map[string]RunState{"T1": RunIdle, "T2": RunIdle},
			settings:    DefaultSettings(),
			want:        []string{"T1", "T2"},
		},
		{
			// Round 2 correction cell 2: max_running 3, one idle CI-waiter + one
			// working task (running 2), two candidates -> pass 2 grants 1 and
			// refuses 1. used = 2 + 1 = 3, so the remaining idle CI-waiter stops.
			name:    "stop_used_count_pass2_stops_one",
			refused: 1, running: 2, granted: 1, maxRunning: 3,
			tasks: []Task{
				ciPassedTask("T2", "c2", 0),
				workingHolder,
				{ID: "T4", Title: "feat: T4"},
				{ID: "T5", Title: "feat: T5"},
			},
			facts: map[string]Facts{
				"T2": ciPendingFacts("c2"),
				"T3": workingHolderFacts,
			},
			workerState: map[string]RunState{"T2": RunIdle, "T3": RunWorking},
			settings:    DefaultSettings(),
			want:        []string{"T2"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			type runSnap struct {
				runID string
				ptr   *RunRecord
				val   RunRecord
			}
			snaps := make([]runSnap, len(c.tasks))
			for i, tk := range c.tasks {
				if a := OpenAttempt(tk); a != nil && a.Run != nil {
					snaps[i] = runSnap{runID: a.RunID, ptr: a.Run, val: *a.Run}
				}
			}

			got := YieldSlots(c.refused, c.running, c.granted, c.maxRunning, c.tasks, c.facts, c.workerState, c.asks, c.settings, t0)
			if !slices.Equal(got, c.want) {
				t.Errorf("YieldSlots = %v, want %v", got, c.want)
			}
			for i, tk := range c.tasks {
				if snaps[i].ptr == nil {
					continue
				}
				a := OpenAttempt(tk)
				if a == nil || a.RunID != snaps[i].runID || a.Run != snaps[i].ptr ||
					a.Run.State != snaps[i].val.State || a.Run.Result != snaps[i].val.Result ||
					a.Run.Address != snaps[i].val.Address || a.Run.SessionID != snaps[i].val.SessionID ||
					a.Run.Resumable != snaps[i].val.Resumable {
					t.Errorf("task %s Run mutated by YieldSlots: got runID=%q run=%+v, want runID=%q run=%+v",
						tk.ID, a.RunID, a.Run, snaps[i].runID, snaps[i].val)
				}
			}
		})
	}
}
