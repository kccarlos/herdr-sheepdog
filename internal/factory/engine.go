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
	Bin          string            // sheepdog binary named in run prompts
	RunEnv       map[string]string // added to every run's environment (daemon port)

	mu        sync.Mutex
	wake      chan struct{}
	prs       map[string]flow.PR  // last PR snapshot per attempt worktree (in memory)
	errs      map[string]string   // last gh or git error per worktree and kind, logged once per change
	tips      map[string]tipFacts // last git reading per worktree (in memory)
	waits     map[string]string   // last N9/N10 wait reason per project/task, logged once per change
	ghUnavail map[string]bool     // project fact github_unavailable (design.md 7.3)
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
		if st, err := world.RunState(ctx, e.HC, s.Project.RunID); err == nil && st == flow.RunGone {
			s.Project.RunID, s.Project.RunTokenHash = "", ""
		}
	}
	s.Project.Folder, s.Project.Goal, s.Project.Started, s.Project.Acts = root, goal, true, nil
	// The owner starting again is slice 1's retry (plan.md: "the owner
	// starts again"): failed executor acts no longer block their ask.
	for ti := range s.Tasks {
		s.Tasks[ti].Acts = dropFailed(s.Tasks[ti].Acts)
		for ai := range s.Tasks[ti].Attempts {
			s.Tasks[ti].Attempts[ai].Acts = dropFailed(s.Tasks[ti].Attempts[ai].Acts)
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

// execStopRuns is N3: a parked task keeps no run alive. The start acts are
// forgotten, since those runs were stopped on purpose: after unpark the rows
// start them again.
func (e *Engine) execStopRuns(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t := &s.Tasks[ti]
	if err := e.stopRuns(ctx, taskRuns(id, *t)); err != nil {
		return "", false, err
	}
	clearRuns(t)
	if a := flow.OpenAttempt(*t); a != nil {
		forgetStarts(a, true)
	}
	return "stopped the task's runs", false, nil
}

// forgetStarts drops the start acts of runs stopped on purpose (the
// reviewer's, and the worker's and what it was told when worker is set), so
// the rows start and tell them again.
func forgetStarts(a *flow.Attempt, worker bool) {
	a.Acts = slices.DeleteFunc(a.Acts, func(x flow.Act) bool {
		return worker && (x.Key == flow.AskStartWorker || strings.HasPrefix(x.Key, flow.AskTellFindings+"@")) ||
			strings.HasPrefix(x.Key, flow.AskStartReview+"@")
	})
}

func clearRuns(t *flow.Task) {
	for ai := range t.Attempts {
		a := &t.Attempts[ai]
		a.RunID, a.RunTokenHash, a.Review = "", "", nil
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
	return slices.DeleteFunc(acts, func(a flow.Act) bool { return a.Result == "failed" })
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
		clearRuns(&s.Tasks[ti])
	}
	if err := e.stopRuns(ctx, runs); err != nil {
		return err
	}
	s.Project.RunID, s.Project.RunTokenHash, s.Project.Started, s.Project.Acts = "", "", false, nil
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
		prompt := coordinatorPrompt(coordinatorTemplate, s.Project, cmp.Or(e.Bin, "sheepdog"))
		termID, abort, err := e.launchRunLocked(ctx, id, &s, &s.Project.RunTokenHash, world.RunSpec{
			Name: coordName(id), Cwd: s.Project.Folder, ClaudeConfig: e.Paths.ClaudeConfig,
			Argv:   world.ClaudeArgv(flow.RoleCoordinator, "opus", "medium", "bypassPermissions"),
			Prompt: prompt,
			Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.model": "opus"},
		})
		if abort || ctx.Err() != nil {
			return
		}
		line := LogLine{By: "engine", Facts: map[string]any{"coordinator": flow.RunGone}, Rule: "J7", Asked: []string{"coordinator"}}
		if err != nil {
			e.logError("factory.exec_error", id, err)
			s.Project.Acts = append(s.Project.Acts, flow.Act{Key: "coordinator", Result: "failed", At: time.Now()})
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
	var candidates []string
	workerState, workerErr := map[string]flow.RunState{}, map[string]error{}
	for _, t := range s.Tasks {
		if a := flow.OpenAttempt(t); a != nil && !flow.Ended(t) && a.RunID != "" {
			workerState[t.ID], workerErr[t.ID] = world.RunState(ctx, e.HC, a.RunID)
		} else if a == nil && !flow.Ended(t) && !t.Parked && flow.OpenRecap(t) == nil && flow.Count(t.Acts, flow.AskNewAttempt) == 0 {
			if notDone, dropped := flow.UnmetDeps(t, s.Tasks); len(notDone) == 0 && len(dropped) == 0 {
				candidates = append(candidates, t.ID)
			}
		}
	}
	granted := flow.GrantSlots(candidates, s.Tasks, flow.RunningTasks(s.Tasks, workerState), s.Settings.MaxRunning)
	for i := range s.Tasks {
		if ctx.Err() == nil {
			e.passTaskLocked(ctx, id, &s, i, granted[s.Tasks[i].ID], workerState[s.Tasks[i].ID], workerErr[s.Tasks[i].ID])
		}
	}
	if ctx.Err() == nil {
		e.deliverWakesLocked(ctx, id, &s)
	}
}

// launchRunLocked adopts a live same-name run left by a crash (keeping its
// token) or mints a new token, persists only its hash before StartRun, and
// clears the hash if StartRun fails (design.md 7.2).
func (e *Engine) launchRunLocked(ctx context.Context, id string, s *flow.State, hash *string, spec world.RunSpec) (string, bool, error) {
	rs, err := world.RunState(ctx, e.HC, spec.Name)
	if err != nil {
		e.logError("factory.hc_error", id, err)
		return "", true, err
	}
	env := map[string]string{}
	maps.Copy(env, e.RunEnv)
	maps.Copy(env, spec.Env)
	if rs == flow.RunGone {
		tok := rand.Text()
		*hash = tokenHash(tok)
		if err := SaveState(e.Paths.State(id), *s); err != nil {
			*hash = ""
			e.logError("factory.commit_error", id, err)
			return "", true, err
		}
		env["SHEEPDOG_RUN_TOKEN"] = tok
	}
	spec.Env = env
	termID, err := world.StartRun(ctx, e.HC, spec)
	if err != nil && rs == flow.RunGone {
		*hash = ""
	}
	return termID, false, err
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
	flow.AskTellStop:     (*Engine).execTellStop,
	flow.AskMergeOnly:    (*Engine).execMergeOnly,
	flow.AskAccept:       (*Engine).execRecordAccept,
	flow.AskMerge:        (*Engine).execMerge,
	flow.AskMarkDone:     (*Engine).execMarkDone,
	flow.AskCleanup:      (*Engine).execCleanup,
	flow.AskStopRuns:     (*Engine).execStopRuns,
}

func (e *Engine) passTaskLocked(ctx context.Context, id string, s *flow.State, ti int, hasSlot bool, workerSt flow.RunState, workerErr error) {
	var facts flow.Facts
	// An ended task needs no facts (N1 matches first), and cleanup may
	// have removed its worktree.
	if oa := flow.OpenAttempt(s.Tasks[ti]); oa != nil && !flow.Ended(s.Tasks[ti]) {
		a := *oa
		gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
		tip, rel, _, err := world.InspectWorktree(gctx, a.Worktree, flow.H(a))
		if err == nil {
			facts.Lines, facts.LinesTimeout, facts.TipMergeOnly, err = e.readTipLocked(gctx, a, tip, rel)
		}
		cancel()
		if e.logOnce("factory.git_error", id, a.Worktree, err); err != nil {
			return
		}
		if workerErr != nil {
			e.logError("factory.hc_error", id, workerErr)
			return
		}
		facts.Worker.State = workerSt
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
		facts.NoSlot = !hasSlot && flow.Count(s.Tasks[ti].Acts, flow.AskNewAttempt) == 0
	}
	now := time.Now()
	r, d := flow.FirstMatch(flow.TaskRows, flow.In{Task: s.Tasks[ti], Settings: s.Settings, Facts: facts, Now: now})
	waitKey := id + "/" + s.Tasks[ti].ID
	if r.ID != "N9" && r.ID != "N10" {
		delete(e.waits, waitKey)
	} else if waitVal := r.ID + ":" + d.Why; e.waits[waitKey] != waitVal {
		if e.waits == nil {
			e.waits = map[string]string{}
		}
		e.waits[waitKey] = waitVal
		if len(d.Asked) == 0 {
			if err := e.commitLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: facts, Rule: r.ID, Decision: "waiting", Why: d.Why}); err != nil {
				e.logError("factory.commit_error", id, err)
			}
			return
		}
	}
	if len(d.Asked) == 0 {
		return
	}
	var decs []string
	whys := []string{d.Why}
	act := func(k, res string) {
		a := flow.Act{Key: k, Result: res, At: now}
		if oa := flow.OpenAttempt(s.Tasks[ti]); oa != nil {
			oa.Acts = append(oa.Acts, a)
		} else {
			s.Tasks[ti].Acts = append(s.Tasks[ti].Acts, a)
		}
	}
	for i, ask := range d.Asked {
		verb, _, _ := strings.Cut(ask, "@")
		if verb == flow.AskWake {
			act(ask, "done")
			decs = append(decs, holdWake(s, ti, r.ID, d.Why, ask, facts, now))
			continue
		}
		fn, built := taskExecutors[verb]
		if !built {
			e.logEvent("factory.not_built_yet", map[string]string{"project": id, "task": s.Tasks[ti].ID, "rule": r.ID, "ask": ask})
			act(ask, "not_built_yet")
			decs, whys = append(decs, "not built yet"), append(whys, ask+": not built yet")
			continue
		}
		// An executor runs to its end even when the pass is cancelled (its
		// calls carry their own timeouts), so its outcome is recorded and
		// the next pass does not ask again; the rest waits for that pass.
		dec, abort, err := fn(e, context.WithoutCancel(ctx), id, s, ti, facts)
		if abort {
			return
		} else if err != nil {
			e.logError("factory.exec_error", id, err)
			act(ask, "failed")
			whys = append(whys, err.Error())
		} else {
			act(ask, "done")
			decs = append(decs, dec)
		}
		if ctx.Err() != nil {
			d.Asked = d.Asked[:i+1] // the rest is asked again next pass
			break
		}
	}
	if err := e.commitLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: facts, Rule: r.ID, Asked: d.Asked, Decision: strings.Join(decs, "; "), Why: strings.Join(whys, "; ")}); err != nil {
		e.logError("factory.commit_error", id, err)
	}
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
		t.Attempts = append(t.Attempts, flow.Attempt{BaseCommit: wt.BaseCommit, Branch: wt.Branch, Worktree: wt.Path, Ready: []flow.Ready{}, Rounds: []flow.Round{}, Accepts: []flow.Accept{}, Acts: []flow.Act{},
			SizeCap: s.Settings.SizeCapFactor * t.EstimateLines})
	}
	return "new attempt " + wt.Branch, false, err
}

