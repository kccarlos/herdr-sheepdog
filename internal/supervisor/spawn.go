package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Starting a new agent (docs/dev/roles.md "Starting an agent"), by voice
// (the orchestrator's prompt tool with new_agent_dir) or by a tap on New
// agent in the app (UserNewAgent). Both go through startAgent. The
// judgement — which project the user meant, which kind, whether to ask —
// is the orchestrator's; the daemon gives it the candidate directories
// when a name does not resolve, and holds the rails, which are about
// authorisation and runaways only:
//
//   - by voice, the user's own recent words must be behind it, not yet
//     spent on another write (spawnRailLocked, the same fresh-input
//     reading as the approval rail; one input starts one agent); a tap
//     is the user's own and needs no words;
//   - the kind is one of brain.SpawnKinds by voice, one of the installed
//     kinds herdr supports by tap (InstalledKinds);
//   - the directory must resolve, symlinks followed, inside a project
//     root (supervisor.project_roots) or be an existing workspace's
//     directory: never an arbitrary path;
//   - at most spawnMax new agents per spawnWindow, both paths counted.
//
// The agent is started with herdr's agent.start in a new pane at its
// shell prompt: herdr types the kind's own executable and reports when
// the agent is detected and ready. agent.start needs a name; the daemon
// gives a placeholder (state.LaunchNamePrefix) the store ignores, so the
// agent gets its computed spoken name, and clears it once the launch is
// done. A first-run trust question is a normal pending question. The
// first instruction, when there is one, follows once the agent sits idle
// at its prompt.

// Spawner is a Herdr that can open workspaces and panes and start an
// agent in one. A Herdr without it refuses new agents.
type Spawner interface {
	Snapshot(ctx context.Context) (herdr.Snapshot, error)
	CreateWorkspace(ctx context.Context, cwd, label string) (herdr.PaneInfo, error)
	SplitPane(ctx context.Context, paneID, cwd string) (herdr.PaneInfo, error)
	ProcessInfo(ctx context.Context, paneID string) (herdr.ProcessInfo, error)
	StartAgent(ctx context.Context, paneID, name, kind string, args []string, timeout time.Duration) error
	GetAgent(ctx context.Context, target string) (herdr.AgentInfo, error)
	AgentKinds(ctx context.Context) ([]string, error)
}

var _ Spawner = (*herdr.Client)(nil)

// The rate cap: runaway protection, not a judgement.
const (
	spawnMax    = 3
	spawnWindow = 10 * time.Minute
)

// Timings of a new agent's start. Vars so tests can shorten them.
var (
	// spawnPoll is how often the start-up watch looks.
	spawnPoll = time.Second
	// spawnShellWait bounds the wait for a new pane's shell to be the
	// pane's foreground process (and to have printed) before agent.start
	// types into it.
	spawnShellWait = 5 * time.Second
	// spawnShellSettle is the pause once the shell is ready, for a prompt
	// that is still being drawn.
	spawnShellSettle = 200 * time.Millisecond
	// spawnStartWait is herdr's agent.start timeout: how long the agent
	// may take to be detected before herdr drops the launch and the
	// start has failed. herdr sees a starting Claude or Codex within
	// seconds, even at its trust question.
	spawnStartWait = 60 * time.Second
	// spawnFirstWait bounds the whole wait, from the start, for the
	// agent to sit idle (its trust question answered) before the first
	// instruction goes; past it the instruction is dropped, logged and
	// the orchestrator told.
	spawnFirstWait = 10 * time.Minute
)

// spawnScreenLines is how many of a not-started agent's last pane lines
// the orchestrator is given.
const spawnScreenLines = 15

// spawnModel is the model names the command line accepts: letters,
// digits and . _ : - only, so nothing reaches the agent's argv but a word.
var spawnModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// lookPath finds an executable on the daemon's PATH (the PATH of the
// shell that installed the service). A var so tests can fake it.
var lookPath = exec.LookPath

