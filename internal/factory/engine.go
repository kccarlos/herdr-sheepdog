package factory

import (
	"cmp"
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

//go:embed coordinator.md
var coordinatorTemplate string

//go:embed worker.md
var workerTemplate string

//go:embed reviewer.md
var reviewerTemplate string

var lockGitTimeout = 15 * time.Second

// ghTimeout bounds one GitHub executor or snapshot (a push plus gh calls).
var ghTimeout = time.Minute

// Engine runs the factory loop and handles owner start and stop commands.
type Engine struct {
	Paths        Paths
	HC           *hc.Client
	Log          logging.Logger
	PassInterval time.Duration
	Now          func() time.Time  // clock; nil uses time.Now
	Bin          string            // sheepdog binary named in run prompts
	RunEnv       map[string]string // added to every run's environment (daemon port)

	mu        sync.Mutex
	wake      chan struct{}
	prs       map[string]flow.PR  // last PR snapshot per attempt worktree (in memory)
	errs      map[string]string   // last gh or git error per worktree and kind, logged once per change
	tips      map[string]tipFacts // last git reading per worktree (in memory)
	waits     map[string]string   // last N9/N10/N38 wait reason per project/task, logged once per change
	ghUnavail map[string]bool     // project fact github_unavailable (design.md 7.3)
	hcUnavail map[string]bool     // project fact hc_unavailable (design.md 7.3)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// tipFacts are N12's and N20's git facts for one tip and H: the merge base,
// and so the line count, moves only with the tip.
type tipFacts struct {
	tip, h    string
	lines     int
	mergeOnly bool
}

// New constructs an Engine. Nothing runs or touches disk until Run or Start.
func New(paths Paths, client *hc.Client) *Engine {
	return &Engine{Paths: paths, HC: client, wake: make(chan struct{}, 1)}
}

// Start records the project (storing its id in local git config), marks it
// started, and wakes the loop. A non-zero maxRunning sets Settings.MaxRunning.
func (e *Engine) Start(folder, goal string, maxRunning int) error {
	folder, goal = strings.TrimSpace(folder), strings.TrimSpace(goal)
	if folder == "" || goal == "" {
		return errors.New("folder and goal are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
	defer cancel()
	root, id, err := world.RepoProject(ctx, folder, true)
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
	if maxRunning != 0 {
		s.Settings.MaxRunning = maxRunning
	}
	if err := s.Settings.Validate(); err != nil {
		return err
	}
	if s.Project.RunID != "" {
		if st, err := e.advanceRunLocked(ctx, id, "", &s, s.Project.RunID, flow.RoleCoordinator, "claude", &s.Project.Run); err == nil && st.State == flow.RunGone {
			s.Project.RunID, s.Project.RunTokenHash, s.Project.Run = "", "", nil
		}
	}
	s.Project.Folder, s.Project.Goal, s.Project.Started, s.Project.Acts, s.Project.Base = root, goal, true, nil, nil
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
		stopRec(&rev.Run, flow.RoleReviewer, st, now)
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
		return a.Result == "failed" || a.Key == flow.AskWake+"@worker_failed" || strings.HasPrefix(a.Key, flow.AskWake+"@reviewer_failed@") || strings.HasPrefix(a.Key, flow.AskWake+"@stuck@") || strings.HasPrefix(a.Key, flow.AskWake+"@failed@")
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
	runs := []string{cmp.Or(s.Project.RunID, coordName(id))}
	for ti := range s.Tasks {
		runs = append(runs, taskRuns(id, s.Tasks[ti])...)
	}
	if err := e.stopRuns(ctx, runs); err != nil {
		return err
	}
	now := e.now()
	for ti := range s.Tasks {
		stopTaskRuns(&s.Tasks[ti], s.Settings, now)
	}
	s.Project.RunID, s.Project.RunTokenHash, s.Project.Run, s.Project.Started, s.Project.Acts, s.Project.Base = "", "", nil, false, nil, nil
	return e.commitLocked(id, &s, LogLine{By: "owner", Input: map[string]string{"cmd": "stop"}, Rule: "stop", Decision: "stopped", Why: "stopped by owner"})
}

// Run drives the periodic pass loop until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(cmp.Or(e.PassInterval, flow.PassInterval))
	defer t.Stop()
	for trim := true; ; trim = false {
		for _, ent := range e.projectDirs() {
			if ctx.Err() == nil && ent.IsDir() {
				e.passProject(ctx, ent.Name(), trim)
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

func (e *Engine) passProject(ctx context.Context, id string, trim bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := LoadState(e.Paths.State(id))
	if err != nil {
		return
	}
	if trim {
		if err := TrimLog(e.Paths.Log(id), s.Seq); err != nil {
			e.logError("factory.trim_error", id, err)
		}
	}
	if !s.Project.Started {
		return
	}
	if s.Project.RunID == "" && flow.Count(s.Project.Acts, "coordinator") == 0 {
		termID, _, abort, err := e.launchRunLocked(ctx, id, &s, &s.Project.RunTokenHash, &s.Project.Run, s.Project.Run == nil || s.Project.Run.State != flow.PhaseEnded, flow.RunRecord{Role: flow.RoleCoordinator, CLI: "claude"}, world.RunSpec{
			Name: coordName(id), Cwd: s.Project.Folder, ClaudeConfig: e.Paths.ClaudeConfig,
			Argv:   world.ClaudeArgv(flow.RoleCoordinator, "opus", "medium", "bypassPermissions"),
			Prompt: coordinatorPrompt(coordinatorTemplate, s.Project, cmp.Or(e.Bin, "sheepdog")),
			Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.model": "opus"},
		})
		if abort || ctx.Err() != nil {
			return
		}
		line := LogLine{By: "engine", Facts: map[string]any{"coordinator": flow.RunGone}, Rule: "J7", Asked: []string{"coordinator"}}
		if err != nil {
			e.logError("factory.exec_error", id, err)
			s.Project.Acts = append(s.Project.Acts, flow.Act{Key: "coordinator", Result: "failed", At: e.now()})
			line.Why = err.Error()
		} else {
			s.Project.RunID = termID
			line.Asked, line.Decision, line.Why = []string{"coordinator:" + termID}, "started coordinator", "project started with no coordinator run"
		}
		if cErr := e.commitLocked(id, &s, line); cErr != nil || err != nil {
			if cErr != nil {
				e.logError("factory.commit_error", id, cErr)
			}
			return
		}
	}
	hcChecked := s.Project.RunID != "" && (s.Project.Run == nil || s.Project.Run.State != flow.PhaseEnded)
	coordRun, coordErr := e.advanceRunLocked(ctx, id, "", &s, s.Project.RunID, flow.RoleCoordinator, "claude", &s.Project.Run)
	hcErr := coordErr
	if coordErr != nil {
		e.logError("factory.hc_error", id, coordErr)
	}
	e.observeBaseLocked(ctx, id, &s)
	e.sweepProjectLocked(ctx, id, &s)
	baseTried := s.Project.Base != nil && s.Project.Base.Red && flow.Done(s.Project.Acts, flow.AskTellBaseRed+"@"+s.Project.Base.SpellID)
	var candidates []string
	workerRun, workerState, workerErr := map[string]flow.Run{}, map[string]flow.RunState{}, map[string]error{}
	for ti := range s.Tasks {
		t := &s.Tasks[ti]
		if a := flow.OpenAttempt(*t); a != nil && !flow.Ended(*t) && (a.RunID != "" || a.Run != nil && a.Run.State == flow.PhaseEnded) {
			hcChecked = hcChecked || a.RunID != "" && (a.Run == nil || a.Run.State != flow.PhaseEnded)
			r, rerr := e.advanceRunLocked(ctx, id, t.ID, &s, a.RunID, flow.RoleWorker, "claude", &a.Run)
			hcErr = cmp.Or(hcErr, rerr)
			if a.Run != nil && a.Run.State == flow.PhaseEnded && (len(a.Run.Held) > 0 || a.RunID != "") {
				a.Held, a.Run.Held, a.RunID, a.RunTokenHash = appendHeld(a.Held, a.Run.Held...), nil, "", ""
				e.saveLocked(id, s)
			}
			workerRun[t.ID], workerState[t.ID], workerErr[t.ID] = r, r.State, rerr
			if r.State == flow.RunGone && flow.WantsWorkerSlot(*t, s.Settings, e.now()) {
				candidates = append(candidates, t.ID)
			}
		} else if a == nil && !flow.Ended(*t) && !t.Parked && flow.OpenRecap(*t) == nil && flow.WantsNewAttempt(*t, s.Settings, e.now()) {
			if notDone, dropped := flow.UnmetDeps(*t, s.Tasks); len(notDone) == 0 && len(dropped) == 0 {
				candidates = append(candidates, t.ID)
			}
		}
	}
	if hcErr == nil && !hcChecked && e.hcUnavail[id] && e.HC != nil {
		_, hcErr = e.HC.Hello(ctx)
		hcChecked = true
	}
	if hcChecked || hcErr != nil {
		setFlag(&e.hcUnavail, id, hcErr != nil && world.FailureClass(hcErr) != flow.ClassSettled)
	}
	granted := flow.GrantSlots(candidates, s.Tasks, flow.RunningTasks(s.Tasks, workerState), s.Settings.MaxRunning)
	for i := range s.Tasks {
		if ctx.Err() == nil {
			e.passTaskLocked(ctx, id, &s, i, granted[s.Tasks[i].ID], workerRun[s.Tasks[i].ID], workerErr[s.Tasks[i].ID], &baseTried)
		}
	}
	if ctx.Err() == nil && coordErr == nil {
		e.deliverWakesLocked(ctx, id, &s, coordRun.State)
	}
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
	evs, err := world.ObserveRun(ctx, e.HC, **recp)
	if err != nil {
		return flow.Run{}, err
	}
	before := **recp
	for _, ev := range evs {
		old := **recp
		next, row, dec := flow.StepRun(old, ev, s.Settings, now)
		**recp = next
		for _, act := range dec.Asked {
			msg := ev.Msg
			if act == flow.RunActSendHeld && len(old.Held) > 0 {
				msg = old.Held[0]
			}
			if aerr := world.ActRun(ctx, e.HC, next, act, msg); aerr != nil {
				e.logError("factory.exec_error", id, aerr)
				if act == flow.RunActSendHeld {
					(*recp).Held = old.Held
				}
			}
		}
		if (old.State != flow.PhaseEnded && next.State == flow.PhaseEnded && (!fresh || taskID != "")) || row.ID == "R3" || row.ID == "R21" {
			e.commitLogLocked(id, s, LogLine{By: "engine", Task: taskID, Rule: row.ID, Asked: dec.Asked, Decision: dec.Why, Why: fmt.Sprintf("%s run %s: %s", role, next.Address, cmp.Or(next.Reason, dec.Why))})
			before = next
		}
	}
	if !reflect.DeepEqual(before, **recp) {
		e.saveLocked(id, *s)
	}
	return (*recp).TaskRun(), nil
}

// launchRunLocked stops any leftover same-name terminal on a restart (!adopt),
// adopts a live same-name run left by a crash or persists RunRecord (with its
// SessionID) and token hash before StartTerminal, and steps R1/R2 (design.md 5, 7.2).
func (e *Engine) launchRunLocked(ctx context.Context, id string, s *flow.State, hash *string, recp **flow.RunRecord, adopt bool, rec flow.RunRecord, spec world.RunSpec) (string, bool, bool, error) {
	if !adopt && e.HC != nil {
		if err := world.StopRun(ctx, e.HC, spec.Name); err != nil {
			e.logError("factory.hc_error", id, err)
			return "", false, true, err
		}
	}
	env := map[string]string{}
	maps.Copy(env, e.RunEnv)
	maps.Copy(env, spec.Env)
	runID, minted, prevRec, prevHash := "run-"+strings.ToLower(rand.Text()[:12]), false, *recp, *hash
	spec.Labels["sheepdog.run"], spec.Env = runID, env
	now := e.now()
	rec.State, rec.StartedAt, rec.LastActivity = flow.PhaseStarting, now, now
	term, abort, err := world.StartTerminal(ctx, e.HC, spec, func() error {
		tok := rand.Text()
		*hash, *recp, env["SHEEPDOG_RUN_TOKEN"], minted = tokenHash(tok), &rec, tok, true
		if sErr := SaveState(e.Paths.State(id), *s); sErr != nil {
			*hash, *recp = prevHash, prevRec
			e.logError("factory.commit_error", id, sErr)
			return sErr
		}
		return nil
	})
	if err != nil {
		if abort && !minted {
			e.logError("factory.hc_error", id, err)
			return "", false, true, err
		}
		*hash = ""
		rec, _, _ = flow.StepRun(rec, flow.RunEvent{Kind: flow.EvLaunchFailed}, s.Settings, now)
		*recp = &rec
		return "", false, abort, err
	}
	if i := max(slices.Index(term.Argv, "--session-id"), slices.Index(term.Argv, "--resume")); !minted && i >= 0 && i+1 < len(term.Argv) {
		rec.SessionID = term.Argv[i+1]
	}
	rec, _, _ = flow.StepRun(rec, flow.RunEvent{Kind: flow.EvLaunched, Address: term.Address, RunID: term.Labels["sheepdog.run"], CreatedMS: term.CreatedMS}, s.Settings, now)
	rec.Resumable, *recp = rec.SessionID != "", &rec
	return term.Address, minted || len(term.Argv) > 0 && term.Argv[len(term.Argv)-1] == spec.Prompt, false, nil
}

type taskExec func(e *Engine, ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (decision string, abort bool, err error)

// taskExecutors dispatches each ask verb implemented so far to its executor.
// wake_coordinator is held for J12 (wake.go); others (stop_task) are
// logged as "not built yet".
var taskExecutors = map[string]taskExec{
	flow.AskNewAttempt:   (*Engine).execNewAttempt,
	flow.AskStartWorker:  (*Engine).execStartWorker,
	flow.AskPushPR:       (*Engine).execPushPR,
	flow.AskStartReview:  (*Engine).execStartReviewer,
	flow.AskTellFindings: (*Engine).execTellFindings,
	flow.AskOpenRecap:    (*Engine).execOpenRecap,
	flow.AskSizeRecap:    (*Engine).execSizeRecap,
	flow.AskCIRecap:      (*Engine).execCIRecap,
	flow.AskTellStop:     (*Engine).execTellStop,
	flow.AskTellDiverged: (*Engine).execTellDiverged,
	flow.AskTellConflict: (*Engine).execTellConflict,
	flow.AskMergeOnly:    (*Engine).execMergeOnly,
	flow.AskRerunCI:      (*Engine).execRerunCI,
	flow.AskTellCI:       (*Engine).execTellCI,
	flow.AskTellBaseRed:  (*Engine).execTellBaseRed,
	flow.AskDispatchCI:   (*Engine).execDispatchCI,
	flow.AskUpdateBranch: (*Engine).execUpdateBranch,
	flow.AskFailUpdate:   (*Engine).execFailUpdate,
	flow.AskAccept:       (*Engine).execRecordAccept,
	flow.AskMerge:        (*Engine).execMerge,
	flow.AskMarkDone:     (*Engine).execMarkDone,
	flow.AskCleanup:      (*Engine).execCleanup,
	flow.AskStopRuns:     (*Engine).execStopRuns,
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

func actOutage(verb string, f flow.Facts) bool {
	switch verb {
	case flow.AskNewAttempt, flow.AskPushPR, flow.AskRerunCI, flow.AskDispatchCI, flow.AskUpdateBranch, flow.AskMerge:
		return f.GitHubUnavailable
	case flow.AskStartWorker, flow.AskStartReview, flow.AskTellFindings, flow.AskTellStop, flow.AskTellDiverged, flow.AskTellConflict, flow.AskTellCI, flow.AskTellBaseRed, flow.AskStopRuns:
		return f.HCUnavailable
	case flow.AskCleanup:
		return f.GitHubUnavailable || f.HCUnavailable
	default:
		return false
	}
}

func (e *Engine) passTaskLocked(ctx context.Context, id string, s *flow.State, ti int, hasSlot bool, workerRun flow.Run, workerErr error, baseTried *bool) {
	facts := flow.Facts{GitHubUnavailable: e.ghUnavail[id], HCUnavailable: e.hcUnavail[id]}
	if b := s.Project.Base; b != nil && b.Red {
		facts.BaseRed, facts.BaseRedSince = true, b.Since
		if !*baseTried {
			facts.BaseRedSpell = b.SpellID
			facts.BaseRedWoken = flow.Done(s.Project.Acts, flow.AskWake+"@base_red_held@"+b.SpellID)
		}
	}
	// An ended task needs no facts (N1 matches first), and cleanup may
	// have removed its worktree.
	if oa := flow.OpenAttempt(s.Tasks[ti]); oa != nil && !flow.Ended(s.Tasks[ti]) {
		a := *oa
		gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
		tip, rel, _, err := world.InspectWorktree(gctx, a.Worktree, flow.H(a))
		if err == nil {
			facts.Lines, facts.LinesTimeout, facts.TipMergeOnly, err = e.readTipLocked(gctx, a, tip, rel)
			if err == nil && rel == flow.RelDiverged {
				readyHead := a.BaseCommit
				if n := len(a.Ready); n > 0 {
					readyHead = a.Ready[n-1].Head
				}
				if readyHead != flow.H(a) {
					rrel, _ := world.Relation(gctx, a.Worktree, readyHead, tip)
					facts.TipDivergedByMergeOnly = rrel == flow.RelAhead
				}
			}
		}
		cancel()
		if e.logOnce("factory.git_error", id, a.Worktree, err); err != nil {
			return
		}
		if workerErr != nil {
			e.logError("factory.hc_error", id, workerErr)
			return
		}
		facts.Worker = workerRun
		facts.NoSlot = workerRun.State == flow.RunGone && !hasSlot
		facts.Tip, facts.TipRel = tip, rel
		if len(a.Ready) > 0 { // no PR can exist before the first ready
			facts.PR = e.readPRLocked(ctx, id, s, ti)
		}
		facts.GitHubUnavailable = e.ghUnavail[id]
		if e.observeReviewLocked(ctx, id, s, ti, &facts) {
			return
		}
	} else if oa == nil && !flow.Ended(s.Tasks[ti]) {
		facts.DepsNotDone, facts.DepsDropped = flow.UnmetDeps(s.Tasks[ti], s.Tasks)
		facts.NoSlot = !hasSlot && !flow.Exhausted(s.Tasks[ti].Acts, flow.AskNewAttempt, s.Settings)
	}
	now := e.now()
	r, d := flow.FirstMatch(flow.TaskRows, flow.In{Task: s.Tasks[ti], Settings: s.Settings, Facts: facts, Now: now})
	rowChanged := s.Tasks[ti].LastRow != r.ID
	s.Tasks[ti].LastRow = r.ID
	waitKey := id + "/" + s.Tasks[ti].ID
	if r.ID != "N9" && r.ID != "N10" && r.ID != "N38" {
		delete(e.waits, waitKey)
	} else if waitVal := r.ID + ":" + d.Why; e.waits[waitKey] != waitVal {
		if e.waits == nil {
			e.waits = map[string]string{}
		}
		e.waits[waitKey] = waitVal
		if len(d.Asked) == 0 {
			e.commitLogLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: facts, Rule: r.ID, Decision: "waiting", Why: d.Why})
			return
		}
	}
	if len(d.Asked) == 0 {
		if rowChanged {
			e.saveLocked(id, *s)
		}
		return
	}
	var decs []string
	whys := []string{d.Why}
	act := func(k, res, cls, errText string, uncounted bool) { // an outcome replaces the intent or prior uncounted try
		acts := &s.Tasks[ti].Acts
		if oa := flow.OpenAttempt(s.Tasks[ti]); oa != nil {
			acts = &oa.Acts
		}
		entry := flow.Act{Key: k, Result: res, Class: cls, Err: flow.CleanErr(errText), Uncounted: uncounted, At: now}
		if n := len(*acts); res != "asked" && n > 0 && (*acts)[n-1].Key == k && (*acts)[n-1].Result == "asked" {
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
	for i, ask := range d.Asked {
		verb, _, _ := strings.Cut(ask, "@")
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
		if verb == flow.AskRerunCI || verb == flow.AskDispatchCI || verb == flow.AskUpdateBranch { // a GitHub call whose outcome can be lost: saved as asked first, never sent twice
			act(ask, "asked", "", "", false)
			if !e.saveLocked(id, *s) {
				return
			}
		}
		f := facts
		f.Ask = ask
		dec, abort, err := fn(e, context.WithoutCancel(ctx), id, s, ti, f)
		if abort && err != nil {
			return
		} else if abort {
			act(ask, "failed", flow.ClassTransient, dec, true)
			decs = append(decs, dec)
		} else if errors.Is(err, world.ErrNoDispatchWorkflow) {
			act(ask, "skipped", "", "", false)
			decs = append(decs, "no dispatch_workflow set, or its file is missing")
		} else if err != nil {
			e.logError("factory.exec_error", id, err)
			cls := world.FailureClass(err)
			act(ask, "failed", cls, err.Error(), cls != flow.ClassSettled && actOutage(verb, facts))
			whys = append(whys, err.Error())
		} else {
			act(ask, "done", "", "", false)
			decs = append(decs, dec)
		}
		if verb == flow.AskTellBaseRed {
			*baseTried = true
			if err == nil {
				s.Project.Acts = append(s.Project.Acts, flow.Act{Key: ask, Result: "done", At: now})
			}
		}
		if ctx.Err() != nil {
			d.Asked = d.Asked[:i+1] // the rest is asked again next pass
			break
		}
	}
	e.commitLogLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: facts, Rule: r.ID, Asked: d.Asked, Decision: strings.Join(decs, "; "), Why: strings.Join(whys, "; ")})
}

// readTipLocked reads the changed lines at tip and whether it is ahead of H
// by merges of the base only, once per tip and H. The base commit itself
// has no changed lines. A changed-lines read that times out counts as over
// size_cap (design.md 3, last_tip) and is never cached, so the next pass
// reads again.
func (e *Engine) readTipLocked(ctx context.Context, a flow.Attempt, tip string, rel flow.Rel) (int, bool, bool, error) {
	h := flow.H(a)
	if c, ok := e.tips[a.Worktree]; ok && c.tip == tip && c.h == h {
		return c.lines, false, c.mergeOnly, nil
	}
	c, err := tipFacts{tip: tip, h: h}, error(nil)
	if tip != a.BaseCommit {
		c.lines, err = world.ChangedLines(ctx, a.Worktree, tip)
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, true, false, nil
		}
	}
	if err == nil && rel == flow.RelAhead {
		c.mergeOnly, err = world.MergeOnly(ctx, a.Worktree, h, tip)
	}
	if err != nil {
		return 0, false, false, err
	}
	if e.tips == nil {
		e.tips = map[string]tipFacts{}
	}
	e.tips[a.Worktree] = c
	return c.lines, false, c.mergeOnly, nil
}

func (e *Engine) execNewAttempt(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t, n := &s.Tasks[ti], len(s.Tasks[ti].Attempts)+1
	// The branch carries the project and the attempt, so a new project on
	// the same repo never reuses a branch an older PR (even a closed one)
	// already names (seen live: a closed PR from an earlier run blocked N26).
	tag := fmt.Sprintf("%s-%s-%d", strings.TrimPrefix(id, "proj-"), t.ID, n)
	gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
	defer cancel()
	wt, err := world.NewWorktree(gctx, s.Project.Folder, e.Paths.Worktree(id, t.ID, n), tag, t.Title)
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
	model, effort := workerModel, "medium"
	if res, _ := flow.Resolve(flow.RoleWorker, *t, s.Settings, flow.UnprobedFacts); res.CLI == "claude" && res.Model != "" {
		model, effort = res.Model, res.Effort
	}
	adopt := prev == nil || prev.State != flow.PhaseEnded
	sid, resume, bin := world.NewUUID(), mode == flow.ContinueResume && prev != nil && prev.SessionID != "", cmp.Or(e.Bin, "sheepdog")
	if resume || (adopt && prev != nil && prev.SessionID != "") {
		sid = prev.SessionID
	}
	prompt := workerPrompt(workerTemplate, *t, *a, bin)
	if resume {
		prompt = cmp.Or(strings.Join(a.Held, "\n\n"), "Continue where you left off in this worktree (check git status and git log -1); when done, commit and run "+readyCmd(bin, t.Outcomes)+".")
	} else {
		if mode == flow.ContinueHandoff {
			prompt = e.handoffNote(id, *t, *a) + prompt
		}
		if len(a.Held) > 0 {
			prompt = "Messages held for you:\n" + strings.Join(a.Held, "\n\n") + "\n\n" + prompt
		}
	}
	termID, delivered, abort, err := e.launchRunLocked(ctx, id, s, &a.RunTokenHash, &a.Run, adopt, flow.RunRecord{Role: flow.RoleWorker, CLI: "claude", SessionID: sid, Resumed: resume}, world.RunSpec{
		Name: workerName(id, t.ID, len(t.Attempts)), Cwd: a.Worktree, ClaudeConfig: e.Paths.ClaudeConfig,
		Argv: world.ClaudeSessionArgv(flow.RoleWorker, model, effort, "bypassPermissions", sid, resume), Prompt: prompt,
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleWorker, "sheepdog.task": t.ID, "sheepdog.model": model},
	})
	if counted && !abort {
		a.Restarts++
	}
	if err == nil {
		if !delivered {
			a.Run.Held = appendHeld(a.Run.Held, a.Held...)
		}
		a.RunID, a.Held = termID, nil
	}
	return "started worker " + termID, abort, err
}

func (e *Engine) handoffNote(id string, t flow.Task, a flow.Attempt) string {
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
	return fmt.Sprintf("Handoff from an earlier worker run:\n- Brief: %s\n- Rounds so far: %s\n- Last findings: %s\n\n", bp, rounds, last)
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

func reviewerPrompt(tmpl string, t flow.Task, a flow.Attempt, h, kind, diff, rejections string, round int) string {
	return fill(tmpl, "{id}", t.ID, "{title}", t.Title, "{brief}", cmp.Or(strings.TrimSpace(t.Brief), "(none)"),
		"{outcomes}", bullets(t.Outcomes), "{proofs}", proofPrompt(a, h), "{rejections}", rejections, "{diff}", diff, "{kind}", kind, "{head}", h, "{round}", fmt.Sprint(round), "{code}", a.Review.Code)
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

// workerModel is the model every worker runs until Resolve (slice 5b).
const workerModel = "sonnet"

// claudeReviewers is slice 2's ordered reviewer candidate list on Claude Code.
var claudeReviewers = []string{"opus", "sonnet"} // no haiku: a second pass must be as strong as the first; Codex joins in slice 5b

// codexModel is the Codex reviewer's model (second_reviewer = "codex").
const codexModel = "gpt-6.2-sol"

// reviewerCandidates puts Codex after opus when the project opts in, so it
// reviews the second passing round (L8); otherwise slice 2's list.
func reviewerCandidates(st flow.Settings) []string {
	if st.SecondReviewer == world.CodexCLI {
		return []string{"opus", codexModel}
	}
	return claudeReviewers
}

func reviewerOf(model string) flow.Reviewer {
	if model == codexModel {
		return flow.Reviewer{CLI: world.CodexCLI, Model: model, Effort: "medium"}
	}
	return flow.Reviewer{CLI: "claude", Model: model, Effort: "medium"}
}

// nextReviewerModel picks the first candidate other than worker not yet in
// used, or the first candidate other than worker in a fresh session.
func nextReviewerModel(worker string, used, candidates []string) string {
	first := ""
	for _, c := range candidates {
		if c == worker {
			continue
		}
		if !slices.Contains(used, c) {
			return c
		}
		if first == "" {
			first = c
		}
	}
	return first
}

func passingModels(a flow.Attempt, h string) []string {
	var out []string
	for _, r := range flow.PassingCovering(a, h) {
		out = append(out, r.Reviewer.Model)
	}
	return out
}

// observeReviewLocked checks an active reviewer run: stops it if its round is
// void, or reads and accepts its verdict tag once idle (design.md 4.5, 4.8).
func (e *Engine) observeReviewLocked(ctx context.Context, id string, s *flow.State, ti int, facts *flow.Facts) bool {
	t, a := &s.Tasks[ti], &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	if a.Review == nil {
		return false
	}
	voidRound := func(stopped bool) bool {
		if err := world.StopRun(ctx, e.HC, a.Review.RunID); err != nil {
			e.logError("factory.hc_error", id, err)
			return true
		}
		if !stopped {
			a.Review.RunID, a.Review.RunTokenHash, a.Review.Run = "", "", &flow.RunRecord{Role: flow.RoleReviewer, State: flow.PhaseEnded, Result: flow.EndError, EndedAt: e.now()}
		} else {
			stopReviewRec(a.Review, s.Settings, e.now())
		}
		return !e.saveLocked(id, *s)
	}
	if flow.Ended(*t) || facts.PR.State == flow.PRMerged || facts.PR.State == flow.PRClosed ||
		flow.OpenRecap(*t) != nil || !tipKeepsRound(*a, a.Review.Head, *facts) || a.Review.Round != len(a.Rounds)+1 {
		if a.Review.RunID == "" && a.Review.Run != nil && a.Review.Run.State == flow.PhaseEnded {
			return false
		}
		return voidRound(true)
	}
	if a.Review.RunID == "" {
		if a.Review.Run != nil && a.Review.Run.State == flow.PhaseEnded {
			facts.Reviewer = a.Review.Run.TaskRun()
		}
		return false
	}
	var err error
	if facts.Reviewer, err = e.advanceRunLocked(ctx, id, t.ID, s, a.Review.RunID, flow.RoleReviewer, cmp.Or(a.Review.Reviewer.CLI, "claude"), &a.Review.Run); err != nil {
		e.logError("factory.hc_error", id, err)
		return true
	}
	if a.Review.Run != nil && a.Review.Run.State == flow.PhaseEnded && a.Review.RunID != "" {
		a.Review.RunID, a.Review.RunTokenHash = "", ""
		e.saveLocked(id, *s)
	}
	codex := a.Review.Reviewer.CLI == world.CodexCLI
	if facts.Reviewer.State == flow.RunGone || !codex && facts.Reviewer.State != flow.RunIdle {
		return false
	}
	rctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
	read := world.ReadReport
	if codex {
		read = world.ReadCodexReport
	}
	rd, err := read(rctx, e.HC, a.Review.RunID, a.Review.Code)
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
	tip, _, clean, err := world.InspectWorktree(gctx, a.Worktree, flow.H(*a))
	cancel()
	if err != nil {
		e.logError("factory.git_error", id, err)
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
	counted, retries, failed := len(parts) == 4 && parts[2] != flow.ContinueResume, 0, []string(nil)
	if a.Review != nil && a.Review.Round == round && flow.Covers(*a, a.Review.Head, h) {
		retries, failed = a.Review.Retries, slices.Clone(a.Review.FailedModels)
		if counted && a.Review.Reviewer.Model != "" {
			failed = append(failed, a.Review.Reviewer.Model)
		}
	}
	name, model := reviewerName(id, t.ID, len(t.Attempts), round), nextReviewerModel(workerModel, append(passingModels(*a, h), failed...), reviewerCandidates(s.Settings))
	adopt := a.Review == nil || a.Review.Run == nil || a.Review.Run.State != flow.PhaseEnded
	var kind, diff string
	if e.HC != nil && adopt {
		if gt, err := e.HC.Get(ctx, name); err != nil && !errors.Is(err, hc.ErrNotFound) {
			e.logError("factory.hc_error", id, err)
			return "", true, err
		} else if err == nil && gt.State == "live" {
			kind = flow.RoundFull
		}
	}
	if a.Review == nil || a.Review.Round != round || !flow.Covers(*a, a.Review.Head, h) || a.Review.Code == "" || !adopt {
		a.Review = &flow.ReviewRun{Round: round, Head: h, Code: strings.ToLower(rand.Text()[:10]), Reviewer: reviewerOf(model), Retries: retries, FailedModels: failed}
	}
	if counted {
		a.Review.Retries = retries + 1
	}
	if kind == "" {
		gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
		var err error
		kind, diff, err = reviewDiffPrompt(gctx, *a, h)
		cancel()
		if err != nil {
			a.Review.Run = &flow.RunRecord{Role: flow.RoleReviewer, State: flow.PhaseEnded, Result: flow.EndFailed, EndedAt: e.now()}
			return "", false, err
		}
	}
	a.Review.Kind = cmp.Or(a.Review.Kind, kind)
	model, kind = a.Review.Reviewer.Model, a.Review.Kind
	var env map[string]string
	if a.Review.Reviewer.CLI == world.CodexCLI {
		dir := filepath.Join(e.Paths.DataDir, "factory", "codex-home")
		home, err := world.CodexHome(dir)
		switch {
		case errors.Is(err, world.ErrCodexAuthDiverged): // this round falls back to opus
			e.logEvent("factory.codex_auth_diverged", map[string]string{"project": id, "task": t.ID, "file": filepath.Join(dir, "auth.json"),
				"fix": "Codex refreshed its sign-in inside sheepdog's Codex home. Check `codex login`; then move or remove that file to use Codex again."})
			model, a.Review.Reviewer = claudeReviewers[0], reviewerOf(claudeReviewers[0])
		case err != nil:
			a.Review.Run = &flow.RunRecord{Role: flow.RoleReviewer, State: flow.PhaseEnded, Result: flow.EndFailed, EndedAt: e.now()}
			return "", false, err
		default:
			env = map[string]string{"CODEX_HOME": home}
		}
	}
	effort := cmp.Or(a.Review.Reviewer.Effort, "medium")
	argv := world.ClaudeArgv(flow.RoleReviewer, model, effort, "dontAsk")
	if env != nil {
		argv = world.CodexArgv(model, effort, a.Worktree)
	}
	termID, _, abort, err := e.launchRunLocked(ctx, id, s, &a.Review.RunTokenHash, &a.Review.Run, adopt, flow.RunRecord{Role: flow.RoleReviewer, CLI: cmp.Or(a.Review.Reviewer.CLI, "claude")}, world.RunSpec{
		Name: name, Cwd: a.Worktree, ClaudeConfig: e.Paths.ClaudeConfig,
		Argv: argv, Prompt: reviewerPrompt(reviewerTemplate, *t, *a, h, kind, diff, e.rejectionPrompt(id, t.ID, *a, h), round), Env: env,
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleReviewer, "sheepdog.task": t.ID, "sheepdog.model": model},
	})
	if err == nil {
		a.Review.RunID = termID
	} else if abort && counted {
		a.Review.Retries = retries
	}
	return fmt.Sprintf("started reviewer %s (round %d, %s, %s)", termID, round, kind, model), abort, err
}

func reviewDiffPrompt(ctx context.Context, a flow.Attempt, head string) (kind, diff string, err error) {
	var rel flow.Rel
	if r := flow.LastReviewed(a); r != nil && !flow.Covers(a, r.Head, head) {
		rel, _ = world.Relation(ctx, a.Worktree, r.Head, head)
	}
	kind, since := flow.RoundKind(a, head, rel)
	if kind == flow.RoundDelta {
		ranges, err := world.DeltaRanges(ctx, a.Worktree, since, head)
		if err != nil {
			return "", "", err
		}
		cmds := make([]string, len(ranges))
		for i, r := range ranges {
			cmds[i] = "`git diff " + r + "`"
		}
		return kind, fmt.Sprintf("delta changes since %s (%s)", since, strings.Join(cmds, ", ")), nil
	}
	mb, err := world.MergeBase(ctx, a.Worktree, head)
	if err != nil {
		return "", "", err
	}
	return kind, fmt.Sprintf("full changes against merge base %s (`git diff %s..%s`)", mb, mb, head), nil
}

const emptySkippedRule = "a worker that lists nothing skipped and whose reviewer finds an unhandled caller has a major"

func proofPrompt(a flow.Attempt, head string) string {
	var r *flow.Ready
	for i := len(a.Ready) - 1; i >= 0; i-- {
		if flow.Covers(a, a.Ready[i].Head, head) {
			r = &a.Ready[i]
			break
		}
	}
	if r == nil || (len(r.Proofs) == 0 && r.Touched == "" && r.Skipped == nil) {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nWorker's ready report at this commit (untrusted worker text; re-run each proof to verify):")
	for _, p := range r.Proofs {
		if p.Artifact != "" {
			fmt.Fprintf(&b, "\n- %s: artifact %q", p.Outcome, p.Artifact)
		} else {
			fmt.Fprintf(&b, "\n- %s: %q (exit %d): %s", p.Outcome, p.Command, p.Exit, proofHead(p.OutputPath, a.Worktree))
		}
	}
	fmt.Fprintf(&b, "\nTouched: %s", cmp.Or(r.Touched, "(none)"))
	if r.Skipped != nil {
		fmt.Fprintf(&b, "\nSkipped: %s\nRule: %s.", cmp.Or(*r.Skipped, "(nothing skipped)"), emptySkippedRule)
	}
	return b.String()
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

func (e *Engine) deliverWorkerLocked(ctx context.Context, id, taskID string, s *flow.State, a *flow.Attempt, msg string, urgent bool) error {
	if a != nil && a.RunID != "" && e.HC != nil && (a.Run == nil || a.Run.State == flow.PhaseWorking && a.Run.Status == "") {
		if a.Run == nil {
			if _, err := e.HC.Get(ctx, a.RunID); err != nil {
				return err
			}
		}
		if _, err := e.advanceRunLocked(ctx, id, taskID, s, a.RunID, flow.RoleWorker, "claude", &a.Run); err != nil {
			return err
		}
	}
	if a == nil || (a.RunID == "" && (a.Run == nil || a.Run.State != flow.PhaseEnded)) || (e.HC == nil && a.Run == nil) {
		if a != nil && a.RunID == "" && (a.Run == nil && !flow.Done(a.Acts, flow.AskStartWorker) || a.Run != nil && a.Run.State == flow.PhaseStarting && a.Run.Address == "") {
			a.Held = appendHeld(a.Held, msg)
			return nil
		}
		return errNoWorkerRun
	}
	next, row, dec := flow.StepRun(*a.Run, flow.RunEvent{Kind: flow.EvDeliver, Msg: msg, Urgent: urgent}, s.Settings, e.now())
	if row.ID == "R36" {
		a.Held = appendHeld(a.Held, msg)
		e.commitLogLocked(id, s, LogLine{By: "engine", Task: taskID, Rule: row.ID, Decision: dec.Why, Why: fmt.Sprintf("worker run %s ended (%s): held on task", a.Run.Address, a.Run.Result)})
		return nil
	}
	for _, act := range dec.Asked {
		if err := world.ActRun(ctx, e.HC, next, act, msg); err != nil {
			return err
		}
	}
	*a.Run = next
	return nil
}

func (e *Engine) execTellFindings(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t := s.Tasks[ti]
	a := &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	r, bin := a.Rounds[len(a.Rounds)-1], cmp.Or(e.Bin, "sheepdog")
	rc := readyCmd(bin, t.Outcomes)
	if r.Kind == flow.RoundSendBack {
		msg := fmt.Sprintf("The coordinator sent the task back: %s — change what it asks, commit, and run %s again.", r.Why, rc)
		return fmt.Sprintf("told worker the send-back (round %d)", r.N), false, e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false)
	}
	msg := fmt.Sprintf("Review round %d found %d major(s) in %s — fix them, commit, and run %s again, or when every finding is wrong and you change nothing, run %s factory report ready --reject \"<why>\".",
		r.N, r.Majors, r.ReportPath, rc, bin)
	return fmt.Sprintf("told worker findings from round %d", r.N), false, e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false)
}

// execOpenRecap is N25: open a review-triggered recap once per attempt
// (allowed split, new, drop: never continue after a review trigger).
func (e *Engine) execOpenRecap(_ context.Context, _ string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	return openRecap(&s.Tasks[ti], flow.TriggerReview, []string{"split", "new", "drop"}), false, nil
}

// execSizeRecap is N12: open a size recap with the choices it allows now.
func (e *Engine) execSizeRecap(_ context.Context, _ string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	return openRecap(&s.Tasks[ti], flow.TriggerSize, flow.SizeRecapAllowed(s.Tasks[ti], s.Settings)), false, nil
}

// execCIRecap is N13: open a ci recap with the choices it allows now.
func (e *Engine) execCIRecap(_ context.Context, _ string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	return openRecap(&s.Tasks[ti], flow.TriggerCI, flow.SizeRecapAllowed(s.Tasks[ti], s.Settings)), false, nil
}

func openRecap(t *flow.Task, trigger string, allowed []string) string {
	if flow.OpenRecap(*t) == nil {
		t.Recaps = append(t.Recaps, flow.Recap{Trigger: trigger, Allowed: allowed})
	}
	return fmt.Sprintf("opened recap (trigger %s, allowed %s)", trigger, strings.Join(allowed, ", "))
}

// execTellStop is N6 for a size or ci recap: the worker is told once to stop.
func (e *Engine) execTellStop(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := s.Tasks[ti], flow.OpenAttempt(s.Tasks[ti])
	if rc := flow.OpenRecap(t); rc != nil && rc.Trigger == flow.TriggerCI {
		msg := fmt.Sprintf("Task %s hit %d failing CI commits in the last hour. Stop now and write nothing more; the coordinator decides how the task goes on.", t.ID, s.Settings.CIFailsPerHour)
		return "told worker to stop at the CI failure limit", false, e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, true)
	}
	msg := fmt.Sprintf("Task %s is over its size cap: %d changed lines, cap %d. Stop now and write nothing more; the coordinator decides how the task goes on.", t.ID, f.Lines, a.SizeCap)
	if f.LinesTimeout {
		msg = fmt.Sprintf("Task %s timed out reading changed lines (counts as over cap %d). Stop now and write nothing more; the coordinator decides how the task goes on.", t.ID, a.SizeCap)
	}
	return "told worker to stop at the size cap", false, e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, true)
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
	return "told worker: " + why, false, e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false)
}

// execTellConflict is N32: tell the worker once per H to merge the base (never rebase).
func (e *Engine) execTellConflict(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := s.Tasks[ti], flow.OpenAttempt(s.Tasks[ti])
	msg := fmt.Sprintf("%sPR #%d conflicts with the base at %s — merge the base into your branch (never rebase), resolve, commit, and run %s again.",
		pullPrefix(a, f), f.PR.Number, flow.H(*a), readyCmd(cmp.Or(e.Bin, "sheepdog"), t.Outcomes))
	return "told worker PR conflicts with the base", false, e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false)
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
	n, err := world.PushAndOpenPR(gctx, a.Worktree, a.Branch, flow.H(*a), t.Title, prBody(*t))
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
	return "re-ran failed jobs of run " + run, false, world.RerunFailedJobs(gctx, s.Project.Folder, run)
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
	return "told worker CI failed (run " + run + ")", false, e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false)
}

// execTellBaseRed is N38 past main_red_minutes: tell the oldest held task's
// worker once per red spell to merge the base into its branch and say ready.
func (e *Engine) execTellBaseRed(ctx context.Context, id string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	t, a := s.Tasks[ti], flow.OpenAttempt(s.Tasks[ti])
	msg := fmt.Sprintf("%sThe base branch has been red for %d+ minutes — merge the base into your branch (never rebase), fix if needed, commit, and run %s.",
		pullPrefix(a, f), s.Settings.MainRedMinutes, readyCmd(cmp.Or(e.Bin, "sheepdog"), t.Outcomes))
	return "told worker base is red: merge base and say ready", false, e.deliverWorkerLocked(ctx, id, t.ID, s, a, msg, false)
}

// observeBaseLocked reads the push workflow runs on the base branch's head
// commit once per pass (project level, design.md 7.3, N38, KB-MRG-47) and
// persists the red spell in s.Project.Base. Read errors keep the last known
// value and never flip the base red.
func (e *Engine) observeBaseLocked(ctx context.Context, id string, s *flow.State) {
	if s.Project.Folder == "" {
		return
	}
	gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
	res, err := world.BaseStatus(gctx, s.Project.Folder)
	cancel()
	wasRed := s.Project.Base != nil && s.Project.Base.Red
	if err == nil || world.FailureClass(err) != flow.ClassSettled {
		setFlag(&e.ghUnavail, id, err != nil)
	}
	if e.logOnce("factory.base_error", id, id, err); err != nil || !res.Known || res.Red == wasRed {
		return
	}
	now := time.Now()
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
	}
	wakeKey := kind + "@" + spell
	actKey := flow.AskWake + "@" + wakeKey
	var asked []string
	if flow.Count(s.Project.Acts, actKey) == 0 {
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
	return fmt.Sprintf("asked GitHub to update branch for PR #%d at %s", f.PR.Number, h), false, world.UpdateBranch(gctx, s.Project.Folder, f.PR.Number, h)
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
	return "dispatched workflow " + wf + " on " + a.Branch, false, world.DispatchWorkflow(gctx, a.Worktree, a.Branch, flow.H(*a), wf)
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
	p, warn, err := world.PRSnapshot(gctx, s.Project.Folder, a.Branch, a.PR)
	e.logOnce("factory.gh_partial", id, a.Worktree, warn)
	setFlag(&e.ghUnavail, id, err != nil)
	if e.logOnce("factory.gh_error", id, a.Worktree, err); err != nil {
		if p.State == flow.PRNone {
			p = last
		}
		p.Answered, p.Checks = false, flow.ChecksUnreadable // never the last read's checks
	} else {
		if p.Head != "" {
			p.HeadSince = last.HeadSince
			if p.Head != last.Head || p.HeadSince.IsZero() {
				p.HeadSince = time.Now()
			}
			h := flow.H(a)
			p.HeadRel, err = world.Relation(gctx, a.Worktree, h, p.Head)
			fetched := false
			if err != nil && p.Head != h {
				if ferr := world.FetchRemote(gctx, a.Worktree); ferr == nil {
					fetched = true
					p.HeadRel, err = world.Relation(gctx, a.Worktree, h, p.Head)
				}
			}
			e.logOnce("factory.git_error", id, a.Worktree+" head_rel", err) // unknown here (e.g. not fetched): no relation, never a guess
			if err == nil && p.HeadRel == flow.RelAhead {
				mo, merr := world.MergeOnly(gctx, a.Worktree, h, p.Head)
				if merr == nil && !mo && !fetched && (p.Head != last.Head || last.HeadMergeOnlyUnknown) {
					if ferr := world.FetchRemote(gctx, a.Worktree); ferr == nil {
						mo, merr = world.MergeOnly(gctx, a.Worktree, h, p.Head)
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
		oa.CIFails = append(oa.CIFails, flow.CIFail{Head: p.Head, At: time.Now()})
		ciFailAdded = true
	}
	if (ciFailAdded || !reflect.DeepEqual(p, last)) && ctx.Err() == nil {
		if e.prs == nil {
			e.prs = map[string]flow.PR{}
		}
		e.prs[a.Worktree] = p
		e.commitLogLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: p, Rule: "pr_snapshot", Why: "the PR snapshot changed"})
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

// logOnce logs err unless it repeats the last error of typ for key; a nil
// err clears it, so the next failure is logged again.
func (e *Engine) logOnce(typ, id, key string, err error) {
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

func (e *Engine) saveLocked(id string, s flow.State) bool {
	err := SaveState(e.Paths.State(id), s)
	if err != nil {
		e.logError("factory.commit_error", id, err)
	}
	return err == nil
}

func (e *Engine) commitLogLocked(id string, s *flow.State, l LogLine) {
	if err := e.commitLocked(id, s, l); err != nil {
		e.logError("factory.commit_error", id, err)
	}
}

func (e *Engine) commitLocked(id string, s *flow.State, l LogLine) error {
	s.Seq++
	l.Seq, l.At, l.Project = s.Seq, time.Now(), id
	if err := AppendLog(e.Paths.Log(id), l); err != nil {
		return err
	}
	if err := SaveState(e.Paths.State(id), *s); err != nil {
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
	if target != "" {
		if root, id, _ := world.RepoProject(ctx, target, false); id != "" {
			target = id
		} else if root != "" {
			target = root
		}
	}
	var match flow.State
	for _, ent := range e.projectDirs() {
		s, err := LoadState(e.Paths.State(ent.Name()))
		if err != nil {
			continue
		}
		if target != "" && (ent.Name() == target || s.Project.Folder == filepath.Clean(target)) {
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
	return "", flow.State{}, errors.New("no started factory project")
}
