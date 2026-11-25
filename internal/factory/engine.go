package factory

import (
	"cmp"
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
)

//go:embed coordinator.md
var coordinatorTemplate string

//go:embed worker.md
var workerTemplate string

//go:embed reviewer.md
var reviewerTemplate string

//go:embed reviewer_cmd.md
var reviewerCmdTemplate string

//go:embed retro.md
var retroTemplate string

//go:embed plan_reviewer.md
var planReviewerTemplate string

var lockGitTimeout = world.GitStageTimeout

// ghTimeout bounds one GitHub executor or snapshot (a push plus gh calls).
var ghTimeout = time.Minute

// startCheckTimeout bounds StartChecks inside Engine.Start (before e.mu).
const startCheckTimeout = 90 * time.Second

// DiskReading is one volume's space reading for the disk alert observer (design.md 6, 7.3).
type DiskReading struct {
	Volume      string
	UsedPercent int
	Known       bool
	Unknown     string
}

// Engine runs the factory loop and handles owner start and stop commands.
type Engine struct {
	Paths        Paths
	HC           *hc.Client
	Log          logging.Logger
	Stderr       io.Writer // where unpersistable alerts (e.g. disk ENOSPC) are logged; nil uses os.Stderr
	PassInterval time.Duration
	Now          func() time.Time                                      // wall clock; nil uses time.Now
	Since        func(time.Time) time.Duration                         // monotonic elapsed since previous pass; nil uses time.Since (wall delta when Now is set)
	Bin          string                                                // sheepdog binary named in run prompts
	RunEnv       map[string]string                                     // added to every run's environment (daemon port)
	StartCheck   func(context.Context, DoctorOptions) []onboard.Result // nil runs StartChecks

	probeLaunch       func(context.Context, []string) flow.ResolveFacts // test hook
	poolRun           CommandFunc                                       // provider quota reader hook; nil uses hcExecRunner
	diskRead          func(context.Context, string) DiskReading         // volume space reader hook; nil uses real volume reader
	writeState        func(string, flow.State) error                    // store write hook; nil uses SaveState
	writeLog          func(string, LogLine) error                       // log append hook; nil uses AppendLog
	volumeTimeout     time.Duration                                     // protected-folder probe timeout; 0 uses defaultVolumeTimeout
	cliProbes         map[string]cachedCLIProbe                         // shared across launches and projects under mu
	pools             map[string]cachedPool                             // shared per-provider quota readings under mu
	passPools         map[string]poolReading                            // current pass's immutable pool readings under mu
	passPoolCheck     func(flow.Candidate) flow.ResolveReason           // current pass's immutable pool check under mu
	passCoordFallback bool                                              // current pass's J5 fallback sign-in check under mu
	passBaseHCErr     error                                             // current pass's HC RPC error from observeBaseLocked under mu
	storeENOSPC       map[string]bool                                   // pending store ENOSPC per project under mu
	logClean          map[string]bool                                   // true after TrimLog + state write-back succeeded under mu
	git               *world.Git
	mu                sync.Mutex
	wake              chan struct{}
	lastPass          map[string]time.Time            // monotonic reading of previous pass per project (design.md 2.5)
	lastSweep         map[string]time.Time            // per-process last sweep per project (design.md 7.3)
	lastProbe         map[string]time.Time            // per-process last consent/disk probe per project
	consentStreaks    map[string]string               // consecutive positive consent-probe failure kind per project
	memAlerts         map[alertKey]pendingAlert       // active alerts kept in memory when store writes fail
	prs               map[string]flow.PR              // last PR snapshot per attempt worktree (in memory)
	errs              map[string]string               // last gh or git error per worktree and kind, logged once per change
	tips              map[string]tipFacts             // last git reading per worktree (in memory)
	waits             map[string]string               // last wait/finished reason per project or task, logged once per change
	ghUnavail         map[string]bool                 // project fact github_unavailable (design.md 7.3)
	hcUnavail         map[string]bool                 // project fact hc_unavailable (design.md 7.3)
	onInvariant       func([]flow.InvariantViolation) // test hook; nil in production
	changeSubs        []chan Change                   // Ask/Answer/Say subscribers under mu
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// passDelta returns the monotonic time since id's previous pass (capped at
// 2× the pass interval), so machine sleep does not count toward N14/N29
// durations without shifting wall time, and slow passes neither freeze nor jump.
func (e *Engine) passDelta(id string) time.Duration {
	prev := e.lastPass[id]
	if e.lastPass == nil {
		e.lastPass = map[string]time.Time{}
	}
	if prev.IsZero() {
		e.lastPass[id] = e.now()
		return 0
	}
	var dt time.Duration
	switch {
	case e.Since != nil:
		dt = e.Since(prev)
	case e.Now == nil:
		dt = time.Since(prev)
	default:
		dt = e.now().Sub(prev)
	}
	e.lastPass[id] = e.now()
	if dt <= 0 {
		return 0
	}
	return min(dt, 2*cmp.Or(e.PassInterval, flow.PassInterval))
}

// tipFacts are N12's and N20's git facts for one tip and H: the merge base,
// and so the line count, moves only with the tip.
type tipFacts struct {
	tip, h           string
	lines, generated int
	mergeOnly        bool
}

// New constructs an Engine. Nothing runs or touches disk until Run or Start.
func New(paths Paths, client *hc.Client) *Engine {
	e := &Engine{Paths: paths, HC: client, wake: make(chan struct{}, 1), lastSweep: map[string]time.Time{}, lastProbe: map[string]time.Time{}, consentStreaks: map[string]string{}, memAlerts: map[alertKey]pendingAlert{}}
	e.git = world.NewGit(client, e.now, func(event, folder string) { e.logEvent(event, map[string]string{"folder": folder}) })
	return e
}

// Start runs start checks (J1), records the project (storing its id in local
// git config), marks it started, and wakes the loop. A non-zero maxRunning
// sets Settings.MaxRunning.
func (e *Engine) Start(folder, goal string, maxRunning int) error {
	folder, goal = strings.TrimSpace(folder), strings.TrimSpace(goal)
	if folder == "" || goal == "" {
		return errors.New("folder and goal are required")
	}
	check := e.StartCheck
	if check == nil {
		check = StartChecks
	}
	opts := DoctorOptions{Folder: folder, DataDir: e.Paths.DataDir}
	if e.HC != nil {
		opts.HCSocket = e.HC.Socket
	}
	checkCtx, checkCancel := context.WithTimeout(context.Background(), startCheckTimeout)
	results := check(checkCtx, opts)
	checkCancel()
	var failed []string
	for _, r := range results {
		if r.Status == onboard.Fail {
			failed = append(failed, r.Line())
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("not starting; fix these first (sheepdog factory doctor %s shows every check):\n%s", folder, strings.Join(failed, "\n"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
	defer cancel()
	root, id, err := e.git.RepoProject(ctx, folder, true)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := LoadState(e.Paths.State(id))
	if os.IsNotExist(err) {
		s = flow.State{Project: flow.Project{ID: id}, Settings: flow.DefaultSettings()}
	} else if err != nil {
		return err
	}
	if err := e.freshLocked(id, s); err != nil {
		return err
	}
	if maxRunning != 0 {
		s.Settings.MaxRunning = maxRunning
	}
	if err := s.Settings.Validate(); err != nil {
		return err
	}
	if s.Project.RunID != "" {
		_, _ = e.observeCoordLocked(ctx, id, &s)
	}
	if s.Project.Goal != "" && goal != s.Project.Goal && len(s.Tasks) > 0 {
		s.Project.NewGoal, s.Project.NewGoalTasks = true, len(s.Tasks)
		if s.Project.RunID != "" || (s.Project.Run != nil && s.Project.Run.State != flow.PhaseEnded) {
			holdProjectWake(&s, "new_goal", "owner gave a new goal", "goal: "+goal, e.now())
		}
	}
	s.Project.Folder, s.Project.Goal, s.Project.Started, s.Project.Finished, s.Project.Acts, s.Project.Base, s.Project.Invariants, s.Project.WaitKey, s.Project.CoordSilentStops = root, goal, true, false, nil, nil, nil, "", 0
	clearCoordTrack(&s.Project, true, true)
	delete(e.lastSweep, id)
	delete(e.lastProbe, id)
	delete(e.consentStreaks, id)
	// The owner starting again is slice 1's retry (plan.md: "the owner
	// starts again"): failed executor acts no longer block their ask.
	for ti := range s.Tasks {
		s.Tasks[ti].Acts = dropFailed(s.Tasks[ti].Acts)
		for ai := range s.Tasks[ti].Attempts {
			a := &s.Tasks[ti].Attempts[ai]
			a.Acts, a.Restarts = dropFailed(a.Acts), 0
			if a.Review != nil {
				a.Review.Retries, a.Review.FailedModels = 0, nil
			}
		}
	}
	return e.commitLocked(id, &s, LogLine{By: "owner", Input: map[string]string{"cmd": "start", "folder": root, "goal": goal}, Rule: "start", Decision: "started", Why: goal})
}

// taskRuns names every run the task may have started: the recorded ones,
// and by name the ones a crash left unrecorded.
func taskRuns(id string, t flow.Task) []string {
	var runs []string
	for ai, a := range t.Attempts {
		runs = append(runs, cmp.Or(a.RunID, workerName(id, t.ID, ai+1)))
		if a.Review != nil {
			runs = append(runs, a.Review.RunID)
		}
		for r := 1; r <= len(a.Rounds)+1; r++ {
			runs = append(runs, reviewerName(id, t.ID, ai+1, r))
		}
	}
	return runs
}

// execStopRuns is N3: a parked task keeps no run alive. Stopping records
// EndStopped on the runs so unparking restarts them without counting (4.8).
func (e *Engine) execStopRuns(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t := &s.Tasks[ti]
	if err := e.stopRuns(ctx, taskRuns(id, *t)); err != nil {
		return "", false, err
	}
	stopTaskRuns(t, s.Settings, e.now())
	return "stopped the task's runs", false, nil
}

func stopRec(recp **flow.RunRecord, role string, st flow.Settings, now time.Time) {
	if *recp == nil {
		*recp = &flow.RunRecord{Role: role, State: flow.PhaseEnded, Result: flow.EndStopped, EndedAt: now}
	} else if (*recp).State != flow.PhaseEnded {
		r, _, _ := flow.StepRun(**recp, flow.RunEvent{Kind: flow.EvStop}, st, now)
		*recp = &r
	}
}

func stopReviewRec(rev *flow.ReviewRun, st flow.Settings, now time.Time) {
	if rev != nil {
		stopRec(&rev.Run, rev.Role(), st, now)
		rev.RunID, rev.RunTokenHash = "", ""
	}
}

func stopWorkerRec(a *flow.Attempt, st flow.Settings, now time.Time, keepResume bool) {
	if a.RunID != "" || a.Run != nil || flow.Done(a.Acts, flow.AskStartWorker) {
		stopRec(&a.Run, flow.RoleWorker, st, now)
		a.Run.Resumable, a.Held, a.Run.Held, a.RunID, a.RunTokenHash = a.Run.Resumable && keepResume, appendHeld(a.Held, a.Run.Held...), nil, "", ""
	}
}

func stopTaskRuns(t *flow.Task, st flow.Settings, now time.Time) {
	if a := flow.OpenAttempt(*t); a != nil {
		stopWorkerRec(a, st, now, true)
		stopReviewRec(a.Review, st, now)
	}
}

func (e *Engine) stopRuns(ctx context.Context, runs []string) error {
	if e.HC == nil { // without herdr-core (mock mode) nothing was started
		return nil
	}
	for _, r := range runs {
		if err := world.StopRun(ctx, e.HC, r); err != nil {
			return err
		}
	}
	return nil
}

func dropFailed(acts []flow.Act) []flow.Act {
	return slices.DeleteFunc(acts, func(a flow.Act) bool {
		return a.Result == "failed" || a.Key == flow.AskWake+"@worker_failed" || strings.HasPrefix(a.Key, flow.AskWake+"@reviewer_failed@") || strings.HasPrefix(a.Key, flow.AskWake+"@failed@")
	})
}

// Stop marks project stopped and stops every run it started, leaving no process.
func (e *Engine) Stop(project string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	e.mu.Lock()
	defer e.mu.Unlock()
	id, s, err := e.findProjectLocked(ctx, strings.TrimSpace(project))
	if err != nil {
		return err
	}
	freshErr := e.freshLocked(id, s)
	if err := e.stopCoordTerminalsLocked(ctx, id, s); err != nil {
		return err
	}
	var runs []string
	for _, pr := range s.Project.PlanReviews {
		if pr.Run != nil {
			runs = append(runs, cmp.Or(pr.Run.RunID, planReviewerName(id, pr.N)))
		}
	}
	for ti := range s.Tasks {
		runs = append(runs, taskRuns(id, s.Tasks[ti])...)
	}
	if err := e.stopRuns(ctx, runs); err != nil {
		return err
	}
	if freshErr != nil {
		return freshErr
	}
	now := e.now()
	for ti := range s.Tasks {
		stopTaskRuns(&s.Tasks[ti], s.Settings, now)
	}
	for i := range s.Project.PlanReviews {
		stopReviewRec(s.Project.PlanReviews[i].Run, s.Settings, now)
	}
	if n := len(s.Project.PlanReviews); n > 0 && s.Project.PlanReviews[n-1].Verdict == "" {
		s.Project.PlanReviews = s.Project.PlanReviews[:n-1]
	}
	if s.Project.Run != nil {
		stopRec(&s.Project.Run, flow.RoleCoordinator, s.Settings, now)
		reconcileCoordWakes(&s.Project)
	}
	s.Project.RunID, s.Project.RunTokenHash, s.Project.Run, s.Project.Started, s.Project.Finished, s.Project.Acts, s.Project.Base, s.Project.NewGoal, s.Project.NewGoalTasks, s.Project.Invariants, s.Project.WaitKey, s.Project.CoordSilentStops = "", "", nil, false, false, nil, nil, false, 0, nil, "", 0
	clearCoordTrack(&s.Project, true, true)
	delete(e.lastPass, id)
	delete(e.lastSweep, id)
	delete(e.lastProbe, id)
	delete(e.consentStreaks, id)
	if err := e.commitLocked(id, &s, LogLine{By: "owner", Input: map[string]string{"cmd": "stop"}, Rule: "stop", Decision: "stopped", Why: "stopped by owner"}); err != nil {
		return err
	}
	maps.DeleteFunc(e.memAlerts, func(k alertKey, _ pendingAlert) bool { return k.id == id })
	return nil
}

// Run drives the periodic pass loop until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(cmp.Or(e.PassInterval, flow.PassInterval))
	defer t.Stop()
	for {
		for _, ent := range e.projectDirs() {
			if ctx.Err() == nil && ent.IsDir() {
				e.passProject(ctx, ent.Name())
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		case <-t.C:
		}
	}
}

func (e *Engine) projectDirs() []os.DirEntry {
	dir := filepath.Join(e.Paths.DataDir, "factory", "projects")
	if guardTestPath(dir) != nil {
		return nil
	}
	entries, _ := os.ReadDir(dir)
	return entries
}

func (e *Engine) eachStateLocked(fn func(id string, s flow.State) bool) {
	for _, ent := range e.projectDirs() {
		if s, err := LoadState(e.Paths.State(ent.Name())); err == nil && !fn(ent.Name(), s) {
			return
		}
	}
}

func (e *Engine) passProject(ctx context.Context, id string, _ ...bool) {
	pools := e.refreshPools(ctx, id)
	codexProbe := e.probeCodexHome(ctx, id)
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := LoadState(e.Paths.State(id))
	if err != nil {
		return
	}
	if err := e.freshLocked(id, s); err != nil {
		e.logError("factory.trim_error", id, err)
	}
	if !s.Project.Started {
		delete(e.lastPass, id)
		return
	}
	e.flushMemAlertsLocked(id, &s)
	e.passPools = pools
	e.passPoolCheck = poolCheck(pools, e.Paths.poolCooldowns(), s.Settings.PoolFloorPercent, e.now())
	defer func() {
		e.passPools, e.passPoolCheck, e.passCoordFallback, e.passBaseHCErr = nil, nil, false, nil
		delete(e.storeENOSPC, id)
	}()
	ctx, cancel := context.WithTimeout(world.WithProcessTable(world.GitRepository(ctx, s.Project.Folder), time.Duration(s.Settings.StartMinutes)*time.Minute), world.GitPassTimeout)
	defer cancel()
	dt := e.passDelta(id)
	hcChecked, coordErr := e.observeCoordLocked(ctx, id, &s)
	planRun, prChecked, prErr := e.observePlanReviewLocked(ctx, id, &s, dt)
	hcChecked = hcChecked || prChecked
	hcErr := cmp.Or(coordErr, prErr)
	if hcErr != nil {
		e.logError("factory.hc_error", id, hcErr)
	}
	e.passCoordFallback = e.observeCoordSignInLocked(ctx, id, &s, e.now())
	e.observeBaseLocked(ctx, id, &s)
	if err := cmp.Or(e.passBaseHCErr, e.observeConsentDiskLocked(ctx, id, &s, e.now())); err != nil {
		hcErr, hcChecked = cmp.Or(hcErr, err), true
	}
	e.sweepProjectLocked(ctx, id, &s, codexProbe)
	e.checkENOSPCLocked(ctx, id, &s, e.now())
	if !e.cleanLocked(id) {
		return
	}
	baseTried := s.Project.Base != nil && s.Project.Base.Red && flow.Done(s.Project.Acts, flow.AskTellBaseRed+"@"+s.Project.Base.SpellID)
	workerRun, workerState, workerErr := map[string]flow.Run{}, map[string]flow.RunState{}, map[string]error{}
	for ti := range s.Tasks {
		t := &s.Tasks[ti]
		if a := flow.OpenAttempt(*t); a != nil && !flow.Ended(*t) && (a.RunID != "" || a.Run != nil && a.Run.State == flow.PhaseEnded) {
			wasWorking := a.Run != nil && a.Run.State == flow.PhaseWorking
			hcChecked = hcChecked || a.RunID != "" && (a.Run == nil || a.Run.State != flow.PhaseEnded)
			var prevHeld, prevRunHeld []string
			if !t.Parked && e.HC != nil && a.RunID != "" && len(a.Held) > 0 && a.Run != nil && a.Run.State != flow.PhaseEnded && !a.Run.HandoffSent {
				prevHeld, prevRunHeld = a.Held, a.Run.Held
				a.Run.Held, a.Held = appendHeld(slices.Clone(a.Run.Held), a.Held...), nil
			}
			r, rerr := e.advanceRunLocked(ctx, id, t.ID, &s, a.RunID, flow.RoleWorker, "claude", &a.Run)
			hcErr = cmp.Or(hcErr, rerr)
			save := false
			if rerr != nil && len(prevHeld) > 0 {
				a.Held, a.Run.Held = prevHeld, prevRunHeld
			} else if len(prevHeld) > 0 && len(a.Run.Held) > 0 {
				save = true
			}
			if dt > 0 && rerr == nil && wasWorking {
				a.Working += dt
				save = true
			}
			if a.Run != nil && a.Run.State == flow.PhaseEnded && (len(a.Run.Held) > 0 || a.RunID != "") {
				a.Held, a.Run.Held, a.RunID, a.RunTokenHash = appendHeld(a.Held, a.Run.Held...), nil, "", ""
				save = true
			}
			if save {
				e.saveLocked(id, s)
			}
			workerRun[t.ID], workerState[t.ID], workerErr[t.ID] = r, r.State, rerr
		}
	}
	if hcErr == nil && !hcChecked && (e.hcUnavail[id] || s.Project.HasAlert(flow.AlertHCUnavailable)) && e.HC != nil {
		_, hcErr = e.HC.Hello(ctx)
		hcChecked = true
	}
	if ctx.Err() == nil && (hcChecked || hcErr != nil) {
		unavail := hcErr != nil && world.FailureClass(hcErr) != flow.ClassSettled
		setFlag(&e.hcUnavail, id, unavail)
		e.reconcileAlertLocked(id, &s, flow.AlertHCUnavailable, unavail, flow.Alert{Kind: flow.AlertHCUnavailable, Cause: cmp.Or(flow.CleanErr(errText(hcErr)), "herdr-core is unreachable"), At: e.now()}, "alert", "")
	}
	obs, skipped := make([]*flow.Facts, len(s.Tasks)), map[string]bool{}
	for i := range s.Tasks {
		if f, ok := e.observeTaskLocked(ctx, id, &s, i, true, workerRun[s.Tasks[i].ID], workerErr[s.Tasks[i].ID], baseTried, dt); ctx.Err() == nil && ok {
			obs[i] = &f
		} else {
			skipped[s.Tasks[i].ID] = true
		}
	}
	if ctx.Err() == nil {
		now := e.now()
		var candidates []string
		for i, t := range s.Tasks {
			if a := flow.OpenAttempt(t); obs[i] == nil || a != nil && cmp.Or(workerState[t.ID], a.Run.TaskRun().State) != flow.RunGone {
				continue
			}
			obs[i].NoSlot = true
			if _, asks := e.matchTaskStartLocked(id, s, t, *obs[i], now); asks {
				if _, r := resolveWorkerReason(t, s.Settings, flow.ResolveFacts{Pool: e.passPoolCheck}); r.Why != "" {
					obs[i].NoSlot = false
				} else {
					candidates = append(candidates, t.ID)
				}
			}
		}
		running := flow.RunningTasks(s.Tasks, workerState)
		granted := flow.GrantSlots(candidates, s.Tasks, running, s.Settings.MaxRunning, s.Asks...)
		refused := slices.DeleteFunc(candidates, func(cid string) bool { return granted[cid] })
		obsFacts, ghUnavail := make(map[string]flow.Facts, len(obs)), e.ghUnavail[id] || s.Project.HasAlert(flow.AlertGitHubUnavailable)
		for i, t := range s.Tasks {
			if obs[i] != nil {
				if granted[t.ID] {
					obs[i].NoSlot = false
				}
				f := *obs[i]
				f.GitHubUnavailable = ghUnavail
				obsFacts[t.ID] = f
			}
		}
		yieldIDs := flow.YieldSlots(len(refused), running, len(granted), s.Settings.MaxRunning, s.Tasks, obsFacts, workerState, s.Asks, s.Settings, now)
		e.observeNoPoolLocked(ctx, id, &s, obs, now)
		ranTasks := false
		for i := range s.Tasks {
			if ctx.Err() == nil && obs[i] != nil {
				ranTasks = true
				e.actTaskLocked(ctx, id, &s, i, *obs[i], &baseTried)
				e.checkENOSPCLocked(ctx, id, &s, e.now())
			}
		}
		for _, tid := range yieldIDs {
			if ctx.Err() != nil || !e.cleanLocked(id) {
				break
			}
			ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == tid })
			if ti < 0 {
				continue
			}
			if a := flow.OpenAttempt(s.Tasks[ti]); a == nil || a.RunID == "" {
				continue
			} else if r, rerr := e.advanceRunLocked(ctx, id, tid, &s, a.RunID, flow.RoleWorker, "claude", &a.Run); rerr != nil {
				e.logError("factory.hc_error", id, rerr)
			} else if r.State != flow.RunIdle {
			} else if !e.cleanLocked(id) {
				break
			} else if err := e.stopRuns(context.WithoutCancel(ctx), []string{a.RunID}); err != nil {
				e.logError("factory.exec_error", id, err)
			} else {
				stopWorkerRec(a, s.Settings, e.now(), true)
				if !e.commitLogLocked(id, &s, LogLine{By: "engine", Task: tid, Rule: "slot", Decision: "stopped idle worker", Why: "waits on CI; " + strings.Join(refused, ", ") + " has no slot"}) {
					break
				}
			}
		}
		now = e.now()
		if ranTasks {
			e.emitOverrideResultsLocked(id, &s, now)
		}
		if coordErr == nil {
			e.passProjectRulesLocked(ctx, id, &s, now, planRun)
		}
		e.checkInvariantsLocked(id, &s, skipped, now)
	}
	e.checkENOSPCLocked(ctx, id, &s, e.now())
}

func (e *Engine) matchTaskStartLocked(id string, s flow.State, t flow.Task, f flow.Facts, now time.Time) (flow.Row[flow.In], bool) {
	f.NoSlot, f.GitHubUnavailable = false, e.ghUnavail[id] || s.Project.HasAlert(flow.AlertGitHubUnavailable)
	r, d := flow.FirstMatch(flow.TaskRows, flow.In{Task: t, Settings: s.Settings, Facts: f, Asks: s.Asks, Now: now})
	return r, slices.ContainsFunc(d.Asked, func(k string) bool {
		return k == flow.AskNewAttempt || strings.HasPrefix(k, flow.AskStartWorker)
	})
}

func (e *Engine) checkENOSPCLocked(ctx context.Context, id string, s *flow.State, now time.Time) {
	if gitFull := e.git != nil && e.git.TakeNoSpace(s.Project.Folder); !s.Project.HasAlert(flow.AlertDisk) && (e.storeENOSPC[id] || gitFull) {
		e.observeDiskLocked(ctx, id, s, now, gitFull)
	}
}

// checkInvariantsLocked checks I1–I5 after every pass: logs each broken
// invariant once per subject occurrence and wakes the coordinator once, without repairing.
func (e *Engine) checkInvariantsLocked(id string, s *flow.State, skipped map[string]bool, now time.Time) {
	byKey, allKeys, cleared := map[string][]string{}, map[string]struct{}{}, false
	vs := flow.CheckInvariants(flow.InvariantSnapshot{State: *s, Skipped: skipped, Now: now})
	for _, v := range vs {
		k := v.ID + "@" + v.Subject
		byKey[k], allKeys[k] = append(byKey[k], v.Why), struct{}{}
	}
	for k := range s.Project.Invariants {
		allKeys[k] = struct{}{}
	}
	for _, k := range slices.Sorted(maps.Keys(allKeys)) {
		rule, subj, _ := strings.Cut(k, "@")
		why, pfx := strings.Join(byKey[k], "; "), flow.AskWake+"@invariant@"+k+"@"
		if rule == "I1" && skipped[subj] && (why == "" || s.Project.Invariants[k] != "") {
			continue
		}
		if why == "" {
			s.Project.Acts = slices.DeleteFunc(s.Project.Acts, func(a flow.Act) bool { return strings.HasPrefix(a.Key, pfx) })
			delete(s.Project.Invariants, k)
			cleared = true
		} else if s.Project.Invariants[k] != why {
			if s.Project.Invariants == nil {
				s.Project.Invariants = map[string]string{}
			}
			s.Project.Invariants[k] = why
			s.Project.Acts = slices.DeleteFunc(s.Project.Acts, func(a flow.Act) bool { return strings.HasPrefix(a.Key, pfx) })
			wk := "invariant@" + k + "@" + tokenHash(why)[:8]
			ak := flow.AskWake + "@" + wk
			s.Project.Acts = append(s.Project.Acts, flow.Act{Key: ak, Result: "done", At: now})
			holdProjectWake(s, wk, rule+": "+why, "invariant "+rule+" broken: "+why, now)
			if err := e.commitLocked(id, s, LogLine{By: "engine", Rule: rule, Asked: []string{ak}, Decision: "error: invariant broken", Why: why}); err != nil {
				e.logError("factory.commit_error", id, err)
				return
			}
			cleared = false
			e.logEvent("factory.invariant_error", map[string]string{"project": id, "rule": rule, "error": why})
		}
	}
	if cleared {
		if len(s.Project.Invariants) == 0 {
			s.Project.Invariants = nil
		}
		e.saveLocked(id, *s)
	}
	if len(vs) > 0 && e.onInvariant != nil {
		e.onInvariant(vs)
	}
}

func clearCoordTrack(p *flow.Project, stucks, lastWake bool) {
	p.CoordErrorAt, p.CoordErrorTurn, p.CoordWakeAt = time.Time{}, 0, time.Time{}
	if len(p.CoordWakesInFlight) == 0 {
		p.CoordWakeTurn = 0
	}
	p.CoordDialogStucks, p.CoordLastWakeAt = map[bool]int{false: p.CoordDialogStucks}[stucks], map[bool]time.Time{false: p.CoordLastWakeAt}[lastWake]
}

func reconcileCoordWakes(p *flow.Project) bool {
	r := p.Run
	if len(p.CoordWakesInFlight) == 0 || r == nil || (r.State != flow.PhaseEnded && (r.State != flow.PhaseIdle || r.Turn <= p.CoordWakeTurn)) {
		return false
	}
	if r.State == flow.PhaseEnded && (r.Result == flow.EndQuota || r.Turn <= p.CoordWakeTurn) {
		for _, w := range p.CoordWakesInFlight {
			if !slices.ContainsFunc(p.Wakes, func(queued flow.Wake) bool { return queued.Task == w.Task && queued.Key == w.Key }) {
				p.Wakes = append(p.Wakes, w)
			}
		}
	}
	p.CoordWakesInFlight = nil
	return true
}

func (e *Engine) observeCoordLocked(ctx context.Context, id string, s *flow.State) (bool, error) {
	p, err, dirty := &s.Project, error(nil), false
	checked := e.HC != nil && (p.Run == nil || p.Run.State != flow.PhaseEnded) && (p.RunID == "" || p.Run == nil || p.Run.ID == "" || p.Run.Address != p.RunID)
	if checked {
		var terms []hc.Terminal
		terms, err = e.coordTerminalsLocked(ctx, id, p.Run)
		if err == nil {
			for _, t := range terms {
				if t.State != "live" || p.Run != nil && p.Run.ID != "" && p.Run.ID != t.Labels["sheepdog.run"] ||
					(p.Run == nil || p.Run.ID == "") && p.RunID != "" && p.RunID != t.Address {
					continue
				}
				if p.Run == nil {
					p.Run = &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", State: flow.PhaseStarting}
				}
				p.Run.Address, p.Run.ID, p.Run.CreatedMS = t.Address, t.Labels["sheepdog.run"], t.CreatedMS
				choice := terminalChoice(t, flow.Candidate{CLI: p.Run.CLI, Model: p.Run.Model})
				p.Run.CLI, p.Run.Model = choice.CLI, choice.Model
				p.Run.StartedAt = cmp.Or(p.Run.StartedAt, e.now())
				p.Run.LastActivity = cmp.Or(p.Run.LastActivity, p.Run.StartedAt)
				p.RunID, dirty = t.Address, true
				break
			}
			if p.Run == nil && p.RunID != "" || p.Run != nil && p.Run.ID == "" {
				p.Run = &flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude", State: flow.PhaseStarting}
				p.RunID, dirty = "", true
			}
		}
	}
	if err == nil {
		if p.Run != nil && p.Run.State != flow.PhaseEnded {
			checked = checked || p.Run.Address != ""
			if p.Run.ID != "" && p.RunID != p.Run.Address {
				p.RunID, dirty = p.Run.Address, true
			}
		}
		_, err = e.advanceRunLocked(ctx, id, "", s, "", flow.RoleCoordinator, "claude", &p.Run)
	}
	if p.NewGoal && len(s.Tasks) > p.NewGoalTasks && slices.ContainsFunc(s.Tasks[p.NewGoalTasks:], func(t flow.Task) bool { return t.SplitFrom == "" }) {
		p.NewGoal, p.NewGoalTasks, dirty = false, 0, true
	}
	if reconcileCoordWakes(p) {
		dirty = true
	}
	if r := p.Run; r != nil && r.State == flow.PhaseEnded && (p.RunID != "" || p.RunTokenHash != "" || !p.CoordWakeAt.IsZero() || !p.CoordErrorAt.IsZero() || (!flow.IsCoordR29(r) && p.CoordDialogStucks != 0) || (r.Result != flow.EndStopped && p.CoordSilentStops != 0)) {
		p.RunID, p.RunTokenHash, p.CoordSilentStops, dirty = "", "", map[bool]int{true: p.CoordSilentStops}[r.Result == flow.EndStopped], true
		clearCoordTrack(p, !flow.IsCoordR29(r), false)
	} else if r != nil && r.State != flow.PhaseEnded && !p.CoordWakeAt.IsZero() && (r.Turn > p.CoordWakeTurn || r.CLI != world.CodexCLI && r.Status == "working") {
		p.CoordWakeAt, p.CoordSilentStops, dirty = time.Time{}, map[bool]int{false: p.CoordSilentStops}[r.Turn > p.CoordWakeTurn], true
	}
	if dirty {
		e.saveLocked(id, *s)
	}
	return checked, err
}

// passProjectRulesLocked evaluates flow.ProjectRows and executes the matched coordinator action.
func (e *Engine) passProjectRulesLocked(ctx context.Context, id string, s *flow.State, now time.Time, planRun ...flow.Run) {
	if !e.cleanLocked(id) {
		return
	}
	prReady, prWhy := false, ""
	if n := len(s.Project.PlanReviews); (n == 0 || !s.Project.PlanReviews[n-1].Accepted.IsZero()) && slices.ContainsFunc(s.Tasks, func(t flow.Task) bool { return !flow.Ended(t) && t.Unreviewed }) {
		prReady, prWhy = e.planReviewerReady(ctx, *s)
	}
	match := func(rows []flow.Row[flow.ProjectIn]) (flow.Row[flow.ProjectIn], flow.Decision) {
		return flow.FirstMatch(rows, flow.ProjectIn{Project: s.Project, Tasks: s.Tasks, Settings: s.Settings, Now: now, CoordFallbackReady: e.passCoordFallback, PlanReviewerReady: prReady, PlanRun: append(planRun, flow.Run{})[0]})
	}
	if r, d := match(flow.CoordRows); len(d.Asked) > 0 && d.Asked[0] == flow.AskCoordDialogStuck {
		s.Project.CoordDialogStucks++
		s.Project.Acts = append(s.Project.Acts, flow.Act{Key: flow.CoordStuckKey(s.Project.Run), Result: "done", At: now})
		if s.Project.CoordDialogStucks >= 2 {
			e.logEvent("factory.coordinator_stuck", map[string]string{"project": id, "count": fmt.Sprint(s.Project.CoordDialogStucks), "why": d.Why})
			e.reconcileAlertLocked(id, s, flow.AlertAgentsDown+"@j10", true, flow.Alert{Kind: flow.AlertAgentsDown, Cause: d.Why, At: now, Rule: "J10", Scope: "j10_stuck"}, "alert", "")
			if !e.cleanLocked(id) {
				return
			}
		}
		e.commitLogLocked(id, s, LogLine{By: "engine", Rule: r.ID, Asked: d.Asked, Decision: map[bool]string{true: "coordinator stuck at dialog twice in a row; owner attention needed", false: fmt.Sprintf("coordinator stuck at dialog (count %d)", s.Project.CoordDialogStucks)}[s.Project.CoordDialogStucks >= 2], Why: d.Why})
		if !e.cleanLocked(id) {
			return
		}
	}
	r, d := match(flow.ProjectRows)
	if r.ID == "J4" && len(flow.OpenPlanFindings(s.Project)) == 0 {
		if n := len(s.Project.PlanReviews); n > 0 && s.Project.PlanReviews[n-1].Verdict != "" && s.Project.PlanReviews[n-1].Accepted.IsZero() {
			e.acceptPlanLocked(id, s, "J16", []string{flow.AskAcceptPlan}, "accept held plan", now)
			if !e.cleanLocked(id) {
				return
			}
		}
	}
	if r.ID == "J6" || (r.ID == "J4" && !flow.RunLive(s.Project.RunID, s.Project.Run)) {
		if capWhy, capped := flow.CoordHourlyCap(s.Project, s.Settings, now); r.ID == "J6" && capped {
			e.reconcileAlertLocked(id, s, flow.AlertAgentsDown+"@j6", true, flow.Alert{Kind: flow.AlertAgentsDown, Cause: capWhy, At: now, Rule: "J6", Scope: "j6_cap"}, "alert", "")
			if !e.cleanLocked(id) {
				return
			}
		}
		key, finishing := r.ID+":"+d.Why, r.ID == "J4" && !s.Project.Finished
		var cancelled []flow.Ask
		if finishing {
			s.Project.Finished = true
			for ai, a := range s.Asks {
				if a.Status == flow.AskStatusOpen {
					s.Asks[ai], _, _ = flow.StepAsk(a, flow.AskEvent{Kind: flow.EvAskCancel}, now)
					cancelled = append(cancelled, s.Asks[ai])
				}
			}
		}
		saved := false
		if s.Project.WaitKey != key {
			saved = e.logProjectWaitLocked(id, s, r.ID, cmp.Or(map[string]string{"J4": "finished"}[r.ID], "waiting"), d.Why, key)
		} else if finishing {
			saved = e.saveLocked(id, *s)
		}
		if saved {
			for _, a := range cancelled {
				e.notifyChangeLocked(id, s.Seq, Change{Kind: flow.EvAskCancel, AskID: a.ID, Task: a.Task})
			}
		}
		return
	}
	if r.ID != "J7" && s.Project.WaitKey != "" {
		s.Project.WaitKey = ""
		if len(d.Asked) == 0 {
			e.saveLocked(id, *s)
		}
	}
	if len(d.Asked) == 0 {
		return
	}
	commit := func(dec string) {
		e.commitLogLocked(id, s, LogLine{By: "engine", Rule: r.ID, Asked: d.Asked, Decision: dec, Why: d.Why})
	}
	switch ask := d.Asked[0]; ask {
	case flow.AskStartCoordinator:
		e.startCoordLocked(ctx, id, s, d.Why, now)
	case flow.AskStopCoordinator:
		if !e.stopCoordWithBackoffLocked(ctx, id, s, now, false) {
			return
		}
		stopRec(&s.Project.Run, flow.RoleCoordinator, s.Settings, now)
		reconcileCoordWakes(&s.Project)
		s.Project.RunID, s.Project.RunTokenHash = "", ""
		clearCoordTrack(&s.Project, true, false)
		if s.Project.CoordSilentStops = map[bool]int{true: s.Project.CoordSilentStops + 1}[r.ID == "J9"]; s.Project.CoordSilentStops >= 2 {
			e.reconcileAlertLocked(id, s, flow.AlertAgentsDown+"@j9", true, flow.Alert{Kind: flow.AlertAgentsDown, Cause: d.Why, At: now, Rule: "J9", Scope: "j9_silent"}, "alert", "")
			if !e.cleanLocked(id) {
				return
			}
		}
		commit("stopped coordinator")
	case flow.AskDeliverWakes:
		e.deliverWakesLocked(ctx, id, s, now)
	case flow.AskStartPlanReviewer:
		e.startPlanReviewLocked(ctx, id, s, d.Asked, now)
	case flow.AskStopPlanReviewer:
		pr := s.Project.PlanReviews[len(s.Project.PlanReviews)-1]
		if err := e.stopRuns(ctx, []string{cmp.Or(pr.Run.RunID, planReviewerName(id, pr.N))}); err != nil {
			e.logError("factory.hc_error", id, err)
			return
		}
		if pr.Verdict != "" {
			next := *s
			next.Project.PlanReviews = slices.Clone(s.Project.PlanReviews)
			rev := *pr.Run
			stopReviewRec(&rev, next.Settings, now)
			next.Project.PlanReviews[len(next.Project.PlanReviews)-1].Run = &rev
			if e.commitLogLocked(id, &next, LogLine{By: "engine", Rule: r.ID, Asked: d.Asked, Decision: "stopped plan reviewer", Why: d.Why}) {
				*s = next
			}
			return
		}
		e.acceptPlanLocked(id, s, r.ID, d.Asked, d.Why, now)
	case flow.AskAcceptPlan:
		e.acceptPlanLocked(id, s, r.ID, d.Asked, cmp.Or(prWhy, d.Why), now)
	default:
		msg := map[string]string{flow.AskFinalJournal: "All tasks in this plan have ended. Write one final journal line summarizing what landed and any open risks.", flow.AskCoordGoOn: "Your last turn ended with an error. Check your state and go on with the work.", flow.AskHeartbeat: coordSummary(*s, "", true)}[ask]
		if e.sendCoordLocked(ctx, id, s, msg, now, ask != flow.AskFinalJournal) == nil {
			switch ask {
			case flow.AskFinalJournal:
				s.Project.Acts = append(s.Project.Acts, flow.Act{Key: flow.FinalJournalKey(s.Tasks), Result: "done", At: now})
			case flow.AskCoordGoOn:
				s.Project.CoordErrorAt, s.Project.CoordErrorTurn = now, s.Project.Run.Turn
			case flow.AskHeartbeat:
				s.Project.CoordLastWakeAt = now
			}
			commit(map[string]string{flow.AskFinalJournal: "asked coordinator for final journal line", flow.AskCoordGoOn: "told coordinator to go on", flow.AskHeartbeat: "sent heartbeat wake to coordinator"}[ask])
		}
	}
}

func (e *Engine) logProjectWaitLocked(id string, s *flow.State, row, decision, why, key string) bool {
	if s.Project.WaitKey == key {
		return false
	}
	s.Project.WaitKey = key
	return e.commitLogLocked(id, s, LogLine{By: "engine", Rule: row, Decision: decision, Why: why})
}

func (e *Engine) planReviewerReady(ctx context.Context, s flow.State) (bool, string) {
	if s.Settings.PlanReviewRounds <= 0 {
		return false, "plan_review_rounds is 0"
	}
	f := e.probeLaunchFacts(ctx, flow.RolePlanReviewer, flow.Task{}, s.Settings)
	f.Check = launchCapability(flow.RolePlanReviewer)
	_, r := flow.ResolveWithReason(flow.RolePlanReviewer, flow.Task{}, s.Settings, f)
	return r.Why == "", r.Why
}

func (e *Engine) acceptPlanLocked(id string, s *flow.State, rule string, asked []string, why string, now time.Time) {
	next, dec := *s, "plan accepted"
	next.Tasks, next.Project.PlanReviews = slices.Clone(s.Tasks), slices.Clone(s.Project.PlanReviews)
	if n := len(next.Project.PlanReviews); n > 0 && next.Project.PlanReviews[n-1].Accepted.IsZero() {
		pr := &next.Project.PlanReviews[n-1]
		if pr.Run != nil {
			rev := *pr.Run
			stopReviewRec(&rev, next.Settings, now)
			pr.Run = &rev
		}
		if pr.Verdict == "" {
			why, dec = cmp.Or(why, "plan reviewer unavailable"), "plan accepted unreviewed (unavailable)"
			pr.Verdict, pr.Why, pr.At = flow.VerdictUnavailable, why, now
		} else if len(pr.Findings) > 0 {
			pr.Findings = slices.Clone(pr.Findings)
			counts := map[string]int{}
			for i := range pr.Findings {
				if pr.Findings[i].Closed == "" {
					pr.Findings[i].Closed = flow.PlanClosedUnanswered
				}
				counts[pr.Findings[i].Closed]++
			}
			why = fmt.Sprintf("findings: %d dropped, %d amended, %d rejected", counts[flow.PlanClosedDropped], counts[flow.PlanClosedAmended], counts[flow.PlanClosedRejected])
			if n := counts[flow.PlanClosedUnanswered]; n > 0 {
				why += fmt.Sprintf(", %d unanswered", n)
			}
		}
		pr.Accepted = now
		for i := range next.Tasks {
			next.Tasks[i].Unreviewed = next.Tasks[i].Unreviewed && !slices.Contains(pr.Tasks, next.Tasks[i].ID)
		}
	} else {
		why, dec = cmp.Or(why, "plan reviewer unavailable"), "plan accepted unreviewed (unavailable)"
		var tasks []string
		for i, t := range next.Tasks {
			if t.Unreviewed && !flow.Ended(t) {
				tasks = append(tasks, t.ID)
			}
			next.Tasks[i].Unreviewed = false
		}
		next.Project.PlanReviews = append(next.Project.PlanReviews, flow.PlanReview{N: len(next.Project.PlanReviews) + 1, Tasks: tasks, Verdict: flow.VerdictUnavailable, Why: why, At: now, Accepted: now})
	}
	if e.commitLogLocked(id, &next, LogLine{By: "engine", Rule: rule, Asked: asked, Decision: dec, Why: why}) {
		*s = next
	}
}

func (e *Engine) observePlanReviewLocked(ctx context.Context, id string, s *flow.State, dt time.Duration) (flow.Run, bool, error) {
	n := len(s.Project.PlanReviews)
	if n == 0 || !s.Project.PlanReviews[n-1].Accepted.IsZero() || s.Project.PlanReviews[n-1].Verdict != "" || s.Project.PlanReviews[n-1].Run == nil {
		return flow.Run{}, false, nil
	}
	rev := s.Project.PlanReviews[n-1].Run
	wasLive := rev.Run != nil && rev.Run.State != "" && rev.Run.State != flow.PhaseEnded
	checked := e.HC != nil && rev.RunID != "" && (rev.Run == nil || rev.Run.State != flow.PhaseEnded)
	r, err := e.advanceRunLocked(ctx, id, "", s, rev.RunID, flow.RolePlanReviewer, cmp.Or(rev.Reviewer.CLI, "claude"), &rev.Run)
	if err != nil {
		return flow.Run{}, checked, err
	}
	if dt > 0 && wasLive {
		rev.Age += dt
		e.saveLocked(id, *s)
	}
	r.State = cmp.Or(r.State, flow.RunGone)
	return r, checked, nil
}

func (e *Engine) startPlanReviewLocked(ctx context.Context, id string, s *flow.State, asked []string, now time.Time) {
	var avoid []string
	if s.Project.Run != nil && s.Project.Run.Model != "" {
		avoid = []string{cmp.Or(s.Project.Run.CLI, "claude") + "/" + s.Project.Run.Model}
	}
	in, env, reason := e.resolveLaunch(ctx, id, flow.RolePlanReviewer, flow.Task{}, *s, launchCapability(flow.RolePlanReviewer), func(f flow.ResolveFacts) (flow.Intent, flow.ResolveReason) {
		in, _, r := reviewChoiceReason(flow.RolePlanReviewer, flow.Task{}, s.Settings, flow.Attempt{}, "", f, avoid)
		return in, r
	})
	if reason.Why != "" {
		e.acceptPlanLocked(id, s, "J16", asked, reason.Why, now)
		return
	}
	choice, bin, n, code := intentCandidate(in), cmp.Or(e.Bin, "sheepdog"), len(s.Project.PlanReviews)+1, strings.ToLower(rand.Text()[:10])
	var tasks []string
	for _, t := range s.Tasks {
		if t.Unreviewed && !flow.Ended(t) {
			tasks = append(tasks, t.ID)
		}
	}
	draftPath := e.reviewDraftPath(id, "plan", n, code)
	if draftPath != "" && guardTestPath(draftPath) == nil {
		_ = os.MkdirAll(filepath.Dir(draftPath), 0o700)
	}
	argv := launchArgv(flow.RolePlanReviewer, choice, s.Project.Folder, "", false)
	if choice.CLI == "claude" {
		argv = world.ClaudePlanReviewerArgv(choice.Model, choice.Effort, bin, draftPath)
	}
	prompt := planReviewPrompt(planReviewerTemplate, *s, code, bin, draftPath)
	if choice.CLI == world.AgyCLI {
		prompt = "Do not modify, create or delete any files; read and run tests only (except writing findings to " + draftPath + ").\n\n" + prompt
	}
	s.Project.PlanReviews = append(s.Project.PlanReviews, flow.PlanReview{
		N: n, Tasks: tasks, Reviewer: flow.Reviewer(choice), Started: now,
		Run: &flow.ReviewRun{Round: n, Kind: flow.RoundPlan, Code: code, Reviewer: flow.Reviewer(choice)},
	})
	pr := &s.Project.PlanReviews[n-1]
	termID, _, abort, err := e.launchRunLocked(ctx, id, s, &pr.Run.RunTokenHash, &pr.Run.Run, false, flow.RunRecord{Role: flow.RolePlanReviewer, CLI: choice.CLI}, world.RunSpec{
		Name: planReviewerName(id, n), Cwd: s.Project.Folder, ClaudeConfig: e.Paths.ClaudeConfig, Argv: argv, Prompt: prompt, Env: env,
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RolePlanReviewer, "sheepdog.model": choice.Model, "sheepdog.effort": choice.Effort},
	}, hc.Terminal{})
	if err != nil {
		s.Project.PlanReviews = s.Project.PlanReviews[:n-1]
		if !abort {
			e.acceptPlanLocked(id, s, "J16", asked, err.Error(), now)
		}
		return
	}
	pr.Run.RunID = termID
	e.commitLogLocked(id, s, LogLine{By: "engine", Rule: "J16", Asked: []string{fmt.Sprintf("%s@%d", flow.AskStartPlanReviewer, n)}, Decision: fmt.Sprintf("started %s %s (round %d, %s/%s)", flow.RolePlanReviewer, termID, n, choice.CLI, choice.Model), Why: "unreviewed tasks waiting"})
}

func planReviewPrompt(tmpl string, s flow.State, code, bin, findingsPath string) string {
	var under, accepted []string
	for _, t := range s.Tasks {
		if flow.Ended(t) {
			continue
		}
		if t.Unreviewed {
			under = append(under, fmt.Sprintf("- %s %s (for_whom: %s; if_skipped: %s; smallest: %s; not_doing: %s; estimate: %d)\n  brief: %s\n  outcomes:\n%s",
				t.ID, t.Title, t.ForWhom, t.IfSkipped, t.Smallest, t.NotDoing, t.EstimateLines, cmp.Or(strings.TrimSpace(t.Brief), "(none)"), bullets(t.Outcomes)))
		} else {
			accepted = append(accepted, "- "+t.ID+" "+t.Title)
		}
	}
	facts := fmt.Sprintf("Goal: %s\nTasks under review:\n%s\nAccepted tasks:\n%s",
		s.Project.Goal, cmp.Or(strings.Join(under, "\n"), "(none)"), cmp.Or(strings.Join(accepted, "\n"), "(none)"))
	return fill(tmpl, "{facts}", facts, "{findings_path}", findingsPath, "{sheepdog}", bin, "{code}", code)
}

type launchWait struct{ Why, Key string }

func (w launchWait) Error() string { return w.Why }

func launchWaitFor(r flow.ResolveReason) launchWait {
	if r.Reset.IsZero() {
		return launchWait{Why: r.Why, Key: cmp.Or(map[bool]string{true: "quota"}[r.Kind == flow.ReasonPool], r.Why)}
	}
	ts := r.Reset.UTC().Format(time.RFC3339)
	if r.Kind == flow.ReasonPool {
		return launchWait{Why: r.Why, Key: "quota@" + ts}
	}
	_, rest, _ := strings.Cut(r.Why, "; ")
	return launchWait{Why: r.Why, Key: "mixed@" + ts + "@" + rest}
}

// cliProbeTTL lets launches share login checks while picking up sign-in changes promptly.
const cliProbeTTL = 2 * time.Minute

type cachedCLIProbe struct {
	at    time.Time
	facts flow.CLIFacts
	known bool
}

// probeLaunchFacts is called under e.mu by launch executors.
func (e *Engine) probeLaunchFacts(ctx context.Context, role string, task flow.Task, settings flow.Settings) flow.ResolveFacts {
	now, ttl := e.now(), cliProbeTTL
	if p := time.Duration(settings.PoolMinutes) * time.Minute; p > 0 && p < ttl {
		ttl = p
	}
	var clis, stale []string
	for _, c := range flow.Candidates(role, task, settings) {
		if len(cliProbeArgv[c.CLI]) == 0 || slices.Contains(clis, c.CLI) {
			continue
		}
		clis = append(clis, c.CLI)
		if probe, ok := e.cliProbes[c.CLI]; !ok || now.Before(probe.at) || now.Sub(probe.at) >= ttl {
			stale = append(stale, c.CLI)
		}
	}
	if len(stale) > 0 {
		var probed flow.ResolveFacts
		switch {
		case e.probeLaunch != nil:
			probed = e.probeLaunch(ctx, stale)
		case e.HC != nil && e.HC.Socket != "":
			probed = probeResolveFacts(ctx, stale, hcExecRunner(e.HC.Socket))
		}
		if e.cliProbes == nil {
			e.cliProbes = make(map[string]cachedCLIProbe)
		}
		probedAt := e.now()
		for _, cli := range stale {
			facts, known := probed.CLIs[cli]
			e.cliProbes[cli] = cachedCLIProbe{at: probedAt, facts: facts, known: known}
		}
	}
	facts := flow.ResolveFacts{CLIs: make(map[string]flow.CLIFacts, len(clis)), Pool: e.passPoolCheck}
	for _, cli := range clis {
		if probe := e.cliProbes[cli]; probe.known {
			facts.CLIs[cli] = probe.facts
		}
	}
	return facts
}

func launchCapability(role string) func(flow.Candidate) flow.ResolveReason {
	return func(c flow.Candidate) flow.ResolveReason {
		switch role {
		case flow.RoleWorker, flow.RoleCoordinator, flow.RoleReviewer:
			if c.CLI == "claude" || c.CLI == world.CodexCLI || c.CLI == world.AgyCLI {
				return flow.ResolveReason{}
			}
		case flow.RoleRetro, flow.RolePlanReviewer:
			if c.CLI == "claude" || c.CLI == world.AgyCLI {
				return flow.ResolveReason{}
			}
		}
		return flow.ResolveReason{Why: fmt.Sprintf("the engine cannot launch %s as %s yet", c.CLI, role)}
	}
}

func intentCandidate(in flow.Intent) flow.Candidate {
	return flow.Candidate{CLI: in.CLI, Model: in.Model, Effort: in.Effort}
}

func resolveWorker(t flow.Task, s flow.Settings, facts flow.ResolveFacts) (flow.Intent, string) {
	in, r := resolveWorkerReason(t, s, facts)
	return in, r.Why
}

func resolveWorkerReason(t flow.Task, s flow.Settings, facts flow.ResolveFacts) (flow.Intent, flow.ResolveReason) {
	check := facts.Check
	facts.Check = func(c flow.Candidate) flow.ResolveReason {
		if r := launchCapability(flow.RoleWorker)(c); r.Why != "" {
			return r
		}
		_, r := flow.ResolveWithReason(flow.RoleReviewer, t, s, flow.ResolveFacts{Worker: c, Check: launchCapability(flow.RoleReviewer)})
		if r.Why == "" && check != nil {
			return check(c)
		}
		return r
	}
	return flow.ResolveWithReason(flow.RoleWorker, t, s, facts)
}

func launchArgv(role string, c flow.Candidate, cwd, session string, resume bool) []string {
	switch c.CLI {
	case world.CodexCLI:
		switch role {
		case flow.RoleReviewer:
			return world.CodexArgv(c.Model, c.Effort, cwd)
		case flow.RoleWorker, flow.RoleCoordinator:
			if !resume {
				session = ""
			}
			return world.CodexWorkArgv(c.Model, c.Effort, cwd, session)
		}
	case world.AgyCLI:
		switch role {
		case flow.RoleReviewer, flow.RoleRetro, flow.RolePlanReviewer:
			return world.AgyArgv(c.Model, "", "")
		case flow.RoleWorker, flow.RoleCoordinator:
			if !resume {
				session = ""
			}
			return world.AgyArgv(c.Model, session, "")
		}
	case "claude":
		return world.ClaudeSessionArgv(role, c.Model, c.Effort, "bypassPermissions", session, resume)
	}
	return nil
}

func (e *Engine) codexHome(id string) (string, error) {
	dir := e.Paths.CodexHome()
	home, err := world.CodexHome(dir)
	if err == nil {
		e.logOnce("factory.codex_home_error", id, dir, nil)
		return home, nil
	}
	if errors.Is(err, world.ErrCodexAuthDiverged) {
		e.logOnce("factory.codex_auth_diverged", id, dir, errors.New("scoped Codex sign-in was refreshed. Check codex login and move the scoped auth.json to re-enable it"))
	} else {
		e.logOnce("factory.codex_home_error", id, dir, err)
	}
	return "", err
}

func (e *Engine) resolveLaunch(ctx context.Context, id, role string, task flow.Task, s flow.State, check func(flow.Candidate) flow.ResolveReason, resolve func(flow.ResolveFacts) (flow.Intent, flow.ResolveReason)) (flow.Intent, map[string]string, flow.ResolveReason) {
	facts := e.probeLaunchFacts(ctx, role, task, s.Settings)
	if role == flow.RoleCoordinator && s.Project.CoordSignOutHold() {
		check = e.positiveCoordCheck(id, facts.CLIs)
	}
	facts.Check = check
	for {
		in, reason := resolve(facts)
		if reason.Why != "" {
			return in, nil, reason
		} else if stop := flow.StoppingAlert(s.Project.Alerts); stop != nil {
			return flow.Intent{}, nil, flow.ResolveReason{Why: "stopped: " + cmp.Or(stop.Fix, stop.Cause)}
		} else if in.CLI != world.CodexCLI {
			return in, nil, reason
		}
		home, err := e.codexHome(id)
		if err == nil {
			return in, map[string]string{"CODEX_HOME": home}, flow.ResolveReason{}
		}
		facts.Check = func(c flow.Candidate) flow.ResolveReason {
			if c.CLI == world.CodexCLI {
				return flow.ResolveReason{Why: err.Error()}
			}
			if check != nil {
				return check(c)
			}
			return flow.ResolveReason{}
		}
	}
}

func (e *Engine) startCoordLocked(ctx context.Context, id string, s *flow.State, why string, now time.Time) {
	in, env, wait := e.resolveLaunch(ctx, id, flow.RoleCoordinator, flow.Task{}, *s, launchCapability(flow.RoleCoordinator), func(f flow.ResolveFacts) (flow.Intent, flow.ResolveReason) {
		return flow.ResolveWithReason(flow.RoleCoordinator, flow.Task{}, s.Settings, f)
	})
	if wait.Why != "" {
		e.logProjectWaitLocked(id, s, "J7", "waiting", wait.Why, "J7:"+launchWaitFor(wait).Key)
		return
	}
	adopt, bin := s.Project.Run == nil || s.Project.Run.State != flow.PhaseEnded, cmp.Or(e.Bin, "sheepdog")
	if !adopt {
		stopped := e.stopCoordWithBackoffLocked(ctx, id, s, now, true)
		r, d := flow.FirstMatch(flow.CoordRows, flow.ProjectIn{Project: s.Project, Tasks: s.Tasks, Settings: s.Settings, Now: now, CoordFallbackReady: e.passCoordFallback})
		if !stopped {
			e.logProjectWaitLocked(id, s, r.ID, "waiting", d.Why, r.ID+":"+d.Why)
			return
		}
		why = d.Why
	}
	s.Project.WaitKey = ""
	termID, _, abort, err := e.launchRunLocked(ctx, id, s, &s.Project.RunTokenHash, &s.Project.Run, adopt, flow.RunRecord{Role: flow.RoleCoordinator, CLI: in.CLI}, world.RunSpec{
		Name: coordName(id), Cwd: s.Project.Folder, ClaudeConfig: e.Paths.ClaudeConfig, Env: env,
		Argv:   launchArgv(flow.RoleCoordinator, intentCandidate(in), s.Project.Folder, "", false),
		Prompt: map[bool]string{true: coordinatorPrompt(coordinatorTemplate, s.Project, bin), false: coordSummary(*s, bin, false)}[len(s.Tasks) == 0],
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.model": in.Model, "sheepdog.effort": in.Effort},
	}, hc.Terminal{})
	if abort {
		return
	}
	line := LogLine{By: "engine", Facts: map[string]any{"coordinator": flow.RunGone}, Rule: "J7", Asked: []string{flow.AskStartCoordinator}}
	if err != nil {
		e.logError("factory.exec_error", id, err)
		s.Project.Acts, line.Why = append(s.Project.Acts, flow.Act{Key: flow.AskStartCoordinator, Result: "failed", At: now}), err.Error()
	} else {
		s.Project.RunID, s.Project.Acts = termID, append(s.Project.Acts, flow.Act{Key: flow.AskStartCoordinator, Result: "done", At: now})
		clearCoordTrack(&s.Project, false, true)
		line.Asked, line.Decision, line.Why = []string{"coordinator:" + termID}, "started coordinator "+in.CLI+"/"+in.Model, why
	}
	e.commitLogLocked(id, s, line)
}

func (e *Engine) coordTerminalsLocked(ctx context.Context, id string, rec *flow.RunRecord) ([]hc.Terminal, error) {
	list, err := e.HC.List(ctx, true)
	if err != nil {
		return nil, err
	}
	var owned []hc.Terminal
	for _, t := range list.Terminals {
		if hc.IsRemote(t.Address) {
			continue
		}
		label := t.Labels["sheepdog.run"]
		known := rec != nil && rec.ID != "" && label == rec.ID
		if known && rec.CreatedMS != 0 && rec.CreatedMS != t.CreatedMS {
			continue
		}
		if label != "" && (known || t.Labels["sheepdog.project"] == id && t.Labels["sheepdog.role"] == flow.RoleCoordinator) {
			owned = append(owned, t)
		} else if t.Name == coordName(id) {
			e.logOnce("factory.coordinator_name_collision", id, t.Address, fmt.Errorf("leaving terminal %s without this project's coordinator run label alone", t.Address))
		}
	}
	return owned, nil
}

// stopCoordWithBackoffLocked shares the stop budget. J7 keeps retrying so a
// replacement coordinator can deliver the wake.
func (e *Engine) stopCoordWithBackoffLocked(ctx context.Context, id string, s *flow.State, now time.Time, restarting bool) bool {
	const key = flow.AskStopCoordinator
	p := &s.Project
	exhausted := flow.Exhausted(p.Acts, key, s.Settings)
	if exhausted && !restarting {
		return false
	}
	if last, tries := flow.ActRetry(p.Acts, key); tries > 0 && now.Sub(last.At) < flow.EnsureBackoff(s.Settings, tries) {
		return false
	}
	if err := e.stopCoordTerminalsLocked(ctx, id, *s); err != nil {
		e.logError("factory.hc_error", id, err)
		words, cls := flow.CleanErr(err.Error()), world.FailureClass(err)
		uncounted := cls != flow.ClassSettled && actOutage(key, flow.Facts{HCUnavailable: e.hcUnavail[id] || s.Project.HasAlert(flow.AlertHCUnavailable)}, err)
		p.Acts = append(p.Acts, flow.Act{Key: key, Result: "failed", At: now, Class: cls, Err: words, Uncounted: uncounted})
		if !exhausted && flow.Exhausted(p.Acts, key, s.Settings) {
			why := map[bool]string{true: "could not stop the old coordinator", false: "gave up stopping the coordinator"}[restarting]
			holdProjectWake(s, "failed@"+key, why, words, now)
		}
		e.saveLocked(id, *s)
		return false
	}
	p.Acts = slices.DeleteFunc(p.Acts, func(a flow.Act) bool { return a.Key == key })
	return true
}

// stopCoordTerminalsLocked stops labelled coordinator terminals and their process groups.
func (e *Engine) stopCoordTerminalsLocked(ctx context.Context, id string, s flow.State) error {
	if e.HC == nil {
		return nil
	}
	terms, err := e.coordTerminalsLocked(ctx, id, s.Project.Run)
	if err != nil {
		return err
	}
	for _, t := range terms {
		if err := world.StopLabeledRun(ctx, e.HC, t.Address, t.Labels["sheepdog.run"], t.CreatedMS); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) sendCoordLocked(ctx context.Context, id string, s *flow.State, msg string, now time.Time, armWake bool) error {
	if !e.cleanLocked(id) {
		return errStoreDirty
	}
	if err := world.SendRun(ctx, e.HC, s.Project.RunID, msg); err != nil {
		e.logError("factory.exec_error", id, err)
		return err
	}
	if s.Project.Run != nil {
		if armWake && s.Project.CoordWakeAt.IsZero() {
			s.Project.CoordWakeAt, s.Project.CoordWakeTurn = now, s.Project.Run.Turn
		}
		next, _, _ := flow.StepRun(*s.Project.Run, flow.RunEvent{Kind: flow.EvDeliver, Msg: msg}, s.Settings, now)
		*s.Project.Run = next
	}
	return nil
}

// advanceRunLocked observes a run in hc, steps its RunRecord through flow.RunRows, executes any run acts and saves state changes.
func (e *Engine) advanceRunLocked(ctx context.Context, id, taskID string, s *flow.State, runID, role, cli string, recp **flow.RunRecord) (flow.Run, error) {
	if runID == "" && *recp == nil {
		return flow.Run{}, nil
	}
	now, fresh := e.now(), *recp == nil || (runID != "" && (*recp).Address != runID)
	if fresh {
		*recp = &flow.RunRecord{Address: runID, Role: role, CLI: cli, State: flow.PhaseStarting, StartedAt: now, LastActivity: now}
	}
	if (*recp).State == flow.PhaseEnded || e.HC == nil {
		return (*recp).TaskRun(), nil
	}
	evs, err := world.ObserveRun(ctx, e.HC, **recp, e.Paths.CodexHome())
	if err != nil {
		return flow.Run{}, err
	}
	before := **recp
	var handoffPools *[]string
	if i := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return role == flow.RoleWorker && taskID != "" && t.ID == taskID }); i >= 0 {
		if a := flow.OpenAttempt(s.Tasks[i]); a != nil {
			handoffPools = &a.HandoffPools
		}
	}
	if before.StopRetry != nil && evs[0].Kind != flow.EvLost {
		evs = []flow.RunEvent{{Kind: flow.EvStop}}
	} else if n := len(evs); n > 0 && evs[n-1].Kind == flow.EvTick {
		cand := flow.Candidate{CLI: before.CLI, Model: before.Model}
		if p := poolProvider(cand); p != "" && poolWaitReason(p, poolFamily(cand), e.passPools[p], s.Settings.PoolFloorPercent, now).Kind == flow.ReasonPool || before.HandoffHeld {
			why := fmt.Sprintf("the %s pool reached its floor", cmp.Or(p, before.CLI))
			evs = slices.Insert(evs, n-1, flow.RunEvent{Kind: flow.EvPoolFloor, Screen: evs[n-1].Screen, Msg: "The " + why[4:] + ". Write your handoff and stop.", Reason: why, HandoffUsed: handoffPools != nil && slices.Contains(*handoffPools, poolCooldownKey(cand))})
		}
	}
events:
	for _, ev := range evs {
		old := **recp
		next, row, dec := flow.StepRun(old, ev, s.Settings, now)
		if row.ID == "R3" {
			old.Screen = next.Screen
			if retry := old.KeyRetry; retry != nil && now.Sub(retry.At) < flow.PassInterval<<min(max(retry.Tries-1, 0), 3) {
				**recp = old
				continue
			}
			next.KeyRetry = nil
		}
		stop := slices.Contains(dec.Asked, flow.RunActStop)
		if retry := old.StopRetry; stop && retry != nil {
			if now.Sub(retry.At) < flow.EnsureBackoff(s.Settings, retry.Tries) {
				break
			}
			row.ID, next.Result, dec.Why = retry.Row, retry.Result, "ended("+retry.Result+")"
		}
		if old.StopRetry == nil && (row.ID == "R5" || row.ID == "R19") && next.State == flow.PhaseEnded && next.Result == flow.EndQuota {
			e.recordQuotaCooldownLocked(*s, flow.Candidate{CLI: next.CLI, Model: next.Model}, ev.LimitLine, now)
		}
		r31Send := row.ID == "R31" && slices.Contains(dec.Asked, flow.RunActSend)
		var prevPools []string
		revertR31 := func(rec flow.RunRecord) {
			**recp = rec
			if handoffPools != nil {
				*handoffPools = prevPools
			}
		}
		**recp = next
		if r31Send {
			if handoffPools != nil {
				prevPools = slices.Clone(*handoffPools)
				if pool := poolCooldownKey(flow.Candidate{CLI: next.CLI, Model: next.Model}); !slices.Contains(*handoffPools, pool) {
					*handoffPools = append(*handoffPools, pool)
				}
			}
			if !e.saveLocked(id, *s) {
				revertR31(old)
				continue events
			}
			before = next
		}
		if len(dec.Asked) > 0 && !e.cleanLocked(id) {
			**recp = old
			return old.TaskRun(), nil
		}
		for _, act := range dec.Asked {
			msg := ev.Msg
			if act == flow.RunActSendHeld && len(old.Held) > 0 {
				msg = old.Held[0]
			}
			if aerr := world.ActRun(ctx, e.HC, next, act, msg); aerr != nil {
				e.logError("factory.exec_error", id, aerr)
				if act == flow.RunActKeys {
					tries := 1
					if old.KeyRetry != nil {
						tries += old.KeyRetry.Tries
					}
					old.KeyRetry = &flow.KeyRetry{At: now, Tries: tries}
					**recp = old
					continue events
				}
				if act == flow.RunActStop {
					tries := 1
					if old.StopRetry != nil {
						tries += old.StopRetry.Tries
					}
					old.StopRetry = &flow.StopRetry{Row: row.ID, Result: next.Result, At: now, Tries: tries}
					**recp = old
					e.saveLocked(id, *s)
					return old.TaskRun(), nil
				}
				if r31Send {
					var he *hc.Error
					if act == flow.RunActInterrupt || errors.As(aerr, &he) {
						old.HandoffDeadline, old.Reason = next.HandoffDeadline, next.Reason
						revertR31(old)
						e.saveLocked(id, *s)
						before = old
					}
					continue events
				}
				if act == flow.RunActSendHeld {
					**recp = old
				}
			}
		}
		if (old.State != flow.PhaseEnded && next.State == flow.PhaseEnded && (!fresh || taskID != "")) || row.ID == "R3" || row.ID == "R21" || r31Send {
			e.commitLogLocked(id, s, LogLine{By: "engine", Task: taskID, Rule: row.ID, Asked: dec.Asked, Decision: dec.Why, Why: fmt.Sprintf("%s run %s: %s", role, next.Address, cmp.Or(next.Reason, dec.Why))})
			before = next
		}
		if row.ID == "R3" {
			break // Observe the screen again before acting on its timeout.
		}
	}
	if !reflect.DeepEqual(before, **recp) {
		e.saveLocked(id, *s)
	}
	return (*recp).TaskRun(), nil
}

// launchRunLocked persists the run label, session ID and token hash before launch.
// Worker and reviewer crash recovery can still find an existing run by name.
func (e *Engine) launchRunLocked(ctx context.Context, id string, s *flow.State, hash *string, recp **flow.RunRecord, adopt bool, rec flow.RunRecord, spec world.RunSpec, term hc.Terminal) (string, bool, bool, error) {
	// The caller already looked up an adoptable terminal. Use that result
	// for both adoption and removal so a fresh launch does not look it up again.
	if rec.Role != flow.RoleCoordinator && e.HC != nil {
		var err error
		switch {
		case !adopt:
			err = world.StopRun(ctx, e.HC, spec.Name)
		case term.Address != "" && term.State != "live":
			_, err = e.HC.Remove(ctx, term.Address)
		}
		if err != nil && !errors.Is(err, hc.ErrNotFound) {
			e.logError("factory.hc_error", id, err)
			return "", false, true, err
		}
	}
	env := map[string]string{}
	maps.Copy(env, e.RunEnv)
	maps.Copy(env, spec.Env)
	if e.HC != nil {
		env["HC_SOCKET"] = e.HC.Socket
	}
	runID, minted, prevRec, prevHash := "run-"+strings.ToLower(rand.Text()[:12]), false, *recp, *hash
	if rec.Role == flow.RoleCoordinator && e.HC != nil {
		if _, err := e.HC.Get(ctx, spec.Name); err == nil {
			spec.Name = hcName(spec.Name + "-" + runID)
		}
	}
	spec.Labels["sheepdog.run"], spec.Labels["sheepdog.cli"], spec.Env = runID, rec.CLI, env
	spec.Labels["herdr.space"] = id
	rec.ID, rec.Model = runID, spec.Labels["sheepdog.model"]
	now := e.now()
	rec.State, rec.StartedAt, rec.LastActivity = flow.PhaseStarting, now, now
	var abort bool
	var err error
	if term.State != "live" {
		term, abort, err = world.StartTerminal(ctx, e.HC, spec, func() error {
			tok := rand.Text()
			*hash, *recp, env["SHEEPDOG_RUN_TOKEN"], minted = tokenHash(tok), &rec, tok, true
			if sErr := e.saveStateLocked(id, *s); sErr != nil {
				*hash, *recp = prevHash, prevRec
				e.logError("factory.commit_error", id, sErr)
				return sErr
			}
			return nil
		})
	}
	if err != nil {
		if abort && !minted {
			e.logError("factory.hc_error", id, err)
			return "", false, true, err
		}
		if prevRec != nil && prevRec.State == flow.PhaseEnded && prevRec.Result == flow.EndQuota {
			*hash, *recp = "", prevRec
			return "", false, abort, err
		}
		*hash, rec.Model = "", ""
		if prevRec != nil {
			rec.Model = prevRec.Model
		}
		rec, _, _ = flow.StepRun(rec, flow.RunEvent{Kind: flow.EvLaunchFailed}, s.Settings, now)
		*recp = &rec
		return "", false, abort, err
	}
	if i := max(slices.Index(term.Argv, "--session-id"), slices.Index(term.Argv, "--resume"), slices.Index(term.Argv, "--conversation")); !minted && i >= 0 && i+1 < len(term.Argv) {
		rec.SessionID = term.Argv[i+1]
	}
	choice := terminalChoice(term, flow.Candidate{CLI: rec.CLI, Model: rec.Model})
	if !minted && (choice.CLI == world.CodexCLI || choice.CLI == world.AgyCLI && rec.SessionID == "") {
		rec.SessionID, rec.Resumed = "", false
		if prevRec != nil && prevRec.ID == term.Labels["sheepdog.run"] {
			rec.SessionID, rec.Resumed, rec.StartedAt = prevRec.SessionID, prevRec.Resumed, prevRec.StartedAt
		}
	}
	rec.ID, rec.CLI, rec.Model = "", choice.CLI, choice.Model
	rec, _, _ = flow.StepRun(rec, flow.RunEvent{Kind: flow.EvLaunched, Address: term.Address, RunID: term.Labels["sheepdog.run"], CreatedMS: term.CreatedMS}, s.Settings, now)
	rec.Resumable, *recp = rec.SessionID != "", &rec
	return term.Address, minted || len(term.Argv) > 0 && (term.Argv[len(term.Argv)-1] == spec.Prompt || term.Argv[len(term.Argv)-1] == "-i="+spec.Prompt), false, nil
}

func (e *Engine) lookupLaunchTerminal(ctx context.Context, name string, adopt bool) (hc.Terminal, error) {
	if e.HC != nil && adopt {
		t, err := e.HC.Get(ctx, name)
		if err == nil && !hc.IsRemote(t.Address) {
			return t, nil
		}
		if err != nil && !errors.Is(err, hc.ErrNotFound) {
			return hc.Terminal{}, err
		}
	}
	return hc.Terminal{}, nil
}

func terminalChoice(t hc.Terminal, fallback flow.Candidate) flow.Candidate {
	arg := func(flag string) string {
		if i := slices.Index(t.Argv, flag); i >= 0 && i+1 < len(t.Argv) {
			return t.Argv[i+1]
		}
		return ""
	}
	cli, model, effort := t.Labels["sheepdog.cli"], t.Labels["sheepdog.model"], t.Labels["sheepdog.effort"]
	if bin := world.ArgvCLI(t.Argv); bin == world.CodexCLI {
		cli, model = world.CodexCLI, cmp.Or(model, arg("-m"))
	} else if cli == "" && bin == world.AgyCLI {
		cli = world.AgyCLI
	}
	return flow.Candidate{CLI: cmp.Or(cli, fallback.CLI, "claude"), Model: cmp.Or(model, arg("--model"), fallback.Model),
		Effort: cmp.Or(effort, arg("--effort"), fallback.Effort, "medium")}
}

type taskExec func(e *Engine, ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (decision string, abort bool, err error)

// taskExecutors dispatches each ask verb implemented so far to its executor.
// wake_coordinator is held for J12 (wake.go); others (stop_task) are
// logged as "not built yet".
var taskExecutors = map[string]taskExec{
	flow.AskNewAttempt:    (*Engine).execNewAttempt,
	flow.AskStartWorker:   (*Engine).execStartWorker,
	flow.AskPushPR:        (*Engine).execPushPR,
	flow.AskStartReview:   (*Engine).execStartReviewer,
	flow.AskNudgeReview:   (*Engine).execNudgeReviewer,
	flow.AskStopReview:    (*Engine).execStopReviewer,
	flow.AskStartRetro:    (*Engine).execStartReviewer,
	flow.AskNudgeRetro:    (*Engine).execNudgeReviewer,
	flow.AskStopRetro:     (*Engine).execStopReviewer,
	flow.AskTellFindings:  (*Engine).execTellFindings,
	flow.AskOpenRecap:     (*Engine).execOpenRecap,
	flow.AskOwnerNoRecap:  (*Engine).execOpenRecap,
	flow.AskSizeRecap:     (*Engine).execSizeRecap,
	flow.AskCIRecap:       (*Engine).execCIRecap,
	flow.AskTimeRecap:     (*Engine).execTimeRecap,
	flow.AskOpenN24:       (*Engine).execOpenGateAsk,
	flow.AskOpenGate:      (*Engine).execOpenGateAsk,
	flow.AskTellStop:      (*Engine).execTellStop,
	flow.AskTellDiverged:  (*Engine).execTellDiverged,
	flow.AskTellConflict:  (*Engine).execTellConflict,
	flow.AskMergeOnly:     (*Engine).execMergeOnly,
	flow.AskRerunCI:       (*Engine).execRerunCI,
	flow.AskTellCI:        (*Engine).execTellCI,
	flow.AskTellBaseRed:   (*Engine).execTellBaseRed,
	flow.AskDispatchCI:    (*Engine).execDispatchCI,
	flow.AskUpdateBranch:  (*Engine).execUpdateBranch,
	flow.AskFailUpdate:    (*Engine).execFailUpdate,
	flow.AskAccept:        (*Engine).execRecordAccept,
	flow.AskMerge:         (*Engine).execMerge,
	flow.AskSpendOverride: (*Engine).execSpendOverride,
	flow.AskMarkDone:      (*Engine).execMarkDone,
	flow.AskCleanup:       (*Engine).execCleanup,
	flow.AskStopRuns:      (*Engine).execStopRuns,
}

func setFlag(m *map[string]bool, id string, on bool) {
	if !on {
		delete(*m, id)
	} else {
		if *m == nil {
			*m = map[string]bool{}
		}
		(*m)[id] = true
	}
}

func actOutage(verb string, f flow.Facts, err error) bool {
	var gitErr *world.GitError
	if errors.As(err, &gitErr) && gitErr.RPC != nil {
		return f.HCUnavailable
	}
	switch verb {
	case flow.AskNewAttempt, flow.AskPushPR, flow.AskRerunCI, flow.AskDispatchCI, flow.AskUpdateBranch, flow.AskMerge:
		return f.GitHubUnavailable
	case flow.AskStartWorker, flow.AskStartReview, flow.AskNudgeReview, flow.AskStopReview, flow.AskStartRetro, flow.AskNudgeRetro, flow.AskStopRetro, flow.AskTellFindings, flow.AskTellStop, flow.AskTellDiverged, flow.AskTellConflict, flow.AskTellCI, flow.AskTellBaseRed, flow.AskStopRuns, flow.AskStopCoordinator, flow.AskOpenRecap, flow.AskOwnerNoRecap, flow.AskSizeRecap, flow.AskCIRecap, flow.AskTimeRecap:
		return f.HCUnavailable
	case flow.AskCleanup:
		return f.GitHubUnavailable || f.HCUnavailable
	default:
		return false
	}
}

func (e *Engine) passTaskLocked(ctx context.Context, id string, s *flow.State, ti int, hasSlot bool, workerRun flow.Run, workerErr error, baseTried *bool, dt time.Duration) bool {
	if f, ok := e.observeTaskLocked(ctx, id, s, ti, hasSlot, workerRun, workerErr, *baseTried, dt); ok {
		e.actTaskLocked(ctx, id, s, ti, f, baseTried)
		return true
	}
	return false
}

func (e *Engine) observeTaskLocked(ctx context.Context, id string, s *flow.State, ti int, _ bool, workerRun flow.Run, workerErr error, baseTried bool, dt time.Duration) (flow.Facts, bool) {
	facts := flow.Facts{GitHubUnavailable: e.ghUnavail[id] || s.Project.HasAlert(flow.AlertGitHubUnavailable), HCUnavailable: e.hcUnavail[id] || s.Project.HasAlert(flow.AlertHCUnavailable)}
	if b := s.Project.Base; b != nil && b.Red {
		facts.BaseRed, facts.BaseRedSince = true, b.Since
		if !baseTried {
			facts.BaseRedSpell, facts.BaseRedWoken = b.SpellID, flow.Done(s.Project.Acts, flow.AskWake+"@base_red_held@"+b.SpellID)
		}
	}
	// An ended task needs no facts (N1 matches first), and cleanup may
	// have removed its worktree.
	task := s.Tasks[ti]
	facts.SplitFromHadRetro = flow.SplitFromHadRetro(s.Tasks, task)
	oa := flow.OpenAttempt(task)
	if !flow.Ended(task) {
		facts.DepsNotDone, facts.DepsDropped = flow.UnmetDeps(task, s.Tasks)
		facts.OtherMergeInFlight = otherMergeInFlight(s.Tasks, ti)
	}
	if oa != nil && !flow.Ended(task) {
		a := *oa
		gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
		tip, rel, _, err := e.git.InspectWorktree(gctx, a.Worktree, flow.H(a))
		readyHead := a.BaseCommit
		if n := len(a.Ready); n > 0 {
			readyHead = a.Ready[n-1].Head
		}
		if err == nil && rel == flow.RelDiverged && readyHead != flow.H(a) {
			var rrel flow.Rel
			rrel, err = e.git.Relation(gctx, a.Worktree, readyHead, tip)
			facts.TipDivergedByMergeOnly = rrel == flow.RelAhead
		}
		cancel()
		e.logOnce("factory.git_error", id, a.Worktree, err)
		if err != nil {
			return facts, false
		}
		lctx, lcancel := context.WithTimeout(ctx, lockGitTimeout)
		facts.Lines, facts.LinesGenerated, facts.LinesTimeout, facts.TipMergeOnly, err = e.readTipLocked(lctx, a, tip, rel)
		lcancel()
		if e.logOnce("factory.git_size_error", id, a.Worktree, err); err != nil {
			return facts, false
		}
		if workerErr != nil {
			e.logError("factory.hc_error", id, workerErr)
			return facts, false
		}
		facts.Worker = workerRun
		facts.Tip, facts.TipRel = tip, rel
		if len(a.Ready) > 0 { // no PR can exist before the first ready
			facts.PR = e.readPRLocked(ctx, id, s, ti)
		}
		facts.GitHubUnavailable = e.ghUnavail[id] || s.Project.HasAlert(flow.AlertGitHubUnavailable)
		if e.observeReviewLocked(ctx, id, s, ti, &facts, dt) {
			return facts, false
		}
		if !facts.RetroUnavailable {
			facts.RetroUnavailable = retroUnavailable(task, s.Settings, flow.ResolveFacts{})
		}
		if !facts.RetroUnavailable && flow.OpenRecap(task) == nil && flow.Ladder(flow.In{Task: task, Settings: s.Settings, Facts: facts, Now: e.now()}) == flow.RetroAnswer {
			facts.RetroUnavailable = e.retroUnavailableLocked(ctx, task, s.Settings)
		}
	}
	return facts, true
}

func (e *Engine) retroUnavailableLocked(ctx context.Context, task flow.Task, settings flow.Settings) bool {
	return retroUnavailable(task, settings, e.probeLaunchFacts(ctx, flow.RoleRetro, task, settings))
}

func retroUnavailable(task flow.Task, settings flow.Settings, rf flow.ResolveFacts) bool {
	worker := storedWorkerModel(task)
	if worker.Model == "" {
		if in, r := resolveWorkerReason(task, settings, rf); r.Why == "" {
			worker = intentCandidate(in)
		}
	}
	rf.Worker = worker
	rf.Check = launchCapability(flow.RoleRetro)
	_, reason := flow.ResolveWithReason(flow.RoleRetro, task, settings, rf)
	return reason.Why != "" && reason.Kind != flow.ReasonPool && !reason.PoolBlocked
}

func (e *Engine) actTaskLocked(ctx context.Context, id string, s *flow.State, ti int, facts flow.Facts, baseTried *bool) {
	if !e.cleanLocked(id) {
		return
	}
	facts.GitHubUnavailable = e.ghUnavail[id] || s.Project.HasAlert(flow.AlertGitHubUnavailable)
	if *baseTried {
		facts.BaseRedSpell, facts.BaseRedWoken = "", false
	}
	task, now := s.Tasks[ti], e.now()
	if !flow.Ended(task) {
		facts.DepsNotDone, facts.DepsDropped = flow.UnmetDeps(task, s.Tasks)
		facts.OtherMergeInFlight = otherMergeInFlight(s.Tasks, ti)
	}
	if !flow.Ended(task) && !e.cancelStaleGateAsksLocked(id, s, ti, now) {
		return
	}
	r, d := flow.FirstMatch(flow.TaskRows, flow.In{Task: task, Settings: s.Settings, Facts: facts, Asks: s.Asks, Now: now})
	e.reconcileCIStalledLocked(id, s, task, r.ID, facts, now)
	rowChanged := s.Tasks[ti].LastRow != r.ID
	s.Tasks[ti].LastRow = r.ID
	holdWhy := ""
	if len(d.Asked) == 0 && d.Hold {
		holdWhy = d.Why
	} else if facts.NoSlot {
		if _, asks := e.matchTaskStartLocked(id, *s, task, facts, now); asks {
			holdWhy = "no slot is free"
		}
	}
	previousHold, previousHoldKey := s.Tasks[ti].HoldWhy, s.Tasks[ti].HoldKey
	holdChanged := previousHold != holdWhy
	s.Tasks[ti].HoldWhy, s.Tasks[ti].HoldKey = holdWhy, holdWhy
	waitKey, waitVal := id+"/"+s.Tasks[ti].ID, ""
	if r.ID == "N9" || r.ID == "N10" || r.ID == "N38" {
		waitVal = r.ID + ":" + d.Why
	}
	setWait := func() {
		if waitVal != "" {
			if e.waits == nil {
				e.waits = map[string]string{}
			}
			e.waits[waitKey] = waitVal
		}
	}
	if waitVal == "" {
		delete(e.waits, waitKey)
	} else if len(d.Asked) == 0 && e.waits[waitKey] != waitVal {
		if e.commitLogLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: facts, Rule: r.ID, Decision: "waiting", Why: d.Why}) {
			setWait()
		}
		return
	}
	if len(d.Asked) == 0 {
		if holdWhy != "" && (rowChanged || holdChanged) {
			e.commitLogLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: facts, Rule: r.ID, Decision: "waiting", Why: holdWhy})
		} else if rowChanged || holdChanged {
			e.saveLocked(id, *s)
		}
		return
	}
	var decs []string
	whys := []string{d.Why}
	asksBefore := make([]string, len(s.Asks))
	for ai, a := range s.Asks {
		asksBefore[ai] = a.Status
	}
	act := func(k, res, cls, errText string, uncounted bool) { // an outcome replaces the intent or prior uncounted try
		acts := &s.Tasks[ti].Acts
		if oa := flow.OpenAttempt(s.Tasks[ti]); oa != nil {
			acts = &oa.Acts
		}
		entry := flow.Act{Key: k, Result: res, Class: cls, Err: flow.CleanErr(errText), Uncounted: uncounted, At: now}
		if n := len(*acts); n > 0 && (*acts)[n-1].Key == k && (*acts)[n-1].Result == "asked" {
			*acts = (*acts)[:n-1]
		}
		for i := len(*acts) - 1; res == "failed" && uncounted && i >= 0 && (*acts)[i].Key != flow.AskRetry; i-- {
			if (*acts)[i].Key == k {
				if (*acts)[i].Result == "failed" && (*acts)[i].Uncounted {
					entry.Tries = max(1, (*acts)[i].Tries) + 1
					(*acts)[i] = entry
					return
				}
				break
			}
		}
		*acts = append(*acts, entry)
	}
	var deferredAccept string
	for i, ask := range d.Asked {
		verb, _, _ := strings.Cut(ask, "@")
		if verb == flow.AskAccept && r.ID == "N19" {
			deferredAccept = ask
			continue
		}
		if verb == flow.AskWake {
			act(ask, "done", "", "", false)
			if strings.HasPrefix(ask, flow.AskWake+"@base_red_held@") {
				s.Project.Acts = append(s.Project.Acts, flow.Act{Key: ask, Result: "done", At: now})
			}
			decs = append(decs, holdWake(s, ti, r.ID, d.Why, ask, facts, e.Bin, now))
			continue
		}
		fn := taskExecutors[verb]
		// An executor runs to its end even when the pass is cancelled (its
		// calls carry their own timeouts), so its outcome is recorded and
		// the next pass does not ask again; the rest waits for that pass.
		if verb == flow.AskRerunCI || verb == flow.AskDispatchCI || verb == flow.AskUpdateBranch || verb == flow.AskMerge { // persist intent (and any accept) before a GitHub call whose outcome can be lost
			act(ask, "asked", "", "", false)
			if !e.saveLocked(id, *s) {
				return
			}
		}
		if !e.cleanLocked(id) {
			return
		}
		f := facts
		f.Ask = ask
		dec, abort, err := fn(e, context.WithoutCancel(ctx), id, s, ti, f)
		var wait launchWait
		if errors.As(err, &wait) {
			s.Tasks[ti].HoldWhy, s.Tasks[ti].HoldKey = wait.Why, wait.Key
			if rowChanged || previousHoldKey != wait.Key || len(decs) > 0 {
				if e.commitLogLocked(id, s, LogLine{By: "engine", Task: task.ID, Rule: r.ID, Asked: d.Asked[:i+1], Decision: "waiting", Why: wait.Why}) {
					setWait()
				}
			} else if previousHold != wait.Why {
				e.saveLocked(id, *s)
			}
			return
		}
		if abort && err == nil {
			act(ask, "failed", flow.ClassTransient, dec, true)
			decs = append(decs, dec)
		} else if errors.Is(err, world.ErrNoDispatchWorkflow) {
			act(ask, "skipped", "", "", false)
			decs = append(decs, "no dispatch_workflow set, or its file is missing")
		} else if err != nil {
			if !errors.Is(err, world.ErrGitSuppressed) {
				e.logError("factory.exec_error", id, err)
			}
			cls := world.FailureClass(err)
			act(ask, "failed", cls, err.Error(), errors.Is(err, world.ErrGitSuppressed) || cls != flow.ClassSettled && actOutage(verb, facts, err))
			whys = append(whys, err.Error())
		} else {
			act(ask, "done", "", "", false)
			if verb == flow.AskMerge && deferredAccept != "" {
				act(deferredAccept, "done", "", "", false)
			}
			decs = append(decs, dec)
		}
		if verb == flow.AskTellBaseRed {
			*baseTried = true
			if err == nil {
				s.Project.Acts = append(s.Project.Acts, flow.Act{Key: ask, Result: "done", At: now})
			}
		}
		if ctx.Err() != nil || abort && err != nil {
			d.Asked = d.Asked[:i+1] // the rest is asked again next pass
			break
		}
	}
	if err := e.commitLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: facts, Rule: r.ID, Asked: d.Asked, Decision: strings.Join(decs, "; "), Why: strings.Join(whys, "; ")}); err != nil {
		e.logError("factory.commit_error", id, err)
		return
	}
	setWait()
	for ai, a := range s.Asks {
		if ai >= len(asksBefore) {
			e.notifyChangeLocked(id, s.Seq, Change{Kind: flow.EvAskOpen, AskID: a.ID, Task: a.Task})
		} else if asksBefore[ai] != a.Status && a.Status == flow.AskStatusCancelled {
			e.notifyChangeLocked(id, s.Seq, Change{Kind: flow.EvAskCancel, AskID: a.ID, Task: a.Task})
		}
	}
}

