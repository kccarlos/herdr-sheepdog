package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// The user's own agent actions from the app's agent window and Agents
// tab (docs/dev/roles.md "The user's own actions"): New agent, Rename,
// Stop and Close. A tap is the user's, like a typed prompt in the window
// (WindowPrompt), so the words rail that guards the orchestrator's writes
// does not apply and no words are spent. The rails that are not about
// who asked stay:
//
//   - a new agent's kind is one of brain.SpawnKinds and its directory
//     resolves inside supervisor.project_roots or to an open workspace's
//     own directory (resolveSpawnDir), and the runaway cap counts it;
//   - a fresh worktree's branch is a plain git branch name, made by
//     herdr in that directory's repository;
//   - only tracked agents are renamed, stopped or closed, and the pane
//     sheepdog itself runs in is never closed from here.
//
// Every action is told to the orchestrator in one line.

// ErrUserAction is a request the daemon cannot carry out as asked (a bad
// name, kind or branch): the caller's fault, a 400.
var ErrUserAction = errors.New("bad request")

// WorktreeCreator is a Herdr that can make a git worktree and open a
// workspace in it (herdr's worktree.create).
type WorktreeCreator interface {
	CreateWorktree(ctx context.Context, cwd, branch, label string) (herdr.PaneInfo, error)
}

var _ WorktreeCreator = (*herdr.Client)(nil)

