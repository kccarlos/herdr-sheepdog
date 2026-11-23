package flow

import (
	"slices"
	"testing"
	"time"
)

// PR 6.2, TDD step 1: pure flow table tests for N24, N30, N31, N42
// (noOpenOwnerAsk), and GrantSlots owner-answered priority + extra slot
// (design.md 4.5, 4.7, 4.10, 7.1; KB-ASK-10, KB-ASK-13, KB-TSK-12).

var (
	gateOwnerTask = func(in *In) { in.Task.Gate = GateOwner }
	designTask    = func(in *In) { in.Task.Kind = TaskKindDesign }
	designGateOn  = func(in *In) { in.Settings.DesignGate = GateOwner }
	splitOfRetro  = func(in *In) {
		in.Task.SplitFrom = "P1"
		in.Facts.SplitFromHadRetro = true
	}
)

func openAsk(id, kind, task, head string) Ask {
	return Ask{
		ID:        id,
		Kind:      kind,
		Gated:     kind != KindDecision,
		Question:  "Proceed with " + task + "?",
		Options:   []string{"Yes", "No"},
		Recommend: "Yes",
		Task:      task,
		Head:      head,
		Status:    AskStatusOpen,
		At:        now.Add(-time.Minute),
	}
}

func answeredAsk(id, kind, task, head, answer, by string, at time.Time) Ask {
	a := openAsk(id, kind, task, head)
	a.Status = AskStatusAnswered
	a.Answer = answer
	a.AnsweredBy = by
	a.AnsweredAt = at
	return a
}

// TestN24SplitChildAfterRetro checks design.md 4.5 N24 and plan.md 6.2:
// when the ladder says recap for H on a split child whose parent already had a
// retro, N24 holds for an owner ask ("drop it, or tell me how to go on?")
// instead of opening a coordinator recap at N25.
func TestN24SplitChildAfterRetro(t *testing.T) {
	cases := []struct {
		name      string
		in        In
		wantRow   string
		wantAsked bool // true = must ask to open the N24 ask; false = waiting (no duplicate ask)
		checkAsk  bool
	}{
		{
			name:      "split_child_of_retro_parent_opens_owner_ask",
			in:        with(streakRecap, splitOfRetro),
			wantRow:   "N24",
			wantAsked: true,
			checkAsk:  true,
		},
		{
			name: "open_n24_ask_waits_without_opening_duplicate",
			in: with(streakRecap, splitOfRetro, func(in *In) {
				in.Asks = []Ask{openAsk("ask-n24-1", KindGate, "t", "c1")}
			}),
			wantRow:   "N24",
			wantAsked: false,
			checkAsk:  true,
		},
		{
			name:    "non_split_task_falls_through_to_N25",
			in:      with(streakRecap),
			wantRow: "N25",
		},
		{
			name: "split_child_without_parent_retro_falls_through_to_N25",
			in: with(streakRecap, func(in *In) {
				in.Task.SplitFrom = "P1"
				in.Facts.SplitFromHadRetro = false
			}),
			wantRow: "N25",
		},
		{
			name: "owner_already_answered_n24_ask_falls_through_to_N25",
			in: with(streakRecap, splitOfRetro, func(in *In) {
				in.Asks = []Ask{answeredAsk("ask-n24-1", KindGate, "t", "c1", "Continue with guidance", "owner", now)}
			}),
			wantRow: "N25",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, d := FirstMatch(TaskRows, tc.in)
			if r.ID != tc.wantRow {
				t.Fatalf("FirstMatch = %s (%s, asked=%v), want %s", r.ID, d.Why, d.Asked, tc.wantRow)
			}
			if tc.wantRow == "N24" {
				if r.WaitsOn != "the owner" {
					t.Errorf("N24 WaitsOn = %q, want %q", r.WaitsOn, "the owner")
				}
				if r.Stage != "owner_gate" && r.Stage != "owner_answer" {
					t.Errorf("N24 Stage = %q, want owner_gate or owner_answer", r.Stage)
				}
			}
			if tc.checkAsk {
				if tc.wantAsked && len(d.Asked) == 0 {
					t.Errorf("N24 Asked = nil, want non-empty ask to open owner ask")
				}
				if !tc.wantAsked && len(d.Asked) != 0 {
					t.Errorf("N24 Asked = %v, want nil when ask is already open", d.Asked)
				}
			}
		})
	}

	// Order: N23 (worker tip ahead) beats N24; N24 beats N25.
	if r, _ := FirstMatch(TaskRows, with(streakRecap, splitOfRetro, tipAheadF)); r.ID != "N23" {
		t.Errorf("N23 vs N24: got %s, want N23", r.ID)
	}
}

