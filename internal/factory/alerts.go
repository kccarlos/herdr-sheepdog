package factory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
)

type alertKey struct{ id, key string }
type pendingAlert struct {
	alert     flow.Alert
	rule, dec string
}

func setProjectAlert(s *flow.State, key string, a flow.Alert) {
	if s.Project.Alerts == nil {
		s.Project.Alerts = map[string]flow.Alert{}
	}
	s.Project.Alerts[key] = a
}

func (e *Engine) mergeMemAlertsLocked(id string, s *flow.State) {
	for k, p := range e.memAlerts {
		if k.id == id && s.Project.Alerts[k.key].Kind == "" {
			setProjectAlert(s, k.key, p.alert)
		}
	}
}

func (e *Engine) flushMemAlertsLocked(id string, s *flow.State) {
	for _, k := range slices.SortedFunc(maps.Keys(e.memAlerts), func(a, b alertKey) int { return cmp.Compare(a.key, b.key) }) {
		if k.id == id {
			p := e.memAlerts[k]
			setProjectAlert(s, k.key, p.alert)
			if err := e.commitLocked(id, s, LogLine{By: "engine", Rule: p.rule, Asked: []string{p.alert.Kind}, Decision: p.dec, Why: p.alert.Cause}); err == nil {
				delete(e.memAlerts, k)
			} else {
				e.logError("factory.commit_error", id, err)
			}
		}
	}
}

