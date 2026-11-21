package flow

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Run phases, end results, events, screen classes and acts (design.md section 5).
const (
	PhaseStarting, PhaseWorking, PhaseIdle, PhaseBlocked, PhaseEnded                  = "starting", "working", "idle", "blocked", "ended"
	EndFailed, EndSignedOut, EndQuota, EndBlank, EndError, EndResumeFailed            = "failed", "signed_out", "quota", "blank", "error", "resume_failed"
	EndStuck, EndExited, EndLost, EndStopped                                          = "stuck", "exited", "lost", "stopped"
	EvLaunched, EvLaunchFailed, EvScreen, EvStatus, EvDeliver                         = "launched", "launch_failed", "screen", "status", "deliver"
	EvActivity, EvTick, EvExit, EvLost, EvStop                                        = "activity", "tick", "exit", "lost", "stop"
	ScreenNone, ScreenTrust, ScreenConfirm, ScreenOnboarding                          = "none", "trust", "confirm", "onboarding"
	ScreenSignedOut, ScreenLimit                                                      = "signed_out", "limit"
	RunActKeys, RunActSend, RunActSendHeld, RunActInterrupt, RunActStop, RunActRemove = "press_keys", "send_message", "send_held", "interrupt", "stop_run", "remove_terminal"
	RefuseRunEnded                                                                    = "run_ended"
	liveSet, firstHeld                                                                = "starting|working|idle|blocked", 1
)

// RunRecord is a run's persisted state in state.json (design.md sections 3 and 5).
type RunRecord struct {
	ID, Address, Role, CLI, Model, SessionID, State, Result, Screen, ScreenHash, Status, BlockedKind, Question, Reason string
	CreatedMS, Turn                                                                                                    uint64
	StartedAt, EndedAt, StatusSince, LastActivity, LastTurnEnd, BlockedSince, InterruptedAt, LastSentAt                time.Time
	StatusReported, Resumable, Resumed                                                                                 bool
	ScreensAnswered, Held                                                                                              []string
	StopRetry                                                                                                          *StopRetry `json:"stop_retry,omitempty"`
}

// StopRetry preserves a run-table stop until the executor succeeds.
type StopRetry struct {
	Row, Result string
	At          time.Time
	Tries       int
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
	Kind, Address, RunID, Screen, ScreenHash, Status, StatusKind, Msg, Reason, Model string
	CreatedMS, Turn                                                                  uint64
	StatusAt                                                                         time.Time
	Urgent                                                                           bool
}

// RunIn is what a run row sees: the run record, event, settings and clock.
type RunIn struct {
	R   RunRecord
	E   RunEvent
	S   Settings
	Now time.Time
}

// ScreenKeys returns the verified keystrokes that answer a start dialog (design.md 9.2, spike S0.2).
func ScreenKeys(cli, screen string) []string {
	if cli == "agy" && screen == ScreenTrust {
		return []string{"enter"}
	}
	return nil
}

func inSet(list, v string) bool { return slices.Contains(strings.Split(list, "|"), v) }
func isDialog(s string) bool    { return inSet("trust|confirm|onboarding", s) }
func isRevRole(r string) bool   { return inSet("reviewer|retro", r) }
func matchEv(on string, e RunEvent) bool {
	return inSet(on, e.Kind) || inSet(on, e.Kind+":"+cmp.Or(e.Screen, e.Status))
}
func (x RunIn) past(t time.Time, d time.Duration) bool { return !t.IsZero() && x.Now.Sub(t) >= d }
func (x RunIn) pastMin(t time.Time, m int) bool        { return x.past(t, time.Duration(m)*time.Minute) }
func (x RunIn) pastSec(t time.Time, s int) bool        { return x.past(t, time.Duration(s)*time.Second) }
func (x RunIn) startOut() bool {
	return !x.R.StatusReported && x.pastMin(x.R.StartedAt, x.S.StartMinutes)
}
func (x RunIn) blockedOut() bool { return x.pastMin(x.R.BlockedSince, x.S.BlockedMinutes) }
func (x RunIn) idle() bool {
	return inSet("idle|done|error", x.R.Status) && x.pastSec(x.R.StatusSince, x.S.IdleGraceSeconds)
}

func rrow(id, from, on string, guard func(RunIn) bool, to string, acts ...string) Row[RunIn] {
	when := func(x RunIn) bool { return inSet(from, x.R.State) && matchEv(on, x.E) && (guard == nil || guard(x)) }
	return Row[RunIn]{ID: id, WaitsOn: "none", When: when, Then: func(x RunIn) Decision { return Decision{Asked: acts, Why: cmp.Or(to, x.R.State)} }}
}