// spawnDir is one directory a new agent may start in.
type spawnDir struct {
	Name string // its base name: what the user says
	Path string // absolute, symlinks resolved
	// Pane is a pane of an existing workspace in this directory ("" when
	// none): the new agent's pane is split from it.
	Pane string
}

// spawnStart is one new agent for startAgent.
type spawnStart struct {
	Kind string
	// Model is the model flag, by voice only ("" for the CLI's default).
	Model string
	// Dir is a project's name or full path. The agent's pane is split
	// from a workspace already there, unless NewSpace.
	Dir string
	// Space, instead of Dir, is the herdr workspace to start in (a pane
	// split from one of its panes, in that pane's folder).
	Space string
	// NewSpace opens a new workspace in Dir even when one works there.
	NewSpace bool
	// Worktree, when set, is the branch of a fresh git worktree the
	// agent starts in instead of the project folder.
	Worktree string
	// Text is the first instruction, sent once the agent is ready.
	Text string
	// Via is who asked: "orchestrator" or "user" (a tap).
	Via string
	// Authorise, when set, runs under s.mu just before the rate cap is
	// spent: it returns why the start is refused ("" to go on) and
	// spends whatever authorised it. The orchestrator passes the words
	// rail; a tap passes nil.
	Authorise func() string
}

// started is a new agent's pane once agent.start has been asked.
type started struct {
	PaneID string
	Dir    spawnDir
	// Where is what the user hears for the place ("bench", "a new
	// worktree fix-login of bench").
	Where string
}