// worktreeBranch is the branch names a new worktree accepts: git-ref safe
// words joined by / . _ -, never "..", a leading "-" or a trailing ".lock".
var worktreeBranch = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,79}$`)

func validBranch(b string) bool {
	return worktreeBranch.MatchString(b) && !strings.Contains(b, "..") && !strings.Contains(b, "//") &&
		!strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".lock") && !strings.HasSuffix(b, ".")
}

// Project is one directory a new agent may start in (GET /api/projects).
type Project struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Open is true when a herdr workspace already works there.
	Open bool `json:"open"`
	// Git is true when it is a git checkout, so a fresh worktree is
	// possible.
	Git bool `json:"git"`
}

// NewAgentOptions is what the app's New agent sheet offers.
type NewAgentOptions struct {
	Kinds    []string  `json:"kinds"`
	Projects []Project `json:"projects"`
	// Worktree is true when herdr can make worktrees here.
	Worktree bool `json:"worktree"`
}

// NewAgentOptions lists the kinds and directories for a new agent.
func (s *Supervisor) NewAgentOptions(ctx context.Context) (NewAgentOptions, error) {
	sp, ok := s.herdr.(Spawner)
	if !ok {
		return NewAgentOptions{}, errors.New("starting a new agent is not available here")
	}
	snap, err := sp.Snapshot(ctx)
	if err != nil {
		return NewAgentOptions{}, fmt.Errorf("could not list herdr's workspaces: %w", err)
	}
	_, wt := s.herdr.(WorktreeCreator)
	out := NewAgentOptions{Kinds: brain.SpawnKinds, Projects: []Project{}, Worktree: wt}
	for _, d := range s.spawnDirs(snap) {
		_, gerr := os.Stat(filepath.Join(d.Path, ".git"))
		out.Projects = append(out.Projects, Project{Name: d.Name, Path: d.Path, Open: d.Pane != "", Git: gerr == nil})
	}
	return out, nil
}

// NewAgentRequest is the user's New agent.
type NewAgentRequest struct {
	Kind string
	// Dir is a project's name or full path, as for the orchestrator.
	Dir string
	// Worktree, when set, is the branch of a fresh git worktree the agent
	// starts in instead of the project folder.
	Worktree string
	// Text is the optional first prompt, sent once the agent is ready.
	Text string
}

// NewAgentResult is the started agent's pane and the line to show.
type NewAgentResult struct {
	PaneID string
	Spoken string
}

// UserNewAgent starts an agent the user asked for with a tap.
func (s *Supervisor) UserNewAgent(ctx context.Context, req NewAgentRequest) (NewAgentResult, error) {
	refuse := func(reason string, err error) (NewAgentResult, error) {
		s.log.Log(logSource, "supervisor.spawn_refused", "", map[string]any{"reason": reason, "dir": req.Dir, "kind": req.Kind, "via": "user"})
		return NewAgentResult{}, err
	}
	sp, ok := s.herdr.(Spawner)
	if !ok {
		return refuse("no herdr spawner", errors.New("starting a new agent is not available here"))
	}
	if req.Kind != brain.SpawnClaude && req.Kind != brain.SpawnCodex {
		return refuse("bad kind", fmt.Errorf("%w: kind %q: want %s", ErrUserAction, req.Kind, strings.Join(brain.SpawnKinds, " or ")))
	}
	if len(req.Text) > WindowMaxText {
		return refuse("text too long", fmt.Errorf("%w: the first prompt is at most %d bytes", ErrUserAction, WindowMaxText))
	}
	var wt WorktreeCreator
	if req.Worktree != "" {
		if !validBranch(req.Worktree) {
			return refuse("bad branch", fmt.Errorf("%w: %q is not a branch name (letters, digits and . _ / -)", ErrUserAction, req.Worktree))
		}
		if wt, ok = s.herdr.(WorktreeCreator); !ok {
			return refuse("no worktrees", errors.New("this herdr cannot make worktrees"))
		}
	}
	snap, err := sp.Snapshot(ctx)
	if err != nil {
		return refuse("snapshot failed", fmt.Errorf("could not list herdr's workspaces: %w", err))
	}
	dir, reason, err := resolveSpawnDir(req.Dir, s.spawnDirs(snap), s.cfg.ProjectRoots)
	if err != nil {
		return refuse(reason, err)
	}
	s.mu.Lock()
	reason = s.spawnRateLocked()
	if reason == "" {
		s.spawns = append(s.spawns, s.cfg.Now())
	}
	s.mu.Unlock()
	if reason != "" {
		return refuse("rate", fmt.Errorf("%w: %s", brain.ErrNotPermitted, reason))
	}
	var pane herdr.PaneInfo
	switch {
	case wt != nil:
		// worktree.create takes no env, so PaneEnv does not reach this
		// shell; waitShell is the guard against a slow rc file.
		pane, err = wt.CreateWorktree(ctx, dir.Path, req.Worktree, req.Worktree)
	case dir.Pane != "":
		pane, err = sp.SplitPane(ctx, dir.Pane, dir.Path)
	default:
		pane, err = sp.CreateWorkspace(ctx, dir.Path, dir.Name)
	}
	if err != nil || pane.PaneID == "" {
		if err == nil {
			err = errors.New("herdr returned no pane")
		}
		s.log.Log(logSource, "supervisor.spawn_error", "", map[string]any{"dir": dir.Path, "kind": req.Kind, "stage": "pane", "worktree": req.Worktree != "", "via": "user", "error": err.Error()})
		if req.Worktree != "" {
			return NewAgentResult{}, fmt.Errorf("could not make a worktree %s in %s: %w", req.Worktree, dir.Name, err)
		}
		return NewAgentResult{}, fmt.Errorf("could not open a pane in %s: %w", dir.Name, err)
	}
	s.waitShell(ctx, sp, pane.PaneID)
	if err := sp.SendInput(ctx, pane.PaneID, spawnCommand(req.Kind, ""), "enter"); err != nil {
		s.log.Log(logSource, "supervisor.spawn_error", pane.PaneID, map[string]any{"dir": dir.Path, "kind": req.Kind, "stage": "start", "via": "user", "error": err.Error()})
		return NewAgentResult{}, fmt.Errorf("could not start %s in %s: %w", state.KindWord(req.Kind), dir.Name, err)
	}
	s.log.Log(logSource, "supervisor.spawn", pane.PaneID, map[string]any{"dir": dir.Path, "kind": req.Kind, "pane": pane.PaneID, "split": dir.Pane != "" && wt == nil, "worktree": req.Worktree != "", "first_instruction": req.Text != "", "via": "user"})
	if s.track() {
		go func() {
			defer s.replies.Done()
			s.followSpawn(pane.PaneID, req.Kind, req.Text, dir.Name)
		}()
	}
	where := dir.Name
	if req.Worktree != "" {
		where = "a new worktree " + req.Worktree + " of " + dir.Name
	}
	s.tellBrain(fmt.Sprintf("the user started a new %s in %s from the phone", state.KindWord(req.Kind), where))
	return NewAgentResult{PaneID: pane.PaneID, Spoken: "Starting a new " + state.KindWord(req.Kind) + " in " + where + "."}, nil
}

// UserRename renames a tracked agent as the user typed it.
func (s *Supervisor) UserRename(ctx context.Context, paneID, name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || len(name) > 60 {
		return "", fmt.Errorf("%w: a name is 1 to 60 characters", ErrUserAction)
	}
	if _, ok := s.store.Get(paneID); !ok {
		return "", ErrNoSuchAgent
	}
	echo, err := s.rename(ctx, paneID, name)
	if err == nil {
		s.tellBrain(fmt.Sprintf("the user renamed an agent from the phone: %s", echo))
	}
	return echo, err
}

// UserInterrupt stops a tracked agent's current turn, as the
// orchestrator's interrupt does (Ctrl-C, checked, a second one if the
// turn still runs).
func (s *Supervisor) UserInterrupt(ctx context.Context, paneID string) (string, error) {
	row, ok := s.store.Get(paneID)
	if !ok {
		return "", ErrNoSuchAgent
	}
	echo, err := brainHost{s}.Interrupt(ctx, paneID)
	if err == nil {
		s.tellBrain(fmt.Sprintf("the user stopped %s's turn from the phone: %s", aliasOr(state.Say(row)), echo))
	}
	return echo, err
}

// UserClose closes a tracked agent's pane. The app asked the user to
// confirm with the agent's name, which is what the protected-agent rail
// asks the orchestrator for; the daemon's own pane stays refused.
func (s *Supervisor) UserClose(ctx context.Context, paneID string) (string, error) {
	row, ok := s.store.Get(paneID)
	if !ok {
		return "", ErrNoSuchAgent
	}
	name := state.Say(row)
	if s.cfg.OwnPane != "" && paneID == s.cfg.OwnPane {
		s.log.Log(logSource, "supervisor.close_refused", paneID, map[string]any{"agent": aliasOr(row.Alias), "reason": "own pane", "via": "user"})
		return "", fmt.Errorf("%w: %s runs in the pane sheepdog itself runs in; close it on your computer", brain.ErrNotPermitted, aliasOr(name))
	}
	c, ok := s.herdr.(Closer)
	if !ok {
		return "", errors.New("closing an agent is not available here")
	}
	if err := c.ClosePane(ctx, paneID); err != nil {
		s.log.Log(logSource, "supervisor.close_error", paneID, map[string]any{"agent": aliasOr(row.Alias), "via": "user", "error": err.Error()})
		if errors.Is(err, herdr.ErrPaneNotFound) {
			return "", fmt.Errorf("%s's pane is already gone", aliasOr(name))
		}
		return "", fmt.Errorf("could not close %s: %w", aliasOr(name), err)
	}
	s.mu.Lock()
	s.dropPendingFor(paneID, "closed")
	s.mu.Unlock()
	s.log.Log(logSource, "supervisor.close", paneID, map[string]any{"agent": aliasOr(row.Alias), "spoken": name, "kind": row.Kind, "cwd": row.Cwd, "state": string(row.State), "via": "user"})
	s.tellBrain(fmt.Sprintf("the user closed %s from the phone", aliasOr(name)))
	return "Closed " + aliasOr(name) + ".", nil
}
