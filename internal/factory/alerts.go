package factory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

func (e *Engine) reconcileAlertLocked(id string, s *flow.State, key string, active bool, a flow.Alert, logRule, logDec string) {
	if !active {
		if _, ok := s.Project.Alerts[key]; ok {
			if delete(s.Project.Alerts, key); len(s.Project.Alerts) == 0 {
				s.Project.Alerts = nil
			}
			e.saveLocked(id, *s)
		}
	} else if cur, ok := s.Project.Alerts[key]; !ok || cur.Scope != a.Scope {
		if s.Project.Alerts == nil {
			s.Project.Alerts = map[string]flow.Alert{}
		}
		a.At = a.At.UTC()
		s.Project.Alerts[key] = a
		e.commitLogLocked(id, s, LogLine{By: "engine", Rule: cmp.Or(logRule, "alert"), Asked: []string{a.Kind}, Decision: cmp.Or(logDec, "alert raised: "+a.Kind), Why: a.Cause})
	}
}

func (e *Engine) observeNoPoolLocked(ctx context.Context, id string, s *flow.State, obs []*flow.Facts, now time.Time) {
	if len(s.Tasks) == 0 || flow.AllTasksEnded(s.Tasks) && s.Project.NewGoal {
		var r flow.ResolveReason
		if !flow.RunLive(s.Project.RunID, s.Project.Run) && !s.Project.CoordSignOutHold() {
			f := e.probeLaunchFacts(ctx, flow.RoleCoordinator, flow.Task{}, s.Settings)
			f.Check = launchCapability(flow.RoleCoordinator)
			_, r = flow.ResolveWithReason(flow.RoleCoordinator, flow.Task{}, s.Settings, f)
		}
		e.reconcileAlertLocked(id, s, flow.AlertNoPool, r.Kind == flow.ReasonPool || r.PoolBlocked, flow.Alert{Kind: flow.AlertNoPool, Cause: r.Why, At: now, Rule: "J3"}, "J7", "waiting")
		return
	}
	anyObs, need, usable, poolReason := false, false, false, flow.ResolveReason{}
	for i, t := range s.Tasks {
		if obs[i] == nil {
			continue
		}
		anyObs = true
		f := *obs[i]
		f.NoSlot, f.GitHubUnavailable = false, e.ghUnavail[id] || s.Project.HasAlert(flow.AlertGitHubUnavailable)
		r0, d0 := flow.FirstMatch(flow.TaskRows, flow.In{Task: t, Settings: s.Settings, Facts: f, Now: now})
		asksWorker := slices.ContainsFunc(d0.Asked, func(k string) bool { return k == flow.AskNewAttempt || strings.HasPrefix(k, flow.AskStartWorker) })
		if !asksWorker && (f.Worker.State != flow.RunWorking && f.Worker.State != flow.RunIdle || !slices.Contains([]string{"N7", "N22", "N23", "N32", "N33", "N38", "N39"}, r0.ID)) {
			continue
		}
		need = need || asksWorker
		if _, r := resolveWorkerReason(t, s.Settings, e.probeLaunchFacts(ctx, flow.RoleWorker, t, s.Settings)); r.Why == "" {
			usable = true
		} else if (r.Kind == flow.ReasonPool || r.PoolBlocked) && (poolReason.Why == "" || !r.Reset.IsZero() && (poolReason.Reset.IsZero() || r.Reset.Before(poolReason.Reset))) {
			poolReason = r
		}
	}
	if anyObs {
		e.reconcileAlertLocked(id, s, flow.AlertNoPool, need && !usable && poolReason.Why != "", flow.Alert{Kind: flow.AlertNoPool, Cause: poolReason.Why, At: now, Rule: "J3"}, "J3", "")
	}
}

func (e *Engine) positiveCoordCheck(id string, clis map[string]flow.CLIFacts) func(flow.Candidate) flow.ResolveReason {
	return func(c flow.Candidate) flow.ResolveReason {
		if r := launchCapability(flow.RoleCoordinator)(c); r.Why != "" {
			return r
		} else if _, ok := clis[c.CLI]; !ok {
			return flow.ResolveReason{Why: c.CLI + " sign-in is not confirmed"}
		} else if c.CLI == world.CodexCLI {
			if _, err := e.codexHome(id); err != nil {
				return flow.ResolveReason{Why: err.Error()}
			}
		}
		return flow.ResolveReason{}
	}
}