func (e *Engine) reconcileAlertLocked(id string, s *flow.State, key string, active bool, a flow.Alert, logRule, logDec string) {
	ak := alertKey{id, key}
	if !active {
		if cur, ok := s.Project.Alerts[key]; ok {
			if delete(s.Project.Alerts, key); len(s.Project.Alerts) == 0 {
				s.Project.Alerts = nil
			}
			if !e.saveLocked(id, *s) {
				setProjectAlert(s, key, cur)
			}
		}
	} else if cur, ok := s.Project.Alerts[key]; !ok || cur.Scope != a.Scope {
		a.At = a.At.UTC()
		rule, dec := cmp.Or(logRule, "alert"), cmp.Or(logDec, "alert raised: "+a.Kind)
		setProjectAlert(s, key, a)
		if err := e.commitLocked(id, s, LogLine{By: "engine", Rule: rule, Asked: []string{a.Kind}, Decision: dec, Why: a.Cause}); err == nil {
			delete(e.memAlerts, ak)
		} else {
			e.logError("factory.commit_error", id, err)
			if _, had := e.memAlerts[ak]; !had && a.Kind == flow.AlertDisk {
				_, _ = fmt.Fprintf(cmp.Or[io.Writer](e.Stderr, os.Stderr), "factory: alert %s (unpersisted): %s (%v)\n", a.Kind, a.Cause, err)
			}
			if e.memAlerts == nil {
				e.memAlerts = map[alertKey]pendingAlert{}
			}
			e.memAlerts[ak] = pendingAlert{alert: a, rule: rule, dec: dec}
		}
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
		r0, asksWorker := e.matchTaskStartLocked(id, *s, t, f, now)
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
	case flow.ProjectFinished(s.Project, s.Tasks):
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

func hcBoundedExec(ctx context.Context, client *hc.Client, timeout time.Duration, argv ...string) (hc.ExecResult, bool, error) {
	limit, c := timeout.Seconds(), *client
	c.Timeout = timeout + min(timeout, 2*time.Second)
	pctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	res, err := c.Exec(pctx, hc.ExecParams{Argv: argv, Cwd: os.TempDir(), Timeout: &limit})
	return res, res.TimedOut || err != nil && pctx.Err() != nil && ctx.Err() == nil, err
}

func (e *Engine) observeConsentDiskLocked(ctx context.Context, id string, s *flow.State, now time.Time) error {
	if prev := e.lastProbe[id]; !s.Project.HasAlert(flow.AlertConsentPrompt) && !s.Project.HasAlert(flow.AlertDisk) && !e.storeENOSPC[id] && e.consentStreaks[id] == "" && !prev.IsZero() && !now.Before(prev) && now.Sub(prev) < flow.PassInterval {
		return nil
	}
	e.lastProbe[id] = now
	err := e.observeConsentLocked(ctx, id, s, now)
	e.observeDiskLocked(ctx, id, s, now, false)
	return err
}

func (e *Engine) observeConsentLocked(ctx context.Context, id string, s *flow.State, now time.Time) error {
	if e.HC == nil || s.Project.Folder == "" {
		return nil
	}
	timeout, tempDir := cmp.Or(e.volumeTimeout, defaultVolumeTimeout), os.TempDir()
	probe := func(target string) (string, error) {
		res, timedOut, err := hcBoundedExec(ctx, e.HC, timeout, "sh", "-c", probeScript, "sh", target)
		if err != nil && !timedOut {
			var he *hc.Error
			if errors.As(err, &he) {
				return "", nil
			}
			return "", err
		}
		probed, ok := parseProbedDir(string(res.Stdout)), !timedOut && derefExit(res.Code) == 0
		same := probed != "" && strings.TrimPrefix(filepath.Clean(probed), "/private") == strings.TrimPrefix(filepath.Clean(target), "/private")
		return map[bool]string{timedOut: "timeout", ok && probed == "": "blank", ok && same: "ok"}[true], nil
	}
	badKind, err := probe(s.Project.Folder)
	if badKind != "timeout" && badKind != "blank" {
		delete(e.consentStreaks, id)
		if badKind == "ok" {
			e.reconcileAlertLocked(id, s, flow.AlertConsentPrompt, false, flow.Alert{}, "", "")
		}
		return err
	}
	if tk, terr := probe(tempDir); terr != nil || tk != "ok" {
		delete(e.consentStreaks, id)
		return terr
	}
	prev := e.consentStreaks[id]
	e.consentStreaks[id] = badKind
	if prev == badKind || s.Project.HasAlert(flow.AlertConsentPrompt) {
		home, _ := os.UserHomeDir()
		prompt, fix := consentPrompt(home, s.Project.Folder)
		cause := fmt.Sprintf("project folder %s %s twice in herdr-core (%s)", s.Project.Folder, map[string]string{"timeout": "timed out", "blank": "returned blank"}[badKind], prompt)
		e.reconcileAlertLocked(id, s, flow.AlertConsentPrompt, true, flow.Alert{Kind: flow.AlertConsentPrompt, Cause: cause, Fix: fix, At: now, Rule: "J3"}, "J3", "")
	}
	return nil
}

var dfCapacity = regexp.MustCompile(`\s+(\d+)%\s+(.+)$`)

func (e *Engine) readDiskVolume(ctx context.Context, path string) DiskReading {
	if path == "" || e.diskRead == nil && e.HC == nil {
		return DiskReading{Unknown: map[bool]string{true: "empty path", false: "no herdr-core client"}[path == ""]}
	} else if e.diskRead != nil {
		return e.diskRead(ctx, path)
	}
	res, timedOut, err := hcBoundedExec(ctx, e.HC, cmp.Or(e.volumeTimeout, defaultVolumeTimeout), "df", "-kP", path)
	if timedOut || err != nil || derefExit(res.Code) != 0 {
		return DiskReading{Unknown: cmp.Or(map[bool]string{true: "df timed out"}[timedOut], flow.CleanErr(errText(err)), flow.CleanErr(string(res.Stderr)), "df failed")}
	}
	lines := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	if m := dfCapacity.FindStringSubmatch(lines[len(lines)-1]); len(lines) >= 2 && m != nil {
		if pct, perr := strconv.Atoi(m[1]); perr == nil {
			return DiskReading{Volume: strings.TrimSpace(m[2]), UsedPercent: pct, Known: true}
		}
	}
	return DiskReading{Unknown: "unparseable df output"}
}

func (e *Engine) observeDiskLocked(ctx context.Context, id string, s *flow.State, now time.Time, gitFull bool) {
	proj, data := e.readDiskVolume(ctx, s.Project.Folder), e.readDiskVolume(ctx, e.Paths.DataDir)
	limit, projVol, dataVol := cmp.Or(s.Settings.DiskFullPercent, flow.DefaultSettings().DiskFullPercent), cmp.Or(proj.Volume, s.Project.Folder), cmp.Or(data.Volume, e.Paths.DataDir)
	storeFull := e.storeENOSPC[id]
	delete(e.storeENOSPC, id)
	var causes, vols []string
	for _, c := range []struct {
		on       bool
		vol, msg string
	}{
		{storeFull, dataVol, "store write failed (no space left on device) on " + dataVol},
		{gitFull || e.git != nil && e.git.TakeNoSpace(s.Project.Folder), projVol, "git write failed (no space left on device) on " + projVol},
		{proj.Known && proj.UsedPercent >= limit, projVol, fmt.Sprintf("volume %s is %d%% full (limit %d%%)", projVol, proj.UsedPercent, limit)},
		{data.Known && data.UsedPercent >= limit, dataVol, fmt.Sprintf("volume %s is %d%% full (limit %d%%)", dataVol, data.UsedPercent, limit)},
	} {
		if c.on {
			causes = append(causes, c.msg)
			if !slices.Contains(vols, c.vol) {
				vols = append(vols, c.vol)
			}
		}
	}
	raise := func(cause, fixVols string) {
		e.reconcileAlertLocked(id, s, flow.AlertDisk, true, flow.Alert{Kind: flow.AlertDisk, Cause: cause, Fix: "free space on " + fixVols, At: now, Rule: "J3"}, "J3", "")
	}
	if len(causes) > 0 {
		raise(strings.Join(causes, "; "), strings.Join(vols, ", "))
		return
	}
	if !s.Project.HasAlert(flow.AlertDisk) || !proj.Known || !data.Known {
		return
	}
	if e.git != nil && s.Project.Folder != "" {
		gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
		_, err := e.git.Run(gctx, s.Project.Folder, "config", "--local", "sheepdog.projectId", id)
		cancel()
		if err != nil {
			if e.git.TakeNoSpace(s.Project.Folder) {
				raise("git write failed (no space left on device) on "+projVol, projVol)
			}
			return
		}
	}
	e.reconcileAlertLocked(id, s, flow.AlertDisk, false, flow.Alert{}, "", "")
}