// startAgent is the one start path: it resolves the directory, takes a
// slot of the rate cap (after Authorise, under the same lock), opens a
// pane (a fresh worktree, a split of the project's workspace, or a new
// workspace), waits for its shell and asks herdr to start the agent. It
// returns once herdr has the launch; waitStarted tells how it went. The
// caller has checked the kind, model, text and branch.
func (s *Supervisor) startAgent(ctx context.Context, req spawnStart) (started, error) {
	refuse := func(reason string, err error) (started, error) {
		s.log.Log(logSource, "supervisor.spawn_refused", "", map[string]any{"reason": reason, "dir": req.Dir, "kind": req.Kind, "via": req.Via})
		return started{}, err
	}
	sp, ok := s.herdr.(Spawner)
	if !ok {
		return refuse("no herdr spawner", errors.New("starting a new agent is not available here"))
	}
	var wt WorktreeCreator
	if req.Worktree != "" {
		if wt, ok = s.herdr.(WorktreeCreator); !ok {
			return refuse("no worktrees", errors.New("this herdr cannot make worktrees"))
		}
	}
	snap, err := sp.Snapshot(ctx)
	if err != nil {
		return refuse("snapshot failed", fmt.Errorf("could not list herdr's workspaces: %w", err))
	}
	var dir spawnDir
	var reason string
	if req.Space != "" {
		// An open workspace: its own folder, which the directory rail
		// allows as it is.
		if dir, err = spaceDir(snap, req.Space); err != nil {
			return refuse("no such space", err)
		}
	} else {
		dir, reason, err = resolveSpawnDir(req.Dir, s.spawnDirs(snap), s.cfg.ProjectRoots)
		if err != nil {
			return refuse(reason, err)
		}
		if req.NewSpace {
			dir.Pane = ""
		}
	}
	if req.Worktree != "" {
		g, isGit := gitInfo(dir.Path)
		if !isGit {
			return refuse("not git", &ReasonError{Reason: ReasonNotGit, Err: fmt.Errorf("%w: %s is not in a git repository, so it cannot have a fresh worktree", ErrUserAction, dir.Name)})
		}
		if branchExists(ctx, g.root, req.Worktree) {
			return refuse("branch exists", &ReasonError{Reason: ReasonBranchExists, Err: fmt.Errorf("%w: the branch %s already exists in %s; open its worktree instead", ErrUserAction, req.Worktree, g.repo)})
		}
	}
	// What authorised the start and the slot are taken before anything
	// opens, under one lock: two calls racing cannot both spend the
	// same words or the last slot.
	s.mu.Lock()
	reason = ""
	if req.Authorise != nil {
		reason = req.Authorise()
	}
	if reason == "" {
		reason = s.spawnRateLocked()
		if reason == "" {
			s.spawns = append(s.spawns, s.cfg.Now())
		}
	}
	s.mu.Unlock()
	if reason != "" {
		return refuse(reason, fmt.Errorf("%w: %s", brain.ErrNotPermitted, reason))
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
	where := dir.Name
	if req.Worktree != "" {
		where = "a new worktree " + req.Worktree + " of " + dir.Name
	}
	if err != nil || pane.PaneID == "" {
		if err == nil {
			err = errors.New("herdr returned no pane")
		}
		s.log.Log(logSource, "supervisor.spawn_error", "", map[string]any{"dir": dir.Path, "kind": req.Kind, "stage": "pane", "worktree": req.Worktree != "", "via": req.Via, "error": err.Error()})
		if req.Worktree != "" {
			return started{}, fmt.Errorf("could not make a worktree %s in %s: %w", req.Worktree, dir.Name, err)
		}
		return started{}, fmt.Errorf("could not open a pane in %s: %w", dir.Name, err)
	}
	s.waitShell(ctx, sp, pane.PaneID)
	if err := sp.StartAgent(ctx, pane.PaneID, launchName(), req.Kind, spawnArgs(req.Kind, req.Model), spawnStartWait); err != nil {
		s.log.Log(logSource, "supervisor.spawn_error", pane.PaneID, map[string]any{"dir": dir.Path, "kind": req.Kind, "stage": "start", "via": req.Via, "error": err.Error()})
		return started{}, &StartError{PaneID: pane.PaneID, Reason: ReasonStartFailed, Err: fmt.Errorf("could not start %s in %s: %w", state.KindWord(req.Kind), where, err)}
	}
	fields := map[string]any{"dir": dir.Path, "kind": req.Kind, "pane": pane.PaneID, "split": dir.Pane != "" && wt == nil, "worktree": req.Worktree != "", "first_instruction": req.Text != "", "via": req.Via}
	if req.Model != "" {
		fields["model"] = req.Model
	}
	s.log.Log(logSource, "supervisor.spawn", pane.PaneID, fields)
	return started{PaneID: pane.PaneID, Dir: dir, Where: where}, nil
}

// spaceDir is the start place in workspace id: its first pane (split
// from) and that pane's folder, named after the workspace.
func spaceDir(snap herdr.Snapshot, id string) (spawnDir, error) {
	label := snap.WorkspaceLabel(id)
	var first herdr.PaneInfo
	for _, p := range snap.Panes {
		if p.WorkspaceID == id && (first.PaneID == "" || paneLess(p.PaneID, first.PaneID)) {
			first = p
		}
	}
	if first.PaneID == "" {
		return spawnDir{}, fmt.Errorf("%w: %s", ErrNoSuchSpace, id)
	}
	path := realDir(first.Cwd)
	for _, w := range snap.Workspaces {
		if w.WorkspaceID == id && w.Worktree != nil && w.Worktree.CheckoutPath != "" {
			if p := realDir(w.Worktree.CheckoutPath); p != "" {
				path = p
			}
		}
	}
	if path == "" {
		return spawnDir{}, fmt.Errorf("the space %s has no folder", label)
	}
	if label == "" {
		label = filepath.Base(path)
	}
	return spawnDir{Name: label, Path: path, Pane: first.PaneID}, nil
}

// StartError is a start that failed once its pane was open: the pane is
// left at its shell prompt, so the user can open it and see why.
type StartError struct {
	PaneID string
	// Reason is ReasonTimeout when herdr never reported the agent
	// ready, else ReasonStartFailed.
	Reason string
	Err    error
}

func (e *StartError) Error() string { return e.Err.Error() }
func (e *StartError) Unwrap() error { return e.Err }

// The codes of a refused or failed start, for the app's own screens
// (POST /api/agents "reason").
const (
	ReasonNotInstalled = "not_installed"
	ReasonNotGit       = "not_git"
	ReasonBranchExists = "branch_exists"
	ReasonTimeout      = "timeout"
	ReasonStartFailed  = "start_failed"
)

// ReasonError is a refused start with its code.
type ReasonError struct {
	Reason string
	Err    error
}

func (e *ReasonError) Error() string { return e.Err.Error() }
func (e *ReasonError) Unwrap() error { return e.Err }

// branchExists reports whether the repository at root has a local
// branch named branch (already checked by validBranch).
func branchExists(ctx context.Context, root, branch string) bool {
	return exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil
}

// launchName is a fresh placeholder herdr name (state.LaunchNamePrefix).
func launchName() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return state.LaunchNamePrefix + hex.EncodeToString(b)
}