// readTipLocked reads the changed lines at tip and whether it is ahead of H
// by merges of the base only, once per tip and H. The base commit itself
// has no changed lines. A changed-lines read that times out counts as over
// size_cap (design.md 3, last_tip) and is never cached, so the next pass
// reads again.
func (e *Engine) readTipLocked(ctx context.Context, a flow.Attempt, tip string, rel flow.Rel) (int, int, bool, bool, error) {
	h := flow.H(a)
	if c, ok := e.tips[a.Worktree]; ok && c.tip == tip && c.h == h {
		return c.lines, c.generated, false, c.mergeOnly, nil
	}
	c, err := tipFacts{tip: tip, h: h}, error(nil)
	if tip != a.BaseCommit {
		c.lines, c.generated, err = e.git.ChangedLines(ctx, a.Worktree, tip)
		if errors.Is(err, world.ErrChangedLinesOverCap) {
			return 0, 0, true, false, nil
		}
	}
	if err == nil && rel == flow.RelAhead {
		c.mergeOnly, err = e.git.MergeOnly(ctx, a.Worktree, h, tip)
	}
	if err != nil {
		return 0, 0, false, false, err
	}
	if e.tips == nil {
		e.tips = map[string]tipFacts{}
	}
	e.tips[a.Worktree] = c
	return c.lines, c.generated, false, c.mergeOnly, nil
}