func (e *Engine) observeCoordSignInLocked(ctx context.Context, id string, s *flow.State, now time.Time) bool {
	if flow.RunLive(s.Project.RunID, s.Project.Run) {
		if r := s.Project.Run; r != nil && (r.Turn > 0 || !r.LastTurnEnd.IsZero()) {
			for _, k := range []string{flow.AlertAgentsDown, flow.AlertAgentsDown + "@j6", flow.AlertAgentsDown + "@j10", flow.AlertAgentsDown + "@j9"} {
				if k != flow.AlertAgentsDown+"@j9" || len(s.Project.Wakes) == 0 && s.Project.CoordWakeAt.IsZero() {
					e.reconcileAlertLocked(id, s, k, false, flow.Alert{}, "", "")
				}
			}
		}
		return false
	}
	if _, capped := flow.CoordHourlyCap(s.Project, s.Settings, now); !capped {
		e.reconcileAlertLocked(id, s, flow.AlertAgentsDown+"@j6", false, flow.Alert{}, "", "")
	}
	run, held, heldCLI := s.Project.Run, s.Project.CoordSignOutHold(), ""
	runSignedOut := run != nil && run.State == flow.PhaseEnded && run.Result == flow.EndSignedOut && !flow.Done(s.Project.Acts, "j5@"+flow.CoordStuckKey(run))
	switch {
	case held:
		heldCLI = strings.TrimPrefix(s.Project.Alerts[flow.AlertAgentsDown].Scope, "signed_out:")
	case runSignedOut:
		heldCLI = cmp.Or(run.CLI, "claude")
		delete(e.cliProbes, heldCLI)
	case flow.AllTasksEnded(s.Tasks) && !s.Project.NewGoal && len(s.Project.Wakes) == 0:
		return false
	}
	facts := e.probeLaunchFacts(ctx, flow.RoleCoordinator, flow.Task{}, s.Settings)
	raiseJ5 := func(cli string) {
		e.reconcileAlertLocked(id, s, flow.AlertAgentsDown, true, flow.Alert{Kind: flow.AlertAgentsDown, Cause: cli + " coordinator is signed out", Fix: "sign in to " + cli + " at this Mac", At: now, Rule: "J5", Scope: "signed_out:" + cli}, "J5", "")
	}
	if heldCLI == "" {
		for _, c := range flow.Candidates(flow.RoleCoordinator, flow.Task{}, s.Settings) {
			if cf, ok := facts.CLIs[c.CLI]; ok && cf.Installed && launchCapability(flow.RoleCoordinator)(c).Why == "" {
				if cf.SignedIn {
					return false
				}
				heldCLI = cmp.Or(heldCLI, c.CLI)
			}
		}
		if heldCLI != "" {
			raiseJ5(heldCLI)
		}
		return false
	}
	if cf, ok := facts.CLIs[heldCLI]; ok && cf.Installed && cf.SignedIn {
		if runSignedOut {
			s.Project.Acts = append(s.Project.Acts, flow.Act{Key: "j5@" + flow.CoordStuckKey(run), Result: "done", At: now})
		}
		if delete(s.Project.Alerts, flow.AlertAgentsDown); len(s.Project.Alerts) == 0 {
			s.Project.Alerts = nil
		}
		e.saveLocked(id, *s)
		return false
	}
	raiseJ5(heldCLI)
	_, r := flow.ResolveWithReason(flow.RoleCoordinator, flow.Task{}, s.Settings, flow.ResolveFacts{CLIs: facts.CLIs, Pool: e.passPoolCheck, Check: e.positiveCoordCheck(id, facts.CLIs)})
	return r.Why == ""
}

func (e *Engine) reconcileCIStalledLocked(id string, s *flow.State, t flow.Task, rowID string, f flow.Facts, now time.Time) {
	key, a := flow.AlertCIStalled+"@"+t.ID, flow.OpenAttempt(t)
	cur, had := s.Project.Alerts[key]
	if flow.Ended(t) || a == nil {
		e.reconcileAlertLocked(id, s, key, false, flow.Alert{}, "", "")
	} else if h := flow.H(*a); rowID == "N36" && f.PR.Answered && !f.GitHubUnavailable && f.PR.State == flow.PROpen && f.PR.Checks == flow.ChecksPending && f.PR.Head == h &&
		a.PendingChecks != nil && a.PendingChecks.Head == h && !a.PendingChecks.Since.IsZero() && now.Sub(a.PendingChecks.Since) >= time.Duration(s.Settings.CIStalledMinutes)*time.Minute {
		e.reconcileAlertLocked(id, s, key, true, flow.Alert{Kind: flow.AlertCIStalled, Cause: fmt.Sprintf("task %s PR #%d checks pending at %s past %dm", t.ID, f.PR.Number, h[:min(len(h), 7)], s.Settings.CIStalledMinutes), At: now, Rule: "N36", Scope: h}, "N36", "")
	} else if had && (cur.Scope != h || f.PR.Answered && (f.PR.State != flow.PROpen || f.PR.Head != cur.Scope || f.PR.Checks != flow.ChecksPending && f.PR.Checks != flow.ChecksUnreadable)) {
		e.reconcileAlertLocked(id, s, key, false, flow.Alert{}, "", "")
	}
}