// RunRows is the run transition table of design.md section 5: R1-R6, R8-R30, R33-R37.
// R7 (typed first prompt) waits for non-argv launches; R31 and R32 (pool_floor) are slice 5b.
var RunRows = []Row[RunIn]{
	rrow("R1", PhaseStarting, EvLaunched, nil, PhaseStarting),
	rrow("R2", PhaseStarting, EvLaunchFailed, nil, "ended(failed)"),
	rrow("R3", PhaseStarting, EvScreen, func(x RunIn) bool {
		return ScreenKeys(x.R.CLI, x.E.Screen) != nil && !slices.Contains(x.R.ScreensAnswered, x.E.Screen)
	}, PhaseStarting, RunActKeys),
	rrow("R4", PhaseStarting, "screen:signed_out", nil, "ended(signed_out)", RunActStop),
	rrow("R5", PhaseStarting, "screen:limit", nil, "ended(quota)", RunActStop),
	rrow("R6", PhaseStarting, EvScreen, nil, PhaseStarting),
	rrow("R8", PhaseStarting, "status:working", nil, PhaseWorking),
	rrow("R9", PhaseStarting, EvStatus, func(x RunIn) bool {
		return inSet("idle|done", x.E.Status) || x.E.Status == "error" && !isRevRole(x.R.Role)
	}, PhaseWorking),
	rrow("R10", PhaseStarting, EvStatus, func(x RunIn) bool { return x.E.Status != "blocked" && x.E.Status != "error" }, PhaseStarting),
	rrow("R11", PhaseStarting, EvTick, func(x RunIn) bool { return x.startOut() && isDialog(x.R.Screen) }, "ended(stuck)", RunActStop),
	rrow("R11", PhaseStarting, EvTick, RunIn.startOut, "ended(blank)", RunActStop),
	rrow("R12", PhaseStarting, "tick|activity", nil, PhaseStarting),
	rrow("R13", "starting|working", EvExit, func(x RunIn) bool {
		return x.R.Resumed && x.E.Reason != "killed" && !x.pastMin(x.R.StartedAt, x.S.StartMinutes)
	}, "ended(resume_failed)", RunActRemove),
	rrow("R14", "starting|working", "status:blocked", func(x RunIn) bool { return x.E.StatusKind == "auth" && x.pastMin(x.R.StartedAt, x.S.StartMinutes) }, "ended(signed_out)", RunActStop),
	rrow("R15", "starting|working", "status:blocked", nil, PhaseBlocked),
	rrow("R16", "starting|working", EvStatus, func(x RunIn) bool { return x.E.Status == "error" && isRevRole(x.R.Role) }, "ended(error)", RunActStop),
	rrow("R17", PhaseWorking, EvStatus, nil, PhaseWorking),
	rrow("R18", PhaseWorking, EvTick, func(x RunIn) bool { return x.idle() && len(x.R.Held) > 0 && !isRevRole(x.R.Role) }, PhaseWorking, RunActSendHeld),
	rrow("R19", PhaseWorking, EvTick, func(x RunIn) bool {
		return x.idle() && x.R.Status != "error" && cmp.Or(x.E.Screen, x.R.Screen) == ScreenLimit
	}, "ended(quota)", RunActStop),
	rrow("R20", PhaseWorking, EvTick, RunIn.idle, PhaseIdle),
	rrow("R21", PhaseWorking, EvTick, func(x RunIn) bool { return x.R.InterruptedAt.IsZero() && x.pastMin(x.R.LastActivity, x.S.QuietMinutes) }, PhaseWorking, RunActInterrupt),
	rrow("R22", PhaseWorking, EvTick, func(x RunIn) bool {
		return !x.R.LastActivity.After(x.R.InterruptedAt) && x.pastSec(x.R.InterruptedAt, x.S.InterruptSeconds)
	}, "ended(stuck)", RunActStop),
	rrow("R23", PhaseWorking, "tick|activity", nil, PhaseWorking),
	rrow("R24", PhaseIdle, EvDeliver, nil, PhaseWorking, RunActSend),
	rrow("R25", PhaseWorking, EvDeliver, func(x RunIn) bool { return x.E.Urgent }, PhaseWorking, RunActInterrupt, RunActSend),
	rrow("R26", "starting|working|blocked", EvDeliver, nil, ""),
	rrow("R27", "idle|blocked", "status:working", nil, PhaseWorking),
	rrow("R28", PhaseBlocked, "status:idle|status:done", nil, PhaseWorking),
	rrow("R29", PhaseBlocked, EvTick, func(x RunIn) bool { return x.R.BlockedKind == "auth" && x.blockedOut() }, "ended(signed_out)", RunActStop),
	rrow("R29", PhaseBlocked, EvTick, RunIn.blockedOut, "ended(stuck)", RunActStop),
	rrow("R30", "idle|blocked", "status|tick|activity|screen|launched|launch_failed", nil, ""),
	rrow("R33", liveSet, EvExit, nil, "ended(exited)", RunActRemove),
	rrow("R33", liveSet, EvLost, nil, "ended(lost)"),
	rrow("R34", liveSet, EvStop, nil, "ended(stopped)", RunActStop),
	rrow("R35", PhaseWorking, "launched|launch_failed|screen", nil, PhaseWorking),
	rrow("R36", PhaseEnded, EvDeliver, nil, RefuseRunEnded),
	{ID: "R37", WaitsOn: "none", Then: func(RunIn) Decision { return Decision{Why: PhaseEnded} }},
}

