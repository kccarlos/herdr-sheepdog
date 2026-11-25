package flow

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// Project-level asks and bounds for ProjectRows (design.md section 6).
const (
	AskStartCoordinator, AskStopCoordinator, AskFinalJournal         = "coordinator", "stop_coordinator", "final_journal"
	AskCoordGoOn, AskCoordDialogStuck, AskDeliverWakes, AskHeartbeat = "coord_go_on", "coord_dialog_stuck", "deliver_wakes", "heartbeat"
	AskStartPlanReviewer, AskStopPlanReviewer, AskAcceptPlan         = "start_plan_reviewer", "stop_plan_reviewer", "accept_plan"
	coordStartWindow, firstDialogStuck, planFindingsHoldBeats        = time.Hour, 1, 2
)

// ProjectIn is what a project rule sees (design.md section 6).
type ProjectIn struct {
	Project
	Tasks []Task
	Settings
	Now                time.Time
	CoordFallbackReady bool // J5: another configured coordinator candidate has a positive sign-in check
	PlanReviewerReady  bool // J16: a plan_reviewer candidate currently resolves
	PlanRun            Run  // J15: observed state of the active plan_reviewer run
}

// AllTasksEnded reports whether a plan exists and every task carries a terminal mark (J4).
func AllTasksEnded(tasks []Task) bool {
	return len(tasks) > 0 && !slices.ContainsFunc(tasks, func(t Task) bool { return !Ended(t) })
}

// ProjectFinished reports whether J4's finished condition holds (design.md section 6).
func ProjectFinished(p Project, tasks []Task) bool {
	return AllTasksEnded(tasks) && !p.NewGoal && (len(p.Wakes) == 0 || p.Finished)
}

// IsCoordR29 reports whether r ended as stuck at a dialog via R29 (J10).
func IsCoordR29(r *RunRecord) bool {
	return r != nil && r.State == PhaseEnded && r.Result == EndStuck && !r.BlockedSince.IsZero()
}

// CoordStuckKey uniquely identifies one ended coordinator run for J10.
func CoordStuckKey(r *RunRecord) string {
	return AskCoordDialogStuck + "@" + cmp.Or(r.ID, r.Address, r.EndedAt.UTC().Format(time.RFC3339Nano))
}

// FinalJournalKey scopes J4's final journal ask to the current plan's task count.
func FinalJournalKey(tasks []Task) string { return fmt.Sprintf("%s@%d", AskFinalJournal, len(tasks)) }

func (in ProjectIn) alive() bool { return RunLive(in.RunID, in.Run) }
func (in ProjectIn) idle() bool  { return in.Run != nil && in.Run.State == PhaseIdle }
func (in ProjectIn) past(t time.Time, m int) bool {
	return !t.IsZero() && in.Now.Sub(t) >= time.Duration(m)*time.Minute
}
func (in ProjectIn) coordError() bool {
	return in.idle() && in.Run.Status == "error" && (in.CoordErrorAt.IsZero() || in.Run.Turn > in.CoordErrorTurn)
}
func (in ProjectIn) heartbeatDue() bool {
	return in.past(in.CoordLastWakeAt, in.HeartbeatMinutes) || in.CoordLastWakeAt.IsZero() && in.Run != nil && in.past(in.Run.StartedAt, in.HeartbeatMinutes)
}

func (in ProjectIn) planReviewStep() (start, accept bool) {
	if pr := in.lastPlanReview(); pr != nil && pr.Accepted.IsZero() {
		return false, pr.Verdict != "" && (len(OpenPlanFindings(in.Project)) == 0 || in.past(pr.At, planFindingsHoldBeats*in.HeartbeatMinutes))
	}
	if !slices.ContainsFunc(in.Tasks, func(t Task) bool { return !Ended(t) && t.Unreviewed }) {
		return false, false
	}
	if in.PlanReviewRounds > 0 && in.PlanReviewerReady {
		return in.idle(), false
	}
	return false, true
}

