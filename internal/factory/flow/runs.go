package flow

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Run phases, end results, events, screen classes and acts (design.md section 5).
const (
	PhaseStarting, PhaseWorking, PhaseIdle, PhaseBlocked, PhaseEnded                   = "starting", "working", "idle", "blocked", "ended"
	EndFailed, EndSignedOut, EndQuota, EndBlank, EndResumeFailed                       = "failed", "signed_out", "quota", "blank", "resume_failed"
	EndError, EndStuck, EndExited, EndLost, EndStopped                                 = "error", "stuck", "exited", "lost", "stopped"
	EvLaunched, EvLaunchFailed, EvScreen, EvStatus, EvActivity, EvTick, EvExit, EvLost = "launched", "launch_failed", "screen", "status", "activity", "tick", "exit", "lost"
	EvStop, ScreenNone, ScreenTrust, ScreenConfirm, ScreenOnboarding                   = "stop", "none", "trust", "confirm", "onboarding"
	ScreenSignedOut, ScreenLimit, RunActKeys, RunActSendFirst, RunActSendHeld          = "signed_out", "limit", "press_keys", "send_first_prompt", "send_held"
	RunActRead, RunActInterrupt, RunActStop, RunActRemove, liveSet                     = "read_report", "interrupt", "stop_run", "remove_terminal", "starting|working|idle|blocked"
	firstHeld                                                                          = 1
)

// RunRecord is a run's persisted state in state.json (design.md sections 3 and 5).
type RunRecord struct {
	ID, Address, Role, CLI, State, Result, Screen, ScreenHash, Status, BlockedKind, Question, Reason       string
	CreatedMS, Turn                                                                                        uint64
	StartedAt, StatusSince, LastActivity, LastTurnEnd, BlockedSince, InterruptedAt                         time.Time
	PromptViaArgv, PromptSent, Resume, NotResumable, ScreenAnswered, StatusReported, ReportRead, OwnerQuit bool
	Held                                                                                                   []string
}

// TaskRun projects a RunRecord into the Run fact that task rows read.
func (r *RunRecord) TaskRun() Run {
	if r == nil || r.State == "" {
		return Run{}
	}
	return Run{State: cmp.Or(map[string]RunState{PhaseEnded: RunGone, PhaseIdle: RunIdle}[r.State], RunWorking), LastTurnEnd: r.LastTurnEnd}
}

// RunEvent is one input to the run table reported by world or the engine.
type RunEvent struct {
	Kind, Address, RunID, Screen, ScreenHash, Status, StatusKind, Msg, Reason string
	CreatedMS, Turn                                                           uint64
	StatusAt                                                                  time.Time
	OwnerQuit                                                                 bool
}

// RunIn is what a run row sees: the run record, event, settings and clock.
type RunIn struct {
	Run      RunRecord
	Event    RunEvent
	Settings Settings
	Now      time.Time
}

func inSet(list, v string) bool { return slices.Contains(strings.Split(list, "|"), v) }
func isDialog(s string) bool    { return inSet("trust|confirm|onboarding", s) }
func isRevRole(r string) bool   { return inSet("reviewer|retro", r) }

func evalGuard(g string, x RunIn) bool {
	r, e, s := x.Run, x.Event, x.Settings
	past := func(t time.Time, d time.Duration) bool { return !t.IsZero() && x.Now.Sub(t) >= d }
	idleG := inSet("idle|done|error", r.Status) && past(r.StatusSince, time.Duration(s.IdleGraceSeconds)*time.Second)
	return map[string]bool{
		"": true, "dialog": isDialog(e.Screen) && !r.ScreenAnswered, "signed_out": e.Screen == ScreenSignedOut, "limit": e.Screen == ScreenLimit,
		"first_prompt": e.Status == "idle" && !r.PromptViaArgv && !r.PromptSent && !isDialog(r.Screen),
		"working":      e.Status == "working", "argv_done": inSet("idle|done", e.Status) && r.PromptViaArgv,
		"other":     e.Status != "blocked" && (e.Status != "error" || !isRevRole(r.Role)),
		"blank":     !r.StatusReported && past(r.StartedAt, time.Duration(s.StartMinutes)*time.Minute),
		"resume":    r.Resume && !r.StartedAt.IsZero() && !past(r.StartedAt, time.Duration(s.StartMinutes)*time.Minute),
		"auth_late": e.Status == "blocked" && e.StatusKind == "auth" && past(r.StartedAt, time.Duration(s.StartMinutes)*time.Minute),
		"blocked":   e.Status == "blocked", "rev_err": e.Status == "error" && isRevRole(r.Role),
		"held":       idleG && len(r.Held) > 0 && (!isRevRole(r.Role) || r.ReportRead),
		"idle_limit": idleG && inSet("idle|done", r.Status) && inSet(ScreenLimit, cmp.Or(e.Screen, r.Screen)),
		"idle":       idleG, "quiet": r.InterruptedAt.IsZero() && past(r.LastActivity, time.Duration(s.QuietMinutes)*time.Minute),
		"still_quiet": !r.LastActivity.After(r.InterruptedAt) && past(r.InterruptedAt, time.Duration(s.InterruptSeconds)*time.Second),
		"idle_done":   inSet("idle|done", e.Status), "blocked_long": past(r.BlockedSince, time.Duration(s.BlockedMinutes)*time.Minute),
	}[g]
}

