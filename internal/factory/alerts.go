package factory

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

func runAlive(id string, r *flow.RunRecord) bool {
	return r != nil && r.State != flow.PhaseEnded || r == nil && id != ""
}

// reconcileAlertLocked maintains one persisted alert or outage episode on s.Project.Alerts.
func (e *Engine) reconcileAlertLocked(id string, s *flow.State, key string, active bool, a flow.Alert, logRule, logDec string) {
	if !active {
		if _, ok := s.Project.Alerts[key]; ok {
			delete(s.Project.Alerts, key)
			if len(s.Project.Alerts) == 0 {
				s.Project.Alerts = nil
			}
			e.saveLocked(id, *s)
		}
		return
	}
	if cur, ok := s.Project.Alerts[key]; ok && cur.Scope == a.Scope {
		return
	}
	if s.Project.Alerts == nil {
		s.Project.Alerts = map[string]flow.Alert{}
	}
	a.At = a.At.UTC()
	s.Project.Alerts[key] = a
	e.commitLogLocked(id, s, LogLine{By: "engine", Rule: cmp.Or(logRule, "alert"), Asked: []string{a.Kind}, Decision: cmp.Or(logDec, "alert raised: "+a.Kind), Why: a.Cause})
}

func eligibleWorkerTask(t flow.Task, tasks []flow.Task, s flow.Settings) bool {
	if flow.Ended(t) || t.Parked || flow.OpenRecap(t) != nil {
		return false
	}
	if a := flow.OpenAttempt(t); a == nil {
		nd, dr := flow.UnmetDeps(t, tasks)
		return len(nd) == 0 && len(dr) == 0
	} else if a.NothingToChange != "" || flow.Ladder(flow.In{Task: t, Settings: s}) == flow.Passed {
		return false
	}
	return true
}

func (e *Engine) observeNoPoolLocked(id string, s *flow.State, now time.Time) {
	if len(s.Tasks) == 0 {
		var r flow.ResolveReason
		active := !runAlive(s.Project.RunID, s.Project.Run) && !s.Project.CoordSignOutHold() && !flow.CoordStartBlocked(s.Project, s.Settings, now)
		if active {
			_, r = flow.ResolveWithReason(flow.RoleCoordinator, flow.Task{}, s.Settings, flow.ResolveFacts{Check: launchCapability(flow.RoleCoordinator), Pool: e.passPoolCheck})
			active = r.Kind == flow.ReasonPool
		}
		e.reconcileAlertLocked(id, s, flow.AlertNoPool, active, flow.Alert{Kind: flow.AlertNoPool, Cause: r.Why, At: now, Stopping: true, Rule: "J3"}, "J7", "waiting")
		return
	}
	need, usable, poolReason := false, false, flow.ResolveReason{}
	for _, t := range s.Tasks {
		if !eligibleWorkerTask(t, s.Tasks, s.Settings) {
			continue
		}
		if a := flow.OpenAttempt(t); a == nil || (!runAlive(a.RunID, a.Run) && !flow.Done(a.Acts, flow.AskWake+"@worker_failed")) {
			need = true
		}
		if _, r := resolveWorker(t, s.Settings, flow.ResolveFacts{Pool: e.passPoolCheck}); r.Why == "" {
			usable = true
		} else if r.Kind == flow.ReasonPool && (poolReason.Why == "" || (!r.Reset.IsZero() && (poolReason.Reset.IsZero() || r.Reset.Before(poolReason.Reset)))) {
			poolReason = r
		}
	}
	e.reconcileAlertLocked(id, s, flow.AlertNoPool, need && !usable && poolReason.Why != "", flow.Alert{Kind: flow.AlertNoPool, Cause: poolReason.Why, At: now, Stopping: true, Rule: "J3"}, "J3", "")
}

