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

	mu   sync.Mutex
	wake chan struct{}
	prs  map[string]flow.PR // last PR snapshot per attempt worktree (in memory)
}

// New constructs an Engine. Nothing runs or touches disk until Run or Start.
func New(paths Paths, client *hc.Client) *Engine {
	return &Engine{Paths: paths, HC: client, wake: make(chan struct{}, 1)}
}

// Start records the project (storing its id in local git config), marks it
// started, and wakes the loop.
func (e *Engine) Start(folder, goal string) error {
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
		for ai := range s.Tasks[ti].Attempts {
			a := &s.Tasks[ti].Attempts[ai]
			runs = append(runs, cmp.Or(a.RunID, workerName(id, s.Tasks[ti].ID, ai+1)))
			a.RunID, a.RunTokenHash = "", ""
		}
	}
	if e.HC != nil { // without herdr-core (mock mode) nothing was started
		for _, r := range runs {
			if err := world.StopRun(ctx, e.HC, r); err != nil {
				return err
			}
		}
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
		prompt := strings.TrimSpace(strings.NewReplacer("{folder}", s.Project.Folder, "{goal}", s.Project.Goal, "{sheepdog}", cmp.Or(e.Bin, "sheepdog")).Replace(coordinatorTemplate))
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
	for i := range s.Tasks {
		if ctx.Err() == nil {
			e.passTaskLocked(ctx, id, &s, i)
		}
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

type taskExec func(e *Engine, ctx context.Context, id string, s *flow.State, ti int) (decision string, abort bool, err error)

// taskExecutors dispatches each ask verb implemented so far to its executor.
// Asks for PR 1.7–1.8 are absent and logged as "not built yet".
var taskExecutors = map[string]taskExec{
	flow.AskNewAttempt:  (*Engine).execNewAttempt,
	flow.AskStartWorker: (*Engine).execStartWorker,
	flow.AskPushPR:      (*Engine).execPushPR,
}

func (e *Engine) passTaskLocked(ctx context.Context, id string, s *flow.State, ti int) {
	var facts flow.Facts
	if n := len(s.Tasks[ti].Attempts); n > 0 {
		a := s.Tasks[ti].Attempts[n-1]
		gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
		tip, rel, _, err := world.InspectWorktree(gctx, a.Worktree, flow.H(a))
		cancel()
		if err != nil {
			e.logError("factory.git_error", id, err)
			return
		}
		if a.RunID != "" {
			if facts.Worker.State, err = world.RunState(ctx, e.HC, a.RunID); err != nil {
				e.logError("factory.hc_error", id, err)
				return
			}
		}
		facts.Tip, facts.TipRel = tip, rel
		facts.PR = e.readPRLocked(ctx, id, s, ti)
	}
	now := time.Now()
	r, d := flow.FirstMatch(flow.TaskRows, flow.In{Task: s.Tasks[ti], Settings: s.Settings, Facts: facts, Now: now})
	if len(d.Asked) == 0 {
		return
	}
	var decs []string
	whys := []string{d.Why}
	act := func(k, res string) {
		a := flow.Act{Key: k, Result: res, At: now}
		if n := len(s.Tasks[ti].Attempts); n > 0 {
			s.Tasks[ti].Attempts[n-1].Acts = append(s.Tasks[ti].Attempts[n-1].Acts, a)
		} else {
			s.Tasks[ti].Acts = append(s.Tasks[ti].Acts, a)
		}
	}
	for _, ask := range d.Asked {
		verb, _, _ := strings.Cut(ask, "@")
		fn, built := taskExecutors[verb]
		if !built {
			e.logEvent("factory.not_built_yet", map[string]string{"project": id, "task": s.Tasks[ti].ID, "rule": r.ID, "ask": ask})
			act(ask, "not_built_yet")
			decs, whys = append(decs, "not built yet"), append(whys, ask+": not built yet")
			continue
		}
		dec, abort, err := fn(e, ctx, id, s, ti)
		if abort || ctx.Err() != nil {
			return
		} else if err != nil {
			e.logError("factory.exec_error", id, err)
			act(ask, "failed")
			whys = append(whys, err.Error())
		} else {
			act(ask, "done")
			decs = append(decs, dec)
		}
	}
	if err := e.commitLocked(id, s, LogLine{By: "engine", Task: s.Tasks[ti].ID, Facts: facts, Rule: r.ID, Asked: d.Asked, Decision: strings.Join(decs, "; "), Why: strings.Join(whys, "; ")}); err != nil {
		e.logError("factory.commit_error", id, err)
	}
}

func (e *Engine) execNewAttempt(ctx context.Context, id string, s *flow.State, ti int) (string, bool, error) {
	t, n := &s.Tasks[ti], len(s.Tasks[ti].Attempts)+1
	tag := t.ID
	if n > 1 {
		tag = fmt.Sprintf("%s-%d", t.ID, n)
	}
	gctx, cancel := context.WithTimeout(ctx, lockGitTimeout)
	defer cancel()
	wt, err := world.NewWorktree(gctx, s.Project.Folder, e.Paths.Worktree(id, t.ID, n), tag, t.Title)
	if err == nil {
		t.Attempts = append(t.Attempts, flow.Attempt{BaseCommit: wt.BaseCommit, Branch: wt.Branch, Worktree: wt.Path, Ready: []flow.Ready{}, Rounds: []flow.Round{}, Accepts: []flow.Accept{}, Acts: []flow.Act{}})
	}
	return "new attempt " + wt.Branch, false, err
}

func (e *Engine) execStartWorker(ctx context.Context, id string, s *flow.State, ti int) (string, bool, error) {
	t, a := &s.Tasks[ti], &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	prompt := strings.TrimSpace(strings.NewReplacer("{id}", t.ID, "{title}", t.Title, "{brief}", cmp.Or(strings.TrimSpace(t.Brief), "(none)"), "{outcomes}", bullets(t.Outcomes), "{worktree}", a.Worktree, "{branch}", a.Branch, "{sheepdog}", cmp.Or(e.Bin, "sheepdog")).Replace(workerTemplate))
	termID, abort, err := e.launchRunLocked(ctx, id, s, &a.RunTokenHash, world.RunSpec{
		Name: workerName(id, t.ID, len(t.Attempts)), Cwd: a.Worktree, ClaudeConfig: e.Paths.ClaudeConfig,
		Argv: world.ClaudeArgv(flow.RoleWorker, "sonnet", "medium", "bypassPermissions"), Prompt: prompt,
		Labels: map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleWorker, "sheepdog.task": t.ID, "sheepdog.model": "sonnet"},
	})
	if err == nil {
		a.RunID = termID
	}
	return "started worker " + termID, abort, err
}

func bullets(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return "- " + strings.Join(items, "\n- ")
}

// execPushPR is N26: push H and make sure one PR is open for the branch.
// Title and body come from task fields only (design.md 9.4).
func (e *Engine) execPushPR(ctx context.Context, _ string, s *flow.State, ti int) (string, bool, error) {
	t, a := &s.Tasks[ti], &s.Tasks[ti].Attempts[len(s.Tasks[ti].Attempts)-1]
	body := fmt.Sprintf("%s\n\n## Outcomes\n\n%s\n\n---\nOpened by the sheepdog factory.", cmp.Or(strings.TrimSpace(t.Brief), "(no brief)"), bullets(t.Outcomes))
	gctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	n, err := world.PushAndOpenPR(gctx, a.Worktree, a.Branch, flow.H(*a), t.Title, body)
	if err == nil {
		a.PR = n
	}
	return fmt.Sprintf("PR #%d open for %s", n, a.Branch), false, err
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
	if err != nil {
		e.logError("factory.gh_error", id, err)
		p, p.Answered = last, false
	} else if p.Head != "" {
		p.HeadSince = last.HeadSince
		if p.Head != last.Head || p.HeadSince.IsZero() {
			p.HeadSince = time.Now()
		}
		if p.HeadRel, err = world.Relation(gctx, a.Worktree, flow.H(a), p.Head); err != nil {
			e.logError("factory.git_error", id, err) // unknown here (e.g. not fetched): no relation, never a guess
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