func rrow(id, from, on, guard, to string, acts ...string) Row[RunIn] {
	var when func(RunIn) bool
	if from != "" {
		when = func(x RunIn) bool { return inSet(from, x.Run.State) && inSet(on, x.Event.Kind) && evalGuard(guard, x) }
	}
	return Row[RunIn]{ID: id, WaitsOn: "none", When: when, Then: func(x RunIn) Decision {
		why, asked := map[string]string{
			"R11": map[bool]string{false: "ended(blank)", true: "ended(stuck)"}[isDialog(x.Run.Screen)],
			"R29": map[bool]string{false: "ended(stuck)", true: "ended(signed_out)"}[x.Run.BlockedKind == "auth"],
			"R33": map[bool]string{false: "ended(lost)", true: "ended(exited)"}[x.Event.Kind == EvExit],
		}[id], acts
		if (id == "R20" && isRevRole(x.Run.Role)) || (id == "R33" && x.Event.Kind == EvExit) {
			asked = []string{map[bool]string{true: RunActRead, false: RunActRemove}[id == "R20"]}
		}
		return Decision{Asked: asked, Why: cmp.Or(why, to, x.Run.State)}
	}}
}

// RunRows is the run transition table of design.md section 5: R1-R23, R27-R30, R33-R35, R37.
// R24-R26 and R36 (deliver) are left for PR 5a.2; R31 and R32 (pool_floor) are slice 5b.
var RunRows = []Row[RunIn]{
	rrow("R1", PhaseStarting, EvLaunched, "", PhaseStarting),
	rrow("R2", PhaseStarting, EvLaunchFailed, "", "ended(failed)"),
	rrow("R3", PhaseStarting, EvScreen, "dialog", PhaseStarting, RunActKeys),
	rrow("R4", PhaseStarting, EvScreen, "signed_out", "ended(signed_out)", RunActStop),
	rrow("R5", PhaseStarting, EvScreen, "limit", "ended(quota)", RunActStop),
	rrow("R6", PhaseStarting, EvScreen, "", PhaseStarting),
	rrow("R7", PhaseStarting, EvStatus, "first_prompt", PhaseWorking, RunActSendFirst),
	rrow("R8", PhaseStarting, EvStatus, "working", PhaseWorking),
	rrow("R9", PhaseStarting, EvStatus, "argv_done", PhaseWorking),
	rrow("R10", PhaseStarting, EvStatus, "other", PhaseStarting),
	rrow("R11", PhaseStarting, EvTick, "blank", "", RunActStop),
	rrow("R12", PhaseStarting, "tick|activity", "", PhaseStarting),
	rrow("R13", "starting|working", EvExit, "resume", "ended(resume_failed)", RunActRemove),
	rrow("R14", "starting|working", EvStatus, "auth_late", "ended(signed_out)", RunActStop),
	rrow("R15", "starting|working", EvStatus, "blocked", PhaseBlocked),
	rrow("R16", "starting|working", EvStatus, "rev_err", "ended(error)", RunActStop),
	rrow("R17", PhaseWorking, EvStatus, "", PhaseWorking),
	rrow("R18", PhaseWorking, EvTick, "held", PhaseWorking, RunActSendHeld),
	rrow("R19", PhaseWorking, EvTick, "idle_limit", "ended(quota)", RunActStop),
	rrow("R20", PhaseWorking, EvTick, "idle", PhaseIdle),
	rrow("R21", PhaseWorking, EvTick, "quiet", PhaseWorking, RunActInterrupt),
	rrow("R22", PhaseWorking, EvTick, "still_quiet", "ended(stuck)", RunActStop),
	rrow("R23", PhaseWorking, "tick|activity", "", PhaseWorking),
	rrow("R27", "idle|blocked", EvStatus, "working", PhaseWorking),
	rrow("R28", PhaseBlocked, EvStatus, "idle_done", PhaseWorking),
	rrow("R29", PhaseBlocked, EvTick, "blocked_long", "", RunActStop),
	rrow("R30", "idle|blocked", "status|tick|activity|screen|launched|launch_failed", "", ""),
	rrow("R33", liveSet, "exit|lost", "", ""),
	rrow("R34", liveSet, EvStop, "", "ended(stopped)", RunActStop),
	rrow("R35", PhaseWorking, "launched|launch_failed|screen", "", PhaseWorking),
	rrow("R37", "", "", "", PhaseEnded),
}