// TestN30OwnerGateNo checks design.md 4.7 N30: when an owner gate applies
// (Task.Gate == "owner", or Task.Kind == "design" with Settings.DesignGate ==
// "owner"), the owner answered no in this attempt, and no recap was decided
// since, N30 opens an owner_no recap for the coordinator.
func TestN30OwnerGateNo(t *testing.T) {
	noAt := now.Add(-2 * time.Minute)
	ownerNo := func(in *In) {
		in.Asks = []Ask{answeredAsk("ask-gate-no", KindGate, "t", "c1", "No, hold", "owner", noAt)}
	}
	cases := []struct {
		name    string
		in      In
		wantRow string
	}{
		{
			name:    "task_gate_owner_answered_no_matches_N30",
			in:      with(gateOwnerTask, ownerNo),
			wantRow: "N30",
		},
		{
			name:    "design_task_with_design_gate_owner_answered_no_matches_N30",
			in:      with(designTask, designGateOn, ownerNo),
			wantRow: "N30",
		},
		{
			name:    "design_task_without_design_gate_owner_ignores_N30_and_merges",
			in:      with(designTask, ownerNo),
			wantRow: "N42",
		},
		{
			name: "recap_decided_after_owner_no_clears_N30_to_N31",
			in: with(gateOwnerTask, ownerNo, func(in *In) {
				in.Task.Recaps = []Recap{{
					Trigger: TriggerOwnerNo,
					Allowed: []string{"split", "new", "drop"},
					Decided: &RecapDecision{Choice: "new", Why: "reworked", At: now.Add(-time.Minute)},
				}}
			}),
			wantRow: "N31",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, d := FirstMatch(TaskRows, tc.in)
			if r.ID != tc.wantRow {
				t.Fatalf("FirstMatch = %s (%s, asked=%v), want %s", r.ID, d.Why, d.Asked, tc.wantRow)
			}
			if tc.wantRow == "N30" {
				if r.WaitsOn != "the coordinator" || r.Stage != "blocked_or_failed" {
					t.Errorf("N30 WaitsOn=%q Stage=%q, want coordinator/blocked_or_failed", r.WaitsOn, r.Stage)
				}
				if len(d.Asked) == 0 {
					t.Errorf("N30 Asked = nil, want recap open ask")
				}
			}
		})
	}
}