// spawnArgs are the agent's arguments after its executable: the model
// flag, when there is one. model matched spawnModel.
func spawnArgs(kind, model string) []string {
	switch {
	case model == "":
		return nil
	case kind == brain.SpawnCodex:
		return []string{"-m", model}
	default:
		return []string{"--model", model}
	}
}

// startOutcome is how a launch went.
type startOutcome struct {
	// Blocked is true when the agent was detected at a question (its
	// first-run trust question): started, and the question is pending.
	Blocked bool
	// Err is set when it did not start; Tail is then the pane's last
	// lines ("" when the pane is gone).
	Err  error
	Tail string
}

// waitStarted follows a launch through herdr's agent.get until the
// agent is ready (detected and no longer pending), detected at a
// question, or the launch is dropped or times out.
func (s *Supervisor) waitStarted(ctx context.Context, paneID string) startOutcome {
	sp, ok := s.herdr.(Spawner)
	if !ok {
		return startOutcome{Err: errors.New("starting a new agent is not available here")}
	}
	deadline := time.Now().Add(spawnStartWait + 2*time.Second)
	fail := func(why string) startOutcome {
		tail, gone := s.spawnScreen(ctx, paneID)
		if gone {
			why = "its pane was closed"
		}
		return startOutcome{Err: errors.New(why), Tail: tail}
	}
	for {
		info, err := sp.GetAgent(ctx, paneID)
		switch {
		case errors.Is(err, herdr.ErrAgentNotFound):
			return fail(fmt.Sprintf("herdr did not see it running within %d s", int(spawnStartWait.Seconds())))
		case err == nil && info.Status == herdr.StateBlocked:
			return startOutcome{Blocked: true}
		case err == nil && !info.LaunchPending && info.Agent != "" && info.Status != herdr.StateUnknown && info.Status != "":
			return startOutcome{}
		}
		if time.Now().After(deadline) {
			return fail(fmt.Sprintf("it was not ready after %d s", int(spawnStartWait.Seconds())))
		}
		select {
		case <-ctx.Done():
			return fail("the wait was cancelled")
		case <-time.After(spawnPoll / 4):
		}
	}
}

// Spawn implements brain.Spawner.
func (h brainHost) Spawn(ctx context.Context, req brain.SpawnRequest) (string, error) {
	return h.s.spawn(ctx, req)
}

var _ brain.Spawner = brainHost{}