func (e *Engine) execNewAttempt(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t, n := &s.Tasks[ti], len(s.Tasks[ti].Attempts)+1
	if _, _, reason := e.resolveLaunch(ctx, id, flow.RoleWorker, *t, *s, nil, func(f flow.ResolveFacts) (flow.Intent, flow.ResolveReason) {
		return resolveWorkerReason(*t, s.Settings, f)
	}); reason.Why != "" {
		return "", false, launchWaitFor(reason)
	}
	// The branch carries the project and the attempt, so a new project on
	// the same repo never reuses a branch an older PR (even a closed one)
	// already names (seen live: a closed PR from an earlier run blocked N26).
	tag := fmt.Sprintf("%s-%s-%d", strings.TrimPrefix(id, "proj-"), t.ID, n)
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	wt, err := e.git.NewWorktree(gctx, s.Project.Folder, e.Paths.Worktree(id, t.ID, n), tag, t.Title)
	if err == nil {
		t.Acts = slices.DeleteFunc(t.Acts, func(a flow.Act) bool { return a.Key == flow.AskNewAttempt && a.Result == "failed" })
		t.Attempts = append(t.Attempts, flow.Attempt{BaseCommit: wt.BaseCommit, Branch: wt.Branch, Worktree: wt.Path, Ready: []flow.Ready{}, Rounds: []flow.Round{}, Accepts: []flow.Accept{}, Acts: []flow.Act{},
			SizeCap: s.Settings.SizeCapFactor * t.EstimateLines})
	}
	return "new attempt " + wt.Branch, false, err
}