// TestN31OwnerGateAndOpenOwnerAsk checks design.md 4.7 N31 and KB-ASK-13:
// N31 holds a passed task when an open owner ask names the task, or when an
// owner gate applies and no owner yes covers H (including merge_only coverage
// and invalidation on a new commit).
func TestN31OwnerGateAndOpenOwnerAsk(t *testing.T) {
	yesAt := now.Add(-time.Minute)
	cases := []struct {
		name      string
		in        In
		wantRow   string
		wantAsked bool
		checkAsk  bool
	}{
		{
			name:      "gate_owner_without_ask_opens_gate_ask",
			in:        with(gateOwnerTask),
			wantRow:   "N31",
			wantAsked: true,
			checkAsk:  true,
		},
		{
			name: "gate_owner_with_open_gate_ask_for_H_waits_without_duplicate",
			in: with(gateOwnerTask, func(in *In) {
				in.Asks = []Ask{openAsk("ask-gate-1", KindGate, "t", "c1")}
			}),
			wantRow:   "N31",
			wantAsked: false,
			checkAsk:  true,
		},
		{
			name:      "design_task_with_design_gate_owner_holds_at_N31",
			in:        with(designTask, designGateOn),
			wantRow:   "N31",
			wantAsked: true,
			checkAsk:  true,
		},
		{
			name:    "design_task_without_design_gate_owner_passes_to_N42",
			in:      with(designTask),
			wantRow: "N42",
		},
		{
			name: "gate_owner_with_yes_at_H_passes_to_N42",
			in: with(gateOwnerTask, func(in *In) {
				in.Asks = []Ask{answeredAsk("ask-gate-1", KindGate, "t", "c1", "Yes, merge", "owner", yesAt)}
			}),
			wantRow: "N42",
		},
		{
			name: "gate_owner_yes_at_c1_still_covers_merge_only_m2",
			in: with(gateOwnerTask, movedToM2, func(in *In) {
				in.Asks = []Ask{answeredAsk("ask-gate-1", KindGate, "t", "c1", "Yes, merge", "owner", yesAt)}
			}),
			wantRow: "N42",
		},
		{
			name: "gate_owner_yes_at_c1_does_not_cover_new_commit_c2",
			in: with(gateOwnerTask, func(in *In) {
				a0(in).Ready = append(a0(in).Ready, Ready{Head: "c2"})
				a0(in).Rounds = append(a0(in).Rounds,
					Round{N: 3, Kind: RoundDelta, Head: "c2", Verdict: VerdictPass},
					Round{N: 4, Kind: RoundFull, Head: "c2", Verdict: VerdictPass},
				)
				in.Facts.Tip, in.Facts.PR.Head = "c2", "c2"
				in.Asks = []Ask{answeredAsk("ask-gate-1", KindGate, "t", "c1", "Yes, merge", "owner", yesAt)}
			}),
			wantRow:   "N31",
			wantAsked: true,
			checkAsk:  true,
		},
		{
			name: "open_release_ask_holds_task_before_merge_without_opening_gate_ask_KB_ASK_13",
			in: with(func(in *In) {
				in.Asks = []Ask{openAsk("ask-rel-1", KindRelease, "t", "c1")}
			}),
			wantRow:   "N31",
			wantAsked: false,
			checkAsk:  true,
		},
		{
			name: "open_decision_ask_does_not_trigger_N31",
			in: with(func(in *In) {
				in.Asks = []Ask{openAsk("ask-dec-1", KindDecision, "t", "c1")}
			}),
			wantRow: "N42",
		},
		{
			name: "open_owner_ask_for_other_task_does_not_trigger_N31",
			in: with(func(in *In) {
				in.Asks = []Ask{openAsk("ask-rel-other", KindRelease, "other-task", "c1")}
			}),
			wantRow: "N42",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, d := FirstMatch(TaskRows, tc.in)
			if r.ID != tc.wantRow {
				t.Fatalf("FirstMatch = %s (%s, asked=%v), want %s", r.ID, d.Why, d.Asked, tc.wantRow)
			}
			if tc.wantRow == "N31" {
				if r.WaitsOn != "the owner" {
					t.Errorf("N31 WaitsOn = %q, want %q", r.WaitsOn, "the owner")
				}
				if r.Stage != "owner_gate" {
					t.Errorf("N31 Stage = %q, want owner_gate", r.Stage)
				}
			}
			if tc.checkAsk {
				if tc.wantAsked && len(d.Asked) == 0 {
					t.Errorf("N31 Asked = nil, want gate ask open")
				}
				if !tc.wantAsked && len(d.Asked) != 0 {
					t.Errorf("N31 Asked = %v, want nil", d.Asked)
				}
			}
		})
	}

	// Row order: N31 beats N32 (conflict) and N33 (red CI).
	if r, _ := FirstMatch(TaskRows, with(gateOwnerTask, conflict)); r.ID != "N31" {
		t.Errorf("N31 vs N32: got %s, want N31", r.ID)
	}
	if r, _ := FirstMatch(TaskRows, with(gateOwnerTask, red)); r.ID != "N31" {
		t.Errorf("N31 vs N33: got %s, want N31", r.ID)
	}
}

// TestN42NoOpenOwnerAsk tests N42's own When predicate directly (design.md 4.7,
// plan.md 6.2): N42 itself must require no open gated owner ask naming the task.
func TestN42NoOpenOwnerAsk(t *testing.T) {
	i := slices.IndexFunc(TaskRows, func(r Row[In]) bool { return r.ID == "N42" })
	if i < 0 {
		t.Fatal("N42 row not found")
	}
	n42 := TaskRows[i]

	openOwner := with(func(in *In) {
		in.Asks = []Ask{openAsk("ask-rel-1", KindRelease, "t", "c1")}
	})
	if n42.When(openOwner) {
		t.Fatalf("N42.When returned true with an open gated owner ask naming task t; want false (noOpenOwnerAsk)")
	}

	openGate := with(func(in *In) {
		in.Asks = []Ask{openAsk("ask-gate-1", KindGate, "t", "c1")}
	})
	if n42.When(openGate) {
		t.Fatalf("N42.When returned true with an open gate ask naming task t; want false (noOpenOwnerAsk)")
	}

	// Non-blocking asks: open decision ask, answered owner ask, other task's owner ask.
	for name, ask := range map[string]Ask{
		"open_decision":     openAsk("ask-dec-1", KindDecision, "t", "c1"),
		"answered_owner":    answeredAsk("ask-rel-2", KindRelease, "t", "c1", "Yes", "owner", now),
		"other_task_owner":  openAsk("ask-rel-3", KindRelease, "other", "c1"),
	} {
		in := with(func(in *In) { in.Asks = []Ask{ask} })
		if !n42.When(in) {
			t.Errorf("N42.When(%s) = false, want true", name)
		}
	}
}

