package flow

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProjectRowsTableAndEachRow(t *testing.T) {
	if err := CheckTable(ProjectRows); err != nil {
		t.Fatalf("CheckTable(ProjectRows): %v", err)
	}
	def := DefaultSettings()
	openTask := Task{ID: "T1", Title: "feat: open"}
	doneTask := Task{ID: "T1", Title: "feat: done", Done: &Merged{MergedCommit: "m1"}}
	idleRun := &RunRecord{ID: "r1", State: PhaseIdle, StartedAt: now, LastTurnEnd: now, Turn: 1}

	cases := []struct {
		name    string
		in      ProjectIn
		wantRow string
		wantAsk []string
	}{
		{
			name:    "J4 all tasks ended, no coordinator -> finished, no restart",
			in:      ProjectIn{Project: Project{Started: true}, Tasks: []Task{doneTask}, Settings: def, Now: now},
			wantRow: "J4",
		},
		{
			name:    "J4 all tasks ended, idle coordinator -> ask final journal",
			in:      ProjectIn{Project: Project{Started: true, Run: idleRun}, Tasks: []Task{doneTask}, Settings: def, Now: now},
			wantRow: "J4", wantAsk: []string{AskFinalJournal},
		},
		{
			name: "J4 all tasks ended, final journal asked, idle -> stop coordinator",
			in: ProjectIn{
				Project:  Project{Started: true, Run: idleRun, Acts: []Act{{Key: FinalJournalKey([]Task{doneTask}), Result: "done", At: now}}},
				Tasks:    []Task{doneTask},
				Settings: def, Now: now,
			},
			wantRow: "J4", wantAsk: []string{AskStopCoordinator},
		},
		{
			name:    "J4 yields to J7 when owner gave a new goal",
			in:      ProjectIn{Project: Project{Started: true, NewGoal: true}, Tasks: []Task{doneTask}, Settings: def, Now: now},
			wantRow: "J7", wantAsk: []string{AskStartCoordinator},
		},
		{
			name: "J10 coordinator ended stuck at dialog (R29)",
			in: ProjectIn{
				Project:  Project{Started: true, Run: &RunRecord{ID: "r1", State: PhaseEnded, Result: EndStuck, BlockedSince: now.Add(-10 * time.Minute), EndedAt: now}},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J10", wantAsk: []string{AskCoordDialogStuck},
		},
		{
			name: "R11/R22 stuck without BlockedSince is not J10 -> goes to J7",
			in: ProjectIn{
				Project:  Project{Started: true, Run: &RunRecord{ID: "r1", State: PhaseEnded, Result: EndStuck, EndedAt: now}},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J7", wantAsk: []string{AskStartCoordinator},
		},
		{
			name: "J6 gap not elapsed holds restart",
			in: ProjectIn{
				Project:  Project{Started: true, Acts: []Act{{Key: AskStartCoordinator, Result: "done", At: now.Add(-5 * time.Minute)}}},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J6",
		},
		{
			name: "J6 hourly cap reached holds restart even past gap",
			in: ProjectIn{
				Project: Project{Started: true, Acts: []Act{
					{Key: AskStartCoordinator, Result: "done", At: now.Add(-50 * time.Minute)},
					{Key: AskStartCoordinator, Result: "done", At: now.Add(-35 * time.Minute)},
					{Key: AskStartCoordinator, Result: "done", At: now.Add(-15 * time.Minute)},
				}},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J6",
		},
		{
			name:    "J7 starts coordinator when none alive and J6 clear",
			in:      ProjectIn{Project: Project{Started: true}, Settings: def, Now: now},
			wantRow: "J7", wantAsk: []string{AskStartCoordinator},
		},
		{
			name: "J8 first error tells coordinator to go on",
			in: ProjectIn{
				Project:  Project{Started: true, Run: &RunRecord{ID: "r1", State: PhaseIdle, Status: "error", Turn: 1, StartedAt: now}},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J8", wantAsk: []string{AskCoordGoOn},
		},
		{
			name: "J8 second error within quiet_minutes stops coordinator",
			in: ProjectIn{
				Project: Project{
					Started: true, CoordErrorAt: now.Add(-5 * time.Minute), CoordErrorTurn: 1,
					Run: &RunRecord{ID: "r1", State: PhaseIdle, Status: "error", Turn: 2, StartedAt: now.Add(-10 * time.Minute)},
				},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J8", wantAsk: []string{AskStopCoordinator},
		},
		{
			name: "J8 error after quiet_minutes tells go on again",
			in: ProjectIn{
				Project: Project{
					Started: true, CoordErrorAt: now.Add(-21 * time.Minute), CoordErrorTurn: 1,
					Run: &RunRecord{ID: "r1", State: PhaseIdle, Status: "error", Turn: 2, StartedAt: now.Add(-30 * time.Minute)},
				},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J8", wantAsk: []string{AskCoordGoOn},
		},
		{
			name: "J9 silence after delivered wake stops coordinator",
			in: ProjectIn{
				Project: Project{
					Started: true, CoordWakeAt: now.Add(-30 * time.Minute), CoordWakeTurn: 1,
					Run: &RunRecord{ID: "r1", State: PhaseIdle, Turn: 1, StartedAt: now.Add(-40 * time.Minute)},
				},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J9", wantAsk: []string{AskStopCoordinator},
		},
		{
			name: "J12 held wakes when idle",
			in: ProjectIn{
				Project:  Project{Started: true, Run: idleRun, Wakes: []Wake{{Key: "k1", Task: "T1"}}},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J12", wantAsk: []string{AskDeliverWakes},
		},
		{
			name: "J13 heartbeat after interval when idle",
			in: ProjectIn{
				Project:  Project{Started: true, Run: &RunRecord{ID: "r1", State: PhaseIdle, StartedAt: now.Add(-60 * time.Minute), Turn: 1}},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J13", wantAsk: []string{AskHeartbeat},
		},
		{
			name: "J13 does not fire while working -> J14",
			in: ProjectIn{
				Project:  Project{Started: true, Run: &RunRecord{ID: "r1", State: PhaseWorking, StartedAt: now.Add(-90 * time.Minute)}},
				Tasks:    []Task{openTask},
				Settings: def, Now: now,
			},
			wantRow: "J14",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, d := FirstMatch(ProjectRows, c.in)
			if r.ID != c.wantRow || !slices.Equal(d.Asked, c.wantAsk) {
				t.Errorf("got (%s, asked=%v, why=%q), want (%s, asked=%v)", r.ID, d.Asked, d.Why, c.wantRow, c.wantAsk)
			}
		})
	}

	// J10 second time in a row names owner attention in Why.
	in2 := ProjectIn{
		Project: Project{
			Started: true, CoordDialogStucks: 1,
			Run: &RunRecord{ID: "r2", State: PhaseEnded, Result: EndStuck, BlockedSince: now.Add(-10 * time.Minute), EndedAt: now},
		},
		Tasks: []Task{openTask}, Settings: def, Now: now,
	}
	if r, d := FirstMatch(ProjectRows, in2); r.ID != "J10" || !strings.Contains(d.Why, "twice in a row") {
		t.Errorf("J10 2nd time = (%s, %q), want J10 mentioning twice in a row", r.ID, d.Why)
	}
}

func TestProjectRowsReviewFixes(t *testing.T) {
	def := DefaultSettings()
	openTask := Task{ID: "T1", Title: "feat: open"}

	t.Run("J9 does not stop working coordinator", func(t *testing.T) {
		in := ProjectIn{
			Project: Project{
				Started: true, CoordWakeAt: now.Add(-45 * time.Minute), CoordWakeTurn: 1,
				Run: &RunRecord{ID: "r1", State: PhaseWorking, Status: "working", Turn: 1, StartedAt: now.Add(-50 * time.Minute)},
			},
			Tasks: []Task{openTask}, Settings: def, Now: now,
		}
		if r, d := FirstMatch(ProjectRows, in); r.ID != "J14" {
			t.Fatalf("got (%s, why=%q), want J14", r.ID, d.Why)
		}
	})

	t.Run("J9 does not stop idle plan-less coordinator without delivered wake", func(t *testing.T) {
		in := ProjectIn{
			Project: Project{
				Started: true,
				Run:     &RunRecord{ID: "r1", State: PhaseIdle, Turn: 1, StartedAt: now.Add(-35 * time.Minute), LastTurnEnd: now.Add(-30 * time.Minute)},
			},
			Settings: def, Now: now,
		}
		if r, d := FirstMatch(ProjectRows, in); r.ID != "J14" {
			t.Fatalf("got (%s, why=%q), want J14", r.ID, d.Why)
		}
	})

	t.Run("J6 gap and hourly cap report allow time", func(t *testing.T) {
		gapIn := ProjectIn{
			Project:  Project{Started: true, Acts: []Act{{Key: AskStartCoordinator, Result: "done", At: now.Add(-5 * time.Minute)}}},
			Tasks:    []Task{openTask},
			Settings: def, Now: now,
		}
		wantGap := now.Add(5 * time.Minute).Local().Format(time.RFC3339)
		if r, d := FirstMatch(ProjectRows, gapIn); r.ID != "J6" || !strings.Contains(d.Why, wantGap) {
			t.Fatalf("J6 gap = (%s, %q), want J6 naming %s", r.ID, d.Why, wantGap)
		}
		capIn := ProjectIn{
			Project: Project{Started: true, Acts: []Act{
				{Key: AskStartCoordinator, Result: "done", At: now.Add(-50 * time.Minute)},
				{Key: AskStartCoordinator, Result: "done", At: now.Add(-35 * time.Minute)},
				{Key: AskStartCoordinator, Result: "done", At: now.Add(-15 * time.Minute)},
			}},
			Tasks:    []Task{openTask},
			Settings: def, Now: now,
		}
		wantCap := now.Add(10 * time.Minute).Local().Format(time.RFC3339)
		if r, d := FirstMatch(ProjectRows, capIn); r.ID != "J6" || !strings.Contains(d.Why, wantCap) {
			t.Fatalf("J6 hourly cap = (%s, %q), want J6 naming %s", r.ID, d.Why, wantCap)
		}
	})
}

func TestProjectRowsPairwisePriority(t *testing.T) {
	def := DefaultSettings()
	openTask := Task{ID: "T1", Title: "feat: open"}
	doneTask := Task{ID: "T1", Title: "feat: done", Done: &Merged{MergedCommit: "m1"}}
	pairs := []struct {
		winner, loser string
		in            ProjectIn
	}{
		{"J4", "J10", ProjectIn{Project: Project{Started: true, Run: &RunRecord{ID: "r1", State: PhaseEnded, Result: EndStuck, BlockedSince: now}}, Tasks: []Task{doneTask}, Settings: def, Now: now}},
		{"J10", "J6", ProjectIn{Project: Project{Started: true, Acts: []Act{{Key: AskStartCoordinator, Result: "done", At: now}}, Run: &RunRecord{ID: "r1", State: PhaseEnded, Result: EndStuck, BlockedSince: now}}, Tasks: []Task{openTask}, Settings: def, Now: now}},
		{"J6", "J7", ProjectIn{Project: Project{Started: true, Acts: []Act{{Key: AskStartCoordinator, Result: "done", At: now}}}, Tasks: []Task{openTask}, Settings: def, Now: now}},
		{"J8", "J9", ProjectIn{Project: Project{Started: true, CoordWakeAt: now.Add(-30 * time.Minute), CoordWakeTurn: 1, Run: &RunRecord{ID: "r1", State: PhaseIdle, Status: "error", Turn: 1, StartedAt: now.Add(-40 * time.Minute)}}, Tasks: []Task{openTask}, Settings: def, Now: now}},
		{"J9", "J12", ProjectIn{Project: Project{Started: true, CoordWakeAt: now.Add(-30 * time.Minute), CoordWakeTurn: 1, Wakes: []Wake{{Key: "k", Task: "T1"}}, Run: &RunRecord{ID: "r1", State: PhaseIdle, Turn: 1, StartedAt: now.Add(-40 * time.Minute)}}, Tasks: []Task{openTask}, Settings: def, Now: now}},
		{"J12", "J13", ProjectIn{Project: Project{Started: true, Wakes: []Wake{{Key: "k", Task: "T1"}}, Run: &RunRecord{ID: "r1", State: PhaseIdle, StartedAt: now.Add(-60 * time.Minute), Turn: 1}}, Tasks: []Task{openTask}, Settings: def, Now: now}},
	}
	for _, p := range pairs {
		r, _ := FirstMatch(ProjectRows, p.in)
		if r.ID != p.winner {
			t.Errorf("%s over %s: got %s", p.winner, p.loser, r.ID)
		}
		// Swapping adjacent rows must change the winner (design.md 2.3).
		swapped := slices.Clone(ProjectRows)
		i := slices.IndexFunc(swapped, func(row Row[ProjectIn]) bool { return row.ID == p.winner })
		j := slices.IndexFunc(swapped, func(row Row[ProjectIn]) bool { return row.ID == p.loser })
		swapped[i], swapped[j] = swapped[j], swapped[i]
		if sr, _ := FirstMatch(swapped, p.in); sr.ID != p.loser {
			t.Errorf("swapped %s/%s: got %s, want %s", p.winner, p.loser, sr.ID, p.loser)
		}
	}
}