func (e *Engine) execStartWorker(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t, a := &s.Tasks[ti], &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	bin := cmp.Or(e.Bin, "sheepdog")
	prompt := workerPrompt(workerTemplate, *t, *a, bin)
	termID, abort, err := e.launchRunLocked(ctx, id, s, &a.RunTokenHash, world.RunSpec{
		Name: workerName(id, t.ID, len(t.Attempts)), Cwd: a.Worktree, ClaudeConfig: e.Paths.ClaudeConfig,
		Argv: world.ClaudeArgv(flow.RoleWorker, workerModel, "medium", "bypassPermissions"), Prompt: prompt,
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleWorker, "sheepdog.task": t.ID, "sheepdog.model": workerModel},
	})
	if err == nil {
		a.RunID = termID
	}
	return "started worker " + termID, abort, err
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
	voidRound := func() bool {
		if err := world.StopRun(ctx, e.HC, a.Review.RunID); err != nil {
			e.logError("factory.hc_error", id, err)
			return true
		}
		a.Review = nil
		if err := SaveState(e.Paths.State(id), *s); err != nil {
			e.logError("factory.commit_error", id, err)
			return true
		}
		return false
	}
	if flow.Ended(*t) || facts.PR.State == flow.PRMerged || facts.PR.State == flow.PRClosed ||
		!tipKeepsRound(*a, a.Review.Head, *facts) || a.Review.Round != len(a.Rounds)+1 {
		return voidRound()
	}
	if flow.OpenRecap(*t) != nil { // N6: no reviewer runs; after the decision the round starts again
		forgetStarts(a, false)
		return voidRound()
	}
	if a.Review.RunID == "" {
		return false
	}
	var err error
	if facts.Reviewer.State, err = world.RunState(ctx, e.HC, a.Review.RunID); err != nil {
		e.logError("factory.hc_error", id, err)
		return true
	}
	// Codex sends herdr-core no status: its run ends when its tag is read,
	// and no tag for stuck_minutes is S2's (owner decision 2026-11-19).
	codex := a.Review.Reviewer.CLI == world.CodexCLI
	if facts.Reviewer.State == flow.RunGone || !codex && facts.Reviewer.State != flow.RunIdle {
		if a.Review.IdleSince != nil { // seen working again: the next idle read restarts the clock
			a.Review.IdleSince = nil
			if err := SaveState(e.Paths.State(id), *s); err != nil {
				e.logError("factory.commit_error", id, err)
				return true
			}
		}
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
		return e.markReviewIdleLocked(id, s, ti)
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
		// H did not move, so the flow would ask the spent start act again:
		// fail it, and the owner starting again retries the round.
		k := fmt.Sprintf("%s@%d@%s", flow.AskStartReview, a.Review.Round, a.Review.Head)
		for i := range a.Acts {
			if a.Acts[i].Key == k {
				a.Acts[i].Result = "failed"
			}
		}
		return voidRound()
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
				if err := SaveState(e.Paths.State(id), *s); err != nil {
					e.logError("factory.commit_error", id, err)
					return true
				}
			}
		}
		return e.markReviewIdleLocked(id, s, ti)
	} else if err != nil {
		e.logError("factory.exec_error", id, err)
	}
	return true
}