func (e *Engine) execStartWorker(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a, parts := &s.Tasks[ti], &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1], strings.Split(f.Ask, "@")
	mode, counted, prev := "", len(parts) > 2, a.Run
	if len(parts) > 1 {
		mode = parts[1]
	}
	adopt, name := prev == nil || prev.State != flow.PhaseEnded, workerName(id, t.ID, len(t.Attempts))
	term, err := e.lookupLaunchTerminal(ctx, name, adopt)
	if err != nil {
		return "", true, err
	}
	choice, env := flow.Candidate{}, map[string]string(nil)
	if term.State != "live" {
		in, codexEnv, reason := e.resolveLaunch(ctx, id, flow.RoleWorker, *t, *s, nil, func(f flow.ResolveFacts) (flow.Intent, flow.ResolveReason) {
			return resolveWorkerReason(*t, s.Settings, f)
		})
		if reason.Why != "" {
			return "", false, launchWaitFor(reason)
		}
		choice, env = intentCandidate(in), codexEnv
	} else {
		choice = terminalChoice(term, storedWorkerModel(*t))
	}
	sid, resume, bin := "", mode == flow.ContinueResume && prev != nil && prev.SessionID != "" && prev.CLI == choice.CLI, cmp.Or(e.Bin, "sheepdog")
	if mode == flow.ContinueResume && !resume {
		mode = flow.ContinueHandoff
	}
	if resume || (adopt && (term.State == "live" || choice.CLI == "claude") && prev != nil && prev.CLI == choice.CLI && prev.SessionID != "") {
		sid = prev.SessionID
	} else if choice.CLI == "claude" {
		sid = world.NewUUID()
	}
	prompt := workerPrompt(workerTemplate, *t, *a, bin)
	if resume {
		prompt = cmp.Or(strings.Join(a.Held, "\n\n"), "Continue where you left off in this worktree (check git status and git log -1); when done, commit and run "+readyCmd(bin, t.Outcomes)+".")
	} else {
		if len(a.Held) > 0 {
			prompt = "Messages held for you:\n" + strings.Join(a.Held, "\n\n") + "\n\n" + prompt
		}
		if mode == flow.ContinueHandoff {
			prompt = e.handoffNote(ctx, id, *t, *a) + prompt
		}
	}
	termID, delivered, abort, err := e.launchRunLocked(ctx, id, s, &a.RunTokenHash, &a.Run, adopt, flow.RunRecord{Role: flow.RoleWorker, CLI: choice.CLI, SessionID: sid, Resumed: resume}, world.RunSpec{
		Name: name, Cwd: a.Worktree, ClaudeConfig: e.Paths.ClaudeConfig, Env: env,
		Argv: launchArgv(flow.RoleWorker, choice, a.Worktree, sid, resume), Prompt: prompt,
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleWorker, "sheepdog.task": t.ID, "sheepdog.model": choice.Model, "sheepdog.effort": choice.Effort},
	}, term)
	if counted && !abort {
		a.Restarts++
	}
	if err == nil {
		if !delivered {
			a.Run.Held = appendHeld(a.Run.Held, a.Held...)
		}
		a.RunID, a.Held = termID, nil
	}
	return "started worker " + termID + " (" + choice.CLI + "/" + choice.Model + ")", abort, err
}