// TestGrantSlotsOwnerAnsweredPriorityAndExtraSlot checks design.md 4.10 and
// KB-TSK-12: a task the owner just answered ranks ahead of urgent, critical
// path, and oldest, and may use the single extra slot beyond max_running.
func TestGrantSlotsOwnerAnsweredPriorityAndExtraSlot(t *testing.T) {
	cases := []struct {
		name       string
		tasks      []Task
		asks       []Ask
		candidates []string
		running    int
		maxRunning int
		want       []string
	}{
		{
			name: "owner_answered_beats_urgent_and_longer_critical_path",
			tasks: []Task{
				{ID: "U1", EstimateLines: 300, Priority: PriorityUrgent},
				{ID: "O1", EstimateLines: 10},
			},
			asks: []Ask{
				answeredAsk("ask-1", KindRelease, "O1", "", "Yes", "owner", now),
			},
			candidates: []string{"U1", "O1"},
			running:    1, maxRunning: 2,
			want: []string{"O1"},
		},
		{
			name: "owner_answered_takes_extra_slot_at_cap",
			tasks: []Task{
				{ID: "N1", EstimateLines: 200},
				{ID: "O1", EstimateLines: 10},
			},
			asks: []Ask{
				answeredAsk("ask-1", KindGate, "O1", "", "Yes", "owner", now),
			},
			candidates: []string{"N1", "O1"},
			running:    2, maxRunning: 2,
			want: []string{"O1"},
		},
		{
			name: "owner_answered_beats_urgent_for_single_shared_extra_slot_at_cap",
			tasks: []Task{
				{ID: "U1", EstimateLines: 200, Priority: PriorityUrgent},
				{ID: "O1", EstimateLines: 10},
			},
			asks: []Ask{
				answeredAsk("ask-1", KindRelease, "O1", "", "Yes", "owner", now),
			},
			candidates: []string{"U1", "O1"},
			running:    2, maxRunning: 2,
			want: []string{"O1"},
		},
		{
			name: "no_extra_slot_when_already_above_cap",
			tasks: []Task{
				{ID: "O1", EstimateLines: 10},
			},
			asks: []Ask{
				answeredAsk("ask-1", KindRelease, "O1", "", "Yes", "owner", now),
			},
			candidates: []string{"O1"},
			running:    3, maxRunning: 2,
			want: nil,
		},
		{
			name: "coordinator_answered_decision_ask_does_not_grant_owner_priority_or_extra_slot",
			tasks: []Task{
				{ID: "U1", EstimateLines: 10, Priority: PriorityUrgent},
				{ID: "D1", EstimateLines: 10},
			},
			asks: []Ask{
				answeredAsk("ask-dec", KindDecision, "D1", "", "SQLite", RoleCoordinator, now),
			},
			candidates: []string{"U1", "D1"},
			running:    1, maxRunning: 2,
			want: []string{"U1"},
		},
		{
			name: "open_or_cancelled_owner_ask_does_not_grant_extra_slot",
			tasks: []Task{
				{ID: "O1", EstimateLines: 10},
				{ID: "O2", EstimateLines: 10},
			},
			asks: []Ask{
				openAsk("ask-open", KindRelease, "O1", ""),
				{ID: "ask-cancel", Kind: KindRelease, Gated: true, Task: "O2", Status: AskStatusCancelled},
			},
			candidates: []string{"O1", "O2"},
			running:    2, maxRunning: 2,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GrantSlots(tc.candidates, tc.tasks, tc.running, tc.maxRunning, tc.asks...)
			var ids []string
			for id := range got {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(ids, want) {
				t.Errorf("GrantSlots = %v, want %v", ids, want)
			}
		})
	}
}
