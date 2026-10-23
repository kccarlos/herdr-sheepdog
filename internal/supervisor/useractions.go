package supervisor

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
//   - a new agent's kind is one herdr supports and is installed here
//     (InstalledKinds), its directory resolves inside
//     supervisor.project_roots or to an open workspace's own directory
//     (resolveSpawnDir), and the runaway cap counts it (startAgent);
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

// InstalledKinds are the agent kinds a tap may start: the kinds herdr
// supports (its agent manifests) whose executable is on the daemon's
// PATH, in herdr's order. It is information for the app and the rail
// for a tap; the orchestrator's kinds stay brain.SpawnKinds.
func (s *Supervisor) InstalledKinds(ctx context.Context) ([]string, error) {
	sp, ok := s.herdr.(Spawner)
	if !ok {
		return nil, errors.New("starting a new agent is not available here")
	}
	kinds, err := sp.AgentKinds(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not list herdr's agent kinds: %w", err)
	}
	out := []string{}
	for _, k := range kinds {
		if _, err := lookPath(k); err == nil {
			out = append(out, k)
		}
	}
	return out, nil
}

// NewAgentRequest is the user's New agent.
type NewAgentRequest struct {
	Kind string
	// Space is the open space (herdr workspace id) to start in; or, with
	// Space empty, Dir is the folder of a new space: a project's name or
	// full path, as for the orchestrator.
	Space string
	Dir   string
	// Worktree, with Dir, is the branch of a fresh git worktree the new
	// space opens in instead of the project folder.
	Worktree string
	// Text is the optional first prompt, sent once the agent is ready.
	Text string
	// Name is the optional name the user gave the agent (1 to 60
	// characters, as for a rename).
	Name string
}

// NewAgentResult is the started agent's pane and the line to show.
type NewAgentResult struct {
	PaneID string
	Spoken string
	// Blocked is true when the agent started at a question (its
	// first-run trust question), which is now pending.
	Blocked bool
}

// UserNewAgent starts an agent the user asked for with a tap and
// returns once it is ready (or started at a question). A start that
// failed after its pane opened is a *StartError with the pane.
func (s *Supervisor) UserNewAgent(ctx context.Context, req NewAgentRequest) (NewAgentResult, error) {
	refuse := func(reason string, err error) (NewAgentResult, error) {
		s.log.Log(logSource, "supervisor.spawn_refused", "", map[string]any{"reason": reason, "dir": req.Dir, "kind": req.Kind, "via": "user"})
		return NewAgentResult{}, err
	}
	if _, ok := s.herdr.(Spawner); !ok {
		return refuse("no herdr spawner", errors.New("starting a new agent is not available here"))
	}
	req.Name = strings.Join(strings.Fields(req.Name), " ")
	if len(req.Name) > 60 {
		return refuse("bad name", fmt.Errorf("%w: a name is at most 60 characters", ErrUserAction))
	}
	if len(req.Text) > WindowMaxText {
		return refuse("text too long", fmt.Errorf("%w: the first prompt is at most %d bytes", ErrUserAction, WindowMaxText))
	}
	switch {
	case (req.Space == "") == (strings.TrimSpace(req.Dir) == ""):
		return refuse("no place", fmt.Errorf("%w: give a space or a new space's folder", ErrUserAction))
	case req.Worktree != "" && req.Space != "":
		return refuse("worktree in space", fmt.Errorf("%w: a fresh worktree opens a new space; give its folder, not a space", ErrUserAction))
	case req.Worktree != "" && !validBranch(req.Worktree):
		return refuse("bad branch", fmt.Errorf("%w: %q is not a branch name (letters, digits and . _ / -)", ErrUserAction, req.Worktree))
	}
	kinds, err := s.InstalledKinds(ctx)
	if err != nil {
		return refuse("kinds failed", err)
	}
	if !slices.Contains(kinds, req.Kind) {
		return refuse("bad kind", &ReasonError{Reason: ReasonNotInstalled, Err: fmt.Errorf("%w: kind %q is not one herdr supports that is installed here (%s)", ErrUserAction, req.Kind, strings.Join(kinds, ", "))})
	}
	st, err := s.startAgent(ctx, spawnStart{Kind: req.Kind, Space: req.Space, Dir: req.Dir, NewSpace: req.Space == "", Worktree: req.Worktree, Text: req.Text, Name: req.Name, Via: "user"})
	if err != nil {
		return NewAgentResult{}, err
	}
	out := s.waitStarted(ctx, st.PaneID)
	if out.Err != nil {
		s.log.Log(logSource, "supervisor.spawn_not_ready", st.PaneID, map[string]any{"kind": req.Kind, "error": out.Err.Error(), "first_instruction": req.Text != "", "via": "user"})
		msg := state.KindWord(req.Kind) + " did not start in " + st.Where + ": " + out.Err.Error()
		if line := lastLine(out.Tail); line != "" {
			msg += "; its pane shows: " + line
		}
		return NewAgentResult{}, &StartError{PaneID: st.PaneID, Reason: ReasonTimeout, Err: errors.New(msg)}
	}
	if s.track() {
		go func() {
			defer s.replies.Done()
			s.followStart(st, req.Kind, req.Text, false)
		}()
	}
	kw := state.KindWord(req.Kind)
	s.tellBrain(fmt.Sprintf("the user started a new %s in %s from the phone", kw, st.Where))
	res := NewAgentResult{PaneID: st.PaneID, Blocked: out.Blocked, Spoken: kw + " is ready in " + st.Where + "."}
	if out.Blocked {
		res.Spoken = kw + " started in " + st.Where + " and asks a question first."
	}
	return res, nil
}

// lastLine is the last non-empty line of text.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
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