func (e *Engine) handoffNote(ctx context.Context, id string, t flow.Task, a flow.Attempt) string {
	bp := e.Paths.Brief(id, t.ID)
	_ = writeAtomic(bp, []byte(fmt.Sprintf("# %s: %s\n\n%s\n\n## Outcomes\n\n%s\n", t.ID, t.Title, cmp.Or(strings.TrimSpace(t.Brief), "(none)"), bullets(t.Outcomes))))
	rounds, last := "none", "none"
	if len(a.Rounds) > 0 {
		rs := make([]string, len(a.Rounds))
		for i, r := range a.Rounds {
			rs[i] = fmt.Sprintf("r%d %s (%s, %d major(s))", r.N, r.Verdict, r.Kind, r.Majors)
		}
		rounds = fmt.Sprintf("%d (%s)", len(a.Rounds), strings.Join(rs, ", "))
		if lr := a.Rounds[len(a.Rounds)-1]; lr.Kind == flow.RoundSendBack {
			last = "send-back: " + lr.Why
		} else if lr.ReportPath != "" {
			last = lr.ReportPath
		}
	}
	commits, dirty := "none", "none"
	if e.git != nil && a.Worktree != "" {
		gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
		defer cancel()
		gitField := func(args ...string) string {
			res, err := e.git.Run(gctx, a.Worktree, args...)
			if err != nil {
				return fmt.Sprintf("unknown (%s)", flow.CleanErr(err.Error()))
			}
			return cmp.Or(strings.TrimSpace(string(res.Stdout)), "none")
		}
		commits = gitField("log", "--oneline", "--abbrev=7", map[bool]string{true: a.BaseCommit + "..HEAD", false: "-n10"}[a.BaseCommit != ""])
		dirty = gitField("status", "--porcelain", "--untracked-files=normal")
	}
	out := "Handoff from an earlier worker run:\n"
	if a.Run != nil && a.Run.Reason != "" {
		out += "- Reason: " + a.Run.Reason + "\n"
	}
	out += fmt.Sprintf("- Worktree: %s\n- Brief: %s\n- Branch commits: %s\n- Uncommitted files: %s\n- Rounds so far: %s\n- Last findings: %s\n",
		a.Worktree, bp, commits, dirty, rounds, last)
	if a.Run != nil && a.Run.LastChat != "" {
		out += "- Last chat:\n" + a.Run.LastChat + "\n"
	}
	return out + "\n"
}