func (in ProjectIn) planReviewStopWhy() string {
	pr := in.lastPlanReview()
	if pr != nil && pr.Verdict != "" && pr.Run != nil && pr.Run.RunID != "" {
		return "plan reviewer report was accepted"
	}
	if pr != nil && pr.Accepted.IsZero() && pr.Verdict == "" && pr.Run != nil && in.PlanRun.State != RunNone {
		switch {
		case in.PlanRun.State == RunGone, in.PlanRun.State == RunIdle:
			return "plan reviewer " + map[RunState]string{RunGone: "terminal is gone", RunIdle: "went idle without a report"}[in.PlanRun.State]
		case pr.Run.Age >= time.Duration(in.ReviewMinutes)*time.Minute:
			return "plan reviewer exceeded review_minutes"
		case !slices.ContainsFunc(in.Tasks, func(t Task) bool { return !Ended(t) && slices.Contains(pr.Tasks, t.ID) }):
			return "every task in the plan review has ended"
		}
	}
	return ""
}

func (in ProjectIn) coordSilent() bool {
	return in.idle() && StoppingAlert(in.Alerts) == nil && !in.CoordWakeAt.IsZero() && in.Run.Turn <= in.CoordWakeTurn && in.past(in.CoordWakeAt, in.CoordinatorSilentMinutes)
}

func (in ProjectIn) coordStopWhy() string {
	if last, tries := ActRetry(in.Acts, AskStopCoordinator); tries > 0 {
		return fmt.Sprintf("could not stop the old coordinator (%s)", last.Err)
	}
	return ""
}

func (in ProjectIn) startBlocked() (string, bool) {
	var gapUntil, capUntil time.Time
	if last := LastAct(in.Acts, AskStartCoordinator); last != nil && !in.past(last.At, in.CoordinatorGapMinutes) {
		gapUntil = last.At.Add(time.Duration(in.CoordinatorGapMinutes) * time.Minute)
	}
	var recent []time.Time
	for _, a := range in.Acts {
		if a.Key == AskStartCoordinator && in.Now.Sub(a.At) < coordStartWindow {
			recent = append(recent, a.At)
		}
	}
	if in.CoordinatorStartsPerHour > 0 && len(recent) >= in.CoordinatorStartsPerHour {
		slices.SortFunc(recent, time.Time.Compare)
		capUntil = recent[len(recent)-in.CoordinatorStartsPerHour].Add(coordStartWindow)
	}
	if !capUntil.IsZero() && !gapUntil.After(capUntil) {
		return "coordinator_starts_per_hour reached; waiting (allowed at " + capUntil.Local().Format(time.RFC3339) + ")", gapUntil.IsZero()
	} else if !gapUntil.IsZero() {
		return "waiting for coordinator_gap_minutes before restarting coordinator (allowed at " + gapUntil.Local().Format(time.RFC3339) + ")", false
	}
	return "", false
}

// CoordHourlyCap reports whether J6's coordinator_starts_per_hour cap currently holds.
func CoordHourlyCap(p Project, s Settings, now time.Time) (string, bool) {
	return ProjectIn{Project: p, Settings: s, Now: now}.startBlocked()
}

func pdec(why string, ask ...string) Decision { return Decision{Asked: ask, Why: why} }
func prow(id, waits, stage string, when func(ProjectIn) bool, then func(ProjectIn) Decision) Row[ProjectIn] {
	return Row[ProjectIn]{ID: id, WaitsOn: waits, Stage: stage, When: when, Then: then}
}
func pfix(id, waits string, when func(ProjectIn) bool, why string, ask ...string) Row[ProjectIn] {
	return prow(id, waits, "running", when, func(ProjectIn) Decision { return pdec(why, ask...) })
}
func pcond(id, waits, stage string, when, sel func(ProjectIn) bool, yes, no Decision) Row[ProjectIn] {
	return prow(id, waits, stage, when, func(in ProjectIn) Decision { return map[bool]Decision{true: yes, false: no}[sel(in)] })
}