// spawn is the orchestrator's start: the words rail authorises it, and
// it returns as soon as herdr has the launch; how it went reaches the
// orchestrator (and, when it failed, the user) as a daemon fact.
func (s *Supervisor) spawn(ctx context.Context, req brain.SpawnRequest) (string, error) {
	refuse := func(reason string, err error) (string, error) {
		s.log.Log(logSource, "supervisor.spawn_refused", "", map[string]any{"reason": reason, "dir": req.Dir, "kind": req.Kind, "via": "orchestrator"})
		return "", err
	}
	if _, ok := s.herdr.(Spawner); !ok {
		return refuse("no herdr spawner", errors.New("starting a new agent is not available here"))
	}
	if req.Kind != brain.SpawnClaude && req.Kind != brain.SpawnCodex {
		return refuse("bad kind", fmt.Errorf("kind %q: want %s", req.Kind, strings.Join(brain.SpawnKinds, " or ")))
	}
	if req.Model != "" && !spawnModel.MatchString(req.Model) {
		return refuse("bad model", fmt.Errorf("model %q is not a model name", req.Model))
	}
	s.mu.Lock()
	reason := s.spawnRailLocked()
	s.mu.Unlock()
	if reason != "" {
		return refuse(reason, fmt.Errorf("%w: %s", brain.ErrNotPermitted, reason))
	}
	st, err := s.startAgent(ctx, spawnStart{Kind: req.Kind, Model: req.Model, Dir: req.Dir, Text: req.Text, Via: "orchestrator",
		// The rail is read again under the lock, since the words may
		// have answered a prompt while the directory was resolved.
		Authorise: func() string {
			if reason := s.spawnRailLocked(); reason != "" {
				return reason
			}
			s.lastInputLocked().used = true
			return ""
		}})
	if err != nil {
		return "", err
	}
	if s.track() {
		go func() {
			defer s.replies.Done()
			s.followStart(st, req.Kind, req.Text, true)
		}()
	}
	echo := "Starting a new " + state.KindWord(req.Kind) + " in " + st.Dir.Name + "."
	if req.Text != "" {
		// Not "told it": the instruction is held (2026-10-03 live check,
		// the orchestrator said "and told it to run the tests" before the trust
		// question was even answered).
		echo += " It has not been told anything yet: the instruction goes to it once it is ready."
	}
	return echo, nil
}

// spawnRailLocked is the authorisation rail of a new agent: the user's
// newest input — said or typed, never a tap on a question — arrived
// within supervisor.authorise_window_s, has words, is not a "no" or
// only a filler, and has not already authorised a write (an answer to a
// prompt, a new agent, a close: each marks the words used). It does not
// read what the words ask for: that is the orchestrator's judgement. Caller
// holds s.mu.
func (s *Supervisor) spawnRailLocked() string {
	return s.wordsRailLocked("a new agent", "started an agent")
}

// wordsRailLocked is spawnRailLocked's reading for any write that needs
// words of the user's own: what names the write ("a new agent", "closing
// an agent"), spent what the words may already have done. Caller holds
// s.mu.
func (s *Supervisor) wordsRailLocked(what, spent string) string {
	in := s.lastInputLocked()
	now := s.cfg.Now()
	switch {
	case in == nil:
		return "the user has said nothing that asks for " + what
	case in.kind == InputTap:
		return "the user's last input was a tap on a question, not a request for " + what
	case in.used:
		return "the user's last words already answered a prompt or " + spent + "; " + what + " needs words of their own"
	case now.Sub(in.at) > s.cfg.AuthoriseWindow:
		return fmt.Sprintf("the user's last words were %d s ago, past supervisor.authorise_window_s (%d s)", int(now.Sub(in.at).Seconds()), int(s.cfg.AuthoriseWindow.Seconds()))
	case strings.TrimSpace(in.text) == "":
		return "the user's last words were empty"
	case fillerOnly(in.text):
		return fmt.Sprintf("the user only said %q, which asks for nothing", strings.TrimSpace(in.text))
	case negativeInput(in.text):
		return fmt.Sprintf("the user said %q, which does not ask for %s", strings.TrimSpace(in.text), what)
	}
	return ""
}

// spawnRateLocked is the runaway cap: it drops starts older than
// spawnWindow and refuses when spawnMax remain. Caller holds s.mu.
func (s *Supervisor) spawnRateLocked() string {
	now := s.cfg.Now()
	kept := s.spawns[:0]
	for _, t := range s.spawns {
		if now.Sub(t) < spawnWindow {
			kept = append(kept, t)
		}
	}
	s.spawns = kept
	if len(kept) >= spawnMax {
		return fmt.Sprintf("%d new agents were started in the last %d minutes, the most allowed; wait before starting another", len(kept), int(spawnWindow.Minutes()))
	}
	return ""
}

