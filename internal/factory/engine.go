package factory

import (
	"cmp"
	"context"
	_ "embed"
	"errors"
	"log"
	"os"
	"path/filepath"
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

// Engine runs the factory loop and handles owner start and stop commands.
type Engine struct {
	Paths        Paths
	HC           *hc.Client
	Log          logging.Logger
	PassInterval time.Duration

	mu   sync.Mutex
	wake chan struct{}
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
	ctx := context.Background()
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
	if st, err := world.RunState(ctx, e.HC, s.Project.RunID); err == nil && st == flow.RunGone {
		s.Project.RunID = ""
	}
	s.Project.Folder, s.Project.Goal, s.Project.Started = root, goal, true
	if err := e.commitLocked(id, &s, LogLine{By: "owner", Input: map[string]string{"cmd": "start", "folder": root, "goal": goal}, Rule: "start", Decision: "started", Why: goal}); err != nil {
		return err
	}
	select {
	case e.wake <- struct{}{}:
	default:
	}
	return nil
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
	if err := world.StopRun(ctx, e.HC, cmp.Or(s.Project.RunID, coordName(id))); err != nil {
		return err
	}
	s.Project.RunID, s.Project.Started = "", false
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
	if !s.Project.Started || s.Project.RunID != "" {
		return
	}
	prompt := strings.TrimSpace(strings.NewReplacer("{folder}", s.Project.Folder, "{goal}", s.Project.Goal).Replace(coordinatorTemplate))
	termID, err := world.StartRun(ctx, e.HC, world.RunSpec{
		Name:         coordName(id),
		Cwd:          s.Project.Folder,
		ClaudeConfig: e.Paths.ClaudeConfig,
		Argv:         world.ClaudeArgv(flow.RoleCoordinator, "opus", "medium", "bypassPermissions"),
		Prompt:       prompt,
		Labels:       map[string]string{"sheepdog.project": id, "sheepdog.role": flow.RoleCoordinator, "sheepdog.model": "opus"},
	})
	line := LogLine{By: "engine", Facts: map[string]any{"coordinator": flow.RunGone}, Rule: "J7", Asked: []string{"coordinator"}}
	if err != nil {
		line.Why = err.Error()
	} else {
		s.Project.RunID = termID
		line.Asked, line.Decision, line.Why = []string{"coordinator:" + termID}, "started coordinator", "project started with no coordinator run"
	}
	if err := e.commitLocked(id, &s, line); err != nil {
		e.logError("factory.commit_error", id, err)
	}
	// Seam for PR 1.4+: read worktree tips and PR snapshots, evaluate each task
	// in s.Tasks with flow.FirstMatch, execute actions, and commit.
}

func coordName(id string) string { return "coord-" + strings.TrimPrefix(id, "proj-") }

func (e *Engine) logError(typ, id string, err error) {
	if e.Log != nil {
		e.Log.Log("factory", typ, "", map[string]string{"project": id, "error": err.Error()})
	} else {
		log.Printf("factory: %s (%s): %v", typ, id, err)
	}
}

func (e *Engine) commitLocked(id string, s *flow.State, l LogLine) error {
	s.Seq++
	l.Seq, l.At, l.Project = s.Seq, time.Now(), id
	if err := AppendLog(e.Paths.Log(id), l); err != nil {
		return err
	}
	return SaveState(e.Paths.State(id), *s)
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