// sourceMark is a rule line's trailing source, e.g. " [KB-REV-63]" (prompts.md 1).
var sourceMark = regexp.MustCompile(`(?m)[ \t]+\[[^\[\]\n]+\]$`)

// fill strips the source marks from a template, then fills its placeholders,
// so a filled value ending in "]" is never cut.
func fill(tmpl string, pairs ...string) string {
	return strings.TrimSpace(strings.NewReplacer(pairs...).Replace(sourceMark.ReplaceAllString(tmpl, "")))
}

func coordinatorPrompt(tmpl string, p flow.Project, bin string) string {
	return fill(tmpl, "{folder}", p.Folder, "{goal}", p.Goal, "{sheepdog}", bin)
}

func workerPrompt(tmpl string, t flow.Task, a flow.Attempt, bin string) string {
	return fill(tmpl, "{id}", t.ID, "{title}", t.Title, "{brief}", cmp.Or(strings.TrimSpace(t.Brief), "(none)"),
		"{not_doing}", cmp.Or(strings.TrimSpace(t.NotDoing), "(none)"), "{outcomes}", bullets(t.Outcomes), "{ready_cmd}", readyCmd(bin, t.Outcomes),
		"{worktree}", a.Worktree, "{branch}", a.Branch, "{sheepdog}", bin)
}

func (e *Engine) reviewerPromptWith(ctx context.Context, tmpl string, t flow.Task, a flow.Attempt, h, kind, diff, rejections string, round int, bin, findingsPath string) (string, error) {
	proofs, err := e.proofPrompt(ctx, a, h)
	if err != nil {
		return "", err
	}
	return fill(tmpl, "{id}", t.ID, "{title}", t.Title, "{brief}", cmp.Or(strings.TrimSpace(t.Brief), "(none)"),
		"{outcomes}", bullets(t.Outcomes), "{proofs}", proofs, "{rejections}", rejections, "{diff}", diff, "{kind}", kind, "{head}", h, "{round}", fmt.Sprint(round), "{code}", a.Review.Code,
		"{sheepdog}", bin, "{findings_path}", findingsPath), nil
}

func (e *Engine) retroPrompt(tmpl string, t flow.Task, a flow.Attempt, s flow.Settings, h, diff string, lines, round int, bin, findingsPath string) string {
	mode := "initial retro"
	if len(flow.AttemptRetros(a)) > 0 {
		mode = "delta check (bounded fix)"
	}
	workerModel := "(unknown)"
	if wm := storedWorkerModel(t); wm.Model != "" {
		workerModel = wm.CLI + "/" + wm.Model
	}
	var rounds []string
	for _, r := range a.Rounds {
		who := cmp.Or(strings.Trim(r.Reviewer.CLI+"/"+r.Reviewer.Model, "/"), "coordinator")
		line := fmt.Sprintf("- r%d (%s, %s): %s (%d major(s))", r.N, r.Kind, who, r.Verdict, r.Majors)
		if r.ReportPath != "" {
			line += " — " + r.ReportPath
		}
		rounds = append(rounds, line)
	}
	roundsText := "(none)"
	if len(rounds) > 0 {
		roundsText = strings.Join(rounds, "\n")
	}
	code := ""
	if a.Review != nil {
		code = a.Review.Code
	}
	caseFile := fmt.Sprintf("Worker model: %s\nSize: %d lines (estimate %d, cap %d)\nDiff: %s\nPrior rounds:\n%s",
		workerModel, lines, t.EstimateLines, a.SizeCap, diff, roundsText)
	return fill(tmpl, "{id}", t.ID, "{title}", t.Title, "{mode}", mode, "{round}", fmt.Sprint(round), "{head}", h,
		"{brief}", cmp.Or(strings.TrimSpace(t.Brief), "(none)"),
		"{not_doing}", cmp.Or(strings.TrimSpace(t.NotDoing), "(none)"),
		"{outcomes}", bullets(t.Outcomes), "{case_file}", caseFile,
		"{continue_lines}", fmt.Sprint(s.ContinueLines), "{code}", code, "{sheepdog}", bin, "{findings_path}", findingsPath)
}

func (e *Engine) reviewDraftPath(id, task string, round int, code string) string {
	p := e.Paths.ReviewDraft(id, task, round, code)
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func readyCmd(bin string, outcomes []flow.Outcome) string {
	parts := []string{bin + " factory report ready"}
	if needsProofs(outcomes) {
		for _, o := range outcomes {
			flag, val := "--proof", "<command>"
			if strings.HasPrefix(o.ProvedBy, "manual:") {
				flag, val = "--artifact", "<path>"
			} else if cmd, ok := strings.CutPrefix(o.ProvedBy, "cmd:"); ok {
				val = strings.TrimSpace(cmd)
			}
			parts = append(parts, flag+" '"+strings.ReplaceAll(o.Text+"="+val, "'", `'\''`)+"'")
		}
	}
	parts = append(parts, `--touched "<places>"`, `--skipped "<what was left out or not checked>"`)
	return strings.Join(parts, " ")
}

func storedWorkerModel(t flow.Task) flow.Candidate {
	if a := flow.OpenAttempt(t); a != nil && a.Run != nil && a.Run.CLI != "" && a.Run.Model != "" {
		return flow.Candidate{CLI: a.Run.CLI, Model: a.Run.Model}
	}
	return flow.Candidate{}
}

func reviewChoice(t flow.Task, s flow.Settings, a flow.Attempt, h string, facts flow.ResolveFacts, failed []string) (flow.Intent, string, string) {
	in, repeatWhy, reason := reviewChoiceReason(flow.RoleReviewer, t, s, a, h, facts, failed)
	return in, repeatWhy, reason.Why
}

func reviewChoiceReason(role string, t flow.Task, s flow.Settings, a flow.Attempt, h string, facts flow.ResolveFacts, failed []string) (flow.Intent, string, flow.ResolveReason) {
	var used []flow.Candidate
	if role == flow.RoleReviewer {
		for _, r := range flow.Counted(a) {
			if r.Reviewer.Model != "" && flow.Covers(a, r.Head, h) {
				c := flow.Candidate(r.Reviewer)
				c.CLI = cmp.Or(c.CLI, "claude")
				used = append(used, c)
			}
		}
	}
	for _, name := range failed {
		cli, model, qualified := strings.Cut(name, "/")
		if qualified && launchCapability(role)(flow.Candidate{CLI: cli}).Why == "" {
			used = append(used, flow.Candidate{CLI: cli, Model: model})
			continue
		}
		// Stored failures from older daemons only identify the model name.
		for _, c := range flow.Candidates(role, t, s) {
			if c.Model == name {
				used = append(used, c)
			}
		}
	}
	unused := facts
	unused.Avoid = used
	in, reason := flow.ResolveWithReason(role, t, s, unused)
	if reason.Why == "" || len(used) == 0 {
		return in, "", reason
	}
	repeatWhy := reason.Why
	in, reason = flow.ResolveWithReason(role, t, s, facts)
	return in, repeatWhy, reason
}

// observeReviewLocked checks an active reviewer run: stops it if its round is
// void, or reads and accepts its verdict tag once idle (design.md 4.5, 4.8).
func (e *Engine) observeReviewLocked(ctx context.Context, id string, s *flow.State, ti int, facts *flow.Facts, dt time.Duration) bool {
	t, a := &s.Tasks[ti], &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	if a.Review == nil {
		return false
	}
	role := a.Review.Role()
	retroExhausted := func() bool {
		return role == flow.RoleRetro && a.Review.Run != nil && a.Review.Run.State == flow.PhaseEnded &&
			a.Review.Run.Result != flow.EndStopped && a.Review.Run.Result != flow.EndQuota &&
			a.Review.Retries >= s.Settings.ReviewerRetries
	}
	voidRound := func(stopped bool) bool {
		if !e.cleanLocked(id) {
			return true
		}
		if err := e.stopRuns(ctx, []string{cmp.Or(a.Review.RunID, reviewerName(id, t.ID, len(t.Attempts), a.Review.Round))}); err != nil {
			e.logError("factory.hc_error", id, err)
			return true
		}
		if !stopped {
			a.Review.RunID, a.Review.RunTokenHash, a.Review.Run = "", "", &flow.RunRecord{Role: role, State: flow.PhaseEnded, Result: flow.EndError, EndedAt: e.now()}
		} else {
			stopReviewRec(a.Review, s.Settings, e.now())
		}
		return !e.saveLocked(id, *s)
	}
	if flow.Ended(*t) || facts.PR.State == flow.PRMerged || facts.PR.State == flow.PRClosed ||
		flow.OpenRecap(*t) != nil || !tipKeepsRound(*a, a.Review.Head, *facts) ||
		flow.PRHeadDivergedOrForeign(flow.In{Task: *t, Facts: *facts}) || a.Review.Round != len(a.Rounds)+1 ||
		flow.CoveringOverride(*a, flow.H(*a)) != nil {
		if a.Review.RunID == "" && a.Review.Run != nil && a.Review.Run.State == flow.PhaseEnded {
			return false
		}
		return voidRound(true)
	}
	if a.Review.RunID == "" {
		if a.Review.Run != nil && a.Review.Run.State == flow.PhaseEnded {
			facts.Reviewer = a.Review.Run.TaskRun()
			if retroExhausted() {
				facts.RetroUnavailable = true
			}
		}
		return false
	}
	if a.Review.Run != nil && a.Review.Run.State == flow.PhaseEnded && a.Review.RunID != "" {
		if err := e.stopRuns(ctx, []string{a.Review.RunID}); err != nil {
			e.logError("factory.hc_error", id, err)
			return true
		}
		a.Review.RunID, a.Review.RunTokenHash = "", ""
		if !e.saveLocked(id, *s) {
			return true
		}
		facts.Reviewer = a.Review.Run.TaskRun()
		if retroExhausted() {
			facts.RetroUnavailable = true
		}
		return false
	}
	wasLive := a.Review.Run != nil && a.Review.Run.State != "" && a.Review.Run.State != flow.PhaseEnded
	var err error
	if facts.Reviewer, err = e.advanceRunLocked(ctx, id, t.ID, s, a.Review.RunID, role, cmp.Or(a.Review.Reviewer.CLI, "claude"), &a.Review.Run); err != nil {
		e.logError("factory.hc_error", id, err)
		return true
	}
	save := false
	if dt > 0 && wasLive {
		a.Review.Age += dt
		save = true
	}
	if a.Review.Run != nil && a.Review.Run.State == flow.PhaseEnded && a.Review.RunID != "" {
		if err := e.stopRuns(ctx, []string{a.Review.RunID}); err != nil {
			e.logError("factory.hc_error", id, err)
			return true
		}
		a.Review.RunID, a.Review.RunTokenHash = "", ""
		save = true
	}
	if save {
		e.saveLocked(id, *s)
	}
	if retroExhausted() {
		facts.RetroUnavailable = true
	}
	facts.Reviewer.AskPending = a.Review.RunID != "" && ((a.Review.Run != nil && len(a.Review.Run.Held) > 0) ||
		slices.ContainsFunc(s.Asks, func(ask flow.Ask) bool {
			return ask.Status == flow.AskStatusOpen && ask.AskedBy == role && ask.RunID == a.Review.RunID
		}))
	if role == flow.RoleRetro || a.Review.Reviewer.CLI != world.CodexCLI || facts.Reviewer.State == flow.RunGone {
		return false
	}
	if !e.cleanLocked(id) {
		return true
	}
	rctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
	rd, err := world.ReadCodexReport(rctx, e.HC, a.Review.RunID, a.Review.Code)
	cancel()
	if err != nil {
		e.logError("factory.hc_error", id, err)
		return true
	}
	if rd.Status == world.ReportNotYet {
		return false
	}
	// The reviewer's allowed commands can write (tests, git diff --output):
	// a verdict counts only for an untouched worktree at the round's head.
	gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
	tip, _, clean, err := e.git.InspectWorktree(gctx, a.Worktree, flow.H(*a))
	cancel()
	if err != nil {
		e.logOnce("factory.git_error", id, a.Worktree, err)
		return true
	}
	if !clean || tip != facts.Tip {
		why := "the worktree moved"
		if !clean {
			why = "the reviewer left the worktree dirty"
		}
		e.logEvent("factory.review_void", map[string]string{"project": id, "task": t.ID, "round": fmt.Sprint(a.Review.Round), "why": why})
		return voidRound(clean)
	}
	err = e.acceptVerdictLocked(ctx, id, s, ti, rd)
	var ref *Refusal
	if errors.As(err, &ref) {
		facts.Reviewer.Refused = true
		if e.HC != nil && a.Review != nil && a.Review.RunID != "" && !a.Review.RefusalSent {
			msg := fmt.Sprintf("Your verdict was refused (%s). Fix it and end with the report tag again.", ref.Why)
			sctx, scancel := context.WithTimeout(ctx, lockGitTimeout)
			sendErr := world.SendRun(sctx, e.HC, a.Review.RunID, msg)
			scancel()
			if sendErr != nil {
				e.logError("factory.exec_error", id, sendErr)
			} else {
				a.Review.RefusalSent = true
				if !e.saveLocked(id, *s) {
					return true
				}
			}
		}
		return false
	} else if err != nil {
		e.logError("factory.exec_error", id, err)
	}
	return true
}

// tipKeepsRound: a round in flight at x keeps running while H is x or was
// reached from x by merge_only moves, and the tip is H or ahead of it by
// merges of the base only (N20 moves H next).
func tipKeepsRound(a flow.Attempt, x string, f flow.Facts) bool {
	h := flow.H(a)
	return flow.Covers(a, x, h) && (f.TipRel == flow.RelSame || f.TipRel == flow.RelBehind && flow.Covers(a, f.Tip, h) || f.TipRel == flow.RelAhead && f.TipMergeOnly)
}

func (e *Engine) execStartReviewer(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := &s.Tasks[ti], &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	round, h, parts := len(a.Rounds)+1, flow.H(*a), strings.Split(f.Ask, "@")
	isRetro := strings.HasPrefix(f.Ask, flow.AskStartRetro)
	role := flow.RoleReviewer
	if isRetro {
		role = flow.RoleRetro
	}
	prev := a.Review
	counted, retries, failed := len(parts) == 4 && parts[2] != flow.ContinueResume, 0, []string(nil)
	if a.Review != nil && a.Review.Round == round && flow.Covers(*a, a.Review.Head, h) && a.Review.Role() == role {
		retries, failed = a.Review.Retries, slices.Clone(a.Review.FailedModels)
		if counted && a.Review.Reviewer.Model != "" {
			failed = append(failed, cmp.Or(a.Review.Reviewer.CLI, "claude")+"/"+a.Review.Reviewer.Model)
		}
	}
	name := reviewerName(id, t.ID, len(t.Attempts), round)
	worker := storedWorkerModel(*t)
	workers := []flow.Candidate{worker}
	workerNote := ""
	if worker.Model == "" {
		workers = flow.Candidates(flow.RoleWorker, *t, s.Settings)
		workerNote = ". Worker identity is unknown. Selected a " + role + " distinct from every worker candidate"
	}
	check := func(c flow.Candidate) flow.ResolveReason {
		if slices.ContainsFunc(workers, c.SameModel) {
			return flow.ReviewerDiffers
		}
		return launchCapability(role)(c)
	}
	adopt := prev == nil || prev.Run == nil || prev.Run.State != flow.PhaseEnded
	term, err := e.lookupLaunchTerminal(ctx, name, adopt)
	if err != nil {
		return "", true, err
	}
	var choice flow.Candidate
	var kind, diff, repeat string
	var env map[string]string
	if term.State == "live" {
		choice = terminalChoice(term, flow.Candidate{})
		if choice.CLI == "" || choice.Model == "" || (term.Labels["sheepdog.role"] != role && (isRetro || term.Labels["sheepdog.role"] != "")) || check(choice).Why != "" {
			term, adopt = hc.Terminal{}, false
		} else if isRetro {
			kind = flow.RoundRetro
		} else {
			kind = flow.RoundFull
		}
	}
	if term.State != "live" {
		var repeatWhy string
		in, codexEnv, reason := e.resolveLaunch(ctx, id, role, *t, *s, check, func(f flow.ResolveFacts) (flow.Intent, flow.ResolveReason) {
			var in flow.Intent
			var r flow.ResolveReason
			in, repeatWhy, r = reviewChoiceReason(role, *t, s.Settings, *a, h, f, failed)
			return in, r
		})
		if reason.Why != "" {
			if reason.Kind == flow.ReasonPool {
				reason.Why = "All reviewers are out of quota"
				if !reason.Reset.IsZero() {
					reason.Why += "; retries at " + formatReset(reason.Reset, e.now())
				}
			}
			return "", false, launchWaitFor(reason)
		}
		choice, env = intentCandidate(in), codexEnv
		if repeatWhy != "" {
			repeat = ". Repeating a " + role + " in a fresh session because " + repeatWhy
		}
	}
	review := flow.ReviewRun{Round: round, Head: h, Code: strings.ToLower(rand.Text()[:10]), Retries: retries, FailedModels: failed}
	if isRetro {
		review.Kind = flow.RoundRetro
	}
	if adopt && prev != nil && prev.Round == round && flow.Covers(*a, prev.Head, h) && prev.Code != "" && prev.Role() == role {
		review = *prev
	}
	review.Reviewer = flow.Reviewer(choice)
	a.Review = &review
	if counted && term.State != "live" {
		a.Review.Retries = retries + 1
	}
	prevQuota := prev != nil && prev.Run != nil && prev.Run.State == flow.PhaseEnded && prev.Run.Result == flow.EndQuota
	if term.State != "live" {
		gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
		var diffKind string
		diffKind, diff, err = e.reviewDiffPrompt(gctx, *a, h)
		cancel()
		if err != nil {
			a.Review.Run = &flow.RunRecord{Role: role, State: flow.PhaseEnded, Result: flow.EndFailed, EndedAt: e.now()}
			if prevQuota {
				a.Review = prev
			}
			return "", false, err
		}
		if !isRetro {
			kind = diffKind
		}
	}
	a.Review.Kind = cmp.Or(a.Review.Kind, kind)
	kind = a.Review.Kind
	model, effort, bin := choice.Model, choice.Effort, cmp.Or(e.Bin, "sheepdog")
	draftPath := e.reviewDraftPath(id, t.ID, round, a.Review.Code)
	if choice.CLI != world.CodexCLI && draftPath != "" && guardTestPath(draftPath) == nil {
		_ = os.MkdirAll(filepath.Dir(draftPath), 0o700)
	}
	var argv []string
	if choice.CLI == "claude" {
		if isRetro {
			argv = world.ClaudeRetroArgv(model, effort, bin, draftPath)
		} else {
			argv = world.ClaudeReviewerArgv(model, effort, bin, draftPath)
		}
	} else {
		argv = launchArgv(role, choice, a.Worktree, "", false)
	}
	var prompt string
	if isRetro {
		prompt = e.retroPrompt(retroTemplate, *t, *a, s.Settings, h, diff, f.Lines, round, bin, draftPath)
	} else {
		tmpl := reviewerCmdTemplate
		if choice.CLI == world.CodexCLI {
			tmpl = reviewerTemplate
		}
		gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
		defer cancel()
		prompt, err = e.reviewerPromptWith(gctx, tmpl, *t, *a, h, kind, diff, e.rejectionPrompt(id, t.ID, *a, h), round, bin, draftPath)
		if err != nil {
			a.Review = prev
			return "", true, err
		}
	}
	if choice.CLI == world.AgyCLI {
		prompt = "Do not modify, create or delete any files; read and run tests only (except writing findings to " + draftPath + ").\n\n" + prompt
	}
	termID, _, abort, err := e.launchRunLocked(ctx, id, s, &a.Review.RunTokenHash, &a.Review.Run, adopt, flow.RunRecord{Role: role, CLI: choice.CLI}, world.RunSpec{
		Name: name, Cwd: a.Worktree, ClaudeConfig: e.Paths.ClaudeConfig,
		Argv: argv, Prompt: prompt, Env: env,
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": role, "sheepdog.task": t.ID, "sheepdog.model": model, "sheepdog.effort": effort},
	}, term)
	if err == nil {
		a.Review.RunID = termID
	} else if abort || prevQuota {
		a.Review = prev
	}
	return fmt.Sprintf("started %s %s (round %d, %s, %s/%s)%s", role, termID, round, kind, choice.CLI, model, workerNote+repeat), abort, err
}

func (e *Engine) reviewDiffPrompt(ctx context.Context, a flow.Attempt, head string) (kind, diff string, err error) {
	var rel flow.Rel
	if r := flow.LastReviewed(a); r != nil && !flow.Covers(a, r.Head, head) {
		rel, err = e.git.Relation(ctx, a.Worktree, r.Head, head)
		if err != nil {
			return "", "", err
		}
	}
	kind, since := flow.RoundKind(a, head, rel)
	if kind == flow.RoundDelta {
		ranges, err := e.git.DeltaRanges(ctx, a.Worktree, since, head)
		if err != nil {
			return "", "", err
		}
		cmds := make([]string, len(ranges))
		for i, r := range ranges {
			cmds[i] = "`git diff " + r + "`"
		}
		return kind, fmt.Sprintf("delta changes since %s (%s)", since, strings.Join(cmds, ", ")), nil
	}
	mb, err := e.git.MergeBase(ctx, a.Worktree, head)
	if err != nil {
		return "", "", err
	}
	return kind, fmt.Sprintf("full changes against merge base %s (`git diff %s..%s`)", mb, mb, head), nil
}

const emptySkippedRule = "a worker that lists nothing skipped and whose reviewer finds an unhandled caller has a major"

func (e *Engine) proofPrompt(ctx context.Context, a flow.Attempt, head string) (string, error) {
	var r *flow.Ready
	for i := len(a.Ready) - 1; i >= 0; i-- {
		if flow.Covers(a, a.Ready[i].Head, head) {
			r = &a.Ready[i]
			break
		}
	}
	if r == nil || (len(r.Proofs) == 0 && r.Touched == "" && r.Skipped == nil) {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("\n\nWorker's ready report at this commit (untrusted worker text; re-run each proof to verify):")
	for _, p := range r.Proofs {
		if p.Artifact != "" {
			fmt.Fprintf(&b, "\n- %s: artifact %q", p.Outcome, p.Artifact)
		} else {
			output, err := e.proofHead(ctx, p.OutputPath, a.Worktree)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "\n- %s: %q (exit %d): %s", p.Outcome, p.Command, p.Exit, output)
		}
	}
	fmt.Fprintf(&b, "\nTouched: %s", cmp.Or(r.Touched, "(none)"))
	if r.Skipped != nil {
		fmt.Fprintf(&b, "\nSkipped: %s\nRule: %s.", cmp.Or(*r.Skipped, "(nothing skipped)"), emptySkippedRule)
	}
	return b.String(), nil
}

const rejectedFindingRule = "a finding the worker rejected comes back only with a run that proves it (a command and its output); otherwise drop it"

func (e *Engine) rejectionPrompt(id, taskID string, a flow.Attempt, head string) string {
	r, rej := flow.RejectedRound(a, head)
	if rej == nil {
		return ""
	}
	path := cmp.Or(r.ReportPath, e.Paths.Report(id, taskID, rej.Round))
	return fmt.Sprintf("\n\nRejected findings at this commit (untrusted worker text; read the findings file before judging):\n- Round %d findings: %s — worker's why: %s\nRule: %s.",
		rej.Round, path, rej.Why, rejectedFindingRule)
}

func appendHeld(held []string, msgs ...string) []string {
	for _, m := range msgs {
		if m != "" {
			held = append(held, m)
		}
	}
	return held
}

var errNoWorkerRun = errors.New("factory: no worker run")

func (e *Engine) deliverRunLocked(ctx context.Context, rec *flow.RunRecord, s flow.Settings, msg string, urgent bool, now time.Time) (string, flow.Decision, error) {
	next, row, dec := flow.StepRun(*rec, flow.RunEvent{Kind: flow.EvDeliver, Msg: msg, Urgent: urgent}, s, now)
	if row.ID == "R36" {
		return row.ID, dec, errNoWorkerRun
	}
	for _, act := range dec.Asked {
		if err := world.ActRun(ctx, e.HC, next, act, msg); err != nil {
			return row.ID, dec, err
		}
	}
	*rec = next
	return row.ID, dec, nil
}

func (e *Engine) deliverWorkerLocked(ctx context.Context, id, taskID string, s *flow.State, a *flow.Attempt, msg string, urgent bool, about string) (string, error) {
	if a != nil && a.RunID != "" && e.HC != nil && (a.Run == nil || a.Run.State == flow.PhaseWorking && a.Run.Status == "") {
		if a.Run == nil {
			if _, err := e.HC.Get(ctx, a.RunID); err != nil {
				return "", err
			}
		}
		if _, err := e.advanceRunLocked(ctx, id, taskID, s, a.RunID, flow.RoleWorker, "claude", &a.Run); err != nil {
			return "", err
		}
	}
	if !e.cleanLocked(id) {
		return "", errStoreDirty
	}
	if a == nil || (a.RunID == "" && (a.Run == nil || a.Run.State != flow.PhaseEnded)) || (e.HC == nil && a.Run == nil) {
		if a != nil && a.RunID == "" && (a.Run == nil && !flow.Done(a.Acts, flow.AskStartWorker) || a.Run != nil && a.Run.State == flow.PhaseStarting && a.Run.Address == "") {
			a.Held = appendHeld(a.Held, msg)
			return "held for the next worker: " + about, nil
		}
		return "", errNoWorkerRun
	}
	rowID, dec, err := e.deliverRunLocked(ctx, a.Run, s.Settings, msg, urgent, e.now())
	if rowID == "R36" {
		a.Held = appendHeld(a.Held, msg)
		e.commitLogLocked(id, s, LogLine{By: "engine", Task: taskID, Rule: rowID, Decision: dec.Why, Why: fmt.Sprintf("worker run %s ended (%s): held on task", a.Run.Address, a.Run.Result)})
		return "held for the next worker: " + about, nil
	}
	if err != nil {
		return "", err
	}
	return "told worker " + about, nil
}

func (e *Engine) execTellFindings(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t := s.Tasks[ti]
	a := &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	r, bin := a.Rounds[len(a.Rounds)-1], cmp.Or(e.Bin, "sheepdog")
	rc := readyCmd(bin, t.Outcomes)
	if r.Kind == flow.RoundSendBack {
		msg := fmt.Sprintf("The coordinator sent the task back: %s — change what it asks, commit, and run %s again.", r.Why, rc)
		dec, err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false, fmt.Sprintf("the send-back (round %d)", r.N))
		return dec, false, err
	}
	if r.Kind == flow.RoundRetro {
		change := ""
		for i := len(t.Retros) - 1; i >= 0; i-- {
			if t.Retros[i].Attempt == len(t.Attempts) && t.Retros[i].Round == r.N {
				change = t.Retros[i].WorkerChange
				break
			}
		}
		nextCap := f.Lines + s.Settings.ContinueLines
		msg := fmt.Sprintf("Retro round %d granted one bounded fix (%d major(s) in %s; size cap %d lines): %s — make only this bounded fix, commit, and run %s again.",
			r.N, r.Majors, r.ReportPath, nextCap, change, rc)
		dec, err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false, fmt.Sprintf("the bounded retro fix from round %d (size cap %d)", r.N, nextCap))
		if err == nil {
			a.SizeCap = nextCap
		}
		return dec, false, err
	}
	msg := fmt.Sprintf("Review round %d found %d major(s) in %s — fix them, commit, and run %s again, or when every finding is wrong and you change nothing, run %s factory report ready --reject \"<why>\".",
		r.N, r.Majors, r.ReportPath, rc, bin)
	dec, err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false, fmt.Sprintf("findings from round %d", r.N))
	return dec, false, err
}