// spawnDirs are the directories a new agent may start in: every
// existing workspace's directory, then every immediate subdirectory of a
// project root (hidden ones skipped), each once.
func (s *Supervisor) spawnDirs(snap herdr.Snapshot) []spawnDir {
	var out []spawnDir
	seen := map[string]bool{}
	for _, p := range snap.Panes {
		path := realDir(p.Cwd)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, spawnDir{Name: filepath.Base(path), Path: path, Pane: p.PaneID})
	}
	for _, root := range s.cfg.ProjectRoots {
		r := realDir(root)
		if r == "" {
			continue
		}
		entries, err := os.ReadDir(r)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			path := realDir(filepath.Join(r, e.Name()))
			if path == "" || seen[path] || !inside(path, r) {
				continue
			}
			seen[path] = true
			out = append(out, spawnDir{Name: e.Name(), Path: path})
		}
	}
	return out
}

// resolveSpawnDir turns what the orchestrator passed into one allowed
// directory. A path (absolute or ~/…) must resolve, symlinks followed,
// to a directory inside a project root or to an existing workspace's
// directory itself. A name matches a candidate's base name (case and
// punctuation aside) when exactly one does. Otherwise the error lists
// the candidates, for the orchestrator to ask the user.
func resolveSpawnDir(want string, dirs []spawnDir, roots []string) (spawnDir, string, error) {
	want = strings.TrimSpace(want)
	if strings.HasPrefix(want, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			want = filepath.Join(home, strings.TrimPrefix(want, "~"))
		}
	}
	if filepath.IsAbs(want) {
		path := realDir(want)
		if path == "" {
			return spawnDir{}, "no such directory", fmt.Errorf("%s is not a directory; the projects are %s", want, dirNames(dirs))
		}
		for _, d := range dirs {
			if d.Path == path {
				return d, "", nil
			}
		}
		// A workspace's directory counts only as itself (matched above):
		// a shell pane sitting in the home directory must not open every
		// path below it. Only a project root allows what lies inside.
		allowed := false
		for _, r := range roots {
			if rr := realDir(r); rr != "" && inside(path, rr) {
				allowed = true
			}
		}
		if !allowed {
			return spawnDir{}, "outside project roots", fmt.Errorf("%w: %s is outside the project directories (supervisor.project_roots); the projects are %s", brain.ErrNotPermitted, want, dirNames(dirs))
		}
		return spawnDir{Name: filepath.Base(path), Path: path}, "", nil
	}
	if strings.Contains(want, "..") || strings.ContainsRune(want, filepath.Separator) {
		return spawnDir{}, "bad dir name", fmt.Errorf("give the project's name or its full path, not %q; the projects are %s", want, dirNames(dirs))
	}
	var hits []spawnDir
	for _, match := range []func(spawnDir) bool{
		func(d spawnDir) bool { return strings.EqualFold(d.Name, want) },
		func(d spawnDir) bool { return state.Slug(d.Name) != "" && state.Slug(d.Name) == state.Slug(want) },
	} {
		for _, d := range dirs {
			if match(d) {
				hits = append(hits, d)
			}
		}
		if len(hits) > 0 {
			break
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], "", nil
	case 0:
		return spawnDir{}, "unknown dir", fmt.Errorf("no project called %q; the projects are %s; ask the user which one", want, dirNames(dirs))
	default:
		var paths []string
		for _, d := range hits {
			paths = append(paths, d.Path)
		}
		return spawnDir{}, "ambiguous dir", fmt.Errorf("%q matches several directories: %s; ask the user which one", want, strings.Join(paths, ", "))
	}
}