// tipKeepsRound: a round in flight at x keeps running while H is x or was
// reached from x by merge_only moves, and the tip is H or ahead of it by
// merges of the base only (N20 moves H next).
func tipKeepsRound(a flow.Attempt, x string, f flow.Facts) bool {
	return flow.Covers(a, x, flow.H(a)) && (f.TipRel == flow.RelSame || f.TipRel == flow.RelAhead && f.TipMergeOnly)
}

// markReviewIdleLocked starts the stuck row's clock (review.idle_since) the
// first time a pass reads the reviewer idle with no valid tag. A valid tag
// ends the round, and the record with it; the reviewer seen working clears it.
func (e *Engine) markReviewIdleLocked(id string, s *flow.State, ti int) bool {
	rev := s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1].Review
	if rev.IdleSince != nil {
		return false
	}
	now := time.Now()
	rev.IdleSince = &now
	line := LogLine{By: "engine", Task: s.Tasks[ti].ID, Rule: "review_idle", Why: fmt.Sprintf("round %d's reviewer is idle with no valid tag", rev.Round)}
	if err := e.commitLocked(id, s, line); err != nil {
		e.logError("factory.commit_error", id, err)
		return true
	}
	return false
}

func (e *Engine) execStartReviewer(ctx context.Context, id string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t, a := &s.Tasks[ti], &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	round, h := len(a.Rounds)+1, flow.H(*a)
	name, model := reviewerName(id, t.ID, len(t.Attempts), round), nextReviewerModel(workerModel, passingModels(*a, h), reviewerCandidates(s.Settings))
	if e.HC != nil {
		if term, err := e.HC.Get(ctx, name); err == nil && term.State == "live" {
			if a.Review == nil || a.Review.Round != round || a.Review.Head != h || a.Review.Code == "" {
				a.Review = &flow.ReviewRun{Round: round, Kind: flow.RoundFull, Head: h, Code: strings.ToLower(rand.Text()[:10]), Reviewer: reviewerOf(model)}
			}
			a.Review.RunID = term.Address
			startCodexClock(a.Review)
			return fmt.Sprintf("started reviewer %s (round %d, %s, %s)", term.Address, round, cmp.Or(a.Review.Kind, flow.RoundFull), a.Review.Reviewer.Model), false, nil
		} else if err != nil && !errors.Is(err, hc.ErrNotFound) {
			e.logError("factory.hc_error", id, err)
			return "", true, err
		}
	}
	gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
	kind, diff, err := reviewDiffPrompt(gctx, *a, h)
	cancel()
	if err != nil {
		return "", false, err
	}
	if a.Review == nil || a.Review.Round != round || a.Review.Head != h || a.Review.Code == "" {
		a.Review = &flow.ReviewRun{Round: round, Kind: kind, Head: h, Code: strings.ToLower(rand.Text()[:10]), Reviewer: reviewerOf(model)}
	}
	model, kind = a.Review.Reviewer.Model, cmp.Or(a.Review.Kind, kind)
	var env map[string]string
	if a.Review.Reviewer.CLI == world.CodexCLI {
		dir := filepath.Join(e.Paths.DataDir, "factory", "codex-home")
		home, err := world.CodexHome(dir)
		switch {
		case errors.Is(err, world.ErrCodexAuthDiverged): // this round falls back to opus
			e.logEvent("factory.codex_auth_diverged", map[string]string{"project": id, "task": t.ID, "file": filepath.Join(dir, "auth.json"),
				"fix": "Codex refreshed its sign-in inside sheepdog's Codex home. Check `codex login`; then move or remove that file to use Codex again."})
			model = claudeReviewers[0]
			a.Review.Reviewer = reviewerOf(model)
		case err != nil:
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
	prompt := reviewerPrompt(reviewerTemplate, *t, *a, h, kind, diff, e.rejectionPrompt(id, t.ID, *a, h), round)
	termID, abort, err := e.launchRunLocked(ctx, id, s, &a.Review.RunTokenHash, world.RunSpec{
		Name: name, Cwd: a.Worktree, ClaudeConfig: e.Paths.ClaudeConfig,
		Argv: argv, Prompt: prompt, Env: env,
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleReviewer, "sheepdog.task": t.ID, "sheepdog.model": model},
	})
	if err != nil {
		if a.Review.RunID == "" {
			a.Review = nil
		}
		return "", abort, err
	}
	a.Review.RunID = termID
	startCodexClock(a.Review)
	return fmt.Sprintf("started reviewer %s (round %d, %s, %s)", termID, round, kind, model), abort, nil
}

// startCodexClock: Codex reports no idle, so S2's no-tag clock for a Codex
// run starts with the run (a fresh start or one adopted after a restart).
func startCodexClock(r *flow.ReviewRun) {
	if r.Reviewer.CLI == world.CodexCLI && r.IdleSince == nil {
		now := time.Now()
		r.IdleSince = &now
	}
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

func (e *Engine) execTellFindings(ctx context.Context, _ string, s *flow.State, ti int, _ flow.Facts) (string, bool, error) {
	t := s.Tasks[ti]
	a := &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	r, bin := a.Rounds[len(a.Rounds)-1], cmp.Or(e.Bin, "sheepdog")
	rc := readyCmd(bin, t.Outcomes)
	if r.Kind == flow.RoundSendBack {
		msg := fmt.Sprintf("The coordinator sent the task back: %s — change what it asks, commit, and run %s again.", r.Why, rc)
		return fmt.Sprintf("told worker the send-back (round %d)", r.N), false, world.SendRun(ctx, e.HC, a.RunID, msg)
	}
	msg := fmt.Sprintf("Review round %d found %d major(s) in %s — fix them, commit, and run %s again, or when every finding is wrong and you change nothing, run %s factory report ready --reject \"<why>\".",
		r.N, r.Majors, r.ReportPath, rc, bin)
	return fmt.Sprintf("told worker findings from round %d", r.N), false, world.SendRun(ctx, e.HC, a.RunID, msg)
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

func openRecap(t *flow.Task, trigger string, allowed []string) string {
	if flow.OpenRecap(*t) == nil {
		t.Recaps = append(t.Recaps, flow.Recap{Trigger: trigger, Allowed: allowed})
	}
	return fmt.Sprintf("opened recap (trigger %s, allowed %s)", trigger, strings.Join(allowed, ", "))
}

// execTellStop is N6 for a size recap: the worker is told once to stop.
func (e *Engine) execTellStop(ctx context.Context, _ string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	a := flow.OpenAttempt(s.Tasks[ti])
	msg := fmt.Sprintf("Task %s is over its size cap: %d changed lines, cap %d. Stop now and write nothing more; the coordinator decides how the task goes on.", s.Tasks[ti].ID, f.Lines, a.SizeCap)
	if f.LinesTimeout {
		msg = fmt.Sprintf("Task %s timed out reading changed lines (counts as over cap %d). Stop now and write nothing more; the coordinator decides how the task goes on.", s.Tasks[ti].ID, a.SizeCap)
	}
	return "told worker to stop at the size cap", false, world.SendRun(ctx, e.HC, a.RunID, msg)
}

// execMergeOnly is N20: H moves to a tip that is ahead of it by merges of
// the base only. N26 then pushes it; no round is asked.
func (e *Engine) execMergeOnly(_ context.Context, _ string, s *flow.State, ti int, f flow.Facts) (string, bool, error) {
	a := flow.OpenAttempt(s.Tasks[ti])
	from := flow.H(*a)
	a.MergeOnly = append(a.MergeOnly, flow.Advance{From: from, To: f.Tip})
	return fmt.Sprintf("H moved from %s to merge-only tip %s", from, f.Tip), false, nil
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
	p, err := world.PRSnapshot(gctx, s.Project.Folder, a.Branch)
	if e.logOnce("factory.gh_error", id, a.Worktree, err); err != nil {
		if e.ghUnavail == nil {
			e.ghUnavail = map[string]bool{}
		}
		e.ghUnavail[id] = true
		if p.State == flow.PRNone {
			p = last
		}
		p.Answered, p.Checks = false, cmp.Or(p.Checks, flow.ChecksUnreadable)
	} else {
		delete(e.ghUnavail, id)
		if p.Head != "" {
			p.HeadSince = last.HeadSince
			if p.Head != last.Head || p.HeadSince.IsZero() {
				p.HeadSince = time.Now()
			}
			p.HeadRel, err = world.Relation(gctx, a.Worktree, flow.H(a), p.Head)
			e.logOnce("factory.git_error", id, a.Worktree+" head_rel", err) // unknown here (e.g. not fetched): no relation, never a guess
		}
	}
	if p != last && ctx.Err() == nil {
		if e.prs == nil {
			e.prs = map[string]flow.PR{}
		}
		e.prs[a.Worktree] = p
		if err := e.commitLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: p, Rule: "pr_snapshot", Why: "the PR snapshot changed"}); err != nil {
			e.logError("factory.commit_error", id, err)
		}
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