// execOpenRecap is N25 (review) and N30 (owner_no): allowed split, new, drop.
func (e *Engine) execOpenRecap(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	trig := flow.TriggerReview
	if strings.HasPrefix(f.Ask, flow.AskOwnerNoRecap) {
		trig = flow.TriggerOwnerNo
	}
	return e.openRecapLocked(ctx, id, s, ti, trig, []string{"split", "new", "drop"})
}

// execSizeRecap is N12: open a size recap with the choices it allows now.
func (e *Engine) execSizeRecap(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	return e.openRecapLocked(ctx, id, s, ti, flow.TriggerSize, flow.SizeRecapAllowed(s.Tasks[ti], s.Settings))
}

// execCIRecap is N13: open a ci recap with the choices it allows now.
func (e *Engine) execCIRecap(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	return e.openRecapLocked(ctx, id, s, ti, flow.TriggerCI, flow.SizeRecapAllowed(s.Tasks[ti], s.Settings))
}

// execTimeRecap is N14: open a time recap with the choices it allows now.
func (e *Engine) execTimeRecap(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	return e.openRecapLocked(ctx, id, s, ti, flow.TriggerTime, flow.SizeRecapAllowed(s.Tasks[ti], s.Settings))
}

// execNudgeReviewer is N28/N29: nudge an idle reviewer or retro run with no verdict once.
func (e *Engine) execNudgeReviewer(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t := s.Tasks[ti]
	a := flow.OpenAttempt(t)
	if a == nil || a.Review == nil || a.Review.Run == nil || a.Review.RunID == "" {
		return "reviewer already gone", false, nil
	}
	role := a.Review.Role()
	var msg string
	if role == flow.RoleRetro {
		bin, draft := cmp.Or(e.Bin, "sheepdog"), e.reviewDraftPath(id, t.ID, a.Review.Round, a.Review.Code)
		msg = fmt.Sprintf("You went idle without submitting a retro verdict. Write findings to %s and run `%s factory report retro <ship|fix|redesign> --code %s --head %s --cause <cause> --worker-change \"<text>\" --worker-change-kind <check|brief> --coordinator-change \"<text>\" --coordinator-change-kind <check|brief> --findings %s`.", draft, bin, a.Review.Code, a.Review.Head, draft)
	} else if a.Review.Reviewer.CLI == world.CodexCLI {
		msg = fmt.Sprintf("You went idle with no report tag. End your reply with `<report>{\"code\":%q,\"round\":%d,\"head\":%q,\"verdict\":\"pass\"|\"changes\",\"majors\":N}</report>`.", a.Review.Code, a.Review.Round, a.Review.Head)
	} else {
		bin := cmp.Or(e.Bin, "sheepdog")
		draft := e.reviewDraftPath(id, t.ID, a.Review.Round, a.Review.Code)
		msg = fmt.Sprintf("You went idle without submitting a verdict. Write any findings to %s and run `%s factory report review <pass|changes> --code %s --head %s --self-check-misses <n> --deletable \"<text or none>\" --findings %s` (omit the findings flag on pass with no findings).", draft, bin, a.Review.Code, a.Review.Head, draft)
	}
	if _, _, err := e.deliverRunLocked(ctx, a.Review.Run, s.Settings, msg, false, e.now()); err != nil {
		return "", false, err
	}
	return fmt.Sprintf("nudged %s %s for verdict (round %d)", role, a.Review.RunID, a.Review.Round), false, nil
}

// execStopReviewer is N28/N29: stop a reviewer or retro run that stayed idle
// with no report after its nudge, or ran past review_minutes; ending as stuck lets 4.8 retry.
func (e *Engine) execStopReviewer(ctx context.Context, _ string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	a := flow.OpenAttempt(s.Tasks[ti])
	if a == nil || a.Review == nil {
		return "reviewer already gone", false, nil
	}
	runID, role := a.Review.RunID, a.Review.Role()
	if runID != "" {
		if err := e.stopRuns(ctx, []string{runID}); err != nil {
			return "", false, err
		}
	}
	if a.Review.Run == nil {
		a.Review.Run = &flow.RunRecord{Role: role}
	}
	a.Review.Run.State, a.Review.Run.Result, a.Review.Run.EndedAt = flow.PhaseEnded, flow.EndStuck, e.now()
	a.Review.RunID, a.Review.RunTokenHash = "", ""
	return fmt.Sprintf("stopped %s %s (round %d)", role, runID, a.Review.Round), false, nil
}

func (e *Engine) openRecapLocked(ctx context.Context, id string, s *flow.State, ti int, trigger string, allowed []string) (string, bool, error) {
	t := &s.Tasks[ti]
	if a := flow.OpenAttempt(*t); a != nil && a.Review != nil && (a.Review.RunID != "" || a.Review.Run == nil || a.Review.Run.State != flow.PhaseEnded) {
		if err := e.stopRuns(ctx, []string{cmp.Or(a.Review.RunID, reviewerName(id, t.ID, len(t.Attempts), a.Review.Round))}); err != nil {
			return "", false, err
		}
		stopReviewRec(a.Review, s.Settings, e.now())
	}
	if flow.OpenRecap(*t) == nil {
		t.Recaps = append(t.Recaps, flow.Recap{Trigger: trigger, Allowed: allowed})
	}
	return fmt.Sprintf("opened recap (trigger %s, allowed %s)", trigger, strings.Join(allowed, ", ")), false, nil
}

// execTellStop is N6 for a size, ci or time recap: the worker is told once to stop.
func (e *Engine) execTellStop(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := s.Tasks[ti], flow.OpenAttempt(s.Tasks[ti])
	if rc := flow.OpenRecap(t); rc != nil && rc.Trigger == flow.TriggerCI {
		msg := fmt.Sprintf("Task %s hit %d failing CI commits in the last hour. Stop now and write nothing more; the coordinator decides how the task goes on.", t.ID, s.Settings.CIFailsPerHour)
		dec, err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, true, "to stop at the CI failure limit")
		return dec, false, err
	}
	if rc := flow.OpenRecap(t); rc != nil && rc.Trigger == flow.TriggerTime {
		msg := fmt.Sprintf("Task %s hit its %dh working time limit. Stop now and write nothing more; the coordinator decides how the task goes on.", t.ID, s.Settings.BuildHours)
		dec, err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, true, "to stop at the build time limit")
		return dec, false, err
	}
	msg := fmt.Sprintf("Task %s is over its size cap: %d changed lines, cap %d. Stop now and write nothing more; the coordinator decides how the task goes on.", t.ID, f.Lines, a.SizeCap)
	if f.LinesTimeout {
		msg = fmt.Sprintf("Task %s timed out reading changed lines (counts as over cap %d). Stop now and write nothing more; the coordinator decides how the task goes on.", t.ID, a.SizeCap)
	}
	dec, err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, true, "to stop at the size cap")
	return dec, false, err
}

func pullPrefix(a *flow.Attempt, f flow.Facts) string {
	if f.TipRel == flow.RelBehind {
		return fmt.Sprintf("Pull first (`git pull --ff-only origin %s` — the engine updated the branch). ", a.Branch)
	}
	return ""
}

// execTellDiverged is N7: tell the worker once what happened, using flow.N7Why
// so the row and executor cannot drift.
func (e *Engine) execTellDiverged(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := s.Tasks[ti], flow.OpenAttempt(s.Tasks[ti])
	in := flow.In{Task: t, Facts: f}
	why := flow.N7Why(in, false)
	msg := fmt.Sprintf("%s. Then run %s.", why, readyCmd(cmp.Or(e.Bin, "sheepdog"), t.Outcomes))
	if flow.PRHeadDivergedOrForeign(in) {
		msg = pullPrefix(a, f) + msg
	}
	dec, err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false, why)
	return dec, false, err
}

// execTellConflict is N32: tell the worker once per H to merge the base (never rebase).
func (e *Engine) execTellConflict(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := s.Tasks[ti], flow.OpenAttempt(s.Tasks[ti])
	msg := fmt.Sprintf("%sPR #%d conflicts with the base at %s — merge the base into your branch (never rebase), resolve, commit, and run %s again.",
		pullPrefix(a, f), f.PR.Number, flow.H(*a), readyCmd(cmp.Or(e.Bin, "sheepdog"), t.Outcomes))
	dec, err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false, "PR conflicts with the base")
	return dec, false, err
}