// StepRun runs FirstMatch over RunRows and returns the updated RunRecord, matched row and decision.
func StepRun(rec RunRecord, ev RunEvent, s Settings, now time.Time) (RunRecord, Row[RunIn], Decision) {
	sentAt := rec.LastSentAt
	if rec.State == PhaseWorking && rec.Status == "" {
		sentAt = cmp.Or(sentAt, rec.StatusSince) // R24/R25 records saved before LastSentAt was added.
	}
	// R21/R22 bound a quiet run while it waits for fresh status after a send.
	if ev.Kind == EvStatus && !ev.StatusAt.IsZero() && !ev.StatusAt.After(sentAt) {
		return rec, Row[RunIn]{}, Decision{}
	}
	rec.Held, rec.ScreensAnswered = slices.Clone(rec.Held), slices.Clone(rec.ScreensAnswered)
	r, d := FirstMatch(RunRows, RunIn{R: rec, E: ev, S: s, Now: now})
	if rec.Model == "" {
		rec.Model = ev.Model
	}
	if ev.ScreenHash != "" && rec.State != PhaseEnded {
		rec.ScreenHash = ev.ScreenHash
	}
	if res, ok := strings.CutPrefix(d.Why, "ended("); ok {
		rec.State, rec.Result, rec.EndedAt, rec.StopRetry = PhaseEnded, strings.TrimSuffix(res, ")"), now, nil
	}
	stAt := cmp.Or(ev.StatusAt, now)
	switch {
	case r.ID == "R1":
		rec.Address, rec.ID, rec.CreatedMS = cmp.Or(ev.Address, rec.Address), cmp.Or(ev.RunID, rec.ID), cmp.Or(ev.CreatedMS, rec.CreatedMS)
		rec.StartedAt, rec.LastActivity = cmp.Or(rec.StartedAt, now), cmp.Or(rec.LastActivity, now)
	case r.ID == "R3":
		rec.Screen, rec.ScreensAnswered = ev.Screen, append(rec.ScreensAnswered, ev.Screen)
	case inSet("R4|R5|R6", r.ID):
		rec.Screen = ev.Screen
	case inSet("R8|R9|R17|R27|R28", r.ID):
		if r.ID != "R17" {
			rec.State, rec.BlockedSince, rec.BlockedKind, rec.Question = PhaseWorking, time.Time{}, "", ""
		}
		if r.ID != "R17" || ev.Status != rec.Status || rec.StatusSince.IsZero() {
			rec.Status, rec.StatusSince, rec.InterruptedAt = ev.Status, stAt, time.Time{}
			if r.ID != "R9" {
				rec.LastActivity = now
			}
		}
		if ev.Turn > rec.Turn {
			rec.LastTurnEnd = time.Time{}
		}
		rec.StatusReported, rec.Turn = true, max(rec.Turn, ev.Turn)
	case inSet("R12|R23", r.ID) && ev.Kind == EvActivity:
		rec.LastActivity, rec.InterruptedAt = now, time.Time{}
	case r.ID == "R13":
		rec.Resumable, rec.Reason = false, ev.Reason
	case r.ID == "R15":
		if rec.State != PhaseBlocked || rec.BlockedSince.IsZero() {
			rec.BlockedSince = stAt
		}
		rec.State, rec.StatusReported, rec.Status, rec.BlockedKind, rec.Question = PhaseBlocked, true, "blocked", ev.StatusKind, ev.Msg
	case r.ID == "R18":
		rec.Held, rec.StatusSince, rec.LastActivity = rec.Held[firstHeld:], now, now
	case r.ID == "R20":
		if rec.LastTurnEnd.IsZero() || !rec.StatusSince.Before(rec.LastTurnEnd) {
			rec.LastTurnEnd = now
		}
		rec.State, rec.InterruptedAt, rec.Turn = PhaseIdle, time.Time{}, max(rec.Turn, ev.Turn)
	case r.ID == "R21":
		rec.InterruptedAt, rec.ScreenHash = now, ""
	case inSet("R24|R25", r.ID):
		rec.LastSentAt = now
		rec.State, rec.Status, rec.StatusSince, rec.LastActivity, rec.InterruptedAt = PhaseWorking, "", now, now, time.Time{}
	case r.ID == "R26":
		rec.Held = append(rec.Held, ev.Msg)
	case r.ID == "R33":
		rec.Reason = ev.Reason
	}
	return rec, r, d
}