// StepRun runs FirstMatch over RunRows and returns the updated RunRecord, matched row and decision.
func StepRun(rec RunRecord, ev RunEvent, s Settings, now time.Time) (RunRecord, Row[RunIn], Decision) {
	rec.Held = slices.Clone(rec.Held)
	r, d := FirstMatch(RunRows, RunIn{Run: rec, Event: ev, Settings: s, Now: now})
	if ev.ScreenHash != "" && rec.State != PhaseEnded {
		rec.ScreenHash = ev.ScreenHash
	}
	if res, ok := strings.CutPrefix(d.Why, "ended("); ok {
		rec.State, rec.Result = PhaseEnded, strings.TrimSuffix(res, ")")
	}
	stAt := cmp.Or(ev.StatusAt, now)
	switch {
	case r.ID == "R1":
		rec.Address, rec.ID, rec.CreatedMS, rec.StartedAt, rec.LastActivity = cmp.Or(ev.Address, rec.Address), cmp.Or(ev.RunID, rec.ID), cmp.Or(ev.CreatedMS, rec.CreatedMS), cmp.Or(rec.StartedAt, now), cmp.Or(rec.LastActivity, now)
	case inSet("R3|R4|R5|R6", r.ID):
		rec.Screen, rec.ScreenAnswered = ev.Screen, rec.ScreenAnswered || r.ID == "R3"
	case inSet("R7|R8|R9|R10|R17|R27|R28", r.ID):
		if r.ID == "R7" {
			ev.Status, stAt, rec.PromptSent = "working", now, true
		}
		if r.ID != "R17" {
			rec.State = map[bool]string{true: PhaseStarting, false: PhaseWorking}[r.ID == "R10"]
			rec.BlockedSince, rec.BlockedKind, rec.Question = time.Time{}, "", ""
		}
		if r.ID != "R17" || ev.Status != rec.Status || rec.StatusSince.IsZero() {
			rec.Status, rec.StatusSince, rec.InterruptedAt = ev.Status, stAt, time.Time{}
			if !inSet("R9|R10", r.ID) {
				rec.LastActivity = now
			}
		}
		rec.StatusReported, rec.Turn, rec.ReportRead = true, max(rec.Turn, ev.Turn), rec.ReportRead && r.ID != "R27"
	case inSet("R12|R23", r.ID) && ev.Kind == EvActivity:
		rec.LastActivity, rec.InterruptedAt = now, time.Time{}
	case r.ID == "R13":
		rec.NotResumable, rec.Reason = true, ev.Reason
	case r.ID == "R15":
		if rec.State != PhaseBlocked || rec.BlockedSince.IsZero() {
			rec.BlockedSince = stAt
		}
		rec.State, rec.StatusReported, rec.Status, rec.BlockedKind, rec.Question = PhaseBlocked, true, "blocked", ev.StatusKind, ev.Msg
	case r.ID == "R18":
		rec.Held, rec.StatusSince, rec.LastActivity = rec.Held[firstHeld:], now, now
	case r.ID == "R20":
		rec.State, rec.LastTurnEnd, rec.InterruptedAt, rec.Turn, rec.ReportRead = PhaseIdle, now, time.Time{}, max(rec.Turn, ev.Turn), rec.ReportRead || isRevRole(rec.Role)
	case r.ID == "R21":
		rec.InterruptedAt = now
	case r.ID == "R33":
		rec.Reason, rec.OwnerQuit = ev.Reason, ev.OwnerQuit
	}
	return rec, r, d
}