// execMergeOnly is N20: H moves to a tip or PR head that is ahead of it by
// merges of the base only. No round is asked.
func (e *Engine) execMergeOnly(_ context.Context, _ string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	a := flow.OpenAttempt(s.Tasks[ti])
	from := flow.H(*a)
	_, to, _ := strings.Cut(f.Ask, "@")
	a.MergeOnly = append(a.MergeOnly, flow.Advance{From: from, To: to})
	return fmt.Sprintf("H moved from %s to merge-only head %s", from, to), false, nil
}

func bullets(items []flow.Outcome) string {
	if len(items) == 0 {
		return "(none)"
	}
	lines := make([]string, len(items))
	for i, o := range items {
		if o.ProvedBy != "" {
			lines[i] = "- " + o.Text + " (proved by: " + o.ProvedBy + ")"
		} else {
			lines[i] = "- " + o.Text
		}
	}
	return strings.Join(lines, "\n")
}

// execPushPR is N26: push H and make sure one PR is open for the branch.
// Title and body come from task fields only (design.md 9.4).
func (e *Engine) execPushPR(ctx context.Context, _ string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t, a := &s.Tasks[ti], &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	n, err := e.git.PushAndOpenPR(gctx, a.Worktree, a.Branch, flow.H(*a), t.Title, prBody(*t))
	if err == nil {
		a.PR = n
	}
	return fmt.Sprintf("PR #%d open for %s", n, a.Branch), false, err
}

// execRerunCI is N33/N34: re-run the failed jobs of the run in its key.
func (e *Engine) execRerunCI(ctx context.Context, _ string, s *flow.State, _ int, f flow.Facts) (string, bool, error) {
	run := strings.Split(f.Ask, "@")[1]
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	return "re-ran failed jobs of run " + run, false, e.git.RerunFailedJobs(gctx, s.Project.Folder, run)
}

// execTellCI is N33: tell the worker about the failing run in its key;
// prefix "pull first" only when H is ahead of the worker's tip.
func (e *Engine) execTellCI(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := s.Tasks[ti], flow.OpenAttempt(s.Tasks[ti])
	run, names := strings.Split(f.Ask, "@")[1], []string{}
	for _, c := range f.PR.Failed {
		if cmp.Or(c.Run, "none") == run && c.Name != "" && !slices.Contains(names, c.Name) {
			names = append(names, c.Name)
		}
	}
	msg := fmt.Sprintf("%sCI failed on PR #%d at %s (run %s: %s) — fix it, commit, and run %s again.", pullPrefix(a, f), f.PR.Number, flow.H(*a), run, strings.Join(names, ", "), readyCmd(cmp.Or(e.Bin, "sheepdog"), t.Outcomes))
	dec, err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false, "CI failed (run "+run+")")
	return dec, false, err
}

// execTellBaseRed is N38 past main_red_minutes: tell the oldest held task's
// worker once per red spell to merge the base into its branch and say ready.
func (e *Engine) execTellBaseRed(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := s.Tasks[ti], flow.OpenAttempt(s.Tasks[ti])
	msg := fmt.Sprintf("%sThe base branch has been red for %d+ minutes — merge the base into your branch (never rebase), fix if needed, commit, and run %s.",
		pullPrefix(a, f), s.Settings.MainRedMinutes, readyCmd(cmp.Or(e.Bin, "sheepdog"), t.Outcomes))
	dec, err := e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false, "base is red: merge base and say ready")
	return dec, false, err
}

// observeBaseLocked reads the push workflow runs on the base branch's head
// commit once per pass (project level, design.md 7.3, N38, KB-MRG-47) and
// persists the red spell in s.Project.Base. Read errors keep the last known
// value and never flip the base red.
func (e *Engine) observeBaseLocked(ctx context.Context, id string, s *flow.State) {
	e.passBaseHCErr = nil
	if s.Project.Folder == "" {
		return
	}
	gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
	res, err := e.git.BaseStatus(gctx, s.Project.Folder, func(src, base string) bool { ok, _ := parsePRWorkflow(src, base); return ok })
	cancel()
	var ge *world.GitError
	if errors.As(err, &ge) && ge.RPC != nil && ctx.Err() == nil {
		e.passBaseHCErr = ge.RPC
	}
	wasRed, now := s.Project.Base != nil && s.Project.Base.Red, e.now()
	e.observeGitHubLocked(id, s, err)
	if e.logOnce("factory.base_error", id, id, err); err != nil || !res.Known || res.Red == wasRed {
		b := s.Project.Base
		mainRed := b != nil && b.Red && now.Sub(b.Since) >= time.Duration(s.Settings.MainRedMinutes)*time.Minute
		if mainRed || err == nil && res.Known && !res.Red {
			e.reconcileAlertLocked(id, s, flow.AlertMainRed, mainRed, flow.Alert{Kind: flow.AlertMainRed, Cause: fmt.Sprintf("base branch red past %dm (%s)", s.Settings.MainRedMinutes, cmp.Or(b, &flow.BaseState{}).SpellID), At: now, Rule: "N38"}, "N38", "")
		}
		return
	}
	kind, dec, why := "base_green", "base turned green", fmt.Sprintf("base branch %s is green again (%s, run %d)", res.Branch, res.HeadSHA[:min(len(res.HeadSHA), 7)], res.ID)
	if res.Red {
		kind, dec, why = "base_red", "base turned red", fmt.Sprintf("base branch %s is red since %s (run %d, %s): holding branch updates and merges", res.Branch, res.HeadSHA[:min(len(res.HeadSHA), 7)], res.ID, res.Conclusion)
	}
	n, prefix := 1, flow.AskWake+"@"+kind+"@"+res.HeadSHA+"@"
	for _, a := range s.Project.Acts {
		if strings.HasPrefix(a.Key, prefix) {
			n++
		}
	}
	spell := fmt.Sprintf("%s@%d", res.HeadSHA, n)
	if res.Red {
		s.Project.Base = &flow.BaseState{Red: true, Since: now, SpellID: spell}
	} else {
		s.Project.Base = nil
		e.reconcileAlertLocked(id, s, flow.AlertMainRed, false, flow.Alert{}, "", "")
	}
	wakeKey := kind + "@" + spell
	actKey := flow.AskWake + "@" + wakeKey
	var asked []string
	if flow.Count(s.Project.Acts, actKey) == 0 && !s.Project.Finished {
		s.Project.Acts = append(s.Project.Acts, flow.Act{Key: actKey, Result: "done", At: now})
		holdProjectWake(s, wakeKey, "base_observer: "+why, fmt.Sprintf("branch %s; run %d; conclusion %s; head %s", res.Branch, res.ID, res.Conclusion, res.HeadSHA), now)
		asked = []string{actKey}
	}
	e.commitLogLocked(id, s, LogLine{By: "engine", Rule: "base_observer", Asked: asked, Decision: dec, Why: why})
}

// execUpdateBranch is N40: bring a behind PR up to date with a merge commit,
// pinned to H (design.md 9.4, KB-MRG-43).
func (e *Engine) execUpdateBranch(ctx context.Context, _ string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	a := flow.OpenAttempt(s.Tasks[ti])
	h := flow.H(*a)
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	return fmt.Sprintf("asked GitHub to update branch for PR #%d at %s", f.PR.Number, h), false, e.git.UpdateBranch(gctx, s.Project.Folder, f.PR.Number, h)
}

// execFailUpdate is N40 past checks_grace_minutes: the branch update did not
// move the PR head in time, so it becomes a failed try (design.md 4.7, 8).
func (e *Engine) execFailUpdate(_ context.Context, _ string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	a := flow.OpenAttempt(s.Tasks[ti])
	uk := flow.AskUpdateBranch + "@" + flow.H(*a)
	for i := len(a.Acts) - 1; i >= 0; i-- {
		if a.Acts[i].Key == uk {
			a.Acts[i].Result, a.Acts[i].Class, a.Acts[i].Err, a.Acts[i].At = "failed", flow.ClassTimeout, "branch update did not move the PR head within checks_grace_minutes", e.now()
			break
		}
	}
	return "branch update timed out for " + flow.H(*a) + " (failed try)", false, nil
}

// execDispatchCI is N35: dispatch the dispatch_workflow setting's workflow on the PR branch.
func (e *Engine) execDispatchCI(ctx context.Context, _ string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	a, wf := flow.OpenAttempt(s.Tasks[ti]), s.Settings.DispatchWorkflow
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	return "dispatched workflow " + wf + " on " + a.Branch, false, e.git.DispatchWorkflow(gctx, a.Worktree, a.Branch, flow.H(*a), wf)
}

// prBody is the PR body and squash message, from task fields only (design.md 9.4).
func prBody(t flow.Task) string {
	return fmt.Sprintf("%s\n\n## Outcomes\n\n%s\n\n---\nOpened by the sheepdog factory.", cmp.Or(strings.TrimSpace(t.Brief), "(no brief)"), bullets(t.Outcomes))
}

// readPRLocked is the attempt's PR snapshot (design.md 7.3). A failed read
// is not a snapshot: the last one is kept with Answered false. HeadSince is
// when this head was first seen by this daemon. A change is logged, so the
// log shows each move of the PR (checks pending -> green).
func (e *Engine) readPRLocked(ctx context.Context, id string, s *flow.State, ti int) flow.PR {
	a := s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	last := e.prs[a.Worktree]
	p, warn, err := e.git.PRSnapshot(gctx, s.Project.Folder, a.Branch, a.PR)
	e.logOnce("factory.gh_partial", id, a.Worktree, warn)
	e.observeGitHubLocked(id, s, err)
	if e.logOnce("factory.gh_error", id, a.Worktree, err); err != nil {
		if p.State == flow.PRNone {
			p = last
		}
		if p.State == flow.PRMerged || p.State == flow.PRClosed {
			p.State = flow.PRNone // a stale terminal state could end the attempt
		}
		if p.Head == last.Head {
			p.HeadSince = last.HeadSince
		}
		p.Answered, p.Checks = false, flow.ChecksUnreadable // never the last read's checks
	} else {
		if p.Head != "" {
			p.HeadSince = last.HeadSince
			if p.Head != last.Head || p.HeadSince.IsZero() {
				p.HeadSince = e.now()
			}
			h := flow.H(a)
			p.HeadRel, err = e.git.Relation(gctx, a.Worktree, h, p.Head)
			fetched := false
			if err != nil && p.Head != h {
				if ferr := e.git.FetchRemote(gctx, a.Worktree); ferr == nil {
					fetched = true
					p.HeadRel, err = e.git.Relation(gctx, a.Worktree, h, p.Head)
				}
			}
			e.logOnce("factory.git_error", id, a.Worktree+" head_rel", err) // unknown here (e.g. not fetched): no relation, never a guess
			if err == nil && p.HeadRel == flow.RelAhead {
				mo, merr := e.git.MergeOnly(gctx, a.Worktree, h, p.Head)
				if merr == nil && !mo && !fetched && (p.Head != last.Head || last.HeadMergeOnlyUnknown) {
					if ferr := e.git.FetchRemote(gctx, a.Worktree); ferr == nil {
						mo, merr = e.git.MergeOnly(gctx, a.Worktree, h, p.Head)
					} else {
						merr = ferr
					}
				}
				e.logOnce("factory.git_error", id, a.Worktree+" head_merge_only", merr)
				p.HeadMergeOnly = merr == nil && mo
				p.HeadMergeOnlyUnknown = merr != nil
			}
		}
	}
	ciFailAdded := false
	oa := &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	if p.Answered && p.State == flow.PROpen && p.Head != "" && p.Checks == flow.ChecksRed &&
		!slices.ContainsFunc(oa.CIFails, func(f flow.CIFail) bool { return f.Head == p.Head }) {
		oa.CIFails = append(oa.CIFails, flow.CIFail{Head: p.Head, At: e.now()})
		ciFailAdded = true
	}
	oldPending := oa.PendingChecks
	if p.Answered {
		switch {
		case p.State == flow.PROpen && p.Head != "" && p.Checks == flow.ChecksPending:
			if oldPending == nil || oldPending.Head != p.Head {
				oa.PendingChecks = &flow.PendingChecks{Head: p.Head, Since: e.now().Round(0)}
			}
		case p.State != flow.PROpen || p.Checks != flow.ChecksUnreadable || oldPending != nil && p.Head != "" && p.Head != oldPending.Head:
			oa.PendingChecks = nil
		}
	}
	if (oldPending != oa.PendingChecks || ciFailAdded || !reflect.DeepEqual(p, last)) && ctx.Err() == nil &&
		e.commitLogLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: p, Rule: "pr_snapshot", Why: "the PR snapshot changed"}) {
		if e.prs == nil {
			e.prs = map[string]flow.PR{}
		}
		e.prs[a.Worktree] = p
	}
	return p
}

func coordName(id string) string { return hcName("coord-" + strings.TrimPrefix(id, "proj-")) }
func workerName(id, task string, n int) string {
	return hcName(fmt.Sprintf("worker-%s-%s-%d", strings.TrimPrefix(id, "proj-"), task, n))
}
func reviewerName(id, task string, attempt, round int) string {
	return hcName(fmt.Sprintf("rev-%s-%s-%d-%d", strings.TrimPrefix(id, "proj-"), task, attempt, round))
}
func planReviewerName(id string, n int) string {
	return hcName(fmt.Sprintf("planrev-%s-%d", strings.TrimPrefix(id, "proj-"), n))
}

// hcName fits a terminal name to herdr-core's rule: 1-32 of a-z 0-9 - _ .
// starting with a letter. Task ids come from the coordinator's plan, so
// they may hold capitals or other characters.
func hcName(s string) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			b[i] = '-'
		}
	}
	if len(b) == 0 || b[0] < 'a' || b[0] > 'z' {
		b = append([]byte{'x'}, b...)
	}
	if len(b) > 32 || string(b) != s {
		return string(b[:min(len(b), 23)]) + "-" + tokenHash(s)[:8]
	}
	return string(b)
}

func (e *Engine) logError(typ, id string, err error) {
	e.logEvent(typ, map[string]string{"project": id, "error": err.Error()})
}

func (e *Engine) observeGitHubLocked(id string, s *flow.State, err error) {
	if err == nil || world.GitHubUnavailable(err) || errors.Is(err, world.ErrChecksDenied) {
		setFlag(&e.ghUnavail, id, err != nil)
		e.reconcileAlertLocked(id, s, flow.AlertGitHubUnavailable, err != nil, flow.Alert{Kind: flow.AlertGitHubUnavailable, Cause: cmp.Or(flow.CleanErr(errText(err)), "GitHub is unreachable"), At: e.now()}, "alert", "")
	}
}

// logOnce logs err unless it repeats the last error of typ for key; a nil
// err clears it, so the next failure is logged again.
func (e *Engine) logOnce(typ, id, key string, err error) {
	if errors.Is(err, world.ErrGitSuppressed) {
		return
	}
	k := typ + " " + key
	if err == nil {
		delete(e.errs, k)
		return
	}
	if e.errs[k] == err.Error() {
		return
	}
	if e.errs == nil {
		e.errs = map[string]string{}
	}
	e.errs[k] = err.Error()
	e.logError(typ, id, err)
}

func (e *Engine) logEvent(typ string, fields map[string]string) {
	if e.Log != nil {
		e.Log.Log("factory", typ, "", fields)
	} else {
		log.Printf("factory: %s %v", typ, fields)
	}
}

func (e *Engine) noteStoreENOSPC(id string, err error) error {
	if errors.Is(err, syscall.ENOSPC) {
		setFlag(&e.storeENOSPC, id, true)
	}
	return err
}

var errStoreDirty = errors.New("factory: store dirty")

func (e *Engine) cleanLocked(id string) bool {
	if clean, seen := e.logClean[id]; seen {
		return clean
	}
	_, sErr := os.Stat(e.Paths.State(id))
	_, lErr := os.Stat(e.Paths.Log(id))
	return os.IsNotExist(sErr) && os.IsNotExist(lErr) && e.markStoreLocked(id, nil) == nil
}

func (e *Engine) markStoreLocked(id string, err error) error {
	if e.logClean == nil {
		e.logClean = map[string]bool{}
	}
	if e.logClean[id] = err == nil; err != nil {
		maps.DeleteFunc(e.waits, func(k, _ string) bool { return k == id || strings.HasPrefix(k, id+"/") })
	}
	return e.noteStoreENOSPC(id, err)
}

func (e *Engine) freshLocked(id string, s flow.State) error {
	if e.logClean[id] {
		return nil
	}
	err := TrimLog(e.Paths.Log(id), s.Seq)
	if _, sErr := os.Stat(e.Paths.State(id)); err == nil && !os.IsNotExist(sErr) {
		err = map[bool]func(string, flow.State) error{true: e.writeState, false: SaveState}[e.writeState != nil](e.Paths.State(id), s)
	}
	return e.markStoreLocked(id, err)
}

func (e *Engine) saveStateLocked(id string, s flow.State) error {
	if !e.cleanLocked(id) {
		return errStoreDirty
	}
	return e.markStoreLocked(id, map[bool]func(string, flow.State) error{true: e.writeState, false: SaveState}[e.writeState != nil](e.Paths.State(id), s))
}

func (e *Engine) appendLogLocked(id string, l LogLine) error {
	if !e.cleanLocked(id) {
		return errStoreDirty
	}
	return e.markStoreLocked(id, map[bool]func(string, LogLine) error{true: e.writeLog, false: AppendLog}[e.writeLog != nil](e.Paths.Log(id), l))
}

func (e *Engine) saveLocked(id string, s flow.State) bool {
	err := e.saveStateLocked(id, s)
	if err != nil {
		e.logError("factory.commit_error", id, err)
	}
	return err == nil
}

func (e *Engine) commitLogLocked(id string, s *flow.State, l LogLine) bool {
	err := e.commitLocked(id, s, l)
	if err != nil {
		e.logError("factory.commit_error", id, err)
	}
	return err == nil
}

func (e *Engine) commitLocked(id string, s *flow.State, l LogLine) error {
	s.Seq++
	l.Seq, l.At, l.Project = s.Seq, time.Now(), id
	if err := e.appendLogLocked(id, l); err != nil {
		s.Seq--
		return err
	}
	if err := e.saveStateLocked(id, *s); err != nil {
		s.Seq--
		return err
	}
	if l.By != "engine" {
		select {
		case e.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

func (e *Engine) findProjectLocked(ctx context.Context, target string) (string, flow.State, error) {
	var lookupErr error
	if target != "" {
		root, id, _ := e.git.RepoProject(ctx, target, false)
		switch {
		case id != "":
			target = id
		case root != "":
			target = root
		case filepath.IsAbs(target):
			dirs, err := e.git.CanonicalPaths(world.GitRepository(ctx, target), "/", target)
			lookupErr = err
			target = cmp.Or(dirs[target], target)
		}
	}
	var match flow.State
	for _, ent := range e.projectDirs() {
		s, err := LoadState(e.Paths.State(ent.Name()))
		if err != nil {
			continue
		}
		if target != "" && (ent.Name() == target || underFolder(s.Project.Folder, target)) {
			return ent.Name(), s, nil
		}
		if target == "" && s.Project.Started {
			if match.Project.ID != "" {
				return "", flow.State{}, errors.New("multiple projects started; pass sheepdog factory stop <folder>")
			}
			match = s
		}
	}
	if match.Project.ID != "" {
		return match.Project.ID, match, nil
	}
	return "", flow.State{}, cmp.Or(lookupErr, errors.New("no started factory project"))
}
