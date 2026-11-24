package flow

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestJ16PlanReviewProjectRows covers plan.md 11.1's J16 cases, J4 finished-project
// precedence, and J16's placement directly below J13.
func TestJ16PlanReviewProjectRows(t *testing.T) {
	def := DefaultSettings()
	on := def
	on.PlanReviewRounds = 1
	idleCoord := &RunRecord{ID: "r1", State: PhaseIdle, StartedAt: now, LastTurnEnd: now, Turn: 1}
	workCoord := &RunRecord{ID: "r1", State: PhaseWorking, StartedAt: now, Turn: 1}
	unrev := Task{ID: "T1", Title: "feat: new", Unreviewed: true}
	openTask := Task{ID: "T0", Title: "feat: accepted"}
	droppedHeld := Task{ID: "T1", Title: "feat: optional", Unreviewed: true, Dropped: &Dropped{Why: "optional"}}

	mk := func(run *RunRecord, tasks []Task, s Settings, ready bool, prs ...PlanReview) ProjectIn {
		return ProjectIn{Project: Project{Started: true, Run: run, PlanReviews: prs}, Tasks: tasks, Settings: s, Now: now, PlanReviewerReady: ready}
	}
	tStart, tVerdict := now.Add(-20*time.Minute), now.Add(-10*time.Minute)
	cases := []struct {
		name    string
		in      ProjectIn
		wantRow string
		wantAsk []string
	}{
		{"start when candidate ready and coord idle", mk(idleCoord, []Task{unrev}, on, true), "J16", []string{AskStartPlanReviewer}},
		{"start after prior review accepted", mk(idleCoord, []Task{openTask, unrev}, on, true, PlanReview{N: 1, Tasks: []string{"T0"}, Verdict: VerdictPass, Started: tStart, At: tVerdict, Accepted: tVerdict}), "J16", []string{AskStartPlanReviewer}},
		{"accept when plan_review_rounds is 0 now", mk(idleCoord, []Task{unrev}, def, true), "J16", []string{AskAcceptPlan}},
		{"accept when no candidate is ready", mk(idleCoord, []Task{unrev}, on, false), "J16", []string{AskAcceptPlan}},
		{"accept when every finding is closed", mk(idleCoord, []Task{unrev}, on, true, PlanReview{N: 1, Tasks: []string{"T1"}, Verdict: VerdictChanges, Started: tStart, At: tVerdict, Findings: []PlanFinding{{Kind: "shrink", Task: "T1", Why: "broad", Closed: "amended"}}}), "J16", []string{AskAcceptPlan}},
		{"accept when every held task was dropped", mk(idleCoord, []Task{openTask, droppedHeld}, on, true, PlanReview{N: 1, Tasks: []string{"T1"}, Verdict: VerdictChanges, Started: tStart, At: tVerdict, Findings: []PlanFinding{{Kind: "delete", Task: "T1", Why: "opt", Closed: "dropped"}}}), "J16", []string{AskAcceptPlan}},
		{"no match while a finding is open", mk(idleCoord, []Task{unrev}, on, true, PlanReview{N: 1, Tasks: []string{"T1"}, Verdict: VerdictChanges, Started: tStart, At: tVerdict, Findings: []PlanFinding{{Kind: "delete", Task: "T1", Why: "opt"}}}), "J14", nil},
		{"no match while a plan reviewer run is in flight", mk(idleCoord, []Task{unrev}, on, true, PlanReview{N: 1, Tasks: []string{"T1"}, Started: tStart, Run: &ReviewRun{Kind: RoundPlan, Round: 1, RunID: "plan-1"}}), "J14", nil},
		{"J4 finished project beats J16 and starts nothing", mk(idleCoord, []Task{droppedHeld}, on, true), "J4", []string{AskFinalJournal}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if r, d := FirstMatch(ProjectRows, c.in); r.ID != c.wantRow || !slices.Equal(d.Asked, c.wantAsk) {
				t.Errorf("got (%s, asked=%v, why=%q), want (%s, asked=%v)", r.ID, d.Asked, d.Why, c.wantRow, c.wantAsk)
			}
		})
	}
	// While the coordinator is working, J16 neither starts a plan reviewer nor accepts.
	if r, d := FirstMatch(ProjectRows, mk(workCoord, []Task{unrev}, on, true)); len(d.Asked) != 0 {
		t.Errorf("coord working: got (%s, asked=%v), want no start/accept ask", r.ID, d.Asked)
	}
	// J16 sits directly below J13: J13 wins when both hold, and swapping makes J16 win.
	hbCoord := &RunRecord{ID: "r1", State: PhaseIdle, StartedAt: now.Add(-time.Duration(on.HeartbeatMinutes) * time.Minute), Turn: 1}
	both := mk(hbCoord, []Task{unrev}, on, true)
	if r, d := FirstMatch(ProjectRows, both); r.ID != "J13" || !slices.Equal(d.Asked, []string{AskHeartbeat}) {
		t.Fatalf("J13 over J16: got (%s, %v), want J13 [%s]", r.ID, d.Asked, AskHeartbeat)
	}
	i13 := slices.IndexFunc(ProjectRows, func(r Row[ProjectIn]) bool { return r.ID == "J13" })
	i16 := slices.IndexFunc(ProjectRows, func(r Row[ProjectIn]) bool { return r.ID == "J16" })
	if i13 < 0 || i16 != i13+1 {
		t.Fatalf("J16 must sit directly below J13 in ProjectRows (J13=%d, J16=%d)", i13, i16)
	}
	swapped := slices.Clone(ProjectRows)
	swapped[i13], swapped[i16] = swapped[i16], swapped[i13]
	if r, d := FirstMatch(swapped, both); r.ID != "J16" || !slices.Equal(d.Asked, []string{AskStartPlanReviewer}) {
		t.Errorf("swapped J13/J16: got (%s, %v), want J16 [%s]", r.ID, d.Asked, AskStartPlanReviewer)
	}
}