func positiveCoordCheck(clis map[string]flow.CLIFacts) func(flow.Candidate) flow.ResolveReason {
	return func(c flow.Candidate) flow.ResolveReason {
		if r := launchCapability(flow.RoleCoordinator)(c); r.Why != "" {
			return r
		}
		if cf, ok := clis[c.CLI]; !ok || !cf.Installed || !cf.SignedIn {
			return flow.ResolveReason{Why: c.CLI + " sign-in is not confirmed"}
		}
		return flow.ResolveReason{}
	}
}

func (e *Engine) observeCoordSignInLocked(ctx context.Context, id string, s *flow.State, justSignedOut bool, now time.Time) bool {
	if justSignedOut {
		cli := cmp.Or(s.Project.Run.CLI, "claude")
		delete(e.cliProbes, cli)
		facts := e.probeLaunchFacts(ctx, flow.RoleCoordinator, flow.Task{}, s.Settings)
		if cf, ok := facts.CLIs[cli]; !ok || !cf.Installed || !cf.SignedIn {
			e.reconcileAlertLocked(id, s, flow.AlertAgentsDown, true, flow.Alert{Kind: flow.AlertAgentsDown, Cause: cli + " coordinator is signed out", Fix: "sign in to " + cli + " at this Mac", At: now, Rule: "J5", Scope: "signed_out:" + cli}, "J5", "")
		}
	}
	if !s.Project.CoordSignOutHold() {
		return false
	}
	if runAlive(s.Project.RunID, s.Project.Run) {
		if s.Project.Run != nil && s.Project.Run.Turn > 0 {
			e.reconcileAlertLocked(id, s, flow.AlertAgentsDown, false, flow.Alert{}, "", "")
		}
		return false
	}
	heldCLI := strings.TrimPrefix(s.Project.Alerts[flow.AlertAgentsDown].Scope, "signed_out:")
	facts := e.probeLaunchFacts(ctx, flow.RoleCoordinator, flow.Task{}, s.Settings)
	if cf, ok := facts.CLIs[heldCLI]; ok && cf.Installed && cf.SignedIn {
		e.reconcileAlertLocked(id, s, flow.AlertAgentsDown, false, flow.Alert{}, "", "")
		return false
	}
	_, r := flow.ResolveWithReason(flow.RoleCoordinator, flow.Task{}, s.Settings, flow.ResolveFacts{CLIs: facts.CLIs, Pool: e.passPoolCheck, Check: positiveCoordCheck(facts.CLIs)})
	return r.Why == ""
}

func (e *Engine) reconcileCIStalledLocked(id string, s *flow.State, t flow.Task, rowID string, f flow.Facts, now time.Time) {
	key, a := flow.AlertCIStalled+"@"+t.ID, flow.OpenAttempt(t)
	cur, had := s.Project.Alerts[key]
	if flow.Ended(t) || a == nil {
		e.reconcileAlertLocked(id, s, key, false, flow.Alert{}, "", "")
		return
	}
	h := flow.H(*a)
	if had && cur.Scope != h {
		e.reconcileAlertLocked(id, s, key, false, flow.Alert{}, "", "")
		had = false
	}
	if rowID == "N36" && f.PR.Answered && !f.GitHubUnavailable && f.PR.State == flow.PROpen && f.PR.Checks == flow.ChecksPending && f.PR.Head == h &&
		a.PendingChecks != nil && a.PendingChecks.Head == h && !a.PendingChecks.Since.IsZero() && now.Sub(a.PendingChecks.Since) >= time.Duration(s.Settings.CIStalledMinutes)*time.Minute {
		e.reconcileAlertLocked(id, s, key, true, flow.Alert{Kind: flow.AlertCIStalled, Cause: fmt.Sprintf("task %s PR #%d checks pending at %s past %dm", t.ID, f.PR.Number, h[:min(len(h), 7)], s.Settings.CIStalledMinutes), At: now, Rule: "N36", Scope: h}, "N36", "")
	} else if had && f.PR.Answered && (f.PR.State != flow.PROpen || f.PR.Head != cur.Scope || (f.PR.Checks != flow.ChecksPending && f.PR.Checks != flow.ChecksUnreadable)) {
		e.reconcileAlertLocked(id, s, key, false, flow.Alert{}, "", "")
	}
}
