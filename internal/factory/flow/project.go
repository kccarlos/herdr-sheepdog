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
	coordStartWindow, firstDialogStuck                               = time.Hour, 1
)

// ProjectIn is what a project rule sees (design.md section 6).
type ProjectIn struct {
	Project
	Tasks []Task
	Settings
	Now time.Time
}

// AllTasksEnded reports whether a plan exists and every task carries a terminal mark (J4).
func AllTasksEnded(tasks []Task) bool {
	return len(tasks) > 0 && !slices.ContainsFunc(tasks, func(t Task) bool { return !Ended(t) })
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

func (in ProjectIn) alive() bool {
	return in.Run != nil && in.Run.State != PhaseEnded || in.Run == nil && in.RunID != ""
}
func (in ProjectIn) idle() bool { return in.Run != nil && in.Run.State == PhaseIdle }
func (in ProjectIn) past(t time.Time, m int) bool {
	return !t.IsZero() && in.Now.Sub(t) >= time.Duration(m)*time.Minute
}
func (in ProjectIn) coordError() bool {
	return in.idle() && in.Run.Status == "error" && (in.CoordErrorAt.IsZero() || in.Run.Turn > in.CoordErrorTurn)
}
func (in ProjectIn) heartbeatDue() bool {
	return in.past(in.CoordLastWakeAt, in.HeartbeatMinutes) || in.CoordLastWakeAt.IsZero() && in.Run != nil && in.past(in.Run.StartedAt, in.HeartbeatMinutes)
}

func (in ProjectIn) coordSilent() bool {
	return in.idle() && !in.CoordWakeAt.IsZero() && in.Run.Turn <= in.CoordWakeTurn && in.past(in.CoordWakeAt, in.CoordinatorSilentMinutes)
}

func (in ProjectIn) startBlockedWhy() string {
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
	// Hourly limit raises agents_down in slice 5b (alerts table).
	if !capUntil.IsZero() && !gapUntil.After(capUntil) {
		return "coordinator_starts_per_hour reached; waiting (allowed at " + capUntil.UTC().Format(time.RFC3339) + ")"
	} else if !gapUntil.IsZero() {
		return "waiting for coordinator_gap_minutes before restarting coordinator (allowed at " + gapUntil.UTC().Format(time.RFC3339) + ")"
	}
	return ""
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

// ProjectRows is the ordered project rule table for the coordinator (design.md section 6).
// J1 (doctor) is slice 8; J3/J5 (alerts/sign-in) are slice 5b; J11 (rotation) is slice 13.
// J10 sits before J6/J7 so an R29 dialog-stuck end is noted before J6 waits or J7 restarts it.
var ProjectRows = []Row[ProjectIn]{
	pcond("J2", "owner", "stopped", func(in ProjectIn) bool { return !in.Started }, ProjectIn.alive, pdec("Stopped by you: stop the coordinator", AskStopCoordinator), pdec("Stopped by you")),
	prow("J4", "none", "finished", func(in ProjectIn) bool { return AllTasksEnded(in.Tasks) && !in.NewGoal && len(in.Wakes) == 0 }, func(in ProjectIn) Decision {
		if !in.alive() || !in.idle() {
			return pdec(map[bool]string{true: "Finished: all tasks ended", false: "Waiting for coordinator to finish final journal line"}[!in.alive()])
		}
		return map[bool]Decision{true: pdec("Stop coordinator after final journal line", AskStopCoordinator), false: pdec("Ask coordinator for final journal line", AskFinalJournal)}[Done(in.Acts, FinalJournalKey(in.Tasks))]
	}),
	// Alerts (agents_down) are slice 5b; second time in a row logs plainly for the owner.
	pcond("J10", "engine", "running", func(in ProjectIn) bool { return IsCoordR29(in.Run) && !Done(in.Acts, CoordStuckKey(in.Run)) }, func(in ProjectIn) bool { return in.CoordDialogStucks >= firstDialogStuck }, pdec("coordinator stuck at a dialog twice in a row (R29); needs owner attention", AskCoordDialogStuck), pdec("coordinator ended stuck at a dialog (R29); J7 will restart it", AskCoordDialogStuck)),
	prow("J6", "engine", "running", func(in ProjectIn) bool { return !in.alive() && in.startBlockedWhy() != "" }, func(in ProjectIn) Decision { return pdec(in.startBlockedWhy()) }),
	pcond("J7", "engine", "running", func(in ProjectIn) bool { return !in.alive() }, func(in ProjectIn) bool { return len(in.Tasks) == 0 }, pdec("project started with no coordinator run", AskStartCoordinator), pdec("no coordinator run is alive; start with handoff", AskStartCoordinator)),
	pcond("J8", "coordinator", "running", ProjectIn.coordError, func(in ProjectIn) bool {
		return !in.CoordErrorAt.IsZero() && !in.past(in.CoordErrorAt, in.QuietMinutes)
	}, pdec("coordinator reported another error within quiet_minutes: stop it", AskStopCoordinator), pdec("coordinator reported an error at turn end: tell it to go on", AskCoordGoOn)),
	// Second-in-a-row agents_down alert is slice 5b.
	pfix("J9", "coordinator", ProjectIn.coordSilent, "coordinator stayed silent for coordinator_silent_minutes: stop it", AskStopCoordinator),
	pfix("J12", "coordinator", func(in ProjectIn) bool { return in.idle() && len(in.Wakes) > 0 }, "wakes held and the coordinator is idle", AskDeliverWakes),
	pfix("J13", "coordinator", func(in ProjectIn) bool { return in.idle() && in.heartbeatDue() }, "no wake for heartbeat_minutes and coordinator is idle", AskHeartbeat),
	pfix("J14", "none", nil, "running"),
}