// TestN10UnreviewedReasonAndOrder checks design.md 4.2 N10's unreviewed hold,
// its precedence before noSlot, and row order N9 > N10 > N11.
func TestN10UnreviewedReasonAndOrder(t *testing.T) {
	unrev := func(in *In) { in.Task.Unreviewed = true }
	r, d := FirstMatch(TaskRows, with(noAttempts, unrev))
	if r.ID != "N10" || len(d.Asked) != 0 || r.Stage != "queued" || !strings.Contains(d.Why, "plan review") {
		t.Fatalf("unreviewed no-attempt = (%s stage=%q asked=%v why=%q), want N10 queued naming plan review", r.ID, r.Stage, d.Asked, d.Why)
	}
	if _, d := FirstMatch(TaskRows, with(noAttempts, unrev, noSlotF)); !strings.Contains(d.Why, "plan review") {
		t.Errorf("N10 with unreviewed+noSlot why = %q, want plan review first", d.Why)
	}
	for _, pair := range []struct {
		win, lose string
		in        In
	}{
		{"N9", "N10", with(noAttempts, waitDep, unrev)},
		{"N10", "N11", with(noAttempts, unrev)},
	} {
		if r, _ := FirstMatch(TaskRows, pair.in); r.ID != pair.win {
			t.Errorf("%s over %s: got %s", pair.win, pair.lose, r.ID)
		}
		rows := slices.Clone(TaskRows)
		i := slices.IndexFunc(rows, func(r Row[In]) bool { return r.ID == pair.win })
		if i < 0 || i+1 >= len(rows) || rows[i+1].ID != pair.lose {
			t.Fatalf("%s and %s are not neighbours", pair.win, pair.lose)
		}
		rows[i], rows[i+1] = rows[i+1], rows[i]
		if r, _ := FirstMatch(rows, pair.in); r.ID != pair.lose {
			t.Errorf("swapped %s/%s: got %s, want %s", pair.win, pair.lose, r.ID, pair.lose)
		}
	}
}

// TestPlanReviewSettingsRangeChangedAndRole checks plan.md 11.1 settings and RolePlanReviewer.
func TestPlanReviewSettingsRangeChangedAndRole(t *testing.T) {
	def := DefaultSettings()
	if def.PlanReviewRounds != 0 {
		t.Fatalf("DefaultSettings().PlanReviewRounds = %d, want 0 (until 11.3, P8)", def.PlanReviewRounds)
	}
	if slices.ContainsFunc(def.Changed(), func(s string) bool { return strings.HasPrefix(s, "plan_review_rounds") }) {
		t.Errorf("default Changed() unexpectedly lists plan_review_rounds: %v", def.Changed())
	}
	for _, tc := range []struct {
		n  int
		ok bool
	}{{0, true}, {1, true}, {-1, false}, {2, false}} {
		s := DefaultSettings()
		s.PlanReviewRounds = tc.n
		if err := s.Validate(); (err == nil) != tc.ok || (!tc.ok && !strings.Contains(err.Error(), "plan_review_rounds")) {
			t.Errorf("Validate(PlanReviewRounds=%d) = %v, want ok=%v", tc.n, err, tc.ok)
		}
	}
	s := DefaultSettings()
	s.PlanReviewRounds = 1
	if !slices.Contains(s.Changed(), "plan_review_rounds 1") {
		t.Errorf("Changed() with PlanReviewRounds=1 = %v, want plan_review_rounds 1", s.Changed())
	}
	if RolePlanReviewer != "plan_reviewer" {
		t.Fatalf("RolePlanReviewer = %q, want plan_reviewer", RolePlanReviewer)
	}
	s.Models = map[string][]Candidate{RolePlanReviewer: {{CLI: "claude", Model: "sonnet", Effort: "medium"}}}
	if err := s.Validate(); err != nil {
		t.Fatalf("Validate with models.plan_reviewer: %v", err)
	}
	in, why := Resolve(RolePlanReviewer, Task{}, s, ResolveFacts{CLIs: map[string]CLIFacts{"claude": {Installed: true, SignedIn: true}}})
	if why != "" || in.Perm != PermReadOnly || in.Continue != ContinueFresh || in.Role != RolePlanReviewer {
		t.Errorf("Resolve(RolePlanReviewer) = (%+v, %q), want read-only fresh intent", in, why)
	}
	if got := (&ReviewRun{Kind: RoundPlan}).Role(); got != RolePlanReviewer {
		t.Errorf("ReviewRun{Kind:RoundPlan}.Role() = %q, want %q", got, RolePlanReviewer)
	}
}