// dirNames lists the candidates' names for an error the orchestrator reads.
func dirNames(dirs []spawnDir) string {
	if len(dirs) == 0 {
		return "none (no workspace is open and supervisor.project_roots has no subdirectories)"
	}
	names := make([]string, 0, len(dirs))
	for _, d := range dirs {
		names = append(names, d.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// realDir is path made absolute with every symlink resolved, or "" when
// it is not an existing directory.
func realDir(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return ""
	}
	return real
}

// inside reports whether path is strictly below root (both resolved).
func inside(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// waitShell waits, up to spawnShellWait, until a new pane's shell is the
// pane's foreground process (pane.process_info, the predicate herdr's own
// agent.start uses) and has printed something, then settles briefly, so
// agent.start does not type into a shell still starting (an rc-file
// child such as oh-my-zsh's update check holds the foreground and eats
// keystrokes). A pane that cannot be inspected, or a shell not ready by
// the deadline, is started in anyway; the timeout is logged.
func (s *Supervisor) waitShell(ctx context.Context, sp Spawner, paneID string) {
	deadline := time.Now().Add(spawnShellWait)
	for {
		info, err := sp.ProcessInfo(ctx, paneID)
		if err != nil {
			s.log.Log(logSource, "supervisor.spawn_shell_unchecked", paneID, map[string]any{"error": err.Error()})
			return
		}
		if info.ShellForeground() {
			read, err := s.herdr.Read(ctx, paneID, herdr.SourceRecentUnwrapped, 20)
			if err != nil || strings.TrimSpace(read.Text) != "" {
				break
			}
		}
		if time.Now().After(deadline) {
			s.log.Log(logSource, "supervisor.spawn_shell_timeout", paneID, map[string]any{"waited_ms": spawnShellWait.Milliseconds()})
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(spawnPoll / 4):
		}
	}
	select {
	case <-ctx.Done():
	case <-time.After(spawnShellSettle):
	}
}

// followStart watches a new agent after agent.start. By voice
// (waitLaunch) it first follows the launch (waitStarted): a start that
// failed is told to the orchestrator, with the pane's last lines, and
// to the user as a daemon fact. Then, on both paths, it waits for the
// watcher to put the agent in the roster and for it to sit idle at its
// prompt (its trust question answered by the user through the normal
// pending path), clears the placeholder herdr name, and sends the first
// instruction, when there is one, through the instruction send path
// (sendInstruction's menu guard included).
func (s *Supervisor) followStart(st started, kind, text string, waitLaunch bool) {
	ctx := s.life
	start := time.Now()
	paneID := st.PaneID
	if waitLaunch {
		if out := s.waitStarted(ctx, paneID); out.Err != nil {
			s.spawnFailed(st, kind, text, out)
			return
		}
	}
	first := start.Add(spawnFirstWait)
	seen, named := false, true
	renames := 0
	for {
		row, ok := s.store.Get(paneID)
		switch {
		case ok && row.State != herdr.StateUnknown && row.State != "":
			if !seen {
				seen = true
				s.log.Log(logSource, "supervisor.spawn_ready", paneID, map[string]any{"kind": kind, "state": string(row.State), "agent": aliasOr(row.Alias), "waited_s": int(time.Since(start).Seconds())})
			}
			if named && row.State != herdr.StateBlocked && renames < 5 {
				// herdr refuses while the launch is pending; the next
				// look tries again.
				renames++
				if r, can := s.herdr.(Renamer); !can || r.Rename(ctx, paneID, "") == nil {
					named = false
				}
			}
			if text == "" {
				if !named || renames >= 5 {
					return
				}
				break
			}
			if row.State == herdr.StateIdle || row.State == herdr.StateDone {
				_, err := brainHost{s}.Prompt(ctx, paneID, text)
				fields := map[string]any{"kind": kind, "text": text}
				if err != nil {
					fields["error"] = err.Error()
				}
				s.log.Log(logSource, "supervisor.spawn_instruction", paneID, fields)
				return
			}
		case seen && !ok:
			s.spawnGone(paneID, kind, text)
			return
		}
		if time.Now().After(first) {
			reason := "the agent never sat idle"
			if !seen {
				reason = "herdr never recognised the agent"
			}
			s.log.Log(logSource, "supervisor.spawn_instruction_dropped", paneID, map[string]any{"kind": kind, "text": text, "reason": reason, "waited_s": int(spawnFirstWait.Seconds())})
			if text != "" {
				s.tellBrain(fmt.Sprintf("the new %s's first instruction was not sent: %s within %d minutes. The instruction was: %q", state.KindWord(kind), reason, int(spawnFirstWait.Minutes()), text))
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(spawnPoll):
		}
	}
}

// spawnFailed tells the orchestrator and the user that a voice start
// did not start: the user asked for it and must not think it is running.
func (s *Supervisor) spawnFailed(st started, kind, text string, out startOutcome) {
	s.log.Log(logSource, "supervisor.spawn_not_ready", st.PaneID, map[string]any{"kind": kind, "error": out.Err.Error(), "first_instruction": text != ""})
	line := "the new " + state.KindWord(kind) + " in " + st.Where + " did not start: " + out.Err.Error()
	if text != "" {
		line += fmt.Sprintf("; its first instruction was not sent: %q", text)
	}
	if out.Tail != "" {
		line += ". The last lines of its pane:\n" + out.Tail
	}
	s.tellBrain(line)
	s.pushTagged("", spawnNotStartedLine(kind, st.Where, out.Err.Error(), out.Tail), voice.UrgencyIdle, map[string]any{"from": "daemon", "kind": "spawn check"})
}

// spawnScreen reads the last lines of a new agent's pane; gone is true
// when herdr says the pane no longer exists.
func (s *Supervisor) spawnScreen(ctx context.Context, paneID string) (tail string, gone bool) {
	read, err := s.herdr.Read(ctx, paneID, herdr.SourceRecentUnwrapped, spawnScreenLines)
	if err != nil {
		if errors.Is(err, herdr.ErrPaneNotFound) {
			return "", true
		}
		return "(could not read the pane: " + err.Error() + ")", false
	}
	text := strings.TrimSpace(read.Text)
	if text == "" {
		return "(the pane is empty)", false
	}
	return text, false
}

// spawnGone ends a start-up watch whose pane closed before the first
// instruction went.
func (s *Supervisor) spawnGone(paneID, kind, text string) {
	s.log.Log(logSource, "supervisor.spawn_instruction_dropped", paneID, map[string]any{"kind": kind, "text": text, "reason": "the pane closed"})
	if text != "" {
		s.tellBrain(fmt.Sprintf("the new %s's pane was closed before it was ready; its first instruction was not sent: %q", state.KindWord(kind), text))
	}
}

// tellBrain puts a fact the daemon saw into the orchestrator's transcript, for
// its next turn (no turn is started).
func (s *Supervisor) tellBrain(line string) {
	if s.brain != nil {
		s.brain.Daemon(line)
	}
}

// spawnNotStartedLine is the daemon's fact for the user about a new agent
// that did not start: what was started where, and the pane's last
// lines, for the voice to retell.
func spawnNotStartedLine(kind, where, reason, tail string) string {
	line := fmt.Sprintf("The new %s%s has not started: %s.", state.KindWord(kind), inDir(where), reason)
	if strings.TrimSpace(tail) == "" {
		return line
	}
	lines := strings.Split(strings.TrimSpace(tail), "\n")
	if len(lines) > spawnTellLines {
		lines = lines[len(lines)-spawnTellLines:]
	}
	return line + " The last lines of its pane: " + strings.Join(lines, " / ")
}

// inDir is " in <dir>", or "" without a directory name.
func inDir(dirName string) string {
	if dirName == "" {
		return ""
	}
	return " in " + dirName
}

// spawnTellLines is how many pane lines the user's fact quotes.
const spawnTellLines = 5