// CoordRows is the ordered rule table for the coordinator (design.md section 6).
// J1 (doctor) is slice 8; J3/J5 (alerts/sign-in) are slice 5b; J11 (rotation) is slice 13.
// J2 lives in passProject's early return and Engine.Stop.
// J10 sits before J6/J7 so an R29 dialog-stuck end is noted before J6 waits or J7 restarts it.
var CoordRows = []Row[ProjectIn]{
	prow("J3", "owner", "stopped", func(in ProjectIn) bool { return StoppingAlert(in.Alerts) != nil && !in.alive() }, func(in ProjectIn) Decision {
		a := StoppingAlert(in.Alerts)
		return pdec("Stopped: " + cmp.Or(a.Fix, a.Cause))
	}),
	prow("J4", "none", "finished", func(in ProjectIn) bool { return ProjectFinished(in.Project, in.Tasks) }, func(in ProjectIn) Decision {
		if !in.alive() || !in.idle() {
			return pdec(map[bool]string{true: "Finished: all tasks ended", false: "Finished: waiting for coordinator to finish final journal line"}[!in.alive()])
		}
		return map[bool]Decision{true: pdec("Finished: stop coordinator after final journal line", AskStopCoordinator), false: pdec("Finished: ask coordinator for final journal line", AskFinalJournal)}[Done(in.Acts, FinalJournalKey(in.Tasks))]
	}),
	prow("J5", "owner", "running", func(in ProjectIn) bool { return !in.alive() && in.CoordSignOutHold() && !in.CoordFallbackReady }, func(in ProjectIn) Decision {
		a := in.Alerts[AlertAgentsDown]
		return pdec(cmp.Or(a.Fix, a.Cause))
	}),
	pcond("J10", "engine", "running", func(in ProjectIn) bool { return IsCoordR29(in.Run) && !Done(in.Acts, CoordStuckKey(in.Run)) }, func(in ProjectIn) bool { return in.CoordDialogStucks >= firstDialogStuck }, pdec("coordinator stuck at a dialog twice in a row (R29); needs owner attention", AskCoordDialogStuck), pdec("coordinator ended stuck at a dialog (R29); J7 will restart it", AskCoordDialogStuck)),
	prow("J6", "engine", "running", func(in ProjectIn) bool { why, _ := in.startBlocked(); return !in.alive() && why != "" }, func(in ProjectIn) Decision {
		why, _ := in.startBlocked()
		if stopWhy := in.coordStopWhy(); stopWhy != "" {
			why += ". " + stopWhy
		}
		return pdec(why)
	}),
	prow("J7", "engine", "running", func(in ProjectIn) bool { return !in.alive() }, func(in ProjectIn) Decision {
		return pdec(cmp.Or(in.coordStopWhy(), map[bool]string{true: "project started with no coordinator run", false: "no coordinator run is alive; start with handoff"}[len(in.Tasks) == 0]), AskStartCoordinator)
	}),
	pcond("J8", "coordinator", "running", ProjectIn.coordError, func(in ProjectIn) bool {
		return !in.CoordErrorAt.IsZero() && !in.past(in.CoordErrorAt, in.QuietMinutes)
	}, pdec("coordinator reported another error within quiet_minutes: stop it", AskStopCoordinator), pdec("coordinator reported an error at turn end: tell it to go on", AskCoordGoOn)),
	pfix("J9", "coordinator", ProjectIn.coordSilent, "coordinator stayed silent for coordinator_silent_minutes: stop it", AskStopCoordinator),
	pfix("J12", "coordinator", func(in ProjectIn) bool { return in.idle() && len(in.Wakes) > 0 }, "wakes held and the coordinator is idle", AskDeliverWakes),
	pfix("J13", "coordinator", func(in ProjectIn) bool { return in.idle() && in.heartbeatDue() }, "no wake for heartbeat_minutes and coordinator is idle", AskHeartbeat),
	pcond("J16", "engine", "running", func(in ProjectIn) bool { s, a := in.planReviewStep(); return s || a }, func(in ProjectIn) bool { s, _ := in.planReviewStep(); return s }, pdec("unreviewed tasks waiting and coordinator is idle: start plan reviewer", AskStartPlanReviewer), pdec("accept held plan", AskAcceptPlan)),
	pfix("J14", "none", nil, "running"),
}

// ProjectRows prepends J15 (plan-reviewer terminal cleanup) ahead of CoordRows
// so a plan reviewer never outlives a stopped, alerted, or finished project.
var ProjectRows = append([]Row[ProjectIn]{
	prow("J15", "engine", "running", func(in ProjectIn) bool { return in.planReviewStopWhy() != "" }, func(in ProjectIn) Decision {
		return pdec(in.planReviewStopWhy(), AskStopPlanReviewer)
	}),
}, CoordRows...)