// TestJ15PlanReviewProjectRows covers plan.md 11.2 and design.md 6: J15 sits
// directly above J3 and stops gone, idle, overdue, or all-dropped plan reviews.
func TestJ15PlanReviewProjectRows(t *testing.T) {
	on := DefaultSettings()
	on.PlanReviewRounds = 1
	idle := &RunRecord{ID: "r1", State: PhaseIdle, StartedAt: now, LastTurnEnd: now, Turn: 1}
	unrev := []Task{{ID: "T1", Title: "feat: new", Unreviewed: true}}
	dropped := []Task{{ID: "T1", Title: "feat: opt", Unreviewed: true, Dropped: &Dropped{Why: "opt"}}}
	disk := map[string]Alert{AlertDisk: {Kind: AlertDisk, Cause: "full"}}
	mk := func(coord *RunRecord, tasks []Task, alerts map[string]Alert, age time.Duration, state RunState) ProjectIn {
		pr := PlanReview{N: 1, Tasks: []string{"T1"}, Started: now.Add(-5 * time.Minute), Run: &ReviewRun{Kind: RoundPlan, Round: 1, RunID: "plan-1", Age: age}}
		return ProjectIn{Project: Project{Started: true, Run: coord, Alerts: alerts, PlanReviews: []PlanReview{pr}}, Tasks: tasks, Settings: on, Now: now, PlanRun: Run{State: state}}
	}
	for _, c := range []struct {
		name, wantRow string
		wantAsk       []string
		in            ProjectIn
	}{
		{"gone", "J15", []string{AskStopPlanReviewer}, mk(idle, unrev, nil, time.Minute, RunGone)},
		{"idle", "J15", []string{AskStopPlanReviewer}, mk(idle, unrev, nil, time.Minute, RunIdle)},
		{"overdue", "J15", []string{AskStopPlanReviewer}, mk(idle, unrev, nil, time.Duration(on.ReviewMinutes)*time.Minute, RunWorking)},
		{"all tasks dropped (beats J4)", "J15", []string{AskStopPlanReviewer}, mk(idle, dropped, nil, time.Minute, RunWorking)},
		{"beats J3 stopping alert", "J15", []string{AskStopPlanReviewer}, mk(nil, unrev, disk, time.Minute, RunIdle)},
		{"working under limit stays J14", "J14", nil, mk(idle, unrev, nil, time.Minute, RunWorking)},
	} {
		if r, d := FirstMatch(ProjectRows, c.in); r.ID != c.wantRow || !slices.Equal(d.Asked, c.wantAsk) {
			t.Errorf("%s: got (%s, asked=%v, why=%q), want (%s, asked=%v)", c.name, r.ID, d.Asked, d.Why, c.wantRow, c.wantAsk)
		}
	}
	i15 := slices.IndexFunc(ProjectRows, func(r Row[ProjectIn]) bool { return r.ID == "J15" })
	i3 := slices.IndexFunc(ProjectRows, func(r Row[ProjectIn]) bool { return r.ID == "J3" })
	if i15 != 0 || i3 != 1 {
		t.Fatalf("J15 must sit directly above J3 at top of ProjectRows (J15=%d, J3=%d)", i15, i3)
	}
	swapped := slices.Clone(ProjectRows)
	swapped[i15], swapped[i3] = swapped[i3], swapped[i15]
	if r, _ := FirstMatch(swapped, mk(nil, unrev, disk, time.Minute, RunIdle)); r.ID != "J3" {
		t.Errorf("swapped J15/J3: got %s, want J3", r.ID)
	}
}
